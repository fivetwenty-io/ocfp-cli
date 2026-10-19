package gcp

import (
	"context"
	"errors"
	"fmt"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"strings"
)

func (m *NetworkManager) nativeLB() *LoadBalancerManager {
	return &LoadBalancerManager{client: m.client}
}
func (m *NetworkManager) CreateLoadBalancer(ctx context.Context, req *cpi.LoadBalancer) (*cpi.LoadBalancer, error) {
	if req == nil {
		return nil, errors.New("load-balancer request is required")
	}

	tags := map[string]string{}

	for _, tag := range req.Tags {
		k, v, ok := strings.Cut(tag, "=")
		if !ok {
			return nil, errors.New("load-balancer tags require key=value")
		}

		tags[k] = v
	}

	scheme := req.Type
	if scheme == "external" || scheme == "" {
		scheme = "internet-facing"
	}
	// Validate supplied backend requests before creating frontend resources.
	normalized, err := normalizeLBRequest(&cpi.CreateLoadBalancerRequest{Name: req.Name, Scheme: scheme, Protocol: req.Protocol, Port: req.Port, TargetPort: req.TargetPort, NetworkID: req.NetworkID, SubnetIDs: req.SubnetIDs, SecurityGroups: req.SecurityGroups, Tags: tags, HealthCheck: req.HealthCheck, Algorithm: req.Algorithm})
	if err != nil {
		return nil, err
	}

	if len(req.Backends) > 0 {
		return nil, fmt.Errorf("%w: create the load balancer, then attach validated backends", ErrNotImplemented)
	}

	return m.nativeLB().CreateLoadBalancer(ctx, normalized)
}
func (m *NetworkManager) GetLoadBalancer(ctx context.Context, id string) (*cpi.LoadBalancer, error) {
	return m.nativeLB().GetLoadBalancer(ctx, id)
}
func (m *NetworkManager) ListLoadBalancers(ctx context.Context, filters map[string]string) ([]*cpi.LoadBalancer, error) {
	return m.nativeLB().ListLoadBalancers(ctx, filters)
}
func (m *NetworkManager) DeleteLoadBalancer(ctx context.Context, id string) error {
	return m.nativeLB().DeleteLoadBalancer(ctx, id)
}
func (m *NetworkManager) UpdateLoadBalancer(ctx context.Context, lb *cpi.LoadBalancer) error {
	if lb == nil {
		return errors.New("load-balancer update is required")
	}

	actual, err := m.GetLoadBalancer(ctx, lb.ID)
	if err != nil {
		return err
	}

	if lb.Algorithm != "" && lb.Algorithm != actual.Algorithm {
		return fmt.Errorf("%w: GCP uses native capacity distribution", ErrNotImplemented)
	}

	if lb.Protocol != actual.Protocol || lb.Port != actual.Port || lb.TargetPort != actual.TargetPort {
		return fmt.Errorf("%w: changing listener requires replacement", ErrNotImplemented)
	}

	if lb.HealthCheck != nil {
		return m.nativeLB().ConfigureHealthCheck(ctx, lb.ID, lb.HealthCheck)
	}

	return nil
}
func (m *NetworkManager) GetBackendPools(ctx context.Context, id string) ([]*cpi.BackendPool, error) {
	backends, err := m.nativeLB().listBackends(ctx, id)
	if err != nil {
		return nil, err
	}

	pool := &cpi.BackendPool{ID: id, Name: lbName(id)}

	for _, b := range backends {
		status := "active"
		if !b.Enabled {
			status = "disabled"
		}

		pool.Members = append(pool.Members, &cpi.BackendMember{ID: b.ID, IPAddress: b.Address, Port: b.Port, TargetPort: b.Port, Weight: b.Weight, Status: status})
	}

	return []*cpi.BackendPool{pool}, nil
}
func (m *NetworkManager) AddBackendMember(ctx context.Context, id string, member *cpi.BackendMember) error {
	if member == nil {
		return errors.New("backend member required")
	}

	port := member.TargetPort
	if port == 0 {
		port = member.Port
	}

	return m.nativeLB().AddBackend(ctx, id, &cpi.Backend{ID: member.ID, Address: member.IPAddress, Port: port, Weight: member.Weight, Enabled: member.Status != "disabled"})
}
func (m *NetworkManager) RemoveBackendMember(ctx context.Context, id, ip string) error {
	return m.nativeLB().RemoveBackend(ctx, id, ip)
}
func (m *NetworkManager) ConfigureHealthCheck(ctx context.Context, id string, check *cpi.HealthCheck) error {
	return m.nativeLB().ConfigureHealthCheck(ctx, id, check)
}
func (m *NetworkManager) GetLoadBalancerHealth(ctx context.Context, id string) (*cpi.HealthStatus, error) {
	return m.nativeLB().GetHealthStatus(ctx, id)
}
