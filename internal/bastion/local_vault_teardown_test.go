package bastion

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// local_vault_teardown phase registration.
//
// The workstation-local inception vault (started by `ocfp bootstrap`) must
// be decommissioned once the bastion-side inception vault has taken over.
// `ocfp vault migrate` runs on the bastion and can never reach the
// workstation's tmux server, so init bastion — the process that actually
// runs on the workstation — is the only place this teardown can happen.
// ---------------------------------------------------------------------------

// TestPhaseLists_ContainLocalVaultTeardown pins the phase into both
// remote-mode phase lists so a future edit that drops it from either list
// fails immediately, independent of the generic parity test.
func TestPhaseLists_ContainLocalVaultTeardown(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))

	assert.True(t, sequentialPhaseNameSet(m)["local_vault_teardown"],
		"local_vault_teardown missing from sequential phase list")
	assert.True(t, parallelModePhaseNameSet(m)["local_vault_teardown"],
		"local_vault_teardown missing from parallel-mode phase lists")
}

// TestPhaseLists_LocalVaultTeardownRunsLast pins the gating that makes the
// teardown safe: it must run after health_check, so reaching it implies the
// bastion-side inception vault was set up, populated, and verified. Killing
// the workstation vault any earlier could destroy the only running copy.
func TestPhaseLists_LocalVaultTeardownRunsLast(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))

	assertLast := func(t *testing.T, names []string, listLabel string) {
		t.Helper()

		healthCheckIdx := indexOf(names, "health_check")
		teardownIdx := indexOf(names, "local_vault_teardown")

		require.GreaterOrEqualf(t, healthCheckIdx, 0, "%s: health_check not found", listLabel)
		require.GreaterOrEqualf(t, teardownIdx, 0, "%s: local_vault_teardown not found", listLabel)

		assert.Lessf(t, healthCheckIdx, teardownIdx,
			"%s: health_check (index %d) must run before local_vault_teardown (index %d)",
			listLabel, healthCheckIdx, teardownIdx)
	}

	assertLast(t, sequentialPhaseNameOrder(m), "sequential phase list")
	assertLast(t, parallelModePhaseNameOrder(m), "parallel-mode phase lists")
}

// TestLocalPhases_OmitLocalVaultTeardown guards the inverse: in
// LocalExecutor mode the process runs on the bastion itself, where the
// "local" inception vault is the live one that was just set up. Tearing it
// down there would destroy the vault the init just created.
func TestLocalPhases_OmitLocalVaultTeardown(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	le := &LocalExecutor{}

	for _, phase := range le.getLocalPhases(m) {
		assert.NotEqual(t, "local_vault_teardown", phase.name,
			"local_vault_teardown must not run in LocalExecutor mode (it would kill the bastion's own vault)")
	}
}
