package gcp

import (
	"cloud.google.com/go/compute/apiv1/computepb"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"google.golang.org/api/iterator"
	gproto "google.golang.org/protobuf/proto"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
)

// A disabled anchor preserves the logical group independently of its rules.
// Each active firewall contains exactly one CPI rule, avoiding a cross product
// of unrelated source ranges and permissions.
type firewallOwner struct {
	Managed     string            `json:"managed"`
	Group       string            `json:"group"`
	Description string            `json:"description,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

func firewallDescription(group, description string, tags map[string]string) string {
	b, _ := json.Marshal(firewallOwner{Managed: "ocfp", Group: group, Description: description, Tags: tags}) //nolint:errchkjson // Only strings and a string map; encoding cannot fail.

	return string(b)
}
func ownedFirewall(f *computepb.Firewall, group string) bool {
	var o firewallOwner

	return json.Unmarshal([]byte(f.GetDescription()), &o) == nil && o.Managed == "ocfp" && o.Group == group
}
func (m *SecurityManager) CreateSecurityGroup(ctx context.Context, req *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	if req == nil || req.Name == "" || req.NetworkID == "" {
		return nil, errors.New("security group name and network are required")
	}
	// Validate all rules before creating the anchor.
	for _, rule := range req.Rules {
		if _, err := makeFirewall(req.Name, req.NetworkID, rule); err != nil {
			return nil, err
		}
	}

	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return nil, err
	}

	project := m.client.getConfig().GetNetworkProject()

	existing, err := m.client.getFirewallsClient().Get(ctx, &computepb.GetFirewallRequest{Project: project, Firewall: req.Name})
	if err == nil {
		if !ownedFirewall(existing, req.Name) || !existing.GetDisabled() || !reflect.DeepEqual(m.convertFirewallToSecurityGroup(existing).Tags, req.Tags) || canonicalURL(existing.GetNetwork()) != canonicalURL(FormatNetworkURL(project, req.NetworkID)) {
			return nil, fmt.Errorf("security group %s conflicts with an existing firewall", req.Name)
		}
	} else {
		if !IsNotFound(WrapGCPError(err, "GetSecurityGroup")) {
			return nil, WrapGCPError(err, "GetSecurityGroup")
		}

		anchor := &computepb.Firewall{Name: proto(req.Name), Network: proto(FormatNetworkURL(project, req.NetworkID)), Disabled: proto(true), TargetTags: []string{FormatNetworkTag(req.Name)}, Allowed: []*computepb.Allowed{{IPProtocol: proto("all")}}, SourceRanges: []string{"0.0.0.0/0"}, Description: proto(firewallDescription(req.Name, req.Description, req.Tags))}

		op, insertErr := m.client.getFirewallsClient().Insert(ctx, &computepb.InsertFirewallRequest{Project: project, FirewallResource: anchor})
		if insertErr != nil {
			return nil, WrapGCPError(insertErr, "CreateSecurityGroup")
		}

		if err = op.Wait(ctx); err != nil {
			return nil, WrapGCPError(err, "CreateSecurityGroup.Wait")
		}
	}

	for _, rule := range req.Rules {
		if err := m.AddSecurityRule(ctx, req.Name, rule); err != nil {
			return nil, err
		}
	}

	return m.GetSecurityGroup(ctx, req.Name)
}
func (m *SecurityManager) GetSecurityGroup(ctx context.Context, id string) (*cpi.SecurityGroup, error) {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return nil, err
	}

	f, err := m.client.getFirewallsClient().Get(ctx, &computepb.GetFirewallRequest{Project: m.client.getConfig().GetNetworkProject(), Firewall: ExtractNameFromURL(id)})
	if err != nil {
		return nil, WrapGCPError(err, "GetSecurityGroup")
	}

	group := m.convertFirewallToSecurityGroup(f)
	if ownedFirewall(f, f.GetName()) {
		group.Rules, err = m.ListSecurityRules(ctx, f.GetName())
	}

	return group, err
}
func (m *SecurityManager) ListSecurityGroups(ctx context.Context, filters map[string]string) ([]*cpi.SecurityGroup, error) {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return nil, err
	}

	it := m.client.getFirewallsClient().List(ctx, &computepb.ListFirewallsRequest{Project: m.client.getConfig().GetNetworkProject()})
	groups := []*cpi.SecurityGroup{}

	for {
		f, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "ListSecurityGroups")
		}

		if !ownedFirewall(f, f.GetName()) {
			continue
		}

		g := m.convertFirewallToSecurityGroup(f)
		match := true

		for k, v := range filters {
			switch k {
			case "network_id":
				match = match && g.NetworkID == ExtractNameFromURL(v)
			case "name":
				match = match && g.Name == v
			default:
				match = match && logicalTag(g.Tags, k) == v
			}
		}

		if match {
			g.Rules, err = m.ListSecurityRules(ctx, g.ID)
			if err != nil {
				return nil, err
			}

			groups = append(groups, g)
		}
	}

	return groups, nil
}
func (m *SecurityManager) DeleteSecurityGroup(ctx context.Context, id string) error {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return err
	}

	project := m.client.getConfig().GetNetworkProject()

	anchor, err := m.client.getFirewallsClient().Get(ctx, &computepb.GetFirewallRequest{Project: project, Firewall: ExtractNameFromURL(id)})
	if err != nil {
		wrapped := WrapGCPError(err, "DeleteSecurityGroup.Get")
		if IsNotFound(wrapped) {
			return nil
		}

		return wrapped
	}

	group := anchor.GetName()
	if !ownedFirewall(anchor, group) {
		return fmt.Errorf("refusing to delete unmanaged security group %s", group)
	}
	// Enumerate provider objects, including disabled children with no active rules.
	it := m.client.getFirewallsClient().List(ctx, &computepb.ListFirewallsRequest{Project: project})

	var children []string

	for {
		f, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return WrapGCPError(err, "DeleteSecurityGroup.List")
		}

		if ownedFirewall(f, group) && f.GetName() != group {
			children = append(children, f.GetName())
		}
	}

	for _, child := range children {
		if err := m.deleteFirewall(ctx, child); err != nil {
			return err
		}
	}

	return m.deleteFirewall(ctx, group)
}
func makeFirewall(group, network string, rule *cpi.SecurityRule) (*computepb.Firewall, error) {
	if rule == nil {
		return nil, errors.New("security rule is required")
	}

	if rule.RemoteIPCIDR != "" {
		if _, err := netip.ParsePrefix(rule.RemoteIPCIDR); err != nil {
			return nil, fmt.Errorf("invalid remote CIDR: %w", err)
		}
	}

	direction := strings.ToUpper(rule.Direction)
	if direction != "INGRESS" && direction != "EGRESS" {
		return nil, fmt.Errorf("invalid security rule direction %q", rule.Direction)
	}

	protocol := strings.ToLower(rule.Protocol)
	if protocol == "-1" {
		protocol = "all"
	}

	switch protocol {
	case "tcp", "udp", "icmp", "all", "esp", "ah", "sctp":
	default:
		return nil, fmt.Errorf("unsupported firewall protocol %q", protocol)
	}

	if rule.RemoteGroup != "" && rule.RemoteIPCIDR != "" {
		return nil, errors.New("remote group and CIDR are mutually exclusive")
	}

	if direction == "EGRESS" && rule.RemoteGroup != "" {
		return nil, fmt.Errorf("%w: GCP egress cannot target network tags", ErrNotImplemented)
	}

	allowed, err := firewallAllowed(protocol, rule)
	if err != nil {
		return nil, err
	}

	raw, err := json.Marshal([]interface{}{direction, protocol, rule.PortRangeMin, rule.PortRangeMax, rule.RemoteIPCIDR, rule.RemoteGroup, rule.Description})
	if err != nil {
		return nil, fmt.Errorf("encode firewall identity: %w", err)
	}

	digest := sha256.Sum256(raw)

	prefix := SanitizeLabel(group)
	if len(prefix) > 40 {
		prefix = prefix[:40]
	}

	f := &computepb.Firewall{Name: proto(fmt.Sprintf("%s-%x", prefix, digest[:8])), Network: proto(network), Direction: proto(direction), TargetTags: []string{FormatNetworkTag(group)}, Allowed: []*computepb.Allowed{allowed}, Description: proto(firewallDescription(group, rule.Description, nil))}
	setFirewallRemote(f, direction, rule)

	return f, nil
}
func (m *SecurityManager) AddSecurityRule(ctx context.Context, groupID string, rule *cpi.SecurityRule) error {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return err
	}

	project := m.client.getConfig().GetNetworkProject()
	groupID = ExtractNameFromURL(groupID)

	anchor, err := m.client.getFirewallsClient().Get(ctx, &computepb.GetFirewallRequest{Project: project, Firewall: groupID})
	if err != nil {
		return WrapGCPError(err, "AddSecurityRule.GetGroup")
	}

	if !ownedFirewall(anchor, groupID) {
		return fmt.Errorf("refusing to mutate unmanaged security group %s", groupID)
	}

	f, err := makeFirewall(groupID, anchor.GetNetwork(), rule)
	if err != nil {
		return err
	}

	existing, err := m.client.getFirewallsClient().Get(ctx, &computepb.GetFirewallRequest{Project: project, Firewall: f.GetName()})
	if err == nil {
		if !ownedFirewall(existing, groupID) {
			return errors.New("firewall name collision")
		}

		if !sameFirewallPermissions(existing, f) {
			return fmt.Errorf("firewall %s has drifted; refusing to adopt different permissions", f.GetName())
		}

		return nil
	}

	if !IsNotFound(WrapGCPError(err, "AddSecurityRule.Get")) {
		return WrapGCPError(err, "AddSecurityRule.Get")
	}

	op, err := m.client.getFirewallsClient().Insert(ctx, &computepb.InsertFirewallRequest{Project: project, FirewallResource: f})
	if err != nil {
		return WrapGCPError(err, "AddSecurityRule")
	}

	return WrapGCPError(op.Wait(ctx), "AddSecurityRule.Wait")
}
func (m *SecurityManager) RemoveSecurityRule(ctx context.Context, groupID, ruleID string) error {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return err
	}

	f, err := m.client.getFirewallsClient().Get(ctx, &computepb.GetFirewallRequest{Project: m.client.getConfig().GetNetworkProject(), Firewall: ExtractNameFromURL(ruleID)})
	if err != nil {
		wrapped := WrapGCPError(err, "RemoveSecurityRule.Get")
		if IsNotFound(wrapped) {
			return nil
		}

		return wrapped
	}

	if !ownedFirewall(f, ExtractNameFromURL(groupID)) || f.GetName() == ExtractNameFromURL(groupID) {
		return errors.New("rule does not belong to security group")
	}

	return m.deleteFirewall(ctx, f.GetName())
}
func (m *SecurityManager) deleteFirewall(ctx context.Context, name string) error {
	op, err := m.client.getFirewallsClient().Delete(ctx, &computepb.DeleteFirewallRequest{Project: m.client.getConfig().GetNetworkProject(), Firewall: name})
	if err != nil {
		wrapped := WrapGCPError(err, "DeleteFirewall")
		if IsNotFound(wrapped) {
			return nil
		}

		return wrapped
	}

	return WrapGCPError(op.Wait(ctx), "DeleteFirewall.Wait")
}
func (m *SecurityManager) ListSecurityRules(ctx context.Context, groupID string) ([]*cpi.SecurityRule, error) {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return nil, err
	}

	groupID = ExtractNameFromURL(groupID)
	it := m.client.getFirewallsClient().List(ctx, &computepb.ListFirewallsRequest{Project: m.client.getConfig().GetNetworkProject()})
	rules := []*cpi.SecurityRule{}

	for {
		f, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "ListSecurityRules")
		}

		if ownedFirewall(f, groupID) && f.GetName() != groupID {
			rules = append(rules, m.extractRulesFromFirewall(f)...)
		}
	}

	return rules, nil
}
func (m *SecurityManager) convertFirewallToSecurityGroup(f *computepb.Firewall) *cpi.SecurityGroup {
	var owner firewallOwner

	_ = json.Unmarshal([]byte(f.GetDescription()), &owner)

	description := f.GetDescription()
	if owner.Managed == "ocfp" {
		description = owner.Description
	}

	return &cpi.SecurityGroup{ID: f.GetName(), Name: f.GetName(), Description: description, NetworkID: ExtractNameFromURL(f.GetNetwork()), Tags: owner.Tags, Rules: m.extractRulesFromFirewall(f), CreatedAt: ParseTimestamp(f.GetCreationTimestamp())}
}
func (m *SecurityManager) extractRulesFromFirewall(f *computepb.Firewall) []*cpi.SecurityRule {
	if f.GetDisabled() {
		return nil
	}

	var owner firewallOwner

	_ = json.Unmarshal([]byte(f.GetDescription()), &owner)

	direction := strings.ToLower(f.GetDirection())
	if direction == "" {
		direction = "ingress"
	}

	ranges := f.GetSourceRanges()
	if direction == "egress" {
		ranges = f.GetDestinationRanges()
	}

	if len(ranges) == 0 {
		ranges = []string{""}
	}

	rules := []*cpi.SecurityRule{}

	for _, allowed := range f.GetAllowed() {
		ports := allowed.GetPorts()
		if len(ports) == 0 {
			ports = []string{"0-65535"}
		}

		for _, port := range ports {
			portMin, portMax := parseSecurityPortRange(port)
			for _, remote := range ranges {
				r := &cpi.SecurityRule{ID: f.GetName(), Direction: direction, Protocol: allowed.GetIPProtocol(), PortRangeMin: portMin, PortRangeMax: portMax, RemoteIPCIDR: remote, Description: owner.Description}
				if len(f.GetSourceTags()) > 0 {
					r.RemoteGroup = strings.TrimPrefix(f.GetSourceTags()[0], "sg-")
				}

				rules = append(rules, r)
			}
		}
	}

	return rules
}
func logicalTag(tags map[string]string, key string) string {
	key = stripLabelPrefix(key)
	if value, ok := tags[key]; ok {
		return value
	}

	return tags[SanitizeLabel(key)]
}

func parseSecurityPortRange(port string) (int, int) {
	if strings.Contains(port, "-") {
		parts := strings.Split(port, "-")
		if len(parts) == 2 { //nolint:mnd
			var portMin, portMax int

			_, _ = fmt.Sscanf(parts[0], "%d", &portMin)
			_, _ = fmt.Sscanf(parts[1], "%d", &portMax)

			return portMin, portMax
		}
	}

	var p int

	_, _ = fmt.Sscanf(port, "%d", &p)

	return p, p
}

// Compare authorization semantics, excluding server-populated identity fields.
func sameFirewallPermissions(a, b *computepb.Firewall) bool {
	return strings.TrimPrefix(a.GetNetwork(), "https://www.googleapis.com/compute/v1/") == strings.TrimPrefix(b.GetNetwork(), "https://www.googleapis.com/compute/v1/") && a.GetDirection() == b.GetDirection() &&
		a.GetDisabled() == b.GetDisabled() && reflect.DeepEqual(a.GetTargetTags(), b.GetTargetTags()) &&
		reflect.DeepEqual(a.GetSourceRanges(), b.GetSourceRanges()) && reflect.DeepEqual(a.GetDestinationRanges(), b.GetDestinationRanges()) &&
		reflect.DeepEqual(a.GetSourceTags(), b.GetSourceTags()) && gproto.Equal(&computepb.Firewall{Allowed: a.GetAllowed()}, &computepb.Firewall{Allowed: b.GetAllowed()}) &&
		len(a.GetDenied()) == 0 && len(a.GetSourceServiceAccounts()) == 0 && len(a.GetTargetServiceAccounts()) == 0
}

func firewallAllowed(protocol string, rule *cpi.SecurityRule) (*computepb.Allowed, error) {
	allowed := &computepb.Allowed{IPProtocol: proto(protocol)}
	if protocol == "tcp" || protocol == "udp" || protocol == "sctp" {
		if rule.PortRangeMin < 0 || rule.PortRangeMax > 65535 || rule.PortRangeMax < rule.PortRangeMin {
			return nil, errors.New("invalid port range")
		}

		if rule.PortRangeMin > 0 || (rule.PortRangeMax > 0 && rule.PortRangeMax < 65535) {
			port := strconv.Itoa(rule.PortRangeMin)
			if rule.PortRangeMax != rule.PortRangeMin {
				port = fmt.Sprintf("%d-%d", rule.PortRangeMin, rule.PortRangeMax)
			}

			allowed.Ports = []string{port}
		}
	}

	return allowed, nil
}

func setFirewallRemote(f *computepb.Firewall, direction string, rule *cpi.SecurityRule) {
	remote := rule.RemoteIPCIDR
	if remote == "" {
		remote = "0.0.0.0/0"
	}

	switch {
	case direction == "EGRESS":
		f.DestinationRanges = []string{remote}
	case rule.RemoteGroup != "":
		f.SourceTags = []string{FormatNetworkTag(ExtractNameFromURL(rule.RemoteGroup))}
	default:
		f.SourceRanges = []string{remote}
	}
}
