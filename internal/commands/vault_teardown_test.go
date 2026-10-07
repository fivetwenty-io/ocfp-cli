package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// runInceptionTeardown tears the bloc's vault down with the probe, ownership check,
// and key recovery scripted by fake. The cleanup is the real one, run against
// faked stop commands, so a test sees exactly what teardown moves on disk.
// It returns the commands the stop ran.
func runInceptionTeardown(t *testing.T, paths map[string]string, fake *fakeInception, force bool) (*fakeVaultOps, error) {
	t.Helper()

	ops := installFakeVaultOps(t)

	// The cleanup reads the bloc's target from ~/.saferc, which must never
	// be the real one.
	t.Setenv("HOME", t.TempDir())

	scripted := fake.steps()
	steps := teardownSteps{
		probe:       scripted.probe,
		ownsVault:   scripted.ownsVault,
		recoverKeys: scripted.recoverKeys,
		cleanup: func(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error {
			fake.record("cleanup")

			return cleanupExistingVault(ctx, paths, log)
		},
	}

	return ops, tearDownInceptionVault(context.Background(), paths, steps, force, zap.NewNop().Sugar())
}

// An open vault, initialized and unsealed, can be reopened after it stops
// only with both keys. When they are not saved and cannot be recovered,
// teardown refuses before it keeps a token, stops, or archives anything, and
// the vault keeps running.
func TestTeardown_OpenVaultWithoutSavedKeysIsLeftRunning(t *testing.T) {
	for name, tc := range map[string]struct {
		root, unseal bool
	}{
		"no root token": {unseal: true},
		"no unseal key": {root: true},
		"neither key":   {},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeRaftData(t, paths["vaultDir"])
			writeKeys(t, paths, tc.root, tc.unseal)

			before := treeDigest(t, blocDir(paths))
			fake := &fakeInception{probe: healthyRaftProbe(), session: true}

			ops, err := runInceptionTeardown(t, paths, fake, false)
			require.ErrorIs(t, err, ErrInceptionKeysNotSaved)
			assert.Contains(t, err.Error(), "left running")
			assert.Contains(t, err.Error(), "--force")
			assert.NotContains(t, err.Error(), testSealKey)
			assert.NotContains(t, err.Error(), "SENTINEL")
			assert.Equal(t, []string{"probe", "recover-keys"}, fake.calls,
				"nothing may be stopped or archived while the keys are not saved")
			assert.Empty(t, ops.commands, "no stop command may run")
			assert.Equal(t, before, treeDigest(t, blocDir(paths)), "no token is kept and nothing is moved")
			assert.Empty(t, archivesOf(t, paths))
		})
	}
}

// A key that recovery saves from the running vault's log lets teardown go on
// to the stop and the archive, with the recovered key archived beside the
// data.
func TestTeardown_OpenVaultWhoseKeysAreRecoveredIsArchived(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, false)

	fake := &fakeInception{
		probe:   healthyRaftProbe(),
		session: true,
		recover: func(paths map[string]string) { writeKeys(t, paths, false, true) },
	}

	_, err := runInceptionTeardown(t, paths, fake, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"probe", "recover-keys", "cleanup"}, fake.calls)

	archives := archivesOf(t, paths)
	require.Len(t, archives, 1)
	assert.Equal(t, testSealKey+"\n", readKey(t, filepath.Join(archives[0], "unseal.keys")))
	assert.FileExists(t, filepath.Join(archives[0], "data", "vault.db"))
}

// Teardown archives an open vault whose keys are both saved without trying
// to recover anything, exactly as it did before it checked the keys.
func TestTeardown_OpenVaultWithSavedKeysIsArchived(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{probe: healthyRaftProbe(), session: true}

	_, err := runInceptionTeardown(t, paths, fake, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"probe", "cleanup"}, fake.calls)

	archives := archivesOf(t, paths)
	require.Len(t, archives, 1)
	assert.FileExists(t, filepath.Join(archives[0], "root.key"))
	assert.FileExists(t, filepath.Join(archives[0], "unseal.keys"))
}

// A sealed, never-initialized, or stopped vault holds nothing in memory that
// its data and key files do not, so teardown archives it whatever its keys
// are, and never asks for them.
func TestTeardown_VaultThatIsNotOpenIsArchivedWithoutAKeyCheck(t *testing.T) {
	for name, probe := range map[string]vaultProbe{
		"sealed":            {state: vaultProbeVault, initialized: true, sealed: true, storageType: "raft"},
		"never initialized": {state: vaultProbeVault, sealed: true, storageType: "raft"},
		"stopped":           stoppedProbe(),
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeRaftData(t, paths["vaultDir"])
			writeKeys(t, paths, false, true)

			fake := &fakeInception{probe: probe, session: true}

			_, err := runInceptionTeardown(t, paths, fake, false)
			require.NoError(t, err)
			assert.Equal(t, []string{"probe", "cleanup"}, fake.calls)

			archives := archivesOf(t, paths)
			require.Len(t, archives, 1)
			assert.FileExists(t, filepath.Join(archives[0], "data", "vault.db"))
			assert.FileExists(t, filepath.Join(archives[0], "unseal.keys"))
		})
	}
}

// --force archives an open vault whose keys cannot be saved. It still tries
// to recover them first, because a key recovered now is one the archive can
// be reopened with.
func TestTeardown_ForceArchivesAnOpenVaultWithoutSavedKeys(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, false)

	fake := &fakeInception{probe: healthyRaftProbe(), session: true}

	_, err := runInceptionTeardown(t, paths, fake, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"probe", "recover-keys", "cleanup"}, fake.calls)

	archives := archivesOf(t, paths)
	require.Len(t, archives, 1)
	assert.FileExists(t, filepath.Join(archives[0], "data", "vault.db"))
	assert.FileExists(t, filepath.Join(archives[0], "root.key"))
}

// The ownership guard still runs first. A vault that is not this bloc's is
// refused before its keys are looked at, with or without --force.
func TestTeardown_VaultThatIsNotTheBlocsIsRefusedBeforeTheKeyCheck(t *testing.T) {
	for _, force := range []bool{false, true} {
		paths := reconcilePaths(t)
		writeRaftData(t, paths["vaultDir"])

		fake := &fakeInception{probe: healthyRaftProbe(), notOurs: true}

		ops, err := runInceptionTeardown(t, paths, fake, force)
		require.ErrorIs(t, err, ErrInceptionPortTaken)
		assert.Equal(t, []string{"probe"}, fake.calls)
		assert.Empty(t, ops.commands)
		assert.Empty(t, archivesOf(t, paths))
	}
}

// A cut-short unseal key is repaired from the vault's log before the key
// check, the way ocfp vault inception repairs it, so teardown archives the
// whole key rather than refusing a vault whose key it can find.
func TestTeardown_OpenVaultWithCutShortUnsealKeyIsRepairedAndArchived(t *testing.T) {
	paths := reconcilePaths(t)
	require.NoError(t, os.MkdirAll(paths["logDir"], 0o700))
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, false)
	writeTruncatedUnsealKey(t, paths, 57)
	require.NoError(t, os.WriteFile(paths["logFile"],
		[]byte("Now targeting x\nYour Vault Seal Key is "+testSealKey+"\n"), 0o600))

	fake := &fakeInception{probe: healthyRaftProbe(), session: true}

	_, err := runInceptionTeardown(t, paths, fake, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"probe", "cleanup"}, fake.calls, "a repaired key leaves nothing to recover")

	archives := archivesOf(t, paths)
	require.Len(t, archives, 1)
	assert.Equal(t, testSealKey+"\n", readKey(t, filepath.Join(archives[0], "unseal.keys")))
	assert.FileExists(t, filepath.Join(archives[0], "data", "vault.db"))
}

// When no output holds the whole key a cut-short unseal.keys is the start
// of, teardown refuses the way inception does, leaves the vault running, and
// touches nothing. The key file holds no usable key, so --force is the way to
// archive the vault anyway, and the error says so.
func TestTeardown_OpenVaultWithUnrepairableUnsealKeyIsLeftRunning(t *testing.T) {
	paths := reconcilePaths(t)
	require.NoError(t, os.MkdirAll(paths["logDir"], 0o700))
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, false)
	writeTruncatedUnsealKey(t, paths, 57)
	require.NoError(t, os.WriteFile(paths["logFile"], []byte("Now targeting x\n"), 0o600))

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: healthyRaftProbe(), session: true}

	ops, err := runInceptionTeardown(t, paths, fake, false)
	require.ErrorIs(t, err, ErrUnsealKeyFileMalformed)
	assert.Contains(t, err.Error(), "left running")
	assert.Contains(t, err.Error(), "--force")
	assert.NotContains(t, err.Error(), testSealKey[:20])
	assert.Equal(t, []string{"probe"}, fake.calls)
	assert.Equal(t, []string{historyCommand(paths)}, ops.commands, "only the pane's history may be read")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
	assert.Empty(t, archivesOf(t, paths))
}

// A key file that cannot be read may hold the key, so teardown refuses with
// the vault left running and touches nothing. The fix is the file's owner or
// mode, not --force, so the error does not offer --force.
func TestTeardown_OpenVaultWithUnreadableKeyFileIsLeftRunning(t *testing.T) {
	for _, keyFile := range []string{"rootKeyFile", "unsealKeysFile"} {
		t.Run(keyFile, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeRaftData(t, paths["vaultDir"])
			writeKeys(t, paths, true, true)

			before := treeDigest(t, blocDir(paths))
			restore := makeUnreadable(t, paths[keyFile])
			fake := &fakeInception{probe: healthyRaftProbe(), session: true}

			ops, err := runInceptionTeardown(t, paths, fake, false)
			require.ErrorIs(t, err, ErrInceptionKeyFileUnreadable)
			assert.Contains(t, err.Error(), paths[keyFile])
			assert.Contains(t, err.Error(), "left running")
			assert.NotContains(t, err.Error(), "--force")
			assert.Equal(t, []string{"probe"}, fake.calls)
			assert.Empty(t, ops.commands, "no stop command may run")

			restore()
			assert.Equal(t, before, treeDigest(t, blocDir(paths)))
			assert.Empty(t, archivesOf(t, paths))
		})
	}
}

// When recovering a missing key fails, teardown refuses with the vault left
// running and touches nothing. The failure is not that the keys are missing
// but that recovery broke, so the error does not offer --force.
func TestTeardown_OpenVaultWhoseKeyRecoveryFailsIsLeftRunning(t *testing.T) {
	errRecover := errors.New("recovery broke")

	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, false)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: healthyRaftProbe(), session: true, recoverErr: errRecover}

	ops, err := runInceptionTeardown(t, paths, fake, false)
	require.ErrorIs(t, err, errRecover)
	assert.Contains(t, err.Error(), "left running")
	assert.NotContains(t, err.Error(), "--force")
	assert.Equal(t, []string{"probe", "recover-keys"}, fake.calls)
	assert.Empty(t, ops.commands, "no stop command may run")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
	assert.Empty(t, archivesOf(t, paths))
}

// --force archives an open vault even when recovering its keys fails, after
// trying the recovery, and the archive holds the key that was saved.
func TestTeardown_ForceArchivesAnOpenVaultWhoseKeyRecoveryFails(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, false)

	fake := &fakeInception{probe: healthyRaftProbe(), session: true, recoverErr: errors.New("recovery broke")}

	ops, err := runInceptionTeardown(t, paths, fake, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"probe", "recover-keys", "cleanup"}, fake.calls)
	assert.NotEmpty(t, ops.commands, "the stop runs")

	archives := archivesOf(t, paths)
	require.Len(t, archives, 1)
	assert.FileExists(t, filepath.Join(archives[0], "data", "vault.db"))
	assert.FileExists(t, filepath.Join(archives[0], "root.key"))
	assert.NoFileExists(t, filepath.Join(archives[0], "unseal.keys"))
}

func TestVaultTeardownCmd_ForceFlagDefaultsOff(t *testing.T) {
	flag := newVaultTeardownCmd().Flags().Lookup("force")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}
