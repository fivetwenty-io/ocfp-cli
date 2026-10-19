package gcp

import (
	"cloud.google.com/go/compute/apiv1/computepb"
	"context"
	"errors"
	"fmt"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"google.golang.org/api/iterator"
)

// Native cloud load balancing is not implemented by the PVE baseline either.
// The former GCP create path mixed global HTTP backends with regional
// forwarding rules and leaked resources. Fail before mutation until a complete
// supported architecture and backend contract is available.
func unsupportedLoadBalancer() error {
	return fmt.Errorf("%w: native GCP load balancer mutation requires a supported forwarding/backend architecture; use the deployment's HAProxy ingress", ErrNotImplemented)
}
func (m *LoadBalancerManager) CreateLoadBalancer(context.Context, *cpi.CreateLoadBalancerRequest) (*cpi.LoadBalancer, error) {
	return nil, unsupportedLoadBalancer()
}
func (m *LoadBalancerManager) UpdateLoadBalancer(context.Context, string, *cpi.UpdateLoadBalancerRequest) error {
	return unsupportedLoadBalancer()
}
func (m *LoadBalancerManager) DeleteLoadBalancer(context.Context, string) error {
	return unsupportedLoadBalancer()
}
func (m *LoadBalancerManager) AddBackend(context.Context, string, *cpi.Backend) error {
	return unsupportedLoadBalancer()
}
func (m *LoadBalancerManager) RemoveBackend(context.Context, string, string) error {
	return unsupportedLoadBalancer()
}
func (m *LoadBalancerManager) EnableBackend(context.Context, string, string) error {
	return unsupportedLoadBalancer()
}
func (m *LoadBalancerManager) DisableBackend(context.Context, string, string) error {
	return unsupportedLoadBalancer()
}
func (m *LoadBalancerManager) ConfigureHealthCheck(context.Context, string, *cpi.HealthCheck) error {
	return unsupportedLoadBalancer()
}
func (m *LoadBalancerManager) GetHealthStatus(context.Context, string) (*cpi.HealthStatus, error) {
	return nil, unsupportedLoadBalancer()
}
func (m *LoadBalancerManager) GetLoadBalancer(ctx context.Context, id string) (*cpi.LoadBalancer, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	region := config.Region

	fr, err := m.client.getForwardingRulesClient().Get(ctx, &computepb.GetForwardingRuleRequest{ //nolint:varnamelen
		Project:        projectID,
		Region:         region,
		ForwardingRule: id,
	})
	if err != nil {
		return nil, WrapGCPError(err, "GetLoadBalancer")
	}

	return m.convertForwardingRule(fr), nil
}

// ListLoadBalancers lists load balancers.
func (m *LoadBalancerManager) ListLoadBalancers(ctx context.Context, _filters map[string]string) ([]*cpi.LoadBalancer, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	region := config.Region

	var loadBalancers []*cpi.LoadBalancer

	it := m.client.getForwardingRulesClient().List(ctx, &computepb.ListForwardingRulesRequest{ //nolint:varnamelen
		Project: projectID,
		Region:  region,
	})

	for {
		fr, err := it.Next() //nolint:varnamelen
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "ListLoadBalancers")
		}

		loadBalancers = append(loadBalancers, m.convertForwardingRule(fr))
	}

	return loadBalancers, nil
}

func (m *LoadBalancerManager) convertForwardingRule(fr *computepb.ForwardingRule) *cpi.LoadBalancer { //nolint:varnamelen
	lbType := "external"
	if fr.GetLoadBalancingScheme() == "INTERNAL" {
		lbType = "internal"
	}

	return &cpi.LoadBalancer{
		ID:        fr.GetName(),
		Name:      fr.GetName(),
		Type:      lbType,
		IPAddress: fr.GetIPAddress(),
		Status:    "active",
		State:     cpi.ResourceStateActive,
		CreatedAt: ParseTimestamp(fr.GetCreationTimestamp()),
	}
}
