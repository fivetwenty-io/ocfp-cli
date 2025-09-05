package stackit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

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
		NetworkID:      config.NetworkID,
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
func (m *NetworkManager) GetBackendPools(ctx context.Context, lbID string) ([]*cpi.BackendPool, error) {
	logger.WithOperation("GetBackendPools").Debugf("Listing backend pools via SDK for %s", lbID)
	// Not directly exposed; return empty slice for now
	return []*cpi.BackendPool{}, nil
}

// AddBackendMember adds a backend member to a load balancer.
func (m *NetworkManager) AddBackendMember(ctx context.Context, lbID string, member *cpi.BackendMember) error {
	logger.WithOperation("AddBackendMember").Infof("Adding backend %s to LB %s", member.IPAddress, lbID)
	backend := &cpi.Backend{Address: member.IPAddress, Port: member.Port, Name: ""}

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

// CreateNetwork creates a new network.
func (m *NetworkManager) CreateNetwork(ctx context.Context, req *cpi.CreateNetworkRequest) (*cpi.Network, error) {
	logger.WithOperation("CreateNetwork").Infof("Creating network via SDK: %s", req.Name)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	payload := iaas.NewCreateNetworkPayload(req.Name)
	if len(req.Tags) > 0 {
		lm := make(map[string]interface{}, len(req.Tags))
		for k, v := range req.Tags {
			lm[k] = v
		}

		payload.SetLabels(lm)
	}

	created, err := cli.CreateNetwork(ctx, m.client.config.ProjectID).CreateNetworkPayload(*payload).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas CreateNetwork failed: %w", err)
	}

	out := &cpi.Network{ID: stringOrEmpty(created.GetNetworkIdOk()), Name: stringOrEmpty(created.GetNameOk())}
	out.Tags = mapAnyToString(created.GetLabels())
	logger.WithOperation("CreateNetwork").Infof("Network created: %s", out.ID)

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

	out := &cpi.Network{ID: stringOrEmpty(got.GetNetworkIdOk()), Name: stringOrEmpty(got.GetNameOk())}
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

	var list []*cpi.Network

	for _, n := range items {
		labels := mapAnyToString(n.GetLabels())

		out := &cpi.Network{ID: stringOrEmpty(n.GetNetworkIdOk()), Name: stringOrEmpty(n.GetNameOk()), Tags: labels}
		if matchLabels(labels, filters) {
			list = append(list, out)
		}
	}

	logger.WithOperation("ListNetworks").Debugf("Found %d networks", len(list))

	return list, nil
}

// DeleteNetwork deletes a network.
func (m *NetworkManager) DeleteNetwork(ctx context.Context, networkID string) error {
	logger.WithOperation("DeleteNetwork").Infof("Deleting network: %s", networkID)

	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	if err := cli.DeleteNetwork(ctx, m.client.config.ProjectID, networkID).Execute(); err != nil {
		return fmt.Errorf("stackit iaas DeleteNetwork failed: %w", err)
	}

	logger.WithOperation("DeleteNetwork").Infof("Network deleted: %s", networkID)

	return nil
}

// CreateSubnet creates a subnet.
func (m *NetworkManager) CreateSubnet(ctx context.Context, req *cpi.CreateSubnetRequest) (*cpi.Subnet, error) {
	return nil, errors.New("stackit: subnets are not supported; use networks and labels")
}

// GetSubnet retrieves a subnet.
func (m *NetworkManager) GetSubnet(ctx context.Context, id string) (*cpi.Subnet, error) {
	return nil, errors.New("stackit: subnets are not supported")
}

// ListSubnets lists subnets in a network.
func (m *NetworkManager) ListSubnets(ctx context.Context, networkID string) ([]*cpi.Subnet, error) {
	// STACKIT does not expose subnets; return empty list
	logger.WithOperation("ListSubnets").Debugf("STACKIT: returning no subnets for network %s", networkID)

	return []*cpi.Subnet{}, nil
}

// DeleteSubnet deletes a subnet.
func (m *NetworkManager) DeleteSubnet(ctx context.Context, id string) error {
	logger.WithOperation("DeleteSubnet").Infof("STACKIT: subnets unsupported; nothing to delete: %s", id)

	return nil
}

// AllocateFloatingIP allocates a floating IP.
func (m *NetworkManager) AllocateFloatingIP(ctx context.Context, req *cpi.AllocateFloatingIPRequest) (*cpi.FloatingIP, error) {
	logger.WithOperation("AllocateFloatingIP").Info("Allocating public IP (floating)")
	// Delegate to CreatePublicIP with labels
	create := &cpi.CreatePublicIPRequest{Labels: req.Tags}

	ip, err := m.CreatePublicIP(ctx, create)
	if err != nil {
		return nil, err
	}

	return &cpi.FloatingIP{ID: ip.ID, Address: ip.Address, NetworkID: req.NetworkID, Tags: ip.Labels}, nil
}

// GetFloatingIP retrieves a floating IP.
func (m *NetworkManager) GetFloatingIP(ctx context.Context, id string) (*cpi.FloatingIP, error) {
	iaasClient, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	got, err := iaasClient.GetPublicIP(ctx, m.client.config.ProjectID, id).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas GetPublicIP failed: %w", err)
	}

	out := &cpi.FloatingIP{
		ID:      stringOrEmpty(got.GetIdOk()),
		Address: stringOrEmpty(got.GetIpOk()),
	}
	if ni, ok := got.GetNetworkInterfaceOk(); ok && ni != nil {
		out.NetworkID = *ni
	}

	return out, nil
}

// ListFloatingIPs lists floating IPs.
func (m *NetworkManager) ListFloatingIPs(ctx context.Context) ([]*cpi.FloatingIP, error) {
	iaasClient, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	resp, err := iaasClient.ListPublicIPs(ctx, m.client.config.ProjectID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas ListPublicIPs failed: %w", err)
	}

	items, _ := resp.GetItemsOk()

	var list []*cpi.FloatingIP
	for _, ip := range items {
		floatingIP := &cpi.FloatingIP{
			ID:      stringOrEmpty(ip.GetIdOk()),
			Address: stringOrEmpty(ip.GetIpOk()),
		}
		if ni, ok := ip.GetNetworkInterfaceOk(); ok && ni != nil {
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

	return cli.AddPublicIpToServer(ctx, m.client.config.ProjectID, instanceID, ipID).Execute()
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
					// Get public IP and see if matches
					pip, err := cli.GetPublicIP(ctx, m.client.config.ProjectID, ipID).Execute()
					if err == nil {
						if ni, ok := pip.GetNetworkInterfaceOk(); ok && ni != nil && *ni == nid {
							return cli.RemovePublicIpFromServer(ctx, m.client.config.ProjectID, sid, ipID).Execute()
						}
					}
				}
			}
		}
	}

	return fmt.Errorf("could not find server associated with public IP %s", ipID)
}

// ReleaseFloatingIP releases a floating IP.
func (m *NetworkManager) ReleaseFloatingIP(ctx context.Context, id string) error {
	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	if err := cli.DeletePublicIP(ctx, m.client.config.ProjectID, id).Execute(); err != nil {
		return fmt.Errorf("stackit iaas DeletePublicIP failed: %w", err)
	}

	return nil
}

// CreateRouter creates a router.
func (m *NetworkManager) CreateRouter(ctx context.Context, req *cpi.CreateRouterRequest) (*cpi.Router, error) {
	logger.WithOperation("CreateRouter").Infof("Creating router: %s", req.Name)

	// Mock implementation
	router := &cpi.Router{
		ID:   "router-123", // Fixed ID for testing
		Name: req.Name,
	}

	logger.WithOperation("CreateRouter").Infof("Router created: %s", router.ID)

	// TODO: Implement actual STACKIT API call
	return router, nil
}

// GetRouter retrieves a router.
func (m *NetworkManager) GetRouter(ctx context.Context, id string) (*cpi.Router, error) {
	// TODO: Implement
	return nil, errors.New("not implemented")
}

// ListRouters lists routers.
func (m *NetworkManager) ListRouters(ctx context.Context) ([]*cpi.Router, error) {
	// TODO: Implement
	return nil, errors.New("not implemented")
}

// AttachRouterInterface attaches a subnet to a router.
func (m *NetworkManager) AttachRouterInterface(ctx context.Context, routerID string, subnetID string) error {
	logger.WithOperation("AttachRouterInterface").Infof("Attaching subnet %s to router %s", subnetID, routerID)
	// TODO: Implement actual STACKIT API call
	return nil
}

// DetachRouterInterface detaches a subnet from a router.
func (m *NetworkManager) DetachRouterInterface(ctx context.Context, routerID string, subnetID string) error {
	logger.WithOperation("DetachRouterInterface").Infof("Detaching subnet %s from router %s", subnetID, routerID)
	// TODO: Implement actual STACKIT API call
	return nil
}

// DeleteRouter deletes a router.
func (m *NetworkManager) DeleteRouter(ctx context.Context, id string) error {
	// TODO: Implement
	return errors.New("not implemented")
}

// Public IP operations

// CreatePublicIP creates a public IP address.
func (m *NetworkManager) CreatePublicIP(ctx context.Context, req *cpi.CreatePublicIPRequest) (*cpi.PublicIP, error) {
	logger.WithOperation("CreatePublicIP").Infof("Creating public IP: %s (job: %s, index: %s)", req.Name, req.Job, req.Index)

	// Build labels
	labels := map[string]interface{}{}
	for k, v := range req.Labels {
		labels[k] = v
	}

	labels["managed-by"] = "ocfp"
	if req.Job != "" {
		labels["job"] = req.Job
	}

	if req.Index != "" {
		labels["index"] = req.Index
	}

	if req.Name != "" {
		labels["name"] = req.Name
	}

	// Create via SDK
	iaasClient, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}

	payload := iaas.CreatePublicIPPayload{}
	payload.SetLabels(labels)

	created, err := iaasClient.CreatePublicIP(ctx, m.client.config.ProjectID).
		CreatePublicIPPayload(payload).
		Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas CreatePublicIP failed: %w", err)
	}

	out := &cpi.PublicIP{
		ID:      stringOrEmpty(created.GetIdOk()),
		Address: stringOrEmpty(created.GetIpOk()),
		Labels:  mapAnyToString(created.GetLabels()),
	}
	if out.Labels != nil {
		out.Job = out.Labels["job"]
		out.Index = out.Labels["index"]
		out.Name = out.Labels["name"]
	}

	logger.WithOperation("CreatePublicIP").Infof("Public IP created: %s (%s)", out.ID, out.Address)

	return out, nil
}

// ListPublicIPs lists public IPs with optional filtering.
func (m *NetworkManager) ListPublicIPs(ctx context.Context, filters map[string]string) ([]*cpi.PublicIP, error) {
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

	var list []*cpi.PublicIP

	for _, ip := range items {
		labels := mapAnyToString(ip.GetLabels())

		out := &cpi.PublicIP{
			ID:      stringOrEmpty(ip.GetIdOk()),
			Address: stringOrEmpty(ip.GetIpOk()),
			Labels:  labels,
		}
		if labels != nil {
			out.Job = labels["job"]
			out.Index = labels["index"]
			out.Name = labels["name"]
		}

		if matchLabels(labels, filters) {
			list = append(list, out)
		}
	}

	return list, nil
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
		// TODO: Inspect error for 404 mapping if needed
		return nil, fmt.Errorf("stackit iaas GetPublicIP failed: %w", err)
	}

	out := &cpi.PublicIP{
		ID:      stringOrEmpty(got.GetIdOk()),
		Address: stringOrEmpty(got.GetIpOk()),
		Labels:  mapAnyToString(got.GetLabels()),
	}
	if out.Labels != nil {
		out.Job = out.Labels["job"]
		out.Index = out.Labels["index"]
		out.Name = out.Labels["name"]
	}

	return out, nil
}

// DeletePublicIP deletes a public IP.
func (m *NetworkManager) DeletePublicIP(ctx context.Context, id string) error {
	logger.WithOperation("DeletePublicIP").Infof("Deleting public IP: %s", id)

	iaasClient, err := m.client.getIAASClient()
	if err != nil {
		return err
	}

	err = iaasClient.DeletePublicIP(ctx, m.client.config.ProjectID, id).Execute()
	if err != nil {
		return fmt.Errorf("stackit iaas DeletePublicIP failed: %w", err)
	}

	logger.WithOperation("DeletePublicIP").Infof("Public IP deleted: %s", id)

	return nil
}

// mapAnyToString converts map[string]interface{} to map[string]string.
func mapAnyToString(in map[string]interface{}) map[string]string {
	if in == nil {
		return nil
	}

	out := make(map[string]string, len(in))
	for key, value := range in {
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

// matchLabels filters by label:foo=value filters.
func matchLabels(labels map[string]string, filters map[string]string) bool {
	if len(filters) == 0 {
		return true
	}

	for k, v := range filters {
		if strings.HasPrefix(k, "label:") {
			key := strings.TrimPrefix(k, "label:")
			if labels == nil || labels[key] != v {
				return false
			}
		}
	}

	return true
}

// EnsureJumpboxPublicIPs ensures the required number of jumpbox public IPs exist.
func (m *NetworkManager) EnsureJumpboxPublicIPs(ctx context.Context, blocName string, count int) ([]*cpi.PublicIP, error) {
	if count <= 0 {
		count = 2 // Default to 2 jumpbox IPs
	}

	logger.WithOperation("EnsureJumpboxPublicIPs").Infof("Ensuring %d jumpbox public IPs for bloc %s", count, blocName)

	// Find existing jumpbox IPs
	filters := map[string]string{
		"label:managed-by": "ocfp",
		"label:bloc":       blocName,
		"label:job":        "jumpbox",
	}

	existingIPs, err := m.ListPublicIPs(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("failed to list existing jumpbox IPs: %w", err)
	}

	// Index existing IPs
	ipsByIndex := make(map[string]*cpi.PublicIP)

	for _, ip := range existingIPs {
		if ip.Labels != nil && ip.Labels["index"] != "" {
			ipsByIndex[ip.Labels["index"]] = ip
		}
	}

	// Create missing IPs
	var allIPs []*cpi.PublicIP

	for ipIndex := range count {
		index := strconv.Itoa(ipIndex)
		if existingIP, exists := ipsByIndex[index]; exists {
			logger.WithOperation("EnsureJumpboxPublicIPs").Infof("Jumpbox IP with index %s already exists: %s", index, existingIP.Address)
			allIPs = append(allIPs, existingIP)
		} else {
			// Create new IP
			req := &cpi.CreatePublicIPRequest{
				Name:  fmt.Sprintf("%s-jumpbox-%d", blocName, ipIndex),
				Job:   "jumpbox",
				Index: index,
				Labels: map[string]string{
					"bloc": blocName,
					"env":  "mgmt",
				},
			}

			newIP, err := m.CreatePublicIP(ctx, req)
			if err != nil {
				logger.WithOperation("EnsureJumpboxPublicIPs").Errorf("Failed to create jumpbox IP with index %s: %v", index, err)

				continue
			}

			logger.WithOperation("EnsureJumpboxPublicIPs").Infof("Created jumpbox IP with index %s: %s", index, newIP.Address)
			allIPs = append(allIPs, newIP)
		}
	}

	return allIPs, nil
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

// ensurePublicIPsForJob is a helper to ensure a number of IPs by job label.
func (m *NetworkManager) ensurePublicIPsForJob(ctx context.Context, blocName, job string, count int) ([]*cpi.PublicIP, error) {
	// Find existing IPs for this job
	filters := map[string]string{
		"label:managed-by": "ocfp",
		"label:bloc":       blocName,
		"label:job":        job,
	}

	existingIPs, err := m.ListPublicIPs(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("failed to list existing %s IPs: %w", job, err)
	}

	// Index existing IPs by index label
	ipsByIndex := make(map[string]*cpi.PublicIP)

	for _, ip := range existingIPs {
		if ip.Labels != nil && ip.Labels["index"] != "" {
			ipsByIndex[ip.Labels["index"]] = ip
		}
	}

	var allIPs []*cpi.PublicIP

	for ipIndex := range count {
		indexString := strconv.Itoa(ipIndex)
		if existingIP, ok := ipsByIndex[indexString]; ok {
			logger.WithOperation("ensurePublicIPsForJob").Infof("%s IP with index %s already exists: %s", job, indexString, existingIP.Address)
			allIPs = append(allIPs, existingIP)

			continue
		}

		// Create new IP
		req := &cpi.CreatePublicIPRequest{
			Name:  fmt.Sprintf("%s-%s-%d", blocName, job, ipIndex),
			Job:   job,
			Index: indexString,
			Labels: map[string]string{
				"bloc": blocName,
				"env":  "mgmt",
			},
		}

		newIP, err := m.CreatePublicIP(ctx, req)
		if err != nil {
			logger.WithOperation("ensurePublicIPsForJob").Errorf("Failed to create %s IP with index %s: %v", job, indexString, err)

			continue
		}

		logger.WithOperation("ensurePublicIPsForJob").Infof("Created %s IP with index %s: %s", job, indexString, newIP.Address)
		allIPs = append(allIPs, newIP)
	}

	return allIPs, nil
}
