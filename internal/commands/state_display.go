package commands

import (
	"fmt"
	"sort"
	"sync"

	"github.com/ocfp/ocfp-cli-go/internal/state"
)

const (
	// Display sort order priorities for resource types.
	sortOrderServers        = 1
	sortOrderVolumes        = 2
	sortOrderBuckets        = 3
	sortOrderLoadBalancers  = 4
	sortOrderPublicIPs      = 5
	sortOrderKeys           = 6
	sortOrderNetworks       = 7
	sortOrderSubnets        = 8
	sortOrderSecurityGroups = 9
	sortOrderRouters        = 10
	sortOrderSnapshots      = 11
	sortOrderDefault        = 999

	// Display truncation lengths for table columns.
	maxIDLength          = 40
	maxNameLength        = 50
	maxFingerprintLength = 50
	maxPropertiesLength  = 40
	minTruncateLength    = 3
)

// ResourceDisplayConfig holds display configuration for a resource category.
type ResourceDisplayConfig struct {
	Emoji       string
	DisplayName string
	SortOrder   int
}

// initResourceDisplayConfigMap initializes the display configuration map.
func initResourceDisplayConfigMap() map[string]ResourceDisplayConfig {
	return map[string]ResourceDisplayConfig{
		FlagServers: {
			Emoji:       "💻",
			DisplayName: "Compute Instances",
			SortOrder:   sortOrderServers,
		},
		FlagVolumes: {
			Emoji:       "💾",
			DisplayName: "Block Volumes",
			SortOrder:   sortOrderVolumes,
		},
		FlagBuckets: {
			Emoji:       "🗄️",
			DisplayName: "Object Storage Buckets",
			SortOrder:   sortOrderBuckets,
		},
		FlagLoadBalancers: {
			Emoji:       "⚖️",
			DisplayName: "Load Balancers",
			SortOrder:   sortOrderLoadBalancers,
		},
		FlagPublicIPs: {
			Emoji:       "🌐",
			DisplayName: "Public IPs",
			SortOrder:   sortOrderPublicIPs,
		},
		FlagKeys: {
			Emoji:       "🔑",
			DisplayName: "SSH Key Pairs",
			SortOrder:   sortOrderKeys,
		},
		FlagNetworks: {
			Emoji:       "📡",
			DisplayName: "Networks",
			SortOrder:   sortOrderNetworks,
		},
		FlagSubnets: {
			Emoji:       "🔌",
			DisplayName: "Subnets",
			SortOrder:   sortOrderSubnets,
		},
		FlagSecurityGroups: {
			Emoji:       "🔒",
			DisplayName: "Security Groups",
			SortOrder:   sortOrderSecurityGroups,
		},
		FlagRouters: {
			Emoji:       "🔄",
			DisplayName: "Routers",
			SortOrder:   sortOrderRouters,
		},
		FlagSnapshots: {
			Emoji:       "📸",
			DisplayName: "Volume Snapshots",
			SortOrder:   sortOrderSnapshots,
		},
	}
}

// getResourceDisplayConfigMap returns the resource display configuration map.
// Uses sync.OnceValue for thread-safe lazy initialization without global variables.
//
//nolint:gochecknoglobals // sync.OnceValue is the recommended pattern for lazy initialization
var getResourceDisplayConfigMap = sync.OnceValue(initResourceDisplayConfigMap)

// GetDisplayHeading generates a heading with emoji, name, and count.
func GetDisplayHeading(flagName string, count int) string {
	config, ok := getResourceDisplayConfigMap()[flagName]
	if !ok {
		// Fallback for unknown flag
		return fmt.Sprintf("Resources (%d)", count)
	}

	return fmt.Sprintf("%s %s (%d)", config.Emoji, config.DisplayName, count)
}

// GetDisplayConfig returns the display configuration for a flag.
func GetDisplayConfig(flagName string) ResourceDisplayConfig {
	config, ok := getResourceDisplayConfigMap()[flagName]
	if !ok {
		return ResourceDisplayConfig{
			Emoji:       "📦",
			DisplayName: "Resources",
			SortOrder:   sortOrderDefault,
		}
	}

	return config
}

// GetSortedDisplayCategories returns category names sorted by their sort order.
func GetSortedDisplayCategories(grouped map[string][]*state.Resource) []string {
	categories := make([]string, 0, len(grouped))

	for category := range grouped {
		categories = append(categories, category)
	}

	// Sort by display sort order
	sort.Slice(categories, func(i, j int) bool {
		configI := GetDisplayConfig(categories[i])
		configJ := GetDisplayConfig(categories[j])

		return configI.SortOrder < configJ.SortOrder
	})

	return categories
}

// GetHeadersForCategory returns appropriate column headers for a resource category.
func GetHeadersForCategory(category string) []string {
	switch category {
	case FlagLoadBalancers:
		return []string{"ID", "Name", "State", "Properties", "Metadata"}
	case FlagPublicIPs:
		return []string{"ID", "Name", "IP Address", "State", "Metadata"}
	case FlagKeys:
		return []string{"ID", "Name", "Fingerprint", "Metadata"}
	case FlagSecurityGroups:
		return []string{"ID", "Name", "Rules", "Metadata"}
	case FlagSubnets:
		return []string{"ID", "Name", "CIDR", "AZ", "Metadata"}
	case FlagNetworks:
		return []string{"ID", "Name", "CIDR", "Metadata"}
	default:
		return []string{"ID", "Name", "State", "Metadata"}
	}
}

// FormatResourceRow formats a resource into table row cells based on category.
func FormatResourceRow(resource *state.Resource, category string) []string {
	resourceID := truncate(resource.ID, maxIDLength)
	name := truncate(resource.Name, maxNameLength)
	resState := getResourceState(resource)
	metadata := FormatMetadataColumn(resource.Tags)

	switch category {
	case FlagLoadBalancers:
		props := extractProperties(resource)

		return []string{resourceID, name, resState, props, metadata}

	case FlagPublicIPs:
		ipAddress := extractIPAddress(resource)

		return []string{resourceID, name, ipAddress, resState, metadata}

	case FlagKeys:
		fingerprint := extractFingerprint(resource)
		// Keypairs don't support metadata/tags in most cloud providers
		// They are identified by name pattern instead: {bloc-name}-keypair
		return []string{resourceID, name, fingerprint, "N/A"}

	case FlagSecurityGroups:
		rules := extractRuleCount(resource)

		return []string{resourceID, name, rules, metadata}

	case FlagSubnets:
		cidr := extractCIDR(resource)
		az := extractAvailabilityZone(resource)

		return []string{resourceID, name, cidr, az, metadata}

	case FlagNetworks:
		cidr := extractCIDR(resource)

		return []string{resourceID, name, cidr, metadata}

	default:
		return []string{resourceID, name, resState, metadata}
	}
}

// getResourceState returns a display-friendly state for a resource.
func getResourceState(resource *state.Resource) string {
	// Try to get state from properties
	if resource.State != "" {
		return resource.State
	}

	// Check properties for status/state fields
	if resource.Properties != nil {
		if status, ok := resource.Properties["status"].(string); ok {
			return status
		}

		if stateVal, ok := resource.Properties["state"].(string); ok {
			return stateVal
		}
	}

	return "unknown"
}

// extractIPAddress extracts IP address from resource properties.
func extractIPAddress(resource *state.Resource) string {
	if resource.Properties == nil {
		return "-"
	}

	if ip, ok := resource.Properties["ip_address"].(string); ok && ip != "" {
		return ip
	}

	if ip, ok := resource.Properties["address"].(string); ok && ip != "" {
		return ip
	}

	if ip, ok := resource.Properties["public_ip"].(string); ok && ip != "" {
		return ip
	}

	return "-"
}

// extractFingerprint extracts SSH key fingerprint from resource properties.
func extractFingerprint(resource *state.Resource) string {
	if resource.Properties == nil {
		return "-"
	}

	if fp, ok := resource.Properties["fingerprint"].(string); ok && fp != "" {
		return truncate(fp, maxFingerprintLength)
	}

	return "-"
}

// extractRuleCount extracts security group rule count from resource properties.
func extractRuleCount(resource *state.Resource) string {
	if resource.Properties == nil {
		return "0 rules"
	}

	// Try different property names
	if rules, ok := resource.Properties["rules"].([]interface{}); ok {
		return fmt.Sprintf("%d rules", len(rules))
	}

	if count, ok := resource.Properties["rule_count"].(int); ok {
		return fmt.Sprintf("%d rules", count)
	}

	if count, ok := resource.Properties["rule_count"].(float64); ok {
		return fmt.Sprintf("%d rules", int(count))
	}

	return "0 rules"
}

// extractCIDR extracts CIDR from resource properties.
func extractCIDR(resource *state.Resource) string {
	if resource.Properties == nil {
		return "-"
	}

	if cidr, ok := resource.Properties["cidr"].(string); ok && cidr != "" {
		return cidr
	}

	if cidr, ok := resource.Properties["cidr_block"].(string); ok && cidr != "" {
		return cidr
	}

	return "-"
}

// extractAvailabilityZone extracts availability zone from resource properties.
func extractAvailabilityZone(resource *state.Resource) string {
	if resource.Properties == nil {
		return "-"
	}

	if az, ok := resource.Properties["availability_zone"].(string); ok && az != "" {
		return az
	}

	if az, ok := resource.Properties["az"].(string); ok && az != "" {
		return az
	}

	return "-"
}

// extractProperties extracts general properties summary from resource.
func extractProperties(resource *state.Resource) string {
	if resource.Properties == nil {
		return "-"
	}

	// Try to extract meaningful properties
	props := []string{}

	if listeners, ok := resource.Properties["listeners"].([]interface{}); ok && len(listeners) > 0 {
		props = append(props, fmt.Sprintf("%d listeners", len(listeners)))
	}

	if pools, ok := resource.Properties["pools"].([]interface{}); ok && len(pools) > 0 {
		props = append(props, fmt.Sprintf("%d pools", len(pools)))
	}

	if len(props) > 0 {
		return truncate(fmt.Sprintf("%v", props), maxPropertiesLength)
	}

	return "-"
}

// truncate truncates a string to the specified length, adding "..." if needed.
func truncate(str string, maxLen int) string {
	if len(str) <= maxLen {
		return str
	}

	if maxLen < minTruncateLength {
		return str[:maxLen]
	}

	return str[:maxLen-3] + "..."
}
