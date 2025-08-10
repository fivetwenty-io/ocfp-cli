package stackit

import (
	"context"
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
	// TODO: Implement
	return &cpi.SecurityGroup{
		ID:          fmt.Sprintf("sg-%s", req.Name),
		Name:        req.Name,
		Description: req.Description,
		NetworkID:   req.NetworkID,
		Rules:       req.Rules,
		Tags:        req.Tags,
	}, nil
}

func (m *SecurityManager) GetSecurityGroup(ctx context.Context, id string) (*cpi.SecurityGroup, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *SecurityManager) ListSecurityGroups(ctx context.Context, filters map[string]string) ([]*cpi.SecurityGroup, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *SecurityManager) DeleteSecurityGroup(ctx context.Context, id string) error {
	return fmt.Errorf("not implemented")
}

func (m *SecurityManager) AddSecurityRule(ctx context.Context, groupID string, rule *cpi.SecurityRule) error {
	return fmt.Errorf("not implemented")
}

func (m *SecurityManager) RemoveSecurityRule(ctx context.Context, groupID string, ruleID string) error {
	return fmt.Errorf("not implemented")
}

func (m *SecurityManager) ListSecurityRules(ctx context.Context, groupID string) ([]*cpi.SecurityRule, error) {
	return nil, fmt.Errorf("not implemented")
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
