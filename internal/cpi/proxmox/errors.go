package proxmox

import (
	"errors"
	"fmt"
)

// Proxmox provider errors.
var (
	// Authentication errors.
	ErrAuthenticationFailed         = errors.New("proxmox: authentication failed")
	ErrAPITokenRequired             = errors.New("proxmox: API token (token_id and token_secret) or username/password required")
	ErrHostRequired                 = errors.New("proxmox: host URL is required")
	ErrInvalidCredentials           = errors.New("proxmox: invalid credentials provided")
	ErrTicketExpired                = errors.New("proxmox: authentication ticket expired")

	// Resource errors.
	ErrVMNotFound                   = errors.New("proxmox: virtual machine not found")
	ErrNodeNotFound                 = errors.New("proxmox: node not found")
	ErrStorageNotFound              = errors.New("proxmox: storage pool not found")
	ErrVolumeNotFound               = errors.New("proxmox: volume not found")
	ErrSnapshotNotFound             = errors.New("proxmox: snapshot not found")
	ErrNetworkNotFound              = errors.New("proxmox: network/bridge not found")
	ErrTemplateNotFound             = errors.New("proxmox: template not found")
	ErrFirewallGroupNotFound        = errors.New("proxmox: firewall group not found")

	// Cluster errors.
	ErrNoAvailableNode              = errors.New("proxmox: no node with sufficient resources available")
	ErrClusterNotAvailable          = errors.New("proxmox: cluster information not available")
	ErrNodeOffline                  = errors.New("proxmox: target node is offline")

	// Feature limitation errors.
	ErrLXCNotSupported              = errors.New("proxmox: LXC containers not supported; use QEMU VMs only")
	ErrLoadBalancersNotSupported    = errors.New("proxmox: load balancers not natively supported")
	ErrBucketsNotSupported          = errors.New("proxmox: object storage buckets not natively supported; use external MinIO/Ceph")
	ErrFloatingIPsNotSupported      = errors.New("proxmox: floating IPs require external IP management")
	ErrRoutersNotSupported          = errors.New("proxmox: routers not supported; use external network configuration")

	// Network errors.
	ErrSDNNotConfigured             = errors.New("proxmox: SDN not configured on cluster")
	ErrBridgeNotFound               = errors.New("proxmox: bridge not found")
	ErrBridgeInUse                  = errors.New("proxmox: bridge is in use by VMs")
	ErrInvalidNetworkMode           = errors.New("proxmox: invalid network mode; use 'bridge' or 'sdn'")

	// Storage errors.
	ErrStoragePoolNotConfigured     = errors.New("proxmox: default storage pool not configured")
	ErrVolumeAttached               = errors.New("proxmox: volume is attached to a VM")
	ErrInvalidVolumeFormat          = errors.New("proxmox: invalid volume format")

	// Configuration errors.
	ErrConfigIsRequired             = errors.New("proxmox: config is required")
	ErrInvalidFlavorSpec            = errors.New("proxmox: invalid flavor specification")

	// Task errors.
	ErrTaskFailed                   = errors.New("proxmox: task failed")
	ErrTaskTimeout                  = errors.New("proxmox: task timed out")

	// Operation errors.
	ErrVMAlreadyRunning             = errors.New("proxmox: VM is already running")
	ErrVMAlreadyStopped             = errors.New("proxmox: VM is already stopped")
	ErrCreateKeyPairNotSupported    = errors.New("proxmox: CreateKeyPair not supported; use ImportKeyPair with a public key")
	ErrEnableBackendNotImplemented  = errors.New("proxmox: EnableBackend not implemented")
	ErrDisableBackendNotImplemented = errors.New("proxmox: DisableBackend not implemented")
	ErrGetHealthStatusNotImplemented = errors.New("proxmox: GetHealthStatus not implemented")
	ErrSubnetsNotSupported          = errors.New("proxmox: subnets are not supported; use bridge networks")
)

// Dynamic error constructors.

// ErrInvalidConfigType returns an error for invalid config type.
func ErrInvalidConfigType(config interface{}) error {
	return fmt.Errorf("proxmox: invalid config type: %T", config) //nolint:err113 // dynamic error with context
}

// ErrVMIDNotFound returns an error when a VM ID is not found.
func ErrVMIDNotFound(vmid int) error {
	return fmt.Errorf("proxmox: VM with ID %d not found", vmid) //nolint:err113 // dynamic error with context
}

// ErrVMNameNotFound returns an error when a VM by name is not found.
func ErrVMNameNotFound(name string) error {
	return fmt.Errorf("proxmox: VM with name %q not found", name) //nolint:err113 // dynamic error with context
}

// ErrNodeNotAvailable returns an error when a specific node is not available.
func ErrNodeNotAvailable(node string) error {
	return fmt.Errorf("proxmox: node %q is not available", node) //nolint:err113 // dynamic error with context
}

// ErrStoragePoolNotFound returns an error when a storage pool is not found.
func ErrStoragePoolNotFound(storage string) error {
	return fmt.Errorf("proxmox: storage pool %q not found", storage) //nolint:err113 // dynamic error with context
}

// ErrBridgeNotAvailable returns an error when a bridge is not available.
func ErrBridgeNotAvailable(bridge string) error {
	return fmt.Errorf("proxmox: bridge %q is not available", bridge) //nolint:err113 // dynamic error with context
}

// ErrFlavorNotFound returns an error when a flavor preset is not found.
func ErrFlavorNotFound(flavor string) error {
	return fmt.Errorf("proxmox: flavor %q not found", flavor) //nolint:err113 // dynamic error with context
}

// ErrTemplateVMIDNotFound returns an error when a template VM ID is not found.
func ErrTemplateVMIDNotFound(vmid int) error {
	return fmt.Errorf("proxmox: template with VMID %d not found", vmid) //nolint:err113 // dynamic error with context
}

// ErrTaskFailedWithStatus returns an error with task failure details.
func ErrTaskFailedWithStatus(upid, status string) error {
	return fmt.Errorf("proxmox: task %s failed with status: %s", upid, status) //nolint:err113 // dynamic error with context
}

// ErrInsufficientResources returns an error when resources are insufficient.
func ErrInsufficientResources(node string, cpuReq int, memReqMB int) error {
	return fmt.Errorf("proxmox: node %q has insufficient resources (need CPU: %d, RAM: %dMB)", node, cpuReq, memReqMB) //nolint:err113 // dynamic error with context
}

// ErrSecurityGroupNotFound returns an error when a firewall group is not found.
func ErrSecurityGroupNotFound(name string) error {
	return fmt.Errorf("proxmox: firewall group %q not found", name) //nolint:err113 // dynamic error with context
}

// ErrCloudInitAttachFailed returns an error when cloud-init attachment fails.
func ErrCloudInitAttachFailed(vmid int, reason string) error {
	return fmt.Errorf("proxmox: failed to attach cloud-init to VM %d: %s", vmid, reason) //nolint:err113 // dynamic error with context
}
