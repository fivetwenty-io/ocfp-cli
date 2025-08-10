package cpi

import (
	"context"
	"fmt"
)

// Provider is the main interface that all cloud providers must implement
type Provider interface {
	// Provider information
	Name() string
	Region() string

	// Authentication
	Authenticate(ctx context.Context) error
	ValidateCredentials(ctx context.Context) error

	// Resource managers
	Network() NetworkManager
	Compute() ComputeManager
	Storage() StorageManager
	Security() SecurityManager
	LoadBalancer() LoadBalancerManager

	// Lifecycle
	Initialize(ctx context.Context, config interface{}) error
	Cleanup(ctx context.Context) error
}

// NetworkManager handles network-related operations
type NetworkManager interface {
	// VPC/Network operations
	CreateNetwork(ctx context.Context, req *CreateNetworkRequest) (*Network, error)
	GetNetwork(ctx context.Context, id string) (*Network, error)
	ListNetworks(ctx context.Context, filters map[string]string) ([]*Network, error)
	DeleteNetwork(ctx context.Context, id string) error

	// Subnet operations
	CreateSubnet(ctx context.Context, req *CreateSubnetRequest) (*Subnet, error)
	GetSubnet(ctx context.Context, id string) (*Subnet, error)
	ListSubnets(ctx context.Context, networkID string) ([]*Subnet, error)
	DeleteSubnet(ctx context.Context, id string) error

	// Floating IP operations
	AllocateFloatingIP(ctx context.Context, req *AllocateFloatingIPRequest) (*FloatingIP, error)
	GetFloatingIP(ctx context.Context, id string) (*FloatingIP, error)
	ListFloatingIPs(ctx context.Context) ([]*FloatingIP, error)
	AssociateFloatingIP(ctx context.Context, ipID string, instanceID string) error
	DisassociateFloatingIP(ctx context.Context, ipID string) error
	ReleaseFloatingIP(ctx context.Context, id string) error

	// Router operations
	CreateRouter(ctx context.Context, req *CreateRouterRequest) (*Router, error)
	GetRouter(ctx context.Context, id string) (*Router, error)
	ListRouters(ctx context.Context) ([]*Router, error)
	AttachRouterInterface(ctx context.Context, routerID string, subnetID string) error
	DetachRouterInterface(ctx context.Context, routerID string, subnetID string) error
	DeleteRouter(ctx context.Context, id string) error

	// Load Balancer operations
	CreateLoadBalancer(ctx context.Context, config *LoadBalancer) (*LoadBalancer, error)
	GetLoadBalancer(ctx context.Context, nameOrID string) (*LoadBalancer, error)
	ListLoadBalancers(ctx context.Context, filters map[string]string) ([]*LoadBalancer, error)
	UpdateLoadBalancer(ctx context.Context, lb *LoadBalancer) error
	DeleteLoadBalancer(ctx context.Context, id string) error

	// Backend pool operations
	GetBackendPools(ctx context.Context, lbID string) ([]*BackendPool, error)
	AddBackendMember(ctx context.Context, lbID string, member *BackendMember) error
	RemoveBackendMember(ctx context.Context, lbID string, memberIP string) error

	// Health check operations
	ConfigureHealthCheck(ctx context.Context, lbID string, check *HealthCheck) error
	GetLoadBalancerHealth(ctx context.Context, lbID string) (*HealthStatus, error)
}

// ComputeManager handles compute-related operations
type ComputeManager interface {
	// Instance operations
	CreateInstance(ctx context.Context, req *CreateInstanceRequest) (*Instance, error)
	GetInstance(ctx context.Context, id string) (*Instance, error)
	ListInstances(ctx context.Context, filters map[string]string) ([]*Instance, error)
	StartInstance(ctx context.Context, id string) error
	StopInstance(ctx context.Context, id string) error
	RebootInstance(ctx context.Context, id string) error
	DeleteInstance(ctx context.Context, id string) error

	// SSH key operations
	CreateKeyPair(ctx context.Context, name string) (*KeyPair, error)
	ImportKeyPair(ctx context.Context, name string, publicKey string) error
	GetKeyPair(ctx context.Context, name string) (*KeyPair, error)
	ListKeyPairs(ctx context.Context) ([]*KeyPair, error)
	DeleteKeyPair(ctx context.Context, name string) error

	// Image operations
	ListImages(ctx context.Context, filters map[string]string) ([]*Image, error)
	GetImage(ctx context.Context, id string) (*Image, error)

	// Flavor operations
	ListFlavors(ctx context.Context) ([]*Flavor, error)
	GetFlavor(ctx context.Context, id string) (*Flavor, error)
}

// StorageManager handles storage-related operations
type StorageManager interface {
	// Volume operations
	CreateVolume(ctx context.Context, req *CreateVolumeRequest) (*Volume, error)
	GetVolume(ctx context.Context, id string) (*Volume, error)
	ListVolumes(ctx context.Context, filters map[string]string) ([]*Volume, error)
	AttachVolume(ctx context.Context, volumeID string, instanceID string, device string) error
	DetachVolume(ctx context.Context, volumeID string) error
	ResizeVolume(ctx context.Context, id string, size int) error
	DeleteVolume(ctx context.Context, id string) error

	// Snapshot operations
	CreateSnapshot(ctx context.Context, volumeID string, name string) (*Snapshot, error)
	GetSnapshot(ctx context.Context, id string) (*Snapshot, error)
	ListSnapshots(ctx context.Context, volumeID string) ([]*Snapshot, error)
	DeleteSnapshot(ctx context.Context, id string) error

	// Object storage operations
	CreateBucket(ctx context.Context, name string) (*Bucket, error)
	GetBucket(ctx context.Context, name string) (*Bucket, error)
	ListBuckets(ctx context.Context) ([]*Bucket, error)
	DeleteBucket(ctx context.Context, name string) error
	EmptyBucket(ctx context.Context, name string) error
}

// SecurityManager handles security-related operations
type SecurityManager interface {
	// Security group operations
	CreateSecurityGroup(ctx context.Context, req *CreateSecurityGroupRequest) (*SecurityGroup, error)
	GetSecurityGroup(ctx context.Context, id string) (*SecurityGroup, error)
	ListSecurityGroups(ctx context.Context, filters map[string]string) ([]*SecurityGroup, error)
	DeleteSecurityGroup(ctx context.Context, id string) error

	// Security rules operations
	AddSecurityRule(ctx context.Context, groupID string, rule *SecurityRule) error
	RemoveSecurityRule(ctx context.Context, groupID string, ruleID string) error
	ListSecurityRules(ctx context.Context, groupID string) ([]*SecurityRule, error)
}

// LoadBalancerManager handles load balancer operations
type LoadBalancerManager interface {
	CreateLoadBalancer(ctx context.Context, req *CreateLoadBalancerRequest) (*LoadBalancer, error)
	GetLoadBalancer(ctx context.Context, id string) (*LoadBalancer, error)
	ListLoadBalancers(ctx context.Context, filters map[string]string) ([]*LoadBalancer, error)
	UpdateLoadBalancer(ctx context.Context, id string, req *UpdateLoadBalancerRequest) error
	DeleteLoadBalancer(ctx context.Context, id string) error

	// Backend operations
	AddBackend(ctx context.Context, lbID string, backend *Backend) error
	RemoveBackend(ctx context.Context, lbID string, backendID string) error
	EnableBackend(ctx context.Context, lbID string, backendID string) error
	DisableBackend(ctx context.Context, lbID string, backendID string) error

	// Health check operations
	ConfigureHealthCheck(ctx context.Context, lbID string, check *HealthCheck) error
	GetHealthStatus(ctx context.Context, lbID string) (*HealthStatus, error)
}

// Resource represents a generic cloud resource
type Resource interface {
	GetID() string
	GetName() string
	GetType() string
	GetTags() map[string]string
	GetState() string
	GetCreatedAt() string
}

// ResourceState represents the state of a resource
type ResourceState string

const (
	ResourceStateCreating  ResourceState = "creating"
	ResourceStateActive    ResourceState = "active"
	ResourceStateAvailable ResourceState = "available"
	ResourceStateInUse     ResourceState = "in-use"
	ResourceStateStopped   ResourceState = "stopped"
	ResourceStateDeleting  ResourceState = "deleting"
	ResourceStateDeleted   ResourceState = "deleted"
	ResourceStateError     ResourceState = "error"
	ResourceStateUnknown   ResourceState = "unknown"
)

// ProviderError represents a provider-specific error
type ProviderError struct {
	Provider string
	Code     string
	Message  string
	Details  map[string]interface{}
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("[%s] %s: %s", e.Provider, e.Code, e.Message)
}

// IsNotFound returns true if the error indicates a resource was not found
func IsNotFound(err error) bool {
	if perr, ok := err.(*ProviderError); ok {
		return perr.Code == "NotFound" || perr.Code == "404"
	}
	return false
}

// IsAlreadyExists returns true if the error indicates a resource already exists
func IsAlreadyExists(err error) bool {
	if perr, ok := err.(*ProviderError); ok {
		return perr.Code == "AlreadyExists" || perr.Code == "409"
	}
	return false
}

// IsUnauthorized returns true if the error indicates an authentication failure
func IsUnauthorized(err error) bool {
	if perr, ok := err.(*ProviderError); ok {
		return perr.Code == "Unauthorized" || perr.Code == "401"
	}
	return false
}
