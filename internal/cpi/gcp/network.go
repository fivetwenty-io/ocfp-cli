package gcp

import (
	"context"
	"errors"
	"fmt"
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
		Description:           proto(resourceDescription(req.Description, req.Tags)),
		AutoCreateSubnetworks: proto(false), // Use custom subnet mode
	}

	op, err := m.client.getNetworksClient().Insert(ctx, &computepb.InsertNetworkRequest{ //nolint:varnamelen
		Project:         projectID,
		NetworkResource: network,
	})
	if err != nil {
		wrapped := WrapGCPError(err, "CreateNetwork")
		if IsAlreadyExists(wrapped) {
			existing, getErr := m.client.getNetworksClient().Get(ctx, &computepb.GetNetworkRequest{Project: projectID, Network: req.Name})
			if getErr != nil {
				return nil, WrapGCPError(getErr, "CreateNetwork.Reconcile")
			}

			if !matchesFilters(existing.GetDescription(), req.Tags) || len(req.Tags) == 0 || existing.GetAutoCreateSubnetworks() {
				return nil, fmt.Errorf("existing network %s does not match requested ownership/configuration", req.Name)
			}

			return m.convertNetwork(existing), nil
		}

		return nil, wrapped
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
		wrapped := WrapGCPError(err, "DeleteNetwork")
		if IsNotFound(wrapped) {
			return nil
		}

		return wrapped
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
		Description:           proto(resourceDescription("", req.Tags)),
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
		wrapped := WrapGCPError(err, "CreateSubnet")
		if IsAlreadyExists(wrapped) {
			existing, getErr := m.client.getSubnetworksClient().Get(ctx, &computepb.GetSubnetworkRequest{Project: projectID, Region: region, Subnetwork: req.Name})
			if getErr != nil {
				return nil, WrapGCPError(getErr, "CreateSubnet.Reconcile")
			}

			if len(req.Tags) == 0 || !matchesFilters(existing.GetDescription(), req.Tags) || existing.GetIpCidrRange() != req.CIDR || canonicalURL(existing.GetNetwork()) != canonicalURL(networkURL) || existing.GetPrivateIpGoogleAccess() != config.EnablePrivateGoogleAccess {
				return nil, fmt.Errorf("existing subnet %s does not match requested ownership/configuration", req.Name)
			}

			return m.convertSubnet(existing), nil
		}

		return nil, wrapped
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
	projectID, region, id = regionalScope(projectID, region, id)

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
	projectID, region, id = regionalScope(projectID, region, id)

	op, err := m.client.getSubnetworksClient().Delete(ctx, &computepb.DeleteSubnetworkRequest{ //nolint:varnamelen
		Project:    projectID,
		Region:     region,
		Subnetwork: id,
	})
	if err != nil {
		wrapped := WrapGCPError(err, "DeleteSubnet")
		if IsNotFound(wrapped) {
			return nil
		}

		return wrapped
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
	return (&SecurityManager{client: m.client}).CreateSecurityGroup(ctx, req)
}

// GetSecurityGroup retrieves a firewall rule by name.
func (m *NetworkManager) GetSecurityGroup(ctx context.Context, id string) (*cpi.SecurityGroup, error) {
	return (&SecurityManager{client: m.client}).GetSecurityGroup(ctx, id)
}

// ListSecurityGroups lists firewall rules.
func (m *NetworkManager) ListSecurityGroups(ctx context.Context, _filters map[string]string) ([]*cpi.SecurityGroup, error) {
	return (&SecurityManager{client: m.client}).ListSecurityGroups(ctx, _filters)
}

// DeleteSecurityGroup deletes a firewall rule.
func (m *NetworkManager) DeleteSecurityGroup(ctx context.Context, id string) error {
	return (&SecurityManager{client: m.client}).DeleteSecurityGroup(ctx, id)
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
	projectID, region, id = regionalScope(projectID, region, id)

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
	projectID, region, id = regionalScope(projectID, region, id)

	op, err := m.client.getAddressesClient().Delete(ctx, &computepb.DeleteAddressRequest{ //nolint:varnamelen
		Project: projectID,
		Region:  region,
		Address: id,
	})
	if err != nil {
		wrapped := WrapGCPError(err, "DeletePublicIP")
		if IsNotFound(wrapped) {
			return nil
		}

		return wrapped
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
	projectID, region, id = regionalScope(projectID, region, id)

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

		if address.GetAddressType() == "EXTERNAL" && matchesLabelFilters(address.GetLabels(), _filters) {
			floatingIPs = append(floatingIPs, m.convertAddressToFloatingIP(address))
		}
	}

	return floatingIPs, nil
}

// AssociateFloatingIP associates a floating IP with an instance.
func (m *NetworkManager) AssociateFloatingIP(ctx context.Context, ipID, instanceID string) error {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return err
	}

	cfg := m.client.getConfig()
	project, zone, name := resourceScope(cfg.ProjectID, cfg.Zone, instanceID)

	addressProject, addressRegion, addressName := regionalScope(project, GetRegionFromZone(zone), ipID)
	if addressProject != project || addressRegion != GetRegionFromZone(zone) {
		return errors.New("address and instance must share project and region")
	}

	address, err := m.client.getAddressesClient().Get(ctx, &computepb.GetAddressRequest{Project: addressProject, Region: addressRegion, Address: addressName})
	if err != nil {
		return WrapGCPError(err, "AssociateFloatingIP.GetAddress")
	}

	for _, user := range address.GetUsers() {
		if ExtractNameFromURL(user) != name || ExtractZoneFromURL(user) != zone {
			return errors.New("address is already associated with another resource")
		}
	}

	vm, err := m.client.getInstancesClient().Get(ctx, &computepb.GetInstanceRequest{Project: project, Zone: zone, Instance: name})
	if err != nil {
		return WrapGCPError(err, "AssociateFloatingIP.GetInstance")
	}

	if len(vm.GetNetworkInterfaces()) != 1 {
		return errors.New("floating IP association requires exactly one network interface")
	}

	nic := vm.GetNetworkInterfaces()[0]
	for _, access := range nic.GetAccessConfigs() {
		if access.GetNatIP() == address.GetAddress() {
			return nil
		}
	}

	if len(nic.GetAccessConfigs()) > 1 {
		return errors.New("multiple access configurations require explicit selection")
	}

	return m.replaceAccessConfig(ctx, project, zone, name, nic, address)
}

func (m *NetworkManager) replaceAccessConfig(ctx context.Context, project, zone, name string, nic *computepb.NetworkInterface, address *computepb.Address) error {
	var (
		err      error
		previous *computepb.AccessConfig
	)
	if len(nic.GetAccessConfigs()) == 1 {
		previous = nic.GetAccessConfigs()[0]
		if err = m.deleteAccessConfig(ctx, project, zone, name, nic.GetName(), previous.GetName()); err != nil {
			return err
		}
	}

	access := &computepb.AccessConfig{Name: proto("External NAT"), Type: proto("ONE_TO_ONE_NAT"), NatIP: proto(address.GetAddress()), NetworkTier: proto(address.GetNetworkTier())}
	if access.GetNetworkTier() == "" {
		access.NetworkTier = proto("PREMIUM")
	}

	if err = m.addAccessConfig(ctx, project, zone, name, nic.GetName(), access); err != nil {
		if previous != nil {
			rollbackErr := m.addAccessConfig(ctx, project, zone, name, nic.GetName(), previous)
			if rollbackErr != nil {
				return fmt.Errorf("associate floating IP: %w; restore previous access configuration: %w", err, rollbackErr)
			}
		}

		return err
	}

	return nil
}
func (m *NetworkManager) addAccessConfig(ctx context.Context, project, zone, instance, nic string, access *computepb.AccessConfig) error {
	op, err := m.client.getInstancesClient().AddAccessConfig(ctx, &computepb.AddAccessConfigInstanceRequest{Project: project, Zone: zone, Instance: instance, NetworkInterface: nic, AccessConfigResource: access})
	if err != nil {
		return WrapGCPError(err, "AddAccessConfig")
	}

	return WrapGCPError(op.Wait(ctx), "AddAccessConfig.Wait")
}
func (m *NetworkManager) deleteAccessConfig(ctx context.Context, project, zone, instance, nic, access string) error {
	op, err := m.client.getInstancesClient().DeleteAccessConfig(ctx, &computepb.DeleteAccessConfigInstanceRequest{Project: project, Zone: zone, Instance: instance, NetworkInterface: nic, AccessConfig: access})
	if err != nil {
		return WrapGCPError(err, "DeleteAccessConfig")
	}

	return WrapGCPError(op.Wait(ctx), "DeleteAccessConfig.Wait")
}
func (m *NetworkManager) DisassociateFloatingIP(ctx context.Context, ipID string) error {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return err
	}

	cfg := m.client.getConfig()
	addressProject, addressRegion, addressName := regionalScope(cfg.ProjectID, cfg.Region, ipID)

	address, err := m.client.getAddressesClient().Get(ctx, &computepb.GetAddressRequest{Project: addressProject, Region: addressRegion, Address: addressName})
	if err != nil {
		return WrapGCPError(err, "DisassociateFloatingIP.GetAddress")
	}

	for _, user := range address.GetUsers() {
		if !strings.Contains(user, "/instances/") {
			return errors.New("address is associated with a non-instance resource")
		}

		project, zone, name := resourceScope(cfg.ProjectID, cfg.Zone, user)

		vm, getErr := m.client.getInstancesClient().Get(ctx, &computepb.GetInstanceRequest{Project: project, Zone: zone, Instance: name})
		if getErr != nil {
			return WrapGCPError(getErr, "DisassociateFloatingIP.GetInstance")
		}

		for _, nic := range vm.GetNetworkInterfaces() {
			for _, access := range nic.GetAccessConfigs() {
				if access.GetNatIP() == address.GetAddress() {
					if err = m.deleteAccessConfig(ctx, project, zone, name, nic.GetName(), access.GetName()); err != nil {
						return err
					}
				}
			}
		}
	}

	return nil
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
	projectID, region, id = regionalScope(projectID, region, id)

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
	projectID, region, id = regionalScope(projectID, region, id)

	op, err := m.client.getRoutersClient().Delete(ctx, &computepb.DeleteRouterRequest{ //nolint:varnamelen
		Project: projectID,
		Region:  region,
		Router:  id,
	})
	if err != nil {
		wrapped := WrapGCPError(err, "DeleteRouter")
		if IsNotFound(wrapped) {
			return nil
		}

		return wrapped
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "DeleteRouter.Wait")
	}

	logger.Debugw("Deleted Cloud Router", "name", id, "region", region)

	return nil
}

// Helper functions

func (m *NetworkManager) convertNetwork(network *computepb.Network) *cpi.Network {
	return &cpi.Network{
		ID:        network.GetName(),
		Name:      network.GetName(),
		Tags:      resourceTags(network.GetDescription()),
		State:     cpi.ResourceStateActive,
		CreatedAt: ParseTimestamp(network.GetCreationTimestamp()),
	}
}

func (m *NetworkManager) convertSubnet(subnet *computepb.Subnetwork) *cpi.Subnet {
	return &cpi.Subnet{
		ID:               subnet.GetName(),
		Name:             subnet.GetName(),
		Tags:             resourceTags(subnet.GetDescription()),
		NetworkID:        ExtractNameFromURL(subnet.GetNetwork()),
		CIDR:             subnet.GetIpCidrRange(),
		AvailabilityZone: ExtractRegionFromURL(subnet.GetSelfLink()),
		State:            cpi.ResourceStateActive,
		CreatedAt:        ParseTimestamp(subnet.GetCreationTimestamp()),
	}
}

func (m *NetworkManager) convertAddressToPublicIP(address *computepb.Address) *cpi.PublicIP {
	status := "available"
	if address.GetStatus() == AddressStatusInUse {
		status = "associated"
	}

	return &cpi.PublicIP{
		ID:        regionalIdentity(address.GetRegion(), "addresses", address.GetName()),
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
		ID:        regionalIdentity(address.GetRegion(), "addresses", address.GetName()),
		Address:   address.GetAddress(),
		Status:    status,
		Tags:      decodedLabels(address.GetLabels()),
		CreatedAt: ParseTimestamp(address.GetCreationTimestamp()),
	}
}

func (m *NetworkManager) convertRouter(router *computepb.Router) *cpi.Router {
	return &cpi.Router{
		ID:        router.GetName(),
		Name:      router.GetName(),
		NetworkID: ExtractNameFromURL(router.GetNetwork()),
		State:     cpi.ResourceStateActive,
		CreatedAt: ParseTimestamp(router.GetCreationTimestamp()),
	}
}

func matchesFilters(description string, filters map[string]string) bool {
	if len(filters) == 0 {
		return true
	}

	tags := resourceTags(description)
	if tags == nil {
		return false
	}

	for key, value := range filters {
		if logicalTag(tags, key) != value {
			return false
		}
	}

	return true
}
