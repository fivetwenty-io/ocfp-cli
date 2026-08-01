package vault

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPVEReservedIPs_MgmtOcfDisjoint is the core acceptance test for the
// tiered layout: mgmt and ocf must compute DIFFERENT, non-overlapping
// reserved-ips on the identical shared /22, eliminating the collision the
// plan documents (wayne bloc 2026-07-22: mgmt concourse allocated on top of
// live ocf cf VMs).
func TestPVEReservedIPs_MgmtOcfDisjoint(t *testing.T) {
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
	netCfg := config.NetworkConfig{AvailableBandStart: 40, AvailableBandEnd: 50}

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
	netCfg := config.NetworkConfig{AvailableBandStart: 40}

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
			netCfg := config.NetworkConfig{AvailableBandStart: tt.start, AvailableBandEnd: tt.end}
			_, err := pveReservedIPsForSubnet("10.64.64.0/22", "mgmt", 0, netCfg, logger.Get())
			assert.ErrorIs(t, err, ErrPVEBandOverrideOutOfRange)
		})
	}
}

func TestPVEReservedIPs_NoOverrideLeavesDefaultTableUnmodified(t *testing.T) {
	// Guards against applyPVEMgmtBandOverride mutating the shared default
	// table in place (which would corrupt subsequent calls across subnets).
	first, err := pveReservedIPsForSubnet("10.64.64.0/22", "mgmt", 0, config.NetworkConfig{}, logger.Get())
	require.NoError(t, err)

	netCfg := config.NetworkConfig{AvailableBandStart: 40, AvailableBandEnd: 50}
	_, err = pveReservedIPsForSubnet("10.64.64.0/22", "mgmt", 0, netCfg, logger.Get())
	require.NoError(t, err)

	second, err := pveReservedIPsForSubnet("10.64.64.0/22", "mgmt", 0, config.NetworkConfig{}, logger.Get())
	require.NoError(t, err)

	assert.Equal(t, first["available_0"], second["available_0"], "default table must not be mutated by a prior override call")
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

func ipToUint32Test(t *testing.T, ip string) uint32 {
	t.Helper()

	v, ok := vaultIPToUint32(ip)
	require.True(t, ok, "failed to parse IP %s", ip)

	return v
}
