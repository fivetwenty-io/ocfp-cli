package proxmox

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// SecurityManager handles Proxmox firewall operations.
type SecurityManager struct {
	client *Client
}

// CreateSecurityGroup creates a firewall security group.
func (m *SecurityManager) CreateSecurityGroup(ctx context.Context, req *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	logger.WithOperation("CreateSecurityGroup").Infof("Creating firewall group: %s", req.Name)

	// Create cluster-level firewall group
	params := map[string]interface{}{
		"group":   req.Name,
		"comment": req.Description,
	}

	_, err := m.client.pveClient.PostCtx(ctx, "/cluster/firewall/groups", params)
	if err != nil {
		// Check if already exists
		if strings.Contains(err.Error(), "already exists") {
			logger.Infof("Firewall group %s already exists", req.Name)
		} else {
			return nil, fmt.Errorf("failed to create firewall group: %w", err)
		}
	}

	// Add rules if provided
	for _, rule := range req.Rules {
		_ = m.AddSecurityRule(ctx, req.Name, rule)
	}

	return &cpi.SecurityGroup{
		ID:          req.Name,
		Name:        req.Name,
		Description: req.Description,
		Rules:       req.Rules,
		Tags:        req.Tags,
		CreatedAt:   time.Now(),
	}, nil
}

// GetSecurityGroup retrieves a firewall group.
func (m *SecurityManager) GetSecurityGroup(ctx context.Context, id string) (*cpi.SecurityGroup, error) { //nolint:varnamelen // id is clear in context
	path := "/cluster/firewall/groups/" + id

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
		ID:    id,
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

		name := getStringFromMap(groupData, "group")

		// Apply name filter
		if nameFilter, ok := filters["name"]; ok && name != nameFilter {
			continue
		}

		groups = append(groups, &cpi.SecurityGroup{
			ID:          name,
			Name:        name,
			Description: getStringFromMap(groupData, "comment"),
			Tags:        make(map[string]string),
		})
	}

	return groups, nil
}

// DeleteSecurityGroup deletes a firewall group.
func (m *SecurityManager) DeleteSecurityGroup(ctx context.Context, id string) error {
	path := "/cluster/firewall/groups/" + id

	_, err := m.client.pveClient.DeleteCtx(ctx, path, nil)
	if err != nil {
		return fmt.Errorf("failed to delete firewall group: %w", err)
	}

	return nil
}

// AddSecurityRule adds a rule to a firewall group.
func (m *SecurityManager) AddSecurityRule(ctx context.Context, groupID string, rule *cpi.SecurityRule) error {
	path := "/cluster/firewall/groups/" + groupID

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
		"type":   ruleType,
		"action": "ACCEPT",
		"enable": 1,
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
	path := fmt.Sprintf("/cluster/firewall/groups/%s/%s", groupID, ruleID)

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
