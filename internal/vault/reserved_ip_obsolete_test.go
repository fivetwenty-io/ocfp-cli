package vault

import (
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// obsoleteKeys returns the reported obsolete key names, sorted by report
// order, so a test can assert on names without restating paths.
func obsoleteKeys(report ReservedIPReport) []string {
	keys := make([]string, 0, len(report.Obsoletes))
	for _, obs := range report.Obsoletes {
		keys = append(keys, obs.Key)
	}

	return keys
}

// seedStaleBandRecord writes a reserved-ips record holding both the numeric
// band keys the current table derives and the lettered band keys an earlier
// build wrote. This is the shape found on the two blocs provisioned before
// the strategy split (2026-08-03): the lettered pair survived every populate
// and every migrate, because nothing ever compared vault's key set against
// the derivation's.
func seedStaleBandRecord(safe *fakeSafe, path string) {
	safe.data[path] = map[string]interface{}{
		"bosh_ip":     "10.0.0.64",
		"available_0": "10.0.0.96",
		"available_1": "10.0.3.254",
		"available_a": "10.0.0.12",
		"available_b": "10.0.0.29",
		"reserved_c":  "10.0.0.62",
		"reserved_d":  "10.0.3.254",
	}
}

// derivedBandRecord is what the current table produces for the record
// seedStaleBandRecord seeds: the same numeric keys, none of the lettered
// ones.
func derivedBandRecord() map[string]interface{} {
	return map[string]interface{}{
		"bosh_ip":     "10.0.0.64",
		"available_0": "10.0.0.96",
		"available_1": "10.0.3.254",
	}
}

// TestReservedIPGuardReportsObsoleteKeys proves a complete-record write
// notices keys vault holds that the derivation no longer produces, and
// reports them without removing anything while force is unset.
func TestReservedIPGuardReportsObsoleteKeys(t *testing.T) {
	under := newFakeSafe()
	seedStaleBandRecord(under, testReservedPath)

	guard := newReservedIPGuard(under, false, logger.Get())

	err := setCompleteRecord(guard, testReservedPath, derivedBandRecord())
	require.NoError(t, err)

	assert.Equal(t,
		[]string{"available_a", "available_b", "reserved_c", "reserved_d"},
		obsoleteKeys(guard.Report()))

	// Reporting only: a status pass must not remove anything.
	for _, key := range []string{"available_a", "available_b", "reserved_c", "reserved_d"} {
		assert.Contains(t, under.data[testReservedPath], key,
			"key %q was removed without force", key)
	}
}

// TestReservedIPGuardForceDeletesObsoleteKeys proves the migrate path
// actually removes them, so Genesis' cloud-config IPAM stops unioning a
// retired band pair into the live one.
func TestReservedIPGuardForceDeletesObsoleteKeys(t *testing.T) {
	under := newFakeSafe()
	seedStaleBandRecord(under, testReservedPath)

	guard := newReservedIPGuard(under, true, logger.Get())

	err := setCompleteRecord(guard, testReservedPath, derivedBandRecord())
	require.NoError(t, err)

	assert.Equal(t,
		[]string{"available_a", "available_b", "reserved_c", "reserved_d"},
		obsoleteKeys(guard.Report()))

	for _, key := range []string{"available_a", "available_b", "reserved_c", "reserved_d"} {
		assert.NotContains(t, under.data[testReservedPath], key,
			"obsolete key %q survived migrate", key)
	}

	// The derived keys and the scheme stamp are untouched by the purge.
	assert.Equal(t, "10.0.0.64", under.data[testReservedPath]["bosh_ip"])
	assert.Equal(t, reservedIPSchemeVersion, under.data[testReservedPath][reservedIPSchemeKey])
}

// TestReservedIPGuardKeepsSchemeStamp proves the stamp the guard itself
// writes is never mistaken for an obsolete key, in either direction.
func TestReservedIPGuardKeepsSchemeStamp(t *testing.T) {
	under := newFakeSafe()
	under.data[testReservedPath] = map[string]interface{}{
		"bosh_ip":             "10.0.0.64",
		reservedIPSchemeKey:   reservedIPSchemeVersion,
		"available_0":         "10.0.0.96",
		"available_1":         "10.0.3.254",
		"unrelated_operator1": "kept",
	}

	guard := newReservedIPGuard(under, true, logger.Get())

	err := setCompleteRecord(guard, testReservedPath, derivedBandRecord())
	require.NoError(t, err)

	assert.Equal(t, []string{"unrelated_operator1"}, obsoleteKeys(guard.Report()))
	assert.Equal(t, reservedIPSchemeVersion, under.data[testReservedPath][reservedIPSchemeKey])
}

// TestReservedIPGuardPartialWriteReportsNoObsolete proves a plain
// SetMultiple is still treated as a partial write. The infra subnet's
// role-keyed record is assembled from whatever bootstrap state happens to
// hold, so keys absent from one write are not evidence of retirement —
// treating them as obsolete would delete live addresses.
func TestReservedIPGuardPartialWriteReportsNoObsolete(t *testing.T) {
	under := newFakeSafe()
	seedStaleBandRecord(under, testReservedPath)

	guard := newReservedIPGuard(under, true, logger.Get())

	err := guard.SetMultiple(testReservedPath, derivedBandRecord())
	require.NoError(t, err)

	assert.Empty(t, guard.Report().Obsoletes)
	assert.Contains(t, under.data[testReservedPath], "available_a")
}

// TestReservedIPGuardSubPathReportsNoObsolete proves a per-role sub-path
// (.../reserved-ips/bastion:ip) is never purged: it is not a record, it
// holds one address, and a complete-record write never targets it.
func TestReservedIPGuardSubPathReportsNoObsolete(t *testing.T) {
	const rolePath = testReservedPath + "/bastion"

	under := newFakeSafe()
	under.data[rolePath] = map[string]interface{}{
		"ip":     "10.0.0.3",
		"legacy": "10.0.0.3",
	}

	guard := newReservedIPGuard(under, true, logger.Get())

	err := setCompleteRecord(guard, rolePath, map[string]interface{}{"ip": "10.0.0.3"})
	require.NoError(t, err)

	assert.Empty(t, guard.Report().Obsoletes)
	assert.Contains(t, under.data[rolePath], "legacy")
}

// TestReservedIPGuardObsoleteDeleteErrorPropagates proves a failed purge is
// an error rather than a silently incomplete migrate — the operator would
// otherwise be told the record was cleaned when the stale pair is still
// there for Genesis to union.
func TestReservedIPGuardObsoleteDeleteErrorPropagates(t *testing.T) {
	under := &deleteFailingSafe{fakeSafe: newFakeSafe()}
	seedStaleBandRecord(under.fakeSafe, testReservedPath)

	guard := newReservedIPGuard(under, true, logger.Get())

	err := setCompleteRecord(guard, testReservedPath, derivedBandRecord())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "available_a")
}

// deleteFailingSafe makes Delete fail so the purge's error path is covered.
type deleteFailingSafe struct {
	*fakeSafe
}

func (d *deleteFailingSafe) Delete(_, _ string) error {
	return ErrSecretNotFound
}

// TestReservedIPReportRenderingObsolete proves the obsolete section reaches
// the operator, naming both the key and the command that removes it.
func TestReservedIPReportRenderingObsolete(t *testing.T) {
	report := ReservedIPReport{
		Drifts:  nil,
		Schemes: nil,
		Obsoletes: []ReservedIPObsolete{
			{Path: testReservedPath, Key: "reserved_c", Existing: "10.0.0.62"},
		},
	}

	var sb strings.Builder

	WriteReservedIPReport(&sb, report)

	out := sb.String()
	assert.Contains(t, out, "reserved_c")
	assert.Contains(t, out, "10.0.0.62")
	assert.Contains(t, out, "reserved-ips migrate")
}

// TestReservedIPGuardDryRunNeverDeletes proves a forced dry-run still
// removes nothing: the guard sits above the recording safe, whose Delete is
// a deliberate no-op, so the plan can show a purge that has not happened.
func TestReservedIPGuardDryRunNeverDeletes(t *testing.T) {
	under := newFakeSafe()
	seedStaleBandRecord(under, testReservedPath)

	guard := newReservedIPGuard(newRecordingSafe(under), true, logger.Get())

	err := setCompleteRecord(guard, testReservedPath, derivedBandRecord())
	require.NoError(t, err)

	assert.NotEmpty(t, guard.Report().Obsoletes, "a dry run must still report them")

	for _, key := range []string{"available_a", "available_b", "reserved_c", "reserved_d"} {
		assert.Contains(t, under.data[testReservedPath], key,
			"a dry run removed %q from the live safe", key)
	}
}

// staleBandKeys are the band keys an earlier build wrote alongside the
// numeric ones. They are the state outputs internal/bootstrap emits
// (available_a/b, reserved_a..d); an older ocfp propagated them verbatim
// into the tier-specific vault records, where they have no meaning.
var staleBandKeys = []string{"available_a", "available_b", "reserved_c", "reserved_d"}

// reservedIPRecordPaths returns every reserved-ips record the fake safe
// holds — the workload records the derivation writes, not their per-role
// sub-paths.
func reservedIPRecordPaths(safe *fakeSafe) []string {
	paths := make([]string, 0, len(safe.data))

	for path := range safe.data {
		if isReservedIPRecordPath(path) {
			paths = append(paths, path)
		}
	}

	return paths
}

// TestReservedIPsMigratePurgesObsoleteBandKeys is the regression test for
// the field failure: a record carrying BOTH key families reads clean to
// `status` and survives `migrate`, while Genesis' cloud-config IPAM unions
// every reserved*/available* pair it finds — so one retired pair spanning
// the live band reserves the whole thing, and the director fails to
// generate a cloud-config with "Not enough available IPs in the subnet for
// the network 'compilation'".
//
// It drives the real migrate/status entry point rather than the guard, so
// the wiring from provider write site through guard to report is covered.
func TestReservedIPsMigratePurgesObsoleteBandKeys(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	safe := newFakeSafe()
	mgr := newReservedIPsScopeTestManager(pveScopeTestConfig(), safe)

	// Provision the records this build derives, then age them: inject the
	// key family the older build also wrote.
	_, err := mgr.reservedIPs(&ReservedIPOptions{Apply: true}) //nolint:exhaustruct
	require.NoError(t, err)

	records := reservedIPRecordPaths(safe)
	require.NotEmpty(t, records, "expected the derivation to write reserved-ips records")

	for _, path := range records {
		safe.data[path]["available_a"] = "10.64.64.12"
		safe.data[path]["available_b"] = "10.64.64.29"
		safe.data[path]["reserved_c"] = "10.64.64.30"
		safe.data[path]["reserved_d"] = "10.64.67.254"
	}

	// A status pass must SEE them. Before this change it reported the
	// records clean, because it only ever compared the keys the derivation
	// produces.
	status, err := mgr.reservedIPs(&ReservedIPOptions{Apply: false}) //nolint:exhaustruct
	require.NoError(t, err)
	assert.Len(t, status.Obsoletes, len(records)*len(staleBandKeys))

	for _, path := range records {
		for _, key := range staleBandKeys {
			assert.Contains(t, safe.data[path], key, "a status pass must not remove %q", key)
		}
	}

	// Migrating must remove them.
	applied, err := mgr.reservedIPs(&ReservedIPOptions{Apply: true}) //nolint:exhaustruct
	require.NoError(t, err)
	assert.Len(t, applied.Obsoletes, len(records)*len(staleBandKeys))

	for _, path := range records {
		for _, key := range staleBandKeys {
			assert.NotContains(t, safe.data[path], key, "%s:%s survived migrate", path, key)
		}

		assert.Contains(t, safe.data[path], "available_0", "the live band was purged too")
	}

	// And the record is then clean: a second status reports nothing.
	after, err := mgr.reservedIPs(&ReservedIPOptions{Apply: false}) //nolint:exhaustruct
	require.NoError(t, err)
	assert.Empty(t, after.Obsoletes)
	assert.Empty(t, after.Drifts)
}
