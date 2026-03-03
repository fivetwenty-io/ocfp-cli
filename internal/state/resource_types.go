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

// Resource category constants for grouping cloud resources.
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
	registry := make(map[string]ResourceTypeInfo)

	// Add resource types by category
	addNetworkResourceTypes(registry)
	addComputeResourceTypes(registry)
	addStorageResourceTypes(registry)

	return registry
}

// addNetworkResourceTypes adds network resource type definitions to the registry.
func addNetworkResourceTypes(registry map[string]ResourceTypeInfo) {
	registry[ResourceTypeNetwork] = ResourceTypeInfo{
		Type:     ResourceTypeNetwork,
		Category: CategoryNetwork,
		Name:     "Network/VPC",
	}
	registry[ResourceTypeSubnet] = ResourceTypeInfo{
		Type:     ResourceTypeSubnet,
		Category: CategoryNetwork,
		Name:     "Subnet",
	}
	registry[ResourceTypeSecurityGroup] = ResourceTypeInfo{
		Type:     ResourceTypeSecurityGroup,
		Category: CategoryNetwork,
		Name:     "Security Group",
	}
	registry[ResourceTypePublicIP] = ResourceTypeInfo{
		Type:     ResourceTypePublicIP,
		Category: CategoryNetwork,
		Name:     "Public IP",
	}
	registry[ResourceTypeFloatingIP] = ResourceTypeInfo{
		Type:     ResourceTypeFloatingIP,
		Category: CategoryNetwork,
		Name:     "Floating IP",
	}
	registry[ResourceTypeRouter] = ResourceTypeInfo{
		Type:     ResourceTypeRouter,
		Category: CategoryNetwork,
		Name:     "Router",
	}
	registry[ResourceTypeLoadBalancer] = ResourceTypeInfo{
		Type:     ResourceTypeLoadBalancer,
		Category: CategoryNetwork,
		Name:     "Load Balancer",
	}
}

// addComputeResourceTypes adds compute resource type definitions to the registry.
func addComputeResourceTypes(registry map[string]ResourceTypeInfo) {
	registry[ResourceTypeInstance] = ResourceTypeInfo{
		Type:     ResourceTypeInstance,
		Category: CategoryCompute,
		Name:     "Compute Instance",
	}
	registry[ResourceTypeKeyPair] = ResourceTypeInfo{
		Type:     ResourceTypeKeyPair,
		Category: CategoryCompute,
		Name:     "SSH Key Pair",
	}
	registry[ResourceTypeVolume] = ResourceTypeInfo{
		Type:     ResourceTypeVolume,
		Category: CategoryCompute,
		Name:     "Block Volume",
	}
	registry[ResourceTypeImage] = ResourceTypeInfo{
		Type:     ResourceTypeImage,
		Category: CategoryCompute,
		Name:     "Compute Image",
	}
	registry[ResourceTypeFlavor] = ResourceTypeInfo{
		Type:     ResourceTypeFlavor,
		Category: CategoryCompute,
		Name:     "Compute Flavor",
	}
}

// addStorageResourceTypes adds storage resource type definitions to the registry.
func addStorageResourceTypes(registry map[string]ResourceTypeInfo) {
	registry[ResourceTypeBucket] = ResourceTypeInfo{
		Type:     ResourceTypeBucket,
		Category: CategoryStorage,
		Name:     "Object Storage Bucket",
	}
	registry[ResourceTypeSnapshot] = ResourceTypeInfo{
		Type:     ResourceTypeSnapshot,
		Category: CategoryStorage,
		Name:     "Volume Snapshot",
	}
	registry[ResourceTypeCredentialsGroup] = ResourceTypeInfo{
		Type:     ResourceTypeCredentialsGroup,
		Category: CategoryStorage,
		Name:     "Credentials Group",
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
