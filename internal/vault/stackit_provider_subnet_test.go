package vault

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"github.com/stretchr/testify/assert"
)

func TestBuildSubnetDataContainsAllRequiredFields(t *testing.T) {
	// Create a test provider
	cfg := &config.Config{
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
		Region: "eu01",
	}

	provider := &StackitVaultProvider{
		BaseVaultProvider: &providers.BaseVaultProvider{
			Config:   cfg,
			BlocName: "test-bloc",
		},
		logger: logger.Get(),
	}

	// Create test network info
	networkInfo := &subnetNetworkInfo{
		network:    "10.0.1.0",
		cidrPrefix: "10.0.1",
		gateway:    "10.0.1.1",
		lastHost:   "10.0.1.254",
	}

	// Build subnet data
	subnetData := provider.buildSubnetData("ocfp", 0, "10.0.1.0/24", networkInfo, "eu01-1")

	// Verify all required fields exist
	requiredFields := []string{
		// Original fields
		"id", "cidr_block", "cidr_prefix", "ip_0", "ip_n",
		"gateway", "dns", "az", "type",
		// New fields for Perl parity
		"subnet_cidr", "subnet_prefix", "net_cidr", "net_prefix",
		"name", "subnet_num", "provider", "provider_type",
		"parent_cidr", "environment", "region", "virtual",
	}

	for _, field := range requiredFields {
		assert.Contains(t, subnetData, field, "Field %s should be present", field)
	}

	// Verify specific values
	assert.Equal(t, "10.0.1.0/24", subnetData["subnet_cidr"])
	assert.Equal(t, "10.0.1", subnetData["subnet_prefix"])
	assert.Equal(t, "10.0.0.0/16", subnetData["net_cidr"])
	assert.Equal(t, "10.0.0", subnetData["net_prefix"])
	assert.Equal(t, "ocfp-0", subnetData["name"])
	assert.Equal(t, 0, subnetData["subnet_num"])
	assert.Equal(t, "stackit", subnetData["provider"])
	assert.Equal(t, "virtual_subnet", subnetData["provider_type"])
	assert.Equal(t, "10.0.1.0/24", subnetData["parent_cidr"])
	assert.Equal(t, "test-bloc", subnetData["environment"])
	assert.Equal(t, "eu01", subnetData["region"])
	assert.Equal(t, "true", subnetData["virtual"])
}

func TestBuildSubnetDataReservedSubnetNoVirtualFlag(t *testing.T) {
	cfg := &config.Config{
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
		Region: "eu01",
	}

	provider := &StackitVaultProvider{
		BaseVaultProvider: &providers.BaseVaultProvider{
			Config:   cfg,
			BlocName: "test-bloc",
		},
		logger: logger.Get(),
	}

	networkInfo := &subnetNetworkInfo{
		network:    "10.0.1.0",
		cidrPrefix: "10.0.1",
		gateway:    "10.0.1.1",
		lastHost:   "10.0.1.254",
	}

	// Build subnet data for reserved subnet
	subnetData := provider.buildSubnetData("reserved", 0, "10.0.1.0/24", networkInfo, "eu01-1")

	// Verify virtual flag is NOT present for reserved subnets
	assert.NotContains(t, subnetData, "virtual", "Reserved subnets should not have virtual flag")
}

func TestCalculateNetworkPrefix(t *testing.T) {
	cfg := &config.Config{}
	provider := &StackitVaultProvider{
		BaseVaultProvider: &providers.BaseVaultProvider{
			Config: cfg,
		},
		logger: logger.Get(),
	}

	tests := []struct {
		name     string
		cidr     string
		expected string
	}{
		{
			name:     "Standard /16 network",
			cidr:     "10.0.0.0/16",
			expected: "10.0.0",
		},
		{
			name:     "Standard /24 network",
			cidr:     "192.168.1.0/24",
			expected: "192.168.1",
		},
		{
			name:     "Class A network",
			cidr:     "172.16.0.0/12",
			expected: "172.16.0",
		},
		{
			name:     "Invalid CIDR",
			cidr:     "invalid",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := provider.calculateNetworkPrefix(tt.cidr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetNetworkIDFromStateReturnsEmptyWhenNoState(t *testing.T) {
	cfg := &config.Config{}
	provider := &StackitVaultProvider{
		BaseVaultProvider: &providers.BaseVaultProvider{
			Config:   cfg,
			BlocName: "test-bloc",
		},
		logger: logger.Get(),
	}

	// When no state manager exists, should return empty string
	networkID := provider.getNetworkIDFromState()
	assert.Equal(t, "", networkID)
}
