package vault

import (
	"fmt"
	"maps"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/fivetwenty-io/ocfp-cli/internal/logger"
	"github.com/fivetwenty-io/ocfp-cli/internal/netlayout"
	"github.com/fivetwenty-io/ocfp-cli/internal/reservedip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// controlPlaneKeys are the three keys the control-plane role adds to a mgmt
// reserved-ips record: the address itself, and the _a/_b bounds Genesis
// brackets the role's static range with.
var controlPlaneKeys = []string{"control_plane_ip", "control_plane_a", "control_plane_b"} //nolint:gochecknoglobals // test fixture

// TestControlPlaneReservedIPs_KitKeys pins the exact keys and addresses a
// kit reads from secret/config/{bloc}/mgmt/net/subnets/ocfp-{N}/reserved-ips
// for the control-plane role under every built-in strategy.
func TestControlPlaneReservedIPs_KitKeys(t *testing.T) {
	t.Run("wide", func(t *testing.T) {
		const cidr = "10.64.64.0/22"

		mgmt, err := reservedIPsForSubnet(cidr, MgmtEnvType, 0, &config.Config{}, logger.Get())
		require.NoError(t, err)

		assert.Equal(t, "10.64.64.23", mgmt["control_plane_ip"])
		assert.Equal(t, "10.64.64.22", mgmt["control_plane_a"])
		assert.Equal(t, "10.64.64.24", mgmt["control_plane_b"])

		// The role sits inside the reserved complement the record already
		// carried, so the band keys are exactly what scheme "2" wrote.
		assert.Equal(t, "10.64.64.0", mgmt["reserved_0"])
		assert.Equal(t, "10.64.64.31", mgmt["reserved_1"])
		assert.Equal(t, "10.64.64.32", mgmt["available_0"])
		assert.Equal(t, "10.64.64.63", mgmt["available_1"])

		ocf, err := reservedIPsForSubnet(cidr, OCFEnvType, 0, &config.Config{}, logger.Get())
		require.NoError(t, err)

		for _, key := range controlPlaneKeys {
			assert.NotContains(t, ocf, key, "the control-plane role is mgmt-only")
		}
	})

	t.Run("spanning", func(t *testing.T) {
		cfg := &config.Config{Network: config.NetworkConfig{Strategy: "spanning"}}

		mgmt0, err := reservedIPsForSubnet("10.4.4.0/22", MgmtEnvType, 0, cfg, logger.Get())
		require.NoError(t, err)

		assert.Equal(t, "10.4.4.23", mgmt0["control_plane_ip"])
		assert.Equal(t, "10.4.4.22", mgmt0["control_plane_a"])
		assert.Equal(t, "10.4.4.24", mgmt0["control_plane_b"])

		for idx, cidr := range map[int]string{1: "10.4.8.0/22", 2: "10.4.12.0/22"} {
			mgmt, err := reservedIPsForSubnet(cidr, MgmtEnvType, idx, cfg, logger.Get())
			require.NoError(t, err)

			for _, key := range controlPlaneKeys {
				assert.NotContains(t, mgmt, key, "control_plane is pinned to subnet 0, absent on index %d", idx)
			}
		}
	})

	t.Run("compact", func(t *testing.T) {
		cfg := &config.Config{Network: config.NetworkConfig{Strategy: "compact"}}

		mgmt, err := reservedIPsForSubnet("10.64.64.0/26", MgmtEnvType, 0, cfg, logger.Get())
		require.NoError(t, err)

		for _, key := range controlPlaneKeys {
			assert.NotContains(t, mgmt, key, "compact has no free mgmt offset and places no control-plane static")
		}
	})
}

// TestReservedIPs_EveryStaticWrittenOutsideBands proves, for every built-in
// strategy, subnet index, and tier, that every named static the strategy
// places on that index reaches the record at its own offset. The engine
// silently drops a static whose address is already taken, so a missing or
// moved key here is how two roles colliding would show up. It also proves
// no static falls inside its tier's available band, except the ocf haproxy
// static, which the cf kit deliberately claims from inside its band.
func TestReservedIPs_EveryStaticWrittenOutsideBands(t *testing.T) {
	cidrs := []string{"10.4.4.0/22", "10.4.8.0/22", "10.4.12.0/22"}

	for _, name := range []string{"wide", "spanning", "compact"} {
		t.Run(name, func(t *testing.T) {
			layout, err := netlayout.Lookup(name)
			require.NoError(t, err)

			table, err := layout.WorkloadTable(cidrs[0])
			require.NoError(t, err)

			cfg := &config.Config{Network: config.NetworkConfig{Strategy: name}}

			for idx := range layout.MinSubnets() {
				cidr := cidrs[idx]
				base, _, err := reservedip.ParseCIDR(cidr)
				require.NoError(t, err)

				for _, envType := range []string{MgmtEnvType, OCFEnvType} {
					record, err := reservedIPsForSubnet(cidr, envType, idx, cfg, logger.Get())
					require.NoError(t, err)

					bandStart := ipToUint32Test(t, record["available_0"].(string))
					bandEnd := ipToUint32Test(t, record["available_1"].(string))

					for role, tiers := range table {
						if role == "available" || role == "reserved" {
							continue
						}

						offset, key, placed := staticOn(role, tiers[envType], idx)
						if !placed {
							continue
						}

						want := reservedip.AddOffsetToIP(base, offset)
						assert.Equal(t, want, record[key], "%s index %d %s: %s", name, idx, envType, key)

						ip := ipToUint32Test(t, want)
						inBand := ip >= bandStart && ip <= bandEnd

						if envType == OCFEnvType && role == "haproxy" {
							assert.True(t, inBand, "%s index %d: ocf haproxy must sit inside the ocf band", name, idx)

							continue
						}

						assert.False(t, inBand, "%s index %d %s: %s at %s lies inside the available band",
							name, idx, envType, key, want)
					}
				}
			}
		})
	}
}

// staticOn returns the offset and output key of the static assignment a on
// subnet index idx, and whether it is placed there at all.
func staticOn(role string, a *reservedip.Assignment, idx int) (int, string, bool) {
	if a == nil {
		return 0, "", false
	}

	key := role + "_ip"
	if a.IPKey != "" {
		key = a.IPKey
	}

	if len(a.SubnetMapping) == 0 {
		return a.Offset, key, a.Offset > 0
	}

	for offset, subnets := range a.SubnetMapping {
		if reservedip.ContainsInt(subnets, idx) {
			return offset, key, true
		}
	}

	return 0, "", false
}

// TestControlPlaneReservedIPs_Override proves network.statics.mgmt.
// controlPlane moves the role, and only the role, through the same path
// every provider's writer uses.
func TestControlPlaneReservedIPs_Override(t *testing.T) {
	const cidr = "10.64.64.0/22"

	cfg := &config.Config{Network: config.NetworkConfig{
		Statics: config.NetworkStatics{Mgmt: config.MgmtStatics{ControlPlane: []int{30}}},
	}}

	mgmt, err := reservedIPsForSubnet(cidr, MgmtEnvType, 0, cfg, logger.Get())
	require.NoError(t, err)

	assert.Equal(t, "10.64.64.30", mgmt["control_plane_ip"])
	assert.Equal(t, "10.64.64.29", mgmt["control_plane_a"])
	assert.Equal(t, "10.64.64.31", mgmt["control_plane_b"])

	defaults, err := reservedIPsForSubnet(cidr, MgmtEnvType, 0, &config.Config{}, logger.Get())
	require.NoError(t, err)

	for key, value := range defaults {
		if key == "control_plane_ip" || key == "control_plane_a" || key == "control_plane_b" {
			continue
		}

		assert.Equal(t, value, mgmt[key], "the override must not move %s", key)
	}

	ocf, err := reservedIPsForSubnet(cidr, OCFEnvType, 0, cfg, logger.Get())
	require.NoError(t, err)
	assert.NotContains(t, ocf, "control_plane_ip")
}

// TestControlPlaneReservedIPs_OverrideRejected proves a bad override fails
// the write with the netlayout sentinel and names the config key, so the
// operator knows which setting to fix.
func TestControlPlaneReservedIPs_OverrideRejected(t *testing.T) {
	tests := []struct {
		name     string
		strategy string
		cidr     string
		offsets  []int
		wantErr  error
	}{
		{name: "inside mgmt band", cidr: "10.64.64.0/22", offsets: []int{40}, wantErr: netlayout.ErrStaticOverrideInBand},
		{name: "on another static", cidr: "10.64.64.0/22", offsets: []int{9}, wantErr: netlayout.ErrStaticOverrideCollidesStatic},
		{name: "two entries", cidr: "10.64.64.0/22", offsets: []int{23, 24}, wantErr: netlayout.ErrStaticOverrideCount},
		{name: "gateway", cidr: "10.64.64.0/22", offsets: []int{1}, wantErr: netlayout.ErrStaticOverrideGateway},
		{name: "compact has no role", strategy: "compact", cidr: "10.64.64.0/26", offsets: []int{30}, wantErr: netlayout.ErrStaticOverrideUnknownRole},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Network: config.NetworkConfig{
				Strategy: tc.strategy,
				Statics:  config.NetworkStatics{Mgmt: config.MgmtStatics{ControlPlane: tc.offsets}},
			}}

			for _, envType := range []string{MgmtEnvType, OCFEnvType} {
				record, err := reservedIPsForSubnet(tc.cidr, envType, 0, cfg, logger.Get())
				require.ErrorIs(t, err, tc.wantErr, "%s tier", envType)
				assert.Contains(t, err.Error(), "network.statics.mgmt.controlPlane")
				assert.Nil(t, record)
			}
		})
	}
}

// TestControlPlaneReservedIPs_BandOverrideInteraction proves a mgmt band
// override that reaches offset 23 is refused loudly rather than silently
// dropping the role, and that moving the role clear of the band with the
// static override makes the same band override valid.
func TestControlPlaneReservedIPs_BandOverrideInteraction(t *testing.T) {
	const cidr = "10.64.64.0/22"

	band := config.NetworkBands{Mgmt: config.Band{Start: 23, End: 63}}

	_, err := reservedIPsForSubnet(cidr, MgmtEnvType, 0,
		&config.Config{Network: config.NetworkConfig{Bands: band}}, logger.Get())
	require.ErrorIs(t, err, netlayout.ErrBandOverrideCollidesStatic)
	assert.Contains(t, err.Error(), `"control_plane" at offset 23`)

	cfg := &config.Config{Network: config.NetworkConfig{
		Bands:   band,
		Statics: config.NetworkStatics{Mgmt: config.MgmtStatics{ControlPlane: []int{80}}},
	}}

	mgmt, err := reservedIPsForSubnet(cidr, MgmtEnvType, 0, cfg, logger.Get())
	require.NoError(t, err)

	assert.Equal(t, "10.64.64.80", mgmt["control_plane_ip"])
	assert.Equal(t, "10.64.64.23", mgmt["available_0"])
	assert.Equal(t, "10.64.64.63", mgmt["available_1"])
}

// TestControlPlaneReservedIPs_DriftGuardIsAdditive drives the real PVE
// workload-subnet writer through the drift guard against a record an older
// build wrote, which holds every key except the control-plane ones. The
// guard must forward exactly the three new keys, report no drift, no
// obsolete keys, and no scheme mismatch, and leave the scheme stamp as it
// was. That is the proof no scheme bump is needed and no live address
// moves on an existing bloc.
func TestControlPlaneReservedIPs_DriftGuardIsAdditive(t *testing.T) {
	tests := []struct {
		name     string
		strategy string
		scheme   string
		cidr     string
		idx      int
		wantAdds int
	}{
		{name: "wide", strategy: "wide", scheme: "2", cidr: "10.64.64.0/22", idx: 0, wantAdds: 3},
		{name: "spanning index 0", strategy: "spanning", scheme: "4-spanning", cidr: "10.4.4.0/22", idx: 0, wantAdds: 3},
		{name: "spanning index 1", strategy: "spanning", scheme: "4-spanning", cidr: "10.4.8.0/22", idx: 1, wantAdds: 0},
		{name: "spanning index 2", strategy: "spanning", scheme: "4-spanning", cidr: "10.4.12.0/22", idx: 2, wantAdds: 0},
		{name: "compact", strategy: "compact", scheme: "3-compact", cidr: "10.64.64.0/26", idx: 0, wantAdds: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Provider: "pve", Network: config.NetworkConfig{Strategy: tc.strategy}}
			subnetPath := fmt.Sprintf("secret/config/%s/mgmt/net/subnets/ocfp-%d", guardTestBloc, tc.idx)
			reservedPath := subnetPath + "/reserved-ips"
			genesisName := fmt.Sprintf("ocfp-%d", tc.idx)

			// What this build writes to a fresh vault.
			fresh := newFakeSafe()
			require.NoError(t, NewPVEVaultProvider(cfg, fresh, guardTestBloc).
				writeTieredReservedIPs(tc.cidr, MgmtEnvType, tc.idx, genesisName, subnetPath))

			current := fresh.data[reservedPath]
			require.NotEmpty(t, current)

			// The record an earlier build left behind: everything except the
			// control-plane keys, stamped with the strategy's scheme.
			older := maps.Clone(current)
			for _, key := range controlPlaneKeys {
				delete(older, key)
			}

			older[reservedIPSchemeKey] = tc.scheme

			under := newCountingSafe()
			require.NoError(t, under.fakeSafe.SetMultiple(reservedPath, older))

			under.adds, under.mutations = 0, 0

			guard := newReservedIPGuardWithScheme(under, false, tc.scheme, logger.Get())
			require.NoError(t, NewPVEVaultProvider(cfg, guard, guardTestBloc).
				writeTieredReservedIPs(tc.cidr, MgmtEnvType, tc.idx, genesisName, subnetPath))

			report := guard.Report()
			assert.Empty(t, report.Drifts, "no held address may move")
			assert.Empty(t, report.Obsoletes, "no held key may be retired")
			assert.Empty(t, report.Schemes, "no scheme mismatch may be reported")

			assert.Equal(t, tc.wantAdds, under.adds, "only the control-plane keys are new")
			assert.Zero(t, under.mutations, "no held value may change")
			assert.Equal(t, tc.scheme, under.data[reservedPath][reservedIPSchemeKey], "the stamp is unchanged")

			for _, key := range controlPlaneKeys {
				if tc.wantAdds == 0 {
					assert.NotContains(t, under.data[reservedPath], key)

					continue
				}

				assert.Equal(t, current[key], under.data[reservedPath][key])
			}
		})
	}
}

// TestControlPlaneReservedIPs_OverrideCheckedOnPlacingSubnetOnly proves the
// writer hands each subnet's own index to the override check. It uses a
// bring-your-own spanning strategy with a closed ocf band, because the
// builtin spanning strategy's /25 floor and open ocf band from 96 make
// every offset subnet 0 accepts fit every other subnet. Subnet 0 is a /24
// and places control_plane, so offset 200 is written there. Subnet 1 is a
// /25 that ends at offset 127 and does not place the role, so its records
// are written without the key instead of rejecting the override.
func TestControlPlaneReservedIPs_OverrideCheckedOnPlacingSubnetOnly(t *testing.T) {
	def, err := netlayout.LoadDefinition([]byte(`
name: mixed-size-spanning
scheme_version: "test-mixed-size-spanning"
placement: spanning
min_prefix: 25
min_subnets: 2
tiers:
  mgmt:
    statics:
      bastion:       { offset: 3, subnets: [0] }
      control_plane: { offset: 23, subnets: [0] }
    available: { start: 32, end: 63 }
  ocf:
    statics:
      bosh: { offset: 64, subnets: [0] }
    available: { start: 96, end: 120 }
`), "mixed-size-spanning.yaml")
	require.NoError(t, err)

	layout, err := netlayout.Compile(def)
	require.NoError(t, err)

	netCfg := config.NetworkConfig{Statics: config.NetworkStatics{Mgmt: config.MgmtStatics{ControlPlane: []int{200}}}}

	mgmt0, err := reservedIPsForSubnetWithLayout(layout, "10.0.0.0/24", MgmtEnvType, 0, netCfg, logger.Get())
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.200", mgmt0["control_plane_ip"])

	for _, envType := range []string{MgmtEnvType, OCFEnvType} {
		rec, err := reservedIPsForSubnetWithLayout(layout, "10.0.1.0/25", envType, 1, netCfg, logger.Get())
		require.NoError(t, err, "%s subnet 1 rejected an override it does not place", envType)
		assert.NotContains(t, rec, "control_plane_ip")
	}

	_, err = reservedIPsForSubnetWithLayout(layout, "10.0.1.0/25", MgmtEnvType, 0, netCfg, logger.Get())
	require.ErrorIs(t, err, netlayout.ErrStaticOverrideOutsideSubnet)
}
