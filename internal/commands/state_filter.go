package commands

import (
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/spf13/cobra"
)

// ResourceFilter handles filtering of resources based on display flags and property filters.
type ResourceFilter struct {
	flags           ResourceDisplayFlags
	propertyFilters *PropertyFilterSet
}

// NewResourceFilter creates a new resource filter with the given flags.
// If no flags are set, it defaults to showing all resources.
func NewResourceFilter(flags ResourceDisplayFlags) *ResourceFilter {
	// Apply normalization: if no specific flags set, enable all
	flags.NormalizeFlags()

	return &ResourceFilter{
		flags:           flags,
		propertyFilters: nil,
	}
}

// NewResourceFilterWithProperties creates a resource filter with both display flags and property filters.
func NewResourceFilterWithProperties(flags ResourceDisplayFlags, propertyFilters *PropertyFilterSet) *ResourceFilter {
	// Apply normalization: if no specific flags set, enable all
	flags.NormalizeFlags()

	return &ResourceFilter{
		flags:           flags,
		propertyFilters: propertyFilters,
	}
}

// ParseDisplayFlagsFromCmd extracts display flags from a cobra command.
func ParseDisplayFlagsFromCmd(cmd *cobra.Command) ResourceDisplayFlags {
	flags := ResourceDisplayFlags{
		All:            getFlagBool(cmd, FlagAll),
		Servers:        getFlagBool(cmd, FlagServers) || getFlagBool(cmd, "instances"),
		Volumes:        getFlagBool(cmd, FlagVolumes),
		Buckets:        getFlagBool(cmd, FlagBuckets),
		LoadBalancers:  getFlagBool(cmd, FlagLoadBalancers) || getFlagBool(cmd, "lbs"),
		PublicIPs:      getFlagBool(cmd, FlagPublicIPs),
		Keys:           getFlagBool(cmd, FlagKeys) || getFlagBool(cmd, "key-pairs"),
		Networks:       getFlagBool(cmd, FlagNetworks) || getFlagBool(cmd, "nets"),
		Subnets:        getFlagBool(cmd, FlagSubnets),
		SecurityGroups: getFlagBool(cmd, FlagSecurityGroups) || getFlagBool(cmd, "sgs"),
		Routers:        getFlagBool(cmd, FlagRouters),
		Snapshots:      getFlagBool(cmd, FlagSnapshots),
	}

	return flags
}

// getFlagBool safely retrieves a boolean flag value from a command.
func getFlagBool(cmd *cobra.Command, name string) bool {
	flag := cmd.Flags().Lookup(name)
	if flag == nil {
		return false
	}

	val, err := cmd.Flags().GetBool(name)
	if err != nil {
		return false
	}

	return val
}

// ShouldDisplay determines if a resource type should be displayed based on filter flags.
func (f *ResourceFilter) ShouldDisplay(resourceType string) bool {
	// If --all flag is set, display everything
	if f.flags.All {
		return true
	}

	// Check if resource type matches any enabled flag
	enabledFlags := f.flags.GetEnabledFlags()

	for flagName, enabled := range enabledFlags {
		if !enabled {
			continue
		}

		// Get resource types for this flag
		resourceTypes := GetResourceTypesForFlag(flagName)

		// Check if our resource type is in the list
		if contains(resourceTypes, resourceType) {
			return true
		}
	}

	return false
}

// FilterResources filters a map of resources based on the display flags and property filters.
// Filtering happens in two stages:
// 1. Resource type filtering (based on display flags)
// 2. Property filtering (based on property filter queries).
func (f *ResourceFilter) FilterResources(resources map[string]*state.Resource) map[string]*state.Resource {
	filtered := make(map[string]*state.Resource)

	for key, resource := range resources {
		// Stage 1: Check resource type filter
		if !f.ShouldDisplay(resource.Type) {
			continue
		}

		// Stage 2: Check property filters (if any)
		if f.propertyFilters != nil && !f.propertyFilters.Matches(resource) {
			continue
		}

		filtered[key] = resource
	}

	return filtered
}

// GroupByDisplayCategory groups resources by their display flag category.
// Returns a map of flag name to list of resources.
func (f *ResourceFilter) GroupByDisplayCategory(resources map[string]*state.Resource) map[string][]*state.Resource {
	grouped := make(map[string][]*state.Resource)

	for _, resource := range resources {
		if !f.ShouldDisplay(resource.Type) {
			continue
		}

		// Determine which flag category this resource belongs to
		flagName := GetFlagForResourceType(resource.Type)
		if flagName == "" {
			// Unknown resource type, skip
			continue
		}

		grouped[flagName] = append(grouped[flagName], resource)
	}

	return grouped
}

// GetDisplayFlags returns the display flags used by this filter.
func (f *ResourceFilter) GetDisplayFlags() ResourceDisplayFlags {
	return f.flags
}

// contains checks if a string slice contains a value.
func contains(slice []string, value string) bool {
	for _, item := range slice {
		if item == value {
			return true
		}
	}

	return false
}
