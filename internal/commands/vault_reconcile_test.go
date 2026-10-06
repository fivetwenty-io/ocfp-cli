package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// fakeInception scripts the steps the start-up state machine takes and
// records them in order, so each test can assert both what ran and what the
// disk looks like afterwards.
type fakeInception struct {
	probe          vaultProbe
	session        bool
	target         bool
	retargetErr    error
	startErrs      []error // one per start call; missing entries succeed
	stopErrs       []error // one per stop call; missing entries succeed
	clusterPortErr error
	notOurs        bool  // the vault on the port is not this bloc's
	ownsErr        error // the ownership check could not decide
	calls          []string
}

func (f *fakeInception) record(call string) {
	f.calls = append(f.calls, call)
}

func (f *fakeInception) steps() inceptionSteps {
	return inceptionSteps{
		probe: func(context.Context, string) vaultProbe {
			f.record("probe")

			return f.probe
		},
		hasSession: func(context.Context, map[string]string) bool { return f.session },
		ownsVault: func(context.Context, map[string]string, vaultDataState) (bool, error) {
			return !f.notOurs, f.ownsErr
		},
		targetRegistered: func(context.Context, map[string]string) bool {
			return f.target
		},
		retarget: func(context.Context, map[string]string, *zap.SugaredLogger) error {
			f.record("retarget")

			return f.retargetErr
		},
		stop: func(context.Context, map[string]string, *zap.SugaredLogger) error {
			f.record("stop")

			return popErr(&f.stopErrs)
		},
		clusterPortFree: func(_ context.Context, port string) error {
			f.record("cluster-port " + port)

			return f.clusterPortErr
		},
		start: func(_ context.Context, _ map[string]string, _ inceptionTools, mode safeLocalMode, _ *zap.SugaredLogger) error {
			f.record("start " + modeName(mode))

			return popErr(&f.startErrs)
		},
		finish: func(_ context.Context, _ map[string]string, mode safeLocalMode, _ *zap.SugaredLogger) error {
			f.record("finish " + modeName(mode))

			return nil
		},
		now: func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) },
	}
}

func popErr(errs *[]error) error {
	if len(*errs) == 0 {
		return nil
	}

	err := (*errs)[0]
	*errs = (*errs)[1:]

	return err
}

func modeName(mode safeLocalMode) string {
	if mode == safeLocalRestart {
		return "restart"
	}

	return "fresh"
}

// reconcilePaths returns a bloc's paths moved into a temp bloc layout with no
// data and no keys yet.
func reconcilePaths(t *testing.T) map[string]string {
	t.Helper()

	paths := getVaultInceptionPaths("ocfp-lab-drgao", false)
	vaultRoot := filepath.Join(t.TempDir(), "ocfp-lab-drgao", "vault")

	paths["vaultDir"] = filepath.Join(vaultRoot, "data")
	paths["rootKeyFile"] = filepath.Join(vaultRoot, "root.key")
	paths["unsealKeysFile"] = filepath.Join(vaultRoot, "unseal.keys")
	paths["pidFile"] = filepath.Join(paths["vaultDir"], "vault.pid")
	paths["logDir"] = filepath.Join(t.TempDir(), "logs")
	paths["logFile"] = filepath.Join(paths["logDir"], VaultInceptionLogFile)

	require.NoError(t, os.MkdirAll(vaultRoot, 0o700))

	return paths
}

func writeRaftData(t *testing.T, dir string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "raft"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "vault.db"), []byte("raft-data"), 0o600))
}

func writeFileData(t *testing.T, dir string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "core"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "core", "_keyring"), []byte("file-data"), 0o600))
}

func writeKeys(t *testing.T, paths map[string]string, root, unseal bool) {
	t.Helper()

	if root {
		require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte("s.ROOT-TOKEN-SENTINEL\n"), 0o600))
	}

	if unseal {
		require.NoError(t, os.WriteFile(paths["unsealKeysFile"], []byte("UNSEAL-KEY-SENTINEL\n"), 0o600))
	}
}

// treeDigest maps every file under dir to a hash of its contents, so a test
// can prove that a run left the disk exactly as it found it.
func treeDigest(t *testing.T, dir string) map[string]string {
	t.Helper()

	digest := map[string]string{}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, _ := filepath.Rel(dir, path)
		if d.IsDir() {
			digest[rel+"/"] = "dir"

			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		sum := sha256.Sum256(data)
		digest[rel] = hex.EncodeToString(sum[:])

		return nil
	})
	require.NoError(t, err)

	return digest
}

// blocDir is the directory that holds <bloc>/vault and any archive of it.
func blocDir(paths map[string]string) string {
	return filepath.Dir(filepath.Dir(paths["vaultDir"]))
}

func archivesOf(t *testing.T, paths map[string]string) []string {
	t.Helper()

	archives, err := filepath.Glob(filepath.Dir(paths["vaultDir"]) + VaultArchiveSuffix + "*")
	require.NoError(t, err)

	return archives
}

func runReconcile(t *testing.T, paths map[string]string, fake *fakeInception) error {
	t.Helper()

	return reconcileInceptionVault(context.Background(), &inceptionRun{
		paths: paths,
		tools: testTools(),
		steps: fake.steps(),
		log:   zap.NewNop().Sugar(),
	})
}

func stoppedProbe() vaultProbe {
	return vaultProbe{state: vaultProbeStopped}
}

func TestReconcile_RaftWithBothKeysRestartsInPlace(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: stoppedProbe()}

	require.NoError(t, runReconcile(t, paths, fake))

	assert.Equal(t, []string{
		"probe", "stop", "cluster-port " + paths["clusterPort"], "start restart", "finish restart",
	}, fake.calls)
	assert.Equal(t, before, treeDigest(t, blocDir(paths)), "a restart in place must not move or change anything")
	assert.Empty(t, archivesOf(t, paths))
}

func TestReconcile_RaftMissingAKeyArchivesAndStartsFresh(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, false)

	fake := &fakeInception{probe: stoppedProbe()}

	require.NoError(t, runReconcile(t, paths, fake))

	assert.Equal(t, []string{
		"probe", "stop", "cluster-port " + paths["clusterPort"], "start fresh", "finish fresh",
	}, fake.calls)

	archives := archivesOf(t, paths)
	require.Len(t, archives, 1)
	assert.FileExists(t, filepath.Join(archives[0], "data", "vault.db"))
	assert.DirExists(t, filepath.Join(archives[0], "data", "raft"))
	assert.FileExists(t, filepath.Join(archives[0], "root.key"), "the remaining key must be kept with the data")
}

// An empty key file is as good as a missing one. safe refuses an empty token
// file before it launches, which would otherwise wedge every later start.
func TestReconcile_BlankKeyFileCountsAsMissing(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, false, true)
	require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte(" \n"), 0o600))

	fake := &fakeInception{probe: stoppedProbe()}

	require.NoError(t, runReconcile(t, paths, fake))
	assert.Contains(t, fake.calls, "start fresh")
	assert.NotContains(t, fake.calls, "start restart")
	require.Len(t, archivesOf(t, paths), 1)
}

func TestReconcile_RejectedKeysArchiveAndStartFresh(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{
		probe:     stoppedProbe(),
		startErrs: []error{fmt.Errorf("%w: !! The root token in x was rejected by OpenBao", ErrVaultKeysRejected)},
	}

	require.NoError(t, runReconcile(t, paths, fake))

	assert.Equal(t, []string{
		"probe", "stop", "cluster-port " + paths["clusterPort"],
		"start restart", "stop", "start fresh", "finish fresh",
	}, fake.calls)

	archives := archivesOf(t, paths)
	require.Len(t, archives, 1)
	assert.FileExists(t, filepath.Join(archives[0], "data", "vault.db"))
	assert.FileExists(t, filepath.Join(archives[0], "root.key"))
	assert.FileExists(t, filepath.Join(archives[0], "unseal.keys"))
}

// A rejected key that leaves a vault we cannot stop must not archive data out
// from under the running engine.
func TestReconcile_RejectedKeysButStuckVaultArchivesNothing(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{
		probe:     stoppedProbe(),
		startErrs: []error{ErrVaultKeysRejected},
		stopErrs:  []error{nil, ErrVaultWouldNotStop},
	}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrVaultWouldNotStop)
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
	assert.NotContains(t, fake.calls, "start fresh")
}

// Anything other than a key failure says nothing about the keys, so the vault
// keeps its data and keys exactly where they are.
func TestReconcile_EnvironmentalRestartFailureChangesNothing(t *testing.T) {
	for _, startErr := range []error{
		fmt.Errorf("%w: !! port 18234 is already in use", ErrVaultStartupError),
		ErrVaultNotReady,
		ErrTmuxSessionFailed,
	} {
		t.Run(startErr.Error(), func(t *testing.T) {
			paths := reconcilePaths(t)
			writeRaftData(t, paths["vaultDir"])
			writeKeys(t, paths, true, true)

			before := treeDigest(t, blocDir(paths))
			fake := &fakeInception{probe: stoppedProbe(), startErrs: []error{startErr}}

			err := runReconcile(t, paths, fake)
			require.ErrorIs(t, err, startErr)
			assert.Equal(t, []string{
				"probe", "stop", "cluster-port " + paths["clusterPort"], "start restart", "stop",
			}, fake.calls, "a failed restart is stopped and nothing else happens")
			assert.Equal(t, before, treeDigest(t, blocDir(paths)))
			assert.Empty(t, archivesOf(t, paths))
		})
	}
}

func TestReconcile_ClusterPortTakenChangesNothing(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: stoppedProbe(), clusterPortErr: errors.New("address already in use")}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrInceptionClusterPortTaken)
	assert.Contains(t, err.Error(), paths["clusterPort"])
	assert.Contains(t, err.Error(), paths["port"])
	assert.Contains(t, err.Error(), "OCFP_VAULT_INCEPTION_PORT")
	assert.NotContains(t, strings.Join(fake.calls, "\n"), "start")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
}

func TestReconcile_AbsentWithLeftoverKeysArchivesAndStartsFresh(t *testing.T) {
	paths := reconcilePaths(t)
	writeKeys(t, paths, true, true)

	fake := &fakeInception{probe: stoppedProbe()}

	require.NoError(t, runReconcile(t, paths, fake))

	assert.Equal(t, []string{
		"probe", "stop", "cluster-port " + paths["clusterPort"], "start fresh", "finish fresh",
	}, fake.calls)

	archives := archivesOf(t, paths)
	require.Len(t, archives, 1)
	assert.FileExists(t, filepath.Join(archives[0], "root.key"))
	assert.FileExists(t, filepath.Join(archives[0], "unseal.keys"))
	assert.NoFileExists(t, paths["rootKeyFile"], "stale keys must never be reused by the new vault")
}

func TestReconcile_AbsentWithoutKeysStartsFresh(t *testing.T) {
	paths := reconcilePaths(t)
	fake := &fakeInception{probe: stoppedProbe()}

	require.NoError(t, runReconcile(t, paths, fake))

	assert.Equal(t, []string{
		"probe", "stop", "cluster-port " + paths["clusterPort"], "start fresh", "finish fresh",
	}, fake.calls)
	assert.Empty(t, archivesOf(t, paths))
}

func TestReconcile_MixedDataChangesNothing(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: stoppedProbe()}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrVaultDataMixed)
	assert.Contains(t, err.Error(), paths["vaultDir"])
	assert.Equal(t, []string{"probe"}, fake.calls, "a mixed directory is refused before anything stops")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
}

func TestReconcile_StrangerOnThePortChangesNothing(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: vaultProbe{state: vaultProbeStranger}}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrInceptionPortTaken)
	assert.Contains(t, err.Error(), paths["port"])
	assert.Contains(t, err.Error(), "OCFP_VAULT_INCEPTION_PORT")
	assert.Equal(t, []string{"probe"}, fake.calls)
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
}

func healthyRaftProbe() vaultProbe {
	return vaultProbe{state: vaultProbeVault, initialized: true, storageType: "raft"}
}

func TestReconcile_HealthyRaftVaultIsLeftRunning(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{probe: healthyRaftProbe(), session: true, target: true}

	require.NoError(t, runReconcile(t, paths, fake))
	assert.Equal(t, []string{"probe"}, fake.calls)
}

// The fast path no longer depends on the current safe target, which any
// sibling bloc can move. A healthy vault whose target went missing is
// re-targeted, never restarted.
func TestReconcile_HealthyRaftVaultWithoutTargetIsRetargeted(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{probe: healthyRaftProbe(), session: true}

	require.NoError(t, runReconcile(t, paths, fake))
	assert.Equal(t, []string{"probe", "retarget"}, fake.calls)
}

func TestReconcile_HealthyVaultWithoutTargetOrTokenIsAnError(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, false, true)

	fake := &fakeInception{probe: healthyRaftProbe(), session: true}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrInceptionTargetLost)
	assert.Equal(t, []string{"probe"}, fake.calls, "a running vault is never stopped just to recover its target")
}

// A vault that answers without naming its storage is judged by its data.
func TestReconcile_HealthyVaultWithoutStorageTypeUsesTheDisk(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{
		probe:   vaultProbe{state: vaultProbeVault, initialized: true},
		session: true,
		target:  true,
	}

	require.NoError(t, runReconcile(t, paths, fake))
	assert.Equal(t, []string{"probe"}, fake.calls)
}

func TestReconcile_UnhealthyRaftVaultIsStoppedAndRestarted(t *testing.T) {
	for name, tc := range map[string]struct {
		probe   vaultProbe
		session bool
	}{
		"sealed":             {probe: vaultProbe{state: vaultProbeVault, initialized: true, sealed: true, storageType: "raft"}, session: true},
		"orphaned engine":    {probe: healthyRaftProbe(), session: false},
		"never initialized":  {probe: vaultProbe{state: vaultProbeVault, sealed: true, storageType: "raft"}, session: true},
		"stopped but locked": {probe: stoppedProbe(), session: true},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeRaftData(t, paths["vaultDir"])
			writeKeys(t, paths, true, true)

			fake := &fakeInception{probe: tc.probe, session: tc.session, target: true}

			require.NoError(t, runReconcile(t, paths, fake))
			assert.Equal(t, "stop", fake.calls[len(fake.calls)-4])
			assert.Equal(t, []string{"start restart", "finish restart"}, fake.calls[len(fake.calls)-2:])
			assert.Empty(t, archivesOf(t, paths))
		})
	}
}

func TestReconcile_StopFailureChangesNothing(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, false)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{
		probe:    vaultProbe{state: vaultProbeVault, initialized: true, sealed: true, storageType: "raft"},
		stopErrs: []error{ErrVaultWouldNotStop},
	}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrVaultWouldNotStop)
	assert.NotContains(t, strings.Join(fake.calls, "\n"), "start")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)), "nothing may be archived while the vault may still run")
}

// The legacy and test layouts keep one key file for both keys, and that file
// ends up holding only the root token. Such a vault cannot be reopened, so it
// goes down the archive path rather than feeding a token to the unseal prompt.
func TestReconcile_SharedKeyFileIsNotAPairOfKeys(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, false)
	paths["unsealKeysFile"] = paths["rootKeyFile"]

	fake := &fakeInception{probe: stoppedProbe()}

	require.NoError(t, runReconcile(t, paths, fake))
	assert.NotContains(t, fake.calls, "start restart")
	assert.Contains(t, fake.calls, "start fresh")
}

// A vault that answers on this bloc's port but does not hold this bloc's data
// belongs to someone else, such as a sibling bloc whose derived port
// collides with this one. It must never be stopped or replaced.
func TestReconcile_SomeoneElsesVaultOnThePortChangesNothing(t *testing.T) {
	for name, probe := range map[string]vaultProbe{
		"healthy": healthyRaftProbe(),
		"sealed":  {state: vaultProbeVault, initialized: true, sealed: true, storageType: "raft"},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeRaftData(t, paths["vaultDir"])
			writeKeys(t, paths, true, true)

			before := treeDigest(t, blocDir(paths))
			fake := &fakeInception{probe: probe, session: true, target: true, notOurs: true}

			err := runReconcile(t, paths, fake)
			require.ErrorIs(t, err, ErrInceptionPortTaken)
			assert.Contains(t, err.Error(), paths["port"])
			assert.Equal(t, []string{"probe"}, fake.calls)
			assert.Equal(t, before, treeDigest(t, blocDir(paths)))
		})
	}
}

func TestReconcile_UnknownOwnershipChangesNothing(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: healthyRaftProbe(), session: true, ownsErr: ErrVaultLockUnknown}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrVaultLockUnknown)
	assert.Equal(t, []string{"probe"}, fake.calls)
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
}

// A fresh start that fails after an archive must say where the old vault
// went, so nobody mistakes it for lost.
func TestReconcile_FailedFreshStartAfterArchiveNamesTheArchive(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, false)

	fake := &fakeInception{probe: stoppedProbe(), startErrs: []error{ErrVaultNotReady}}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrVaultNotReady)

	archives := archivesOf(t, paths)
	require.Len(t, archives, 1)
	assert.Contains(t, err.Error(), archives[0])
	assert.Equal(t, "stop", fake.calls[len(fake.calls)-1], "the failed fresh vault is stopped")
}

// A file-backed vault with both keys is never archived or replaced by a
// fresh raft vault, because its keys still open it.
func TestReconcile_FileVaultWithKeysIsLeftAsItIs(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: stoppedProbe()}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrFileVaultNotMigrated)
	assert.Contains(t, err.Error(), paths["vaultDir"])
	assert.NotContains(t, strings.Join(fake.calls, "\n"), "start")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
	assert.Empty(t, archivesOf(t, paths))
}
