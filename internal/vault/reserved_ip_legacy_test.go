package vault

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The two fixtures are the reserved-ips subtrees exported from ocfp-lab-pmx
// and ocfp-lab-pve-cpi immediately before the 2026-07-28 populate that
// rewrote their live addresses. Both blocs were provisioned before commit
// 04a4395 moved the ocf tier's statics out of the window it shared with
// mgmt, so their recorded addresses are the empirical record of the earlier
// scheme. Driving the current derivation against them is the regression:
// the guard must keep every recorded address and report the divergence.
const (
	legacyFixturePMX    = "testdata/reserved_ips_legacy_pmx.json"
	legacyFixturePVECPI = "testdata/reserved_ips_legacy_pve_cpi.json"

	// legacyWorkloadPrefixLen is the prefix length of a PVE workload
	// subnet. Each fixture records the subnet's network address as
	// reserved_a, which is what the CIDR is reconstructed from.
	legacyWorkloadPrefixLen = "/22"
)

// legacyFixture maps a bloc-relative vault path to the record it held.
type legacyFixture map[string]map[string]interface{}

func loadLegacyFixture(t *testing.T, name string) legacyFixture {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(name))
	require.NoError(t, err)

	var fixture legacyFixture

	require.NoError(t, json.Unmarshal(raw, &fixture))
	require.NotEmpty(t, fixture)

	return fixture
}

// legacyPathParts splits a fixture path ("ocf/net/subnets/ocfp-0/reserved-ips")
// into its env type and subnet name.
func legacyPathParts(path string) (envType, subnet string, ok bool) {
	segments := strings.Split(path, "/")
	if len(segments) != 5 { //nolint:mnd // env/net/subnets/<name>/reserved-ips
		return "", "", false
	}

	return segments[0], segments[3], true
}

// TestReservedIPGuardLegacyFixturesKeepRecordedAddresses drives the current
// PVE derivation at every workload subnet in both fixtures and asserts that
// no recorded address moved.
func TestReservedIPGuardLegacyFixturesKeepRecordedAddresses(t *testing.T) {
	for _, fixtureName := range []string{legacyFixturePMX, legacyFixturePVECPI} {
		t.Run(filepath.Base(fixtureName), func(t *testing.T) {
			fixture := loadLegacyFixture(t, fixtureName)

			under := newCountingSafe()
			original := map[string]map[string]interface{}{}

			for path, record := range fixture {
				seeded := map[string]interface{}{}
				maps.Copy(seeded, record)
				require.NoError(t, under.fakeSafe.SetMultiple(path, seeded))

				kept := map[string]interface{}{}
				maps.Copy(kept, record)
				original[path] = kept
			}

			under.adds = 0
			under.mutations = 0

			guard := newReservedIPGuard(under, false, logger.Get())
			applyLegacyDerivation(t, guard, fixture)

			assert.Zero(t, under.mutations,
				"the current derivation must not change any address a legacy bloc already records")
			assert.True(t, guard.Report().HasDrift(),
				"the divergence must be reported, not silently dropped")

			for path, record := range original {
				for key, value := range record {
					assert.Equal(t, value, under.data[path][key],
						"%s:%s must keep the address the bloc is deployed at", path, key)
				}
			}
		})
	}
}

// TestReservedIPGuardLegacyFixturesReportDirectorMoves pins the specific
// addresses the incident moved. The ocf director answering at the pre-populate
// address is what made the rewrite a live-traffic defect rather than a
// cosmetic one.
func TestReservedIPGuardLegacyFixturesReportDirectorMoves(t *testing.T) {
	fixture := loadLegacyFixture(t, legacyFixturePVECPI)

	under := newCountingSafe()
	for path, record := range fixture {
		seeded := map[string]interface{}{}
		maps.Copy(seeded, record)
		require.NoError(t, under.fakeSafe.SetMultiple(path, seeded))
	}

	guard := newReservedIPGuard(under, false, logger.Get())
	applyLegacyDerivation(t, guard, fixture)

	const ocfSubnet = "ocf/net/subnets/ocfp-0/reserved-ips"

	drifted := map[string]ReservedIPDrift{}

	for _, drift := range guard.Report().Drifts {
		if drift.Path == ocfSubnet {
			drifted[drift.Key] = drift
		}
	}

	for _, key := range []string{"bosh_ip", "director_ip", "ip", "vault_ip", "jumpbox_ip", "blacksmith_ip", "haproxy_ip"} {
		require.Contains(t, drifted, key, "%s moved between schemes and must be reported", key)
	}

	// The live ocf director answers at .4; the current table derives .64.
	assert.Equal(t, "10.254.20.4", drifted["director_ip"].Existing)
	assert.Equal(t, "10.254.20.64", drifted["director_ip"].Derived)
	assert.Equal(t, "10.254.20.4", under.data[ocfSubnet]["director_ip"])
}

// TestReservedIPGuardLegacyFixturesStillAddNewRoles is the other half of the
// policy: roles the legacy table never had (shout, garage, rustfs) must
// still reach these blocs.
func TestReservedIPGuardLegacyFixturesStillAddNewRoles(t *testing.T) {
	fixture := loadLegacyFixture(t, legacyFixturePMX)

	under := newCountingSafe()
	for path, record := range fixture {
		seeded := map[string]interface{}{}
		maps.Copy(seeded, record)
		require.NoError(t, under.fakeSafe.SetMultiple(path, seeded))
	}

	guard := newReservedIPGuard(under, false, logger.Get())
	applyLegacyDerivation(t, guard, fixture)

	const mgmtSubnet = "mgmt/net/subnets/ocfp-0/reserved-ips"

	// shout and garage post-date both fixtures and are mgmt-tier statics
	// well inside the mgmt window, so adding them moves nothing.
	assert.Contains(t, under.data[mgmtSubnet], "shout_ip")
	assert.Contains(t, under.data[mgmtSubnet], "garage_ip")
}

// TestReservedIPGuardLegacyFixturesWithholdSchemeStamp asserts these blocs
// are reported as provisioned under an earlier scheme rather than being
// relabelled as conforming to the current one.
func TestReservedIPGuardLegacyFixturesWithholdSchemeStamp(t *testing.T) {
	fixture := loadLegacyFixture(t, legacyFixturePVECPI)

	under := newCountingSafe()
	for path, record := range fixture {
		seeded := map[string]interface{}{}
		maps.Copy(seeded, record)
		require.NoError(t, under.fakeSafe.SetMultiple(path, seeded))
	}

	guard := newReservedIPGuard(under, false, logger.Get())
	applyLegacyDerivation(t, guard, fixture)

	require.NotEmpty(t, guard.Report().Schemes)

	for _, scheme := range guard.Report().Schemes {
		assert.Empty(t, scheme.Existing, "these blocs predate the stamp")
		assert.Equal(t, reservedIPSchemeVersion, scheme.Current)
		assert.NotContains(t, under.data[scheme.Path], reservedIPSchemeKey,
			"a drifting record must not be stamped as conforming")
	}
}

// applyLegacyDerivation runs the current PVE reserved-IP derivation through
// the guard for every workload subnet in the fixture, reproducing what
// `ocfp vault populate` does against these blocs. The infra subnet is
// skipped: its addresses come from bootstrap state, not the offset table.
func applyLegacyDerivation(t *testing.T, guard *reservedIPGuard, fixture legacyFixture) {
	t.Helper()

	applied := 0

	for path, record := range fixture {
		envType, subnet, ok := legacyPathParts(path)
		if !ok {
			continue
		}

		subnetNum, isWorkload := pveWorkloadSubnetIndex(subnet)
		if !isWorkload {
			continue
		}

		network, ok := record["reserved_a"].(string)
		require.True(t, ok, "%s must record its network address as reserved_a", path)

		derived, err := pveReservedIPsForSubnet(
			network+legacyWorkloadPrefixLen, envType, subnetNum, config.NetworkConfig{}, logger.Get())
		require.NoError(t, err)

		// populate writes the two compatibility aliases alongside bosh_ip.
		if boshIP, found := derived["bosh_ip"]; found {
			derived["ip"] = boshIP
			derived["director_ip"] = boshIP
		}

		require.NoError(t, guard.SetMultiple(path, derived))

		applied++
	}

	require.Equal(t, 6, applied, "both tiers of all three workload subnets must be exercised") //nolint:mnd
}
