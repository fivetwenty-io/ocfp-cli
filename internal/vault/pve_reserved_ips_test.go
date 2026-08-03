package vault

import (
	"fmt"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
	"github.com/ocfp/ocfp-cli-go/internal/reservedip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPVEReservedIPs_MgmtOcfDisjoint is the core acceptance test for the
// tiered layout: mgmt and ocf must compute DIFFERENT, non-overlapping
// reserved-ips on the identical shared /22, eliminating the collision the
// plan documents (wayne bloc 2026-07-22: mgmt concourse allocated on top of
// live ocf cf VMs). Parametrized over every registered netlayout strategy —
// compact must uphold the same disjointness guarantee as wide, on its own
// (compressed) offsets.
func TestPVEReservedIPs_MgmtOcfDisjoint(t *testing.T) {
	t.Run("wide", testPVEReservedIPsMgmtOcfDisjointWide)
	t.Run("compact", testPVEReservedIPsMgmtOcfDisjointCompact)
}

func testPVEReservedIPsMgmtOcfDisjointWide(t *testing.T) {
	cidr := "10.64.64.0/22"

	mgmt, err := pveReservedIPsForSubnet(cidr, "mgmt", 0, config.NetworkConfig{}, logger.Get())
	require.NoError(t, err)

	ocf, err := pveReservedIPsForSubnet(cidr, "ocf", 0, config.NetworkConfig{}, logger.Get())
	require.NoError(t, err)

	assert.Equal(t, "10.64.64.4", mgmt["bosh_ip"])
	assert.Equal(t, "10.64.64.64", ocf["bosh_ip"])
	assert.NotEqual(t, mgmt["bosh_ip"], ocf["bosh_ip"])

	assert.Equal(t, "10.64.64.5", mgmt["vault_ip"])
	assert.Equal(t, "10.64.64.65", ocf["vault_ip"])

	assert.Equal(t, "10.64.64.6", mgmt["jumpbox_ip"])
	assert.Equal(t, "10.64.64.66", ocf["jumpbox_ip"])

	assert.Equal(t, "10.64.64.10", mgmt["blacksmith_ip"])
	assert.Equal(t, "10.64.64.67", ocf["blacksmith_ip"])

	// haproxy sits INSIDE the ocf available band (start+1): the cf kit derives
	// its subnet statics from the top of its band claim, and the manifest's
	// haproxy_ip must land inside that claim-derived static window.
	assert.Equal(t, "10.64.64.97", ocf["haproxy_ip"], "ocf haproxy: available band start + 1")
	assert.NotContains(t, mgmt, "haproxy_ip", "mgmt has no CF, no haproxy static")

	// mgmt-only named statics must never appear in ocf's tree.
	for _, key := range []string{"bastion_ip", "concourse_ip", "prometheus_ip", "shield_ip", "artifacts_ip",
		"wireguard_ip", "ovpn_ip", "rustfs_ip", "rustfs_ip_smoke", "proxycache_ip", "nfs_ip", "ocfp_ui_ip",
		"doomsday_ip", "shout_ip", "garage_ip", "garage_ip_smoke"} {
		assert.Contains(t, mgmt, key)
		assert.NotContains(t, ocf, key)
	}

	// doomsday/shout: kits/doomsday and kits/shout's ocfp.yml do an
	// unconditional (no-default) vault read of
	// net/subnets/ocfp-1/reserved-ips:doomsday_ip (and :shout_ip) — a
	// missing key FATALs the manifest merge. Assert they land inside the
	// mgmt reserved complement too (offsets 18/19, inside "0-31,64->").
	assert.Equal(t, "10.64.64.18", mgmt["doomsday_ip"])
	assert.Equal(t, "10.64.64.19", mgmt["shout_ip"])

	// rustfs/garage are alternative blobstore implementations (only one
	// deploys per bloc) so they must never collide on the same offset.
	assert.Equal(t, "10.64.64.14", mgmt["rustfs_ip"])
	assert.Equal(t, "10.64.64.20", mgmt["garage_ip"])
	assert.NotEqual(t, mgmt["rustfs_ip"], mgmt["garage_ip"])
	assert.Equal(t, "10.64.64.21", mgmt["rustfs_ip_smoke"])
	assert.Equal(t, "10.64.64.22", mgmt["garage_ip_smoke"])

	// available/reserved bands must not overlap between tiers.
	assert.Equal(t, "10.64.64.32", mgmt["available_0"])
	assert.Equal(t, "10.64.64.63", mgmt["available_1"])
	assert.Equal(t, "10.64.64.96", ocf["available_0"])
	assert.Equal(t, "10.64.67.254", ocf["available_1"], "open-ended ocf band runs to the /22's last usable host")

	mgmtEnd := ipToUint32Test(t, mgmt["available_1"].(string))
	ocfStart := ipToUint32Test(t, ocf["available_0"].(string))
	assert.True(t, mgmtEnd < ocfStart, "mgmt available band must end before ocf available band starts")
}

// testPVEReservedIPsMgmtOcfDisjointCompact mirrors
// testPVEReservedIPsMgmtOcfDisjointWide on compact's compressed offsets
// (mgmt statics 3-22 identical to wide, ocf statics 23-26, haproxy 37,
// mgmt available 28-35, ocf available 36->).
func testPVEReservedIPsMgmtOcfDisjointCompact(t *testing.T) {
	cidr := "10.64.64.0/22"
	netCfg := config.NetworkConfig{Strategy: "compact"}

	mgmt, err := pveReservedIPsForSubnet(cidr, "mgmt", 0, netCfg, logger.Get())
	require.NoError(t, err)

	ocf, err := pveReservedIPsForSubnet(cidr, "ocf", 0, netCfg, logger.Get())
	require.NoError(t, err)

	assert.Equal(t, "10.64.64.4", mgmt["bosh_ip"])
	assert.Equal(t, "10.64.64.23", ocf["bosh_ip"])
	assert.NotEqual(t, mgmt["bosh_ip"], ocf["bosh_ip"])

	assert.Equal(t, "10.64.64.5", mgmt["vault_ip"])
	assert.Equal(t, "10.64.64.24", ocf["vault_ip"])

	assert.Equal(t, "10.64.64.6", mgmt["jumpbox_ip"])
	assert.Equal(t, "10.64.64.25", ocf["jumpbox_ip"])

	assert.Equal(t, "10.64.64.10", mgmt["blacksmith_ip"])
	assert.Equal(t, "10.64.64.26", ocf["blacksmith_ip"])

	// haproxy sits INSIDE the ocf available band (start+1), same coupling as
	// wide's.
	assert.Equal(t, "10.64.64.37", ocf["haproxy_ip"], "ocf haproxy: available band start + 1")
	assert.NotContains(t, mgmt, "haproxy_ip", "mgmt has no CF, no haproxy static")

	// mgmt-only named statics must never appear in ocf's tree.
	for _, key := range []string{"bastion_ip", "concourse_ip", "prometheus_ip", "shield_ip", "artifacts_ip",
		"wireguard_ip", "ovpn_ip", "rustfs_ip", "rustfs_ip_smoke", "proxycache_ip", "nfs_ip", "ocfp_ui_ip",
		"doomsday_ip", "shout_ip", "garage_ip", "garage_ip_smoke"} {
		assert.Contains(t, mgmt, key)
		assert.NotContains(t, ocf, key)
	}

	assert.Equal(t, "10.64.64.18", mgmt["doomsday_ip"])
	assert.Equal(t, "10.64.64.19", mgmt["shout_ip"])

	assert.Equal(t, "10.64.64.14", mgmt["rustfs_ip"])
	assert.Equal(t, "10.64.64.20", mgmt["garage_ip"])
	assert.NotEqual(t, mgmt["rustfs_ip"], mgmt["garage_ip"])
	assert.Equal(t, "10.64.64.21", mgmt["rustfs_ip_smoke"])
	assert.Equal(t, "10.64.64.22", mgmt["garage_ip_smoke"])

	// available/reserved bands must not overlap between tiers.
	assert.Equal(t, "10.64.64.28", mgmt["available_0"])
	assert.Equal(t, "10.64.64.35", mgmt["available_1"])
	assert.Equal(t, "10.64.64.36", ocf["available_0"])
	assert.Equal(t, "10.64.67.254", ocf["available_1"], "open-ended ocf band runs to the /22's last usable host")

	mgmtEnd := ipToUint32Test(t, mgmt["available_1"].(string))
	ocfStart := ipToUint32Test(t, ocf["available_0"].(string))
	assert.True(t, mgmtEnd < ocfStart, "mgmt available band must end before ocf available band starts")
}

func TestPVEReservedIPs_ExactOffsetTable(t *testing.T) {
	cidr := "10.64.68.0/22" // ocfp-1's base in a 4x/22 carve
	mgmt, err := pveReservedIPsForSubnet(cidr, "mgmt", 1, config.NetworkConfig{}, logger.Get())
	require.NoError(t, err)

	want := map[string]string{
		"bastion_ip":      "10.64.68.3",
		"bosh_ip":         "10.64.68.4",
		"vault_ip":        "10.64.68.5",
		"jumpbox_ip":      "10.64.68.6",
		"concourse_ip":    "10.64.68.7",
		"prometheus_ip":   "10.64.68.8",
		"shield_ip":       "10.64.68.9",
		"blacksmith_ip":   "10.64.68.10",
		"artifacts_ip":    "10.64.68.11",
		"wireguard_ip":    "10.64.68.12",
		"ovpn_ip":         "10.64.68.13",
		"rustfs_ip":       "10.64.68.14",
		"proxycache_ip":   "10.64.68.15",
		"nfs_ip":          "10.64.68.16",
		"ocfp_ui_ip":      "10.64.68.17",
		"doomsday_ip":     "10.64.68.18",
		"shout_ip":        "10.64.68.19",
		"garage_ip":       "10.64.68.20",
		"rustfs_ip_smoke": "10.64.68.21",
		"garage_ip_smoke": "10.64.68.22",
	}

	for key, ip := range want {
		assert.Equal(t, ip, mgmt[key], "key %s", key)
	}
}

func TestPVEReservedIPs_SubnetIndexDoesNotAffectRoleOffsets(t *testing.T) {
	// Every PVE workload subnet is its own physical /22 (unlike STACKIT's
	// shared address space), so subnetNum must not gate which roles appear.
	cidr := "10.1.2.0/22"

	subnet0, err := pveReservedIPsForSubnet(cidr, "mgmt", 0, config.NetworkConfig{}, logger.Get())
	require.NoError(t, err)
	subnet2, err := pveReservedIPsForSubnet(cidr, "mgmt", 2, config.NetworkConfig{}, logger.Get())
	require.NoError(t, err)

	assert.Equal(t, subnet0["bosh_ip"], subnet2["bosh_ip"], "same CIDR => same offset regardless of AZ index")
}

func TestPVEReservedIPs_MgmtBandOverride(t *testing.T) {
	cidr := "10.64.64.0/22"
	netCfg := config.NetworkConfig{Bands: config.NetworkBands{Mgmt: config.Band{Start: 40, End: 50}}}

	mgmt, err := pveReservedIPsForSubnet(cidr, "mgmt", 0, netCfg, logger.Get())
	require.NoError(t, err)

	assert.Equal(t, "10.64.64.40", mgmt["available_0"])
	assert.Equal(t, "10.64.64.50", mgmt["available_1"])

	// Override must not leak into the ocf tier.
	ocf, err := pveReservedIPsForSubnet(cidr, "ocf", 0, netCfg, logger.Get())
	require.NoError(t, err)
	assert.Equal(t, "10.64.64.96", ocf["available_0"])
}

func TestPVEReservedIPs_MgmtBandOverridePartialErrors(t *testing.T) {
	netCfg := config.NetworkConfig{Bands: config.NetworkBands{Mgmt: config.Band{Start: 40}}}

	_, err := pveReservedIPsForSubnet("10.64.64.0/22", "mgmt", 0, netCfg, logger.Get())
	assert.ErrorIs(t, err, ErrPVEBandOverridePartial)
}

func TestPVEReservedIPs_MgmtBandOverrideOutOfRangeErrors(t *testing.T) {
	tests := []struct {
		name  string
		start int
		end   int
	}{
		{"below floor collides with named statics", 10, 50},
		{"above ceiling collides with ocf zone", 40, 70},
		{"end not after start", 50, 40},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			netCfg := config.NetworkConfig{Bands: config.NetworkBands{Mgmt: config.Band{Start: tt.start, End: tt.end}}}
			_, err := pveReservedIPsForSubnet("10.64.64.0/22", "mgmt", 0, netCfg, logger.Get())
			assert.ErrorIs(t, err, ErrPVEBandOverrideOutOfRange)
		})
	}
}

// TestPVEReservedIPs_MgmtBandOverrideUnsupportedForNonWideStrategy proves
// applyPVEMgmtBandOverride rejects an explicit Bands.Mgmt override for any
// strategy other than wide, rather than validating it against wide's
// floor/ceiling literals (32/63), which do not describe compact's mgmt zone
// (28-35) and would silently admit an out-of-band override or reject a
// valid one for the wrong reason.
func TestPVEReservedIPs_MgmtBandOverrideUnsupportedForNonWideStrategy(t *testing.T) {
	netCfg := config.NetworkConfig{
		Strategy: "compact",
		Bands:    config.NetworkBands{Mgmt: config.Band{Start: 30, End: 34}},
	}

	_, err := pveReservedIPsForSubnet("10.64.64.0/26", "mgmt", 0, netCfg, logger.Get())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPVEBandOverrideUnsupportedStrategy)
	assert.Contains(t, err.Error(), `mgmt band override not supported for strategy "compact"`)
}

// TestPVEReservedIPs_MgmtBandOverrideStillWorksForWide guards against a
// regression where threading the strategy name into
// applyPVEMgmtBandOverride broke the wide path it was already exercising
// (TestPVEReservedIPs_MgmtBandOverride above uses the empty-Strategy
// default; this proves an explicit "wide" Strategy behaves identically).
func TestPVEReservedIPs_MgmtBandOverrideStillWorksForWide(t *testing.T) {
	netCfg := config.NetworkConfig{
		Strategy: "wide",
		Bands:    config.NetworkBands{Mgmt: config.Band{Start: 40, End: 50}},
	}

	mgmt, err := pveReservedIPsForSubnet("10.64.64.0/22", "mgmt", 0, netCfg, logger.Get())
	require.NoError(t, err)
	assert.Equal(t, "10.64.64.40", mgmt["available_0"])
	assert.Equal(t, "10.64.64.50", mgmt["available_1"])
}

func TestPVEReservedIPs_NoOverrideLeavesDefaultTableUnmodified(t *testing.T) {
	// Guards against applyPVEMgmtBandOverride mutating the shared default
	// table in place (which would corrupt subsequent calls across subnets).
	first, err := pveReservedIPsForSubnet("10.64.64.0/22", "mgmt", 0, config.NetworkConfig{}, logger.Get())
	require.NoError(t, err)

	netCfg := config.NetworkConfig{Bands: config.NetworkBands{Mgmt: config.Band{Start: 40, End: 50}}}
	_, err = pveReservedIPsForSubnet("10.64.64.0/22", "mgmt", 0, netCfg, logger.Get())
	require.NoError(t, err)

	second, err := pveReservedIPsForSubnet("10.64.64.0/22", "mgmt", 0, config.NetworkConfig{}, logger.Get())
	require.NoError(t, err)

	assert.Equal(t, first["available_0"], second["available_0"], "default table must not be mutated by a prior override call")
}

// TestPVEMgmtBandOverrideBoundsMatchWideMgmtAvailable couples
// pveMgmtBandOverrideFloor/Ceiling to the wide strategy's own emitted mgmt
// available band, rather than trusting the two to stay in sync by
// convention alone: the floor/ceiling are declared as literals (see their
// doc comment in pve_reserved_ips.go) because they cannot be imported from
// netlayout directly, so without this test a future wide retune would
// silently admit an override that reaches into ocf's territory instead of
// failing loudly here.
func TestPVEMgmtBandOverrideBoundsMatchWideMgmtAvailable(t *testing.T) {
	table, err := netlayout.Default().WorkloadTable("")
	require.NoError(t, err)

	spec := table["available"]["mgmt"].RangeSpec

	var start, end int

	n, err := fmt.Sscanf(spec, "%d-%d", &start, &end)
	require.NoError(t, err)
	require.Equal(t, 2, n, "wide's mgmt available RangeSpec %q must be a plain start-end range", spec)

	assert.Equal(t, start, pveMgmtBandOverrideFloor,
		"wide's mgmt available band start drifted from the override floor")
	assert.Equal(t, end, pveMgmtBandOverrideCeiling,
		"wide's mgmt available band end drifted from the override ceiling")
}

// TestPVEReservedIPs_WideRejectsSubnetSmallerThan25 proves the vault-layer
// PVE reserved-ips path enforces the wide strategy's minimum subnet size
// (see TestWideValidateSubnet_RejectsSubnetSmallerThan25 for the netlayout-
// layer proof): a /26 workload subnet cannot fit the strategy's highest
// fixed offset, so it must hard-error here rather than silently emitting an
// IP outside the subnet.
func TestPVEReservedIPs_WideRejectsSubnetSmallerThan25(t *testing.T) {
	_, err := pveReservedIPsForSubnet("10.64.64.0/26", "mgmt", 0, config.NetworkConfig{}, logger.Get())
	require.Error(t, err)
	assert.ErrorIs(t, err, netlayout.ErrSubnetTooSmall)
}

// TestResolveLayout proves resolveLayout is a thin, testable wrapper over
// netlayout.Lookup(netCfg.Strategy): empty Strategy resolves to the "wide"
// default, "compact" resolves by name (see TestPVEReservedIPs_CompactSelection
// below for compact's real WorkloadTable exercised through the runtime
// path), and an unrecognized name surfaces netlayout.ErrUnknownStrategy for
// errors.Is callers.
func TestResolveLayout(t *testing.T) {
	t.Run("EmptyStrategyResolvesToWide", func(t *testing.T) {
		layout, err := resolveLayout(config.NetworkConfig{})
		require.NoError(t, err)
		assert.Equal(t, "wide", layout.Name())
	})

	t.Run("CompactStrategyResolvesByName", func(t *testing.T) {
		layout, err := resolveLayout(config.NetworkConfig{Strategy: "compact"})
		require.NoError(t, err)
		assert.Equal(t, "compact", layout.Name())
	})

	t.Run("UnknownStrategyWrapsErrUnknownStrategy", func(t *testing.T) {
		_, err := resolveLayout(config.NetworkConfig{Strategy: "bogus"})
		require.Error(t, err)
		assert.ErrorIs(t, err, netlayout.ErrUnknownStrategy)
	})
}

// TestPVEReservedIPs_CompactSelection proves the compact strategy is
// selectable through the full runtime path — resolveLayout,
// ValidateSubnet, WorkloadTable, and reservedip.Calculate — not merely
// registered by name: on a /26 workload subnet (compact's own MinPrefix),
// the ocf tier's bosh static lands at the compact table's offset 23.
func TestPVEReservedIPs_CompactSelection(t *testing.T) {
	cidr := "10.64.64.0/26"
	netCfg := config.NetworkConfig{Strategy: "compact"}

	ocf, err := pveReservedIPsForSubnet(cidr, "ocf", 0, netCfg, logger.Get())
	require.NoError(t, err)

	assert.Equal(t, "10.64.64.23", ocf["bosh_ip"], "compact ocf bosh: base + offset 23")

	mgmt, err := pveReservedIPsForSubnet(cidr, "mgmt", 0, netCfg, logger.Get())
	require.NoError(t, err)

	assert.Equal(t, "10.64.64.4", mgmt["bosh_ip"], "compact mgmt bosh: identical to wide's offset 4")
}

// workloadTableErrLayout is a fake Layout whose ValidateSubnet passes but
// whose WorkloadTable fails, so the table-construction guard in
// pveReservedIPsForSubnetWithLayout can be exercised in isolation from any
// registered strategy's real behavior.
type workloadTableErrLayout struct{}

func (workloadTableErrLayout) Name() string          { return "fake-table-err" }
func (workloadTableErrLayout) SchemeVersion() string { return "0-test" }
func (workloadTableErrLayout) MinPrefix() int        { return 25 }
func (workloadTableErrLayout) MinSubnets() int       { return 1 }

func (workloadTableErrLayout) Placement() netlayout.Placement {
	return netlayout.PlacementColocated
}

func (workloadTableErrLayout) WorkloadTable(_ string) (reservedip.AssignmentTable, error) {
	return nil, netlayout.ErrNotImplemented
}

func (workloadTableErrLayout) LayerASlots(_, _ string, _ int) (netlayout.LayerASlots, error) {
	return netlayout.LayerASlots{}, netlayout.ErrNotImplemented
}

func (workloadTableErrLayout) ValidateSubnet(_ string) error { return nil }

func (workloadTableErrLayout) ValidateSubnetSet(_ []string) error { return nil }

func (workloadTableErrLayout) ValidateBand(_ netlayout.Tier, _ string, _, _ int) error {
	return netlayout.ErrNotImplemented
}

// TestPVEReservedIPs_WorkloadTableErrorFailsLoudly locks the guard on the
// WorkloadTable step itself: a layout that validates the subnet but cannot
// produce a table must error out with the strategy and step named, never
// continue into applyPVEMgmtBandOverride with a nil table.
func TestPVEReservedIPs_WorkloadTableErrorFailsLoudly(t *testing.T) {
	table, err := pveReservedIPsForSubnetWithLayout(
		workloadTableErrLayout{}, "10.64.64.0/22", "mgmt", 0, config.NetworkConfig{}, logger.Get())
	require.Error(t, err)
	assert.ErrorIs(t, err, netlayout.ErrNotImplemented)
	assert.Contains(t, err.Error(), `strategy "fake-table-err"`)
	assert.Contains(t, err.Error(), "workload table")
	assert.Nil(t, table, "must not return a partial/empty table alongside the error")
}

func ipToUint32Test(t *testing.T, ip string) uint32 {
	t.Helper()

	v, ok := vaultIPToUint32(ip)
	require.True(t, ok, "failed to parse IP %s", ip)

	return v
}
