package gcp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// CreateSecurityGroup creates a firewall rule (GCP uses network tags for grouping).
//nolint:dupl // intentionally similar CPI implementation
func (m *SecurityManager) CreateSecurityGroup(ctx context.Context, req *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	projectID := m.client.getConfig().GetNetworkProject()
	networkTag := FormatNetworkTag(req.Name)

	// Build network URL
	networkURL := FormatNetworkURL(projectID, req.NetworkID)

	// Create firewall rule for this security group
	firewall := &computepb.Firewall{
		Name:        proto(req.Name),
		Description: proto(req.Description),
		Network:     proto(networkURL),
		TargetTags:  []string{networkTag},
		Allowed:     []*computepb.Allowed{},
	}

	// Add rules
	for _, rule := range req.Rules {
		allowed := &computepb.Allowed{
			IPProtocol: proto(rule.Protocol),
		}

		if rule.PortRangeMin > 0 && rule.PortRangeMax > 0 {
			if rule.PortRangeMin == rule.PortRangeMax {
				allowed.Ports = []string{strconv.Itoa(rule.PortRangeMin)}
			} else {
				allowed.Ports = []string{fmt.Sprintf("%d-%d", rule.PortRangeMin, rule.PortRangeMax)}
			}
		}

		firewall.Allowed = append(firewall.Allowed, allowed)

		// Set source ranges for ingress
		if rule.Direction == DirectionIngress && rule.RemoteIPCIDR != "" {
			firewall.SourceRanges = append(firewall.SourceRanges, rule.RemoteIPCIDR)
		}
	}

	// Default to allow from anywhere if no source ranges specified
	if len(firewall.GetSourceRanges()) == 0 {
		firewall.SourceRanges = []string{"0.0.0.0/0"}
	}

	op, err := m.client.getFirewallsClient().Insert(ctx, &computepb.InsertFirewallRequest{ //nolint:varnamelen
		Project:          projectID,
		FirewallResource: firewall,
	})
	if err != nil {
		return nil, WrapGCPError(err, "CreateSecurityGroup")
	}

	err = op.Wait(ctx)
	if err != nil {
		return nil, WrapGCPError(err, "CreateSecurityGroup.Wait")
	}

	logger.Debugw("Created firewall rule", "name", req.Name, "networkTag", networkTag)

	return m.GetSecurityGroup(ctx, req.Name)
}

// GetSecurityGroup retrieves a firewall rule by name.
func (m *SecurityManager) GetSecurityGroup(ctx context.Context, id string) (*cpi.SecurityGroup, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	projectID := m.client.getConfig().GetNetworkProject()

	firewall, err := m.client.getFirewallsClient().Get(ctx, &computepb.GetFirewallRequest{
		Project:  projectID,
		Firewall: id,
	})
	if err != nil {
		return nil, WrapGCPError(err, "GetSecurityGroup")
	}

	return m.convertFirewallToSecurityGroup(firewall), nil
}

// ListSecurityGroups lists firewall rules.
func (m *SecurityManager) ListSecurityGroups(ctx context.Context, filters map[string]string) ([]*cpi.SecurityGroup, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	projectID := m.client.getConfig().GetNetworkProject()

	var securityGroups []*cpi.SecurityGroup

	it := m.client.getFirewallsClient().List(ctx, &computepb.ListFirewallsRequest{
		Project: projectID,
	})

	for {
		firewall, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "ListSecurityGroups")
		}

		// Filter by network if specified
		if networkID, ok := filters["network_id"]; ok {
			if !strings.HasSuffix(firewall.GetNetwork(), "/"+networkID) {
				continue
			}
		}

		securityGroups = append(securityGroups, m.convertFirewallToSecurityGroup(firewall))
	}

	return securityGroups, nil
}

// DeleteSecurityGroup deletes a firewall rule.
func (m *SecurityManager) DeleteSecurityGroup(ctx context.Context, id string) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	projectID := m.client.getConfig().GetNetworkProject()

	op, err := m.client.getFirewallsClient().Delete(ctx, &computepb.DeleteFirewallRequest{ //nolint:varnamelen
		Project:  projectID,
		Firewall: id,
	})
	if err != nil {
		return WrapGCPError(err, "DeleteSecurityGroup")
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "DeleteSecurityGroup.Wait")
	}

	logger.Debugw("Deleted firewall rule", "name", id)

	return nil
}

// AddSecurityRule adds a rule to a security group (creates a new firewall rule).
func (m *SecurityManager) AddSecurityRule(ctx context.Context, groupID string, rule *cpi.SecurityRule) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	projectID := m.client.getConfig().GetNetworkProject()

	// Get existing firewall to get network and target tags
	existing, err := m.client.getFirewallsClient().Get(ctx, &computepb.GetFirewallRequest{
		Project:  projectID,
		Firewall: groupID,
	})
	if err != nil {
		return WrapGCPError(err, "AddSecurityRule.GetExisting")
	}

	// Create a new firewall rule for this specific rule
	ruleName := FormatFirewallRuleName(groupID, rule.Direction, rule.PortRangeMin)

	allowed := &computepb.Allowed{
		IPProtocol: proto(rule.Protocol),
	}

	if rule.PortRangeMin > 0 && rule.PortRangeMax > 0 {
		if rule.PortRangeMin == rule.PortRangeMax {
			allowed.Ports = []string{strconv.Itoa(rule.PortRangeMin)}
		} else {
			allowed.Ports = []string{fmt.Sprintf("%d-%d", rule.PortRangeMin, rule.PortRangeMax)}
		}
	}

	sourceRanges := []string{"0.0.0.0/0"}
	if rule.RemoteIPCIDR != "" {
		sourceRanges = []string{rule.RemoteIPCIDR}
	}

	firewall := &computepb.Firewall{
		Name:         proto(ruleName),
		Description:  proto(rule.Description),
		Network:      existing.Network,
		TargetTags:   existing.GetTargetTags(),
		Allowed:      []*computepb.Allowed{allowed},
		SourceRanges: sourceRanges,
	}

	op, err := m.client.getFirewallsClient().Insert(ctx, &computepb.InsertFirewallRequest{ //nolint:varnamelen
		Project:          projectID,
		FirewallResource: firewall,
	})
	if err != nil {
		return WrapGCPError(err, "AddSecurityRule")
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "AddSecurityRule.Wait")
	}

	logger.Debugw("Added security rule", "group", groupID, "rule", ruleName)

	return nil
}

// RemoveSecurityRule removes a rule from a security group (deletes the firewall rule).
func (m *SecurityManager) RemoveSecurityRule(ctx context.Context, groupID string, ruleID string) error {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	projectID := m.client.getConfig().GetNetworkProject()

	op, err := m.client.getFirewallsClient().Delete(ctx, &computepb.DeleteFirewallRequest{ //nolint:varnamelen
		Project:  projectID,
		Firewall: ruleID,
	})
	if err != nil {
		return WrapGCPError(err, "RemoveSecurityRule")
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "RemoveSecurityRule.Wait")
	}

	logger.Debugw("Removed security rule", "group", groupID, "rule", ruleID)

	return nil
}

// ListSecurityRules lists rules in a security group.
func (m *SecurityManager) ListSecurityRules(ctx context.Context, groupID string) ([]*cpi.SecurityRule, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	projectID := m.client.getConfig().GetNetworkProject()

	// Get the main firewall rule
	firewall, err := m.client.getFirewallsClient().Get(ctx, &computepb.GetFirewallRequest{
		Project:  projectID,
		Firewall: groupID,
	})
	if err != nil {
		return nil, WrapGCPError(err, "ListSecurityRules")
	}

	return m.extractRulesFromFirewall(firewall), nil
}

// Helper functions

func (m *SecurityManager) convertFirewallToSecurityGroup(firewall *computepb.Firewall) *cpi.SecurityGroup {
	rules := m.extractRulesFromFirewall(firewall)

	return &cpi.SecurityGroup{
		ID:          strconv.FormatUint(firewall.GetId(), 10),
		Name:        firewall.GetName(),
		Description: firewall.GetDescription(),
		NetworkID:   ExtractNameFromURL(firewall.GetNetwork()),
		Rules:       rules,
		CreatedAt:   ParseTimestamp(firewall.GetCreationTimestamp()),
	}
}

func (m *SecurityManager) extractRulesFromFirewall(firewall *computepb.Firewall) []*cpi.SecurityRule {
	var rules []*cpi.SecurityRule

	// Determine direction based on firewall configuration
	direction := DirectionIngress
	if firewall.GetDirection() == "EGRESS" {
		direction = "egress"
	}

	// Get remote CIDR from source ranges
	remoteCIDR := ""
	if len(firewall.GetSourceRanges()) > 0 {
		remoteCIDR = firewall.GetSourceRanges()[0]
	}

	// Convert allowed rules
	for _, allowed := range firewall.GetAllowed() {
		for _, port := range allowed.GetPorts() {
			portMin, portMax := parseSecurityPortRange(port)
			rules = append(rules, &cpi.SecurityRule{
				ID:           fmt.Sprintf("%d-%s-%s", firewall.GetId(), allowed.GetIPProtocol(), port),
				Direction:    direction,
				Protocol:     allowed.GetIPProtocol(),
				PortRangeMin: portMin,
				PortRangeMax: portMax,
				RemoteIPCIDR: remoteCIDR,
				Description:  firewall.GetDescription(),
			})
		}

		// If no ports specified, it's an all-ports rule
		if len(allowed.GetPorts()) == 0 {
			rules = append(rules, &cpi.SecurityRule{
				ID:           fmt.Sprintf("%d-%s-all", firewall.GetId(), allowed.GetIPProtocol()),
				Direction:    direction,
				Protocol:     allowed.GetIPProtocol(),
				PortRangeMin: 0,
				PortRangeMax: 65535, //nolint:mnd
				RemoteIPCIDR: remoteCIDR,
				Description:  firewall.GetDescription(),
			})
		}
	}

	return rules
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
