package gcp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// CreateNetwork creates a new VPC network.
func (m *NetworkManager) CreateNetwork(ctx context.Context, req *cpi.NetworkRequest) (*cpi.Network, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	projectID := m.client.getConfig().GetNetworkProject()
	// Note: GCP VPC Networks don't support labels directly
	_ = BuildLabelsWithBloc(req.Name, "", req.Tags) // Labels stored via description or firewall rules

	network := &computepb.Network{
		Name:                  proto(req.Name),
		Description:           proto(req.Description),
		AutoCreateSubnetworks: proto(false), // Use custom subnet mode
	}

	op, err := m.client.getNetworksClient().Insert(ctx, &computepb.InsertNetworkRequest{ //nolint:varnamelen
		Project:         projectID,
		NetworkResource: network,
	})
	if err != nil {
		return nil, WrapGCPError(err, "CreateNetwork")
	}

	// Wait for operation to complete
	err = op.Wait(ctx)
	if err != nil {
		return nil, WrapGCPError(err, "CreateNetwork.Wait")
	}

	logger.Debugw("Created VPC network", "name", req.Name, "project", projectID)

	// Fetch the created network
	return m.GetNetwork(ctx, req.Name)
}

// GetNetwork retrieves a VPC network by name or ID.
func (m *NetworkManager) GetNetwork(ctx context.Context, id string) (*cpi.Network, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	projectID := m.client.getConfig().GetNetworkProject()

	network, err := m.client.getNetworksClient().Get(ctx, &computepb.GetNetworkRequest{
		Project: projectID,
		Network: id,
	})
	if err != nil {
		return nil, WrapGCPError(err, "GetNetwork")
	}

	return m.convertNetwork(network), nil
}

// ListNetworks lists VPC networks with optional filters.
func (m *NetworkManager) ListNetworks(ctx context.Context, filters map[string]string) ([]*cpi.Network, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	projectID := m.client.getConfig().GetNetworkProject()

	var networks []*cpi.Network

	it := m.client.getNetworksClient().List(ctx, &computepb.ListNetworksRequest{
		Project: projectID,
	})

	for {
		network, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "ListNetworks")
		}

		// Apply label filters
		if matchesFilters(network.GetDescription(), filters) {
			networks = append(networks, m.convertNetwork(network))
		}
	}

	return networks, nil
}

// DeleteNetwork deletes a VPC network.
func (m *NetworkManager) DeleteNetwork(ctx context.Context, id string) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	projectID := m.client.getConfig().GetNetworkProject()

	op, err := m.client.getNetworksClient().Delete(ctx, &computepb.DeleteNetworkRequest{ //nolint:varnamelen
		Project: projectID,
		Network: id,
	})
	if err != nil {
		return WrapGCPError(err, "DeleteNetwork")
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "DeleteNetwork.Wait")
	}

	logger.Debugw("Deleted VPC network", "name", id, "project", projectID)

	return nil
}

// CreateSubnet creates a new subnetwork.
func (m *NetworkManager) CreateSubnet(ctx context.Context, req *cpi.SubnetRequest) (*cpi.Subnet, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.GetNetworkProject()
	region := config.Region

	// Build network URL
	networkURL := FormatNetworkURL(projectID, req.NetworkID)

	subnetwork := &computepb.Subnetwork{
		Name:                  proto(req.Name),
		IpCidrRange:           proto(req.CIDR),
		Network:               proto(networkURL),
		Region:                proto(region),
		PrivateIpGoogleAccess: proto(config.EnablePrivateGoogleAccess),
	}

	op, err := m.client.getSubnetworksClient().Insert(ctx, &computepb.InsertSubnetworkRequest{ //nolint:varnamelen
		Project:            projectID,
		Region:             region,
		SubnetworkResource: subnetwork,
	})
	if err != nil {
		return nil, WrapGCPError(err, "CreateSubnet")
	}

	err = op.Wait(ctx)
	if err != nil {
		return nil, WrapGCPError(err, "CreateSubnet.Wait")
	}

	logger.Debugw("Created subnetwork", "name", req.Name, "cidr", req.CIDR, "region", region)

	return m.GetSubnet(ctx, req.Name)
}

// GetSubnet retrieves a subnetwork by name or ID.
func (m *NetworkManager) GetSubnet(ctx context.Context, id string) (*cpi.Subnet, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.GetNetworkProject()
	region := config.Region

	subnet, err := m.client.getSubnetworksClient().Get(ctx, &computepb.GetSubnetworkRequest{
		Project:    projectID,
		Region:     region,
		Subnetwork: id,
	})
	if err != nil {
		return nil, WrapGCPError(err, "GetSubnet")
	}

	return m.convertSubnet(subnet), nil
}

// ListSubnets lists subnetworks in a network.
func (m *NetworkManager) ListSubnets(ctx context.Context, networkID string) ([]*cpi.Subnet, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.GetNetworkProject()
	region := config.Region

	var subnets []*cpi.Subnet

	it := m.client.getSubnetworksClient().List(ctx, &computepb.ListSubnetworksRequest{ //nolint:varnamelen
		Project: projectID,
		Region:  region,
	})

	for {
		subnet, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "ListSubnets")
		}

		// Filter by network if specified
		if networkID == "" || strings.HasSuffix(subnet.GetNetwork(), "/"+networkID) {
			subnets = append(subnets, m.convertSubnet(subnet))
		}
	}

	return subnets, nil
}

// DeleteSubnet deletes a subnetwork.
func (m *NetworkManager) DeleteSubnet(ctx context.Context, id string) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	config := m.client.getConfig()
	projectID := config.GetNetworkProject()
	region := config.Region

	op, err := m.client.getSubnetworksClient().Delete(ctx, &computepb.DeleteSubnetworkRequest{ //nolint:varnamelen
		Project:    projectID,
		Region:     region,
		Subnetwork: id,
	})
	if err != nil {
		return WrapGCPError(err, "DeleteSubnet")
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "DeleteSubnet.Wait")
	}

	logger.Debugw("Deleted subnetwork", "name", id, "region", region)

	return nil
}

// CreateSecurityGroup creates a firewall rule (GCP uses network tags for grouping).
//
//nolint:dupl // intentionally similar CPI implementation
func (m *NetworkManager) CreateSecurityGroup(ctx context.Context, req *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	projectID := m.client.getConfig().GetNetworkProject()
	networkTag := FormatNetworkTag(req.Name)

	// Build network URL
	networkURL := FormatNetworkURL(projectID, req.NetworkID)

	// Create firewall rule for this security group
	firewall := &computepb.Firewall{
		Name:        proto(req.Name),
		Description: proto(req.Description),
		Network:     proto(networkURL),
		TargetTags:  []string{networkTag},
		Allowed:     []*computepb.Allowed{},
	}

	// Add rules
	for _, rule := range req.Rules {
		allowed := &computepb.Allowed{
			IPProtocol: proto(rule.Protocol),
		}
		if rule.PortRangeMin > 0 && rule.PortRangeMax > 0 {
			if rule.PortRangeMin == rule.PortRangeMax {
				allowed.Ports = []string{strconv.Itoa(rule.PortRangeMin)}
			} else {
				allowed.Ports = []string{fmt.Sprintf("%d-%d", rule.PortRangeMin, rule.PortRangeMax)}
			}
		}

		firewall.Allowed = append(firewall.Allowed, allowed)

		// Set source ranges for ingress
		if rule.Direction == DirectionIngress && rule.RemoteIPCIDR != "" {
			firewall.SourceRanges = append(firewall.SourceRanges, rule.RemoteIPCIDR)
		}
	}

	// Default to allow from anywhere if no source ranges specified
	if len(firewall.GetSourceRanges()) == 0 {
		firewall.SourceRanges = []string{"0.0.0.0/0"}
	}

	op, err := m.client.getFirewallsClient().Insert(ctx, &computepb.InsertFirewallRequest{ //nolint:varnamelen
		Project:          projectID,
		FirewallResource: firewall,
	})
	if err != nil {
		return nil, WrapGCPError(err, "CreateSecurityGroup")
	}

	err = op.Wait(ctx)
	if err != nil {
		return nil, WrapGCPError(err, "CreateSecurityGroup.Wait")
	}

	logger.Debugw("Created firewall rule", "name", req.Name, "networkTag", networkTag)

	return m.GetSecurityGroup(ctx, req.Name)
}

// GetSecurityGroup retrieves a firewall rule by name.
func (m *NetworkManager) GetSecurityGroup(ctx context.Context, id string) (*cpi.SecurityGroup, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	projectID := m.client.getConfig().GetNetworkProject()

	firewall, err := m.client.getFirewallsClient().Get(ctx, &computepb.GetFirewallRequest{
		Project:  projectID,
		Firewall: id,
	})
	if err != nil {
		return nil, WrapGCPError(err, "GetSecurityGroup")
	}

	return m.convertFirewallToSecurityGroup(firewall), nil
}

// ListSecurityGroups lists firewall rules.
func (m *NetworkManager) ListSecurityGroups(ctx context.Context, _filters map[string]string) ([]*cpi.SecurityGroup, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	projectID := m.client.getConfig().GetNetworkProject()

	var securityGroups []*cpi.SecurityGroup

	it := m.client.getFirewallsClient().List(ctx, &computepb.ListFirewallsRequest{
		Project: projectID,
	})

	for {
		firewall, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "ListSecurityGroups")
		}

		securityGroups = append(securityGroups, m.convertFirewallToSecurityGroup(firewall))
	}

	return securityGroups, nil
}

// DeleteSecurityGroup deletes a firewall rule.
func (m *NetworkManager) DeleteSecurityGroup(ctx context.Context, id string) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	projectID := m.client.getConfig().GetNetworkProject()

	op, err := m.client.getFirewallsClient().Delete(ctx, &computepb.DeleteFirewallRequest{ //nolint:varnamelen
		Project:  projectID,
		Firewall: id,
	})
	if err != nil {
		return WrapGCPError(err, "DeleteSecurityGroup")
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "DeleteSecurityGroup.Wait")
	}

	logger.Debugw("Deleted firewall rule", "name", id)

	return nil
}

// CreatePublicIP reserves a static external IP address.
func (m *NetworkManager) CreatePublicIP(ctx context.Context, req *cpi.PublicIPRequest) (*cpi.PublicIP, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	region := config.Region

	labels := BuildLabels(req.Name, req.Tags)
	if req.Job != "" {
		labels[JobLabel] = SanitizeLabel(req.Job)
	}

	if req.Index != "" {
		labels[IndexLabel] = SanitizeLabel(req.Index)
	}

	address := &computepb.Address{
		Name:        proto(req.Name),
		AddressType: proto("EXTERNAL"),
		Labels:      labels,
	}

	op, err := m.client.getAddressesClient().Insert(ctx, &computepb.InsertAddressRequest{ //nolint:varnamelen
		Project:         projectID,
		Region:          region,
		AddressResource: address,
	})
	if err != nil {
		return nil, WrapGCPError(err, "CreatePublicIP")
	}

	err = op.Wait(ctx)
	if err != nil {
		return nil, WrapGCPError(err, "CreatePublicIP.Wait")
	}

	logger.Debugw("Created public IP", "name", req.Name, "region", region)

	return m.GetPublicIP(ctx, req.Name)
}

// GetPublicIP retrieves a public IP address by name.
func (m *NetworkManager) GetPublicIP(ctx context.Context, id string) (*cpi.PublicIP, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	region := config.Region

	address, err := m.client.getAddressesClient().Get(ctx, &computepb.GetAddressRequest{
		Project: projectID,
		Region:  region,
		Address: id,
	})
	if err != nil {
		return nil, WrapGCPError(err, "GetPublicIP")
	}

	return m.convertAddressToPublicIP(address), nil
}

// ListPublicIPs lists public IP addresses.
func (m *NetworkManager) ListPublicIPs(ctx context.Context) ([]*cpi.PublicIP, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	region := config.Region

	var publicIPs []*cpi.PublicIP

	it := m.client.getAddressesClient().List(ctx, &computepb.ListAddressesRequest{ //nolint:varnamelen
		Project: projectID,
		Region:  region,
	})

	for {
		address, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "ListPublicIPs")
		}

		// Only include external addresses
		if address.GetAddressType() == "EXTERNAL" {
			publicIPs = append(publicIPs, m.convertAddressToPublicIP(address))
		}
	}

	return publicIPs, nil
}

// DeletePublicIP releases a public IP address.
func (m *NetworkManager) DeletePublicIP(ctx context.Context, id string) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	region := config.Region

	op, err := m.client.getAddressesClient().Delete(ctx, &computepb.DeleteAddressRequest{ //nolint:varnamelen
		Project: projectID,
		Region:  region,
		Address: id,
	})
	if err != nil {
		return WrapGCPError(err, "DeletePublicIP")
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "DeletePublicIP.Wait")
	}

	logger.Debugw("Deleted public IP", "name", id, "region", region)

	return nil
}

// AllocateFloatingIP allocates a floating (static) IP.
func (m *NetworkManager) AllocateFloatingIP(ctx context.Context, req *cpi.AllocateFloatingIPRequest) (*cpi.FloatingIP, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	region := config.Region

	name := fmt.Sprintf("floating-ip-%d", time.Now().Unix())
	labels := BuildLabels(name, req.Tags)

	address := &computepb.Address{
		Name:        proto(name),
		AddressType: proto("EXTERNAL"),
		Labels:      labels,
	}

	op, err := m.client.getAddressesClient().Insert(ctx, &computepb.InsertAddressRequest{ //nolint:varnamelen
		Project:         projectID,
		Region:          region,
		AddressResource: address,
	})
	if err != nil {
		return nil, WrapGCPError(err, "AllocateFloatingIP")
	}

	err = op.Wait(ctx)
	if err != nil {
		return nil, WrapGCPError(err, "AllocateFloatingIP.Wait")
	}

	logger.Debugw("Allocated floating IP", "name", name, "region", region)

	return m.GetFloatingIP(ctx, name)
}

// GetFloatingIP retrieves a floating IP by name.
func (m *NetworkManager) GetFloatingIP(ctx context.Context, id string) (*cpi.FloatingIP, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	region := config.Region

	address, err := m.client.getAddressesClient().Get(ctx, &computepb.GetAddressRequest{
		Project: projectID,
		Region:  region,
		Address: id,
	})
	if err != nil {
		return nil, WrapGCPError(err, "GetFloatingIP")
	}

	return m.convertAddressToFloatingIP(address), nil
}

// ListFloatingIPs lists floating IPs.
func (m *NetworkManager) ListFloatingIPs(ctx context.Context, _filters map[string]string) ([]*cpi.FloatingIP, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	region := config.Region

	var floatingIPs []*cpi.FloatingIP

	it := m.client.getAddressesClient().List(ctx, &computepb.ListAddressesRequest{ //nolint:varnamelen
		Project: projectID,
		Region:  region,
	})

	for {
		address, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "ListFloatingIPs")
		}

		if address.GetAddressType() == "EXTERNAL" {
			floatingIPs = append(floatingIPs, m.convertAddressToFloatingIP(address))
		}
	}

	return floatingIPs, nil
}

// AssociateFloatingIP associates a floating IP with an instance.
func (m *NetworkManager) AssociateFloatingIP(_ctx context.Context, _ipID string, _instanceID string) error {
	// In GCP, this is done by updating the instance's access config
	// This is a more complex operation that requires getting the instance,
	// finding the network interface, and adding an access config
	return fmt.Errorf("%w: AssociateFloatingIP - use instance network interface configuration", ErrNotImplemented)
}

// DisassociateFloatingIP disassociates a floating IP from an instance.
func (m *NetworkManager) DisassociateFloatingIP(_ctx context.Context, _ipID string) error {
	// In GCP, this is done by removing the access config from the instance
	return fmt.Errorf("%w: DisassociateFloatingIP - use instance network interface configuration", ErrNotImplemented)
}

// ReleaseFloatingIP releases a floating IP.
func (m *NetworkManager) ReleaseFloatingIP(ctx context.Context, id string) error {
	return m.DeletePublicIP(ctx, id)
}

// CreateRouter creates a Cloud Router.
func (m *NetworkManager) CreateRouter(ctx context.Context, req *cpi.CreateRouterRequest) (*cpi.Router, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.GetNetworkProject()
	region := config.Region

	networkURL := FormatNetworkURL(projectID, req.NetworkID)

	router := &computepb.Router{
		Name:    proto(req.Name),
		Network: proto(networkURL),
	}

	op, err := m.client.getRoutersClient().Insert(ctx, &computepb.InsertRouterRequest{ //nolint:varnamelen
		Project:        projectID,
		Region:         region,
		RouterResource: router,
	})
	if err != nil {
		return nil, WrapGCPError(err, "CreateRouter")
	}

	err = op.Wait(ctx)
	if err != nil {
		return nil, WrapGCPError(err, "CreateRouter.Wait")
	}

	logger.Debugw("Created Cloud Router", "name", req.Name, "region", region)

	return m.GetRouter(ctx, req.Name)
}

// GetRouter retrieves a Cloud Router by name.
func (m *NetworkManager) GetRouter(ctx context.Context, id string) (*cpi.Router, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.GetNetworkProject()
	region := config.Region

	router, err := m.client.getRoutersClient().Get(ctx, &computepb.GetRouterRequest{
		Project: projectID,
		Region:  region,
		Router:  id,
	})
	if err != nil {
		return nil, WrapGCPError(err, "GetRouter")
	}

	return m.convertRouter(router), nil
}

// ListRouters lists Cloud Routers.
func (m *NetworkManager) ListRouters(ctx context.Context) ([]*cpi.Router, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.GetNetworkProject()
	region := config.Region

	var routers []*cpi.Router

	it := m.client.getRoutersClient().List(ctx, &computepb.ListRoutersRequest{ //nolint:varnamelen
		Project: projectID,
		Region:  region,
	})

	for {
		router, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "ListRouters")
		}

		routers = append(routers, m.convertRouter(router))
	}

	return routers, nil
}

// AttachRouterInterface attaches a subnet to a router.
func (m *NetworkManager) AttachRouterInterface(_ctx context.Context, _routerID string, _subnetID string) error {
	// GCP Cloud Routers don't have explicit interface attachment like OpenStack
	// They automatically route for networks they're associated with
	// NAT configuration is done separately via Cloud NAT
	return fmt.Errorf("%w: AttachRouterInterface - use Cloud NAT configuration", ErrNotImplemented)
}

// DetachRouterInterface detaches a subnet from a router.
func (m *NetworkManager) DetachRouterInterface(_ctx context.Context, _routerID string, _subnetID string) error {
	return fmt.Errorf("%w: DetachRouterInterface - use Cloud NAT configuration", ErrNotImplemented)
}

// DeleteRouter deletes a Cloud Router.
func (m *NetworkManager) DeleteRouter(ctx context.Context, id string) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	config := m.client.getConfig()
	projectID := config.GetNetworkProject()
	region := config.Region

	op, err := m.client.getRoutersClient().Delete(ctx, &computepb.DeleteRouterRequest{ //nolint:varnamelen
		Project: projectID,
		Region:  region,
		Router:  id,
	})
	if err != nil {
		return WrapGCPError(err, "DeleteRouter")
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "DeleteRouter.Wait")
	}

	logger.Debugw("Deleted Cloud Router", "name", id, "region", region)

	return nil
}

// CreateLoadBalancer creates a load balancer (delegates to LoadBalancerManager).
func (m *NetworkManager) CreateLoadBalancer(_ctx context.Context, _config *cpi.LoadBalancer) (*cpi.LoadBalancer, error) {
	return nil, fmt.Errorf("%w: use LoadBalancerManager", ErrNotImplemented)
}

// GetLoadBalancer retrieves a load balancer.
func (m *NetworkManager) GetLoadBalancer(_ctx context.Context, _nameOrID string) (*cpi.LoadBalancer, error) {
	return nil, fmt.Errorf("%w: use LoadBalancerManager", ErrNotImplemented)
}

// ListLoadBalancers lists load balancers.
func (m *NetworkManager) ListLoadBalancers(_ctx context.Context, _filters map[string]string) ([]*cpi.LoadBalancer, error) {
	return nil, fmt.Errorf("%w: use LoadBalancerManager", ErrNotImplemented)
}

// UpdateLoadBalancer updates a load balancer.
func (m *NetworkManager) UpdateLoadBalancer(_ctx context.Context, _lb *cpi.LoadBalancer) error {
	return fmt.Errorf("%w: use LoadBalancerManager", ErrNotImplemented)
}

// DeleteLoadBalancer deletes a load balancer.
func (m *NetworkManager) DeleteLoadBalancer(_ctx context.Context, _id string) error {
	return fmt.Errorf("%w: use LoadBalancerManager", ErrNotImplemented)
}

// GetBackendPools retrieves backend pools for a load balancer.
func (m *NetworkManager) GetBackendPools(_ctx context.Context, _lbID string) ([]*cpi.BackendPool, error) {
	return nil, fmt.Errorf("%w: use LoadBalancerManager", ErrNotImplemented)
}

// AddBackendMember adds a member to a backend pool.
func (m *NetworkManager) AddBackendMember(_ctx context.Context, _lbID string, _member *cpi.BackendMember) error {
	return fmt.Errorf("%w: use LoadBalancerManager", ErrNotImplemented)
}

// RemoveBackendMember removes a member from a backend pool.
func (m *NetworkManager) RemoveBackendMember(_ctx context.Context, _lbID string, _memberIP string) error {
	return fmt.Errorf("%w: use LoadBalancerManager", ErrNotImplemented)
}

// ConfigureHealthCheck configures a health check for a load balancer.
func (m *NetworkManager) ConfigureHealthCheck(_ctx context.Context, _lbID string, _check *cpi.HealthCheck) error {
	return fmt.Errorf("%w: use LoadBalancerManager", ErrNotImplemented)
}

// GetLoadBalancerHealth retrieves health status for a load balancer.
func (m *NetworkManager) GetLoadBalancerHealth(_ctx context.Context, _lbID string) (*cpi.HealthStatus, error) {
	return nil, fmt.Errorf("%w: use LoadBalancerManager", ErrNotImplemented)
}

// Helper functions

func (m *NetworkManager) convertNetwork(network *computepb.Network) *cpi.Network {
	return &cpi.Network{
		ID:        strconv.FormatUint(network.GetId(), 10),
		Name:      network.GetName(),
		State:     cpi.ResourceStateActive,
		CreatedAt: ParseTimestamp(network.GetCreationTimestamp()),
	}
}

func (m *NetworkManager) convertSubnet(subnet *computepb.Subnetwork) *cpi.Subnet {
	return &cpi.Subnet{
		ID:               strconv.FormatUint(subnet.GetId(), 10),
		Name:             subnet.GetName(),
		NetworkID:        ExtractNameFromURL(subnet.GetNetwork()),
		CIDR:             subnet.GetIpCidrRange(),
		AvailabilityZone: ExtractRegionFromURL(subnet.GetSelfLink()),
		State:            cpi.ResourceStateActive,
		CreatedAt:        ParseTimestamp(subnet.GetCreationTimestamp()),
	}
}

func (m *NetworkManager) convertFirewallToSecurityGroup(firewall *computepb.Firewall) *cpi.SecurityGroup {
	var rules []*cpi.SecurityRule

	for _, allowed := range firewall.GetAllowed() {
		for _, port := range allowed.GetPorts() {
			portMin, portMax := parsePortRange(port)
			rules = append(rules, &cpi.SecurityRule{
				Direction:    DirectionIngress,
				Protocol:     allowed.GetIPProtocol(),
				PortRangeMin: portMin,
				PortRangeMax: portMax,
			})
		}
	}

	return &cpi.SecurityGroup{
		ID:          strconv.FormatUint(firewall.GetId(), 10),
		Name:        firewall.GetName(),
		Description: firewall.GetDescription(),
		NetworkID:   ExtractNameFromURL(firewall.GetNetwork()),
		Rules:       rules,
		CreatedAt:   ParseTimestamp(firewall.GetCreationTimestamp()),
	}
}

func (m *NetworkManager) convertAddressToPublicIP(address *computepb.Address) *cpi.PublicIP {
	status := "available"
	if address.GetStatus() == AddressStatusInUse {
		status = "associated"
	}

	return &cpi.PublicIP{
		ID:        strconv.FormatUint(address.GetId(), 10),
		Name:      address.GetName(),
		IPAddress: address.GetAddress(),
		Address:   address.GetAddress(),
		Status:    status,
		Job:       address.GetLabels()[JobLabel],
		Index:     address.GetLabels()[IndexLabel],
		Labels:    address.GetLabels(),
		CreatedAt: ParseTimestamp(address.GetCreationTimestamp()),
	}
}

func (m *NetworkManager) convertAddressToFloatingIP(address *computepb.Address) *cpi.FloatingIP {
	status := "available"
	if address.GetStatus() == AddressStatusInUse {
		status = "associated"
	}

	return &cpi.FloatingIP{
		ID:        strconv.FormatUint(address.GetId(), 10),
		Address:   address.GetAddress(),
		Status:    status,
		Tags:      address.GetLabels(),
		CreatedAt: ParseTimestamp(address.GetCreationTimestamp()),
	}
}

func (m *NetworkManager) convertRouter(router *computepb.Router) *cpi.Router {
	return &cpi.Router{
		ID:        strconv.FormatUint(router.GetId(), 10),
		Name:      router.GetName(),
		NetworkID: ExtractNameFromURL(router.GetNetwork()),
		State:     cpi.ResourceStateActive,
		CreatedAt: ParseTimestamp(router.GetCreationTimestamp()),
	}
}

func parsePortRange(port string) (int, int) {
	if strings.Contains(port, "-") {
		parts := strings.Split(port, "-")
		if len(parts) == 2 { //nolint:mnd
			var portMin, portMax int

			_, _ = fmt.Sscanf(parts[0], "%d", &portMin)
			_, _ = fmt.Sscanf(parts[1], "%d", &portMax)

			return portMin, portMax
		}
	}

	var p int

	_, _ = fmt.Sscanf(port, "%d", &p)

	return p, p
}

func matchesFilters(description string, filters map[string]string) bool {
	// Simple filter matching - in production would use proper label filtering
	if len(filters) == 0 {
		return true
	}

	for _, v := range filters {
		if strings.Contains(description, v) {
			return true
		}
	}

	return false
}
