package aws

import (
	"context"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// NetworkManager implements network operations for AWS.
type NetworkManager struct {
	client *Client
}

// ComputeManager implements compute operations for AWS.
type ComputeManager struct {
	client *Client
}

// StorageManager implements storage operations for AWS.
type StorageManager struct {
	client *Client
}

// Network interface implementation - methods in network.go

// Security Group operations delegated to SecurityManager

// CreateSecurityGroup delegates to the security manager to create a security group.
func (m *NetworkManager) CreateSecurityGroup(ctx context.Context, req *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	return m.client.security.CreateSecurityGroup(ctx, req)
}

// GetSecurityGroup delegates to the security manager to get a security group.
func (m *NetworkManager) GetSecurityGroup(ctx context.Context, id string) (*cpi.SecurityGroup, error) {
	return m.client.security.GetSecurityGroup(ctx, id)
}

// ListSecurityGroups delegates to the security manager to list security groups.
func (m *NetworkManager) ListSecurityGroups(ctx context.Context, filters map[string]string) ([]*cpi.SecurityGroup, error) {
	return m.client.security.ListSecurityGroups(ctx, filters)
}

// DeleteSecurityGroup delegates to the security manager to delete a security group.
func (m *NetworkManager) DeleteSecurityGroup(ctx context.Context, id string) error {
	return m.client.security.DeleteSecurityGroup(ctx, id)
}

// Public IP operations - implemented using AWS Elastic IPs (Floating IPs)

// CreatePublicIP allocates a new Elastic IP address.
func (m *NetworkManager) CreatePublicIP(ctx context.Context, req *cpi.PublicIPRequest) (*cpi.PublicIP, error) {
	// Map PublicIP request to FloatingIP request
	floatingIPReq := &cpi.AllocateFloatingIPRequest{
		NetworkID: req.NetworkID,
		Tags:      req.Tags,
	}

	floatingIP, err := m.AllocateFloatingIP(ctx, floatingIPReq)
	if err != nil {
		return nil, err
	}

	// Convert FloatingIP to PublicIP
	return &cpi.PublicIP{
		ID:         floatingIP.ID,
		IPAddress:  floatingIP.Address, // Set IPAddress for bootstrap code compatibility
		Address:    floatingIP.Address, // Set Address for other code compatibility
		Status:     floatingIP.Status,
		InstanceID: floatingIP.InstanceID,
		NetworkID:  floatingIP.NetworkID,
		Tags:       floatingIP.Tags,
		CreatedAt:  floatingIP.CreatedAt,
	}, nil
}

// GetPublicIP retrieves an Elastic IP address by allocation ID.
func (m *NetworkManager) GetPublicIP(ctx context.Context, id string) (*cpi.PublicIP, error) {
	floatingIP, err := m.GetFloatingIP(ctx, id)
	if err != nil {
		return nil, err
	}

	// Convert FloatingIP to PublicIP
	return &cpi.PublicIP{
		ID:         floatingIP.ID,
		IPAddress:  floatingIP.Address, // Set IPAddress for bootstrap code compatibility
		Address:    floatingIP.Address, // Set Address for other code compatibility
		Status:     floatingIP.Status,
		InstanceID: floatingIP.InstanceID,
		NetworkID:  floatingIP.NetworkID,
		Tags:       floatingIP.Tags,
		CreatedAt:  floatingIP.CreatedAt,
	}, nil
}

// ListPublicIPs lists allocated Elastic IP addresses.
func (m *NetworkManager) ListPublicIPs(ctx context.Context) ([]*cpi.PublicIP, error) {
	floatingIPs, err := m.ListFloatingIPs(ctx, nil)
	if err != nil {
		return nil, err
	}

	// Convert FloatingIPs to PublicIPs
	publicIPs := make([]*cpi.PublicIP, 0, len(floatingIPs))

	for _, floatingIP := range floatingIPs {
		publicIPs = append(publicIPs, &cpi.PublicIP{
			ID:         floatingIP.ID,
			IPAddress:  floatingIP.Address, // Set IPAddress for bootstrap code compatibility
			Address:    floatingIP.Address, // Set Address for other code compatibility
			Status:     floatingIP.Status,
			InstanceID: floatingIP.InstanceID,
			NetworkID:  floatingIP.NetworkID,
			Tags:       floatingIP.Tags,
			CreatedAt:  floatingIP.CreatedAt,
		})
	}

	return publicIPs, nil
}

// DeletePublicIP releases an Elastic IP address.
func (m *NetworkManager) DeletePublicIP(ctx context.Context, id string) error {
	return m.ReleaseFloatingIP(ctx, id)
}

// Load Balancer operations delegated to LoadBalancerManager

// CreateLoadBalancer delegates to the load balancer manager.
func (m *NetworkManager) CreateLoadBalancer(ctx context.Context, config *cpi.LoadBalancer) (*cpi.LoadBalancer, error) {
	req := &cpi.CreateLoadBalancerRequest{
		Name:           config.Name,
		NetworkID:      config.NetworkID,
		SubnetIDs:      config.SubnetIDs,
		SecurityGroups: config.SecurityGroups,
	}

	return m.client.loadBalancer.CreateLoadBalancer(ctx, req)
}

// GetLoadBalancer delegates to the load balancer manager.
func (m *NetworkManager) GetLoadBalancer(ctx context.Context, nameOrID string) (*cpi.LoadBalancer, error) {
	return m.client.loadBalancer.GetLoadBalancer(ctx, nameOrID)
}

// ListLoadBalancers delegates to the load balancer manager.
func (m *NetworkManager) ListLoadBalancers(ctx context.Context, filters map[string]string) ([]*cpi.LoadBalancer, error) {
	return m.client.loadBalancer.ListLoadBalancers(ctx, filters)
}

// UpdateLoadBalancer delegates to the load balancer manager.
func (m *NetworkManager) UpdateLoadBalancer(ctx context.Context, loadBalancer *cpi.LoadBalancer) error {
	req := &cpi.UpdateLoadBalancerRequest{
		Name:           &loadBalancer.Name,
		SecurityGroups: loadBalancer.SecurityGroups,
	}

	return m.client.loadBalancer.UpdateLoadBalancer(ctx, loadBalancer.ID, req)
}

// DeleteLoadBalancer delegates to the load balancer manager.
func (m *NetworkManager) DeleteLoadBalancer(ctx context.Context, id string) error {
	return m.client.loadBalancer.DeleteLoadBalancer(ctx, id)
}

// GetBackendPools retrieves backend pools for a load balancer (not implemented).
func (m *NetworkManager) GetBackendPools(_ctx context.Context, _lbID string) ([]*cpi.BackendPool, error) {
	return nil, &cpi.ProviderError{Provider: "aws", Code: "NotImplemented", Message: "GetBackendPools not yet implemented"}
}

// AddBackendMember delegates to the load balancer manager.
func (m *NetworkManager) AddBackendMember(ctx context.Context, lbID string, member *cpi.BackendMember) error {
	backend := &cpi.Backend{
		Address: member.IPAddress,
		Port:    member.Port,
		Enabled: true,
	}

	return m.client.loadBalancer.AddBackend(ctx, lbID, backend)
}

// RemoveBackendMember delegates to the load balancer manager.
func (m *NetworkManager) RemoveBackendMember(ctx context.Context, lbID string, memberIP string) error {
	return m.client.loadBalancer.RemoveBackend(ctx, lbID, memberIP)
}

// ConfigureHealthCheck delegates to the load balancer manager.
func (m *NetworkManager) ConfigureHealthCheck(ctx context.Context, lbID string, check *cpi.HealthCheck) error {
	return m.client.loadBalancer.ConfigureHealthCheck(ctx, lbID, check)
}

// GetLoadBalancerHealth delegates to the load balancer manager.
func (m *NetworkManager) GetLoadBalancerHealth(ctx context.Context, lbID string) (*cpi.HealthStatus, error) {
	return m.client.loadBalancer.GetHealthStatus(ctx, lbID)
}

// Compute interface implementation - methods in compute.go

// Storage interface implementation - methods in storage.go

// Security interface implementation - methods in security.go

// LoadBalancer interface implementation - methods in loadbalancer.go
