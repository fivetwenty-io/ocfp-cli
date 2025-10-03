package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
)

// TestFormatStateOutput tests the state table formatter with various resource counts
func TestFormatStateOutput(t *testing.T) {
	tests := []struct {
		name                   string
		state                  *state.State
		expectedSections       int
		expectedTotalResources int
	}{
		{
			name: "State with network resources",
			state: &state.State{
				BlocName: "test-bloc",
				Provider: "aws",
				Region:   "us-east-1",
				Resources: map[string]*state.Resource{
					"net-1": {
						ID:   "vpc-12345",
						Type: "network",
						Name: "test-vpc",
						Properties: map[string]interface{}{
							"state": "available",
						},
					},
					"sg-1": {
						ID:   "sg-67890",
						Type: "security_group",
						Name: "test-sg",
						Properties: map[string]interface{}{
							"state": "active",
						},
					},
				},
			},
			expectedSections:       2, // Network section + Total section
			expectedTotalResources: 2,
		},
		{
			name: "State with mixed resource types",
			state: &state.State{
				BlocName: "multi-bloc",
				Provider: "stackit",
				Region:   "eu-central-1",
				Resources: map[string]*state.Resource{
					"net-1": {
						ID:   "net-12345",
						Type: "network",
						Name: "test-network",
					},
					"vm-1": {
						ID:   "i-67890",
						Type: "compute_instance",
						Name: "test-vm",
					},
					"vol-1": {
						ID:   "vol-abc123",
						Type: "block_volume",
						Name: "test-volume",
					},
					"bucket-1": {
						ID:   "test-bucket",
						Type: "object_storage_bucket",
						Name: "test-bucket",
					},
				},
			},
			expectedSections:       4, // Network, Compute, Storage sections + Total
			expectedTotalResources: 4,
		},
		{
			name: "Empty state",
			state: &state.State{
				BlocName:  "empty-bloc",
				Provider:  "aws",
				Resources: map[string]*state.Resource{},
			},
			expectedSections:       1, // Only Total section (with 0 resources)
			expectedTotalResources: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table := FormatStateOutput(tt.state)

			// Verify table title contains bloc name
			if table.Title == "" {
				t.Error("Expected table title to be set")
			}

			// Verify summary contains provider info
			if table.Summary == "" && tt.state.Provider != "" {
				t.Error("Expected table summary to contain provider info")
			}

			// Verify section count
			if len(table.Sections) != tt.expectedSections {
				t.Errorf("Expected %d sections, got %d", tt.expectedSections, len(table.Sections))
			}

			// Count total rows across all resource sections (excluding Total section)
			totalRows := 0
			for _, section := range table.Sections {
				if section.Title != "" && len(section.Headers) > 0 {
					totalRows += len(section.Rows)
				}
			}

			if totalRows != tt.expectedTotalResources {
				t.Errorf("Expected %d total resource rows, got %d", tt.expectedTotalResources, totalRows)
			}
		})
	}
}

// TestFormatStateOutputResourceOrdering tests that resources are sorted correctly
func TestFormatStateOutputResourceOrdering(t *testing.T) {
	st := &state.State{
		BlocName: "test-bloc",
		Provider: "aws",
		Resources: map[string]*state.Resource{
			"net-1": {
				ID:   "vpc-3",
				Type: "network",
				Name: "vpc-c",
			},
			"net-2": {
				ID:   "vpc-1",
				Type: "network",
				Name: "vpc-a",
			},
			"net-3": {
				ID:   "vpc-2",
				Type: "network",
				Name: "vpc-b",
			},
			"sg-1": {
				ID:   "sg-2",
				Type: "security_group",
				Name: "sg-b",
			},
			"sg-2": {
				ID:   "sg-1",
				Type: "security_group",
				Name: "sg-a",
			},
		},
	}

	table := FormatStateOutput(st)

	// Find the Network section
	var networkSection *ui.Section
	for i := range table.Sections {
		if len(table.Sections[i].Rows) > 0 && table.Sections[i].Headers != nil {
			networkSection = &table.Sections[i]
			break
		}
	}

	if networkSection == nil {
		t.Fatal("Expected to find network section")
	}

	// Verify resources are sorted by type then by name
	// Should be: network/vpc-a, network/vpc-b, network/vpc-c, security_group/sg-a, security_group/sg-b
	expectedOrder := []string{
		"vpc-a", "vpc-b", "vpc-c", "sg-a", "sg-b",
	}

	if len(networkSection.Rows) != len(expectedOrder) {
		t.Fatalf("Expected %d rows, got %d", len(expectedOrder), len(networkSection.Rows))
	}

	for i, expectedName := range expectedOrder {
		// Name is in column index 2
		if len(networkSection.Rows[i]) < 3 {
			t.Fatalf("Row %d has fewer than 3 columns", i)
		}
		actualName := networkSection.Rows[i][2]
		if actualName != expectedName {
			t.Errorf("Row %d: expected name %q, got %q", i, expectedName, actualName)
		}
	}
}

// TestGroupResourcesByCategory tests resource categorization
func TestGroupResourcesByCategory(t *testing.T) {
	resources := map[string]*state.Resource{
		"net-1": {
			Type: "network",
		},
		"vm-1": {
			Type: "compute_instance",
		},
		"vol-1": {
			Type: "block_volume",
		},
		"bucket-1": {
			Type: "object_storage_bucket",
		},
		"sg-1": {
			Type: "security_group",
		},
		"keypair-1": {
			Type: "ssh_key_pair",
		},
	}

	grouped := groupResourcesByCategory(resources)

	// Verify network category (network, security_group)
	if len(grouped[state.CategoryNetwork]) != 2 {
		t.Errorf("Expected 2 network resources, got %d", len(grouped[state.CategoryNetwork]))
	}

	// Verify compute category (compute_instance, ssh_keypair, block_volume)
	if len(grouped[state.CategoryCompute]) != 3 {
		t.Errorf("Expected 3 compute resources, got %d", len(grouped[state.CategoryCompute]))
	}

	// Verify storage category (object_storage_bucket only)
	if len(grouped[state.CategoryStorage]) != 1 {
		t.Errorf("Expected 1 storage resource, got %d", len(grouped[state.CategoryStorage]))
	}
}

// TestGetResourceState tests state extraction from resource properties
func TestGetResourceState(t *testing.T) {
	tests := []struct {
		name          string
		resource      *state.Resource
		expectedState string
	}{
		{
			name: "Resource with State field",
			resource: &state.Resource{
				State: "running",
			},
			expectedState: "running",
		},
		{
			name: "Resource with status in properties",
			resource: &state.Resource{
				Properties: map[string]interface{}{
					"status": "active",
				},
			},
			expectedState: "active",
		},
		{
			name: "Resource with state in properties",
			resource: &state.Resource{
				Properties: map[string]interface{}{
					"state": "available",
				},
			},
			expectedState: "available",
		},
		{
			name: "Resource with no state info",
			resource: &state.Resource{
				Properties: map[string]interface{}{},
			},
			expectedState: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actualState := getResourceState(tt.resource)
			if actualState != tt.expectedState {
				t.Errorf("Expected state %q, got %q", tt.expectedState, actualState)
			}
		})
	}
}

// TestTruncate tests string truncation
func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "Short string",
			input:    "hello",
			maxLen:   10,
			expected: "hello",
		},
		{
			name:     "Exact length",
			input:    "hello",
			maxLen:   5,
			expected: "hello",
		},
		{
			name:     "Long string",
			input:    "this is a very long string that needs truncation",
			maxLen:   20,
			expected: "this is a very lo...",
		},
		{
			name:     "Very short maxLen",
			input:    "hello",
			maxLen:   2,
			expected: "he",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := truncate(tt.input, tt.maxLen)
			if actual != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, actual)
			}
			if len(actual) > tt.maxLen {
				t.Errorf("Truncated string length %d exceeds maxLen %d", len(actual), tt.maxLen)
			}
		})
	}
}

// TestStateDisplayWithMissingFile tests that Load creates empty state for missing files
func TestStateDisplayWithMissingFile(t *testing.T) {
	// Create a temporary directory that has no state file
	tmpDir, err := os.MkdirTemp("", "ocfp-state-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a state manager for a non-existent bloc
	stateDir := filepath.Join(tmpDir, "nonexistent-bloc")
	manager, err := state.NewManager(stateDir)
	if err != nil {
		t.Fatalf("Failed to create state manager: %v", err)
	}

	// Attempt to load state - should create empty state, not error
	st, err := manager.Load("nonexistent-bloc")
	if err != nil {
		t.Fatalf("Unexpected error when loading non-existent state file: %v", err)
	}

	// Verify empty state was created
	if st == nil {
		t.Fatal("Expected state to be created, got nil")
	}

	if len(st.Resources) != 0 {
		t.Errorf("Expected empty resources, got %d resources", len(st.Resources))
	}

	if st.BlocName != "nonexistent-bloc" {
		t.Errorf("Expected bloc name 'nonexistent-bloc', got %q", st.BlocName)
	}
}

// TestCategoryName tests category name conversion
func TestCategoryName(t *testing.T) {
	tests := []struct {
		category state.ResourceCategory
		expected string
	}{
		{state.CategoryNetwork, "Network"},
		{state.CategoryCompute, "Compute"},
		{state.CategoryStorage, "Storage"},
		{state.ResourceCategory("unknown"), "Other"},
	}

	for _, tt := range tests {
		t.Run(string(tt.category), func(t *testing.T) {
			actual := categoryName(tt.category)
			if actual != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, actual)
			}
		})
	}
}

// TestFormatStateOutputFiltered tests the filtered state display functionality.
func TestFormatStateOutputFiltered(t *testing.T) {
	tests := []struct {
		name                 string
		state                *state.State
		flags                ResourceDisplayFlags
		expectedSectionCount int
		expectServerSection  bool
		expectVolumeSection  bool
		expectBucketSection  bool
	}{
		{
			name: "Show all resources (default)",
			state: &state.State{
				BlocName: "test-bloc",
				Provider: "aws",
				Region:   "us-east-1",
				Resources: map[string]*state.Resource{
					"compute_instance.server-1": {
						ID:    "i-123",
						Type:  state.ResourceTypeInstance,
						Name:  "server-1",
						State: "running",
					},
					"block_volume.volume-1": {
						ID:    "vol-123",
						Type:  state.ResourceTypeVolume,
						Name:  "volume-1",
						State: "in-use",
					},
				},
			},
			flags: ResourceDisplayFlags{
				All: true,
			},
			expectedSectionCount: 3, // Servers + Volumes + Total
			expectServerSection:  true,
			expectVolumeSection:  true,
		},
		{
			name: "Show only servers",
			state: &state.State{
				BlocName: "test-bloc",
				Provider: "aws",
				Region:   "us-east-1",
				Resources: map[string]*state.Resource{
					"compute_instance.server-1": {
						ID:    "i-123",
						Type:  state.ResourceTypeInstance,
						Name:  "server-1",
						State: "running",
					},
					"block_volume.volume-1": {
						ID:    "vol-123",
						Type:  state.ResourceTypeVolume,
						Name:  "volume-1",
						State: "in-use",
					},
				},
			},
			flags: ResourceDisplayFlags{
				Servers: true,
			},
			expectedSectionCount: 2, // Servers + Total
			expectServerSection:  true,
			expectVolumeSection:  false,
		},
		{
			name: "Show servers and volumes",
			state: &state.State{
				BlocName: "test-bloc",
				Provider: "aws",
				Region:   "us-east-1",
				Resources: map[string]*state.Resource{
					"compute_instance.server-1": {
						ID:    "i-123",
						Type:  state.ResourceTypeInstance,
						Name:  "server-1",
						State: "running",
					},
					"block_volume.volume-1": {
						ID:    "vol-123",
						Type:  state.ResourceTypeVolume,
						Name:  "volume-1",
						State: "in-use",
					},
					"object_storage_bucket.bucket-1": {
						ID:    "bucket-123",
						Type:  state.ResourceTypeBucket,
						Name:  "bucket-1",
						State: "active",
					},
				},
			},
			flags: ResourceDisplayFlags{
				Servers: true,
				Volumes: true,
			},
			expectedSectionCount: 3, // Servers + Volumes + Total
			expectServerSection:  true,
			expectVolumeSection:  true,
			expectBucketSection:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := &ResourceFilter{flags: tt.flags}
			table := FormatStateOutputFiltered(tt.state, filter)

			if table == nil {
				t.Fatal("FormatStateOutputFiltered returned nil")
			}

			// Check section count
			if len(table.Sections) != tt.expectedSectionCount {
				t.Errorf("Expected %d sections, got %d", tt.expectedSectionCount, len(table.Sections))
			}

			// Check for specific sections
			hasServers := false
			hasVolumes := false
			hasBuckets := false

			for _, section := range table.Sections {
				if section.Title != "" {
					if strings.Contains(section.Title, "💻") {
						hasServers = true
					}
					if strings.Contains(section.Title, "💾") {
						hasVolumes = true
					}
					if strings.Contains(section.Title, "🗄️") {
						hasBuckets = true
					}
				}
			}

			if hasServers != tt.expectServerSection {
				t.Errorf("Expected server section: %v, got: %v", tt.expectServerSection, hasServers)
			}
			if hasVolumes != tt.expectVolumeSection {
				t.Errorf("Expected volume section: %v, got: %v", tt.expectVolumeSection, hasVolumes)
			}
			if hasBuckets != tt.expectBucketSection {
				t.Errorf("Expected bucket section: %v, got: %v", tt.expectBucketSection, hasBuckets)
			}
		})
	}
}

// TestFilterResourceChanges tests filtering of sync output changes.
func TestFilterResourceChanges(t *testing.T) {
	changes := []ResourceChange{
		{
			ResourceType: state.ResourceTypeInstance,
			ResourceID:   "i-123",
			ResourceName: "server-1",
		},
		{
			ResourceType: state.ResourceTypeVolume,
			ResourceID:   "vol-123",
			ResourceName: "volume-1",
		},
		{
			ResourceType: state.ResourceTypeBucket,
			ResourceID:   "bucket-123",
			ResourceName: "bucket-1",
		},
	}

	tests := []struct {
		name          string
		filter        *ResourceFilter
		expectedCount int
	}{
		{
			name: "All flag shows all changes",
			filter: &ResourceFilter{
				flags: ResourceDisplayFlags{All: true},
			},
			expectedCount: 3,
		},
		{
			name: "Servers flag shows only instances",
			filter: &ResourceFilter{
				flags: ResourceDisplayFlags{Servers: true},
			},
			expectedCount: 1,
		},
		{
			name: "Multiple flags show matching resources",
			filter: &ResourceFilter{
				flags: ResourceDisplayFlags{
					Servers: true,
					Volumes: true,
				},
			},
			expectedCount: 2,
		},
		{
			name: "No matching flags shows nothing",
			filter: &ResourceFilter{
				flags: ResourceDisplayFlags{LoadBalancers: true},
			},
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := filterResourceChanges(changes, tt.filter)
			if len(filtered) != tt.expectedCount {
				t.Errorf("Expected %d filtered changes, got %d", tt.expectedCount, len(filtered))
			}
		})
	}
}

// TestGroupChangesByFlag tests grouping of resource changes by flag.
func TestGroupChangesByFlag(t *testing.T) {
	changes := []ResourceChange{
		{
			ResourceType: state.ResourceTypeInstance,
			ResourceID:   "i-123",
			ResourceName: "server-1",
		},
		{
			ResourceType: state.ResourceTypeInstance,
			ResourceID:   "i-456",
			ResourceName: "server-2",
		},
		{
			ResourceType: state.ResourceTypeVolume,
			ResourceID:   "vol-123",
			ResourceName: "volume-1",
		},
	}

	grouped := groupChangesByFlag(changes)

	// Check that we have the right number of groups
	if len(grouped) != 2 {
		t.Errorf("Expected 2 groups, got %d", len(grouped))
	}

	// Check servers group
	serversGroup, ok := grouped[FlagServers]
	if !ok {
		t.Error("Servers group not found")
	} else if len(serversGroup) != 2 {
		t.Errorf("Expected 2 servers, got %d", len(serversGroup))
	}

	// Check volumes group
	volumesGroup, ok := grouped[FlagVolumes]
	if !ok {
		t.Error("Volumes group not found")
	} else if len(volumesGroup) != 1 {
		t.Errorf("Expected 1 volume, got %d", len(volumesGroup))
	}
}
