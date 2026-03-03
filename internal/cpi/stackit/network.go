package stackit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas"
)

// NetworkManager handles STACKIT network operations.
type NetworkManager struct {
	client *Client
}

// Load Balancer operations - delegate to the SDK-backed LoadBalancerManager

// CreateLoadBalancer creates a new load balancer (SDK-backed).
func (m *NetworkManager) CreateLoadBalancer(ctx context.Context, config *cpi.LoadBalancer) (*cpi.LoadBalancer, error) {
	logger.WithOperation("CreateLoadBalancer").Infof("Creating load balancer via SDK: %s", config.Name)
	req := &cpi.CreateLoadBalancerRequest{
		Name:           config.Name,
		Type:           "",
		Scheme:         "",
		NetworkID:      config.NetworkID,
		SubnetIDs:      []string{},
		SecurityGroups: config.SecurityGroups,
		Tags:           mapFromSlice(config.Tags),
	}

	return m.client.loadBalancer.CreateLoadBalancer(ctx, req)
}

// GetLoadBalancer retrieves a load balancer by name or ID.
func (m *NetworkManager) GetLoadBalancer(ctx context.Context, nameOrID string) (*cpi.LoadBalancer, error) {
	logger.WithOperation("GetLoadBalancer").Debugf("Getting load balancer via SDK: %s", nameOrID)

	return m.client.loadBalancer.GetLoadBalancer(ctx, nameOrID)
}

// ListLoadBalancers lists all load balancers.
func (m *NetworkManager) ListLoadBalancers(ctx context.Context, filters map[string]string) ([]*cpi.LoadBalancer, error) {
	logger.WithOperation("ListLoadBalancers").Debug("Listing load balancers via SDK")

	return m.client.loadBalancer.ListLoadBalancers(ctx, filters)
}

// UpdateLoadBalancer updates a load balancer.
func (m *NetworkManager) UpdateLoadBalancer(ctx context.Context, lb *cpi.LoadBalancer) error {
	logger.WithOperation("UpdateLoadBalancer").Infof("Updating load balancer via SDK: %s", lb.ID)
	req := &cpi.UpdateLoadBalancerRequest{Name: &lb.Name, SecurityGroups: lb.SecurityGroups, Tags: mapFromSlice(lb.Tags)}

	return m.client.loadBalancer.UpdateLoadBalancer(ctx, lb.ID, req)
}

// DeleteLoadBalancer deletes a load balancer.
func (m *NetworkManager) DeleteLoadBalancer(ctx context.Context, loadBalancerID string) error {
	logger.WithOperation("DeleteLoadBalancer").Infof("Deleting load balancer via SDK: %s", loadBalancerID)

	return m.client.loadBalancer.DeleteLoadBalancer(ctx, loadBalancerID)
}

// GetBackendPools retrieves backend pools for a load balancer.
func (m *NetworkManager) GetBackendPools(_ctx context.Context, lbID string) ([]*cpi.BackendPool, error) {
	logger.WithOperation("GetBackendPools").Debugf("Listing backend pools via SDK for %s", lbID)
	// Not directly exposed; return empty slice for now
	return []*cpi.BackendPool{}, nil
}

// AddBackendMember adds a backend member to a load balancer.
func (m *NetworkManager) AddBackendMember(ctx context.Context, lbID string, member *cpi.BackendMember) error {
	logger.WithOperation("AddBackendMember").Infof("Adding backend %s to LB %s", member.IPAddress, lbID)
	backend := &cpi.Backend{
		ID:      "",
		Name:    "",
		Address: member.IPAddress,
		Port:    member.Port,
		Weight:  0,
		Enabled: true,
		Health:  "",
	}

	return m.client.loadBalancer.AddBackend(ctx, lbID, backend)
}

// RemoveBackendMember removes a backend member from a load balancer.
func (m *NetworkManager) RemoveBackendMember(ctx context.Context, lbID string, memberIP string) error {
	logger.WithOperation("RemoveBackendMember").Infof("Removing backend %s from LB %s", memberIP, lbID)

	return m.client.loadBalancer.RemoveBackend(ctx, lbID, memberIP)
}

// ConfigureHealthCheck configures health check for a load balancer.
func (m *NetworkManager) ConfigureHealthCheck(ctx context.Context, lbID string, check *cpi.HealthCheck) error {
	logger.WithOperation("ConfigureHealthCheck").Infof("Configuring health check for LB %s", lbID)

	return m.client.loadBalancer.ConfigureHealthCheck(ctx, lbID, check)
}

// GetLoadBalancerHealth retrieves health status for a load balancer.
func (m *NetworkManager) GetLoadBalancerHealth(ctx context.Context, lbID string) (*cpi.HealthStatus, error) {
	logger.WithOperation("GetLoadBalancerHealth").Debugf("Getting health status for LB %s", lbID)

	return m.client.loadBalancer.GetHealthStatus(ctx, lbID)
}

// CreateNetwork creates a new network.
func (m *NetworkManager) CreateNetwork(ctx context.Context, req *cpi.NetworkRequest) (*cpi.Network, error) {
	logger.WithOperation("CreateNetwork").Infof("Creating network via SDK: %s", req.Name)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	payload := m.buildNetworkPayload(req)

	created, err := cli.CreateNetwork(ctx, m.client.config.ProjectID).CreateNetworkPayload(payload).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas CreateNetwork failed: %w", err)
	}

	networkID := stringOrEmpty(created.GetNetworkIdOk())
	logger.WithOperation("CreateNetwork").Infof("Network created: %s with CIDR: %s", networkID, req.CIDR)

	out := m.buildNetworkFromResponse(created, req)

	return out, nil
}

// GetNetwork retrieves a network by ID.
func (m *NetworkManager) GetNetwork(ctx context.Context, networkID string) (*cpi.Network, error) {
	logger.WithOperation("GetNetwork").Debugf("Getting network via SDK: %s", networkID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	got, err := cli.GetNetwork(ctx, m.client.config.ProjectID, networkID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas GetNetwork failed: %w", err)
	}

	out := &cpi.Network{
		ID:         stringOrEmpty(got.GetNetworkIdOk()),
		Name:       stringOrEmpty(got.GetNameOk()),
		CIDR:       "",
		Region:     "",
		State:      cpi.ResourceStateUnknown,
		Tags:       map[string]string{},
		DNSServers: []string{},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	out.Tags = mapAnyToString(got.GetLabels())

	return out, nil
}

// ListNetworks lists all networks.
func (m *NetworkManager) ListNetworks(ctx context.Context, filters map[string]string) ([]*cpi.Network, error) {
	logger.WithOperation("ListNetworks").Debug("Listing networks via SDK")

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	resp, err := cli.ListNetworks(ctx, m.client.config.ProjectID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas ListNetworks failed: %w", err)
	}

	items, _ := resp.GetItemsOk()

	list := make([]*cpi.Network, 0, len(items))

	for _, network := range items {
		labels := mapAnyToString(network.GetLabels())

		// Apply label filtering - skip resources without required metadata
		if !matchLabels(labels, filters) {
			continue
		}

		out := &cpi.Network{
			ID:         stringOrEmpty(network.GetNetworkIdOk()),
			Name:       stringOrEmpty(network.GetNameOk()),
			CIDR:       "",
			Region:     "",
			State:      cpi.ResourceStateUnknown,
			Tags:       labels,
			DNSServers: []string{},
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
		list = append(list, out)
	}

	logger.WithOperation("ListNetworks").Debugf("Found %d networks (after filtering)", len(list))

	return list, nil
}

// DeleteNetwork deletes a network.
func (m *NetworkManager) DeleteNetwork(ctx context.Context, networkID string) error {
	logger.WithOperation("DeleteNetwork").Infof("Deleting network: %s", networkID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	err = cli.DeleteNetwork(ctx, m.client.config.ProjectID, networkID).Execute()
	if err != nil {
		return fmt.Errorf("stackit iaas DeleteNetwork failed: %w", err)
	}

	logger.WithOperation("DeleteNetwork").Infof("Network deleted: %s", networkID)

	return nil
}

// CreateSubnet creates a subnet.
func (m *NetworkManager) CreateSubnet(_ctx context.Context, _req *cpi.SubnetRequest) (*cpi.Subnet, error) {
	return nil, ErrSubnetsNotSupported
}

// GetSubnet retrieves a subnet.
func (m *NetworkManager) GetSubnet(_ctx context.Context, _id string) (*cpi.Subnet, error) {
	return nil, ErrSubnetsNotSupportedUseNetworksAndLabels
}

// ListSubnets lists subnets in a network.
func (m *NetworkManager) ListSubnets(_ctx context.Context, networkID string) ([]*cpi.Subnet, error) {
	// STACKIT does not expose subnets; return empty list
	logger.WithOperation("ListSubnets").Debugf("STACKIT: returning no subnets for network %s", networkID)

	return []*cpi.Subnet{}, nil
}

// DeleteSubnet deletes a subnet.
func (m *NetworkManager) DeleteSubnet(_ctx context.Context, id string) error {
	logger.WithOperation("DeleteSubnet").Infof("STACKIT: subnets unsupported; nothing to delete: %s", id)

	return nil
}

// AllocateFloatingIP allocates a floating IP.
func (m *NetworkManager) AllocateFloatingIP(ctx context.Context, req *cpi.AllocateFloatingIPRequest) (*cpi.FloatingIP, error) {
	logger.WithOperation("AllocateFloatingIP").Info("Allocating public IP (floating)")
	// Delegate to CreatePublicIP with labels
	create := &cpi.PublicIPRequest{
		Name:      "",
		Job:       "",
		Index:     "",
		NetworkID: "",
		Labels:    req.Tags,
		Tags:      map[string]string{},
	}

	publicIP, err := m.CreatePublicIP(ctx, create)
	if err != nil {
		return nil, err
	}

	return &cpi.FloatingIP{
		ID:         publicIP.ID,
		Address:    publicIP.Address,
		Status:     "",
		InstanceID: "",
		NetworkID:  req.NetworkID,
		Tags:       publicIP.Labels,
		CreatedAt:  time.Now(),
	}, nil
}

// GetFloatingIP retrieves a floating IP.
func (m *NetworkManager) GetFloatingIP(ctx context.Context, floatingIPID string) (*cpi.FloatingIP, error) {
	iaasClient, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	got, err := iaasClient.GetPublicIP(ctx, m.client.config.ProjectID, floatingIPID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas GetPublicIP failed: %w", err)
	}

	out := &cpi.FloatingIP{
		ID:         stringOrEmpty(got.GetIdOk()),
		Address:    stringOrEmpty(got.GetIpOk()),
		Status:     "",
		InstanceID: "",
		NetworkID:  "",
		Tags:       map[string]string{},
		CreatedAt:  time.Now(),
	}
	if ni, ok := got.GetNetworkInterfaceOk(); ok && ni != nil {
		out.NetworkID = *ni
	}

	return out, nil
}

// ListFloatingIPs lists floating IPs.
func (m *NetworkManager) ListFloatingIPs(ctx context.Context, _filters map[string]string) ([]*cpi.FloatingIP, error) {
	iaasClient, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	// Note: STACKIT doesn't support server-side filtering for public IPs via tags
	// Filters would need to be applied client-side if needed
	resp, err := iaasClient.ListPublicIPs(ctx, m.client.config.ProjectID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas ListPublicIPs failed: %w", err)
	}

	items, _ := resp.GetItemsOk()

	list := make([]*cpi.FloatingIP, 0, len(items))
	for _, publicIPItem := range items {
		floatingIP := &cpi.FloatingIP{
			ID:         stringOrEmpty(publicIPItem.GetIdOk()),
			Address:    stringOrEmpty(publicIPItem.GetIpOk()),
			Status:     "",
			InstanceID: "",
			NetworkID:  "",
			Tags:       map[string]string{},
			CreatedAt:  time.Now(),
		}
		if ni, ok := publicIPItem.GetNetworkInterfaceOk(); ok && ni != nil {
			floatingIP.NetworkID = *ni
		}

		list = append(list, floatingIP)
	}

	return list, nil
}

// AssociateFloatingIP associates a floating IP with an instance.
func (m *NetworkManager) AssociateFloatingIP(ctx context.Context, ipID string, instanceID string) error {
	logger.WithOperation("AssociateFloatingIP").Infof("Associating public IP %s with server %s", ipID, instanceID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	err = cli.AddPublicIpToServer(ctx, m.client.config.ProjectID, instanceID, ipID).Execute()
	if err != nil {
		return fmt.Errorf("failed to associate floating IP %s to instance %s: %w", ipID, instanceID, err)
	}

	return nil
}

// DisassociateFloatingIP disassociates a floating IP.
func (m *NetworkManager) DisassociateFloatingIP(ctx context.Context, ipID string) error {
	logger.WithOperation("DisassociateFloatingIP").Infof("Disassociating public IP %s", ipID)
	// Need the server ID to remove association; this CPI method lacks it.
	// Fallback: list servers and find NIC that has this IP, then remove.
	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}
	// Find server by walking NICs and public IP mapping
	servers, err := cli.ListServers(ctx, m.client.config.ProjectID).Execute()
	if err != nil {
		return fmt.Errorf("list servers failed: %w", err)
	}

	items, _ := servers.GetItemsOk()
	for _, s := range items {
		sid, ok := s.GetIdOk()
		if !ok {
			continue
		}

		nicsResp, err := cli.ListServerNics(ctx, m.client.config.ProjectID, sid).Execute()
		if err != nil {
			continue
		}

		if nics, ok := nicsResp.GetItemsOk(); ok {
			for _, nic := range nics {
				if nid, ok := nic.GetIdOk(); ok {
					found, err := m.checkPublicIPMatch(ctx, cli, ipID, nid, sid)
					if err == nil && found {
						return nil
					}
				}
			}
		}
	}

	return ErrCouldNotFindServerAssociatedWithPublicIP(ipID)
}

// ReleaseFloatingIP releases a floating IP.
func (m *NetworkManager) ReleaseFloatingIP(ctx context.Context, floatingIPID string) error {
	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	err = cli.DeletePublicIP(ctx, m.client.config.ProjectID, floatingIPID).Execute()
	if err != nil {
		return fmt.Errorf("stackit iaas DeletePublicIP failed: %w", err)
	}

	return nil
}

// CreateRouter creates a router.
func (m *NetworkManager) CreateRouter(_ctx context.Context, req *cpi.CreateRouterRequest) (*cpi.Router, error) {
	logger.WithOperation("CreateRouter").Infof("Creating router: %s", req.Name)

	// Mock implementation
	router := &cpi.Router{
		ID:              "router-123", // Fixed ID for testing
		Name:            req.Name,
		NetworkID:       "",
		ExternalGateway: "",
		State:           cpi.ResourceStateUnknown,
		Routes:          []*cpi.Route{},
		Interfaces:      []string{},
		Tags:            map[string]string{},
		CreatedAt:       time.Now(),
	}

	logger.WithOperation("CreateRouter").Infof("Router created: %s", router.ID)

	// Pending: implement actual STACKIT API call
	return router, nil
}

// GetRouter retrieves a router.
func (m *NetworkManager) GetRouter(_ctx context.Context, _id string) (*cpi.Router, error) {
	// Pending: implement
	return nil, ErrNotImplemented
}

// ListRouters lists routers.
func (m *NetworkManager) ListRouters(_ctx context.Context) ([]*cpi.Router, error) {
	// Pending: implement
	return nil, ErrNotImplemented
}

// AttachRouterInterface attaches a subnet to a router.
func (m *NetworkManager) AttachRouterInterface(_ctx context.Context, routerID string, subnetID string) error {
	logger.WithOperation("AttachRouterInterface").Infof("Attaching subnet %s to router %s", subnetID, routerID)
	// Pending: implement actual STACKIT API call
	return nil
}

// DetachRouterInterface detaches a subnet from a router.
func (m *NetworkManager) DetachRouterInterface(_ctx context.Context, routerID string, subnetID string) error {
	logger.WithOperation("DetachRouterInterface").Infof("Detaching subnet %s from router %s", subnetID, routerID)
	// Pending: implement actual STACKIT API call
	return nil
}

// DeleteRouter deletes a router.
func (m *NetworkManager) DeleteRouter(_ctx context.Context, _id string) error {
	// Pending: implement
	return ErrNotImplemented
}

// Public IP operations

// CreateSecurityGroup creates a security group.
func (m *NetworkManager) CreateSecurityGroup(ctx context.Context, req *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	return m.client.security.CreateSecurityGroup(ctx, req)
}

// GetSecurityGroup retrieves a security group by ID.
func (m *NetworkManager) GetSecurityGroup(ctx context.Context, id string) (*cpi.SecurityGroup, error) {
	return m.client.security.GetSecurityGroup(ctx, id)
}

// ListSecurityGroups lists all security groups.
func (m *NetworkManager) ListSecurityGroups(ctx context.Context, filters map[string]string) ([]*cpi.SecurityGroup, error) {
	return m.client.security.ListSecurityGroups(ctx, filters)
}

// DeleteSecurityGroup deletes a security group.
func (m *NetworkManager) DeleteSecurityGroup(ctx context.Context, id string) error {
	return m.client.security.DeleteSecurityGroup(ctx, id)
}

// CreatePublicIP creates a public IP address.
func (m *NetworkManager) CreatePublicIP(ctx context.Context, req *cpi.PublicIPRequest) (*cpi.PublicIP, error) {
	logger.WithOperation("CreatePublicIP").Infof("Creating public IP: %s (job: %s, index: %s)", req.Name, req.Job, req.Index)

	labels := buildPublicIPLabels(req)

	created, err := m.createPublicIPViaSDK(ctx, labels)
	if err != nil {
		return nil, err
	}

	publicIP := buildPublicIPFromResponse(created)
	logger.WithOperation("CreatePublicIP").Infof("Public IP created: %s (%s)", publicIP.ID, publicIP.Address)

	return publicIP, nil
}

// ListPublicIPs lists public IPs with optional filtering.
func (m *NetworkManager) ListPublicIPs(ctx context.Context) ([]*cpi.PublicIP, error) {
	logger.WithOperation("ListPublicIPs").Debug("Listing public IPs")

	iaasClient, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	resp, err := iaasClient.ListPublicIPs(ctx, m.client.config.ProjectID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas ListPublicIPs failed: %w", err)
	}

	items, _ := resp.GetItemsOk()

	list := make([]*cpi.PublicIP, 0, len(items))

	for _, ip := range items {
		labels := mapAnyToString(ip.GetLabels())
		ipAddress := stringOrEmpty(ip.GetIpOk())

		out := &cpi.PublicIP{
			ID:         stringOrEmpty(ip.GetIdOk()),
			IPAddress:  ipAddress, // Set IPAddress for bootstrap code compatibility
			Address:    ipAddress, // Set Address for other code compatibility
			Name:       "",
			Status:     "",
			Job:        "",
			Index:      "",
			InstanceID: "",
			NetworkID:  "",
			Labels:     labels,
			Tags:       map[string]string{},
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
		if labels != nil {
			out.Job = labels["job"]
			out.Index = labels["index"]
			out.Name = labels["name"]
		}

		list = append(list, out)
	}

	return list, nil
}

// ListPublicIPsWithFilters - STACKIT-specific method that supports filtering.
func (m *NetworkManager) ListPublicIPsWithFilters(ctx context.Context, filters map[string]string) ([]*cpi.PublicIP, error) {
	logger.WithOperation("ListPublicIPsWithFilters").Debugw("Listing public IPs with filters", "filters", filters)

	// Get all public IPs first
	allIPs, err := m.ListPublicIPs(ctx)
	if err != nil {
		return nil, err
	}

	if len(filters) == 0 {
		return allIPs, nil
	}

	// Filter the results
	var filteredIPs []*cpi.PublicIP

	for _, ip := range allIPs {
		if matchLabels(ip.Labels, filters) {
			filteredIPs = append(filteredIPs, ip)
		}
	}

	return filteredIPs, nil
}

// GetPublicIP retrieves a public IP by ID.
func (m *NetworkManager) GetPublicIP(ctx context.Context, publicIPID string) (*cpi.PublicIP, error) {
	logger.WithOperation("GetPublicIP").Debugf("Getting public IP: %s", publicIPID)

	iaasClient, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	got, err := iaasClient.GetPublicIP(ctx, m.client.config.ProjectID, publicIPID).Execute()
	if err != nil {
		// Pending: inspect error for 404 mapping if needed
		return nil, fmt.Errorf("stackit iaas GetPublicIP failed: %w", err)
	}

	ipAddress := stringOrEmpty(got.GetIpOk())

	out := &cpi.PublicIP{
		ID:         stringOrEmpty(got.GetIdOk()),
		IPAddress:  ipAddress, // Set IPAddress for bootstrap code compatibility
		Address:    ipAddress, // Set Address for other code compatibility
		Name:       "",
		Status:     "",
		Job:        "",
		Index:      "",
		InstanceID: "",
		NetworkID:  "",
		Labels:     mapAnyToString(got.GetLabels()),
		Tags:       map[string]string{},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if out.Labels != nil {
		out.Job = out.Labels["job"]
		out.Index = out.Labels["index"]
		out.Name = out.Labels["name"]
	}

	return out, nil
}

// DeletePublicIP deletes a public IP.
func (m *NetworkManager) DeletePublicIP(ctx context.Context, publicIPID string) error {
	logger.WithOperation("DeletePublicIP").Infof("Deleting public IP: %s", publicIPID)

	iaasClient, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	err = iaasClient.DeletePublicIP(ctx, m.client.config.ProjectID, publicIPID).Execute()
	if err != nil {
		return fmt.Errorf("stackit iaas DeletePublicIP failed: %w", err)
	}

	logger.WithOperation("DeletePublicIP").Infof("Public IP deleted: %s", publicIPID)

	return nil
}

// EnsureJumpboxPublicIPs ensures the required number of jumpbox public IPs exist.
func (m *NetworkManager) EnsureJumpboxPublicIPs(ctx context.Context, blocName string, count int) ([]*cpi.PublicIP, error) {
	if count <= 0 {
		count = 2 // Default to 2 jumpbox IPs
	}

	logger.WithOperation("EnsureJumpboxPublicIPs").Infof("Ensuring %d jumpbox public IPs for bloc %s", count, blocName)

	return m.ensurePublicIPsForJob(ctx, blocName, "jumpbox", count)
}

// EnsureOpsPublicIPs ensures there is at least one ops public IP.
func (m *NetworkManager) EnsureOpsPublicIPs(ctx context.Context, blocName string, count int) ([]*cpi.PublicIP, error) {
	if count <= 0 {
		count = 1
	}

	logger.WithOperation("EnsureOpsPublicIPs").Infof("Ensuring %d ops public IP(s) for bloc %s", count, blocName)

	return m.ensurePublicIPsForJob(ctx, blocName, "ops", count)
}

// EnsureRouterPublicIPs ensures the required number of router public IPs exist.
func (m *NetworkManager) EnsureRouterPublicIPs(ctx context.Context, blocName string, count int) ([]*cpi.PublicIP, error) {
	if count <= 0 {
		count = 4 // Default to 4 router IPs
	}

	logger.WithOperation("EnsureRouterPublicIPs").Infof("Ensuring %d router public IPs for bloc %s", count, blocName)

	return m.ensurePublicIPsForJob(ctx, blocName, "router", count)
}

// EnsureCFSSHPublicIPs ensures the required number of cf-ssh public IPs exist.
func (m *NetworkManager) EnsureCFSSHPublicIPs(ctx context.Context, blocName string, count int) ([]*cpi.PublicIP, error) {
	if count <= 0 {
		count = 1 // Default to 1 cf-ssh IP
	}

	logger.WithOperation("EnsureCFSSHPublicIPs").Infof("Ensuring %d cf-ssh public IPs for bloc %s", count, blocName)

	return m.ensurePublicIPsForJob(ctx, blocName, "cf-ssh", count)
}

// EnsureTCPRouterPublicIPs ensures the required number of tcp-router public IPs exist.
func (m *NetworkManager) EnsureTCPRouterPublicIPs(ctx context.Context, blocName string, count int) ([]*cpi.PublicIP, error) {
	if count <= 0 {
		count = 2 // Default to 2 tcp-router IPs
	}

	logger.WithOperation("EnsureTCPRouterPublicIPs").Infof("Ensuring %d tcp-router public IPs for bloc %s", count, blocName)

	return m.ensurePublicIPsForJob(ctx, blocName, "tcp-router", count)
}

// ListNetworkInterfaces lists all network interfaces in the project.
func (m *NetworkManager) ListNetworkInterfaces(ctx context.Context, filters map[string]string) ([]*cpi.NetworkInterface, error) {
	logger.WithOperation("ListNetworkInterfaces").Debug("Listing network interfaces via SDK")

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, fmt.Errorf("failed to get IAAS client: %w", err)
	}

	networks, err := m.fetchNetworkList(ctx, cli)
	if err != nil {
		return nil, err
	}

	networkInterfaces := m.collectNetworkInterfaces(ctx, cli, networks, filters)

	logger.WithOperation("ListNetworkInterfaces").Debugf("Found %d network interfaces", len(networkInterfaces))

	return networkInterfaces, nil
}

// DeleteNetworkInterface deletes a network interface.
func (m *NetworkManager) DeleteNetworkInterface(ctx context.Context, nicID string) error {
	logger.WithOperation("DeleteNetworkInterface").Debugf("Deleting network interface: %s", nicID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return fmt.Errorf("failed to get IAAS client: %w", err)
	}

	// List all networks to find which network this NIC belongs to
	networksResp, err := cli.ListNetworks(ctx, m.client.config.ProjectID).Execute()
	if err != nil {
		return fmt.Errorf("failed to list networks: %w", err)
	}

	networks, ok := networksResp.GetItemsOk()
	if !ok {
		return ErrNoNetworksFound
	}

	// Find the network that contains this NIC
	var foundNetworkID string

	for _, network := range networks {
		networkID, exists := network.GetNetworkIdOk()
		if !exists || networkID == "" {
			continue
		}

		nicsResp, err := cli.ListNics(ctx, m.client.config.ProjectID, networkID).Execute()
		if err != nil {
			continue
		}

		items, ok := nicsResp.GetItemsOk()
		if !ok {
			continue
		}

		for _, nic := range items {
			if nicIDVal, ok := nic.GetIdOk(); ok && nicIDVal == nicID {
				foundNetworkID = networkID

				break
			}
		}

		if foundNetworkID != "" {
			break
		}
	}

	if foundNetworkID == "" {
		return fmt.Errorf("%w: %s", ErrNetworkInterfaceNotFound, nicID)
	}

	// Delete the NIC from its network
	err = cli.DeleteNic(ctx, m.client.config.ProjectID, foundNetworkID, nicID).Execute()
	if err != nil {
		return fmt.Errorf("failed to delete network interface: %w", err)
	}

	logger.WithOperation("DeleteNetworkInterface").Debugf("Network interface deleted: %s", nicID)

	return nil
}

// helper: convert []string tags into a map for requests
func mapFromSlice(tags []string) map[string]string {
	if len(tags) == 0 {
		return nil
	}

	m := make(map[string]string, len(tags))
	for _, t := range tags {
		m[t] = "true"
	}

	return m
}

// buildNetworkPayload builds the network creation payload.
func (m *NetworkManager) buildNetworkPayload(req *cpi.NetworkRequest) iaas.CreateNetworkPayload {
	// Build payload with direct struct initialization (matching STACKIT CLI pattern)
	namePtr := req.Name
	routed := true

	payload := iaas.CreateNetworkPayload{
		Name:   &namePtr,
		Routed: &routed,
	}

	// Set labels using setter with STACKIT sanitization
	if len(req.Tags) > 0 {
		labels := sanitizeLabelsForStackit(req.Tags)
		payload.SetLabels(labels)
	}

	// Configure address family with CIDR if provided
	if req.CIDR != "" {
		addressFamily := m.buildAddressFamily(req)
		payload.AddressFamily = addressFamily
	}

	return payload
}

// buildAddressFamily builds address family configuration with CIDR and DNS.
func (m *NetworkManager) buildAddressFamily(req *cpi.NetworkRequest) *iaas.CreateNetworkAddressFamily {
	logger.WithOperation("CreateNetwork").Infof("Setting network CIDR: %s", req.CIDR)

	// Convert CIDR string to pointer
	cidrPtr := req.CIDR

	// Create IPv4 body with direct struct initialization
	ipv4Body := &iaas.CreateNetworkIPv4Body{
		Prefix: &cidrPtr,
	}

	// Set DNS nameservers if provided
	if len(req.DNSServers) > 0 {
		nameservers := req.DNSServers
		ipv4Body.Nameservers = &nameservers
	}

	// Assign Ipv4 directly to AddressFamily (not using setter)
	addressFamily := &iaas.CreateNetworkAddressFamily{
		Ipv4: ipv4Body,
	}

	return addressFamily
}

// buildNetworkFromResponse converts API response to CPI Network.
func (m *NetworkManager) buildNetworkFromResponse(created *iaas.Network, req *cpi.NetworkRequest) *cpi.Network {
	out := &cpi.Network{
		ID:         stringOrEmpty(created.GetNetworkIdOk()),
		Name:       stringOrEmpty(created.GetNameOk()),
		CIDR:       req.CIDR,
		Region:     "",
		State:      cpi.ResourceStateUnknown,
		Tags:       map[string]string{},
		DNSServers: req.DNSServers,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	out.Tags = mapAnyToString(created.GetLabels())

	return out
}

// mapAnyToString converts map[string]interface{} to map[string]string.
func mapAnyToString(input map[string]interface{}) map[string]string {
	if input == nil {
		return nil
	}

	out := make(map[string]string, len(input))
	for key, value := range input {
		switch typedValue := value.(type) {
		case string:
			out[key] = typedValue
		case fmt.Stringer:
			out[key] = typedValue.String()
		default:
			out[key] = fmt.Sprintf("%v", value)
		}
	}

	return out
}

func stringOrEmpty(val string, _ bool) string { return val }

// sanitizeLabelsForStackit converts tags/labels to STACKIT-compliant format.
// STACKIT labels must match regex for values: ^(-|_|[a-z0-9]){0,63}$
// This means: lowercase alphanumeric, hyphens, underscores only - NO colons or special chars.
// Timestamp labels (created-at, updated-at) are filtered out as they cannot be represented in valid format.
func sanitizeLabelsForStackit(tags map[string]string) map[string]interface{} {
	if tags == nil {
		return map[string]interface{}{}
	}

	labels := make(map[string]interface{}, len(tags))
	for key, value := range tags {
		// Skip timestamp labels - STACKIT doesn't support them in label values
		// STACKIT label values must match regex: ^(-|_|[a-z0-9]){0,63}$
		if key == "created-at" || key == "updated-at" || key == "created_at" || key == "updated_at" {
			continue
		}

		labels[key] = value
	}

	return labels
}

// matchLabels filters by label:foo=value filters.
// matchLabels filters by label:foo=value or plain foo=value filters.
// All filters are treated as label filters for STACKIT resources.
func matchLabels(labels map[string]string, filters map[string]string) bool {
	if len(filters) == 0 {
		return true
	}

	for filterKey, filterValue := range filters {
		var key string

		switch {
		case strings.HasPrefix(filterKey, "label:"):
			key = strings.TrimPrefix(filterKey, "label:")
		case strings.HasPrefix(filterKey, "label."):
			key = strings.TrimPrefix(filterKey, "label.")
		default:
			// Treat unprefixed filters as label filters
			key = filterKey
		}

		if labels == nil || labels[key] != filterValue {
			return false
		}
	}

	return true
}

// ensurePublicIPsForJob is a helper to ensure a number of IPs by job label.
func (m *NetworkManager) ensurePublicIPsForJob(ctx context.Context, blocName, job string, count int) ([]*cpi.PublicIP, error) {
	// Find existing IPs for this job
	existingIPs, err := m.ListPublicIPs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list existing %s IPs: %w", job, err)
	}

	ipsByKey := m.indexExistingIPsByJob(existingIPs, job)
	allIPs, errs := m.createMissingPublicIPs(ctx, blocName, job, count, ipsByKey)

	// Return partial results with errors if any creation failed
	if len(errs) > 0 {
		return allIPs, fmt.Errorf("partial failure creating %s IPs (%d/%d created): %w", job, len(allIPs), count, errors.Join(errs...))
	}

	return allIPs, nil
}

// indexExistingIPsByJob indexes existing public IPs by job and index.
func (m *NetworkManager) indexExistingIPsByJob(existingIPs []*cpi.PublicIP, job string) map[string]*cpi.PublicIP {
	ipsByKey := make(map[string]*cpi.PublicIP)

	for _, publicIP := range existingIPs {
		if publicIP.Labels == nil {
			continue
		}

		// Validate this IP belongs to the correct job
		ipJob, hasJob := publicIP.Labels["job"]
		ipIndex, hasIndex := publicIP.Labels["index"]

		if !hasJob || !hasIndex {
			continue
		}

		// Only index IPs that match this specific job
		if ipJob == job {
			key := fmt.Sprintf("%s-%s", job, ipIndex)
			ipsByKey[key] = publicIP
		}
	}

	return ipsByKey
}

// createMissingPublicIPs creates public IPs that don't already exist.
func (m *NetworkManager) createMissingPublicIPs(ctx context.Context, blocName, job string, count int, ipsByKey map[string]*cpi.PublicIP) ([]*cpi.PublicIP, []error) {
	result := make([]*cpi.PublicIP, 0, count)

	var errs []error

	for ipIndex := range count {
		indexString := strconv.Itoa(ipIndex)
		key := fmt.Sprintf("%s-%s", job, indexString)

		// Check if IP already exists with correct job and index
		if existingIP, ok := ipsByKey[key]; ok {
			logger.WithOperation("ensurePublicIPsForJob").Infof("%s IP with index %s already exists: %s", job, indexString, existingIP.Address)
			result = append(result, existingIP)

			continue
		}

		// Create new IP
		newIP, err := m.createNewPublicIP(ctx, blocName, job, ipIndex, indexString)
		if err != nil {
			logger.WithOperation("ensurePublicIPsForJob").Errorf("Failed to create %s IP with index %s: %v", job, indexString, err)
			errs = append(errs, fmt.Errorf("failed to create %s IP index %s: %w", job, indexString, err))

			continue
		}

		logger.WithOperation("ensurePublicIPsForJob").Infof("Created %s IP with index %s: %s", job, indexString, newIP.Address)
		result = append(result, newIP)
	}

	return result, errs
}

// createNewPublicIP creates a new public IP with the specified labels.
func (m *NetworkManager) createNewPublicIP(ctx context.Context, blocName, job string, ipIndex int, indexString string) (*cpi.PublicIP, error) {
	req := &cpi.PublicIPRequest{
		Name:      fmt.Sprintf("%s-%s-%d", blocName, job, ipIndex),
		Job:       job,
		Index:     indexString,
		NetworkID: "",
		Labels: map[string]string{
			"bloc": blocName,
			"env":  "mgmt",
		},
		Tags: map[string]string{},
	}

	return m.CreatePublicIP(ctx, req)
}

func buildPublicIPLabels(req *cpi.PublicIPRequest) map[string]interface{} {
	// Merge labels and tags into a single map
	mergedTags := make(map[string]string)

	// First, copy all labels from the request
	for k, v := range req.Labels {
		mergedTags[k] = v
	}

	// Then, merge in all tags (which contain bloc and other metadata)
	for k, v := range req.Tags {
		mergedTags[k] = v
	}

	// Add/override with required metadata
	mergedTags["managed-by"] = "ocfp"
	if req.Job != "" {
		mergedTags["job"] = req.Job
	}

	if req.Index != "" {
		mergedTags["index"] = req.Index
	}

	if req.Name != "" {
		mergedTags["name"] = req.Name
	}

	// Sanitize for STACKIT (handles timestamp conversion)
	return sanitizeLabelsForStackit(mergedTags)
}

func buildPublicIPFromResponse(created *iaas.PublicIp) *cpi.PublicIP {
	ipAddress := stringOrEmpty(created.GetIpOk())
	out := &cpi.PublicIP{
		ID:         stringOrEmpty(created.GetIdOk()),
		IPAddress:  ipAddress, // Set IPAddress for bootstrap code compatibility
		Address:    ipAddress, // Set Address for other code compatibility
		Name:       "",
		Status:     "",
		Job:        "",
		Index:      "",
		InstanceID: "",
		NetworkID:  "",
		Labels:     mapAnyToString(created.GetLabels()),
		Tags:       map[string]string{},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	if out.Labels != nil {
		out.Job = out.Labels["job"]
		out.Index = out.Labels["index"]
		out.Name = out.Labels["name"]
	}

	return out
}

func (m *NetworkManager) createPublicIPViaSDK(ctx context.Context, labels map[string]interface{}) (*iaas.PublicIp, error) {
	iaasClient, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	payload := iaas.CreatePublicIPPayload{
		Id:               nil,
		Ip:               nil,
		Labels:           nil,
		NetworkInterface: nil,
	}
	payload.SetLabels(labels)

	created, err := iaasClient.CreatePublicIP(ctx, m.client.config.ProjectID).
		CreatePublicIPPayload(payload).
		Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas CreatePublicIP failed: %w", err)
	}

	return created, nil
}

func (m *NetworkManager) checkPublicIPMatch(ctx context.Context, cli *iaas.APIClient, ipID string, nid string, sid string) (bool, error) {
	pip, err := cli.GetPublicIP(ctx, m.client.config.ProjectID, ipID).Execute()
	if err != nil {
		return false, fmt.Errorf("failed to get public IP %s: %w", ipID, err)
	}

	if ni, ok := pip.GetNetworkInterfaceOk(); ok && ni != nil && *ni == nid {
		err = cli.RemovePublicIpFromServer(ctx, m.client.config.ProjectID, sid, ipID).Execute()
		if err != nil {
			return true, fmt.Errorf("failed to remove public IP %s from server %s: %w", ipID, sid, err)
		}

		return true, nil
	}

	return false, nil
}

// fetchNetworkList retrieves the list of networks from the API.
func (m *NetworkManager) fetchNetworkList(ctx context.Context, cli *iaas.APIClient) ([]iaas.Network, error) {
	networksResp, err := cli.ListNetworks(ctx, m.client.config.ProjectID).Execute()
	if err != nil {
		return nil, fmt.Errorf("failed to list networks: %w", err)
	}

	networks, ok := networksResp.GetItemsOk()
	if !ok {
		logger.WithOperation("ListNetworkInterfaces").Debug("No networks found")

		return []iaas.Network{}, nil
	}

	return networks, nil
}

// collectNetworkInterfaces collects NICs from all networks with filtering.
func (m *NetworkManager) collectNetworkInterfaces(ctx context.Context, cli *iaas.APIClient, networks []iaas.Network, filters map[string]string) []*cpi.NetworkInterface {
	networkInterfaces := make([]*cpi.NetworkInterface, 0)

	for _, network := range networks {
		networkID, exists := network.GetNetworkIdOk()
		if !exists || networkID == "" {
			continue
		}

		nics := m.fetchNICsForNetwork(ctx, cli, networkID)
		filtered := m.filterAndConvertNICs(nics, networkID, filters)
		networkInterfaces = append(networkInterfaces, filtered...)
	}

	return networkInterfaces
}

// fetchNICsForNetwork retrieves NICs for a specific network.
func (m *NetworkManager) fetchNICsForNetwork(ctx context.Context, cli *iaas.APIClient, networkID string) []iaas.NIC {
	nicsResp, err := cli.ListNics(ctx, m.client.config.ProjectID, networkID).Execute()
	if err != nil {
		logger.WithOperation("ListNetworkInterfaces").Warnf("Failed to list NICs for network %s: %v", networkID, err)

		return []iaas.NIC{}
	}

	items, ok := nicsResp.GetItemsOk()
	if !ok {
		return []iaas.NIC{}
	}

	return items
}

// filterAndConvertNICs filters provider-managed NICs and applies user filters.
func (m *NetworkManager) filterAndConvertNICs(nics []iaas.NIC, networkID string, filters map[string]string) []*cpi.NetworkInterface {
	result := make([]*cpi.NetworkInterface, 0)

	for _, nic := range nics {
		// Skip metadata and gateway port NICs - they are provider-managed and cannot be deleted
		nicType, hasType := nic.GetTypeOk()
		if hasType && (nicType == "metadata" || nicType == "gateway") {
			logger.WithOperation("ListNetworkInterfaces").Debugf("Skipping provider-managed NIC type: %s (ID: %s)",
				nicType, stringOrEmpty(nic.GetIdOk()))

			continue
		}

		// Apply filters if provided
		if len(filters) > 0 {
			if !matchesNICFilters(nic, filters) {
				continue
			}
		}

		networkInterface := buildNetworkInterfaceFromSDK(nic, networkID)
		result = append(result, networkInterface)
	}

	return result
}

// buildNetworkInterfaceFromSDK converts STACKIT SDK NIC to CPI NetworkInterface.
func buildNetworkInterfaceFromSDK(nic iaas.NIC, networkID string) *cpi.NetworkInterface {
	netInterface := &cpi.NetworkInterface{
		ID:               stringOrEmpty(nic.GetIdOk()),
		Name:             stringOrEmpty(nic.GetNameOk()),
		IPv4:             stringOrEmpty(nic.GetIpv4Ok()),
		IPv6:             stringOrEmpty(nic.GetIpv6Ok()),
		MAC:              stringOrEmpty(nic.GetMacOk()),
		NetworkID:        networkID,
		SecurityGroupIDs: []string{},
		AllowedAddresses: []string{},
		Labels:           map[string]string{},
	}

	// Extract security group IDs
	if sgs, ok := nic.GetSecurityGroupsOk(); ok {
		netInterface.SecurityGroupIDs = append(netInterface.SecurityGroupIDs, sgs...)
	}

	// Extract allowed addresses
	if addrs, ok := nic.GetAllowedAddressesOk(); ok {
		for _, addr := range addrs {
			if addr.String != nil {
				netInterface.AllowedAddresses = append(netInterface.AllowedAddresses, *addr.String)
			}
		}
	}

	// Extract labels
	if labels, ok := nic.GetLabelsOk(); ok {
		for k, v := range labels {
			if strVal, ok := v.(string); ok {
				netInterface.Labels[k] = strVal
			}
		}
	}

	return netInterface
}

// matchesNICFilters checks if a NIC matches the provided filters.
func matchesNICFilters(nic iaas.NIC, filters map[string]string) bool {
	// Extract NIC labels
	labels := mapAnyToString(nic.GetLabels())

	// Apply label filtering using the standard matchLabels function
	// This enforces metadata requirements: resources without proper labels are filtered out
	if !matchLabels(labels, filters) {
		return false
	}

	// Additional filter checks for specific fields
	for key, value := range filters {
		switch key {
		case "network_id":
			// Network ID is passed separately in the context, so skip this filter
			continue
		case "instance_id":
			// Note: STACKIT NICs don't directly expose instance_id in the NIC object
			// This would require additional API calls to determine attachment
			continue
		case "name":
			if name, ok := nic.GetNameOk(); !ok || name != value {
				return false
			}
		}
	}

	return true
}
