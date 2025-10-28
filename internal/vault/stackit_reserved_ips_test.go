package vault

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseCIDR tests CIDR parsing functionality.
func TestParseCIDR(t *testing.T) {
	tests := []struct {
		name        string
		cidr        string
		wantBaseIP  string
		wantNetBits int
		wantErr     bool
	}{
		{
			name:        "valid /24 network",
			cidr:        "10.10.1.0/24",
			wantBaseIP:  "10.10.1.0",
			wantNetBits: 24,
			wantErr:     false,
		},
		{
			name:        "valid /16 network",
			cidr:        "192.168.0.0/16",
			wantBaseIP:  "192.168.0.0",
			wantNetBits: 16,
			wantErr:     false,
		},
		{
			name:        "valid /26 network",
			cidr:        "172.16.50.0/26",
			wantBaseIP:  "172.16.50.0",
			wantNetBits: 26,
			wantErr:     false,
		},
		{
			name:    "invalid - missing network bits",
			cidr:    "10.10.1.0",
			wantErr: true,
		},
		{
			name:    "invalid - bad IP format",
			cidr:    "10.10.1/24",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseIP, netBits, err := parseCIDR(tt.cidr)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantBaseIP, baseIP)
			assert.Equal(t, tt.wantNetBits, netBits)
		})
	}
}

// TestAddOffsetToIP tests IP offset calculation.
func TestAddOffsetToIP(t *testing.T) {
	tests := []struct {
		name   string
		baseIP string
		offset int
		want   string
	}{
		{
			name:   "simple offset within same subnet",
			baseIP: "10.10.1.0",
			offset: 5,
			want:   "10.10.1.5",
		},
		{
			name:   "offset to end of subnet",
			baseIP: "10.10.1.0",
			offset: 254,
			want:   "10.10.1.254",
		},
		{
			name:   "offset with overflow to next subnet",
			baseIP: "10.10.1.200",
			offset: 100,
			want:   "10.10.2.44",
		},
		{
			name:   "zero offset",
			baseIP: "192.168.1.0",
			offset: 0,
			want:   "192.168.1.0",
		},
		{
			name:   "large offset",
			baseIP: "172.16.0.0",
			offset: 300,
			want:   "172.16.1.44",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := addOffsetToIP(tt.baseIP, tt.offset)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestCalculateLastHostOffset tests last host offset calculation.
func TestCalculateLastHostOffset(t *testing.T) {
	tests := []struct {
		name        string
		networkBits int
		want        int
	}{
		{
			name:        "/24 network - 254 usable hosts",
			networkBits: 24,
			want:        254,
		},
		{
			name:        "/26 network - 62 usable hosts",
			networkBits: 26,
			want:        62,
		},
		{
			name:        "/16 network - 65534 usable hosts",
			networkBits: 16,
			want:        65534,
		},
		{
			name:        "/30 network - 2 usable hosts",
			networkBits: 30,
			want:        2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateLastHostOffset(tt.networkBits)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestParseIPRangeSpec tests IP range specification parsing.
func TestParseIPRangeSpec(t *testing.T) {
	tests := []struct {
		name        string
		rangeSpec   string
		baseIP      string
		networkBits int
		want        []ipRange
		wantErr     bool
	}{
		{
			name:        "simple range",
			rangeSpec:   "11-29",
			baseIP:      "10.10.1.0",
			networkBits: 24,
			want: []ipRange{
				{Start: "10.10.1.11", End: "10.10.1.29"},
			},
		},
		{
			name:        "multiple ranges",
			rangeSpec:   "0-10,30->",
			baseIP:      "10.10.1.0",
			networkBits: 24,
			want: []ipRange{
				{Start: "10.10.1.0", End: "10.10.1.10"},
				{Start: "10.10.1.30", End: "10.10.1.254"},
			},
		},
		{
			name:        "open-ended range",
			rangeSpec:   "38->",
			baseIP:      "192.168.1.0",
			networkBits: 24,
			want: []ipRange{
				{Start: "192.168.1.38", End: "192.168.1.254"},
			},
		},
		{
			name:        "single value",
			rangeSpec:   "5",
			baseIP:      "172.16.0.0",
			networkBits: 24,
			want: []ipRange{
				{Start: "172.16.0.5", End: "172.16.0.5"},
			},
		},
		{
			name:        "complex multi-range",
			rangeSpec:   "0-36",
			baseIP:      "10.20.30.0",
			networkBits: 24,
			want: []ipRange{
				{Start: "10.20.30.0", End: "10.20.30.36"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseIPRangeSpec(tt.rangeSpec, tt.baseIP, tt.networkBits)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, len(tt.want), len(got), "number of ranges mismatch")

			for i := range tt.want {
				assert.Equal(t, tt.want[i].Start, got[i].Start, "range %d start mismatch", i)
				assert.Equal(t, tt.want[i].End, got[i].End, "range %d end mismatch", i)
			}
		})
	}
}

// TestContainsInt tests integer slice containment.
func TestContainsInt(t *testing.T) {
	tests := []struct {
		name  string
		slice []int
		value int
		want  bool
	}{
		{
			name:  "contains value",
			slice: []int{0, 1, 2},
			value: 1,
			want:  true,
		},
		{
			name:  "does not contain value",
			slice: []int{0, 1, 2},
			value: 5,
			want:  false,
		},
		{
			name:  "empty slice",
			slice: []int{},
			value: 0,
			want:  false,
		},
		{
			name:  "contains first element",
			slice: []int{10, 20, 30},
			value: 10,
			want:  true,
		},
		{
			name:  "contains last element",
			slice: []int{10, 20, 30},
			value: 30,
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsInt(tt.slice, tt.value)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestSortAssignmentTypes tests assignment type sorting.
func TestSortAssignmentTypes(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "already sorted",
			input: []string{"bosh", "vault", "jumpbox"},
			want:  []string{"bosh", "vault", "jumpbox"},
		},
		{
			name:  "reverse order",
			input: []string{"reserved", "available", "bosh"},
			want:  []string{"bosh", "available", "reserved"},
		},
		{
			name:  "mixed order",
			input: []string{"shield", "bosh", "prometheus", "vault"},
			want:  []string{"bosh", "vault", "prometheus", "shield"},
		},
		{
			name:  "all types",
			input: []string{"reserved", "available", "blacksmith", "bastion", "ocfp_ui", "doomsday", "shield", "prometheus", "concourse", "jumpbox", "vault", "bosh"},
			want:  []string{"bosh", "vault", "jumpbox", "concourse", "prometheus", "shield", "doomsday", "ocfp_ui", "bastion", "blacksmith", "available", "reserved"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a copy to avoid modifying input
			input := make([]string, len(tt.input))
			copy(input, tt.input)

			sortAssignmentTypes(input)
			assert.Equal(t, tt.want, input)
		})
	}
}

// TestGetDefaultReservedIPAssignments tests the default IP assignment structure.
func TestGetDefaultReservedIPAssignments(t *testing.T) {
	assignments := getDefaultReservedIPAssignments()

	// Test that all expected systems are present
	expectedSystems := []string{
		"bosh", "vault", "jumpbox", "concourse", "prometheus",
		"shield", "doomsday", "ocfp_ui", "bastion", "blacksmith",
		"available", "reserved",
	}

	for _, system := range expectedSystems {
		t.Run("system_"+system, func(t *testing.T) {
			assert.Contains(t, assignments, system, "missing system: %s", system)
		})
	}

	// Test specific assignment structures
	t.Run("bosh_mgmt_offset", func(t *testing.T) {
		bosh := assignments["bosh"]["mgmt"]
		require.NotNil(t, bosh)
		require.NotNil(t, bosh.SubnetMapping)
		assert.Contains(t, bosh.SubnetMapping, 4)
		assert.Equal(t, []int{0}, bosh.SubnetMapping[4])
	})

	t.Run("vault_simple_offset", func(t *testing.T) {
		vault := assignments["vault"]["mgmt"]
		require.NotNil(t, vault)
		assert.Equal(t, 5, vault.Offset)
	})

	t.Run("available_range_spec", func(t *testing.T) {
		available := assignments["available"]["mgmt"]
		require.NotNil(t, available)
		assert.Equal(t, "11-29", available.RangeSpec)
	})

	t.Run("reserved_multi_range", func(t *testing.T) {
		reserved := assignments["reserved"]["mgmt"]
		require.NotNil(t, reserved)
		assert.Equal(t, "0-10,30->", reserved.RangeSpec)
	})
}

// TestCalculateReservedIPs_MgmtSubnet tests mgmt environment IP allocation.
func TestCalculateReservedIPs_MgmtSubnet(t *testing.T) {
	provider := &StackitVaultProvider{}
	assignments := getDefaultReservedIPAssignments()

	cidr := "10.10.1.0/24"
	envType := "mgmt"
	subnetNum := 0

	vaultIPs, err := provider.calculateReservedIPs(cidr, assignments, envType, subnetNum)
	require.NoError(t, err)
	require.NotNil(t, vaultIPs)

	// Test specific IP assignments for mgmt-0
	t.Run("bastion_ip", func(t *testing.T) {
		assert.Equal(t, "10.10.1.3", vaultIPs["bastion_ip"])
	})

	t.Run("bosh_ip", func(t *testing.T) {
		assert.Equal(t, "10.10.1.4", vaultIPs["bosh_ip"])
	})

	t.Run("vault_ip", func(t *testing.T) {
		assert.Equal(t, "10.10.1.5", vaultIPs["vault_ip"])
	})

	t.Run("jumpbox_ip", func(t *testing.T) {
		assert.Equal(t, "10.10.1.6", vaultIPs["jumpbox_ip"])
	})

	t.Run("concourse_ip", func(t *testing.T) {
		assert.Equal(t, "10.10.1.7", vaultIPs["concourse_ip"])
	})

	t.Run("prometheus_ip", func(t *testing.T) {
		assert.Equal(t, "10.10.1.8", vaultIPs["prometheus_ip"])
	})

	t.Run("shield_ip", func(t *testing.T) {
		assert.Equal(t, "10.10.1.9", vaultIPs["shield_ip"])
	})

	t.Run("blacksmith_ip", func(t *testing.T) {
		assert.Equal(t, "10.10.1.10", vaultIPs["blacksmith_ip"])
	})

	// Test available range
	t.Run("available_range", func(t *testing.T) {
		assert.Equal(t, "10.10.1.11", vaultIPs["available_0"])
		assert.Equal(t, "10.10.1.29", vaultIPs["available_1"])
	})

	// Test reserved ranges
	t.Run("reserved_ranges", func(t *testing.T) {
		assert.Equal(t, "10.10.1.0", vaultIPs["reserved_0"])
		assert.Equal(t, "10.10.1.10", vaultIPs["reserved_1"])
		assert.Equal(t, "10.10.1.30", vaultIPs["reserved_2"])
		assert.Equal(t, "10.10.1.254", vaultIPs["reserved_3"])
	})
}

// TestCalculateReservedIPs_MgmtSubnet1 tests mgmt environment subnet 1.
func TestCalculateReservedIPs_MgmtSubnet1(t *testing.T) {
	provider := &StackitVaultProvider{}
	assignments := getDefaultReservedIPAssignments()

	cidr := "10.10.2.0/24"
	envType := "mgmt"
	subnetNum := 1

	vaultIPs, err := provider.calculateReservedIPs(cidr, assignments, envType, subnetNum)
	require.NoError(t, err)
	require.NotNil(t, vaultIPs)

	// For subnet 1, doomsday should be assigned at offset 9
	t.Run("doomsday_ip", func(t *testing.T) {
		assert.Equal(t, "10.10.2.9", vaultIPs["doomsday_ip"])
	})

	// Other simple offsets should still work
	t.Run("vault_ip", func(t *testing.T) {
		assert.Equal(t, "10.10.2.5", vaultIPs["vault_ip"])
	})
}

// TestCalculateReservedIPs_OCFSubnet tests ocf environment IP allocation.
func TestCalculateReservedIPs_OCFSubnet(t *testing.T) {
	provider := &StackitVaultProvider{}
	assignments := getDefaultReservedIPAssignments()

	cidr := "10.20.1.0/24"
	envType := "ocf"
	subnetNum := 0

	vaultIPs, err := provider.calculateReservedIPs(cidr, assignments, envType, subnetNum)
	require.NoError(t, err)
	require.NotNil(t, vaultIPs)

	// Test OCF-specific IP assignments for ocfp-0
	t.Run("bosh_ip", func(t *testing.T) {
		assert.Equal(t, "10.20.1.31", vaultIPs["bosh_ip"])
	})

	t.Run("vault_ip", func(t *testing.T) {
		assert.Equal(t, "10.20.1.32", vaultIPs["vault_ip"])
	})

	t.Run("jumpbox_ip", func(t *testing.T) {
		assert.Equal(t, "10.20.1.33", vaultIPs["jumpbox_ip"])
	})

	t.Run("concourse_ip", func(t *testing.T) {
		assert.Equal(t, "10.20.1.34", vaultIPs["concourse_ip"])
	})

	t.Run("prometheus_ip", func(t *testing.T) {
		assert.Equal(t, "10.20.1.35", vaultIPs["prometheus_ip"])
	})

	t.Run("shield_ip", func(t *testing.T) {
		assert.Equal(t, "10.20.1.36", vaultIPs["shield_ip"])
	})

	t.Run("bastion_ip", func(t *testing.T) {
		assert.Equal(t, "10.20.1.37", vaultIPs["bastion_ip"])
	})

	// Test available range for OCF subnet 0
	t.Run("available_range", func(t *testing.T) {
		assert.Equal(t, "10.20.1.38", vaultIPs["available_0"])
		assert.Equal(t, "10.20.1.254", vaultIPs["available_1"])
	})

	// Test reserved range for OCF subnet 0
	t.Run("reserved_range", func(t *testing.T) {
		assert.Equal(t, "10.20.1.0", vaultIPs["reserved_0"])
		assert.Equal(t, "10.20.1.37", vaultIPs["reserved_1"])
	})
}

// TestCalculateReservedIPs_OCFSubnet1 tests ocf environment subnet 1.
func TestCalculateReservedIPs_OCFSubnet1(t *testing.T) {
	provider := &StackitVaultProvider{}
	assignments := getDefaultReservedIPAssignments()

	cidr := "10.20.2.0/24"
	envType := "ocf"
	subnetNum := 1

	vaultIPs, err := provider.calculateReservedIPs(cidr, assignments, envType, subnetNum)
	require.NoError(t, err)
	require.NotNil(t, vaultIPs)

	// For OCF subnet 1, blacksmith should be at offset 36
	t.Run("blacksmith_ip", func(t *testing.T) {
		assert.Equal(t, "10.20.2.36", vaultIPs["blacksmith_ip"])
	})

	// Test available range for OCF subnet 1
	t.Run("available_range", func(t *testing.T) {
		assert.Equal(t, "10.20.2.37", vaultIPs["available_0"])
		assert.Equal(t, "10.20.2.254", vaultIPs["available_1"])
	})

	// Test reserved range for OCF subnet 1
	t.Run("reserved_range", func(t *testing.T) {
		assert.Equal(t, "10.20.2.0", vaultIPs["reserved_0"])
		assert.Equal(t, "10.20.2.36", vaultIPs["reserved_1"])
	})
}

// TestCalculateReservedIPs_DifferentCIDRs tests various CIDR ranges.
func TestCalculateReservedIPs_DifferentCIDRs(t *testing.T) {
	provider := &StackitVaultProvider{}
	assignments := getDefaultReservedIPAssignments()

	tests := []struct {
		name      string
		cidr      string
		envType   string
		subnetNum int
		checkIP   string
		checkKey  string
	}{
		{
			name:      "192.168.x.x network",
			cidr:      "192.168.50.0/24",
			envType:   "mgmt",
			subnetNum: 0,
			checkKey:  "bosh_ip",
			checkIP:   "192.168.50.4",
		},
		{
			name:      "172.16.x.x network",
			cidr:      "172.16.100.0/24",
			envType:   "mgmt",
			subnetNum: 0,
			checkKey:  "vault_ip",
			checkIP:   "172.16.100.5",
		},
		{
			name:      "/26 smaller subnet",
			cidr:      "10.0.0.0/26",
			envType:   "mgmt",
			subnetNum: 0,
			checkKey:  "jumpbox_ip",
			checkIP:   "10.0.0.6",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vaultIPs, err := provider.calculateReservedIPs(tt.cidr, assignments, tt.envType, tt.subnetNum)
			require.NoError(t, err)
			assert.Equal(t, tt.checkIP, vaultIPs[tt.checkKey])
		})
	}
}
