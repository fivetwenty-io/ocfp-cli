package vault

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStackitProvider_BOSHMetaAZ_FirstAZ_Deterministic verifies that BOSH meta
// always uses the first AZ (alphabetically sorted) regardless of map iteration order.
func TestStackitProvider_BOSHMetaAZ_FirstAZ_Deterministic(t *testing.T) {
	tests := []struct {
		name            string
		azs             map[string]config.AvailabilityZone
		expectedFirstAZ string
	}{
		{
			name: "eu01_region_three_azs",
			azs: map[string]config.AvailabilityZone{
				"eu01-3": {Zone: "eu01-3", CloudProperties: `{"availability_zone": "eu01-3"}`},
				"eu01-1": {Zone: "eu01-1", CloudProperties: `{"availability_zone": "eu01-1"}`},
				"eu01-2": {Zone: "eu01-2", CloudProperties: `{"availability_zone": "eu01-2"}`},
			},
			expectedFirstAZ: "eu01-1",
		},
		{
			name: "us_west_region_two_azs",
			azs: map[string]config.AvailabilityZone{
				"us-west-2b": {Zone: "us-west-2b"},
				"us-west-2a": {Zone: "us-west-2a"},
			},
			expectedFirstAZ: "us-west-2a",
		},
		{
			name: "single_az",
			azs: map[string]config.AvailabilityZone{
				"eu01-1": {Zone: "eu01-1"},
			},
			expectedFirstAZ: "eu01-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Name:     "test-bloc",
				Provider: "stackit",
				Region:   "eu01",
				AZs:      tt.azs,
			}

			provider := &StackitVaultProvider{
				BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
				logger:            logger.Get(),
			}

			// Run multiple times to ensure deterministic behavior
			for i := 0; i < 10; i++ {
				az := provider.getAvailabilityZone(0)
				assert.Equal(t, tt.expectedFirstAZ, az,
					"Iteration %d: First AZ must be %s, got %s", i+1, tt.expectedFirstAZ, az)
			}
		})
	}
}

// TestStackitProvider_BOSHSubnetAZ_MgmtAndOCF verifies that both mgmt and ocf
// BOSH directors use the first AZ consistently.
func TestStackitProvider_BOSHSubnetAZ_MgmtAndOCF(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Region:   "eu01",
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
		AZs: map[string]config.AvailabilityZone{
			"eu01-3": {Zone: "eu01-3", CloudProperties: `{"availability_zone": "eu01-3"}`},
			"eu01-1": {Zone: "eu01-1", CloudProperties: `{"availability_zone": "eu01-1"}`},
			"eu01-2": {Zone: "eu01-2", CloudProperties: `{"availability_zone": "eu01-2"}`},
		},
	}

	provider := &StackitVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
		logger:            logger.Get(),
	}

	t.Run("mgmt_bosh_uses_first_az", func(t *testing.T) {
		// Subnet 0 should always get first AZ (eu01-1)
		firstAZ := provider.getAvailabilityZone(0)
		assert.Equal(t, "eu01-1", firstAZ, "mgmt BOSH must use first AZ (eu01-1)")

		// Verify it's consistent across multiple calls
		for i := 0; i < 5; i++ {
			az := provider.getAvailabilityZone(0)
			assert.Equal(t, "eu01-1", az, "mgmt BOSH AZ must be consistent")
		}
	})

	t.Run("ocf_bosh_uses_first_az", func(t *testing.T) {
		// Subnet 0 should always get first AZ (eu01-1) for OCF as well
		firstAZ := provider.getAvailabilityZone(0)
		assert.Equal(t, "eu01-1", firstAZ, "ocf BOSH must use first AZ (eu01-1)")

		// Verify it's consistent across multiple calls
		for i := 0; i < 5; i++ {
			az := provider.getAvailabilityZone(0)
			assert.Equal(t, "eu01-1", az, "ocf BOSH AZ must be consistent")
		}
	})

	t.Run("subnet_az_distribution", func(t *testing.T) {
		// Verify proper distribution across all subnets
		expectedAZs := []string{"eu01-1", "eu01-2", "eu01-3"}
		for i := 0; i < 3; i++ {
			az := provider.getAvailabilityZone(i)
			assert.Equal(t, expectedAZs[i], az,
				"Subnet %d should use AZ %s", i, expectedAZs[i])
		}
	})
}

// TestAWSProvider_BOSHMetaAZ_FirstAZ_Deterministic verifies that AWS BOSH meta
// always uses the first AZ (alphabetically sorted) regardless of map iteration order.
func TestAWSProvider_BOSHMetaAZ_FirstAZ_Deterministic(t *testing.T) {
	tests := []struct {
		name            string
		azs             map[string]config.AvailabilityZone
		expectedFirstAZ string
	}{
		{
			name: "us_east_three_azs",
			azs: map[string]config.AvailabilityZone{
				"us-east-1c": {Zone: "us-east-1c"},
				"us-east-1a": {Zone: "us-east-1a"},
				"us-east-1b": {Zone: "us-east-1b"},
			},
			expectedFirstAZ: "us-east-1a",
		},
		{
			name: "us_west_two_azs",
			azs: map[string]config.AvailabilityZone{
				"us-west-2b": {Zone: "us-west-2b"},
				"us-west-2a": {Zone: "us-west-2a"},
			},
			expectedFirstAZ: "us-west-2a",
		},
		{
			name: "single_az",
			azs: map[string]config.AvailabilityZone{
				"us-east-1a": {Zone: "us-east-1a"},
			},
			expectedFirstAZ: "us-east-1a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Name:     "test-bloc",
				Provider: "aws",
				Region:   "us-east-1",
				AZs:      tt.azs,
			}

			provider := &AWSVaultProvider{
				BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
			}

			// Run multiple times to ensure deterministic behavior
			for i := 0; i < 10; i++ {
				az := provider.getAvailabilityZone(0)
				assert.Equal(t, tt.expectedFirstAZ, az,
					"Iteration %d: First AZ must be %s, got %s", i+1, tt.expectedFirstAZ, az)
			}
		})
	}
}

// TestAWSProvider_BOSHSubnetAZ_MgmtAndOCF verifies that both mgmt and ocf
// BOSH directors use the first AZ consistently for AWS.
func TestAWSProvider_BOSHSubnetAZ_MgmtAndOCF(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "aws",
		Region:   "us-east-1",
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
		AZs: map[string]config.AvailabilityZone{
			"us-east-1c": {Zone: "us-east-1c"},
			"us-east-1a": {Zone: "us-east-1a"},
			"us-east-1b": {Zone: "us-east-1b"},
		},
	}

	provider := &AWSVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
	}

	t.Run("mgmt_bosh_uses_first_az", func(t *testing.T) {
		// Subnet 0 should always get first AZ (us-east-1a)
		firstAZ := provider.getAvailabilityZone(0)
		assert.Equal(t, "us-east-1a", firstAZ, "mgmt BOSH must use first AZ (us-east-1a)")

		// Verify it's consistent across multiple calls
		for i := 0; i < 5; i++ {
			az := provider.getAvailabilityZone(0)
			assert.Equal(t, "us-east-1a", az, "mgmt BOSH AZ must be consistent")
		}
	})

	t.Run("ocf_bosh_uses_first_az", func(t *testing.T) {
		// Subnet 0 should always get first AZ (us-east-1a) for OCF as well
		firstAZ := provider.getAvailabilityZone(0)
		assert.Equal(t, "us-east-1a", firstAZ, "ocf BOSH must use first AZ (us-east-1a)")

		// Verify it's consistent across multiple calls
		for i := 0; i < 5; i++ {
			az := provider.getAvailabilityZone(0)
			assert.Equal(t, "us-east-1a", az, "ocf BOSH AZ must be consistent")
		}
	})

	t.Run("subnet_az_distribution", func(t *testing.T) {
		// Verify proper distribution across all subnets
		expectedAZs := []string{"us-east-1a", "us-east-1b", "us-east-1c"}
		for i := 0; i < 3; i++ {
			az := provider.getAvailabilityZone(i)
			assert.Equal(t, expectedAZs[i], az,
				"Subnet %d should use AZ %s", i, expectedAZs[i])
		}
	})
}

// TestBothProviders_AZConsistency_RegressionTest is a regression test for the bug
// where Go map iteration caused random AZ selection.
func TestBothProviders_AZConsistency_RegressionTest(t *testing.T) {
	t.Run("stackit_provider_regression", func(t *testing.T) {
		cfg := &config.Config{
			Name:     "test-bloc",
			Provider: "stackit",
			Region:   "eu01",
			AZs: map[string]config.AvailabilityZone{
				"eu01-3": {Zone: "eu01-3"},
				"eu01-1": {Zone: "eu01-1"},
				"eu01-2": {Zone: "eu01-2"},
			},
		}

		provider := &StackitVaultProvider{
			BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
		}

		// Simulate multiple vault populate runs - should always return same result
		results := make(map[string]int)
		for i := 0; i < 100; i++ {
			az := provider.getAvailabilityZone(0)
			results[az]++
		}

		// Should only have ONE unique result (eu01-1)
		require.Len(t, results, 1, "Should only return one AZ consistently")
		assert.Equal(t, 100, results["eu01-1"], "All 100 runs should return eu01-1")
		assert.Equal(t, 0, results["eu01-2"], "Should never return eu01-2")
		assert.Equal(t, 0, results["eu01-3"], "Should never return eu01-3")
	})

	t.Run("aws_provider_regression", func(t *testing.T) {
		cfg := &config.Config{
			Name:     "test-bloc",
			Provider: "aws",
			Region:   "us-east-1",
			AZs: map[string]config.AvailabilityZone{
				"us-east-1c": {Zone: "us-east-1c"},
				"us-east-1a": {Zone: "us-east-1a"},
				"us-east-1b": {Zone: "us-east-1b"},
			},
		}

		provider := &AWSVaultProvider{
			BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
		}

		// Simulate multiple vault populate runs - should always return same result
		results := make(map[string]int)
		for i := 0; i < 100; i++ {
			az := provider.getAvailabilityZone(0)
			results[az]++
		}

		// Should only have ONE unique result (us-east-1a)
		require.Len(t, results, 1, "Should only return one AZ consistently")
		assert.Equal(t, 100, results["us-east-1a"], "All 100 runs should return us-east-1a")
		assert.Equal(t, 0, results["us-east-1b"], "Should never return us-east-1b")
		assert.Equal(t, 0, results["us-east-1c"], "Should never return us-east-1c")
	})
}
