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

// CreateNetwork creates a new virtual network (VNet).
func (m *NetworkManager) CreateNetwork(ctx context.Context, req *cpi.NetworkRequest) (*cpi.Network, error) {
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

	// Prepare VNet parameters
	vnetParams := armnetwork.VirtualNetwork{
		Location: to.Ptr(m.client.getLocation()),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{
				AddressPrefixes: []*string{to.Ptr(req.CIDR)},
			},
		},
		Tags: BuildTags(MergeTags(m.client.config.DefaultTags, req.Tags)),
	}

	// Add DNS servers if specified
	if len(req.DNSServers) > 0 {
		dnsServers := make([]*string, len(req.DNSServers))
		for i, dns := range req.DNSServers {
			dnsServers[i] = to.Ptr(dns)
		}

		vnetParams.Properties.DhcpOptions = &armnetwork.DhcpOptions{
			DNSServers: dnsServers,
		}
	}

	// Create the VNet
	poller, err := m.client.virtualNetworksClient.BeginCreateOrUpdate(
		ctx,
		m.client.getResourceGroup(),
		req.Name,
		vnetParams,
		nil,
	)
	if err != nil {
		return nil, WrapAzureError(err, "CreateNetwork")
	}

	// Wait for completion
	result, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, WrapAzureError(err, "CreateNetwork")
	}

	logger.Infow("Created virtual network", "name", req.Name, "cidr", req.CIDR)

	return m.vnetToNetwork(&result.VirtualNetwork), nil
}

// GetNetwork retrieves a virtual network by ID or name.
func (m *NetworkManager) GetNetwork(ctx context.Context, id string) (*cpi.Network, error) { //nolint:varnamelen // id is clear in context
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	// Extract name from ID if needed
	name := ExtractResourceName(id)

	result, err := m.client.virtualNetworksClient.Get(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return nil, WrapAzureError(err, "GetNetwork")
	}

	return m.vnetToNetwork(&result.VirtualNetwork), nil
}

// ListNetworks lists all virtual networks.
func (m *NetworkManager) ListNetworks(ctx context.Context, filters map[string]string) ([]*cpi.Network, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	pager := m.client.virtualNetworksClient.NewListPager(m.client.getResourceGroup(), nil)

	var networks []*cpi.Network

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, WrapAzureError(err, "ListNetworks")
		}

		for _, vnet := range page.Value {
			network := m.vnetToNetwork(vnet)
			if matchesFilters(network.Tags, filters) {
				networks = append(networks, network)
			}
		}
	}

	return networks, nil
}

// DeleteNetwork deletes a virtual network.
func (m *NetworkManager) DeleteNetwork(ctx context.Context, id string) error { //nolint:varnamelen // id is clear in context
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	name := ExtractResourceName(id)

	poller, err := m.client.virtualNetworksClient.BeginDelete(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return WrapAzureError(err, "DeleteNetwork")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "DeleteNetwork")
	}

	logger.Infow("Deleted virtual network", "name", name)

	return nil
}

// CreateSubnet creates a new subnet.
func (m *NetworkManager) CreateSubnet(ctx context.Context, req *cpi.SubnetRequest) (*cpi.Subnet, error) {
	if req == nil {
		return nil, ErrInvalidRequest
	}

	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	// Extract VNet name from network ID
	vnetName := ExtractResourceName(req.NetworkID)

	subnetParams := armnetwork.Subnet{
		Properties: &armnetwork.SubnetPropertiesFormat{
			AddressPrefix: to.Ptr(req.CIDR),
		},
	}

	poller, err := m.client.subnetsClient.BeginCreateOrUpdate(
		ctx,
		m.client.getResourceGroup(),
		vnetName,
		req.Name,
		subnetParams,
		nil,
	)
	if err != nil {
		return nil, WrapAzureError(err, "CreateSubnet")
	}

	result, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, WrapAzureError(err, "CreateSubnet")
	}

	logger.Infow("Created subnet", "name", req.Name, "vnet", vnetName, "cidr", req.CIDR)

	return m.subnetToSubnet(&result.Subnet, vnetName), nil
}

// GetSubnet retrieves a subnet by ID.
func (m *NetworkManager) GetSubnet(ctx context.Context, id string) (*cpi.Subnet, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	// Parse subnet ID to get VNet and subnet names
	resourceID, err := ParseResourceID(id)
	if err != nil {
		// Try to extract from simple format
		return nil, fmt.Errorf("%w: %s", ErrInvalidSubnetIDFormat, id)
	}

	// For subnet IDs, the resource name contains vnet/subnet
	parts := splitSubnetPath(resourceID.ResourceName)
	if len(parts) != 2 { //nolint:mnd
		return nil, fmt.Errorf("%w: %s", ErrInvalidSubnetIDFormat, id)
	}

	vnetName := parts[0]
	subnetName := parts[1]

	result, err := m.client.subnetsClient.Get(ctx, m.client.getResourceGroup(), vnetName, subnetName, nil)
	if err != nil {
		return nil, WrapAzureError(err, "GetSubnet")
	}

	return m.subnetToSubnet(&result.Subnet, vnetName), nil
}

// ListSubnets lists all subnets in a virtual network.
func (m *NetworkManager) ListSubnets(ctx context.Context, networkID string) ([]*cpi.Subnet, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	vnetName := ExtractResourceName(networkID)

	pager := m.client.subnetsClient.NewListPager(m.client.getResourceGroup(), vnetName, nil)

	var subnets []*cpi.Subnet

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, WrapAzureError(err, "ListSubnets")
		}

		for _, subnet := range page.Value {
			subnets = append(subnets, m.subnetToSubnet(subnet, vnetName))
		}
	}

	return subnets, nil
}

// DeleteSubnet deletes a subnet.
func (m *NetworkManager) DeleteSubnet(ctx context.Context, id string) error { //nolint:varnamelen // id is clear in context
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	// Parse subnet ID
	resourceID, err := ParseResourceID(id)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidSubnetIDFormat, id)
	}

	parts := splitSubnetPath(resourceID.ResourceName)
	if len(parts) != 2 { //nolint:mnd
		return fmt.Errorf("%w: %s", ErrInvalidSubnetIDFormat, id)
	}

	vnetName := parts[0]
	subnetName := parts[1]

	poller, err := m.client.subnetsClient.BeginDelete(ctx, m.client.getResourceGroup(), vnetName, subnetName, nil)
	if err != nil {
		return WrapAzureError(err, "DeleteSubnet")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "DeleteSubnet")
	}

	logger.Infow("Deleted subnet", "name", subnetName, "vnet", vnetName)

	return nil
}

// CreateSecurityGroup creates a new network security group.
func (m *NetworkManager) CreateSecurityGroup(ctx context.Context, req *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	return m.client.security.CreateSecurityGroup(ctx, req)
}

// GetSecurityGroup retrieves a network security group by ID.
func (m *NetworkManager) GetSecurityGroup(ctx context.Context, id string) (*cpi.SecurityGroup, error) {
	return m.client.security.GetSecurityGroup(ctx, id)
}

// ListSecurityGroups lists all network security groups.
func (m *NetworkManager) ListSecurityGroups(ctx context.Context, filters map[string]string) ([]*cpi.SecurityGroup, error) {
	return m.client.security.ListSecurityGroups(ctx, filters)
}

// DeleteSecurityGroup deletes a network security group.
func (m *NetworkManager) DeleteSecurityGroup(ctx context.Context, id string) error {
	return m.client.security.DeleteSecurityGroup(ctx, id)
}

// CreatePublicIP creates a new public IP address.
//
//nolint:funlen // Azure public IP creation with tag merging is inherently detailed
func (m *NetworkManager) CreatePublicIP(ctx context.Context, req *cpi.PublicIPRequest) (*cpi.PublicIP, error) {
	if req == nil {
		return nil, ErrInvalidRequest
	}

	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	// Prepare public IP parameters
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
		Tags: BuildTags(MergeTags(m.client.config.DefaultTags, req.Tags)),
	}

	// Add labels as tags
	if req.Labels != nil {
		for k, v := range req.Labels {
			if publicIPParams.Tags == nil {
				publicIPParams.Tags = make(map[string]*string)
			}

			publicIPParams.Tags[k] = to.Ptr(v)
		}
	}

	// Add job and index as tags
	if req.Job != "" {
		if publicIPParams.Tags == nil {
			publicIPParams.Tags = make(map[string]*string)
		}

		publicIPParams.Tags["ocfp-job"] = to.Ptr(req.Job)
	}

	if req.Index != "" {
		if publicIPParams.Tags == nil {
			publicIPParams.Tags = make(map[string]*string)
		}

		publicIPParams.Tags["ocfp-index"] = to.Ptr(req.Index)
	}

	poller, err := m.client.publicIPAddressesClient.BeginCreateOrUpdate(
		ctx,
		m.client.getResourceGroup(),
		req.Name,
		publicIPParams,
		nil,
	)
	if err != nil {
		return nil, WrapAzureError(err, "CreatePublicIP")
	}

	result, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, WrapAzureError(err, "CreatePublicIP")
	}

	logger.Infow("Created public IP", "name", req.Name)

	return m.publicIPToPublicIP(&result.PublicIPAddress), nil
}

// GetPublicIP retrieves a public IP by ID or name.
func (m *NetworkManager) GetPublicIP(ctx context.Context, id string) (*cpi.PublicIP, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	name := ExtractResourceName(id)

	result, err := m.client.publicIPAddressesClient.Get(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return nil, WrapAzureError(err, "GetPublicIP")
	}

	return m.publicIPToPublicIP(&result.PublicIPAddress), nil
}

// ListPublicIPs lists all public IP addresses.
func (m *NetworkManager) ListPublicIPs(ctx context.Context) ([]*cpi.PublicIP, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	pager := m.client.publicIPAddressesClient.NewListPager(m.client.getResourceGroup(), nil)

	var publicIPs []*cpi.PublicIP

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, WrapAzureError(err, "ListPublicIPs")
		}

		for _, pip := range page.Value {
			publicIPs = append(publicIPs, m.publicIPToPublicIP(pip))
		}
	}

	return publicIPs, nil
}

// DeletePublicIP deletes a public IP address.
func (m *NetworkManager) DeletePublicIP(ctx context.Context, id string) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	name := ExtractResourceName(id)

	poller, err := m.client.publicIPAddressesClient.BeginDelete(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return WrapAzureError(err, "DeletePublicIP")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "DeletePublicIP")
	}

	logger.Infow("Deleted public IP", "name", name)

	return nil
}

// AllocateFloatingIP allocates a floating IP (same as public IP in Azure).
func (m *NetworkManager) AllocateFloatingIP(ctx context.Context, req *cpi.AllocateFloatingIPRequest) (*cpi.FloatingIP, error) {
	publicIPReq := &cpi.PublicIPRequest{
		Name: GenerateUniqueName("fip", 24), //nolint:mnd
		Tags: req.Tags,
	}

	publicIP, err := m.CreatePublicIP(ctx, publicIPReq)
	if err != nil {
		return nil, err
	}

	return &cpi.FloatingIP{
		ID:        publicIP.ID,
		Address:   publicIP.IPAddress,
		Status:    publicIP.Status,
		Tags:      publicIP.Tags,
		CreatedAt: publicIP.CreatedAt,
	}, nil
}

// GetFloatingIP retrieves a floating IP by ID.
func (m *NetworkManager) GetFloatingIP(ctx context.Context, id string) (*cpi.FloatingIP, error) {
	publicIP, err := m.GetPublicIP(ctx, id)
	if err != nil {
		return nil, err
	}

	return &cpi.FloatingIP{
		ID:         publicIP.ID,
		Address:    publicIP.IPAddress,
		Status:     publicIP.Status,
		InstanceID: publicIP.InstanceID,
		Tags:       publicIP.Tags,
		CreatedAt:  publicIP.CreatedAt,
	}, nil
}

// ListFloatingIPs lists all floating IPs.
func (m *NetworkManager) ListFloatingIPs(ctx context.Context, filters map[string]string) ([]*cpi.FloatingIP, error) {
	publicIPs, err := m.ListPublicIPs(ctx)
	if err != nil {
		return nil, err
	}

	var floatingIPs []*cpi.FloatingIP

	for _, pip := range publicIPs {
		if matchesFilters(pip.Tags, filters) {
			floatingIPs = append(floatingIPs, &cpi.FloatingIP{
				ID:         pip.ID,
				Address:    pip.IPAddress,
				Status:     pip.Status,
				InstanceID: pip.InstanceID,
				Tags:       pip.Tags,
				CreatedAt:  pip.CreatedAt,
			})
		}
	}

	return floatingIPs, nil
}

// AssociateFloatingIP associates a floating IP with an instance.
func (m *NetworkManager) AssociateFloatingIP(_ctx context.Context, _ipID string, _instanceID string) error {
	// In Azure, public IPs are associated via NIC configuration
	// This requires updating the VM's NIC, which is more complex
	// For now, return not implemented
	return ErrNotImplemented
}

// DisassociateFloatingIP disassociates a floating IP from an instance.
func (m *NetworkManager) DisassociateFloatingIP(_ctx context.Context, _ipID string) error {
	return ErrNotImplemented
}

// ReleaseFloatingIP releases a floating IP.
func (m *NetworkManager) ReleaseFloatingIP(ctx context.Context, id string) error {
	return m.DeletePublicIP(ctx, id)
}

// CreateRouter creates a new route table (Azure's equivalent to a router).
func (m *NetworkManager) CreateRouter(ctx context.Context, req *cpi.CreateRouterRequest) (*cpi.Router, error) {
	if req == nil {
		return nil, ErrInvalidRequest
	}

	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	routeTableParams := armnetwork.RouteTable{
		Location: to.Ptr(m.client.getLocation()),
		Tags:     BuildTags(MergeTags(m.client.config.DefaultTags, req.Tags)),
	}

	poller, err := m.client.routeTablesClient.BeginCreateOrUpdate(
		ctx,
		m.client.getResourceGroup(),
		req.Name,
		routeTableParams,
		nil,
	)
	if err != nil {
		return nil, WrapAzureError(err, "CreateRouter")
	}

	result, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, WrapAzureError(err, "CreateRouter")
	}

	logger.Infow("Created route table", "name", req.Name)

	return m.routeTableToRouter(&result.RouteTable), nil
}

// GetRouter retrieves a route table by ID.
func (m *NetworkManager) GetRouter(ctx context.Context, id string) (*cpi.Router, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	name := ExtractResourceName(id)

	result, err := m.client.routeTablesClient.Get(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return nil, WrapAzureError(err, "GetRouter")
	}

	return m.routeTableToRouter(&result.RouteTable), nil
}

// ListRouters lists all route tables.
func (m *NetworkManager) ListRouters(ctx context.Context) ([]*cpi.Router, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	pager := m.client.routeTablesClient.NewListPager(m.client.getResourceGroup(), nil)

	var routers []*cpi.Router

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, WrapAzureError(err, "ListRouters")
		}

		for _, rt := range page.Value {
			routers = append(routers, m.routeTableToRouter(rt))
		}
	}

	return routers, nil
}

// AttachRouterInterface associates a route table with a subnet.
func (m *NetworkManager) AttachRouterInterface(_ctx context.Context, _routerID string, _subnetID string) error {
	// In Azure, this requires updating the subnet to reference the route table
	return ErrNotImplemented
}

// DetachRouterInterface disassociates a route table from a subnet.
func (m *NetworkManager) DetachRouterInterface(_ctx context.Context, _routerID string, _subnetID string) error {
	return ErrNotImplemented
}

// DeleteRouter deletes a route table.
func (m *NetworkManager) DeleteRouter(ctx context.Context, id string) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	name := ExtractResourceName(id)

	poller, err := m.client.routeTablesClient.BeginDelete(ctx, m.client.getResourceGroup(), name, nil)
	if err != nil {
		return WrapAzureError(err, "DeleteRouter")
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return WrapAzureError(err, "DeleteRouter")
	}

	logger.Infow("Deleted route table", "name", name)

	return nil
}

// CreateLoadBalancer creates a new load balancer.
func (m *NetworkManager) CreateLoadBalancer(ctx context.Context, config *cpi.LoadBalancer) (*cpi.LoadBalancer, error) {
	return m.client.loadBalancer.createLoadBalancerFromConfig(ctx, config)
}

// GetLoadBalancer retrieves a load balancer by name or ID.
func (m *NetworkManager) GetLoadBalancer(ctx context.Context, nameOrID string) (*cpi.LoadBalancer, error) {
	return m.client.loadBalancer.GetLoadBalancer(ctx, nameOrID)
}

// ListLoadBalancers lists all load balancers.
func (m *NetworkManager) ListLoadBalancers(ctx context.Context, filters map[string]string) ([]*cpi.LoadBalancer, error) {
	return m.client.loadBalancer.ListLoadBalancers(ctx, filters)
}

// UpdateLoadBalancer updates a load balancer.
func (m *NetworkManager) UpdateLoadBalancer(ctx context.Context, lb *cpi.LoadBalancer) error {
	return m.client.loadBalancer.UpdateLoadBalancer(ctx, lb.ID, &cpi.UpdateLoadBalancerRequest{
		Name: &lb.Name,
	})
}

// DeleteLoadBalancer deletes a load balancer.
func (m *NetworkManager) DeleteLoadBalancer(ctx context.Context, id string) error {
	return m.client.loadBalancer.DeleteLoadBalancer(ctx, id)
}

// GetBackendPools retrieves backend pools for a load balancer.
func (m *NetworkManager) GetBackendPools(ctx context.Context, lbID string) ([]*cpi.BackendPool, error) { //nolint:varnamelen // lb is clear in context
	// Get load balancer and extract backend pools
	lb, err := m.GetLoadBalancer(ctx, lbID) //nolint:varnamelen
	if err != nil {
		return nil, err
	}

	// Convert backends to backend pools
	var pools []*cpi.BackendPool

	if lb != nil && len(lb.Backends) > 0 {
		pool := &cpi.BackendPool{
			ID:   lb.ID + "/backendPool",
			Name: "default",
		}
		for _, backend := range lb.Backends {
			pool.Members = append(pool.Members, &cpi.BackendMember{
				ID:        backend.ID,
				IPAddress: backend.Address,
				Port:      backend.Port,
				Weight:    backend.Weight,
			})
		}

		pools = append(pools, pool)
	}

	return pools, nil
}

// AddBackendMember adds a member to a load balancer backend pool.
func (m *NetworkManager) AddBackendMember(ctx context.Context, lbID string, member *cpi.BackendMember) error {
	return m.client.loadBalancer.AddBackend(ctx, lbID, &cpi.Backend{
		ID:      member.ID,
		Address: member.IPAddress,
		Port:    member.Port,
		Weight:  member.Weight,
		Enabled: true,
	})
}

// RemoveBackendMember removes a member from a load balancer backend pool.
func (m *NetworkManager) RemoveBackendMember(ctx context.Context, lbID string, memberIP string) error {
	return m.client.loadBalancer.RemoveBackend(ctx, lbID, memberIP)
}

// ConfigureHealthCheck configures a health check for a load balancer.
func (m *NetworkManager) ConfigureHealthCheck(ctx context.Context, lbID string, check *cpi.HealthCheck) error {
	return m.client.loadBalancer.ConfigureHealthCheck(ctx, lbID, check)
}

// GetLoadBalancerHealth retrieves health status of a load balancer.
func (m *NetworkManager) GetLoadBalancerHealth(ctx context.Context, lbID string) (*cpi.HealthStatus, error) {
	return m.client.loadBalancer.GetHealthStatus(ctx, lbID)
}

// Helper functions

func (m *NetworkManager) vnetToNetwork(vnet *armnetwork.VirtualNetwork) *cpi.Network {
	if vnet == nil {
		return nil
	}

	network := &cpi.Network{
		ID:        DerefString(vnet.ID),
		Name:      DerefString(vnet.Name),
		Region:    DerefString(vnet.Location),
		Tags:      ExtractTags(vnet.Tags),
		CreatedAt: time.Now(), // Azure doesn't expose creation time directly
	}

	if vnet.Properties != nil {
		network.State = MapProvisioningStateToResourceState(DerefString((*string)(vnet.Properties.ProvisioningState)))

		if vnet.Properties.AddressSpace != nil && len(vnet.Properties.AddressSpace.AddressPrefixes) > 0 {
			network.CIDR = DerefString(vnet.Properties.AddressSpace.AddressPrefixes[0])
		}

		if vnet.Properties.DhcpOptions != nil && vnet.Properties.DhcpOptions.DNSServers != nil {
			for _, dns := range vnet.Properties.DhcpOptions.DNSServers {
				network.DNSServers = append(network.DNSServers, DerefString(dns))
			}
		}
	}

	return network
}

func (m *NetworkManager) subnetToSubnet(subnet *armnetwork.Subnet, vnetName string) *cpi.Subnet {
	if subnet == nil {
		return nil
	} //nolint:varnamelen // s is clear in context

	s := &cpi.Subnet{ //nolint:varnamelen
		ID:        DerefString(subnet.ID),
		Name:      DerefString(subnet.Name),
		NetworkID: BuildVNetID(m.client.getSubscriptionID(), m.client.getResourceGroup(), vnetName),
		CreatedAt: time.Now(),
	}

	if subnet.Properties != nil {
		s.CIDR = DerefString(subnet.Properties.AddressPrefix)
		s.State = MapProvisioningStateToResourceState(DerefString((*string)(subnet.Properties.ProvisioningState)))
	}

	return s
}

func (m *NetworkManager) publicIPToPublicIP(pip *armnetwork.PublicIPAddress) *cpi.PublicIP {
	if pip == nil {
		return nil
	}

	publicIP := &cpi.PublicIP{
		ID:        DerefString(pip.ID),
		Name:      DerefString(pip.Name),
		Tags:      ExtractTags(pip.Tags),
		Labels:    ExtractTags(pip.Tags),
		CreatedAt: time.Now(),
	}

	if pip.Properties != nil {
		publicIP.IPAddress = DerefString(pip.Properties.IPAddress)
		publicIP.Address = publicIP.IPAddress

		// Determine status
		hasIP := publicIP.IPAddress != ""
		hasAssociation := pip.Properties.IPConfiguration != nil

		publicIP.Status = MapIPAllocationStateToStatus(hasIP, hasAssociation)

		if hasAssociation && pip.Properties.IPConfiguration.ID != nil {
			// Extract VM ID from IP configuration
			publicIP.InstanceID = DerefString(pip.Properties.IPConfiguration.ID)
		}
	}

	// Extract job and index from tags
	if publicIP.Tags != nil {
		if job, ok := publicIP.Tags["ocfp-job"]; ok {
			publicIP.Job = job
		}

		if index, ok := publicIP.Tags["ocfp-index"]; ok {
			publicIP.Index = index
		}
	}

	return publicIP
}

func (m *NetworkManager) routeTableToRouter(rt *armnetwork.RouteTable) *cpi.Router { //nolint:varnamelen
	if rt == nil {
		return nil
	}

	router := &cpi.Router{
		ID:        DerefString(rt.ID),
		Name:      DerefString(rt.Name),
		Tags:      ExtractTags(rt.Tags),
		CreatedAt: time.Now(),
	}

	if rt.Properties != nil {
		router.State = MapProvisioningStateToResourceState(DerefString((*string)(rt.Properties.ProvisioningState)))

		// Extract routes
		if rt.Properties.Routes != nil {
			for _, route := range rt.Properties.Routes {
				if route.Properties != nil {
					router.Routes = append(router.Routes, &cpi.Route{
						Destination: DerefString(route.Properties.AddressPrefix),
						NextHop:     DerefString(route.Properties.NextHopIPAddress),
					})
				}
			}
		}

		// Extract associated subnets
		if rt.Properties.Subnets != nil {
			for _, subnet := range rt.Properties.Subnets {
				router.Interfaces = append(router.Interfaces, DerefString(subnet.ID))
			}
		}
	}

	return router
}

func matchesFilters(tags map[string]string, filters map[string]string) bool {
	if len(filters) == 0 {
		return true
	}

	for key, value := range filters {
		if tagValue, ok := tags[key]; !ok || tagValue != value {
			return false
		}
	}

	return true
}

func splitSubnetPath(path string) []string {
	// Path format: virtualNetworks/vnetName/subnets/subnetName
	// We need to extract vnetName and subnetName
	parts := make([]string, 0, 2) //nolint:mnd

	// Simple split - look for the pattern
	if idx := findSubstr(path, "/subnets/"); idx != -1 {
		vnetPart := path[:idx]
		subnetPart := path[idx+9:] // len("/subnets/") = 9

		// Remove "virtualNetworks/" prefix if present
		if prefixIdx := findSubstr(vnetPart, "virtualNetworks/"); prefixIdx != -1 {
			vnetPart = vnetPart[prefixIdx+16:] // len("virtualNetworks/") = 16
		}

		parts = append(parts, vnetPart, subnetPart)
	}

	return parts
}

func findSubstr(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}

	return -1
}
