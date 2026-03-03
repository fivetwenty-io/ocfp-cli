package azure

import (
	"context"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v5"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// lbSchemeInternal is the scheme identifier for internal load balancers.
const lbSchemeInternal = "internal"

// CreateLoadBalancer creates a new Azure Load Balancer.
//
//nolint:funlen // Azure LB creation requires constructing multiple nested resource configs
func (m *LoadBalancerManager) CreateLoadBalancer(ctx context.Context, req *cpi.CreateLoadBalancerRequest) (*cpi.LoadBalancer, error) {
	if req == nil {
		return nil, ErrInvalidRequest
	}

	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	// Ensure resource group exists
	err = m.client.EnsureResourceGroup(ctx)
	if err != nil {
		return nil, err
	}

	// Determine if this is an internal or external load balancer
	isInternal := req.Scheme == lbSchemeInternal

	// Create frontend IP configuration
	frontendName := req.Name + "-frontend"

	var frontendConfig *armnetwork.FrontendIPConfiguration

	if isInternal {
		// Internal load balancer - use private IP from subnet
		if len(req.SubnetIDs) == 0 {
			return nil, ErrSubnetIDRequired
		}

		frontendConfig = &armnetwork.FrontendIPConfiguration{
			Name: to.Ptr(frontendName),
			Properties: &armnetwork.FrontendIPConfigurationPropertiesFormat{
				PrivateIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodDynamic),
				Subnet: &armnetwork.Subnet{
					ID: to.Ptr(req.SubnetIDs[0]),
				},
			},
		}
	} else {
		// External load balancer - create public IP first
		publicIPName := req.Name + "-pip"

		publicIP, err := m.createPublicIPForLB(ctx, publicIPName, req.Tags)
		if err != nil {
			return nil, fmt.Errorf("failed to create public IP for load balancer: %w", err)
		}

		frontendConfig = &armnetwork.FrontendIPConfiguration{
			Name: to.Ptr(frontendName),
			Properties: &armnetwork.FrontendIPConfigurationPropertiesFormat{
				PublicIPAddress: &armnetwork.PublicIPAddress{
					ID: publicIP.ID,
				},
			},
		}
	}

	// Create backend address pool
	backendPoolName := req.Name + "-backend"
	backendPool := &armnetwork.BackendAddressPool{
		Name: to.Ptr(backendPoolName),
	}

	// Create health probe
	probeName := req.Name + "-probe"
	probe := &armnetwork.Probe{
		Name: to.Ptr(probeName),
		Properties: &armnetwork.ProbePropertiesFormat{
			Protocol:          to.Ptr(armnetwork.ProbeProtocolTCP),
			Port:              to.Ptr(int32(80)), //nolint:mnd
			IntervalInSeconds: to.Ptr(int32(15)), //nolint:mnd
			NumberOfProbes:    to.Ptr(int32(2)),  //nolint:mnd
		},
	}

	// Create load balancing rule
	ruleName := req.Name + "-rule"
	rule := &armnetwork.LoadBalancingRule{
		Name: to.Ptr(ruleName),
		Properties: &armnetwork.LoadBalancingRulePropertiesFormat{
			Protocol:             to.Ptr(armnetwork.TransportProtocolTCP),
			FrontendPort:         to.Ptr(int32(80)), //nolint:mnd
			BackendPort:          to.Ptr(int32(80)), //nolint:mnd
			EnableFloatingIP:     to.Ptr(false),
			IdleTimeoutInMinutes: to.Ptr(int32(4)), //nolint:mnd
			FrontendIPConfiguration: &armnetwork.SubResource{
				ID: to.Ptr(fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/frontendIPConfigurations/%s",
					m.client.getSubscriptionID(), m.client.getResourceGroup(), req.Name, frontendName)),
			},
			BackendAddressPool: &armnetwork.SubResource{
				ID: to.Ptr(fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/backendAddressPools/%s",
					m.client.getSubscriptionID(), m.client.getResourceGroup(), req.Name, backendPoolName)),
			},
			Probe: &armnetwork.SubResource{
				ID: to.Ptr(fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/probes/%s",
					m.client.getSubscriptionID(), m.client.getResourceGroup(), req.Name, probeName)),
			},
		},
	}

	// Prepare load balancer parameters
	lbParams := armnetwork.LoadBalancer{
		Location: to.Ptr(m.client.getLocation()),
		SKU: &armnetwork.LoadBalancerSKU{
			Name: to.Ptr(armnetwork.LoadBalancerSKUNameStandard),
			Tier: to.Ptr(armnetwork.LoadBalancerSKUTierRegional),
		},
		Properties: &armnetwork.LoadBalancerPropertiesFormat{
			FrontendIPConfigurations: []*armnetwork.FrontendIPConfiguration{frontendConfig},
			BackendAddressPools:      []*armnetwork.BackendAddressPool{backendPool},
			Probes:                   []*armnetwork.Probe{probe},
			LoadBalancingRules:       []*armnetwork.LoadBalancingRule{rule},
		},
		Tags: BuildTags(MergeTags(m.client.config.DefaultTags, req.Tags)),
	}

	poller, err := m.client.loadBalancersClient.BeginCreateOrUpdate(
		ctx,
		m.client.getResourceGroup(),
		req.Name,
		lbParams,
		nil,
	)
	if err != nil {
		return nil, WrapAzureError(err, "CreateLoadBalancer")
	}

	result, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, WrapAzureError(err, "CreateLoadBalancer")
	}

	logger.Infow("Created load balancer", "name", req.Name, "type", req.Type)

	return m.lbToLoadBalancer(&result.LoadBalancer), nil
}

// createLoadBalancerFromConfig creates a load balancer from a cpi.LoadBalancer config.
func (m *LoadBalancerManager) createLoadBalancerFromConfig(ctx context.Context, config *cpi.LoadBalancer) (*cpi.LoadBalancer, error) {
	if config == nil {
		return nil, ErrInvalidRequest
	}

	req := &cpi.CreateLoadBalancerRequest{
		Name:      config.Name,
		Type:      config.Type,
		NetworkID: config.NetworkID,
		SubnetIDs: config.SubnetIDs,
	}

	if config.Type == lbSchemeInternal {
		req.Scheme = lbSchemeInternal
	} else {
		req.Scheme = "internet-facing"
	}

	return m.CreateLoadBalancer(ctx, req)
}

// GetLoadBalancer retrieves a load balancer by ID or name.
func (m *LoadBalancerManager) GetLoadBalancer(ctx context.Context, id string) (*cpi.LoadBalancer, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	name := ExtractResourceName(id)

	result, err := m.client.loadBalancersClient.Get(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return nil, WrapAzureError(err, "GetLoadBalancer")
	}

	return m.lbToLoadBalancer(&result.LoadBalancer), nil
}

// ListLoadBalancers lists all load balancers.
func (m *LoadBalancerManager) ListLoadBalancers(ctx context.Context, filters map[string]string) ([]*cpi.LoadBalancer, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	pager := m.client.loadBalancersClient.NewListPager(m.client.getResourceGroup(), nil)

	var loadBalancers []*cpi.LoadBalancer

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, WrapAzureError(err, "ListLoadBalancers")
		}

		for _, lb := range page.Value {
			loadBalancer := m.lbToLoadBalancer(lb)
			if matchesLBFilters(loadBalancer.Tags, filters) {
				loadBalancers = append(loadBalancers, loadBalancer)
			}
		}
	}

	return loadBalancers, nil
}

// UpdateLoadBalancer updates a load balancer.
func (m *LoadBalancerManager) UpdateLoadBalancer(ctx context.Context, id string, req *cpi.UpdateLoadBalancerRequest) error { //nolint:varnamelen
	if req == nil {
		return ErrInvalidRequest
	}

	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	name := ExtractResourceName(id)
	//nolint:varnamelen // lb is clear in context
	// Get current load balancer
	lb, err := m.client.loadBalancersClient.Get(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return WrapAzureError(err, "UpdateLoadBalancer.Get")
	}

	// Update tags if provided
	if req.Tags != nil {
		lb.Tags = BuildTags(req.Tags)
	}

	poller, err := m.client.loadBalancersClient.BeginCreateOrUpdate(
		ctx,
		m.client.getResourceGroup(),
		name,
		lb.LoadBalancer,
		nil,
	)
	if err != nil {
		return WrapAzureError(err, "UpdateLoadBalancer")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "UpdateLoadBalancer")
	}

	logger.Infow("Updated load balancer", "name", name)

	return nil
}

// DeleteLoadBalancer deletes a load balancer.
//
//nolint:varnamelen // id is clear in context
func (m *LoadBalancerManager) DeleteLoadBalancer(ctx context.Context, id string) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	name := ExtractResourceName(id)

	poller, err := m.client.loadBalancersClient.BeginDelete(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return WrapAzureError(err, "DeleteLoadBalancer")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "DeleteLoadBalancer")
	}

	logger.Infow("Deleted load balancer", "name", name)

	return nil
}

// AddBackend adds a backend to a load balancer.
func (m *LoadBalancerManager) AddBackend(ctx context.Context, lbID string, backend *cpi.Backend) error {
	if backend == nil {
		return ErrInvalidRequest
	}

	// In Azure, backends are managed through NIC IP configurations
	// This is a simplified implementation
	logger.Warnw("AddBackend requires NIC-level configuration in Azure", "lb", lbID, "backend", backend.Address)

	return ErrNotImplemented
}

// RemoveBackend removes a backend from a load balancer.
func (m *LoadBalancerManager) RemoveBackend(ctx context.Context, lbID string, backendID string) error {
	logger.Warnw("RemoveBackend requires NIC-level configuration in Azure", "lb", lbID, "backend", backendID)

	return ErrNotImplemented
}

// EnableBackend enables a backend in a load balancer.
func (m *LoadBalancerManager) EnableBackend(ctx context.Context, lbID string, backendID string) error {
	// Azure doesn't have a direct "enable/disable" concept for backends
	return ErrNotImplemented
}

// DisableBackend disables a backend in a load balancer.
func (m *LoadBalancerManager) DisableBackend(ctx context.Context, lbID string, backendID string) error {
	return ErrNotImplemented
}

// ConfigureHealthCheck configures a health check for a load balancer.
func (m *LoadBalancerManager) ConfigureHealthCheck(ctx context.Context, lbID string, check *cpi.HealthCheck) error {
	if check == nil {
		return ErrInvalidRequest
	}

	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	name := ExtractResourceName(lbID)
	//nolint:varnamelen // lb is clear in context
	// Get current load balancer
	lb, err := m.client.loadBalancersClient.Get(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return WrapAzureError(err, "ConfigureHealthCheck.Get")
	}

	// Update or add probe
	if lb.Properties == nil || lb.Properties.Probes == nil || len(lb.Properties.Probes) == 0 {
		return fmt.Errorf("%w: %s", ErrLoadBalancerNoProbes, name)
	}

	// Update the first probe
	probe := lb.Properties.Probes[0]

	// Map protocol
	switch check.Protocol {
	case "http":
		probe.Properties.Protocol = to.Ptr(armnetwork.ProbeProtocolHTTP)
		probe.Properties.RequestPath = to.Ptr(check.Path)
	case "https":
		probe.Properties.Protocol = to.Ptr(armnetwork.ProbeProtocolHTTPS)
		probe.Properties.RequestPath = to.Ptr(check.Path)
	default:
		probe.Properties.Protocol = to.Ptr(armnetwork.ProbeProtocolTCP)
	}

	probe.Properties.Port = to.Ptr(int32(check.Port))                         //nolint:gosec // port values are within int32 range
	probe.Properties.IntervalInSeconds = to.Ptr(int32(check.Interval))        //nolint:gosec // interval is a small config value
	probe.Properties.NumberOfProbes = to.Ptr(int32(check.UnhealthyThreshold)) //nolint:gosec // threshold is a small config value

	poller, err := m.client.loadBalancersClient.BeginCreateOrUpdate(
		ctx,
		m.client.getResourceGroup(),
		name,
		lb.LoadBalancer,
		nil,
	)
	if err != nil {
		return WrapAzureError(err, "ConfigureHealthCheck")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "ConfigureHealthCheck")
	}

	logger.Infow("Configured health check", "lb", name, "protocol", check.Protocol, "port", check.Port)

	return nil
}

// GetHealthStatus retrieves health status of a load balancer.
func (m *LoadBalancerManager) GetHealthStatus(ctx context.Context, lbID string) (*cpi.HealthStatus, error) {
	// Azure Load Balancer health status requires metrics API
	// This is a simplified implementation
	return &cpi.HealthStatus{
		LoadBalancerID: lbID,
		Healthy:        0,
		Unhealthy:      0,
		Total:          0,
		Backends:       make(map[string]string),
	}, nil
}

// Helper functions

func (m *LoadBalancerManager) createPublicIPForLB(ctx context.Context, name string, tags map[string]string) (*armnetwork.PublicIPAddress, error) {
	publicIPParams := armnetwork.PublicIPAddress{
		Location: to.Ptr(m.client.getLocation()),
		Properties: &armnetwork.PublicIPAddressPropertiesFormat{
			PublicIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodStatic),
			PublicIPAddressVersion:   to.Ptr(armnetwork.IPVersionIPv4),
		},
		SKU: &armnetwork.PublicIPAddressSKU{
			Name: to.Ptr(armnetwork.PublicIPAddressSKUNameStandard),
			Tier: to.Ptr(armnetwork.PublicIPAddressSKUTierRegional),
		},
		Tags: BuildTags(MergeTags(m.client.config.DefaultTags, tags)),
	}

	poller, err := m.client.publicIPAddressesClient.BeginCreateOrUpdate(
		ctx,
		m.client.getResourceGroup(),
		name,
		publicIPParams,
		nil,
	)
	if err != nil {
		return nil, WrapAzureError(err, "createPublicIPForLB")
	}

	result, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, WrapAzureError(err, "createPublicIPForLB")
	}

	return &result.PublicIPAddress, nil
}

func (m *LoadBalancerManager) lbToLoadBalancer(lb *armnetwork.LoadBalancer) *cpi.LoadBalancer { //nolint:varnamelen
	if lb == nil {
		return nil
	}

	loadBalancer := &cpi.LoadBalancer{
		ID:        DerefString(lb.ID),
		Name:      DerefString(lb.Name),
		Tags:      tagsToSlice(ExtractTags(lb.Tags)),
		CreatedAt: time.Now(),
	}

	if lb.Properties == nil {
		return loadBalancer
	}

	loadBalancer.State = MapProvisioningStateToResourceState(string(*lb.Properties.ProvisioningState))
	m.populateLBFrontend(loadBalancer, lb.Properties.FrontendIPConfigurations)
	m.populateLBBackends(loadBalancer, lb.Properties.BackendAddressPools)
	m.populateLBHealthCheck(loadBalancer, lb.Properties.Probes)

	return loadBalancer
}

// populateLBFrontend extracts load balancer type and IP from frontend configuration.
func (m *LoadBalancerManager) populateLBFrontend(lb *cpi.LoadBalancer, frontends []*armnetwork.FrontendIPConfiguration) { //nolint:varnamelen
	if len(frontends) == 0 {
		return
	}

	frontend := frontends[0]
	if frontend.Properties == nil {
		return
	}

	if frontend.Properties.PublicIPAddress != nil {
		lb.Type = "external"
	} else if frontend.Properties.PrivateIPAddress != nil {
		lb.Type = lbSchemeInternal
		lb.IPAddress = DerefString(frontend.Properties.PrivateIPAddress)
	}
}

// populateLBBackends extracts backend addresses from backend address pools.
func (m *LoadBalancerManager) populateLBBackends(lb *cpi.LoadBalancer, pools []*armnetwork.BackendAddressPool) { //nolint:varnamelen
	for _, pool := range pools {
		if pool.Properties == nil {
			continue
		}

		for _, addr := range pool.Properties.LoadBalancerBackendAddresses {
			if addr.Properties == nil || addr.Properties.IPAddress == nil {
				continue
			}

			lb.Backends = append(lb.Backends, &cpi.Backend{
				ID:      DerefString(addr.Name),
				Name:    DerefString(addr.Name),
				Address: DerefString(addr.Properties.IPAddress),
				Enabled: true,
			})
		}
	}
}

// populateLBHealthCheck extracts health check configuration from probes.
func (m *LoadBalancerManager) populateLBHealthCheck(lb *cpi.LoadBalancer, probes []*armnetwork.Probe) { //nolint:varnamelen
	if len(probes) == 0 {
		return
	}

	probe := probes[0]
	if probe.Properties == nil {
		return
	}

	lb.HealthCheck = &cpi.HealthCheck{
		Protocol:           string(*probe.Properties.Protocol),
		Port:               int(DerefInt32(probe.Properties.Port)),
		Interval:           int(DerefInt32(probe.Properties.IntervalInSeconds)),
		UnhealthyThreshold: int(DerefInt32(probe.Properties.NumberOfProbes)),
	}

	if probe.Properties.RequestPath != nil {
		lb.HealthCheck.Path = DerefString(probe.Properties.RequestPath)
	}
}

func tagsToSlice(tags map[string]string) []string {
	if tags == nil {
		return nil
	}

	var result []string
	for k, v := range tags {
		result = append(result, k+"="+v)
	}

	return result
}

func matchesLBFilters(tags []string, filters map[string]string) bool {
	if len(filters) == 0 {
		return true
	}

	// Convert tag slice to map for easier matching
	tagMap := make(map[string]string)

	for _, tag := range tags {
		for i, c := range tag {
			if c == '=' {
				tagMap[tag[:i]] = tag[i+1:]

				break
			}
		}
	}

	for key, value := range filters {
		if tagValue, ok := tagMap[key]; !ok || tagValue != value {
			return false
		}
	}

	return true
}
