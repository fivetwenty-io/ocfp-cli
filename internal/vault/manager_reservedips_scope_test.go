package vault

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// newReservedIPsScopeTestManager builds a Manager wired to safe, with no
// live vault client. The dispatch under test (populate/populateReservedIPsPhase
// and reservedIPs) never touches m.client, so this is sufficient to drive
// it without a real vault connection.
func newReservedIPsScopeTestManager(cfg *config.Config, safe SafeInterface) *Manager {
	return &Manager{ //nolint:exhaustruct // client/startTime are irrelevant to write-scoping
		safe:     safe,
		config:   cfg,
		blocName: "test-bloc",
		logger:   zap.NewNop().Sugar(),
	}
}

// pveScopeTestConfig returns a minimal PVE config valid for reserved-ip
// derivation (resolveLayout/pveReservedIPsForSubnet need a real CIDR).
func pveScopeTestConfig() *config.Config {
	return &config.Config{ //nolint:exhaustruct // only the fields reserved-ip derivation reads are needed
		Provider: "pve",
		Network:  config.NetworkConfig{CIDR: "10.64.64.0/19"}, //nolint:exhaustruct
	}
}

// assertOnlyReservedIPPaths fails the test if written contains any path
// that is not a reserved-ips record or one of its per-role sub-paths, per
// isReservedIPPath (the same predicate the guard itself applies).
func assertOnlyReservedIPPaths(t *testing.T, written []string) {
	t.Helper()

	require.NotEmpty(t, written, "expected at least one path to be written")

	for _, path := range written {
		assert.True(t, isReservedIPPath(path), "path %q is not a reserved-ips path", path)
	}
}

// TestPopulate_PhaseReservedIPs_WritesOnlyReservedIPsPaths drives populate
// through the reserved-ips phase (no bootstrap state present, so the
// stateless-fallback writer runs) against a fake vault backend, and asserts
// every path it wrote is a reserved-ips path -- never the full config tree.
func TestPopulate_PhaseReservedIPs_WritesOnlyReservedIPsPaths(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	safe := newFakeSafe()
	mgr := newReservedIPsScopeTestManager(pveScopeTestConfig(), safe)

	opts := &PopulateOptions{ //nolint:exhaustruct // ForceReallocate/reporter default is what's under test
		Subcommand: PhaseReservedIPs,
	}

	err := mgr.populate(opts)
	require.NoError(t, err)

	written := make([]string, 0, len(safe.data))
	for path := range safe.data {
		written = append(written, path)
	}

	assertOnlyReservedIPPaths(t, written)
}

// TestPopulate_PhaseReservedIPs_StateDriven_WritesOnlyReservedIPsPaths seeds
// bootstrap state with one workload subnet and one infra subnet, so the
// state-driven writers (writeTieredReservedIPs and writeReservedIPs) run
// instead of the stateless fallback the other tests in this file exercise,
// and asserts they are scoped identically: every written path is a
// reserved-ips path.
func TestPopulate_PhaseReservedIPs_StateDriven_WritesOnlyReservedIPsPaths(t *testing.T) {
	const blocName = "test-bloc"

	sm := seedPVEState(t, blocName)

	require.NoError(t, sm.AddResource(&state.Resource{ //nolint:exhaustruct // only the fields configureReservedIPsForEnv reads are needed
		ID:         "subnet-infra",
		Type:       "subnet",
		Name:       blocName + "-infra",
		Properties: map[string]any{"cidr": "10.64.64.0/22"},
	}))
	require.NoError(t, sm.AddResource(&state.Resource{ //nolint:exhaustruct
		ID:         "subnet-ocfp-0",
		Type:       "subnet",
		Name:       blocName + "-ocfp-0",
		Properties: map[string]any{"cidr": "10.64.68.0/22"},
	}))
	require.NoError(t, sm.SetOutput("reserved_"+blocName+"-infra_bastion_ip", "10.64.64.3"))
	require.NoError(t, sm.Save())

	safe := newFakeSafe()
	mgr := newReservedIPsScopeTestManager(pveScopeTestConfig(), safe)

	opts := &PopulateOptions{Subcommand: PhaseReservedIPs} //nolint:exhaustruct
	require.NoError(t, mgr.populate(opts))

	written := make([]string, 0, len(safe.data))
	for path := range safe.data {
		written = append(written, path)
	}

	assertOnlyReservedIPPaths(t, written)

	// Prove the state-driven writers actually ran rather than the fallback:
	// the infra subnet's per-role sub-path is only ever written from state
	// outputs, and the workload record's bosh_ip derives from the seeded
	// subnet's own /22 (base + 4 on the mgmt tier), not the fallback carve
	// of the config CIDR.
	var sawInfraBastion, sawSeededWorkload bool

	for path, keys := range safe.data {
		if strings.HasSuffix(path, "/subnets/infra/reserved-ips/bastion") {
			sawInfraBastion = true
		}

		if strings.HasSuffix(path, "/subnets/ocfp-0/reserved-ips") && keys["bosh_ip"] == "10.64.68.4" {
			sawSeededWorkload = true
		}
	}

	assert.True(t, sawInfraBastion, "state-driven infra role writer did not run")
	assert.True(t, sawSeededWorkload, "state-driven tiered workload writer did not run")
}

// TestPopulate_PhaseReservedIPs_NonPVEProvider_ReturnsNamedError proves a
// non-PVE provider attempting the reserved-ips populate phase gets a clear
// named error rather than a silent no-op or a full-tree write.
func TestPopulate_PhaseReservedIPs_NonPVEProvider_ReturnsNamedError(t *testing.T) {
	safe := newFakeSafe()

	cfg := &config.Config{ //nolint:exhaustruct
		Provider: "aws",
		Region:   "us-east-1",
	}
	mgr := newReservedIPsScopeTestManager(cfg, safe)

	opts := &PopulateOptions{Subcommand: PhaseReservedIPs} //nolint:exhaustruct

	err := mgr.populate(opts)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrReservedIPsRequiresPVE))
	assert.Empty(t, safe.data, "non-PVE provider must not write anything")
}

// TestPopulateDryRun_PhaseReservedIPs_WritesOnlyReservedIPsPaths covers the
// dry-run switch case added alongside the real one: the recorded plan must
// contain only reserved-ips paths too.
func TestPopulateDryRun_PhaseReservedIPs_WritesOnlyReservedIPsPaths(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	safe := newFakeSafe()
	mgr := newReservedIPsScopeTestManager(pveScopeTestConfig(), safe)

	opts := &PopulateOptions{Subcommand: PhaseReservedIPs} //nolint:exhaustruct

	var buf bytes.Buffer

	err := mgr.populateDryRun(opts, safe, "https://vault.example.test:8200", &buf)
	require.NoError(t, err)

	assert.Empty(t, safe.data, "dry run must not write the live safe")
}

// TestReservedIPs_UsesScopedWrite_OnPVE drives the migrate path (Apply:
// true) directly against the live safe and asserts every path it wrote is a
// reserved-ips path -- proving it no longer goes through provider.Configure
// (the full-tree writer every other phase in this file uses).
func TestReservedIPs_UsesScopedWrite_OnPVE(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	safe := newFakeSafe()
	mgr := newReservedIPsScopeTestManager(pveScopeTestConfig(), safe)

	report, err := mgr.reservedIPs(&ReservedIPOptions{Apply: true}) //nolint:exhaustruct
	require.NoError(t, err)
	assert.Empty(t, report.Drifts, "first write against an empty vault must not report drift")

	written := make([]string, 0, len(safe.data))
	for path := range safe.data {
		written = append(written, path)
	}

	assertOnlyReservedIPPaths(t, written)
}

// TestReservedIPs_StatusPass_WritesNothingToLiveSafe covers the Apply=false
// (status) path: derivation runs against a recording safe, so the live safe
// under it must stay untouched regardless of write scoping.
func TestReservedIPs_StatusPass_WritesNothingToLiveSafe(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	safe := newFakeSafe()
	mgr := newReservedIPsScopeTestManager(pveScopeTestConfig(), safe)

	_, err := mgr.reservedIPs(&ReservedIPOptions{Apply: false}) //nolint:exhaustruct
	require.NoError(t, err)

	assert.Empty(t, safe.data, "a status pass must not write the live safe")
}

// TestReservedIPs_NonPVEProvider_ReturnsNamedError proves the migrate/status
// entry point gets the same named error a non-PVE provider gets from
// populate, rather than the old behavior of running provider.Configure's
// full-tree write against whatever provider is configured.
func TestReservedIPs_NonPVEProvider_ReturnsNamedError(t *testing.T) {
	safe := newFakeSafe()

	cfg := &config.Config{ //nolint:exhaustruct
		Provider: "gcp",
		Region:   "us-central1",
	}
	mgr := newReservedIPsScopeTestManager(cfg, safe)

	_, err := mgr.reservedIPs(&ReservedIPOptions{Apply: true}) //nolint:exhaustruct
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrReservedIPsRequiresPVE))
	assert.Empty(t, safe.data, "non-PVE provider must not write anything")
}
