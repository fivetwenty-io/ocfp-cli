package stackit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas"
)

// NetworkManager handles STACKIT network operations
type NetworkManager struct {
	client *Client
}

// Load Balancer operations - placeholder implementations

// CreateLoadBalancer creates a new load balancer
func (m *NetworkManager) CreateLoadBalancer(ctx context.Context, config *cpi.LoadBalancer) (*cpi.LoadBalancer, error) {
	logger.WithOperation("CreateLoadBalancer").Infof("Creating load balancer: %s", config.Name)
	// TODO: Implement STACKIT load balancer creation
	return config, nil
}

// GetLoadBalancer retrieves a load balancer by name or ID
func (m *NetworkManager) GetLoadBalancer(ctx context.Context, nameOrID string) (*cpi.LoadBalancer, error) {
	logger.WithOperation("GetLoadBalancer").Infof("Getting load balancer: %s", nameOrID)
	// TODO: Implement STACKIT load balancer retrieval
	return &cpi.LoadBalancer{
		ID:        nameOrID,
		Name:      nameOrID,
		Status:    "active",
		IPAddress: "10.0.0.100",
		Port:      80,
	}, nil
}

// ListLoadBalancers lists all load balancers
func (m *NetworkManager) ListLoadBalancers(ctx context.Context, filters map[string]string) ([]*cpi.LoadBalancer, error) {
	logger.WithOperation("ListLoadBalancers").Info("Listing load balancers")
	// TODO: Implement STACKIT load balancer listing
	return []*cpi.LoadBalancer{}, nil
}

// UpdateLoadBalancer updates a load balancer
func (m *NetworkManager) UpdateLoadBalancer(ctx context.Context, lb *cpi.LoadBalancer) error {
	logger.WithOperation("UpdateLoadBalancer").Infof("Updating load balancer: %s", lb.Name)
	// TODO: Implement STACKIT load balancer update
	return nil
}

// DeleteLoadBalancer deletes a load balancer
func (m *NetworkManager) DeleteLoadBalancer(ctx context.Context, id string) error {
	logger.WithOperation("DeleteLoadBalancer").Infof("Deleting load balancer: %s", id)
	// TODO: Implement STACKIT load balancer deletion
	return nil
}

// GetBackendPools retrieves backend pools for a load balancer
func (m *NetworkManager) GetBackendPools(ctx context.Context, lbID string) ([]*cpi.BackendPool, error) {
	logger.WithOperation("GetBackendPools").Infof("Getting backend pools for load balancer: %s", lbID)
	// TODO: Implement STACKIT backend pool retrieval
	return []*cpi.BackendPool{}, nil
}

// AddBackendMember adds a backend member to a load balancer
func (m *NetworkManager) AddBackendMember(ctx context.Context, lbID string, member *cpi.BackendMember) error {
	logger.WithOperation("AddBackendMember").Infof("Adding backend member to load balancer: %s", lbID)
	// TODO: Implement STACKIT backend member addition
	return nil
}

// RemoveBackendMember removes a backend member from a load balancer
func (m *NetworkManager) RemoveBackendMember(ctx context.Context, lbID string, memberIP string) error {
	logger.WithOperation("RemoveBackendMember").Infof("Removing backend member from load balancer: %s", lbID)
	// TODO: Implement STACKIT backend member removal
	return nil
}

// ConfigureHealthCheck configures health check for a load balancer
func (m *NetworkManager) ConfigureHealthCheck(ctx context.Context, lbID string, check *cpi.HealthCheck) error {
	logger.WithOperation("ConfigureHealthCheck").Infof("Configuring health check for load balancer: %s", lbID)
	// TODO: Implement STACKIT health check configuration
	return nil
}

// GetLoadBalancerHealth retrieves health status for a load balancer
func (m *NetworkManager) GetLoadBalancerHealth(ctx context.Context, lbID string) (*cpi.HealthStatus, error) {
	logger.WithOperation("GetLoadBalancerHealth").Infof("Getting health status for load balancer: %s", lbID)
	// TODO: Implement STACKIT health status retrieval
	return &cpi.HealthStatus{
		LoadBalancerID: lbID,
		Healthy:        0,
		Unhealthy:      0,
		Total:          0,
	}, nil
}

// CreateNetwork creates a new network
func (m *NetworkManager) CreateNetwork(ctx context.Context, req *cpi.CreateNetworkRequest) (*cpi.Network, error) {
	logger.WithOperation("CreateNetwork").Infof("Creating network: %s", req.Name)

	// Prepare API request
	apiReq := map[string]interface{}{
		"name":            req.Name,
		"cidr":            req.CIDR,
		"dns_nameservers": req.DNSServers,
		"labels":          req.Tags,
	}

	httpReq, err := m.client.newRequest(ctx, "POST", "/v1/projects/"+m.client.config.ProjectID+"/networks", apiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create network: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		return nil, m.client.parseError(resp)
	}

	// Parse response
	var network cpi.Network
	if err := json.NewDecoder(resp.Body).Decode(&network); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	logger.WithOperation("CreateNetwork").Infof("Network created: %s", network.ID)
	return &network, nil
}

// GetNetwork retrieves a network by ID
func (m *NetworkManager) GetNetwork(ctx context.Context, id string) (*cpi.Network, error) {
	logger.WithOperation("GetNetwork").Debugf("Getting network: %s", id)

	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/networks/"+id, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get network: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &cpi.ProviderError{
			Provider: "stackit",
			Code:     "NotFound",
			Message:  fmt.Sprintf("Network %s not found", id),
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	var network cpi.Network
	if err := json.NewDecoder(resp.Body).Decode(&network); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &network, nil
}

// ListNetworks lists all networks
func (m *NetworkManager) ListNetworks(ctx context.Context, filters map[string]string) ([]*cpi.Network, error) {
	logger.WithOperation("ListNetworks").Debug("Listing networks")

	// Build query parameters
	query := "?"
	for k, v := range filters {
		query += fmt.Sprintf("%s=%s&", k, v)
	}

	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/networks"+query, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to list networks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	var result struct {
		Networks []*cpi.Network `json:"networks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	logger.WithOperation("ListNetworks").Debugf("Found %d networks", len(result.Networks))
	return result.Networks, nil
}

// DeleteNetwork deletes a network
func (m *NetworkManager) DeleteNetwork(ctx context.Context, id string) error {
	logger.WithOperation("DeleteNetwork").Infof("Deleting network: %s", id)

	httpReq, err := m.client.newRequest(ctx, "DELETE", "/v1/projects/"+m.client.config.ProjectID+"/networks/"+id, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return fmt.Errorf("failed to delete network: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil // Already deleted
	}

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return m.client.parseError(resp)
	}

	logger.WithOperation("DeleteNetwork").Infof("Network deleted: %s", id)
	return nil
}

// CreateSubnet creates a subnet
func (m *NetworkManager) CreateSubnet(ctx context.Context, req *cpi.CreateSubnetRequest) (*cpi.Subnet, error) {
	logger.WithOperation("CreateSubnet").Infof("Creating subnet: %s", req.Name)

	apiReq := map[string]interface{}{
		"name":              req.Name,
		"network_id":        req.NetworkID,
		"cidr":              req.CIDR,
		"availability_zone": req.AvailabilityZone,
		"labels":            req.Tags,
	}

	httpReq, err := m.client.newRequest(ctx, "POST", "/v1/projects/"+m.client.config.ProjectID+"/subnets", apiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create subnet: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		return nil, m.client.parseError(resp)
	}

	var subnet cpi.Subnet
	if err := json.NewDecoder(resp.Body).Decode(&subnet); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	logger.WithOperation("CreateSubnet").Infof("Subnet created: %s", subnet.ID)
	return &subnet, nil
}

// GetSubnet retrieves a subnet
func (m *NetworkManager) GetSubnet(ctx context.Context, id string) (*cpi.Subnet, error) {
	logger.WithOperation("GetSubnet").Debugf("Getting subnet: %s", id)

	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/subnets/"+id, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get subnet: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &cpi.ProviderError{
			Provider: "stackit",
			Code:     "NotFound",
			Message:  fmt.Sprintf("Subnet %s not found", id),
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	var subnet cpi.Subnet
	if err := json.NewDecoder(resp.Body).Decode(&subnet); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &subnet, nil
}

// ListSubnets lists subnets in a network
func (m *NetworkManager) ListSubnets(ctx context.Context, networkID string) ([]*cpi.Subnet, error) {
	logger.WithOperation("ListSubnets").Debugf("Listing subnets for network: %s", networkID)

	query := fmt.Sprintf("?network_id=%s", networkID)
	httpReq, err := m.client.newRequest(ctx, "GET", "/v1/projects/"+m.client.config.ProjectID+"/subnets"+query, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to list subnets: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, m.client.parseError(resp)
	}

	var result struct {
		Subnets []*cpi.Subnet `json:"subnets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return result.Subnets, nil
}

// DeleteSubnet deletes a subnet
func (m *NetworkManager) DeleteSubnet(ctx context.Context, id string) error {
	logger.WithOperation("DeleteSubnet").Infof("Deleting subnet: %s", id)

	httpReq, err := m.client.newRequest(ctx, "DELETE", "/v1/projects/"+m.client.config.ProjectID+"/subnets/"+id, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return fmt.Errorf("failed to delete subnet: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil // Already deleted
	}

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return m.client.parseError(resp)
	}

	logger.WithOperation("DeleteSubnet").Infof("Subnet deleted: %s", id)
	return nil
}

// AllocateFloatingIP allocates a floating IP
func (m *NetworkManager) AllocateFloatingIP(ctx context.Context, req *cpi.AllocateFloatingIPRequest) (*cpi.FloatingIP, error) {
	logger.WithOperation("AllocateFloatingIP").Info("Allocating floating IP")

	apiReq := map[string]interface{}{
		"network_id": req.NetworkID,
		"labels":     req.Tags,
	}

	httpReq, err := m.client.newRequest(ctx, "POST", "/v1/projects/"+m.client.config.ProjectID+"/floating-ips", apiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := m.client.do(ctx, httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate floating IP: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		return nil, m.client.parseError(resp)
	}

	var floatingIP cpi.FloatingIP
	if err := json.NewDecoder(resp.Body).Decode(&floatingIP); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	logger.WithOperation("AllocateFloatingIP").Infof("Floating IP allocated: %s", floatingIP.Address)
	return &floatingIP, nil
}

// GetFloatingIP retrieves a floating IP
func (m *NetworkManager) GetFloatingIP(ctx context.Context, id string) (*cpi.FloatingIP, error) {
	// TODO: Implement
	return nil, fmt.Errorf("not implemented")
}

// ListFloatingIPs lists floating IPs
func (m *NetworkManager) ListFloatingIPs(ctx context.Context) ([]*cpi.FloatingIP, error) {
	// TODO: Implement
	return nil, fmt.Errorf("not implemented")
}

// AssociateFloatingIP associates a floating IP with an instance
func (m *NetworkManager) AssociateFloatingIP(ctx context.Context, ipID string, instanceID string) error {
	logger.WithOperation("AssociateFloatingIP").Infof("Associating floating IP %s with instance %s", ipID, instanceID)
	// TODO: Implement actual STACKIT API call
	return nil
}

// DisassociateFloatingIP disassociates a floating IP
func (m *NetworkManager) DisassociateFloatingIP(ctx context.Context, ipID string) error {
	logger.WithOperation("DisassociateFloatingIP").Infof("Disassociating floating IP %s", ipID)
	// TODO: Implement actual STACKIT API call
	return nil
}

// ReleaseFloatingIP releases a floating IP
func (m *NetworkManager) ReleaseFloatingIP(ctx context.Context, id string) error {
	// TODO: Implement
	return fmt.Errorf("not implemented")
}

// CreateRouter creates a router
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

// GetRouter retrieves a router
func (m *NetworkManager) GetRouter(ctx context.Context, id string) (*cpi.Router, error) {
	// TODO: Implement
	return nil, fmt.Errorf("not implemented")
}

// ListRouters lists routers
func (m *NetworkManager) ListRouters(ctx context.Context) ([]*cpi.Router, error) {
	// TODO: Implement
	return nil, fmt.Errorf("not implemented")
}

// AttachRouterInterface attaches a subnet to a router
func (m *NetworkManager) AttachRouterInterface(ctx context.Context, routerID string, subnetID string) error {
	logger.WithOperation("AttachRouterInterface").Infof("Attaching subnet %s to router %s", subnetID, routerID)
	// TODO: Implement actual STACKIT API call
	return nil
}

// DetachRouterInterface detaches a subnet from a router
func (m *NetworkManager) DetachRouterInterface(ctx context.Context, routerID string, subnetID string) error {
	logger.WithOperation("DetachRouterInterface").Infof("Detaching subnet %s from router %s", subnetID, routerID)
	// TODO: Implement actual STACKIT API call
	return nil
}

// DeleteRouter deletes a router
func (m *NetworkManager) DeleteRouter(ctx context.Context, id string) error {
	// TODO: Implement
	return fmt.Errorf("not implemented")
}

// Public IP operations

// CreatePublicIP creates a public IP address
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

// ListPublicIPs lists public IPs with optional filtering
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

// GetPublicIP retrieves a public IP by ID
func (m *NetworkManager) GetPublicIP(ctx context.Context, id string) (*cpi.PublicIP, error) {
	logger.WithOperation("GetPublicIP").Debugf("Getting public IP: %s", id)

	iaasClient, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}
	got, err := iaasClient.GetPublicIP(ctx, m.client.config.ProjectID, id).Execute()
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

// DeletePublicIP deletes a public IP
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

// mapAnyToString converts map[string]interface{} to map[string]string
func mapAnyToString(in map[string]interface{}) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		switch t := v.(type) {
		case string:
			out[k] = t
		case fmt.Stringer:
			out[k] = t.String()
		default:
			out[k] = fmt.Sprintf("%v", v)
		}
	}
	return out
}

func stringOrEmpty(val string, _ bool) string { return val }

// matchLabels filters by label:foo=value filters
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

// EnsureJumpboxPublicIPs ensures the required number of jumpbox public IPs exist
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
	for i := 0; i < count; i++ {
		index := fmt.Sprintf("%d", i)
		if existingIP, exists := ipsByIndex[index]; exists {
			logger.WithOperation("EnsureJumpboxPublicIPs").Infof("Jumpbox IP with index %s already exists: %s", index, existingIP.Address)
			allIPs = append(allIPs, existingIP)
		} else {
			// Create new IP
			req := &cpi.CreatePublicIPRequest{
				Name:  fmt.Sprintf("%s-jumpbox-%d", blocName, i),
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

// EnsureOpsPublicIPs ensures there is at least one ops public IP
func (m *NetworkManager) EnsureOpsPublicIPs(ctx context.Context, blocName string, count int) ([]*cpi.PublicIP, error) {
	if count <= 0 {
		count = 1
	}
	logger.WithOperation("EnsureOpsPublicIPs").Infof("Ensuring %d ops public IP(s) for bloc %s", count, blocName)

	return m.ensurePublicIPsForJob(ctx, blocName, "ops", count)
}

// EnsureRouterPublicIPs ensures the required number of router public IPs exist
func (m *NetworkManager) EnsureRouterPublicIPs(ctx context.Context, blocName string, count int) ([]*cpi.PublicIP, error) {
	if count <= 0 {
		count = 4 // Default to 4 router IPs
	}

	logger.WithOperation("EnsureRouterPublicIPs").Infof("Ensuring %d router public IPs for bloc %s", count, blocName)

	return m.ensurePublicIPsForJob(ctx, blocName, "router", count)
}

// EnsureCFSSHPublicIPs ensures the required number of cf-ssh public IPs exist
func (m *NetworkManager) EnsureCFSSHPublicIPs(ctx context.Context, blocName string, count int) ([]*cpi.PublicIP, error) {
	if count <= 0 {
		count = 1 // Default to 1 cf-ssh IP
	}

	logger.WithOperation("EnsureCFSSHPublicIPs").Infof("Ensuring %d cf-ssh public IPs for bloc %s", count, blocName)

	return m.ensurePublicIPsForJob(ctx, blocName, "cf-ssh", count)
}

// EnsureTCPRouterPublicIPs ensures the required number of tcp-router public IPs exist
func (m *NetworkManager) EnsureTCPRouterPublicIPs(ctx context.Context, blocName string, count int) ([]*cpi.PublicIP, error) {
	if count <= 0 {
		count = 2 // Default to 2 tcp-router IPs
	}

	logger.WithOperation("EnsureTCPRouterPublicIPs").Infof("Ensuring %d tcp-router public IPs for bloc %s", count, blocName)

	return m.ensurePublicIPsForJob(ctx, blocName, "tcp-router", count)
}

// ensurePublicIPsForJob is a helper to ensure a number of IPs by job label
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
	for i := 0; i < count; i++ {
		index := fmt.Sprintf("%d", i)
		if existingIP, ok := ipsByIndex[index]; ok {
			logger.WithOperation("ensurePublicIPsForJob").Infof("%s IP with index %s already exists: %s", job, index, existingIP.Address)
			allIPs = append(allIPs, existingIP)
			continue
		}

		// Create new IP
		req := &cpi.CreatePublicIPRequest{
			Name:  fmt.Sprintf("%s-%s-%d", blocName, job, i),
			Job:   job,
			Index: index,
			Labels: map[string]string{
				"bloc": blocName,
				"env":  "mgmt",
			},
		}

		newIP, err := m.CreatePublicIP(ctx, req)
		if err != nil {
			logger.WithOperation("ensurePublicIPsForJob").Errorf("Failed to create %s IP with index %s: %v", job, index, err)
			continue
		}
		logger.WithOperation("ensurePublicIPsForJob").Infof("Created %s IP with index %s: %s", job, index, newIP.Address)
		allIPs = append(allIPs, newIP)
	}

	return allIPs, nil
}
