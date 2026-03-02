package commands

import (
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/state"
)

func TestGetDisplayHeading(t *testing.T) {
	tests := []struct {
		name     string
		flagName string
		count    int
		expected string
	}{
		{
			name:     "servers heading",
			flagName: FlagServers,
			count:    3,
			expected: "💻 Compute Instances (3)",
		},
		{
			name:     "volumes heading",
			flagName: FlagVolumes,
			count:    2,
			expected: "💾 Block Volumes (2)",
		},
		{
			name:     "buckets heading",
			flagName: FlagBuckets,
			count:    1,
			expected: "🗄️ Object Storage Buckets (1)",
		},
		{
			name:     "unknown flag",
			flagName: "unknown",
			count:    5,
			expected: "Resources (5)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetDisplayHeading(tt.flagName, tt.count)
			if got != tt.expected {
				t.Errorf("GetDisplayHeading() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestGetDisplayConfig(t *testing.T) {
	tests := []struct {
		name        string
		flagName    string
		expectEmoji string
		expectName  string
		expectOrder int
	}{
		{
			name:        "servers config",
			flagName:    FlagServers,
			expectEmoji: "💻",
			expectName:  "Compute Instances",
			expectOrder: 1,
		},
		{
			name:        "volumes config",
			flagName:    FlagVolumes,
			expectEmoji: "💾",
			expectName:  "Block Volumes",
			expectOrder: 2,
		},
		{
			name:        "unknown config",
			flagName:    "unknown",
			expectEmoji: "📦",
			expectName:  "Resources",
			expectOrder: 999,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := GetDisplayConfig(tt.flagName)
			if config.Emoji != tt.expectEmoji {
				t.Errorf("GetDisplayConfig() Emoji = %v, want %v", config.Emoji, tt.expectEmoji)
			}
			if config.DisplayName != tt.expectName {
				t.Errorf("GetDisplayConfig() DisplayName = %v, want %v", config.DisplayName, tt.expectName)
			}
			if config.SortOrder != tt.expectOrder {
				t.Errorf("GetDisplayConfig() SortOrder = %v, want %v", config.SortOrder, tt.expectOrder)
			}
		})
	}
}

func TestGetSortedDisplayCategories(t *testing.T) {
	grouped := map[string][]*state.Resource{
		FlagVolumes: {{Type: state.ResourceTypeVolume}},
		FlagServers: {{Type: state.ResourceTypeInstance}},
		FlagBuckets: {{Type: state.ResourceTypeBucket}},
	}

	categories := GetSortedDisplayCategories(grouped)

	// Check that we got all categories
	if len(categories) != 3 {
		t.Errorf("GetSortedDisplayCategories() returned %d categories, want 3", len(categories))
	}

	// Check that they're sorted by sort order
	// Servers (1) < Volumes (2) < Buckets (3)
	if categories[0] != FlagServers {
		t.Errorf("GetSortedDisplayCategories()[0] = %v, want %v", categories[0], FlagServers)
	}
	if categories[1] != FlagVolumes {
		t.Errorf("GetSortedDisplayCategories()[1] = %v, want %v", categories[1], FlagVolumes)
	}
	if categories[2] != FlagBuckets {
		t.Errorf("GetSortedDisplayCategories()[2] = %v, want %v", categories[2], FlagBuckets)
	}
}

func TestGetHeadersForCategory(t *testing.T) {
	tests := []struct {
		name     string
		category string
		expected []string
	}{
		{
			name:     "load balancers",
			category: FlagLoadBalancers,
			expected: []string{"ID", "Name", "State", "Properties", "Metadata"},
		},
		{
			name:     "public IPs",
			category: FlagPublicIPs,
			expected: []string{"ID", "Name", "IP Address", "State", "Metadata"},
		},
		{
			name:     "keys",
			category: FlagKeys,
			expected: []string{"ID", "Name", "Fingerprint", "Metadata"},
		},
		{
			name:     "default category",
			category: FlagServers,
			expected: []string{"ID", "Name", "State", "Metadata"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetHeadersForCategory(tt.category)
			if len(got) != len(tt.expected) {
				t.Errorf("GetHeadersForCategory() returned %d headers, want %d", len(got), len(tt.expected))
				return
			}

			for i, expected := range tt.expected {
				if got[i] != expected {
					t.Errorf("GetHeadersForCategory()[%d] = %v, want %v", i, got[i], expected)
				}
			}
		})
	}
}

func TestFormatResourceRow(t *testing.T) {
	tests := []struct {
		name        string
		resource    *state.Resource
		category    string
		expectedLen int
		checkValues func([]string) error
	}{
		{
			name: "basic server resource",
			resource: &state.Resource{
				ID:    "i-123456",
				Name:  "test-server",
				Type:  state.ResourceTypeInstance,
				State: "running",
			},
			category:    FlagServers,
			expectedLen: 4,
		},
		{
			name: "public IP resource with address",
			resource: &state.Resource{
				ID:   "ip-123",
				Name: "test-ip",
				Type: state.ResourceTypePublicIP,
				Properties: map[string]interface{}{
					"ip_address": "1.2.3.4",
				},
				State: "allocated",
			},
			category:    FlagPublicIPs,
			expectedLen: 5,
		},
		{
			name: "key pair with fingerprint",
			resource: &state.Resource{
				ID:   "key-123",
				Name: "test-key",
				Type: state.ResourceTypeKeyPair,
				Properties: map[string]interface{}{
					"fingerprint": "aa:bb:cc:dd:ee",
				},
			},
			category:    FlagKeys,
			expectedLen: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatResourceRow(tt.resource, tt.category)
			if len(got) != tt.expectedLen {
				t.Errorf("FormatResourceRow() returned %d columns, want %d", len(got), tt.expectedLen)
			}

			if tt.checkValues != nil {
				if err := tt.checkValues(got); err != nil {
					t.Errorf("FormatResourceRow() validation failed: %v", err)
				}
			}
		})
	}
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "short string not truncated",
			input:    "short",
			maxLen:   10,
			expected: "short",
		},
		{
			name:     "exact length not truncated",
			input:    "exactlen",
			maxLen:   8,
			expected: "exactlen",
		},
		{
			name:     "long string truncated",
			input:    "this is a very long string that needs truncation",
			maxLen:   20,
			expected: "this is a very lo...",
		},
		{
			name:     "very short maxLen",
			input:    "hello",
			maxLen:   2,
			expected: "he",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.input, tt.maxLen)
			if got != tt.expected {
				t.Errorf("truncate() = %v, want %v", got, tt.expected)
			}

			if len(got) > tt.maxLen {
				t.Errorf("truncate() length = %d, exceeds maxLen %d", len(got), tt.maxLen)
			}
		})
	}
}

func TestExtractIPAddress(t *testing.T) {
	tests := []struct {
		name     string
		resource *state.Resource
		expected string
	}{
		{
			name: "ip_address property",
			resource: &state.Resource{
				Properties: map[string]interface{}{
					"ip_address": "1.2.3.4",
				},
			},
			expected: "1.2.3.4",
		},
		{
			name: "address property",
			resource: &state.Resource{
				Properties: map[string]interface{}{
					"address": "5.6.7.8",
				},
			},
			expected: "5.6.7.8",
		},
		{
			name: "public_ip property",
			resource: &state.Resource{
				Properties: map[string]interface{}{
					"public_ip": "9.10.11.12",
				},
			},
			expected: "9.10.11.12",
		},
		{
			name: "no IP property",
			resource: &state.Resource{
				Properties: map[string]interface{}{},
			},
			expected: "-",
		},
		{
			name:     "nil properties",
			resource: &state.Resource{},
			expected: "-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractIPAddress(tt.resource)
			if got != tt.expected {
				t.Errorf("extractIPAddress() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestExtractRuleCount(t *testing.T) {
	tests := []struct {
		name     string
		resource *state.Resource
		expected string
	}{
		{
			name: "rules array",
			resource: &state.Resource{
				Properties: map[string]interface{}{
					"rules": []interface{}{"rule1", "rule2", "rule3"},
				},
			},
			expected: "3 rules",
		},
		{
			name: "rule_count int",
			resource: &state.Resource{
				Properties: map[string]interface{}{
					"rule_count": 5,
				},
			},
			expected: "5 rules",
		},
		{
			name: "rule_count float64",
			resource: &state.Resource{
				Properties: map[string]interface{}{
					"rule_count": float64(7),
				},
			},
			expected: "7 rules",
		},
		{
			name: "no rule property",
			resource: &state.Resource{
				Properties: map[string]interface{}{},
			},
			expected: "0 rules",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRuleCount(tt.resource)
			if got != tt.expected {
				t.Errorf("extractRuleCount() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestExtractCIDR(t *testing.T) {
	tests := []struct {
		name     string
		resource *state.Resource
		expected string
	}{
		{
			name: "cidr property",
			resource: &state.Resource{
				Properties: map[string]interface{}{
					"cidr": "10.0.0.0/24",
				},
			},
			expected: "10.0.0.0/24",
		},
		{
			name: "cidr_block property",
			resource: &state.Resource{
				Properties: map[string]interface{}{
					"cidr_block": "192.168.0.0/16",
				},
			},
			expected: "192.168.0.0/16",
		},
		{
			name: "no CIDR property",
			resource: &state.Resource{
				Properties: map[string]interface{}{},
			},
			expected: "-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCIDR(tt.resource)
			if got != tt.expected {
				t.Errorf("extractCIDR() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestBuildBlocSummary(t *testing.T) {
	st := &state.State{
		Provider: "aws",
		Region:   "us-east-1",
	}

	summary := buildBlocSummary(st)

	if !strings.Contains(summary, "aws") {
		t.Error("buildBlocSummary() missing provider")
	}

	if !strings.Contains(summary, "us-east-1") {
		t.Error("buildBlocSummary() missing region")
	}
}
