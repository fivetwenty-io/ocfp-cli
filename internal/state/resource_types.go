package state

import "sync"

// ResourceType constants for different cloud resources.
const (
	// Network resources.
	ResourceTypeNetwork       = "network"
	ResourceTypeSubnet        = "subnet"
	ResourceTypeSecurityGroup = "security_group"
	ResourceTypePublicIP      = "public_ip"
	ResourceTypeFloatingIP    = "floating_ip"
	ResourceTypeRouter        = "router"
	ResourceTypeLoadBalancer  = "load_balancer"

	// Compute resources.
	ResourceTypeInstance = "compute_instance"
	ResourceTypeKeyPair  = "ssh_key_pair"
	ResourceTypeVolume   = "block_volume"
	ResourceTypeImage    = "compute_image"
	ResourceTypeFlavor   = "compute_flavor"

	// Storage resources.
	ResourceTypeBucket           = "object_storage_bucket"
	ResourceTypeSnapshot         = "volume_snapshot"
	ResourceTypeCredentialsGroup = "credentials_group"
)

// ResourceCategory groups resources by their category.
type ResourceCategory string

const (
	CategoryNetwork ResourceCategory = "network"
	CategoryCompute ResourceCategory = "compute"
	CategoryStorage ResourceCategory = "storage"
)

// ResourceTypeInfo contains metadata about a resource type.
type ResourceTypeInfo struct {
	Type     string
	Category ResourceCategory
	Name     string
}

// initResourceTypeRegistry initializes the resource type registry.
func initResourceTypeRegistry() map[string]ResourceTypeInfo {
	return map[string]ResourceTypeInfo{
		// Network resources
		ResourceTypeNetwork: {
			Type:     ResourceTypeNetwork,
			Category: CategoryNetwork,
			Name:     "Network/VPC",
		},
		ResourceTypeSubnet: {
			Type:     ResourceTypeSubnet,
			Category: CategoryNetwork,
			Name:     "Subnet",
		},
		ResourceTypeSecurityGroup: {
			Type:     ResourceTypeSecurityGroup,
			Category: CategoryNetwork,
			Name:     "Security Group",
		},
		ResourceTypePublicIP: {
			Type:     ResourceTypePublicIP,
			Category: CategoryNetwork,
			Name:     "Public IP",
		},
		ResourceTypeFloatingIP: {
			Type:     ResourceTypeFloatingIP,
			Category: CategoryNetwork,
			Name:     "Floating IP",
		},
		ResourceTypeRouter: {
			Type:     ResourceTypeRouter,
			Category: CategoryNetwork,
			Name:     "Router",
		},
		ResourceTypeLoadBalancer: {
			Type:     ResourceTypeLoadBalancer,
			Category: CategoryNetwork,
			Name:     "Load Balancer",
		},

		// Compute resources
		ResourceTypeInstance: {
			Type:     ResourceTypeInstance,
			Category: CategoryCompute,
			Name:     "Compute Instance",
		},
		ResourceTypeKeyPair: {
			Type:     ResourceTypeKeyPair,
			Category: CategoryCompute,
			Name:     "SSH Key Pair",
		},
		ResourceTypeVolume: {
			Type:     ResourceTypeVolume,
			Category: CategoryCompute,
			Name:     "Block Volume",
		},
		ResourceTypeImage: {
			Type:     ResourceTypeImage,
			Category: CategoryCompute,
			Name:     "Compute Image",
		},
		ResourceTypeFlavor: {
			Type:     ResourceTypeFlavor,
			Category: CategoryCompute,
			Name:     "Compute Flavor",
		},

		// Storage resources
		ResourceTypeBucket: {
			Type:     ResourceTypeBucket,
			Category: CategoryStorage,
			Name:     "Object Storage Bucket",
		},
		ResourceTypeSnapshot: {
			Type:     ResourceTypeSnapshot,
			Category: CategoryStorage,
			Name:     "Volume Snapshot",
		},
		ResourceTypeCredentialsGroup: {
			Type:     ResourceTypeCredentialsGroup,
			Category: CategoryStorage,
			Name:     "Credentials Group",
		},
	}
}

// getResourceTypeRegistry returns the resource type registry.
// Uses sync.OnceValue for thread-safe lazy initialization without global variables.
//
//nolint:gochecknoglobals // sync.OnceValue is the recommended pattern for lazy initialization
var getResourceTypeRegistry = sync.OnceValue(initResourceTypeRegistry)

// GetResourceTypeInfo retrieves metadata for a resource type.
func GetResourceTypeInfo(resourceType string) (ResourceTypeInfo, bool) {
	info, ok := getResourceTypeRegistry()[resourceType]

	return info, ok
}

// GetResourcesByCategory returns all resource types in a category.
func GetResourcesByCategory(category ResourceCategory) []string {
	types := make([]string, 0)

	for resourceType, info := range getResourceTypeRegistry() {
		if info.Category == category {
			types = append(types, resourceType)
		}
	}

	return types
}
