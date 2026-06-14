package pve

import (
	"context"
	"fmt"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// pveGroupNameRegex matches the constraint PVE places on firewall group names:
// must start with a letter and be 2-18 chars of [A-Za-z0-9_-]. Names that
// don't satisfy this (e.g. OCFP bloc names starting with a digit, or names
// longer than 18 chars) are sanitized via sanitizePVEGroupName before being
// sent on the wire. The original name is preserved in the group's comment.
var pveGroupNameRegex = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{1,17}$`)

// pveOCFPCommentRegex extracts the original OCFP name embedded by
// CreateSecurityGroup in the PVE firewall group comment. CreateSecurityGroup
// writes one of these forms:
//
//	"ocfp:<name>"
//	"ocfp:<name> - <description>"
//	"<description> (ocfp:<name>)"   // legacy, still parsed for backward compat
//
// The capture group returns just <name>.
var pveOCFPCommentRegex = regexp.MustCompile(`ocfp:([A-Za-z0-9._-]+)`)

// pveRedundantDescPrefix matches the legacy "Security group for " / "Security
// group " prefix that bootstrap descriptions historically carried. Stripped
// from the comment because the PVE UI column is already labelled "Comment" on
// a firewall-groups page — the words add no information.
var pveRedundantDescPrefix = regexp.MustCompile(`(?i)^security group(?:\s+for)?\s+`)

//nolint:unused // referenced by managers_test.go boundary checks
const pveGroupNameMaxLen = 18

// extractOCFPNameFromComment returns the original OCFP-side name if the PVE
// group comment carries an "ocfp:<name>" marker (see CreateSecurityGroup).
// Returns empty string when no marker is present, in which case callers
// should fall back to the on-wire PVE group ID.
func extractOCFPNameFromComment(comment string) string {
	match := pveOCFPCommentRegex.FindStringSubmatch(comment)
	if len(match) >= 2 {
		return match[1]
	}

	return ""
}

// sanitizePVEGroupName returns name unchanged when it already satisfies the
// PVE firewall group regex; otherwise it returns a deterministic, regex-safe
// identifier derived from a 32-bit FNV-1a hash of the original. The mapping
// is stable for the lifetime of the input string so subsequent
// Get/Delete/Assign calls can reproduce it without state lookup.
func sanitizePVEGroupName(name string) string {
	if pveGroupNameRegex.MatchString(name) {
		return name
	}

	h := fnv.New32a()
	_, _ = h.Write([]byte(name))

	// "g-" prefix guarantees the first-char-must-be-letter constraint;
	// 8 hex chars keeps total length at 10 (well under the 18 limit).
	return fmt.Sprintf("g-%08x", h.Sum32())
}

// SecurityManager handles Proxmox firewall operations.
type SecurityManager struct {
	client *Client
}

// CreateSecurityGroup creates a firewall security group.
func (m *SecurityManager) CreateSecurityGroup(ctx context.Context, req *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	pveName := sanitizePVEGroupName(req.Name)

	logger.WithOperation("CreateSecurityGroup").Infof("Creating firewall group: %s (pve_name=%s)", req.Name, pveName)

	// Preserve the original (human-readable) name in the group comment so
	// operators can map the PVE-side identifier back to the OCFP resource.
	// Format: "ocfp:<name> - <description>" — the marker leads so the PVE
	// firewall UI sorts/filters by it cleanly. Strip the redundant
	// "Security group [for]" prefix that bootstrap descriptions historically
	// included.
	desc := strings.TrimSpace(pveRedundantDescPrefix.ReplaceAllString(req.Description, ""))

	comment := "ocfp:" + req.Name
	if desc != "" {
		comment = comment + " - " + desc
	}

	// Create cluster-level firewall group
	params := map[string]interface{}{
		"group":   pveName,
		"comment": comment,
	}

	_, err := m.client.pveClient.PostCtx(ctx, "/cluster/firewall/groups", params)
	if err != nil {
		// Check if already exists
		if strings.Contains(err.Error(), "already exists") {
			logger.Infof("Firewall group %s already exists", pveName)
		} else {
			return nil, fmt.Errorf("failed to create firewall group: %w", err)
		}
	}

	// Add rules if provided — use the sanitized PVE name on the wire.
	for _, rule := range req.Rules {
		_ = m.AddSecurityRule(ctx, pveName, rule)
	}

	return &cpi.SecurityGroup{
		ID:          pveName,
		Name:        req.Name,
		Description: req.Description,
		Rules:       req.Rules,
		Tags:        req.Tags,
		CreatedAt:   time.Now(),
	}, nil
}

// GetSecurityGroup retrieves a firewall group.
func (m *SecurityManager) GetSecurityGroup(ctx context.Context, id string) (*cpi.SecurityGroup, error) { //nolint:varnamelen // id is clear in context
	pveID := sanitizePVEGroupName(id)
	path := "/cluster/firewall/groups/" + pveID

	resp, err := m.client.pveClient.GetCtx(ctx, path, nil)
	if err != nil {
		return nil, ErrSecurityGroupNotFound(id)
	}

	data, ok := resp.([]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResponseType, resp)
	}

	// Parse rules
	var rules []*cpi.SecurityRule

	for i, item := range data { //nolint:varnamelen // i is clear in context
		ruleData, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		rule := &cpi.SecurityRule{
			ID:           strconv.Itoa(i),
			Direction:    getStringFromMap(ruleData, "type"),
			Protocol:     getStringFromMap(ruleData, "proto"),
			RemoteIPCIDR: getStringFromMap(ruleData, "source"),
			Description:  getStringFromMap(ruleData, "comment"),
		}

		// Parse port range
		if dport := getStringFromMap(ruleData, "dport"); dport != "" {
			if strings.Contains(dport, ":") {
				parts := strings.Split(dport, ":")
				if len(parts) == 2 { //nolint:mnd // splitting port range "from:to" always yields 2 parts
					_, _ = fmt.Sscanf(parts[0], "%d", &rule.PortRangeMin)
					_, _ = fmt.Sscanf(parts[1], "%d", &rule.PortRangeMax)
				}
			} else {
				_, _ = fmt.Sscanf(dport, "%d", &rule.PortRangeMin)
				rule.PortRangeMax = rule.PortRangeMin
			}
		}

		rules = append(rules, rule)
	}

	return &cpi.SecurityGroup{
		ID:    pveID,
		Name:  id,
		Rules: rules,
		Tags:  make(map[string]string),
	}, nil
}

// ListSecurityGroups lists all firewall groups.
func (m *SecurityManager) ListSecurityGroups(ctx context.Context, filters map[string]string) ([]*cpi.SecurityGroup, error) {
	resp, err := m.client.pveClient.GetCtx(ctx, "/cluster/firewall/groups", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list firewall groups: %w", err)
	}

	data, ok := resp.([]interface{})
	if !ok {
		return []*cpi.SecurityGroup{}, nil
	}

	var groups []*cpi.SecurityGroup

	for _, item := range data {
		groupData, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		pveID := getStringFromMap(groupData, "group")
		comment := getStringFromMap(groupData, "comment")

		// Recover the original OCFP-side name when CreateSecurityGroup had to
		// hash it for PVE's 18-char regex; otherwise the on-wire ID is the
		// human name.
		displayName := extractOCFPNameFromComment(comment)
		if displayName == "" {
			displayName = pveID
		}

		// Apply name filter — match against the OCFP-side name so callers can
		// look up resources using the names they created with.
		if nameFilter, ok := filters["name"]; ok && displayName != nameFilter && pveID != nameFilter {
			continue
		}

		groups = append(groups, &cpi.SecurityGroup{
			ID:          pveID,
			Name:        displayName,
			Description: comment,
			Tags:        make(map[string]string),
		})
	}

	return groups, nil
}

// DeleteSecurityGroup deletes a firewall group. PVE refuses to delete a
// non-empty group ("Security group '<id>' is not empty"), so we flush every
// rule first by repeatedly deleting position 0 until the group is empty,
// then issue the group DELETE. Deleting position 0 each iteration is robust
// against PVE renumbering remaining rules after each delete.
func (m *SecurityManager) DeleteSecurityGroup(ctx context.Context, id string) error {
	pveID := sanitizePVEGroupName(id)
	groupPath := "/cluster/firewall/groups/" + pveID

	for {
		resp, err := m.client.pveClient.GetCtx(ctx, groupPath, nil)
		if err != nil {
			// Group already gone — treat as success (idempotent delete).
			return nil //nolint:nilerr // 404/absent group == already deleted
		}

		rules, ok := resp.([]interface{})
		if !ok || len(rules) == 0 {
			break
		}

		rulePath := groupPath + "/0"
		if _, err := m.client.pveClient.DeleteCtx(ctx, rulePath, nil); err != nil {
			return fmt.Errorf("failed to remove firewall rule before group delete: %w", err)
		}
	}

	if _, err := m.client.pveClient.DeleteCtx(ctx, groupPath, nil); err != nil {
		return fmt.Errorf("failed to delete firewall group: %w", err)
	}

	return nil
}

// AddSecurityRule adds a rule to a firewall group.
func (m *SecurityManager) AddSecurityRule(ctx context.Context, groupID string, rule *cpi.SecurityRule) error {
	path := "/cluster/firewall/groups/" + sanitizePVEGroupName(groupID)

	// Map direction
	ruleType := "in"
	if rule.Direction == "egress" {
		ruleType = "out"
	}

	// Build port specification
	var dport string

	if rule.PortRangeMin > 0 {
		if rule.PortRangeMax > rule.PortRangeMin {
			dport = fmt.Sprintf("%d:%d", rule.PortRangeMin, rule.PortRangeMax)
		} else {
			dport = strconv.Itoa(rule.PortRangeMin)
		}
	}

	params := map[string]interface{}{
		pveKeyType:   ruleType,
		"action":     "ACCEPT",
		pveKeyEnable: 1,
	}

	if rule.Protocol != "" && rule.Protocol != "all" {
		params["proto"] = rule.Protocol
	}

	if dport != "" {
		params["dport"] = dport
	}

	if rule.RemoteIPCIDR != "" {
		params["source"] = rule.RemoteIPCIDR
	}

	if rule.Description != "" {
		params["comment"] = rule.Description
	}

	_, err := m.client.pveClient.PostCtx(ctx, path, params)
	if err != nil {
		// Ignore duplicate rule errors
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}

		return fmt.Errorf("failed to add firewall rule: %w", err)
	}

	return nil
}

// RemoveSecurityRule removes a rule from a firewall group.
func (m *SecurityManager) RemoveSecurityRule(ctx context.Context, groupID string, ruleID string) error {
	path := fmt.Sprintf("/cluster/firewall/groups/%s/%s", sanitizePVEGroupName(groupID), ruleID)

	_, err := m.client.pveClient.DeleteCtx(ctx, path, nil)
	if err != nil {
		return fmt.Errorf("failed to remove firewall rule: %w", err)
	}

	return nil
}

// ListSecurityRules lists rules in a firewall group.
func (m *SecurityManager) ListSecurityRules(ctx context.Context, groupID string) ([]*cpi.SecurityRule, error) {
	sg, err := m.GetSecurityGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}

	return sg.Rules, nil
}

// LoadBalancerManager handles load balancer operations (not natively supported).
type LoadBalancerManager struct {
	client *Client
}

// CreateLoadBalancer creates a load balancer (not supported).
func (m *LoadBalancerManager) CreateLoadBalancer(_ctx context.Context, _req *cpi.CreateLoadBalancerRequest) (*cpi.LoadBalancer, error) {
	return nil, ErrLoadBalancersNotSupported
}

// GetLoadBalancer retrieves a load balancer.
func (m *LoadBalancerManager) GetLoadBalancer(_ctx context.Context, _id string) (*cpi.LoadBalancer, error) {
	return nil, ErrLoadBalancersNotSupported
}

// ListLoadBalancers lists load balancers.
func (m *LoadBalancerManager) ListLoadBalancers(_ctx context.Context, _filters map[string]string) ([]*cpi.LoadBalancer, error) {
	return []*cpi.LoadBalancer{}, nil
}

// UpdateLoadBalancer updates a load balancer.
func (m *LoadBalancerManager) UpdateLoadBalancer(_ctx context.Context, _id string, _req *cpi.UpdateLoadBalancerRequest) error {
	return ErrLoadBalancersNotSupported
}

// DeleteLoadBalancer deletes a load balancer.
func (m *LoadBalancerManager) DeleteLoadBalancer(_ctx context.Context, _id string) error {
	return ErrLoadBalancersNotSupported
}

// AddBackend adds a backend to a load balancer.
func (m *LoadBalancerManager) AddBackend(_ctx context.Context, _lbID string, _backend *cpi.Backend) error {
	return ErrLoadBalancersNotSupported
}

// RemoveBackend removes a backend from a load balancer.
func (m *LoadBalancerManager) RemoveBackend(_ctx context.Context, _lbID string, _backendID string) error {
	return ErrLoadBalancersNotSupported
}

// EnableBackend enables a backend.
func (m *LoadBalancerManager) EnableBackend(_ctx context.Context, _lbID string, _backendID string) error {
	return ErrEnableBackendNotImplemented
}

// DisableBackend disables a backend.
func (m *LoadBalancerManager) DisableBackend(_ctx context.Context, _lbID string, _backendID string) error {
	return ErrDisableBackendNotImplemented
}

// ConfigureHealthCheck configures a health check.
func (m *LoadBalancerManager) ConfigureHealthCheck(_ctx context.Context, _lbID string, _check *cpi.HealthCheck) error {
	return ErrLoadBalancersNotSupported
}

// GetHealthStatus retrieves health status.
func (m *LoadBalancerManager) GetHealthStatus(_ctx context.Context, _lbID string) (*cpi.HealthStatus, error) {
	return nil, ErrGetHealthStatusNotImplemented
}
