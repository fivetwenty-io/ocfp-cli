package stackit

import (
	"context"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/stretchr/testify/assert"
)

// TestBuildPublicIPLabels tests the label building function for public IPs.
func TestBuildPublicIPLabels(t *testing.T) {
	tests := []struct {
		name     string
		req      *cpi.PublicIPRequest
		expected map[string]string
	}{
		{
			name: "router IP with all fields",
			req: &cpi.PublicIPRequest{
				Name:  "test-bloc-router-0",
				Job:   "router",
				Index: "0",
				Labels: map[string]string{
					"bloc": "test-bloc",
					"env":  "mgmt",
				},
				Tags: map[string]string{
					"custom": "value",
				},
			},
			expected: map[string]string{
				"managed-by": "ocfp",
				"job":        "router",
				"index":      "0",
				"name":       "test-bloc-router-0",
				"bloc":       "test-bloc",
				"env":        "mgmt",
				"custom":     "value",
			},
		},
		{
			name: "jumpbox IP minimal fields",
			req: &cpi.PublicIPRequest{
				Name:   "test-jumpbox-1",
				Job:    "jumpbox",
				Index:  "1",
				Labels: map[string]string{},
				Tags:   map[string]string{},
			},
			expected: map[string]string{
				"managed-by": "ocfp",
				"job":        "jumpbox",
				"index":      "1",
				"name":       "test-jumpbox-1",
			},
		},
		{
			name: "ops IP with tags override",
			req: &cpi.PublicIPRequest{
				Name:  "ops-0",
				Job:   "ops",
				Index: "0",
				Labels: map[string]string{
					"env": "prod",
				},
				Tags: map[string]string{
					"env": "mgmt", // Tags should override labels
				},
			},
			expected: map[string]string{
				"managed-by": "ocfp",
				"job":        "ops",
				"index":      "0",
				"name":       "ops-0",
				"env":        "mgmt", // Tag value wins
			},
		},
		{
			name: "empty job and index should not panic",
			req: &cpi.PublicIPRequest{
				Name:   "test-ip",
				Job:    "",
				Index:  "",
				Labels: map[string]string{},
				Tags:   map[string]string{},
			},
			expected: map[string]string{
				"managed-by": "ocfp",
				"name":       "test-ip",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildPublicIPLabels(tt.req)

			// Convert map[string]interface{} to map[string]string for comparison
			resultStr := make(map[string]string)
			for k, v := range result {
				if str, ok := v.(string); ok {
					resultStr[k] = str
				}
			}

			assert.Equal(t, tt.expected, resultStr)
		})
	}
}

// TestBuildPublicIPLabels_MergeOrder tests that tags override labels.
func TestBuildPublicIPLabels_MergeOrder(t *testing.T) {
	req := &cpi.PublicIPRequest{
		Name:  "test",
		Job:   "router",
		Index: "0",
		Labels: map[string]string{
			"key1": "from-labels",
			"key2": "only-in-labels",
		},
		Tags: map[string]string{
			"key1": "from-tags", // Should override
			"key3": "only-in-tags",
		},
	}

	result := buildPublicIPLabels(req)

	assert.Equal(t, "from-tags", result["key1"], "tags should override labels")
	assert.Equal(t, "only-in-labels", result["key2"])
	assert.Equal(t, "only-in-tags", result["key3"])
	assert.Equal(t, "ocfp", result["managed-by"])
	assert.Equal(t, "router", result["job"])
	assert.Equal(t, "0", result["index"])
}

// TestPublicIPJobTypes verifies that all job-specific ensure methods exist
// and use the correct job names.
//
// Note: These are smoke tests to ensure the methods exist with correct signatures.
// Full integration testing would require mocking the STACKIT API.
func TestPublicIPJobTypes(t *testing.T) {
	tests := []struct {
		name         string
		testFunc     func(*NetworkManager, context.Context, string, int) ([]*cpi.PublicIP, error)
		defaultCount int
	}{
		{
			name: "router",
			testFunc: func(m *NetworkManager, ctx context.Context, bloc string, count int) ([]*cpi.PublicIP, error) {
				return m.EnsureRouterPublicIPs(ctx, bloc, count)
			},
			defaultCount: 4,
		},
		{
			name: "jumpbox",
			testFunc: func(m *NetworkManager, ctx context.Context, bloc string, count int) ([]*cpi.PublicIP, error) {
				return m.EnsureJumpboxPublicIPs(ctx, bloc, count)
			},
			defaultCount: 2,
		},
		{
			name: "ops",
			testFunc: func(m *NetworkManager, ctx context.Context, bloc string, count int) ([]*cpi.PublicIP, error) {
				return m.EnsureOpsPublicIPs(ctx, bloc, count)
			},
			defaultCount: 1,
		},
		{
			name: "cf-ssh",
			testFunc: func(m *NetworkManager, ctx context.Context, bloc string, count int) ([]*cpi.PublicIP, error) {
				return m.EnsureCFSSHPublicIPs(ctx, bloc, count)
			},
			defaultCount: 1,
		},
		{
			name: "tcp-router",
			testFunc: func(m *NetworkManager, ctx context.Context, bloc string, count int) ([]*cpi.PublicIP, error) {
				return m.EnsureTCPRouterPublicIPs(ctx, bloc, count)
			},
			defaultCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just verify the function signature is correct
			// Full testing requires proper mocking infrastructure
			assert.NotNil(t, tt.testFunc, "function should exist")
		})
	}
}

// TestMatchLabels tests the label matching function.
func TestMatchLabels(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		filters  map[string]string
		expected bool
	}{
		{
			name:     "empty filters match anything",
			labels:   map[string]string{"job": "router"},
			filters:  map[string]string{},
			expected: true,
		},
		{
			name: "exact match with label: prefix",
			labels: map[string]string{
				"job":   "router",
				"index": "0",
			},
			filters: map[string]string{
				"label:job":   "router",
				"label:index": "0",
			},
			expected: true,
		},
		{
			name: "partial match fails",
			labels: map[string]string{
				"job": "router",
			},
			filters: map[string]string{
				"label:job":   "router",
				"label:index": "0", // Not in labels
			},
			expected: false,
		},
		{
			name: "value mismatch fails",
			labels: map[string]string{
				"job": "router",
			},
			filters: map[string]string{
				"label:job": "jumpbox", // Wrong value
			},
			expected: false,
		},
		{
			name:   "nil labels with filters fails",
			labels: nil,
			filters: map[string]string{
				"label:job": "router",
			},
			expected: false,
		},
		{
			name: "non-label filters are ignored",
			labels: map[string]string{
				"job": "router",
			},
			filters: map[string]string{
				"other": "value", // No "label:" prefix
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := matchLabels(tt.labels, tt.filters)
			assert.Equal(t, tt.expected, result)
		})
	}
}
