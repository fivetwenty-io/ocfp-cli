package pve

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// networkModeSDN is the network mode value for Proxmox SDN networking.
const networkModeSDN = "sdn"

// NetworkManager handles Proxmox network operations.
type NetworkManager struct {
	client *Client
}

// CreateNetwork creates a new network (bridge or SDN VNet).
func (m *NetworkManager) CreateNetwork(ctx context.Context, req *cpi.NetworkRequest) (*cpi.Network, error) {
	if m.client.config.NetworkMode == networkModeSDN {
		return m.createSDNNetwork(ctx, req)
	}

	return m.createBridgeNetwork(ctx, req)
}

// GetNetwork retrieves a network by ID.
func (m *NetworkManager) GetNetwork(ctx context.Context, id string) (*cpi.Network, error) {
	if m.client.config.NetworkMode == networkModeSDN {
		return m.getSDNNetwork(ctx, id)
	}

	return m.getBridgeNetwork(ctx, id)
}

// ListNetworks lists all networks.
func (m *NetworkManager) ListNetworks(ctx context.Context, filters map[string]string) ([]*cpi.Network, error) {
	if m.client.config.NetworkMode == networkModeSDN {
		return m.listSDNNetworks(ctx, filters)
	}

	return m.listBridgeNetworks(ctx, filters)
}

// DeleteNetwork deletes a network.
//
// Bridge mode is a no-op by design: Proxmox Linux bridges are operator-
// managed host infrastructure, not OCFP-lifecycle resources. CreateNetwork
// in bridge mode calls EnsureBridge which adopts an existing bridge when
// one is present, so we can't distinguish "OCFP created this" from "the
// operator created this and we adopted it" at delete time. Deleting either
// case is a foot-gun — taking down vmbr0/vmbr1 will brick the host's
// management plane. Operators who genuinely want to remove a bridge can do
// so via the PVE UI or `ip link delete`. SDN VNets ARE OCFP-created, so
// those still get cleaned up.
func (m *NetworkManager) DeleteNetwork(ctx context.Context, id string) error {
	if m.client.config.NetworkMode == networkModeSDN {
		return m.deleteSDNNetwork(ctx, id)
	}

	logger.WithOperation("DeleteNetwork").Infof(
		"PVE bridge mode: skipping host bridge %s (operator-managed; remove manually if intended)",
		id,
	)

	return nil
}

// Subnet operations (limited support - Proxmox bridges don't have native subnets)

// CreateSubnet creates a subnet (limited support).
//
// PVE SDN simple zones expose exactly one L3 subnet per vnet (created
// manually or as part of zone provisioning). Bootstrap logically carves
// multiple AZ subnets out of that single vnet CIDR and routes through the
// virtual-subnet path so this method is normally never invoked for PVE.
// As a belt-and-suspenders safeguard, when CreateSubnet IS called in SDN
// mode and the requested CIDR is fully contained within the parent vnet's
// existing SDN subnet CIDR, we synthesize a success response pointing at
// the parent and skip the API POST — the underlying SDN subnet already
// exists and PVE will reject a duplicate create. The original POST path
// remains as the cold-start fallback when the parent SDN subnet has not
// yet been provisioned for the vnet.
func (m *NetworkManager) CreateSubnet(ctx context.Context, req *cpi.SubnetRequest) (*cpi.Subnet, error) {
	// In SDN mode, we can create subnets
	if m.client.config.NetworkMode == networkModeSDN {
		if parent := m.findContainingSDNSubnet(ctx, req.NetworkID, req.CIDR); parent != nil {
			logger.WithOperation("CreateSubnet").Infof(
				"PVE SDN: requested CIDR %s is within existing vnet subnet %s; reusing parent",
				req.CIDR, parent.CIDR,
			)

			return &cpi.Subnet{
				ID:        parent.ID,
				Name:      req.Name,
				NetworkID: req.NetworkID,
				CIDR:      parent.CIDR,
				State:     cpi.ResourceStateActive,
				Tags:      req.Tags,
				CreatedAt: time.Now(),
			}, nil
		}

		params := map[string]interface{}{
			"subnet": req.CIDR,
			"vnet":   req.NetworkID,
			"type":   "subnet",
		}
		// Per-/22 SDN subnets carry their own routed gateway + SNAT so each
		// availability-zone subnet is a real L3 segment with an in-range gateway
		// (BOSH requires gateway ∈ subnet range; the OCFP per-/22 model relies on
		// the PVE host answering .X.1 for each /22). Omitted fields fall back to
		// PVE defaults.
		if req.Gateway != "" {
			params["gateway"] = req.Gateway
		}
		if req.SNAT {
			params["snat"] = 1
		}

		subnetID := strings.ReplaceAll(req.CIDR, "/", "-")
		path := fmt.Sprintf("/cluster/sdn/vnets/%s/subnets", req.NetworkID)

		_, err := m.client.pveClient.PostCtx(ctx, path, params)
		if err != nil {
			return nil, fmt.Errorf("failed to create SDN subnet: %w", err)
		}

		// Apply SDN changes; log on failure — the subnet is already created.
		if _, applyErr := m.client.pveClient.PutCtx(ctx, "/cluster/sdn", nil); applyErr != nil {
			logger.WithOperation("CreateSubnet").Warnf("SDN apply after subnet create: %v", applyErr)
		}

		return &cpi.Subnet{
			ID:        subnetID,
			Name:      req.Name,
			NetworkID: req.NetworkID,
			CIDR:      req.CIDR,
			State:     cpi.ResourceStateActive,
			Tags:      req.Tags,
			CreatedAt: time.Now(),
		}, nil
	}

	// Bridge mode has no native subnet API. Bootstrap uses logical/virtual
	// subnets (state-only) for PVE so CreateSubnet should never be invoked
	// against the provider in that mode. Return the typed error to surface
	// any caller that bypasses the bootstrap layer.
	return nil, ErrSubnetsNotSupported
}

// GetSubnet retrieves a subnet.
func (m *NetworkManager) GetSubnet(_ctx context.Context, _id string) (*cpi.Subnet, error) {
	return nil, ErrSubnetsNotSupported
}

// ListSubnets lists subnets in a network.
func (m *NetworkManager) ListSubnets(ctx context.Context, networkID string) ([]*cpi.Subnet, error) {
	if m.client.config.NetworkMode == networkModeSDN {
		path := fmt.Sprintf("/cluster/sdn/vnets/%s/subnets", networkID)

		resp, err := m.client.pveClient.GetCtx(ctx, path, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to list SDN subnets: %w", err)
		}

		data, ok := resp.([]interface{})
		if !ok {
			return []*cpi.Subnet{}, nil
		}

		var subnets []*cpi.Subnet

		for _, item := range data {
			subnetData, ok := item.(map[string]interface{})
			if !ok {
				continue
			}

			subnets = append(subnets, &cpi.Subnet{
				ID:        getStringFromMap(subnetData, "subnet"),
				NetworkID: networkID,
				CIDR:      getStringFromMap(subnetData, "subnet"),
				State:     cpi.ResourceStateActive,
				Tags:      make(map[string]string),
			})
		}

		return subnets, nil
	}

	return []*cpi.Subnet{}, nil
}

// DeleteSubnet deletes a subnet.
//
// Bridge mode (the default) has no native subnet concept — bootstrap records
// subnets as logical "virtual:<name>" entries in state only, so there is
// nothing for the provider to delete and this is a no-op success. Returning
// success lets the teardown loop drop the state-only record without retry.
// SDN-mode subnet deletion needs the parent vnet to construct the API path
// and is not exposed through this single-ID interface.
func (m *NetworkManager) DeleteSubnet(_ctx context.Context, id string) error {
	if strings.HasPrefix(id, "virtual:") || m.client.config.NetworkMode != networkModeSDN {
		logger.WithOperation("DeleteSubnet").Infof("PVE: logical subnet, nothing to delete: %s", id)

		return nil
	}

	return ErrSubnetsNotSupported
}

// Security group operations (delegate to security manager)

// CreateSecurityGroup creates a security group.
func (m *NetworkManager) CreateSecurityGroup(ctx context.Context, req *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	return m.client.security.CreateSecurityGroup(ctx, req)
}

// GetSecurityGroup retrieves a security group.
func (m *NetworkManager) GetSecurityGroup(ctx context.Context, id string) (*cpi.SecurityGroup, error) {
	return m.client.security.GetSecurityGroup(ctx, id)
}

// ListSecurityGroups lists security groups.
func (m *NetworkManager) ListSecurityGroups(ctx context.Context, filters map[string]string) ([]*cpi.SecurityGroup, error) {
	return m.client.security.ListSecurityGroups(ctx, filters)
}

// DeleteSecurityGroup deletes a security group.
func (m *NetworkManager) DeleteSecurityGroup(ctx context.Context, id string) error {
	return m.client.security.DeleteSecurityGroup(ctx, id)
}

// Public IP operations (not natively supported)

// CreatePublicIP creates a public IP (not supported).
func (m *NetworkManager) CreatePublicIP(_ctx context.Context, _req *cpi.PublicIPRequest) (*cpi.PublicIP, error) {
	return nil, ErrFloatingIPsNotSupported
}

// GetPublicIP retrieves a public IP.
func (m *NetworkManager) GetPublicIP(_ctx context.Context, _id string) (*cpi.PublicIP, error) {
	return nil, ErrFloatingIPsNotSupported
}

// ListPublicIPs lists public IPs.
func (m *NetworkManager) ListPublicIPs(_ctx context.Context) ([]*cpi.PublicIP, error) {
	return []*cpi.PublicIP{}, nil
}

// DeletePublicIP deletes a public IP.
func (m *NetworkManager) DeletePublicIP(_ctx context.Context, _id string) error {
	return ErrFloatingIPsNotSupported
}

// Floating IP operations (not natively supported)

// AllocateFloatingIP allocates a floating IP.
func (m *NetworkManager) AllocateFloatingIP(_ctx context.Context, _req *cpi.AllocateFloatingIPRequest) (*cpi.FloatingIP, error) {
	return nil, ErrFloatingIPsNotSupported
}

// GetFloatingIP retrieves a floating IP.
func (m *NetworkManager) GetFloatingIP(_ctx context.Context, _id string) (*cpi.FloatingIP, error) {
	return nil, ErrFloatingIPsNotSupported
}

// ListFloatingIPs lists floating IPs.
func (m *NetworkManager) ListFloatingIPs(_ctx context.Context, _filters map[string]string) ([]*cpi.FloatingIP, error) {
	return []*cpi.FloatingIP{}, nil
}

// AssociateFloatingIP associates a floating IP with an instance.
func (m *NetworkManager) AssociateFloatingIP(_ctx context.Context, _ipID string, _instanceID string) error {
	return ErrFloatingIPsNotSupported
}

// DisassociateFloatingIP disassociates a floating IP from an instance.
func (m *NetworkManager) DisassociateFloatingIP(_ctx context.Context, _ipID string) error {
	return ErrFloatingIPsNotSupported
}

// ReleaseFloatingIP releases a floating IP.
func (m *NetworkManager) ReleaseFloatingIP(_ctx context.Context, _id string) error {
	return ErrFloatingIPsNotSupported
}

// Router operations (not supported)

// CreateRouter creates a router.
func (m *NetworkManager) CreateRouter(_ctx context.Context, _req *cpi.CreateRouterRequest) (*cpi.Router, error) {
	return nil, ErrRoutersNotSupported
}

// GetRouter retrieves a router.
func (m *NetworkManager) GetRouter(_ctx context.Context, _id string) (*cpi.Router, error) {
	return nil, ErrRoutersNotSupported
}

// ListRouters lists routers.
func (m *NetworkManager) ListRouters(_ctx context.Context) ([]*cpi.Router, error) {
	return []*cpi.Router{}, nil
}

// AttachRouterInterface attaches a router interface.
func (m *NetworkManager) AttachRouterInterface(_ctx context.Context, _routerID string, _subnetID string) error {
	return ErrRoutersNotSupported
}

// DetachRouterInterface detaches a router interface.
func (m *NetworkManager) DetachRouterInterface(_ctx context.Context, _routerID string, _subnetID string) error {
	return ErrRoutersNotSupported
}

// DeleteRouter deletes a router.
func (m *NetworkManager) DeleteRouter(_ctx context.Context, _id string) error {
	return ErrRoutersNotSupported
}

// Load balancer operations (delegate to load balancer manager)

// CreateLoadBalancer creates a load balancer.
func (m *NetworkManager) CreateLoadBalancer(_ctx context.Context, _config *cpi.LoadBalancer) (*cpi.LoadBalancer, error) {
	return nil, ErrLoadBalancersNotSupported
}

// GetLoadBalancer retrieves a load balancer.
func (m *NetworkManager) GetLoadBalancer(_ctx context.Context, _nameOrID string) (*cpi.LoadBalancer, error) {
	return nil, ErrLoadBalancersNotSupported
}

// ListLoadBalancers lists load balancers.
func (m *NetworkManager) ListLoadBalancers(_ctx context.Context, _filters map[string]string) ([]*cpi.LoadBalancer, error) {
	return []*cpi.LoadBalancer{}, nil
}

// UpdateLoadBalancer updates a load balancer.
func (m *NetworkManager) UpdateLoadBalancer(_ctx context.Context, _lb *cpi.LoadBalancer) error {
	return ErrLoadBalancersNotSupported
}

// DeleteLoadBalancer deletes a load balancer.
func (m *NetworkManager) DeleteLoadBalancer(_ctx context.Context, _id string) error {
	return ErrLoadBalancersNotSupported
}

// GetBackendPools retrieves backend pools for a load balancer.
func (m *NetworkManager) GetBackendPools(_ctx context.Context, _lbID string) ([]*cpi.BackendPool, error) {
	return []*cpi.BackendPool{}, nil
}

// AddBackendMember adds a member to a backend pool.
func (m *NetworkManager) AddBackendMember(_ctx context.Context, _lbID string, _member *cpi.BackendMember) error {
	return ErrLoadBalancersNotSupported
}

// RemoveBackendMember removes a member from a backend pool.
func (m *NetworkManager) RemoveBackendMember(_ctx context.Context, _lbID string, _memberIP string) error {
	return ErrLoadBalancersNotSupported
}

// ConfigureHealthCheck configures a health check for a load balancer.
func (m *NetworkManager) ConfigureHealthCheck(_ctx context.Context, _lbID string, _check *cpi.HealthCheck) error {
	return ErrLoadBalancersNotSupported
}

// GetLoadBalancerHealth retrieves health status of a load balancer.
func (m *NetworkManager) GetLoadBalancerHealth(_ctx context.Context, _lbID string) (*cpi.HealthStatus, error) {
	return nil, ErrLoadBalancersNotSupported
}

// createBridgeNetwork creates a Linux bridge network.
func (m *NetworkManager) createBridgeNetwork(ctx context.Context, req *cpi.NetworkRequest) (*cpi.Network, error) {
	logger.WithOperation("CreateNetwork").Infof("Creating bridge network: %s", req.Name)

	node, err := m.client.getNode(ctx)
	if err != nil {
		return nil, err
	}

	netSvc := m.client.getNetworkService()

	params := map[string]interface{}{
		"autostart": 1,
	}

	if req.CIDR != "" {
		// Extract IP and netmask from CIDR
		parts := strings.Split(req.CIDR, "/")
		if len(parts) == 2 { //nolint:mnd // CIDR format "ip/prefix" always has 2 parts
			params["cidr"] = req.CIDR
		}
	}

	err = netSvc.EnsureBridge(ctx, node, req.Name, params)
	if err != nil {
		return nil, fmt.Errorf("failed to create bridge: %w", err)
	}

	// Reload network configuration
	_ = netSvc.Reload(ctx, node)

	return &cpi.Network{
		ID:        req.Name,
		Name:      req.Name,
		CIDR:      req.CIDR,
		Region:    node,
		State:     cpi.ResourceStateActive,
		Tags:      req.Tags,
		CreatedAt: time.Now(),
	}, nil
}

// createSDNNetwork creates an SDN VNet.
func (m *NetworkManager) createSDNNetwork(ctx context.Context, req *cpi.NetworkRequest) (*cpi.Network, error) {
	logger.WithOperation("CreateNetwork").Infof("Creating SDN network: %s", req.Name)

	// Check if SDN zone is configured
	if m.client.config.SDNZone == "" {
		return nil, ErrSDNNotConfigured
	}

	// Create SDN VNet
	params := map[string]interface{}{
		"vnet": req.Name,
		"zone": m.client.config.SDNZone,
	}

	if req.Tags != nil {
		if alias, ok := req.Tags["alias"]; ok {
			params["alias"] = alias
		}
	}

	_, err := m.client.pveClient.PostCtx(ctx, "/cluster/sdn/vnets", params)
	if err != nil {
		return nil, fmt.Errorf("failed to create SDN VNet: %w", err)
	}

	// Apply SDN changes; log on failure — the VNet is already created.
	if _, applyErr := m.client.pveClient.PutCtx(ctx, "/cluster/sdn", nil); applyErr != nil {
		logger.WithOperation("CreateNetwork").Warnf("SDN apply after vnet create: %v", applyErr)
	}

	return &cpi.Network{
		ID:        req.Name,
		Name:      req.Name,
		CIDR:      req.CIDR,
		Region:    m.client.config.SDNZone,
		State:     cpi.ResourceStateActive,
		Tags:      req.Tags,
		CreatedAt: time.Now(),
	}, nil
}

// getBridgeNetwork retrieves a bridge network.
func (m *NetworkManager) getBridgeNetwork(ctx context.Context, id string) (*cpi.Network, error) { //nolint:varnamelen
	node, err := m.client.getNode(ctx)
	if err != nil {
		return nil, err
	}

	netSvc := m.client.getNetworkService()

	exists, err := netSvc.BridgeExists(ctx, node, id)
	if err != nil {
		return nil, fmt.Errorf("failed to check bridge: %w", err)
	}

	if !exists {
		return nil, ErrBridgeNotFound
	}

	// Get bridge details
	path := buildPVEPathf(node, "network/%s", id)

	resp, err := m.client.pveClient.GetCtx(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get bridge details: %w", err)
	}

	data, ok := resp.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResponseType, resp)
	}

	return &cpi.Network{
		ID:     id,
		Name:   id,
		CIDR:   getStringFromMap(data, "cidr"),
		Region: node,
		State:  cpi.ResourceStateActive,
		Tags:   make(map[string]string),
	}, nil
}

// getSDNNetwork retrieves an SDN VNet.
func (m *NetworkManager) getSDNNetwork(ctx context.Context, id string) (*cpi.Network, error) { //nolint:varnamelen
	path := "/cluster/sdn/vnets/" + id

	resp, err := m.client.pveClient.GetCtx(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get SDN VNet: %w", err)
	}

	data, ok := resp.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResponseType, resp)
	}

	return &cpi.Network{
		ID:     id,
		Name:   getStringFromMap(data, "vnet"),
		Region: getStringFromMap(data, "zone"),
		State:  cpi.ResourceStateActive,
		Tags:   make(map[string]string),
	}, nil
}

// listBridgeNetworks lists bridge networks.
func (m *NetworkManager) listBridgeNetworks(ctx context.Context, filters map[string]string) ([]*cpi.Network, error) {
	node, err := m.client.getNode(ctx)
	if err != nil {
		return nil, err
	}

	path := buildPVEPath(node, "network")

	resp, err := m.client.pveClient.GetCtx(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list networks: %w", err)
	}

	data, ok := resp.([]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResponseType, resp)
	}

	var networks []*cpi.Network

	for _, item := range data {
		netData, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		// Only include bridges
		netType := getStringFromMap(netData, "type")
		if netType != "bridge" {
			continue
		}

		iface := getStringFromMap(netData, "iface")

		// Apply name filter
		if nameFilter, ok := filters["name"]; ok && iface != nameFilter {
			continue
		}

		networks = append(networks, &cpi.Network{
			ID:     iface,
			Name:   iface,
			CIDR:   getStringFromMap(netData, "cidr"),
			Region: node,
			State:  cpi.ResourceStateActive,
			Tags:   make(map[string]string),
		})
	}

	return networks, nil
}

// listSDNNetworks lists SDN VNets.
func (m *NetworkManager) listSDNNetworks(ctx context.Context, filters map[string]string) ([]*cpi.Network, error) {
	resp, err := m.client.pveClient.GetCtx(ctx, "/cluster/sdn/vnets", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list SDN VNets: %w", err)
	}

	data, ok := resp.([]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResponseType, resp)
	}

	var networks []*cpi.Network

	for _, item := range data {
		vnetData, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		vnet := getStringFromMap(vnetData, "vnet")

		// Apply name filter
		if nameFilter, ok := filters["name"]; ok && vnet != nameFilter {
			continue
		}

		networks = append(networks, &cpi.Network{
			ID:     vnet,
			Name:   vnet,
			Region: getStringFromMap(vnetData, "zone"),
			State:  cpi.ResourceStateActive,
			Tags:   make(map[string]string),
		})
	}

	return networks, nil
}

// deleteSDNNetwork deletes an SDN VNet.
func (m *NetworkManager) deleteSDNNetwork(ctx context.Context, id string) error {
	path := "/cluster/sdn/vnets/" + id

	_, err := m.client.pveClient.DeleteCtx(ctx, path, nil)
	if err != nil {
		return fmt.Errorf("failed to delete SDN VNet: %w", err)
	}

	// Apply SDN changes; log on failure — the VNet is already deleted.
	if _, applyErr := m.client.pveClient.PutCtx(ctx, "/cluster/sdn", nil); applyErr != nil {
		logger.WithOperation("DeleteNetwork").Warnf("SDN apply after vnet delete: %v", applyErr)
	}

	return nil
}

// findContainingSDNSubnet returns the existing SDN subnet on the given vnet
// whose CIDR fully contains childCIDR, or nil if the parent subnet does not
// yet exist or the lookup fails. A nil return signals the caller should fall
// back to creating the subnet via the API.
func (m *NetworkManager) findContainingSDNSubnet(ctx context.Context, vnet, childCIDR string) *cpi.Subnet {
	if vnet == "" || childCIDR == "" {
		return nil
	}

	if m.client == nil || m.client.pveClient == nil {
		return nil
	}

	_, childNet, err := net.ParseCIDR(childCIDR)
	if err != nil || childNet == nil {
		return nil
	}

	existing, err := m.ListSubnets(ctx, vnet)
	if err != nil {
		return nil
	}

	for _, candidate := range existing {
		if candidate == nil || candidate.CIDR == "" {
			continue
		}

		_, parentNet, parseErr := net.ParseCIDR(candidate.CIDR)
		if parseErr != nil || parentNet == nil {
			continue
		}

		// Child must be contained in parent: parent's network must include
		// the child's network address and the child's prefix must be at
		// least as specific as the parent's.
		parentOnes, _ := parentNet.Mask.Size()
		childOnes, _ := childNet.Mask.Size()

		if childOnes < parentOnes {
			continue
		}

		if parentNet.Contains(childNet.IP) {
			return candidate
		}
	}

	return nil
}
