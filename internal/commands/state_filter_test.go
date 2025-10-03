package commands

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/state"
)

func TestNewResourceFilter(t *testing.T) {
	tests := []struct {
		name     string
		flags    ResourceDisplayFlags
		expected bool // expected value of All flag after normalization
	}{
		{
			name:     "no flags set should default to all",
			flags:    ResourceDisplayFlags{},
			expected: true,
		},
		{
			name: "servers flag set should not enable all",
			flags: ResourceDisplayFlags{
				Servers: true,
			},
			expected: false,
		},
		{
			name: "all flag explicitly set",
			flags: ResourceDisplayFlags{
				All: true,
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := NewResourceFilter(tt.flags)
			if filter.flags.All != tt.expected {
				t.Errorf("NewResourceFilter() All flag = %v, want %v", filter.flags.All, tt.expected)
			}
		})
	}
}

func TestResourceFilter_ShouldDisplay(t *testing.T) {
	tests := []struct {
		name         string
		flags        ResourceDisplayFlags
		resourceType string
		expected     bool
	}{
		{
			name: "all flag set should display everything",
			flags: ResourceDisplayFlags{
				All: true,
			},
			resourceType: state.ResourceTypeInstance,
			expected:     true,
		},
		{
			name: "servers flag set should display instances",
			flags: ResourceDisplayFlags{
				Servers: true,
			},
			resourceType: state.ResourceTypeInstance,
			expected:     true,
		},
		{
			name: "servers flag set should not display volumes",
			flags: ResourceDisplayFlags{
				Servers: true,
			},
			resourceType: state.ResourceTypeVolume,
			expected:     false,
		},
		{
			name: "multiple flags set should display matching types",
			flags: ResourceDisplayFlags{
				Servers: true,
				Volumes: true,
			},
			resourceType: state.ResourceTypeInstance,
			expected:     true,
		},
		{
			name: "public-ips flag should match both public_ip and floating_ip",
			flags: ResourceDisplayFlags{
				PublicIPs: true,
			},
			resourceType: state.ResourceTypeFloatingIP,
			expected:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip normalization for these tests to test exact flag combinations
			filter := &ResourceFilter{flags: tt.flags}
			got := filter.ShouldDisplay(tt.resourceType)
			if got != tt.expected {
				t.Errorf("ShouldDisplay(%s) = %v, want %v", tt.resourceType, got, tt.expected)
			}
		})
	}
}

func TestResourceFilter_FilterResources(t *testing.T) {
	resources := map[string]*state.Resource{
		"compute_instance.server-1": {
			ID:   "i-123",
			Type: state.ResourceTypeInstance,
			Name: "server-1",
		},
		"block_volume.volume-1": {
			ID:   "vol-123",
			Type: state.ResourceTypeVolume,
			Name: "volume-1",
		},
		"object_storage_bucket.bucket-1": {
			ID:   "bucket-123",
			Type: state.ResourceTypeBucket,
			Name: "bucket-1",
		},
	}

	tests := []struct {
		name          string
		flags         ResourceDisplayFlags
		expectedCount int
	}{
		{
			name: "all flag should return all resources",
			flags: ResourceDisplayFlags{
				All: true,
			},
			expectedCount: 3,
		},
		{
			name: "servers flag should return only instances",
			flags: ResourceDisplayFlags{
				Servers: true,
			},
			expectedCount: 1,
		},
		{
			name: "multiple flags should return matching resources",
			flags: ResourceDisplayFlags{
				Servers: true,
				Volumes: true,
			},
			expectedCount: 2,
		},
		{
			name: "no matching flags should return empty",
			flags: ResourceDisplayFlags{
				LoadBalancers: true,
			},
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := &ResourceFilter{flags: tt.flags}
			filtered := filter.FilterResources(resources)
			if len(filtered) != tt.expectedCount {
				t.Errorf("FilterResources() returned %d resources, want %d", len(filtered), tt.expectedCount)
			}
		})
	}
}

func TestResourceFilter_GroupByDisplayCategory(t *testing.T) {
	resources := map[string]*state.Resource{
		"compute_instance.server-1": {
			ID:   "i-123",
			Type: state.ResourceTypeInstance,
			Name: "server-1",
		},
		"compute_instance.server-2": {
			ID:   "i-456",
			Type: state.ResourceTypeInstance,
			Name: "server-2",
		},
		"block_volume.volume-1": {
			ID:   "vol-123",
			Type: state.ResourceTypeVolume,
			Name: "volume-1",
		},
	}

	tests := []struct {
		name               string
		flags              ResourceDisplayFlags
		expectedCategories []string
		expectedCounts     map[string]int
	}{
		{
			name: "all flag should group all resources",
			flags: ResourceDisplayFlags{
				All: true,
			},
			expectedCategories: []string{FlagServers, FlagVolumes},
			expectedCounts: map[string]int{
				FlagServers: 2,
				FlagVolumes: 1,
			},
		},
		{
			name: "servers flag should group only instances",
			flags: ResourceDisplayFlags{
				Servers: true,
			},
			expectedCategories: []string{FlagServers},
			expectedCounts: map[string]int{
				FlagServers: 2,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := &ResourceFilter{flags: tt.flags}
			grouped := filter.GroupByDisplayCategory(resources)

			if len(grouped) != len(tt.expectedCategories) {
				t.Errorf("GroupByDisplayCategory() returned %d categories, want %d", len(grouped), len(tt.expectedCategories))
			}

			for _, category := range tt.expectedCategories {
				count, ok := tt.expectedCounts[category]
				if !ok {
					t.Errorf("Expected count for category %q not found", category)
					continue
				}

				resources, ok := grouped[category]
				if !ok {
					t.Errorf("Category %q not found in grouped resources", category)
					continue
				}

				if len(resources) != count {
					t.Errorf("GroupByDisplayCategory() category %q has %d resources, want %d", category, len(resources), count)
				}
			}
		})
	}
}

func TestContains(t *testing.T) {
	tests := []struct {
		name     string
		slice    []string
		value    string
		expected bool
	}{
		{
			name:     "value exists in slice",
			slice:    []string{"a", "b", "c"},
			value:    "b",
			expected: true,
		},
		{
			name:     "value does not exist in slice",
			slice:    []string{"a", "b", "c"},
			value:    "d",
			expected: false,
		},
		{
			name:     "empty slice",
			slice:    []string{},
			value:    "a",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := contains(tt.slice, tt.value)
			if got != tt.expected {
				t.Errorf("contains() = %v, want %v", got, tt.expected)
			}
		})
	}
}
