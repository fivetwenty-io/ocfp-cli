package stackit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// ComputeManager handles STACKIT compute operations
type ComputeManager struct {
	client *Client
}

// StorageManager handles STACKIT storage operations
type StorageManager struct {
	client *Client
}

// SecurityManager handles STACKIT security operations
type SecurityManager struct {
	client *Client
}

func (m *SecurityManager) CreateSecurityGroup(ctx context.Context, req *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	payload := map[string]interface{}{
		"name":        req.Name,
		"description": req.Description,
		"network_id":  req.NetworkID,
		"labels":      req.Tags,
	}
	httpReq, err := m.client.newRequest(ctx, "POST", "/v1/projects/"+m.client.config.ProjectID+"/security-groups", payload)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 201 && resp.StatusCode != 200 {
		return nil, m.client.parseError(resp)
	}
	var out cpi.SecurityGroup
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	// Add initial rules if provided
	for _, r := range req.Rules {
		_ = m.AddSecurityRule(ctx, out.ID, r)
	}
	return &out, nil
}

func (m *SecurityManager) GetSecurityGroup(ctx context.Context, id string) (*cpi.SecurityGroup, error) {
	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/security-groups/"+id, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 404 {
		return nil, &cpi.ProviderError{Provider: "stackit", Code: "NotFound", Message: fmt.Sprintf("SecurityGroup %s not found", id)}
	}
	if resp.StatusCode != 200 {
		return nil, m.client.parseError(resp)
	}
	var out cpi.SecurityGroup
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (m *SecurityManager) ListSecurityGroups(ctx context.Context, filters map[string]string) ([]*cpi.SecurityGroup, error) {
	// Basic list; ignore filters for now
	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/security-groups", nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return nil, m.client.parseError(resp)
	}
	var result struct {
		SecurityGroups []*cpi.SecurityGroup `json:"security_groups"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.SecurityGroups, nil
}

func (m *SecurityManager) DeleteSecurityGroup(ctx context.Context, id string) error {
	httpReq, err := m.client.newRequest(ctx, "DELETE", "/v1/projects/"+m.client.config.ProjectID+"/security-groups/"+id, nil)
	if err != nil {
		return err
	}
	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 404 {
		return nil
	}
	if resp.StatusCode != 204 && resp.StatusCode != 200 {
		return m.client.parseError(resp)
	}
	return nil
}

func (m *SecurityManager) AddSecurityRule(ctx context.Context, groupID string, rule *cpi.SecurityRule) error {
	payload := map[string]interface{}{
		"direction":    rule.Direction,
		"protocol":     rule.Protocol,
		"port_min":     rule.PortRangeMin,
		"port_max":     rule.PortRangeMax,
		"remote_cidr":  rule.RemoteIPCIDR,
		"remote_group": rule.RemoteGroup,
		"description":  rule.Description,
	}
	httpReq, err := m.client.newRequest(ctx, "POST", "/v1/projects/"+m.client.config.ProjectID+"/security-groups/"+groupID+"/rules", payload)
	if err != nil {
		return err
	}
	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 201 && resp.StatusCode != 200 {
		return m.client.parseError(resp)
	}
	return nil
}

func (m *SecurityManager) RemoveSecurityRule(ctx context.Context, groupID string, ruleID string) error {
	httpReq, err := m.client.newRequest(ctx, "DELETE", "/v1/projects/"+m.client.config.ProjectID+"/security-groups/"+groupID+"/rules/"+ruleID, nil)
	if err != nil {
		return err
	}
	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 404 {
		return nil
	}
	if resp.StatusCode != 204 && resp.StatusCode != 200 {
		return m.client.parseError(resp)
	}
	return nil
}

func (m *SecurityManager) ListSecurityRules(ctx context.Context, groupID string) ([]*cpi.SecurityRule, error) {
	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/security-groups/"+groupID+"/rules", nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return nil, m.client.parseError(resp)
	}
	var result struct {
		Rules []*cpi.SecurityRule `json:"rules"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Rules, nil
}

// LoadBalancerManager handles STACKIT load balancer operations
type LoadBalancerManager struct {
	client *Client
}

func (m *LoadBalancerManager) CreateLoadBalancer(ctx context.Context, req *cpi.CreateLoadBalancerRequest) (*cpi.LoadBalancer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) GetLoadBalancer(ctx context.Context, id string) (*cpi.LoadBalancer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) ListLoadBalancers(ctx context.Context, filters map[string]string) ([]*cpi.LoadBalancer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) UpdateLoadBalancer(ctx context.Context, id string, req *cpi.UpdateLoadBalancerRequest) error {
	return fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) DeleteLoadBalancer(ctx context.Context, id string) error {
	return fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) AddBackend(ctx context.Context, lbID string, backend *cpi.Backend) error {
	return fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) RemoveBackend(ctx context.Context, lbID string, backendID string) error {
	return fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) EnableBackend(ctx context.Context, lbID string, backendID string) error {
	return fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) DisableBackend(ctx context.Context, lbID string, backendID string) error {
	return fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) ConfigureHealthCheck(ctx context.Context, lbID string, check *cpi.HealthCheck) error {
	return fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) GetHealthStatus(ctx context.Context, lbID string) (*cpi.HealthStatus, error) {
	return nil, fmt.Errorf("not implemented")
}
