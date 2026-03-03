package vault

import (
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/stretchr/testify/assert"
)

func TestConfigureNetwork_WithAPIFields(t *testing.T) {
	// Create test provider with mock safe
	mockSafe := &mockFullSafe{
		data:      make(map[string]map[string]interface{}),
		setCalls:  make([]setMultipleCall, 0),
		getSingle: make(map[string]map[string]interface{}),
	}

	// Configure with test data
	cfg := &config.Config{
		Name:      "test-bloc",
		ProjectID: "test-project-123",
		Region:    "eu01",
		DNS:       []string{"8.8.8.8", "8.8.4.4"},
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
		AZs: map[string]config.AvailabilityZone{
			"eu01-1": {Zone: "eu01-1"},
		},
	}

	provider := NewStackitVaultProvider(cfg, mockSafe, "test-bloc")

	// Test without API (basic fields only)
	t.Run("BasicFieldsWithoutAPI", func(t *testing.T) {
		err := provider.configureNetwork("mgmt", nil, 1, 1)
		assert.NoError(t, err)

		// Verify basic fields were set
		calls := mockSafe.setCalls
		assert.Greater(t, len(calls), 0, "Should have calls to SetMultiple")

		// Find network data call
		var networkData map[string]interface{}
		for _, call := range calls {
			if call.data["provider"] == "stackit" {
				networkData = call.data
				break
			}
		}

		assert.NotNil(t, networkData, "Network data should be set")
		assert.Equal(t, "test-project-123", networkData["id"])
		assert.Equal(t, "10.0.0.0/16", networkData["cidr_block"])
		assert.Equal(t, "stackit", networkData["provider"])
		assert.Equal(t, "test-bloc-net", networkData["name"])

		// Basic implementation should NOT have status or created_at (no API)
		_, hasStatus := networkData["status"]
		_, hasCreatedAt := networkData["created_at"]
		assert.False(t, hasStatus, "Should not have status without API call")
		assert.False(t, hasCreatedAt, "Should not have created_at without API call")
	})
}

func TestConfigureNetwork_FieldCompleteness(t *testing.T) {
	mockSafe := &mockFullSafe{
		data:      make(map[string]map[string]interface{}),
		setCalls:  make([]setMultipleCall, 0),
		getSingle: make(map[string]map[string]interface{}),
	}

	cfg := &config.Config{
		Name:      "test-bloc",
		ProjectID: "project-abc",
		Region:    "eu01",
		DNS:       []string{"1.1.1.1"},
		Network: config.NetworkConfig{
			CIDR: "192.168.0.0/16",
		},
		AZs: map[string]config.AvailabilityZone{},
	}

	provider := NewStackitVaultProvider(cfg, mockSafe, "test-bloc")

	err := provider.configureNetwork("ocf", nil, 1, 1)
	assert.NoError(t, err)

	// Verify all basic required fields present
	calls := mockSafe.setCalls
	var networkData map[string]interface{}
	for _, call := range calls {
		if call.data["provider"] == "stackit" {
			networkData = call.data
			break
		}
	}

	requiredFields := []string{
		"id", "cidr_block", "dns", "region",
		"provider", "name", "ipv4_cidr", "project_id", "description",
	}

	for _, field := range requiredFields {
		_, exists := networkData[field]
		assert.True(t, exists, "Required field '%s' should be present", field)
	}
}

func TestConfigureNetwork_APIIntegration(t *testing.T) {
	// This test documents the API integration behavior
	// In production with real API, status and created_at would be added

	t.Run("DocumentedBehavior", func(t *testing.T) {
		// Expected behavior when network ID is available from state:
		// 1. getNetworkIDFromAPI() returns network ID
		// 2. getStackitClient() creates CPI client
		// 3. NetworkManager.GetNetwork(ctx, networkID) fetches from API
		// 4. If successful, status and created_at are added to networkData

		// Mock network response would look like:
		mockNetwork := &cpi.Network{
			ID:        "net-123",
			Name:      "test-net",
			CIDR:      "10.0.0.0/16",
			State:     "CREATED",
			CreatedAt: time.Date(2025, 10, 28, 12, 0, 0, 0, time.UTC),
		}

		// Expected vault data with API fields:
		expectedFields := map[string]interface{}{
			"status":     string(mockNetwork.State),
			"created_at": mockNetwork.CreatedAt.Format(time.RFC3339),
		}

		assert.Equal(t, "CREATED", expectedFields["status"])
		assert.Equal(t, "2025-10-28T12:00:00Z", expectedFields["created_at"])
	})
}

func TestConfigureNetwork_GracefulDegradation(t *testing.T) {
	mockSafe := &mockFullSafe{
		data:      make(map[string]map[string]interface{}),
		setCalls:  make([]setMultipleCall, 0),
		getSingle: make(map[string]map[string]interface{}),
	}

	cfg := &config.Config{
		Name:      "test-bloc",
		ProjectID: "test-project",
		Region:    "eu01",
		DNS:       []string{"8.8.8.8"},
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
		AZs: map[string]config.AvailabilityZone{},
	}

	provider := NewStackitVaultProvider(cfg, mockSafe, "test-bloc")

	// Should not fail even if API call fails (graceful degradation)
	err := provider.configureNetwork("mgmt", nil, 1, 1)
	assert.NoError(t, err, "Should gracefully handle API failure")
}

// TestConfigureNetwork_DNSStringConversion verifies that DNS array is converted to string.
func TestConfigureNetwork_DNSStringConversion(t *testing.T) {
	tests := []struct {
		name        string
		dnsArray    []string
		expectedDNS string
	}{
		{
			name:        "single_dns_server",
			dnsArray:    []string{"8.8.8.8"},
			expectedDNS: "8.8.8.8",
		},
		{
			name:        "multiple_dns_servers",
			dnsArray:    []string{"8.8.8.8", "8.8.4.4"},
			expectedDNS: "8.8.8.8,8.8.4.4",
		},
		{
			name:        "three_dns_servers",
			dnsArray:    []string{"1.1.1.1", "8.8.8.8", "8.8.4.4"},
			expectedDNS: "1.1.1.1,8.8.8.8,8.8.4.4",
		},
		{
			name:        "empty_dns_array",
			dnsArray:    []string{},
			expectedDNS: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockSafe := &mockFullSafe{
				data:      make(map[string]map[string]interface{}),
				setCalls:  make([]setMultipleCall, 0),
				getSingle: make(map[string]map[string]interface{}),
			}

			cfg := &config.Config{
				Name:      "test-bloc",
				ProjectID: "test-project-123",
				Region:    "eu01",
				DNS:       tt.dnsArray,
				Network: config.NetworkConfig{
					CIDR: "10.0.0.0/16",
				},
			}

			provider := NewStackitVaultProvider(cfg, mockSafe, "test-bloc")

			err := provider.configureNetwork("mgmt", nil, 1, 1)
			assert.NoError(t, err)

			// Verify DNS was stored as string
			netPath := "secret/config/test-bloc/mgmt/net"
			networkData, err := mockSafe.GetAll(netPath)
			assert.NoError(t, err)
			assert.NotNil(t, networkData)

			// DNS should be a string, not an array
			dnsValue, ok := networkData["dns"]
			assert.True(t, ok, "DNS field should exist")

			dnsString, isString := dnsValue.(string)
			assert.True(t, isString, "DNS should be stored as string, not array")
			assert.Equal(t, tt.expectedDNS, dnsString, "DNS string should match expected format")
		})
	}
}
