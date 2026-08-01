package reservedip_test

import (
	"errors"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/reservedip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCIDR(t *testing.T) {
	baseIP, bits, err := reservedip.ParseCIDR("10.10.1.0/24")
	require.NoError(t, err)
	assert.Equal(t, "10.10.1.0", baseIP)
	assert.Equal(t, 24, bits)

	_, _, err = reservedip.ParseCIDR("10.10.1.0")
	assert.Error(t, err, "missing prefix length must error")

	_, _, err = reservedip.ParseCIDR("10.10.1/24")
	assert.Error(t, err, "malformed IP must error")
}

func TestAddOffsetToIP(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		offset int
		want   string
	}{
		{"within subnet", "10.10.1.0", 5, "10.10.1.5"},
		{"overflow to next subnet", "10.10.1.200", 100, "10.10.2.44"},
		{"zero offset", "192.168.1.0", 0, "192.168.1.0"},
		{"large offset", "172.16.0.0", 300, "172.16.1.44"},
		{"negative offset borrows from third octet", "10.10.2.0", -1, "10.10.1.255"},
		{"malformed base returned unchanged", "not-an-ip", 5, "not-an-ip"},
		{"crosses two octet boundaries", "10.64.255.250", 20, "10.65.0.14"},
		// Out-of-representable-range results return baseIP unchanged (NOT ""
		// like the retired pveOffsetIP did — AddOffsetToIP's malformed-input
		// and out-of-range contracts share one "return baseIP unchanged"
		// behavior, documented on the function).
		{"negative offset underflows past the address space", "0.0.0.5", -10, "0.0.0.5"},
		{"positive offset overflows past the address space", "255.255.255.255", 1, "255.255.255.255"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, reservedip.AddOffsetToIP(tt.base, tt.offset))
		})
	}
}

func TestCalculateLastHostOffset(t *testing.T) {
	assert.Equal(t, 254, reservedip.CalculateLastHostOffset(24))
	assert.Equal(t, 62, reservedip.CalculateLastHostOffset(26))
	assert.Equal(t, 65534, reservedip.CalculateLastHostOffset(16))
}

func TestParseIPRangeSpec(t *testing.T) {
	ranges, err := reservedip.ParseIPRangeSpec("0-10,30->", "10.10.1.0", 24)
	require.NoError(t, err)
	require.Len(t, ranges, 2)
	assert.Equal(t, reservedip.IPRange{Start: "10.10.1.0", End: "10.10.1.10"}, ranges[0])
	assert.Equal(t, reservedip.IPRange{Start: "10.10.1.30", End: "10.10.1.254"}, ranges[1])

	_, err = reservedip.ParseIPRangeSpec("garbage", "10.10.1.0", 24)
	assert.Error(t, err, "non-numeric range spec must error rather than silently defaulting to 0")
}

func TestContainsInt(t *testing.T) {
	assert.True(t, reservedip.ContainsInt([]int{0, 1, 2}, 1))
	assert.False(t, reservedip.ContainsInt([]int{0, 1, 2}, 5))
	assert.False(t, reservedip.ContainsInt(nil, 0))
}

func TestSortAssignmentTypes(t *testing.T) {
	priority := map[string]int{"bosh": 1, "vault": 2, "available": 12, "reserved": 13} //nolint:mnd

	types := []string{"reserved", "available", "bosh", "vault"}
	reservedip.SortAssignmentTypes(types, priority)
	assert.Equal(t, []string{"bosh", "vault", "available", "reserved"}, types)

	// Unlisted types sort alphabetically after every prioritized type.
	types = []string{"zzz", "bosh", "aaa"}
	reservedip.SortAssignmentTypes(types, priority)
	assert.Equal(t, []string{"bosh", "aaa", "zzz"}, types)
}

func TestCalculate_OffsetAndRangeAssignments(t *testing.T) {
	assignments := reservedip.AssignmentTable{
		"bosh": {
			"mgmt": {Offset: 4},
			"ocf":  {Offset: 64},
		},
		"available": {
			"mgmt": {RangeSpec: "32-63"},
			"ocf":  {RangeSpec: "96->"},
		},
		"reserved": {
			"mgmt": {RangeSpec: "0-31,64->"},
			"ocf":  {RangeSpec: "0-95"},
		},
	}
	priority := map[string]int{"bosh": 1, "available": 2, "reserved": 3} //nolint:mnd

	mgmt, err := reservedip.Calculate("10.64.64.0/22", assignments, "mgmt", 0, priority, nil)
	require.NoError(t, err)
	ocf, err := reservedip.Calculate("10.64.64.0/22", assignments, "ocf", 0, priority, nil)
	require.NoError(t, err)

	assert.Equal(t, "10.64.64.4", mgmt["bosh_ip"])
	assert.Equal(t, "10.64.64.64", ocf["bosh_ip"])
	assert.NotEqual(t, mgmt["bosh_ip"], ocf["bosh_ip"], "mgmt and ocf bosh_ip must be disjoint on a shared subnet")

	assert.Equal(t, "10.64.64.32", mgmt["available_0"])
	assert.Equal(t, "10.64.64.63", mgmt["available_1"])
	assert.Equal(t, "10.64.64.96", ocf["available_0"])
	assert.Equal(t, "10.64.67.254", ocf["available_1"], "open-ended range resolves to the /22's last usable host")
}

func TestCalculate_SubnetMappingHonorsSubnetNum(t *testing.T) {
	assignments := reservedip.AssignmentTable{
		"doomsday": {
			"mgmt": {SubnetMapping: map[int][]int{9: {1}}},
		},
	}

	subnet0, err := reservedip.Calculate("10.10.1.0/24", assignments, "mgmt", 0, nil, nil)
	require.NoError(t, err)
	assert.NotContains(t, subnet0, "doomsday_ip")

	subnet1, err := reservedip.Calculate("10.10.1.0/24", assignments, "mgmt", 1, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "10.10.1.9", subnet1["doomsday_ip"])
}

func TestCalculate_UnknownEnvTypeFallsBackToOther(t *testing.T) {
	assignments := reservedip.AssignmentTable{
		"bosh": {
			"other": {Offset: 10},
		},
	}

	got, err := reservedip.Calculate("10.10.1.0/24", assignments, "some-unlisted-env", 0, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "10.10.1.10", got["bosh_ip"])
}

func TestCalculate_InvalidCIDRErrors(t *testing.T) {
	_, err := reservedip.Calculate("not-a-cidr", nil, "mgmt", 0, nil, nil)
	assert.Error(t, err)
}

func TestCalculate_OffsetAssignmentWithIPKeyOverride(t *testing.T) {
	assignments := reservedip.AssignmentTable{
		"rustfs": {"mgmt": {Offset: 14}}, //nolint:mnd
		"rustfs_smoke": {
			"mgmt": {Offset: 21, IPKey: "rustfs_ip_smoke"}, //nolint:mnd
		},
	}

	got, err := reservedip.Calculate("10.64.64.0/22", assignments, "mgmt", 0, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, "10.64.64.14", got["rustfs_ip"], "default key is unaffected by an unrelated assignment's IPKey")
	assert.Equal(t, "10.64.64.21", got["rustfs_ip_smoke"], "IPKey overrides the default {assignmentType}_ip key")
	assert.NotContains(t, got, "rustfs_smoke_ip", "the default-shaped key must not also be written when IPKey is set")
	assert.Equal(t, "10.64.64.20", got["rustfs_ip_smoke_a"], "bound keys key off IPKey, not assignmentType, when IPKey is set")
	assert.Equal(t, "10.64.64.22", got["rustfs_ip_smoke_b"])
}

func TestCalculate_ErrOffsetBeyondSubnet(t *testing.T) {
	t.Run("out-of-range offset", func(t *testing.T) {
		// /24 -> last usable host offset 254; a bare Offset assignment of 300
		// is past the subnet.
		assignments := reservedip.AssignmentTable{
			"bosh": {"mgmt": {Offset: 300}}, //nolint:mnd
		}

		got, err := reservedip.Calculate("10.10.1.0/24", assignments, "mgmt", 0, nil, nil)
		require.Error(t, err)
		assert.Truef(t, errors.Is(err, reservedip.ErrOffsetBeyondSubnet), "error %v must be errors.Is-able to ErrOffsetBeyondSubnet", err)
		assert.Contains(t, err.Error(), "300", "message must include the offending offset")
		assert.Contains(t, err.Error(), "254", "message must include the subnet's last usable host offset")
		assert.Nil(t, got)
	})

	t.Run("out-of-range range end", func(t *testing.T) {
		// /24 -> last usable host offset 254; a RangeSpec ending at 300 is
		// past the subnet even though its start (10) is in bounds.
		assignments := reservedip.AssignmentTable{
			"available": {"mgmt": {RangeSpec: "10-300"}},
		}

		got, err := reservedip.Calculate("10.10.1.0/24", assignments, "mgmt", 0, nil, nil)
		require.Error(t, err)
		assert.Truef(t, errors.Is(err, reservedip.ErrOffsetBeyondSubnet), "error %v must be errors.Is-able to ErrOffsetBeyondSubnet", err)
		assert.Contains(t, err.Error(), "300", "message must include the offending range endpoint")
		assert.Contains(t, err.Error(), "254", "message must include the subnet's last usable host offset")
		assert.Nil(t, got)
	})

	t.Run("inverted range end before start", func(t *testing.T) {
		// Both endpoints are within the /24's bounds, but end (10) precedes
		// start (30) — invalid regardless of the subnet's size.
		assignments := reservedip.AssignmentTable{
			"available": {"mgmt": {RangeSpec: "30-10"}},
		}

		got, err := reservedip.Calculate("10.10.1.0/24", assignments, "mgmt", 0, nil, nil)
		require.Error(t, err)
		assert.Truef(t, errors.Is(err, reservedip.ErrOffsetBeyondSubnet), "error %v must be errors.Is-able to ErrOffsetBeyondSubnet", err)
		assert.Contains(t, err.Error(), "30", "message must include the range's start offset")
		assert.Contains(t, err.Error(), "10", "message must include the range's end offset")
		assert.Nil(t, got)
	})

	t.Run("out-of-range SubnetMapping offset", func(t *testing.T) {
		// SubnetMapping resolves an offset just like a bare Offset does; the
		// bounds check must apply to the entry matching subnetNum.
		assignments := reservedip.AssignmentTable{
			"doomsday": {"mgmt": {SubnetMapping: map[int][]int{300: {1}}}}, //nolint:mnd
		}

		got, err := reservedip.Calculate("10.10.1.0/24", assignments, "mgmt", 1, nil, nil)
		require.Error(t, err)
		assert.Truef(t, errors.Is(err, reservedip.ErrOffsetBeyondSubnet), "error %v must be errors.Is-able to ErrOffsetBeyondSubnet", err)
		assert.Nil(t, got)
	})

	t.Run("out-of-range SubnetRanges endpoint", func(t *testing.T) {
		assignments := reservedip.AssignmentTable{
			"reserved": {"mgmt": {SubnetRanges: map[string][]int{"10-300": {1}}}},
		}

		got, err := reservedip.Calculate("10.10.1.0/24", assignments, "mgmt", 1, nil, nil)
		require.Error(t, err)
		assert.Truef(t, errors.Is(err, reservedip.ErrOffsetBeyondSubnet), "error %v must be errors.Is-able to ErrOffsetBeyondSubnet", err)
		assert.Nil(t, got)
	})

	t.Run("malformed range spec still non-fatal, not confused with bounds error", func(t *testing.T) {
		// A non-numeric spec must keep the pre-existing skip-and-warn
		// behavior — it must not be reported as ErrOffsetBeyondSubnet.
		assignments := reservedip.AssignmentTable{
			"available": {"mgmt": {RangeSpec: "not-numeric"}},
		}

		got, err := reservedip.Calculate("10.10.1.0/24", assignments, "mgmt", 0, nil, nil)
		require.NoError(t, err)
		assert.NotContains(t, got, "available_0")
	})
}

func TestCalculate_MalformedRangeSpecSkippedNotFatal(t *testing.T) {
	assignments := reservedip.AssignmentTable{
		"available": {
			"mgmt": {RangeSpec: "not-numeric"},
		},
		"bosh": {
			"mgmt": {Offset: 4},
		},
	}

	got, err := reservedip.Calculate("10.10.1.0/24", assignments, "mgmt", 0, nil, nil)
	require.NoError(t, err, "a malformed range spec must be skipped, not fail the whole subnet")
	assert.NotContains(t, got, "available_0")
	assert.Equal(t, "10.10.1.4", got["bosh_ip"])
}
