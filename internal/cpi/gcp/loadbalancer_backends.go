package gcp

import (
	"cloud.google.com/go/compute/apiv1/computepb"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"google.golang.org/api/iterator"
	"net"
	"strconv"
	"strings"
)

type lbEndpoint struct {
	ID        string `json:"ID"`
	Address   string `json:"Address"`
	IPAddress string `json:"IPAddress"`
	Instance  string `json:"Instance"`
	Zone      string `json:"Zone"`
	Network   string `json:"Network"`
	Subnet    string `json:"Subnet"`
	Port      int    `json:"Port"`
}

func (m *LoadBalancerManager) endpointURL(e *lbEndpoint) string {
	return scopedResource(m.client.getConfig().ProjectID, e.Zone, "networkEndpointGroups", e.ID)
}
func lbBackendPort(req *cpi.CreateLoadBalancerRequest, backend *cpi.Backend) (int, error) {
	if backend == nil || net.ParseIP(backend.Address) == nil {
		return 0, errors.New("backend requires a valid IP address")
	}

	if backend.Weight != 0 && backend.Weight != 1 {
		return 0, fmt.Errorf("%w: GCP endpoint weights other than one are unsupported", ErrNotImplemented)
	}

	port := backend.Port
	if port == 0 {
		port = req.TargetPort
	}

	if port < 1 || port > 65535 {
		return 0, errors.New("invalid backend port")
	}

	return port, nil
}
func (m *LoadBalancerManager) resolveEndpoint(ctx context.Context, req *cpi.CreateLoadBalancerRequest, backend *cpi.Backend) (*lbEndpoint, error) {
	port, err := lbBackendPort(req, backend)
	if err != nil {
		return nil, err
	}

	project := m.client.getConfig().ProjectID
	it := m.client.instancesClient.AggregatedList(ctx, &computepb.AggregatedListInstancesRequest{Project: project})

	var found *lbEndpoint

	for {
		pair, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "LoadBalancer.ResolveBackend")
		}

		for _, vm := range pair.Value.GetInstances() {
			for _, nic := range vm.GetNetworkInterfaces() {
				if canonicalURL(nic.GetNetwork()) != canonicalURL(FormatNetworkURL(m.client.getConfig().GetNetworkProject(), req.NetworkID)) {
					continue
				}

				matches := nic.GetNetworkIP() == backend.Address
				for _, access := range nic.GetAccessConfigs() {
					matches = matches || access.GetNatIP() == backend.Address
				}

				if !matches {
					continue
				}

				if found != nil {
					return nil, errors.New("backend IP maps to multiple instance interfaces")
				}

				digest := sha256.Sum256([]byte(backend.Address + ":" + strconv.Itoa(port)))
				zone := ExtractZoneFromURL(vm.GetZone())
				found = &lbEndpoint{ID: fmt.Sprintf("%s-%x", req.Name, digest[:6]), Address: backend.Address, IPAddress: nic.GetNetworkIP(), Instance: scopedResource(project, zone, "instances", vm.GetName()), Zone: zone, Network: nic.GetNetwork(), Subnet: nic.GetSubnetwork(), Port: port}
			}
		}
	}

	if found == nil {
		return nil, errors.New("backend IP is not an instance in the load-balancer network")
	}

	if found.Zone == "" || found.Subnet == "" {
		return nil, errors.New("backend zone and subnet required")
	}

	return found, nil
}
func (m *LoadBalancerManager) getEndpoint(ctx context.Context, name, url string) (*computepb.NetworkEndpointGroup, *lbEndpoint, error) {
	project, zone, id := resourceScope(m.client.getConfig().ProjectID, m.client.getConfig().Zone, url)
	if project != m.client.getConfig().ProjectID {
		return nil, nil, errors.New("refusing foreign-project endpoint")
	}

	group, err := m.client.networkEndpointGroupsClient.Get(ctx, &computepb.GetNetworkEndpointGroupRequest{Project: project, Zone: zone, NetworkEndpointGroup: id})
	if err != nil {
		return nil, nil, WrapGCPError(err, "LoadBalancer.GetEndpoint")
	}

	owner, err := parseLBOwner(group.GetDescription(), name, "networkEndpointGroups")
	if err != nil {
		return nil, nil, err
	}

	if owner.Endpoint == nil || owner.Endpoint.ID != id || owner.Endpoint.Zone != zone || owner.Endpoint.Port < 1 || owner.Endpoint.Port > 65535 {
		return nil, nil, errors.New("endpoint identity mismatch")
	}

	return group, owner.Endpoint, nil
}
func (m *LoadBalancerManager) AddBackend(ctx context.Context, id string, backend *cpi.Backend) error {
	service, req, err := m.loadLB(ctx, id)
	if err != nil {
		return err
	}

	endpoint, err := m.resolveEndpoint(ctx, req, backend)
	if err != nil {
		return err
	}

	group, _, getErr := m.getEndpoint(ctx, req.Name, m.endpointURL(endpoint))
	if getErr != nil && !IsNotFound(getErr) {
		return getErr
	}

	if group == nil {
		group = &computepb.NetworkEndpointGroup{Name: proto(endpoint.ID), Description: proto(lbDescription(req.Name, "networkEndpointGroups", nil, endpoint)), NetworkEndpointType: proto("GCE_VM_IP_PORT"), Network: proto(endpoint.Network), Subnetwork: proto(endpoint.Subnet)}

		op, insertErr := m.client.networkEndpointGroupsClient.Insert(ctx, &computepb.InsertNetworkEndpointGroupRequest{Project: m.client.getConfig().ProjectID, Zone: endpoint.Zone, NetworkEndpointGroupResource: group})
		if err = waitLBOperation(ctx, op, insertErr); err != nil {
			return err
		}
	}

	if err = m.ensureEndpointFirewall(ctx, req, endpoint); err != nil {
		return err
	}

	found := false

	for _, existing := range service.GetBackends() {
		if canonicalURL(existing.GetGroup()) == m.endpointURL(endpoint) {
			found = true
		}
	}

	if !found {
		native := &computepb.Backend{Group: proto(m.endpointURL(endpoint)), BalancingMode: proto("CONNECTION"), MaxConnectionsPerEndpoint: proto(int32(1000))}
		if req.Protocol == "http" {
			native.BalancingMode = proto("RATE")
			native.MaxConnectionsPerEndpoint = nil
			native.MaxRatePerEndpoint = proto(float32(100))
		}

		service.Backends = append(service.Backends, native)
		if err = m.patchBackendService(ctx, service); err != nil {
			return err
		}
	}

	return m.setEndpointEnabled(ctx, endpoint, backend.Enabled)
}
func (m *LoadBalancerManager) patchBackendService(ctx context.Context, service *computepb.BackendService) error {
	op, err := m.client.backendServicesClient.Update(ctx, &computepb.UpdateBackendServiceRequest{Project: m.client.getConfig().ProjectID, BackendService: service.GetName(), BackendServiceResource: service})

	return waitLBOperation(ctx, op, err)
}
func (m *LoadBalancerManager) endpointEnabled(ctx context.Context, e *lbEndpoint) (bool, error) {
	it := m.client.networkEndpointGroupsClient.ListNetworkEndpoints(ctx, &computepb.ListNetworkEndpointsNetworkEndpointGroupsRequest{Project: m.client.getConfig().ProjectID, Zone: e.Zone, NetworkEndpointGroup: e.ID, NetworkEndpointGroupsListEndpointsRequestResource: &computepb.NetworkEndpointGroupsListEndpointsRequest{}})
	enabled := false

	for {
		item, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return enabled, nil
		}

		if err != nil {
			return false, WrapGCPError(err, "LoadBalancer.ListEndpoints")
		}

		value := item.GetNetworkEndpoint()
		if value.GetIpAddress() != e.IPAddress || int(value.GetPort()) != e.Port {
			return false, errors.New("endpoint group contains a foreign endpoint")
		}

		enabled = true
	}
}

//nolint:gosec // Endpoint ports are validated on creation and metadata reads.
func (m *LoadBalancerManager) setEndpointEnabled(ctx context.Context, e *lbEndpoint, enabled bool) error {
	current, err := m.endpointEnabled(ctx, e)
	if err != nil {
		return err
	}

	if current == enabled {
		return nil
	}

	native := &computepb.NetworkEndpoint{Instance: proto(ExtractNameFromURL(e.Instance)), IpAddress: proto(e.IPAddress), Port: proto(int32(e.Port)) /* validated endpoint port */}
	if enabled {
		op, err := m.client.networkEndpointGroupsClient.AttachNetworkEndpoints(ctx, &computepb.AttachNetworkEndpointsNetworkEndpointGroupRequest{Project: m.client.getConfig().ProjectID, Zone: e.Zone, NetworkEndpointGroup: e.ID, NetworkEndpointGroupsAttachEndpointsRequestResource: &computepb.NetworkEndpointGroupsAttachEndpointsRequest{NetworkEndpoints: []*computepb.NetworkEndpoint{native}}})

		return waitLBOperation(ctx, op, err)
	}

	op, err := m.client.networkEndpointGroupsClient.DetachNetworkEndpoints(ctx, &computepb.DetachNetworkEndpointsNetworkEndpointGroupRequest{Project: m.client.getConfig().ProjectID, Zone: e.Zone, NetworkEndpointGroup: e.ID, NetworkEndpointGroupsDetachEndpointsRequestResource: &computepb.NetworkEndpointGroupsDetachEndpointsRequest{NetworkEndpoints: []*computepb.NetworkEndpoint{native}}})

	return waitLBOperation(ctx, op, err)
}
func (m *LoadBalancerManager) findBackend(ctx context.Context, id, backendID string) (*computepb.BackendService, *lbEndpoint, error) {
	service, req, err := m.loadLB(ctx, id)
	if err != nil {
		return nil, nil, err
	}

	for _, backend := range service.GetBackends() {
		_, endpoint, err := m.getEndpoint(ctx, req.Name, backend.GetGroup())
		if err != nil {
			return nil, nil, err
		}

		if endpoint.ID == backendID || endpoint.Address == backendID {
			return service, endpoint, nil
		}
	}

	return service, nil, &cpi.ProviderError{Provider: "gcp", Code: "NotFound", Message: "backend not found"}
}
func (m *LoadBalancerManager) EnableBackend(ctx context.Context, id, backendID string) error {
	_, e, err := m.findBackend(ctx, id, backendID)
	if err != nil {
		return err
	}

	return m.setEndpointEnabled(ctx, e, true)
}
func (m *LoadBalancerManager) DisableBackend(ctx context.Context, id, backendID string) error {
	_, e, err := m.findBackend(ctx, id, backendID)
	if err != nil {
		return err
	}

	return m.setEndpointEnabled(ctx, e, false)
}
func (m *LoadBalancerManager) RemoveBackend(ctx context.Context, id, backendID string) error {
	service, e, err := m.findBackend(ctx, id, backendID)
	if IsNotFound(err) {
		if service == nil {
			return nil
		}

		endpoints, listErr := m.ownedEndpoints(ctx, lbName(id))
		if listErr != nil {
			return listErr
		}

		for _, orphan := range endpoints {
			if orphan.ID == backendID || orphan.Address == backendID {
				return m.deleteEndpoint(ctx, lbName(id), orphan)
			}
		}

		return nil
	}

	if err != nil {
		return err
	}

	kept := []*computepb.Backend{}

	for _, b := range service.GetBackends() {
		if canonicalURL(b.GetGroup()) != m.endpointURL(e) {
			kept = append(kept, b)
		}
	}

	service.Backends = kept
	if err = m.patchBackendService(ctx, service); err != nil {
		return err
	}

	return m.deleteEndpoint(ctx, lbName(id), e)
}
func (m *LoadBalancerManager) listBackends(ctx context.Context, id string) ([]*cpi.Backend, error) {
	service, req, err := m.loadLB(ctx, id)
	if err != nil {
		return nil, err
	}

	result := []*cpi.Backend{}

	for _, b := range service.GetBackends() {
		_, e, err := m.getEndpoint(ctx, req.Name, b.GetGroup())
		if err != nil {
			return nil, err
		}

		enabled, err := m.endpointEnabled(ctx, e)
		if err != nil {
			return nil, err
		}

		result = append(result, &cpi.Backend{ID: e.ID, Address: e.Address, Port: e.Port, Weight: 1, Enabled: enabled})
	}

	return result, nil
}
func (m *LoadBalancerManager) GetHealthStatus(ctx context.Context, id string) (*cpi.HealthStatus, error) {
	service, req, err := m.loadLB(ctx, id)
	if err != nil {
		return nil, err
	}

	result := &cpi.HealthStatus{LoadBalancerID: id, Backends: map[string]string{}}

	for _, b := range service.GetBackends() {
		_, e, err := m.getEndpoint(ctx, req.Name, b.GetGroup())
		if err != nil {
			return nil, err
		}

		enabled, err := m.endpointEnabled(ctx, e)
		if err != nil {
			return nil, err
		}

		status := "disabled"

		if enabled {
			health, err := m.client.backendServicesClient.GetHealth(ctx, &computepb.GetHealthBackendServiceRequest{Project: m.client.getConfig().ProjectID, BackendService: service.GetName(), ResourceGroupReferenceResource: &computepb.ResourceGroupReference{Group: b.Group}})
			if err != nil {
				return nil, WrapGCPError(err, "LoadBalancer.Health")
			}

			status = "unknown"

			for _, v := range health.GetHealthStatus() {
				if v.GetIpAddress() == e.IPAddress && int(v.GetPort()) == e.Port {
					status = strings.ToLower(v.GetHealthState())
				}
			}
		}

		result.Backends[e.ID] = status

		result.Total++

		switch status {
		case "healthy":
			result.Healthy++
		case "unhealthy":
			result.Unhealthy++
		}
	}

	return result, nil
}
