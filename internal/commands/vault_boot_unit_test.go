package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/ocfp/ocfp-cli-go/internal/vaultboot"
)

const (
	isEnabledLine = "systemctl is-enabled ocfp-vault@ocfp-lab-drgao.service"
	disableLine   = "sudo -n systemctl disable ocfp-vault@ocfp-lab-drgao.service"
)

func TestDisableVaultBootUnit_DisablesAnEnabledUnit(t *testing.T) {
	for _, state := range []string{"enabled", "enabled-runtime"} {
		t.Run(state, func(t *testing.T) {
			ops := installFakeVaultOps(t)
			ops.outputs[isEnabledLine] = []string{state + "\n"}

			require.NoError(t, disableVaultBootUnit(context.Background(), "ocfp-lab-drgao", zap.NewNop().Sugar()))
			assert.Equal(t, []string{isEnabledLine, disableLine}, ops.commands)
		})
	}
}

// Disabling must not stop the unit. Its cgroup is where the vault's tmux
// server was started, and the teardown's own stop is what ends the vault.
func TestDisableVaultBootUnit_NeverStopsTheUnit(t *testing.T) {
	ops := installFakeVaultOps(t)
	ops.outputs[isEnabledLine] = []string{"enabled\n"}

	require.NoError(t, disableVaultBootUnit(context.Background(), "ocfp-lab-drgao", zap.NewNop().Sugar()))

	for _, line := range ops.commands {
		assert.NotContains(t, line, "--now")
		assert.NotContains(t, line, " stop ")
	}
}

// A unit that is not enabled, or a host without systemd, such as a
// workstation running macOS, has nothing to disable.
func TestDisableVaultBootUnit_LeavesAnythingElseAlone(t *testing.T) {
	for name, tc := range map[string]struct {
		output string
		err    error
	}{
		"disabled":      {output: "disabled\n", err: errors.New("exit status 1")},
		"not found":     {output: "not-found\n", err: errors.New("exit status 4")},
		"no systemctl":  {err: errors.New(`exec: "systemctl": executable file not found in $PATH`)},
		"empty answer":  {},
		"masked":        {output: "masked\n", err: errors.New("exit status 1")},
		"static answer": {output: "static\n"},
	} {
		t.Run(name, func(t *testing.T) {
			ops := installFakeVaultOps(t)
			if tc.output != "" {
				ops.outputs[isEnabledLine] = []string{tc.output}
			}

			if tc.err != nil {
				ops.errs[isEnabledLine] = tc.err
			}

			require.NoError(t, disableVaultBootUnit(context.Background(), "ocfp-lab-drgao", zap.NewNop().Sugar()))
			assert.Equal(t, []string{isEnabledLine}, ops.commands)
		})
	}
}

func TestDisableVaultBootUnit_ReportsAFailedDisable(t *testing.T) {
	ops := installFakeVaultOps(t)
	ops.outputs[isEnabledLine] = []string{"enabled\n"}
	ops.errs[disableLine] = errors.New("sudo failed: exit status 1")

	err := disableVaultBootUnit(context.Background(), "ocfp-lab-drgao", zap.NewNop().Sugar())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sudo systemctl disable ocfp-vault@ocfp-lab-drgao.service",
		"the error says how to disable the unit by hand")
}

// A vault with no bloc has no unit, because the unit is per bloc.
func TestDisableVaultBootUnit_SkipsAVaultWithNoBloc(t *testing.T) {
	ops := installFakeVaultOps(t)

	require.NoError(t, disableVaultBootUnit(context.Background(), "", zap.NewNop().Sugar()))
	assert.Empty(t, ops.commands)
}

func TestDisableVaultBootUnit_RejectsABlocTheUnitCannotName(t *testing.T) {
	ops := installFakeVaultOps(t)

	err := disableVaultBootUnit(context.Background(), "bad/bloc", zap.NewNop().Sugar())
	require.ErrorIs(t, err, vaultboot.ErrInvalidBloc)
	assert.Empty(t, ops.commands)
}

// The real teardown steps disable the bloc's own unit.
func TestNewTeardownSteps_DisablesTheBlocsBootUnit(t *testing.T) {
	ops := installFakeVaultOps(t)
	ops.outputs[isEnabledLine] = []string{"enabled\n"}

	steps := newTeardownSteps("ocfp-lab-drgao")
	require.NotNil(t, steps.disableBootUnit)
	require.NoError(t, steps.disableBootUnit(context.Background(), zap.NewNop().Sugar()))
	assert.Equal(t, []string{isEnabledLine, disableLine}, ops.commands)
	assert.True(t, strings.HasPrefix(ops.commands[1], "sudo -n "), "a sudo that wants a password fails instead of hanging")
}

// Test mode keeps its vault apart from the bloc's real one, so a teardown in
// test mode must not disable the real vault's boot unit.
func TestTeardownSteps_TestModeLeavesTheBootUnitAlone(t *testing.T) {
	for name, tc := range map[string]struct {
		testMode bool
		want     []string
	}{
		"test mode":   {testMode: true, want: nil},
		"normal mode": {testMode: false, want: []string{isEnabledLine, disableLine}},
	} {
		t.Run(name, func(t *testing.T) {
			ops := installFakeVaultOps(t)
			ops.outputs[isEnabledLine] = []string{"enabled\n"}

			steps := newTeardownSteps(teardownBootUnitBloc("ocfp-lab-drgao", tc.testMode))
			require.NoError(t, steps.disableBootUnit(context.Background(), zap.NewNop().Sugar()))
			assert.Equal(t, tc.want, ops.commands)
		})
	}
}

// Bastion init asks the installed binary whether it has 'vault start' with
// the command vaultboot builds, so that command must name the hidden flag
// vault start really has.
func TestSupportCheckCommand_NamesVaultStartsOwnFlag(t *testing.T) {
	t.Parallel()

	fields := strings.Fields(vaultboot.SupportCheckCommand("/usr/local/bin/ocfp"))
	require.Equal(t, []string{"/usr/local/bin/ocfp", "vault", "start", "--" + vaultStartCheckSupportFlag}, fields)

	start, _, err := NewVaultCmd().Find(fields[2:3])
	require.NoError(t, err)
	assert.NotNil(t, start.Flags().Lookup(vaultStartCheckSupportFlag))
}
