package gcp

import (
	"cloud.google.com/go/compute/apiv1/computepb"
	"context"
	"errors"
	"fmt"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"google.golang.org/api/iterator"
)

func (m *LoadBalancerManager) ownedEndpoints(ctx context.Context, name string) ([]*lbEndpoint, error) {
	it := m.client.networkEndpointGroupsClient.AggregatedList(ctx, &computepb.AggregatedListNetworkEndpointGroupsRequest{Project: m.client.getConfig().ProjectID})
	result := []*lbEndpoint{}

	for {
		pair, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "LoadBalancer.ListEndpointGroups")
		}

		for _, group := range pair.Value.GetNetworkEndpointGroups() {
			owner, err := parseLBOwner(group.GetDescription(), name, "networkEndpointGroups")
			if err == nil && owner.Endpoint != nil {
				if owner.Endpoint.ID != group.GetName() {
					return nil, errors.New("endpoint metadata/name mismatch")
				}

				result = append(result, owner.Endpoint)
			}
		}
	}

	return result, nil
}
func (m *LoadBalancerManager) DeleteLoadBalancer(ctx context.Context, id string) error {
	service, req, err := m.loadLB(ctx, id)
	if err != nil && !IsNotFound(err) {
		return err
	}

	name := lbName(id)
	if req == nil {
		req = &cpi.CreateLoadBalancerRequest{Name: name, Protocol: "tcp", Port: 80, TargetPort: 80, HealthCheck: &cpi.HealthCheck{Protocol: "tcp", Port: 80}}
	}

	nodes := m.graph(req)
	// Include either proxy architecture when resuming a partially deleted graph.
	if req.Protocol == "tcp" {
		nodes = append(nodes, m.nodeUrlMap(&computepb.UrlMap{Name: proto(name + "-url")}), m.nodeTargetHttpProxy(&computepb.TargetHttpProxy{Name: proto(name + "-proxy")}))
	}

	existing := []lbNode{}

	for _, node := range nodes {
		actual, getErr := node.read(ctx)
		if IsNotFound(WrapGCPError(getErr, "LoadBalancer.Delete.Get")) {
			continue
		}

		if getErr != nil {
			return WrapGCPError(getErr, "LoadBalancer.Delete.Get")
		}

		if _, err = parseLBOwner(lbDescriptionOf(actual), name, node.kind); err != nil {
			return err
		}

		existing = append(existing, node)
	}

	endpoints, err := m.ownedEndpoints(ctx, name)
	if err != nil {
		return err
	}

	if service != nil && len(service.GetBackends()) > 0 {
		service.Backends = nil
		if err = m.patchBackendService(ctx, service); err != nil {
			return err
		}
	}

	for _, endpoint := range endpoints {
		if err = m.deleteEndpoint(ctx, name, endpoint); err != nil {
			return err
		}
	}

	return deleteLBGraph(ctx, existing)
}
func deleteLBGraph(ctx context.Context, nodes []lbNode) error {
	// Forwarding rule first, then its proxy/URL map, backend/health and address.
	order := []string{"forwardingRules", "targetHttpProxies", "targetTcpProxies", "urlMaps", "backendServices", "healthChecks", "addresses"}
	for _, kind := range order {
		for _, node := range nodes {
			if node.kind == kind {
				if err := node.delete(ctx); err != nil {
					return err
				}
			}
		}
	}

	return nil
}
func (m *LoadBalancerManager) UpdateLoadBalancer(ctx context.Context, id string, req *cpi.UpdateLoadBalancerRequest) error {
	if req == nil {
		return errors.New("load-balancer update is required")
	}

	if req.Name != nil && *req.Name != lbName(id) {
		return fmt.Errorf("%w: GCP graph renaming requires replacement", ErrNotImplemented)
	}

	if len(req.SecurityGroups) > 0 {
		return fmt.Errorf("%w: GCP proxy frontends do not support instance security groups", ErrNotImplemented)
	}

	service, spec, err := m.loadLB(ctx, id)
	if err != nil {
		return err
	}

	if req.Tags != nil {
		spec.Tags = req.Tags
	}

	description := lbDescription(spec.Name, "backendServices", spec, nil)
	if len(description) > 2048 {
		return errors.New("load-balancer metadata exceeds GCP description limit")
	}

	service.Description = proto(description)

	return m.patchBackendService(ctx, service)
}
func (m *LoadBalancerManager) ConfigureHealthCheck(ctx context.Context, id string, check *cpi.HealthCheck) error {
	service, spec, err := m.loadLB(ctx, id)
	if err != nil {
		return err
	}

	normalized, err := normalizeLBHealth(check, spec.Protocol, spec.TargetPort)
	if err != nil {
		return err
	}

	endpoints, err := m.ownedEndpoints(ctx, spec.Name)
	if err != nil {
		return err
	}

	oldPort := spec.HealthCheck.Port
	if normalized.Port != oldPort {
		for _, endpoint := range endpoints {
			if err = m.migrateProbePort(ctx, spec.Name, endpoint, oldPort, normalized.Port, true); err != nil {
				return err
			}
		}
	}

	spec.HealthCheck = normalized

	existing, err := m.client.healthChecksClient.Get(ctx, &computepb.GetHealthCheckRequest{Project: m.client.getConfig().ProjectID, HealthCheck: spec.Name + "-hc"})
	if err != nil {
		return WrapGCPError(err, "LoadBalancer.HealthCheck")
	}

	if _, err = parseLBOwner(existing.GetDescription(), spec.Name, "healthChecks"); err != nil {
		return err
	}

	op, err := m.client.healthChecksClient.Patch(ctx, &computepb.PatchHealthCheckRequest{Project: m.client.getConfig().ProjectID, HealthCheck: spec.Name + "-hc", HealthCheckResource: m.healthResource(spec)})
	if err = waitLBOperation(ctx, op, err); err != nil {
		return err
	}

	if normalized.Port != oldPort {
		for _, endpoint := range endpoints {
			if err = m.migrateProbePort(ctx, spec.Name, endpoint, oldPort, normalized.Port, false); err != nil {
				return err
			}
		}
	}

	service.Description = proto(lbDescription(spec.Name, "backendServices", spec, nil))

	return m.patchBackendService(ctx, service)
}
