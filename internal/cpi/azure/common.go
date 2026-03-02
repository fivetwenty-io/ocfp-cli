package azure

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// Azure resource ID pattern for parsing.
var resourceIDPattern = regexp.MustCompile(`^/subscriptions/([^/]+)/resourceGroups/([^/]+)/providers/([^/]+)/([^/]+)/(.+)$`)

// ResourceID represents a parsed Azure resource ID.
type ResourceID struct {
	SubscriptionID    string
	ResourceGroup     string
	Provider          string
	ResourceType      string
	ResourceName      string
}

// ParseResourceID parses an Azure resource ID string.
func ParseResourceID(id string) (*ResourceID, error) {
	matches := resourceIDPattern.FindStringSubmatch(id)
	if matches == nil {
		return nil, fmt.Errorf("invalid Azure resource ID format: %s", id)
	}

	return &ResourceID{
		SubscriptionID: matches[1],
		ResourceGroup:  matches[2],
		Provider:       matches[3],
		ResourceType:   matches[4],
		ResourceName:   matches[5],
	}, nil
}

// BuildResourceID constructs an Azure resource ID.
func BuildResourceID(subscriptionID, resourceGroup, provider, resourceType, resourceName string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/%s/%s/%s",
		subscriptionID, resourceGroup, provider, resourceType, resourceName)
}

// BuildVNetID constructs a VNet resource ID.
func BuildVNetID(subscriptionID, resourceGroup, vnetName string) string {
	return BuildResourceID(subscriptionID, resourceGroup, "Microsoft.Network", "virtualNetworks", vnetName)
}

// BuildSubnetID constructs a Subnet resource ID.
func BuildSubnetID(subscriptionID, resourceGroup, vnetName, subnetName string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/virtualNetworks/%s/subnets/%s",
		subscriptionID, resourceGroup, vnetName, subnetName)
}

// BuildNSGID constructs a Network Security Group resource ID.
func BuildNSGID(subscriptionID, resourceGroup, nsgName string) string {
	return BuildResourceID(subscriptionID, resourceGroup, "Microsoft.Network", "networkSecurityGroups", nsgName)
}

// BuildPublicIPID constructs a Public IP Address resource ID.
func BuildPublicIPID(subscriptionID, resourceGroup, publicIPName string) string {
	return BuildResourceID(subscriptionID, resourceGroup, "Microsoft.Network", "publicIPAddresses", publicIPName)
}

// BuildVMID constructs a Virtual Machine resource ID.
func BuildVMID(subscriptionID, resourceGroup, vmName string) string {
	return BuildResourceID(subscriptionID, resourceGroup, "Microsoft.Compute", "virtualMachines", vmName)
}

// BuildDiskID constructs a Managed Disk resource ID.
func BuildDiskID(subscriptionID, resourceGroup, diskName string) string {
	return BuildResourceID(subscriptionID, resourceGroup, "Microsoft.Compute", "disks", diskName)
}

// BuildNICID constructs a Network Interface resource ID.
func BuildNICID(subscriptionID, resourceGroup, nicName string) string {
	return BuildResourceID(subscriptionID, resourceGroup, "Microsoft.Network", "networkInterfaces", nicName)
}

// BuildLoadBalancerID constructs a Load Balancer resource ID.
func BuildLoadBalancerID(subscriptionID, resourceGroup, lbName string) string {
	return BuildResourceID(subscriptionID, resourceGroup, "Microsoft.Network", "loadBalancers", lbName)
}

// BuildStorageAccountID constructs a Storage Account resource ID.
func BuildStorageAccountID(subscriptionID, resourceGroup, accountName string) string {
	return BuildResourceID(subscriptionID, resourceGroup, "Microsoft.Storage", "storageAccounts", accountName)
}

// ExtractResourceName extracts the resource name from an Azure resource ID.
func ExtractResourceName(resourceID string) string {
	parts := strings.Split(resourceID, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return resourceID
}

// BuildTags creates Azure tags from a map.
func BuildTags(tags map[string]string) map[string]*string {
	if tags == nil {
		return nil
	}

	result := make(map[string]*string)
	for k, v := range tags {
		result[k] = to.Ptr(v)
	}
	return result
}

// ExtractTags converts Azure tags to a regular map.
func ExtractTags(tags map[string]*string) map[string]string {
	if tags == nil {
		return nil
	}

	result := make(map[string]string)
	for k, v := range tags {
		if v != nil {
			result[k] = *v
		}
	}
	return result
}

// MergeTags merges multiple tag maps, with later maps taking precedence.
func MergeTags(tagMaps ...map[string]string) map[string]string {
	result := make(map[string]string)
	for _, tags := range tagMaps {
		for k, v := range tags {
			result[k] = v
		}
	}
	return result
}

// MapProvisioningStateToResourceState maps Azure provisioning state to CPI resource state.
func MapProvisioningStateToResourceState(state string) cpi.ResourceState {
	switch strings.ToLower(state) {
	case "succeeded":
		return cpi.ResourceStateActive
	case "creating", "updating":
		return cpi.ResourceStateCreating
	case "deleting":
		return cpi.ResourceStateDeleting
	case "failed":
		return cpi.ResourceStateError
	default:
		return cpi.ResourceStateUnknown
	}
}

// MapVMPowerStateToResourceState maps Azure VM power state to CPI resource state.
func MapVMPowerStateToResourceState(powerState string) cpi.ResourceState {
	switch strings.ToLower(powerState) {
	case "running", "powerstate/running":
		return cpi.ResourceStateActive
	case "stopped", "deallocated", "powerstate/stopped", "powerstate/deallocated":
		return cpi.ResourceStateStopped
	case "starting", "powerstate/starting":
		return cpi.ResourceStateCreating
	case "stopping", "powerstate/stopping", "deallocating", "powerstate/deallocating":
		return cpi.ResourceStateDeleting
	default:
		return cpi.ResourceStateUnknown
	}
}

// MapDiskStateToResourceState maps Azure disk state to CPI resource state.
func MapDiskStateToResourceState(state string) cpi.ResourceState {
	switch strings.ToLower(state) {
	case "attached":
		return cpi.ResourceStateInUse
	case "unattached", "reserved":
		return cpi.ResourceStateAvailable
	case "activesas":
		return cpi.ResourceStateActive
	default:
		return cpi.ResourceStateUnknown
	}
}

// MapIPAllocationStateToStatus maps Azure IP allocation state to status string.
func MapIPAllocationStateToStatus(allocated bool, associated bool) string {
	if associated {
		return "associated"
	}
	if allocated {
		return "available"
	}
	return "pending"
}

// ValidateAzureResourceName validates that a name is valid for Azure resources.
func ValidateAzureResourceName(name string, minLen, maxLen int) error {
	if len(name) < minLen {
		return fmt.Errorf("name must be at least %d characters", minLen)
	}
	if len(name) > maxLen {
		return fmt.Errorf("name must be at most %d characters", maxLen)
	}

	// Azure resource names typically must start with alphanumeric
	if len(name) > 0 && !isAlphanumeric(rune(name[0])) {
		return fmt.Errorf("name must start with an alphanumeric character")
	}

	// Azure resource names typically must end with alphanumeric
	if len(name) > 0 && !isAlphanumeric(rune(name[len(name)-1])) {
		return fmt.Errorf("name must end with an alphanumeric character")
	}

	return nil
}

func isAlphanumeric(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// SanitizeResourceName sanitizes a name for use as an Azure resource name.
func SanitizeResourceName(name string, maxLen int) string {
	// Replace invalid characters with hyphens
	result := strings.Map(func(r rune) rune {
		if isAlphanumeric(r) || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, name)

	// Remove leading non-alphanumeric characters
	result = strings.TrimLeftFunc(result, func(r rune) bool {
		return !isAlphanumeric(r)
	})

	// Remove trailing non-alphanumeric characters
	result = strings.TrimRightFunc(result, func(r rune) bool {
		return !isAlphanumeric(r)
	})

	// Truncate to max length
	if len(result) > maxLen {
		result = result[:maxLen]
		// Ensure we don't end with a non-alphanumeric after truncation
		result = strings.TrimRightFunc(result, func(r rune) bool {
			return !isAlphanumeric(r)
		})
	}

	return result
}

// GenerateUniqueName generates a unique resource name with a timestamp suffix.
func GenerateUniqueName(prefix string, maxLen int) string {
	timestamp := time.Now().Unix()
	suffix := fmt.Sprintf("-%d", timestamp)

	maxPrefixLen := maxLen - len(suffix)
	if len(prefix) > maxPrefixLen {
		prefix = prefix[:maxPrefixLen]
	}

	return prefix + suffix
}

// IsValidAzureLocation checks if a location is a valid Azure region.
func IsValidAzureLocation(location string) bool {
	// Common Azure regions
	validLocations := map[string]bool{
		// US
		"eastus":             true,
		"eastus2":            true,
		"westus":             true,
		"westus2":            true,
		"westus3":            true,
		"centralus":          true,
		"northcentralus":     true,
		"southcentralus":     true,
		"westcentralus":      true,

		// Europe
		"northeurope":        true,
		"westeurope":         true,
		"uksouth":            true,
		"ukwest":             true,
		"francecentral":      true,
		"francesouth":        true,
		"germanywestcentral": true,
		"germanynorth":       true,
		"switzerlandnorth":   true,
		"switzerlandwest":    true,
		"norwayeast":         true,
		"norwaywest":         true,
		"swedencentral":      true,
		"swedensouth":        true,
		"polandcentral":      true,

		// Asia Pacific
		"eastasia":           true,
		"southeastasia":      true,
		"australiaeast":      true,
		"australiasoutheast": true,
		"australiacentral":   true,
		"australiacentral2":  true,
		"japaneast":          true,
		"japanwest":          true,
		"koreacentral":       true,
		"koreasouth":         true,
		"centralindia":       true,
		"southindia":         true,
		"westindia":          true,

		// Middle East and Africa
		"uaenorth":           true,
		"uaecentral":         true,
		"southafricanorth":   true,
		"southafricawest":    true,
		"qatarcentral":       true,
		"israelcentral":      true,

		// Americas
		"canadacentral":      true,
		"canadaeast":         true,
		"brazilsouth":        true,
		"brazilsoutheast":    true,
		"mexicocentral":      true,
	}

	return validLocations[strings.ToLower(location)]
}

// GetAvailabilityZonesForLocation returns the availability zones for a given Azure location.
func GetAvailabilityZonesForLocation(location string) []string {
	// Locations that support availability zones
	zonesMap := map[string][]string{
		"eastus":         {"1", "2", "3"},
		"eastus2":        {"1", "2", "3"},
		"westus2":        {"1", "2", "3"},
		"westus3":        {"1", "2", "3"},
		"centralus":      {"1", "2", "3"},
		"northeurope":    {"1", "2", "3"},
		"westeurope":     {"1", "2", "3"},
		"uksouth":        {"1", "2", "3"},
		"francecentral":  {"1", "2", "3"},
		"germanywestcentral": {"1", "2", "3"},
		"swedencentral":  {"1", "2", "3"},
		"eastasia":       {"1", "2", "3"},
		"southeastasia":  {"1", "2", "3"},
		"australiaeast":  {"1", "2", "3"},
		"japaneast":      {"1", "2", "3"},
		"koreacentral":   {"1", "2", "3"},
		"canadacentral":  {"1", "2", "3"},
		"brazilsouth":    {"1", "2", "3"},
	}

	zones, ok := zonesMap[strings.ToLower(location)]
	if !ok {
		return nil
	}
	return zones
}

// SupportsAvailabilityZones returns true if the location supports availability zones.
func SupportsAvailabilityZones(location string) bool {
	return GetAvailabilityZonesForLocation(location) != nil
}

// StringPtr returns a pointer to a string (helper for Azure SDK).
func StringPtr(s string) *string {
	return &s
}

// Int32Ptr returns a pointer to an int32 (helper for Azure SDK).
func Int32Ptr(i int32) *int32 {
	return &i
}

// Int64Ptr returns a pointer to an int64 (helper for Azure SDK).
func Int64Ptr(i int64) *int64 {
	return &i
}

// BoolPtr returns a pointer to a bool (helper for Azure SDK).
func BoolPtr(b bool) *bool {
	return &b
}

// DerefString safely dereferences a string pointer.
func DerefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// DerefInt32 safely dereferences an int32 pointer.
func DerefInt32(i *int32) int32 {
	if i == nil {
		return 0
	}
	return *i
}

// DerefInt64 safely dereferences an int64 pointer.
func DerefInt64(i *int64) int64 {
	if i == nil {
		return 0
	}
	return *i
}

// DerefBool safely dereferences a bool pointer.
func DerefBool(b *bool) bool {
	if b == nil {
		return false
	}
	return *b
}
