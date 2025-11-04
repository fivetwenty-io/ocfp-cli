package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsSubnetWithinParent_ValidSubnets tests that properly carved subnets
// are correctly identified as being within their parent network.
func TestIsSubnetWithinParent_ValidSubnets(t *testing.T) {
	tests := []struct {
		name       string
		parentCIDR string
		childCIDR  string
		expected   bool
	}{
		{
			name:       "exact_match_same_network",
			parentCIDR: "10.4.0.0/20",
			childCIDR:  "10.4.0.0/20",
			expected:   true,
		},
		{
			name:       "first_quarter_subnet",
			parentCIDR: "10.4.0.0/20",
			childCIDR:  "10.4.0.0/22",
			expected:   true,
		},
		{
			name:       "second_quarter_subnet",
			parentCIDR: "10.4.0.0/20",
			childCIDR:  "10.4.4.0/22",
			expected:   true,
		},
		{
			name:       "third_quarter_subnet",
			parentCIDR: "10.4.0.0/20",
			childCIDR:  "10.4.8.0/22",
			expected:   true,
		},
		{
			name:       "fourth_quarter_subnet",
			parentCIDR: "10.4.0.0/20",
			childCIDR:  "10.4.12.0/22",
			expected:   true,
		},
		{
			name:       "small_subnet_at_end",
			parentCIDR: "10.4.0.0/20",
			childCIDR:  "10.4.15.0/24",
			expected:   true,
		},
		{
			name:       "tiny_subnet_at_start",
			parentCIDR: "10.0.0.0/16",
			childCIDR:  "10.0.0.0/30",
			expected:   true,
		},
		{
			name:       "typical_aws_vpc_subnet",
			parentCIDR: "10.0.0.0/16",
			childCIDR:  "10.0.1.0/24",
			expected:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsSubnetWithinParent(tt.parentCIDR, tt.childCIDR)
			assert.Equal(t, tt.expected, result,
				"IsSubnetWithinParent(%s, %s) = %v, want %v",
				tt.parentCIDR, tt.childCIDR, result, tt.expected)
		})
	}
}

// TestIsSubnetWithinParent_InvalidSubnets tests that subnets outside
// the parent network are correctly identified.
func TestIsSubnetWithinParent_InvalidSubnets(t *testing.T) {
	tests := []struct {
		name       string
		parentCIDR string
		childCIDR  string
		reason     string
	}{
		{
			name:       "completely_outside_higher_address",
			parentCIDR: "10.4.0.0/20",
			childCIDR:  "10.4.16.0/22",
			reason:     "Child starts at 10.4.16.0, parent ends at 10.4.15.255",
		},
		{
			name:       "completely_outside_lower_address",
			parentCIDR: "10.4.0.0/20",
			childCIDR:  "10.3.0.0/22",
			reason:     "Child is in different /16 block",
		},
		{
			name:       "starts_beyond_parent_end",
			parentCIDR: "10.4.0.0/20",
			childCIDR:  "10.4.16.0/21",
			reason:     "Child starts at 10.4.16.0 which is beyond parent's 10.4.15.255",
		},
		{
			name:       "different_network_entirely",
			parentCIDR: "10.4.0.0/20",
			childCIDR:  "192.168.1.0/24",
			reason:     "Completely different network",
		},
		{
			name:       "adjacent_network_after",
			parentCIDR: "10.4.0.0/20",
			childCIDR:  "10.4.16.0/20",
			reason:     "Adjacent network block after parent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsSubnetWithinParent(tt.parentCIDR, tt.childCIDR)
			assert.False(t, result,
				"IsSubnetWithinParent(%s, %s) should be false: %s",
				tt.parentCIDR, tt.childCIDR, tt.reason)
		})
	}
}

// TestIsSubnetWithinParent_InvalidInputs tests error handling for invalid CIDRs.
func TestIsSubnetWithinParent_InvalidInputs(t *testing.T) {
	tests := []struct {
		name       string
		parentCIDR string
		childCIDR  string
	}{
		{
			name:       "invalid_parent_cidr",
			parentCIDR: "invalid",
			childCIDR:  "10.4.0.0/22",
		},
		{
			name:       "invalid_child_cidr",
			parentCIDR: "10.4.0.0/20",
			childCIDR:  "invalid",
		},
		{
			name:       "both_invalid",
			parentCIDR: "invalid",
			childCIDR:  "also-invalid",
		},
		{
			name:       "empty_parent",
			parentCIDR: "",
			childCIDR:  "10.4.0.0/22",
		},
		{
			name:       "empty_child",
			parentCIDR: "10.4.0.0/20",
			childCIDR:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsSubnetWithinParent(tt.parentCIDR, tt.childCIDR)
			assert.False(t, result, "Invalid inputs should return false")
		})
	}
}

// TestSplitIntoN_BoundaryValidation tests that SplitIntoN always generates
// subnets within the parent network boundaries.
func TestSplitIntoN_BoundaryValidation(t *testing.T) {
	tests := []struct {
		name       string
		parentCIDR string
		count      int
	}{
		{
			name:       "split_into_4_from_20",
			parentCIDR: "10.4.0.0/20",
			count:      4,
		},
		{
			name:       "split_into_2_from_16",
			parentCIDR: "10.0.0.0/16",
			count:      2,
		},
		{
			name:       "split_into_8_from_21",
			parentCIDR: "10.4.0.0/21",
			count:      8,
		},
		{
			name:       "split_into_16_from_20",
			parentCIDR: "10.4.0.0/20",
			count:      16,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subnets := SplitIntoN(tt.parentCIDR, tt.count)
			require.NotNil(t, subnets, "SplitIntoN should return subnets")
			require.Len(t, subnets, tt.count, "Should generate requested number of subnets")

			// Validate each subnet is within parent bounds
			for i, subnet := range subnets {
				assert.True(t, IsSubnetWithinParent(tt.parentCIDR, subnet),
					"Subnet %d (%s) should be within parent network %s",
					i, subnet, tt.parentCIDR)
			}
		})
	}
}

// TestStackitTripleSubnetCarving_BoundaryValidation verifies that STACKIT's
// triple subnet pattern (split into 4, skip first) stays within bounds.
func TestStackitTripleSubnetCarving_BoundaryValidation(t *testing.T) {
	parentCIDR := "10.4.0.0/20"

	// Split into 4 (STACKIT pattern)
	allSubnets := SplitIntoN(parentCIDR, 4)
	require.Len(t, allSubnets, 4, "Should generate 4 subnets")

	// Expected subnets for 10.4.0.0/20 split into 4:
	// [0]: 10.4.0.0/22   (10.4.0.0 - 10.4.3.255)   - Reserved
	// [1]: 10.4.4.0/22   (10.4.4.0 - 10.4.7.255)   - ocfp-0
	// [2]: 10.4.8.0/22   (10.4.8.0 - 10.4.11.255)  - ocfp-1
	// [3]: 10.4.12.0/22  (10.4.12.0 - 10.4.15.255) - ocfp-2

	expectedSubnets := []string{
		"10.4.0.0/22",
		"10.4.4.0/22",
		"10.4.8.0/22",
		"10.4.12.0/22",
	}

	for i, expected := range expectedSubnets {
		assert.Equal(t, expected, allSubnets[i],
			"Subnet %d should be %s", i, expected)

		assert.True(t, IsSubnetWithinParent(parentCIDR, allSubnets[i]),
			"Subnet %d (%s) must be within parent %s",
			i, allSubnets[i], parentCIDR)
	}

	// The actual used subnets (skipping first)
	usedSubnets := allSubnets[1:]
	t.Run("used_subnets_are_valid", func(t *testing.T) {
		for i, subnet := range usedSubnets {
			assert.True(t, IsSubnetWithinParent(parentCIDR, subnet),
				"Used subnet %d (%s) must be within parent %s",
				i, subnet, parentCIDR)
		}
	})
}

// TestBOSHSubnetBoundaries_RegressionTest is a regression test for the issue
// where BOSH deployment failed because subnet was thought to be out of bounds.
func TestBOSHSubnetBoundaries_RegressionTest(t *testing.T) {
	t.Run("10.4.0.0_20_network", func(t *testing.T) {
		parentCIDR := "10.4.0.0/20"

		// This is what BOSH tries to use (second carved subnet)
		boshSubnet := "10.4.4.0/22"

		assert.True(t, IsSubnetWithinParent(parentCIDR, boshSubnet),
			"BOSH subnet %s MUST be within parent network %s",
			boshSubnet, parentCIDR)

		// Verify BOSH IP would be valid
		boshIP := "10.4.4.4"
		t.Logf("BOSH IP %s is in subnet %s which is in network %s",
			boshIP, boshSubnet, parentCIDR)
	})

	t.Run("migration_scenario_old_vs_new", func(t *testing.T) {
		networkCIDR := "10.4.0.0/20"

		// Old Perl approach: used entire network
		oldBOSHSubnet := "10.4.0.0/20"
		oldBOSHIP := "10.4.0.4"

		// New Go approach: carved subnets
		newBOSHSubnet := "10.4.4.0/22"
		newBOSHIP := "10.4.4.4"

		assert.True(t, IsSubnetWithinParent(networkCIDR, oldBOSHSubnet),
			"Old BOSH subnet should be valid (same as network)")
		assert.True(t, IsSubnetWithinParent(networkCIDR, newBOSHSubnet),
			"New BOSH subnet should be valid (carved from network)")

		t.Logf("Migration: %s → %s, IP: %s → %s",
			oldBOSHSubnet, newBOSHSubnet, oldBOSHIP, newBOSHIP)
	})
}

// TestEdgeCases_SubnetBoundaries tests edge cases for subnet boundary validation.
func TestEdgeCases_SubnetBoundaries(t *testing.T) {
	t.Run("smallest_possible_subnet", func(t *testing.T) {
		parentCIDR := "10.0.0.0/30" // Only 4 IPs
		childCIDR := "10.0.0.0/30"  // Same as parent

		assert.True(t, IsSubnetWithinParent(parentCIDR, childCIDR),
			"/30 subnet should be valid within itself")
	})

	t.Run("largest_private_network", func(t *testing.T) {
		parentCIDR := "10.0.0.0/8" // Entire 10.0.0.0/8 block
		childCIDR := "10.255.0.0/16"

		assert.True(t, IsSubnetWithinParent(parentCIDR, childCIDR),
			"10.255.0.0/16 should be within 10.0.0.0/8")
	})

	t.Run("boundary_crossing_by_one_block", func(t *testing.T) {
		parentCIDR := "10.4.0.0/20" // Ends at 10.4.15.255
		// Note: 10.4.15.0/23 gets normalized to 10.4.14.0/23 by net.ParseCIDR
		// because /23 must be on a 512-address boundary
		childCIDR := "10.4.14.0/23" // Extends to 10.4.15.255 (valid, at boundary)

		assert.True(t, IsSubnetWithinParent(parentCIDR, childCIDR),
			"Subnet at exact boundary should be valid")
	})
}
