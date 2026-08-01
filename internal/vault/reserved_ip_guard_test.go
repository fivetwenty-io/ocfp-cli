package vault

import (
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingSafe wraps fakeSafe to distinguish the two write classes the guard
// exists to separate: keys that did not previously exist (additive, always
// allowed) and keys whose value the write would change (mutating, blocked
// unless force is set).
type countingSafe struct {
	*fakeSafe

	adds      int
	mutations int
}

func newCountingSafe() *countingSafe {
	return &countingSafe{fakeSafe: newFakeSafe(), adds: 0, mutations: 0}
}

func (c *countingSafe) SetMultiple(path string, data map[string]interface{}) error {
	existing := c.fakeSafe.data[path]

	for key, value := range data {
		current, ok := existing[key]
		switch {
		case !ok:
			c.adds++
		case valueString(current) != valueString(value):
			c.mutations++
		}
	}

	return c.fakeSafe.SetMultiple(path, data)
}

func (c *countingSafe) Set(path, key string, value interface{}) error {
	return c.SetMultiple(path, map[string]interface{}{key: value})
}

const testReservedPath = "secret/config/bloc/ocf/net/subnets/ocfp-0/reserved-ips"

func TestReservedIPGuardWritesMissingKeys(t *testing.T) {
	under := newCountingSafe()
	guard := newReservedIPGuard(under, false, logger.Get())

	err := guard.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip":  "10.0.0.64",
		"vault_ip": "10.0.0.65",
	})
	require.NoError(t, err)

	assert.Equal(t, "10.0.0.64", under.data[testReservedPath]["bosh_ip"])
	assert.Equal(t, "10.0.0.65", under.data[testReservedPath]["vault_ip"])
	assert.Zero(t, under.mutations)
	assert.Empty(t, guard.Report().Drifts)
}

func TestReservedIPGuardSkipsUnchangedValues(t *testing.T) {
	under := newCountingSafe()
	require.NoError(t, under.fakeSafe.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip": "10.0.0.64",
	}))

	guard := newReservedIPGuard(under, false, logger.Get())
	require.NoError(t, guard.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip": "10.0.0.64",
	}))

	assert.Zero(t, under.mutations)
	assert.Empty(t, guard.Report().Drifts)
}

// TestReservedIPGuardReportsDriftWithoutWriting is the core regression for
// the 2026-07-28 incident: a bloc provisioned under the pre-tiering scheme
// must keep its live addresses, and the divergence must be reported.
func TestReservedIPGuardReportsDriftWithoutWriting(t *testing.T) {
	under := newCountingSafe()
	require.NoError(t, under.fakeSafe.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip":     "10.0.0.4",
		"director_ip": "10.0.0.4",
	}))

	guard := newReservedIPGuard(under, false, logger.Get())
	require.NoError(t, guard.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip":     "10.0.0.64",
		"director_ip": "10.0.0.64",
	}))

	assert.Equal(t, "10.0.0.4", under.data[testReservedPath]["bosh_ip"])
	assert.Equal(t, "10.0.0.4", under.data[testReservedPath]["director_ip"])
	assert.Zero(t, under.mutations)

	drifts := guard.Report().Drifts
	require.Len(t, drifts, 2)
	assert.True(t, guard.Report().HasDrift())

	// Sorted key order keeps the report deterministic.
	assert.Equal(t, "bosh_ip", drifts[0].Key)
	assert.Equal(t, "10.0.0.4", drifts[0].Existing)
	assert.Equal(t, "10.0.0.64", drifts[0].Derived)
	assert.Equal(t, testReservedPath, drifts[0].Path)
	assert.Equal(t, "director_ip", drifts[1].Key)
}

// TestReservedIPGuardStaysAdditiveAlongsideDrift covers the legitimate
// recurring need: a new role (doomsday, shout, rustfs, garage) must still
// reach a bloc whose existing roles have drifted.
func TestReservedIPGuardStaysAdditiveAlongsideDrift(t *testing.T) {
	under := newCountingSafe()
	require.NoError(t, under.fakeSafe.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip": "10.0.0.4",
	}))

	guard := newReservedIPGuard(under, false, logger.Get())
	require.NoError(t, guard.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip":  "10.0.0.64",
		"shout_ip": "10.0.0.19",
	}))

	assert.Equal(t, "10.0.0.4", under.data[testReservedPath]["bosh_ip"])
	assert.Equal(t, "10.0.0.19", under.data[testReservedPath]["shout_ip"])
	assert.Zero(t, under.mutations)
	assert.Len(t, guard.Report().Drifts, 1)
}

func TestReservedIPGuardForceReallocateWrites(t *testing.T) {
	under := newCountingSafe()
	require.NoError(t, under.fakeSafe.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip": "10.0.0.4",
	}))

	guard := newReservedIPGuard(under, true, logger.Get())
	require.NoError(t, guard.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip": "10.0.0.64",
	}))

	assert.Equal(t, "10.0.0.64", under.data[testReservedPath]["bosh_ip"])
	assert.Equal(t, 1, under.mutations)

	// Force applies the change but must still report what it moved.
	require.Len(t, guard.Report().Drifts, 1)
	assert.Equal(t, "10.0.0.4", guard.Report().Drifts[0].Existing)
}

func TestReservedIPGuardIgnoresNonReservedPaths(t *testing.T) {
	const fqdnPath = "secret/config/bloc/mgmt/fqdns"

	under := newCountingSafe()
	require.NoError(t, under.fakeSafe.SetMultiple(fqdnPath, map[string]interface{}{
		"concourse": "old.example.com",
	}))

	guard := newReservedIPGuard(under, false, logger.Get())
	require.NoError(t, guard.SetMultiple(fqdnPath, map[string]interface{}{
		"concourse": "new.example.com",
	}))

	assert.Equal(t, "new.example.com", under.data[fqdnPath]["concourse"])
	assert.Empty(t, guard.Report().Drifts)
}

// TestReservedIPGuardGuardsRoleSubPaths covers the per-role sub-path form
// (.../reserved-ips/<role>:ip) the PVE infra subnet writes.
func TestReservedIPGuardGuardsRoleSubPaths(t *testing.T) {
	const rolePath = "secret/config/bloc/mgmt/net/subnets/infra/reserved-ips/bosh"

	under := newCountingSafe()
	require.NoError(t, under.fakeSafe.SetMultiple(rolePath, map[string]interface{}{
		"ip": "10.0.0.4",
	}))

	guard := newReservedIPGuard(under, false, logger.Get())
	require.NoError(t, guard.Set(rolePath, "ip", "10.0.0.64"))

	assert.Equal(t, "10.0.0.4", under.data[rolePath]["ip"])
	assert.Len(t, guard.Report().Drifts, 1)

	// Role sub-paths carry a single address, not a scheme record.
	assert.NotContains(t, under.data[rolePath], reservedIPSchemeKey)
}

func TestReservedIPGuardStampsSchemeOnFreshWrite(t *testing.T) {
	under := newCountingSafe()
	guard := newReservedIPGuard(under, false, logger.Get())

	require.NoError(t, guard.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip": "10.0.0.64",
	}))

	assert.Equal(t, reservedIPSchemeVersion, under.data[testReservedPath][reservedIPSchemeKey])
	assert.Empty(t, guard.Report().Schemes)
}

// TestReservedIPGuardWithholdsStampOnDrift is what keeps the stamp honest:
// a bloc that disagrees with the current table must not be labelled as
// conforming to it.
func TestReservedIPGuardWithholdsStampOnDrift(t *testing.T) {
	under := newCountingSafe()
	require.NoError(t, under.fakeSafe.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip": "10.0.0.4",
	}))

	guard := newReservedIPGuard(under, false, logger.Get())
	require.NoError(t, guard.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip": "10.0.0.64",
	}))

	assert.NotContains(t, under.data[testReservedPath], reservedIPSchemeKey)

	schemes := guard.Report().Schemes
	require.Len(t, schemes, 1)
	assert.Equal(t, testReservedPath, schemes[0].Path)
	assert.Empty(t, schemes[0].Existing, "legacy blocs predate the stamp")
	assert.Equal(t, reservedIPSchemeVersion, schemes[0].Current)
}

// A pre-stamp bloc whose addresses already agree with the current table is
// simply brought up to date — that is not a migration.
func TestReservedIPGuardStampsAgreeingUnstampedPath(t *testing.T) {
	under := newCountingSafe()
	require.NoError(t, under.fakeSafe.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip": "10.0.0.64",
	}))

	guard := newReservedIPGuard(under, false, logger.Get())
	require.NoError(t, guard.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip": "10.0.0.64",
	}))

	assert.Equal(t, reservedIPSchemeVersion, under.data[testReservedPath][reservedIPSchemeKey])
	assert.Empty(t, guard.Report().Schemes)
}

func TestReservedIPGuardReportsSchemeMismatch(t *testing.T) {
	under := newCountingSafe()
	require.NoError(t, under.fakeSafe.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip":           "10.0.0.4",
		reservedIPSchemeKey: "1",
		"director_ip":       "10.0.0.4",
	}))

	guard := newReservedIPGuard(under, false, logger.Get())
	require.NoError(t, guard.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip":     "10.0.0.64",
		"director_ip": "10.0.0.64",
	}))

	assert.Equal(t, "1", under.data[testReservedPath][reservedIPSchemeKey])

	schemes := guard.Report().Schemes
	require.Len(t, schemes, 1)
	assert.Equal(t, "1", schemes[0].Existing)
}

func TestReservedIPGuardForceRestampsAfterMigration(t *testing.T) {
	under := newCountingSafe()
	require.NoError(t, under.fakeSafe.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip": "10.0.0.4",
	}))

	guard := newReservedIPGuard(under, true, logger.Get())
	require.NoError(t, guard.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip": "10.0.0.64",
	}))

	assert.Equal(t, reservedIPSchemeVersion, under.data[testReservedPath][reservedIPSchemeKey])
}

// TestReservedIPGuardWithSchemeStampsWideVersion covers a guard constructed
// with the wide layout's own scheme, matching how manager.go resolves it
// from the bloc's network config rather than assuming the package default.
func TestReservedIPGuardWithSchemeStampsWideVersion(t *testing.T) {
	wide, err := netlayout.Lookup("wide")
	require.NoError(t, err)

	under := newCountingSafe()
	guard := newReservedIPGuardWithScheme(under, false, wide.SchemeVersion(), logger.Get())

	require.NoError(t, guard.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip": "10.0.0.64",
	}))

	assert.Equal(t, "2", under.data[testReservedPath][reservedIPSchemeKey])
	assert.Empty(t, guard.Report().Schemes)
}

// TestReservedIPGuardWithSchemeStampsCompactVersion is the compact-strategy
// counterpart: a compact bloc's records must carry "3-compact", not wide's
// "2", so a later scheme comparison never mistakes one strategy's table for
// the other's.
func TestReservedIPGuardWithSchemeStampsCompactVersion(t *testing.T) {
	compact, err := netlayout.Lookup("compact")
	require.NoError(t, err)

	under := newCountingSafe()
	guard := newReservedIPGuardWithScheme(under, false, compact.SchemeVersion(), logger.Get())

	require.NoError(t, guard.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip": "10.0.0.64",
	}))

	assert.Equal(t, "3-compact", under.data[testReservedPath][reservedIPSchemeKey])
	assert.Empty(t, guard.Report().Schemes)
}

// A read failure must not be mistaken for "the path is empty" — that is the
// exact misreading that would let the guard overwrite a live address.
func TestReservedIPGuardFailsClosedOnReadError(t *testing.T) {
	under := newCountingSafe()
	under.fakeSafe.failOnRead = assert.AnError

	guard := newReservedIPGuard(under, false, logger.Get())
	err := guard.SetMultiple(testReservedPath, map[string]interface{}{
		"bosh_ip": "10.0.0.64",
	})

	require.Error(t, err)
	assert.Empty(t, under.data[testReservedPath])
}

func TestReservedIPReportRendering(t *testing.T) {
	report := ReservedIPReport{
		Drifts: []ReservedIPDrift{
			{Path: testReservedPath, Key: "bosh_ip", Existing: "10.0.0.4", Derived: "10.0.0.64"},
		},
		Schemes: []ReservedIPScheme{
			{Path: testReservedPath, Existing: "", Current: reservedIPSchemeVersion},
		},
	}

	var sb strings.Builder
	WriteReservedIPReport(&sb, report)

	out := sb.String()
	assert.Contains(t, out, "bosh_ip")
	assert.Contains(t, out, "10.0.0.4")
	assert.Contains(t, out, "10.0.0.64")
	assert.Contains(t, out, "--force-reallocate")
	assert.Contains(t, out, "unstamped")
}

func TestReservedIPReportRenderingEmpty(t *testing.T) {
	var sb strings.Builder
	WriteReservedIPReport(&sb, ReservedIPReport{Drifts: nil, Schemes: nil})

	assert.Empty(t, sb.String())
}
