package commands

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// operatorTarget is a target of the operator's own, current before a run.
const operatorTarget = "ops"

// stoppedRaftVault returns a bloc whose raft vault is stopped with both
// keys saved, which vault start and vault inception restart in place.
func stoppedRaftVault(t *testing.T) map[string]string {
	t.Helper()

	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	return paths
}

// Every vault start, the boot unit's included, registers the bloc's target
// again and makes it current. The restart then puts back the target that
// was current before it. With no target current before, or with the bloc's
// own target current, the bloc's target stays current.
func TestVaultStart_PutsBackTheSafeTargetThatWasCurrent(t *testing.T) {
	for name, tc := range map[string]struct {
		before string
		want   func(paths map[string]string) string
		sets   []string
	}{
		"another target was current": {
			before: operatorTarget,
			want:   func(map[string]string) string { return operatorTarget },
			sets:   []string{operatorTarget},
		},
		"no target was current": {
			want: func(paths map[string]string) string { return paths["vaultName"] },
		},
		"the bloc's target was current": {
			before: "ocfp-lab-drgao-inception",
			want:   func(paths map[string]string) string { return paths["vaultName"] },
		},
	} {
		t.Run(name, func(t *testing.T) {
			paths := stoppedRaftVault(t)
			require.Equal(t, "ocfp-lab-drgao-inception", paths["vaultName"])

			fake := &fakeInception{probe: stoppedProbe(), safeCurrent: tc.before}

			require.NoError(t, runVaultStartWith(t, paths, fake))
			assert.Contains(t, fake.calls, "finish restart", "the bloc's target is registered again")
			assert.Equal(t, tc.want(paths), fake.safeCurrent)
			assert.Equal(t, tc.sets, fake.currentSets)
		})
	}
}

// The vault is up once the restart has finished, so a target that cannot be
// put back is only a warning, and vault start still succeeds.
func TestVaultStart_TargetThatCannotBePutBackOnlyWarns(t *testing.T) {
	paths := stoppedRaftVault(t)
	fake := &fakeInception{
		probe: stoppedProbe(), safeCurrent: operatorTarget, setCurrentErr: errors.New("~/.saferc is read-only"),
	}

	core, logs := observer.New(zapcore.WarnLevel)

	err := restartInceptionVaultInPlace(context.Background(), &vaultStartRun{
		paths: paths, steps: fake.startSteps(), log: zap.New(core).Sugar(),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{operatorTarget}, fake.currentSets)
	assert.Equal(t, paths["vaultName"], fake.safeCurrent)

	warnings := logs.FilterMessageSnippet("current target").All()
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0].Message, "safe target "+operatorTarget)
}

// When safe cannot say which target was current, there is nothing to put
// back, and the bloc's target stays current as it always did.
func TestVaultStart_UnknownEarlierTargetLeavesTheBlocTargetCurrent(t *testing.T) {
	paths := stoppedRaftVault(t)
	fake := &fakeInception{probe: stoppedProbe(), safeCurrentErr: errors.New("safe failed")}

	require.NoError(t, runVaultStartWith(t, paths, fake))
	assert.Empty(t, fake.currentSets)
	assert.Equal(t, paths["vaultName"], fake.safeCurrent)
}

// A vault that vault start leaves running is never targeted, so its run
// changes nothing about safe's current target.
func TestVaultStart_HealthyVaultLeavesTheCurrentTargetAlone(t *testing.T) {
	paths := stoppedRaftVault(t)
	fake := &fakeInception{probe: healthyRaftProbe(), session: true, safeCurrent: operatorTarget}

	require.NoError(t, runVaultStartWith(t, paths, fake))
	assert.Empty(t, fake.currentSets)
	assert.Equal(t, operatorTarget, fake.safeCurrent)
}

// A restart that fails after safe local moved the current target still
// puts back the target that was current before.
func TestVaultStart_FailedRestartStillPutsBackTheTarget(t *testing.T) {
	paths := stoppedRaftVault(t)
	fake := &fakeInception{probe: stoppedProbe(), safeCurrent: operatorTarget, startErrs: []error{ErrVaultNotReady}}

	err := runVaultStartWith(t, paths, fake)
	require.ErrorIs(t, err, ErrVaultNotReady)
	assert.Equal(t, operatorTarget, fake.safeCurrent)
}

// migrate-storage restarts the vault through vault start's restart, so it
// puts back the earlier target the same way.
func TestMigrateStorage_PutsBackTheSafeTargetThatWasCurrent(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{probe: stoppedProbe(), migrate: fakeMigration(t), safeCurrent: operatorTarget}

	var out bytes.Buffer

	err := migrateInceptionVaultStorage(context.Background(),
		newMigrateStorageRun(paths, fake.migrateStorageSteps(nil), &out))
	require.NoError(t, err)
	assert.Contains(t, fake.calls, "finish restart")
	assert.Equal(t, operatorTarget, fake.safeCurrent)
	assert.Equal(t, []string{operatorTarget}, fake.currentSets)
}

// vault inception follows the same rule whenever it brings back the bloc's
// existing vault, whether it restarts the vault or only registers its
// target again. A new vault that it creates stays current, as it always did,
// and a new vault that fails to start leaves the earlier target current.
func TestReconcile_PutsBackTheSafeTargetUnlessItCreatedAVault(t *testing.T) {
	for name, tc := range map[string]struct {
		setup     func(t *testing.T, paths map[string]string)
		fake      fakeInception
		wantCall  string
		wantErr   error
		keepsBloc bool
	}{
		"restart in place": {
			setup: func(t *testing.T, paths map[string]string) {
				writeRaftData(t, paths["vaultDir"])
				writeKeys(t, paths, true, true)
			},
			fake:     fakeInception{probe: stoppedProbe()},
			wantCall: "start restart",
		},
		"healthy vault registered again": {
			setup: func(t *testing.T, paths map[string]string) {
				writeRaftData(t, paths["vaultDir"])
				writeKeys(t, paths, true, true)
			},
			fake:     fakeInception{probe: healthyRaftProbe(), session: true},
			wantCall: "retarget",
		},
		"new vault": {
			setup:     func(*testing.T, map[string]string) {},
			fake:      fakeInception{probe: stoppedProbe()},
			wantCall:  "start fresh",
			keepsBloc: true,
		},
		"new vault after archiving leftover keys": {
			setup:     func(t *testing.T, paths map[string]string) { writeKeys(t, paths, true, true) },
			fake:      fakeInception{probe: stoppedProbe()},
			wantCall:  "start fresh",
			keepsBloc: true,
		},
		"new vault that fails to start": {
			setup:    func(*testing.T, map[string]string) {},
			fake:     fakeInception{probe: stoppedProbe(), startErrs: []error{ErrVaultNotReady}},
			wantCall: "start fresh",
			wantErr:  ErrVaultNotReady,
		},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			tc.setup(t, paths)

			fake := tc.fake
			fake.safeCurrent = operatorTarget

			err := runReconcile(t, paths, &fake)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}

			assert.Contains(t, fake.calls, tc.wantCall)

			if tc.keepsBloc {
				assert.Equal(t, paths["vaultName"], fake.safeCurrent, "a new vault stays current")
				assert.Empty(t, fake.currentSets)

				return
			}

			assert.Equal(t, operatorTarget, fake.safeCurrent)
			assert.Equal(t, []string{operatorTarget}, fake.currentSets)
		})
	}
}

// The real steps read the current target with 'safe target --json' and put
// one back with 'safe target <name>', through the safe ocfp validated.
func TestSafeCurrentTargetSteps_UseSafeTarget(t *testing.T) {
	fake := installFakeVaultOps(t)
	tools := testTools()
	read := tools.safe + " target --json"

	fake.outputs[read] = []string{
		`{"name": "ops", "url": "https://vault.example:8200", "verify": true, "strongbox": false}`,
		`{"name": "", "url": "", "verify": false, "strongbox": false}`,
		`not json`,
	}

	steps := newVaultStartSteps(tools).safeTarget

	current, err := steps.current(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ops", current)

	current, err = steps.current(context.Background())
	require.NoError(t, err)
	assert.Empty(t, current, "no target is current")

	_, err = steps.current(context.Background())
	require.Error(t, err)

	fake.errs[read] = errors.New("exit status 1")
	_, err = steps.current(context.Background())
	require.Error(t, err)

	require.NoError(t, steps.setCurrent(context.Background(), "ops"))
	assert.Contains(t, fake.commands, tools.safe+" target ops")

	fake.errs[tools.safe+" target ops"] = errors.New("exit status 1")
	require.Error(t, steps.setCurrent(context.Background(), "ops"))

	reconcileSteps := newInceptionSteps(tools).safeTarget
	_, _ = reconcileSteps.current(context.Background())
	assert.Equal(t, read, fake.commands[len(fake.commands)-1], "vault inception reads the target the same way")
}

// SAFE_TARGET overrides the current target for one safe command, so a
// remembered target read through it would be the override rather than the
// one in ~/.saferc, and a put-back would act on it too. Both commands run
// without it.
func TestSafeCurrentTargetSteps_IgnoreSafeTargetInTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	safe := filepath.Join(dir, "safe")
	script := `#!/bin/sh
if [ "$1" = target ] && [ "$2" = --json ]; then
  printf '{"name": "%s"}\n' "${SAFE_TARGET:-ops}"
  exit 0
fi
printf '%s' "${SAFE_TARGET:-unset}" > "$(dirname "$0")/set-env"
`
	require.NoError(t, os.WriteFile(safe, []byte(script), 0o700)) // #nosec G306 -- the fake safe must be executable
	t.Setenv("SAFE_TARGET", "override")

	steps := newSafeCurrentTargetSteps(safe)

	current, err := steps.current(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ops", current)

	require.NoError(t, steps.setCurrent(context.Background(), "ops"))

	seen, err := os.ReadFile(filepath.Join(dir, "set-env")) // #nosec G304 -- a file in the test's own temp dir
	require.NoError(t, err)
	assert.Equal(t, "unset", string(seen))
}

// When no target was current before the run, the bloc's target stays
// current, and the run says so rather than leaving the operator to find out.
func TestVaultStart_NoEarlierTargetSaysTheBlocTargetIsCurrent(t *testing.T) {
	paths := stoppedRaftVault(t)
	fake := &fakeInception{probe: stoppedProbe()}

	core, logs := observer.New(zapcore.InfoLevel)

	err := restartInceptionVaultInPlace(context.Background(), &vaultStartRun{
		paths: paths, steps: fake.startSteps(), log: zap.New(core).Sugar(),
	})
	require.NoError(t, err)
	assert.Empty(t, fake.currentSets)

	notes := logs.FilterMessageSnippet("No safe target was current").All()
	require.Len(t, notes, 1)
	assert.Equal(t, zapcore.InfoLevel, notes[0].Level)
	assert.Contains(t, notes[0].Message, paths["vaultName"])
}

// Both commands that put the earlier target back say so in their help.
func TestVaultHelp_DescribesPuttingBackTheCurrentTarget(t *testing.T) {
	for name, cmd := range map[string]string{
		"inception": newVaultInceptionCmd().Long,
		"start":     newVaultStartCmd().Long,
	} {
		long := strings.Join(strings.Fields(cmd), " ")
		assert.Contains(t, long, "puts back whichever target was current before", name)
	}
}
