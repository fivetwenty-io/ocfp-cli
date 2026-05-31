package pve

import (
	"errors"
	"fmt"
)

// Proxmox provider errors.
var (
	// Authentication errors.
	ErrAuthenticationFailed = errors.New("pve: authentication failed")
	ErrAPITokenRequired     = errors.New("pve: API token (token_id and token_secret) or username/password required")
	ErrHostRequired         = errors.New("pve: host URL is required")
	ErrInvalidCredentials   = errors.New("pve: invalid credentials provided")
	ErrTicketExpired        = errors.New("pve: authentication ticket expired")

	// Resource errors.
	ErrVMNotFound            = errors.New("pve: virtual machine not found")
	ErrNodeNotFound          = errors.New("pve: node not found")
	ErrStorageNotFound       = errors.New("pve: storage pool not found")
	ErrVolumeNotFound        = errors.New("pve: volume not found")
	ErrSnapshotNotFound      = errors.New("pve: snapshot not found")
	ErrNetworkNotFound       = errors.New("pve: network/bridge not found")
	ErrTemplateNotFound      = errors.New("pve: template not found")
	ErrFirewallGroupNotFound = errors.New("pve: firewall group not found")

	// Cluster errors.
	ErrNoAvailableNode     = errors.New("pve: no node with sufficient resources available")
	ErrClusterNotAvailable = errors.New("pve: cluster information not available")
	ErrNodeOffline         = errors.New("pve: target node is offline")

	// Feature limitation errors.
	ErrLXCNotSupported           = errors.New("pve: LXC containers not supported; use QEMU VMs only")
	ErrLoadBalancersNotSupported = errors.New("pve: load balancers not natively supported")
	ErrBucketsNotSupported       = errors.New("pve: object storage buckets not natively supported; use external MinIO/Ceph")
	ErrFloatingIPsNotSupported   = errors.New("pve: floating IPs require external IP management")
	ErrRoutersNotSupported       = errors.New("pve: routers not supported; use external network configuration")

	// Network errors.
	ErrSDNNotConfigured   = errors.New("pve: SDN not configured on cluster")
	ErrBridgeNotFound     = errors.New("pve: bridge not found")
	ErrBridgeInUse        = errors.New("pve: bridge is in use by VMs")
	ErrInvalidNetworkMode = errors.New("pve: invalid network mode; use 'bridge' or 'sdn'")

	// Storage errors.
	ErrStoragePoolNotConfigured = errors.New("pve: default storage pool not configured")
	ErrVolumeAttached           = errors.New("pve: volume is attached to a VM")
	ErrInvalidVolumeFormat      = errors.New("pve: invalid volume format")
	ErrInvalidVolumeIDFormat    = errors.New("pve: invalid volume ID format")
	ErrInvalidSnapshotIDFormat  = errors.New("pve: invalid snapshot ID format")
	ErrVolumeResizeUnsupported  = errors.New("pve: volume resize not supported for unattached volumes")
	ErrVolumeNotFoundOnVM       = errors.New("pve: volume not found on VM")

	// Parse errors.
	ErrInvalidVMID            = errors.New("pve: invalid VMID")
	ErrInvalidTemplateVMID    = errors.New("pve: invalid template VMID")
	ErrUnexpectedResponseType = errors.New("pve: unexpected response type")

	// Configuration errors.
	ErrConfigIsRequired  = errors.New("pve: config is required")
	ErrInvalidFlavorSpec = errors.New("pve: invalid flavor specification")
	// ErrMixedAuthConfig is returned when a caller sets fields from both auth modes
	// simultaneously (e.g. AuthToken + Password without TokenSecret, or TokenSecret
	// without AuthToken). Only one complete mode is allowed: either AuthToken +
	// TokenSecret for API token auth, or Username + Password for user/password auth.
	ErrMixedAuthConfig = errors.New("pve: mixed auth configuration: set either (auth_token + token_secret) for API token auth or (username + password) for user/password auth, not both")

	// Task errors.
	ErrTaskFailed  = errors.New("pve: task failed")
	ErrTaskTimeout = errors.New("pve: task timed out")

	// Operation errors.
	ErrVMAlreadyRunning              = errors.New("pve: VM is already running")
	ErrVMAlreadyStopped              = errors.New("pve: VM is already stopped")
	ErrCreateKeyPairNotSupported     = errors.New("pve: CreateKeyPair not supported; use ImportKeyPair with a public key")
	ErrEnableBackendNotImplemented   = errors.New("pve: EnableBackend not implemented")
	ErrDisableBackendNotImplemented  = errors.New("pve: DisableBackend not implemented")
	ErrGetHealthStatusNotImplemented = errors.New("pve: GetHealthStatus not implemented")
	ErrSubnetsNotSupported           = errors.New("pve: subnets are not supported; use bridge networks")
)

// Dynamic error constructors.

// ErrInvalidConfigType returns an error for invalid config type.
func ErrInvalidConfigType(config interface{}) error {
	return fmt.Errorf("pve: invalid config type: %T", config) //nolint:err113 // dynamic error with context
}

// ErrVMIDNotFound returns an error when a VM ID is not found.
func ErrVMIDNotFound(vmid int) error {
	return fmt.Errorf("pve: VM with ID %d not found", vmid) //nolint:err113 // dynamic error with context
}

// ErrVMNameNotFound returns an error when a VM by name is not found.
func ErrVMNameNotFound(name string) error {
	return fmt.Errorf("pve: VM with name %q not found", name) //nolint:err113 // dynamic error with context
}

// ErrNodeNotAvailable returns an error when a specific node is not available.
func ErrNodeNotAvailable(node string) error {
	return fmt.Errorf("pve: node %q is not available", node) //nolint:err113 // dynamic error with context
}

// ErrStoragePoolNotFound returns an error when a storage pool is not found.
func ErrStoragePoolNotFound(storage string) error {
	return fmt.Errorf("pve: storage pool %q not found", storage) //nolint:err113 // dynamic error with context
}

// ErrBridgeNotAvailable returns an error when a bridge is not available.
func ErrBridgeNotAvailable(bridge string) error {
	return fmt.Errorf("pve: bridge %q is not available", bridge) //nolint:err113 // dynamic error with context
}

// ErrFlavorNotFound returns an error when a flavor preset is not found.
func ErrFlavorNotFound(flavor string) error {
	return fmt.Errorf("pve: flavor %q not found", flavor) //nolint:err113 // dynamic error with context
}

// ErrTemplateVMIDNotFound returns an error when a template VM ID is not found.
func ErrTemplateVMIDNotFound(vmid int) error {
	return fmt.Errorf("pve: template with VMID %d not found", vmid) //nolint:err113 // dynamic error with context
}

// ErrTaskFailedWithStatus returns an error with task failure details.
func ErrTaskFailedWithStatus(upid, status string) error {
	return fmt.Errorf("pve: task %s failed with status: %s", upid, status) //nolint:err113 // dynamic error with context
}

// ErrInsufficientResources returns an error when resources are insufficient.
func ErrInsufficientResources(node string, cpuReq int, memReqMB int) error {
	return fmt.Errorf("pve: node %q has insufficient resources (need CPU: %d, RAM: %dMB)", node, cpuReq, memReqMB) //nolint:err113 // dynamic error with context
}

// ErrSecurityGroupNotFound returns an error when a firewall group is not found.
func ErrSecurityGroupNotFound(name string) error {
	return fmt.Errorf("pve: firewall group %q not found", name) //nolint:err113 // dynamic error with context
}

// ErrCloudInitAttachFailed returns an error when cloud-init attachment fails.
func ErrCloudInitAttachFailed(vmid int, reason string) error {
	return fmt.Errorf("pve: failed to attach cloud-init to VM %d: %s", vmid, reason) //nolint:err113 // dynamic error with context
}
