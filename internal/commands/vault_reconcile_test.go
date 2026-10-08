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

	"github.com/ocfp/ocfp-cli-go/internal/keyfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// fakeInception scripts the steps the start-up state machine takes and
// records them in order, so each test can assert both what ran and what the
// disk looks like afterwards.
type fakeInception struct {
	// rootTokenErr is what every check of root.key against the running
	// vault returns, and rootTokenChecks counts those checks.
	rootTokenErr    error
	rootTokenChecks int

	probe          vaultProbe
	session        bool
	target         bool
	retargetErr    error
	startErrs      []error // one per start call; missing entries succeed
	stopErrs       []error // one per stop call; missing entries succeed
	finishErr      error   // what every finish call returns
	clusterPortErr error
	notOurs        bool  // the vault on the port is not this bloc's
	ownsErr        error // the ownership check could not decide
	migrate        func(paths map[string]string) (string, error)
	recover        func(paths map[string]string)                                       // what key recovery finds
	recoverErr     error                                                               // what every key recovery returns, after recover runs
	targetToken    string                                                              // the token ~/.saferc holds for the bloc's target; a stop deletes it
	tokenFiles     []string                                                            // the root token file each start was given
	onStart        func(f *fakeInception, paths map[string]string, mode safeLocalMode) // what a start leaves behind
	disableErr     error                                                               // what teardown's boot unit disable returns
	cleanupErr     error                                                               // what teardown's cleanup returns instead of running
	calls          []string

	// safeCurrent is safe's current target, the one a bare safe command
	// uses. A start, a finish, and a retarget make the bloc's target
	// current, as safe local and safe target do, and a stop that deletes
	// the bloc's target while it is current leaves no target current.
	safeCurrent string
	// safeCurrentErr is what every read of the current target returns.
	safeCurrentErr error
	// setCurrentErr is what every attempt to make a target current returns.
	setCurrentErr error
	// currentSets lists each target the run tried to make current again.
	currentSets []string
}

// targetBloc does what safe local and safe target do to safe's current
// target: they make the bloc's target current.
func (f *fakeInception) targetBloc(paths map[string]string) {
	f.safeCurrent = paths["vaultName"]
}

// rootTokenWorks is the fake check of root.key against the running vault.
func (f *fakeInception) rootTokenWorks(context.Context, map[string]string) error {
	f.rootTokenChecks++

	return f.rootTokenErr
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
		retarget: func(_ context.Context, paths map[string]string, _ *zap.SugaredLogger) error {
			f.record("retarget")

			if f.retargetErr == nil {
				f.targetBloc(paths)
			}

			return f.retargetErr
		},
		stop: func(_ context.Context, paths map[string]string, _ *zap.SugaredLogger) error {
			f.record("stop")

			err := popErr(&f.stopErrs)
			if err == nil {
				// A real stop runs safe target delete, so safe no longer
				// holds the token once the vault is down. A token read
				// after the stop is therefore always gone.
				f.targetToken = ""

				// Deleting the current target leaves none current.
				if f.safeCurrent == paths["vaultName"] {
					f.safeCurrent = ""
				}
			}

			return err
		},
		clusterPortFree: func(_ context.Context, port string) error {
			f.record("cluster-port " + port)

			return f.clusterPortErr
		},
		start: func(_ context.Context, paths map[string]string, _ inceptionTools, mode safeLocalMode, _ *zap.SugaredLogger) error {
			f.record("start " + modeName(mode))
			f.targetBloc(paths)

			if mode == safeLocalRestart {
				f.tokenFiles = append(f.tokenFiles, paths["rootKeyFile"])
			}

			if f.onStart != nil {
				f.onStart(f, paths, mode)
			}

			return popErr(&f.startErrs)
		},
		finish: func(_ context.Context, paths map[string]string, mode safeLocalMode, _ *zap.SugaredLogger) error {
			f.record("finish " + modeName(mode))
			f.targetBloc(paths)

			return f.finishErr
		},
		migrate: func(_ context.Context, paths map[string]string, _ inceptionTools, _ *zap.SugaredLogger) (string, error) {
			f.record("migrate")

			if f.migrate == nil {
				return "", nil
			}

			return f.migrate(paths)
		},
		recoverKeys: func(_ context.Context, paths map[string]string, _ *zap.SugaredLogger) error {
			f.record("recover-keys")

			if f.recover != nil {
				f.recover(paths)
			}

			return f.recoverErr
		},
		targetToken: func(map[string]string) string { return f.targetToken },
		safeTarget: safeCurrentTargetSteps{
			current: func(context.Context) (string, error) {
				if f.safeCurrentErr != nil {
					return "", f.safeCurrentErr
				}

				return f.safeCurrent, nil
			},
			setCurrent: func(_ context.Context, name string) error {
				f.currentSets = append(f.currentSets, name)

				if f.setCurrentErr != nil {
					return f.setCurrentErr
				}

				f.safeCurrent = name

				return nil
			},
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

	paths := mustInceptionPaths(t, "ocfp-lab-drgao", false)
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
		require.NoError(t, os.WriteFile(paths["unsealKeysFile"], []byte(testSealKey+"\n"), 0o600))
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

// requireRefusedForMissingKeys checks a run that found vault data without
// both keys: it refused, started nothing, archived nothing, and said how to
// restore the keys or replace the vault on purpose.
func requireRefusedForMissingKeys(t *testing.T, paths map[string]string, fake *fakeInception, err error) {
	t.Helper()

	require.ErrorIs(t, err, ErrInceptionKeysMissing)
	assert.NotContains(t, strings.Join(fake.calls, "\n"), "start", "a vault with data and no keys is never started")
	assert.NotContains(t, fake.calls, "migrate")
	assert.Empty(t, archivesOf(t, paths))
	assert.Contains(t, err.Error(), paths["vaultDir"])
	assert.Contains(t, err.Error(), paths["rootKeyFile"])
	assert.Contains(t, err.Error(), paths["unsealKeysFile"])
	assert.Contains(t, err.Error(), "'ocfp vault teardown' and then 'ocfp vault inception'")
	assert.NotContains(t, err.Error(), testSealKey)
}

// Vault data without both keys may still be worth keeping, since a key can
// be restored from a backup, so the run refuses rather than archiving the
// data and starting a new, empty vault over it.
func TestReconcile_RaftMissingAKeyRefusesAndChangesNothing(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, false)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: stoppedProbe()}

	err := runReconcile(t, paths, fake)
	requireRefusedForMissingKeys(t, paths, fake, err)

	assert.Equal(t, []string{"probe", "stop", "cluster-port " + paths["clusterPort"]}, fake.calls)
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
}

// An empty key file is as good as a missing one. safe refuses an empty token
// file before it launches, which would otherwise wedge every later start.
func TestReconcile_BlankKeyFileCountsAsMissing(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, false, true)
	require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte(" \n"), 0o600))

	fake := &fakeInception{probe: stoppedProbe()}

	requireRefusedForMissingKeys(t, paths, fake, runReconcile(t, paths, fake))
}

func unsealKeyRejected() error {
	return fmt.Errorf("%w: !! Unable to unseal: invalid key", ErrVaultKeysRejected)
}

// A vault whose saved key the engine refuses still holds its data, and the
// keys may be what is wrong rather than the data, so the run refuses. It
// stops what it started, archives nothing, starts no new vault, and says how
// to replace the vault on purpose.
func TestReconcile_RejectedKeysRefuseAndChangeNothing(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: stoppedProbe(), startErrs: []error{unsealKeyRejected()}}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrInceptionKeysRefused)
	require.ErrorIs(t, err, ErrVaultKeysRejected)

	assert.Equal(t, []string{
		"probe", "stop", "cluster-port " + paths["clusterPort"], "start restart", "stop",
	}, fake.calls, "the refused vault is stopped and nothing else happens")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
	assert.Empty(t, archivesOf(t, paths))

	assert.Contains(t, err.Error(), paths["unsealKeysFile"], "the error names the key file the engine refused")
	assert.Contains(t, err.Error(), paths["vaultDir"])
	assert.Contains(t, err.Error(), "'ocfp vault teardown' and then 'ocfp vault inception'")
	assert.NotContains(t, err.Error(), migrationBackupSuffix, "no file store was kept, so there is nothing to roll back to")
	assert.NotContains(t, err.Error(), testSealKey)
}

// Right after a migration to raft, the file store it came from is still
// beside the data. A refused key then names that store and how to go back
// to it, and a refused root token names root.key.
func TestReconcile_RejectedKeyAfterMigrationNamesTheFileBackup(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	older := paths["vaultDir"] + migrationBackupSuffix + "20261001-090000"
	newer := paths["vaultDir"] + migrationBackupSuffix + "20261006-115900"

	writeFileData(t, older)
	writeFileData(t, newer)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: stoppedProbe(), startErrs: []error{tokenRejected()}}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrInceptionKeysRefused)
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
	assert.Empty(t, archivesOf(t, paths))
	assert.NotContains(t, fake.calls, "start fresh")

	msg := err.Error()
	assert.Contains(t, msg, "the engine refused the key in "+paths["rootKeyFile"])
	assert.Contains(t, msg, older)
	assert.Contains(t, msg, newer)
	assert.Contains(t, msg, "rename "+newer+" to "+paths["vaultDir"], "the rollback starts from the newest file store")
	assert.Contains(t, msg, "'ocfp vault teardown' and then 'ocfp vault inception'")
}

// A new vault that started after an archive but whose keys could not be saved
// is running. The error must say where the old vault went and that the new
// one is up without saved keys, never that it did not start.
func TestReconcile_ArchivedThenNewVaultWithoutSavedKeys(t *testing.T) {
	paths := reconcilePaths(t)
	writeKeys(t, paths, true, false)

	fake := &fakeInception{
		probe:     stoppedProbe(),
		finishErr: fmt.Errorf("%w: the vault is still running", ErrInceptionKeysNotSaved),
	}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrInceptionKeysNotSaved)

	archives := archivesOf(t, paths)
	require.Len(t, archives, 1)
	assert.Contains(t, err.Error(), "the previous inception vault was kept at "+archives[0])
	assert.Contains(t, err.Error(), "a new vault is running")
	assert.NotContains(t, err.Error(), "did not start")
	assert.Equal(t, "finish fresh", fake.calls[len(fake.calls)-1], "the new vault must be left running")
}

// A new vault that fails to finish has started, and may hold the only copy
// of its keys in memory and in its log. Whatever finish reports, the run must
// end there: no stop, no archive, and no second start afterwards.
func TestReconcile_FinishFailureLeavesTheNewVaultRunning(t *testing.T) {
	finishErrs := map[string]error{
		"keys not saved": fmt.Errorf("%w: the vault is still running", ErrInceptionKeysNotSaved),
		"target failed":  errors.New("failed to target vault: safe target exited 1"),
	}

	for name, finishErr := range finishErrs {
		t.Run("fresh/"+name, func(t *testing.T) {
			paths := reconcilePaths(t)
			fake := &fakeInception{probe: stoppedProbe(), finishErr: finishErr}

			err := runReconcile(t, paths, fake)
			require.ErrorIs(t, err, finishErr)

			assert.Equal(t, []string{
				"probe", "stop", "cluster-port " + paths["clusterPort"], "start fresh", "finish fresh",
			}, fake.calls)
			assert.Empty(t, archivesOf(t, paths))
		})
	}
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

// Key files with no data beside them are archived only when nothing beside
// the data directory holds a store those keys may open. A file store kept by
// a migration, or a raft copy moved aside, may be the vault the keys belong
// to, so the run refuses and names it.
func TestReconcile_AbsentWithLeftoverKeysBesideAStoreRefuses(t *testing.T) {
	for _, tc := range []struct {
		suffix string
		write  func(t *testing.T, dir string)
	}{
		{migrationBackupSuffix + "20261006-120000", writeFileData},
		{migrationFailedSuffix + "20261006-120000", writeRaftData},
		{migrationPartialSuffix + "20261006-120000", writeRaftData},
		{migrationStagingSuffix, writeRaftData},
	} {
		t.Run(tc.suffix, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeKeys(t, paths, true, true)

			store := paths["vaultDir"] + tc.suffix
			tc.write(t, store)

			before := treeDigest(t, blocDir(paths))
			fake := &fakeInception{probe: stoppedProbe()}

			err := runReconcile(t, paths, fake)
			require.ErrorIs(t, err, ErrInceptionDataElsewhere)
			assert.Equal(t, []string{"probe", "stop", "cluster-port " + paths["clusterPort"]}, fake.calls)
			assert.Equal(t, before, treeDigest(t, blocDir(paths)))
			assert.Empty(t, archivesOf(t, paths))
			assert.Contains(t, err.Error(), store)
			assert.Contains(t, err.Error(), paths["vaultDir"])
			assert.Contains(t, err.Error(), "'ocfp vault teardown' and then 'ocfp vault inception'")
		})
	}

	t.Run("an empty directory beside the data holds no store", func(t *testing.T) {
		paths := reconcilePaths(t)
		writeKeys(t, paths, true, true)
		require.NoError(t, os.MkdirAll(paths["vaultDir"]+migrationBackupSuffix+"20261006-120000", 0o700))

		fake := &fakeInception{probe: stoppedProbe()}

		require.NoError(t, runReconcile(t, paths, fake))
		assert.Contains(t, fake.calls, "start fresh")
		require.Len(t, archivesOf(t, paths), 1)
	})
}

// With no data and no keys, a store that a migration to raft kept or moved
// aside may still be the vault, once its keys are restored, so no new vault
// starts beside it and nothing changes.
func TestReconcile_AbsentWithoutKeysBesideAStoreRefuses(t *testing.T) {
	paths := reconcilePaths(t)
	store := paths["vaultDir"] + migrationBackupSuffix + "20261006-120000"
	writeFileData(t, store)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: stoppedProbe()}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrInceptionDataElsewhere)
	assert.Equal(t, []string{"probe", "stop", "cluster-port " + paths["clusterPort"]}, fake.calls)
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
	assert.Empty(t, archivesOf(t, paths))
	assert.Contains(t, err.Error(), store)
	assert.Contains(t, err.Error(), "'ocfp vault teardown' and then 'ocfp vault inception'")
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

// safe initializes a new vault and saves its root token in the bloc's target
// before ocfp waits for it to be ready. When the wait fails, the stop deletes
// that target, so the token is kept in root.key first, as before every other
// stop. When it cannot be kept, the new vault is left running.
func TestReconcile_FailedFreshStartKeepsTheNewToken(t *testing.T) {
	startErr := errors.New("timed out waiting for the vault")
	savesToken := func(f *fakeInception, _ map[string]string, mode safeLocalMode) {
		if mode == safeLocalFresh {
			f.targetToken = safeRCToken
		}
	}

	t.Run("kept, then stopped", func(t *testing.T) {
		paths := reconcilePaths(t)
		fake := &fakeInception{probe: stoppedProbe(), startErrs: []error{startErr}, onStart: savesToken}

		err := runReconcile(t, paths, fake)
		require.ErrorIs(t, err, startErr)
		assert.NotContains(t, err.Error(), "SENTINEL")
		assert.Equal(t, []string{"probe", "stop", "cluster-port " + paths["clusterPort"], "start fresh", "stop"}, fake.calls)
		assert.Equal(t, safeRCToken+"\n", readKey(t, paths["rootKeyFile"]))

		info, statErr := os.Stat(paths["rootKeyFile"])
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	})

	t.Run("cannot be kept, so not stopped", func(t *testing.T) {
		paths := reconcilePaths(t)
		fake := &fakeInception{
			probe: stoppedProbe(), startErrs: []error{startErr},
			onStart: func(f *fakeInception, paths map[string]string, mode safeLocalMode) {
				savesToken(f, paths, mode)
				// A root.key that holds a value nobody can read cannot be
				// compared with the token, so the token cannot be kept.
				require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte("x\n"), 0o000))
			},
		}

		if os.Geteuid() == 0 {
			t.Skip("root reads any file, so the key file cannot be made unreadable")
		}

		err := runReconcile(t, paths, fake)
		require.ErrorIs(t, err, startErr)
		require.ErrorIs(t, err, keyfile.ErrUnreadable)
		assert.Contains(t, err.Error(), "left running")
		assert.Equal(t, "start fresh", fake.calls[len(fake.calls)-1], "the new vault must not be stopped")
	})
}

// A key file shared by both keys, as older releases used without a bloc,
// ends up holding only the root token. Such a vault cannot be reopened, so the
// run refuses rather than feeding a token to the unseal prompt.
func TestReconcile_SharedKeyFileIsNotAPairOfKeys(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, false)
	paths["unsealKeysFile"] = paths["rootKeyFile"]

	fake := &fakeInception{probe: stoppedProbe()}

	requireRefusedForMissingKeys(t, paths, fake, runReconcile(t, paths, fake))
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
	writeKeys(t, paths, true, false)

	fake := &fakeInception{probe: stoppedProbe(), startErrs: []error{ErrVaultNotReady}}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrVaultNotReady)

	archives := archivesOf(t, paths)
	require.Len(t, archives, 1)
	assert.Contains(t, err.Error(), archives[0])
	assert.Equal(t, "stop", fake.calls[len(fake.calls)-1], "the failed fresh vault is stopped")
}

// fakeMigration does on disk what a successful migration does: the file
// store becomes the backup, and a raft store takes its place.
func fakeMigration(t *testing.T) func(paths map[string]string) (string, error) {
	t.Helper()

	return func(paths map[string]string) (string, error) {
		backup := paths["vaultDir"] + ".file-backup-20261006-120000"
		require.NoError(t, os.Rename(paths["vaultDir"], backup))
		writeRaftData(t, paths["vaultDir"])

		return backup, nil
	}
}

func TestReconcile_FileVaultWithKeysMigratesThenRestarts(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{probe: stoppedProbe(), migrate: fakeMigration(t)}

	require.NoError(t, runReconcile(t, paths, fake))
	assert.Equal(t, []string{
		"probe", "stop", "cluster-port " + paths["clusterPort"], "migrate", "start restart", "finish restart",
	}, fake.calls)
	assert.Empty(t, archivesOf(t, paths))
}

// File storage without both keys cannot be reopened after a migration, so it
// is never migrated, and the run refuses with the file store left in place.
func TestReconcile_FileVaultMissingAKeyIsRefusedNotMigrated(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, false, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: stoppedProbe(), migrate: fakeMigration(t)}

	requireRefusedForMissingKeys(t, paths, fake, runReconcile(t, paths, fake))
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
}

func TestReconcile_FailedMigrationStartsNothing(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{
		probe:   stoppedProbe(),
		migrate: func(map[string]string) (string, error) { return "", ErrEngineCannotReadFile },
	}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrEngineCannotReadFile)
	assert.NotContains(t, strings.Join(fake.calls, "\n"), "start")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
	assert.Empty(t, archivesOf(t, paths))
}

// Right after a migration even a rejected key must not archive. The keys
// opened the file store a moment ago, so the failure is the migration's, and
// a person has to look at both directories.
func TestReconcile_RestartFailureAfterMigrationNeverArchives(t *testing.T) {
	for _, startErr := range []error{ErrVaultKeysRejected, ErrVaultNotReady} {
		t.Run(startErr.Error(), func(t *testing.T) {
			paths := reconcilePaths(t)
			writeFileData(t, paths["vaultDir"])
			writeKeys(t, paths, true, true)

			fake := &fakeInception{probe: stoppedProbe(), migrate: fakeMigration(t), startErrs: []error{startErr}}

			err := runReconcile(t, paths, fake)
			require.ErrorIs(t, err, startErr)
			require.ErrorIs(t, err, ErrRestartAfterMigrationFailed)
			assert.Contains(t, err.Error(), paths["vaultDir"])
			assert.Contains(t, err.Error(), paths["vaultDir"]+".file-backup-20261006-120000")
			assert.Equal(t, "stop", fake.calls[len(fake.calls)-1])
			assert.NotContains(t, fake.calls, "start fresh")
			assert.Empty(t, archivesOf(t, paths))
			assert.Equal(t, vaultDataRaft, classifyVaultData(paths["vaultDir"]))
		})
	}
}

// A copy that never finished is moved aside and run again from the data,
// which the interrupted run never touched.
func TestReconcile_ResumesAnUnfinishedCopy(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)
	writeRaftData(t, paths["vaultDir"]+".raft-migrating")
	writeJournal(t, paths, "migrating", paths["vaultDir"]+".file-backup-20261006-110000")

	fake := &fakeInception{probe: stoppedProbe(), migrate: fakeMigration(t)}

	require.NoError(t, runReconcile(t, paths, fake))
	assert.Equal(t, []string{
		"probe", "stop", "cluster-port " + paths["clusterPort"], "migrate", "start restart", "finish restart",
	}, fake.calls)
	globOne(t, paths["vaultDir"]+".raft-partial-*")
	assert.Empty(t, archivesOf(t, paths))
}

// With data renamed away and the raft store still in staging, the bloc
// looks empty. The journal is what stops it starting a fresh vault there.
func TestReconcile_ResumesAnUnfinishedSwapAndNeverStartsFresh(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	backup := paths["vaultDir"] + ".file-backup-20261006-110000"
	writeRaftData(t, paths["vaultDir"]+".raft-migrating")
	require.NoError(t, os.Rename(paths["vaultDir"], backup))
	writeJournal(t, paths, "swapping", backup)

	fake := &fakeInception{probe: stoppedProbe()}

	require.NoError(t, runReconcile(t, paths, fake))
	assert.Equal(t, []string{
		"probe", "stop", "cluster-port " + paths["clusterPort"], "start restart", "finish restart",
	}, fake.calls)
	assert.Equal(t, vaultDataRaft, classifyVaultData(paths["vaultDir"]))
	assert.DirExists(t, filepath.Join(backup, "core"))
	assert.Empty(t, archivesOf(t, paths))
}

func TestReconcile_RestartFailureAfterAResumedSwapNeverArchives(t *testing.T) {
	paths := reconcilePaths(t)
	writeKeys(t, paths, true, true)

	backup := paths["vaultDir"] + ".file-backup-20261006-110000"
	writeFileData(t, backup)
	writeRaftData(t, paths["vaultDir"])
	writeJournal(t, paths, "swapping", backup)

	fake := &fakeInception{probe: stoppedProbe(), startErrs: []error{ErrVaultKeysRejected}}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrRestartAfterMigrationFailed)
	assert.Contains(t, err.Error(), backup)
	assert.NotContains(t, fake.calls, "start fresh")
	assert.Empty(t, archivesOf(t, paths))
}

// A migration in flight is never mistaken for a healthy vault, even when a
// vault answers as one, because the journal says the disk is mid-change.
func TestReconcile_JournalSkipsTheFastPath(t *testing.T) {
	paths := reconcilePaths(t)
	writeKeys(t, paths, true, true)

	backup := paths["vaultDir"] + ".file-backup-20261006-110000"
	writeFileData(t, backup)
	writeRaftData(t, paths["vaultDir"])
	writeJournal(t, paths, "swapping", backup)

	fake := &fakeInception{probe: healthyRaftProbe(), session: true, target: true}

	require.NoError(t, runReconcile(t, paths, fake))
	assert.Contains(t, fake.calls, "stop")
	assert.Contains(t, fake.calls, "start restart")
	assert.NoFileExists(t, paths["vaultDir"]+".raft-migration.json")
}

func TestReconcile_UntrustedJournalChangesNothing(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)
	require.NoError(t, os.WriteFile(paths["vaultDir"]+".raft-migration.json", []byte("{"), 0o600))

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: stoppedProbe()}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrMigrationJournalInvalid)
	assert.Equal(t, []string{"probe"}, fake.calls)
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
}

// A journal that says the copy was running while data is no longer the file
// store does not describe anything ocfp left, so nothing moves.
func TestReconcile_CopyJournalWithoutFileDataChangesNothing(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)
	writeJournal(t, paths, "migrating", paths["vaultDir"]+".file-backup-20261006-110000")

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: stoppedProbe()}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrMigrationJournalInvalid)
	assert.NotContains(t, strings.Join(fake.calls, "\n"), "start")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
}

func runningFileProbe() vaultProbe {
	return vaultProbe{state: vaultProbeVault, initialized: true, storageType: "file"}
}

// A running file vault with a key missing gets the key back while it still
// runs, because the log and safe's target only help a live vault. Then it is
// stopped, and only then migrated.
func TestReconcile_RunningFileVaultRecoversKeysThenStopsThenMigrates(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, false, true)

	fake := &fakeInception{
		probe:   runningFileProbe(),
		session: true,
		migrate: fakeMigration(t),
		recover: func(paths map[string]string) { writeKeys(t, paths, true, false) },
	}

	require.NoError(t, runReconcile(t, paths, fake))
	assert.Equal(t, []string{
		"probe", "recover-keys", "stop", "cluster-port " + paths["clusterPort"],
		"migrate", "start restart", "finish restart",
	}, fake.calls)
	assert.Empty(t, archivesOf(t, paths))
}

// A running vault over File data is migrated even when its seal status does
// not report a storage type, and with both keys there is nothing to recover.
func TestReconcile_RunningVaultOverFileDataStopsThenMigrates(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{
		probe:   vaultProbe{state: vaultProbeVault, initialized: true},
		session: true,
		migrate: fakeMigration(t),
	}

	require.NoError(t, runReconcile(t, paths, fake))
	assert.Equal(t, []string{
		"probe", "stop", "cluster-port " + paths["clusterPort"], "migrate", "start restart", "finish restart",
	}, fake.calls)
}

// An open vault, initialized and unsealed, still serves its secrets, but once
// it stops it can be reopened only with both keys. When recovery cannot save
// them, the run fails with the keys-not-saved error before it keeps a token,
// stops, archives, or starts anything, and the vault keeps running. This
// holds for every open vault that is not healthy: one on file data, an engine
// whose session is gone, and one with a migration journal.
func TestReconcile_OpenVaultWithoutRecoverableKeysIsLeftRunning(t *testing.T) {
	for name, tc := range map[string]struct {
		probe        vaultProbe
		file         bool
		session      bool
		root, unseal bool
		journal      bool
	}{
		"file data, no root token":        {probe: runningFileProbe(), file: true, session: true, unseal: true},
		"file data, no unseal key":        {probe: runningFileProbe(), file: true, session: true, root: true},
		"raft engine without its session": {probe: healthyRaftProbe(), unseal: true},
		"raft engine, neither key":        {probe: healthyRaftProbe()},
		"migration journal":               {probe: runningFileProbe(), file: true, session: true, root: true, journal: true},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)

			if tc.file {
				writeFileData(t, paths["vaultDir"])
			} else {
				writeRaftData(t, paths["vaultDir"])
			}

			writeKeys(t, paths, tc.root, tc.unseal)

			if tc.journal {
				writeJournal(t, paths, migratePhaseMigrating, paths["vaultDir"]+".file-backup-20261006-110000")
			}

			before := treeDigest(t, blocDir(paths))
			fake := &fakeInception{
				probe: tc.probe, session: tc.session, migrate: fakeMigration(t), targetToken: safeRCToken,
			}

			err := runReconcile(t, paths, fake)
			require.ErrorIs(t, err, ErrInceptionKeysNotSaved)
			assert.Contains(t, err.Error(), "still running")
			assert.NotContains(t, err.Error(), testSealKey)
			assert.NotContains(t, err.Error(), "SENTINEL")
			assert.Equal(t, []string{"probe", "recover-keys"}, fake.calls,
				"nothing may be stopped, migrated, or started while the keys are not saved")
			assert.Equal(t, before, treeDigest(t, blocDir(paths)),
				"no token is kept and nothing is archived or moved")
			assert.Empty(t, archivesOf(t, paths))
		})
	}
}

// When recovery saves the missing key, an open vault that is not healthy goes
// on to the stop and the restart or migration as before.
func TestReconcile_OpenVaultWhoseKeysAreRecoveredIsRestarted(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, false)

	fake := &fakeInception{
		probe:   healthyRaftProbe(),
		recover: func(paths map[string]string) { writeKeys(t, paths, false, true) },
	}

	require.NoError(t, runReconcile(t, paths, fake))
	assert.Equal(t, []string{
		"probe", "recover-keys", "stop", "cluster-port " + paths["clusterPort"], "start restart", "finish restart",
	}, fake.calls)
	assert.Empty(t, archivesOf(t, paths))
}

// A sealed or never-initialized engine holds nothing in memory that its data
// and key files do not, so stopping it loses nothing. Without both keys the
// run then refuses, with its data and its one key left in place.
func TestReconcile_SealedRunningVaultWithoutKeysIsRefused(t *testing.T) {
	for name, probe := range map[string]vaultProbe{
		"sealed":            {state: vaultProbeVault, initialized: true, sealed: true, storageType: "file"},
		"never initialized": {state: vaultProbeVault, sealed: true, storageType: "file"},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeFileData(t, paths["vaultDir"])
			writeKeys(t, paths, false, true)

			fake := &fakeInception{probe: probe, session: true, migrate: fakeMigration(t)}

			err := runReconcile(t, paths, fake)
			assert.Equal(t, "recover-keys", fake.calls[1])
			requireRefusedForMissingKeys(t, paths, fake, err)
			assert.DirExists(t, filepath.Join(paths["vaultDir"], "core"))
		})
	}
}

// A file vault that will not stop, or whose port or data stays held, must
// never be migrated: the copy would race a live engine writing the store.
func TestReconcile_RunningFileVaultThatWillNotStopIsNotMigrated(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{
		probe:    runningFileProbe(),
		session:  true,
		migrate:  fakeMigration(t),
		stopErrs: []error{ErrVaultWouldNotStop},
	}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrVaultWouldNotStop)
	assert.NotContains(t, fake.calls, "migrate")
	assert.NotContains(t, strings.Join(fake.calls, "\n"), "start")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
}

// Keys can only be recovered from a vault that runs, and a vault that is not
// this bloc's is never asked.
func TestReconcile_KeyRecoveryOnlyForTheBlocsRunningVault(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, false, true)

	fake := &fakeInception{probe: stoppedProbe(), migrate: fakeMigration(t)}
	require.ErrorIs(t, runReconcile(t, paths, fake), ErrInceptionKeysMissing)
	assert.NotContains(t, fake.calls, "recover-keys")

	other := reconcilePaths(t)
	writeFileData(t, other["vaultDir"])
	writeKeys(t, other, false, true)

	stranger := &fakeInception{probe: runningFileProbe(), notOurs: true}
	require.ErrorIs(t, runReconcile(t, other, stranger), ErrInceptionPortTaken)
	assert.NotContains(t, stranger.calls, "recover-keys")
}

// Teardown stops whatever listens on the bloc's port, so it first makes sure
// that is this bloc's own vault. A stranger, a sibling bloc's vault on a
// colliding port, or a listener whose owner lsof cannot name is refused.
func TestGuardInceptionTeardown(t *testing.T) {
	for name, tc := range map[string]struct {
		probe   vaultProbe
		notOurs bool
		ownsErr error
		want    error
	}{
		"stopped":       {probe: stoppedProbe()},
		"ours":          {probe: healthyRaftProbe()},
		"stranger":      {probe: vaultProbe{state: vaultProbeStranger}, want: ErrInceptionPortTaken},
		"sibling":       {probe: healthyRaftProbe(), notOurs: true, want: ErrInceptionPortTaken},
		"owner unknown": {probe: healthyRaftProbe(), ownsErr: ErrVaultOwnerUnknown, want: ErrVaultOwnerUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeRaftData(t, paths["vaultDir"])

			fake := &fakeInception{probe: tc.probe, notOurs: tc.notOurs, ownsErr: tc.ownsErr}
			steps := fake.steps()

			err := guardInceptionTeardown(context.Background(), paths, steps.probe, steps.ownsVault)
			if tc.want == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.want)
		})
	}
}

// writeTruncatedUnsealKey leaves unseal.keys holding the first n digits of
// the key, the way a capture cut at a tmux pane's edge saved it.
func writeTruncatedUnsealKey(t *testing.T, paths map[string]string, n int) {
	t.Helper()

	require.NoError(t, os.WriteFile(paths["unsealKeysFile"], []byte(testSealKey[:n]+"\n"), 0o600))
}

// A cut-short unseal key would be refused by the engine and archive a vault
// whose real key still sits in safe's output. The whole key is recovered
// from that output first, and the vault restarts in place.
func TestReconcile_TruncatedUnsealKeyIsRepairedFromTheOutput(t *testing.T) {
	line := "Your Vault Seal Key is " + testSealKey + "\n"

	for name, place := range map[string]func(fake *fakeVaultOps, paths map[string]string){
		"log": func(_ *fakeVaultOps, paths map[string]string) {
			require.NoError(t, os.WriteFile(paths["logFile"], []byte("Now targeting x\n"+line), 0o600))
		},
		"previous log": func(_ *fakeVaultOps, paths map[string]string) {
			require.NoError(t, os.WriteFile(paths["logFile"], []byte("Now targeting x\n"), 0o600))
			require.NoError(t, os.WriteFile(paths["logFile"]+".previous", []byte(line), 0o600))
		},
		"pane": func(fake *fakeVaultOps, paths map[string]string) {
			fakePaneHistory(fake, paths, line)
		},
		"older log": func(_ *fakeVaultOps, paths map[string]string) {
			require.NoError(t, os.WriteFile(paths["logFile"], []byte("Now targeting x\n"), 0o600))
			require.NoError(t, os.WriteFile(paths["logFile"]+".previous", []byte("Now targeting x\n"), 0o600))
			require.NoError(t, os.WriteFile(paths["logFile"]+".previous-20261006-110000", []byte(line), 0o600))
		},
	} {
		t.Run(name, func(t *testing.T) {
			ops := installFakeVaultOps(t)
			paths := reconcilePaths(t)
			require.NoError(t, os.MkdirAll(paths["logDir"], 0o700))
			writeRaftData(t, paths["vaultDir"])
			writeKeys(t, paths, true, false)
			writeTruncatedUnsealKey(t, paths, 57)
			place(ops, paths)

			fake := &fakeInception{probe: stoppedProbe()}

			require.NoError(t, runReconcile(t, paths, fake))
			assert.Equal(t, []string{
				"probe", "stop", "cluster-port " + paths["clusterPort"], "start restart", "finish restart",
			}, fake.calls)
			assert.Equal(t, testSealKey+"\n", readKey(t, paths["unsealKeysFile"]))

			info, err := os.Stat(paths["unsealKeysFile"])
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
			assert.Empty(t, archivesOf(t, paths))
		})
	}
}

// When safe's output holds no key that the truncated file is the start of,
// nothing is stopped, started, archived, or rewritten, and the error says
// what to do without printing any part of the key.
func TestReconcile_TruncatedUnsealKeyNotFoundChangesNothing(t *testing.T) {
	other := strings.Repeat("fedcba9876543210", 4)

	for name, logged := range map[string]string{
		"no output":           "",
		"another vault's key": "Your Vault Seal Key is " + other + "\n",
		"the same cut key":    "Your Vault Seal Key is " + testSealKey[:57] + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			installFakeVaultOps(t)
			paths := reconcilePaths(t)
			require.NoError(t, os.MkdirAll(paths["logDir"], 0o700))
			writeRaftData(t, paths["vaultDir"])
			writeKeys(t, paths, true, false)
			writeTruncatedUnsealKey(t, paths, 57)
			require.NoError(t, os.WriteFile(paths["logFile"], []byte(logged), 0o600))

			before := treeDigest(t, blocDir(paths))
			fake := &fakeInception{probe: stoppedProbe()}

			err := runReconcile(t, paths, fake)
			require.ErrorIs(t, err, ErrUnsealKeyFileMalformed)
			assert.NotContains(t, err.Error(), testSealKey[:20])
			assert.Contains(t, err.Error(), paths["unsealKeysFile"])
			assert.Contains(t, err.Error(), paths["logFile"])
			assert.Equal(t, []string{"probe"}, fake.calls)
			assert.Equal(t, before, treeDigest(t, blocDir(paths)))
			assert.Empty(t, archivesOf(t, paths))
		})
	}
}

// makeUnreadable takes every permission off path for the rest of the test,
// and gives them back before the temp dir is cleaned up or the tree hashed.
func makeUnreadable(t *testing.T, path string) func() {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode")
	}

	require.NoError(t, os.Chmod(path, 0o000))

	restore := func() { _ = os.Chmod(path, 0o600) }
	t.Cleanup(restore)

	return restore
}

// A key file that exists but cannot be read says nothing about the key, so
// the run stops with an error naming the file and touches nothing, whether
// the vault is stopped or running and healthy.
func TestReconcile_UnreadableKeyFileChangesNothing(t *testing.T) {
	for _, keyFile := range []string{"rootKeyFile", "unsealKeysFile"} {
		for name, probe := range map[string]vaultProbe{"stopped": stoppedProbe(), "healthy": healthyRaftProbe()} {
			t.Run(keyFile+" "+name, func(t *testing.T) {
				paths := reconcilePaths(t)
				writeRaftData(t, paths["vaultDir"])
				writeKeys(t, paths, true, true)

				before := treeDigest(t, blocDir(paths))
				restore := makeUnreadable(t, paths[keyFile])
				fake := &fakeInception{probe: probe, session: true, target: true}

				err := runReconcile(t, paths, fake)
				require.ErrorIs(t, err, ErrInceptionKeyFileUnreadable)
				assert.Contains(t, err.Error(), paths[keyFile])
				assert.NotContains(t, err.Error(), testSealKey)
				assert.Equal(t, []string{"probe"}, fake.calls)

				restore()
				assert.Equal(t, before, treeDigest(t, blocDir(paths)))
				assert.Empty(t, archivesOf(t, paths))
			})
		}
	}
}

// An empty key file holds nothing to protect, so it counts as missing even
// when its mode would keep it from being read.
func TestReadKeyFile(t *testing.T) {
	dir := t.TempDir()

	value, err := keyfile.Read(filepath.Join(dir, "absent"))
	require.NoError(t, err)
	assert.Empty(t, value)

	held := filepath.Join(dir, "held")
	require.NoError(t, os.WriteFile(held, []byte("\n  s.TOKEN-VALUE \n"), 0o600))
	value, err = keyfile.Read(held)
	require.NoError(t, err)
	assert.Equal(t, "s.TOKEN-VALUE", value)

	empty := filepath.Join(dir, "empty")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	makeUnreadable(t, empty)
	value, err = keyfile.Read(empty)
	require.NoError(t, err)
	assert.Empty(t, value)

	makeUnreadable(t, held)
	_, err = keyfile.Read(held)
	require.ErrorIs(t, err, ErrInceptionKeyFileUnreadable)
	assert.Contains(t, err.Error(), held)
	assert.NotContains(t, err.Error(), "s.TOKEN-VALUE")

	_, err = keyfile.Read(dir)
	require.ErrorIs(t, err, ErrInceptionKeyFileUnreadable, "a directory is not a key file")
}

// safe hands the engine only the first line of unseal.keys, and only with
// its line ending stripped, so a whole key behind a blank line or beside a
// stray space reaches the engine as a key it refuses. The key file is
// rewritten to hold the key and a newline before anything starts, and the
// vault restarts in place rather than being archived. root.key gets the same
// treatment, because re-targeting feeds it to safe the same way.
func TestReconcile_KeyFilesWithStrayWhitespaceAreRewritten(t *testing.T) {
	const token = "s.ROOT-TOKEN-SENTINEL"

	for name, wrap := range map[string]func(string) string{
		"leading blank line":  func(k string) string { return "\n" + k + "\n" },
		"trailing space":      func(k string) string { return k + " \n" },
		"leading spaces":      func(k string) string { return "  " + k + "\n" },
		"no newline":          func(k string) string { return k },
		"crlf":                func(k string) string { return k + "\r\n" },
		"trailing blank line": func(k string) string { return k + "\n\n" },
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeRaftData(t, paths["vaultDir"])
			require.NoError(t, os.WriteFile(paths["unsealKeysFile"], []byte(wrap(testSealKey)), 0o600))
			require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte(wrap(token)), 0o600))

			fake := &fakeInception{probe: stoppedProbe()}

			require.NoError(t, runReconcile(t, paths, fake))
			assert.Equal(t, []string{
				"probe", "stop", "cluster-port " + paths["clusterPort"], "start restart", "finish restart",
			}, fake.calls)
			assert.Equal(t, testSealKey+"\n", readKey(t, paths["unsealKeysFile"]))
			assert.Equal(t, token+"\n", readKey(t, paths["rootKeyFile"]))

			for _, keyFile := range []string{paths["rootKeyFile"], paths["unsealKeysFile"]} {
				info, err := os.Stat(keyFile)
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
			}

			assert.Empty(t, archivesOf(t, paths))
		})
	}
}

// A key file already in its canonical form is left exactly as it is.
func TestReconcile_CanonicalKeyFilesAreNotRewritten(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := map[string]os.FileInfo{}

	for _, keyFile := range []string{paths["rootKeyFile"], paths["unsealKeysFile"]} {
		info, err := os.Stat(keyFile)
		require.NoError(t, err)

		before[keyFile] = info
	}

	require.NoError(t, runReconcile(t, paths, &fakeInception{probe: stoppedProbe()}))

	for keyFile, info := range before {
		after, err := os.Stat(keyFile)
		require.NoError(t, err)
		assert.True(t, os.SameFile(info, after), "%s was replaced", keyFile)
	}
}

// safeRCToken is the token the fake ~/.saferc holds for the bloc's target.
const safeRCToken = "s.SAFERC-TOKEN-SENTINEL"

// tokenSidecars lists the copies of safe's token kept beside root.key.
func tokenSidecars(t *testing.T, paths map[string]string) []string {
	t.Helper()

	sidecars, err := filepath.Glob(paths["rootKeyFile"] + ".saferc-*")
	require.NoError(t, err)

	return sidecars
}

// Stopping the vault deletes its safe target, which may hold the only copy of
// the root token. A stopped vault whose root.key is missing gets the token
// from the target before the stop, and then restarts in place.
func TestReconcile_StoppedVaultWithoutRootKeyKeepsTheTargetToken(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, false, true)

	fake := &fakeInception{probe: stoppedProbe(), targetToken: safeRCToken}

	require.NoError(t, runReconcile(t, paths, fake))
	assert.Equal(t, []string{
		"probe", "stop", "cluster-port " + paths["clusterPort"], "start restart", "finish restart",
	}, fake.calls)
	assert.Equal(t, safeRCToken+"\n", readKey(t, paths["rootKeyFile"]))

	info, err := os.Stat(paths["rootKeyFile"])
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.Empty(t, archivesOf(t, paths))
	assert.Empty(t, tokenSidecars(t, paths), "root.key now holds the token, so no copy is needed")
}

// A token of the wrong shape is never written to root.key. It is still kept
// beside it, because the stop deletes the target that holds it.
func TestReconcile_MalformedTargetTokenIsKeptButNeverUsed(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, false, true)

	fake := &fakeInception{probe: stoppedProbe(), targetToken: "s.BAD TOKEN"}

	requireRefusedForMissingKeys(t, paths, fake, runReconcile(t, paths, fake))
	assert.NoFileExists(t, paths["rootKeyFile"])

	kept := tokenSidecars(t, paths)
	require.Len(t, kept, 1)
	assert.Equal(t, "s.BAD TOKEN\n", readKey(t, kept[0]))
}

// When root.key and the target disagree, neither is thrown away: the target's
// token is kept beside root.key, inside <bloc>/vault, before the stop, and a
// second run does not keep a second copy.
func TestReconcile_StoppedVaultWithADifferentRootKeyKeepsBoth(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	for range 2 {
		fake := &fakeInception{probe: stoppedProbe(), targetToken: safeRCToken}

		require.NoError(t, runReconcile(t, paths, fake))
		assert.Equal(t, []string{
			"probe", "stop", "cluster-port " + paths["clusterPort"], "start restart", "finish restart",
		}, fake.calls)
		assert.Equal(t, []string{paths["rootKeyFile"]}, fake.tokenFiles, "root.key opens the vault when the engine takes it")
	}

	assert.Equal(t, "s.ROOT-TOKEN-SENTINEL\n", readKey(t, paths["rootKeyFile"]))

	sidecars := tokenSidecars(t, paths)
	require.Len(t, sidecars, 1)
	assert.Equal(t, filepath.Dir(paths["rootKeyFile"]), filepath.Dir(sidecars[0]))
	assert.Equal(t, safeRCToken+"\n", readKey(t, sidecars[0]))

	info, err := os.Stat(sidecars[0])
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func tokenRejected() error {
	return fmt.Errorf("%w: !! The root token in x was rejected by OpenBao", ErrVaultRootTokenRejected)
}

// A rejected root.key is retried once with the target's token before any
// archive. When the engine takes it, root.key holds it from then on and the
// rejected token is kept beside it.
func TestReconcile_RejectedRootKeyRetriesWithTheTargetToken(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{probe: stoppedProbe(), targetToken: safeRCToken, startErrs: []error{tokenRejected()}}

	require.NoError(t, runReconcile(t, paths, fake))
	assert.Equal(t, []string{
		"probe", "stop", "cluster-port " + paths["clusterPort"],
		"start restart", "stop", "start restart", "finish restart",
	}, fake.calls)

	sidecars := tokenSidecars(t, paths)
	require.Len(t, sidecars, 1)
	assert.Equal(t, []string{paths["rootKeyFile"], sidecars[0]}, fake.tokenFiles)
	assert.Equal(t, safeRCToken+"\n", readKey(t, paths["rootKeyFile"]))

	rejected, err := filepath.Glob(paths["rootKeyFile"] + ".rejected-*")
	require.NoError(t, err)
	require.Len(t, rejected, 1)
	assert.Equal(t, "s.ROOT-TOKEN-SENTINEL\n", readKey(t, rejected[0]))
	assert.Empty(t, archivesOf(t, paths))
}

// When the engine rejects the target's token too, the run refuses with both
// tokens left where they are, and the error names both.
func TestReconcile_BothTokensRejectedRefuses(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{
		probe: stoppedProbe(), targetToken: safeRCToken, startErrs: []error{tokenRejected(), tokenRejected()},
	}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrInceptionKeysRefused)
	assert.Equal(t, []string{
		"probe", "stop", "cluster-port " + paths["clusterPort"],
		"start restart", "stop", "start restart", "stop",
	}, fake.calls)
	assert.Empty(t, archivesOf(t, paths))
	assert.Equal(t, "s.ROOT-TOKEN-SENTINEL\n", readKey(t, paths["rootKeyFile"]))

	kept := tokenSidecars(t, paths)
	require.Len(t, kept, 1)
	assert.Equal(t, safeRCToken+"\n", readKey(t, kept[0]))
	assert.Contains(t, err.Error(), paths["rootKeyFile"])
	assert.Contains(t, err.Error(), kept[0])
	assert.NotContains(t, err.Error(), safeRCToken)
}

// A copy of safe's token that an earlier run kept beside root.key may be the
// token the vault takes, so a refused root token names it, and never quotes
// it. A copy the retry already tried is not offered again.
func TestReconcile_RefusedRootTokenNamesKeptTokenCopies(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	earlier := paths["rootKeyFile"] + ".saferc-20261001-090000"
	require.NoError(t, os.WriteFile(earlier, []byte("s.EARLIER-TOKEN-SENTINEL\n"), 0o600))

	fake := &fakeInception{
		probe: stoppedProbe(), targetToken: safeRCToken, startErrs: []error{tokenRejected(), tokenRejected()},
	}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrInceptionKeysRefused)

	kept := tokenSidecars(t, paths)
	require.Len(t, kept, 2)

	tried := kept[0]
	if tried == earlier {
		tried = kept[1]
	}

	msg := err.Error()
	assert.Contains(t, msg, "safe's target held a different root token, which ocfp kept in "+earlier)
	assert.Equal(t, 1, strings.Count(msg, tried), "the copy the retry tried is named once, as refused")
	assert.NotContains(t, msg, "SENTINEL")
}

// A retry that fails for any reason other than a refused key says nothing
// about the keys, so nothing is archived and root.key is left as it was.
func TestReconcile_RetryThatFailsOtherwiseChangesNothing(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{
		probe: stoppedProbe(), targetToken: safeRCToken, startErrs: []error{tokenRejected(), ErrVaultNotReady},
	}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrVaultNotReady)
	assert.Equal(t, "stop", fake.calls[len(fake.calls)-1])
	assert.NotContains(t, fake.calls, "start fresh")
	assert.Equal(t, "s.ROOT-TOKEN-SENTINEL\n", readKey(t, paths["rootKeyFile"]))
	assert.Empty(t, archivesOf(t, paths))
}

// Only a refused token is retried. A refused unseal key, or a target that
// holds the very token root.key holds, has nothing else to try.
func TestReconcile_NoRetryWithoutADifferentToken(t *testing.T) {
	for name, tc := range map[string]struct {
		targetToken string
		startErr    error
	}{
		"unseal key refused": {targetToken: safeRCToken, startErr: unsealKeyRejected()},
		"same token":         {targetToken: "s.ROOT-TOKEN-SENTINEL", startErr: tokenRejected()},
		"no target":          {startErr: tokenRejected()},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeRaftData(t, paths["vaultDir"])
			writeKeys(t, paths, true, true)

			fake := &fakeInception{probe: stoppedProbe(), targetToken: tc.targetToken, startErrs: []error{tc.startErr}}

			err := runReconcile(t, paths, fake)
			require.ErrorIs(t, err, ErrInceptionKeysRefused)
			assert.Equal(t, []string{
				"probe", "stop", "cluster-port " + paths["clusterPort"], "start restart", "stop",
			}, fake.calls)
			assert.Empty(t, archivesOf(t, paths))
		})
	}
}

// A running vault whose keys were never both saved cannot be reopened once
// it stops, so it is not healthy however well it runs. Its keys are recovered
// while it runs, and when that fails the run fails with the vault still
// running and nothing stopped, archived, or changed.
func TestReconcile_HealthyVaultWithoutSavedKeysFails(t *testing.T) {
	for name, tc := range map[string]struct {
		root, unseal bool
		malformed    string // a value of the wrong shape in root.key
	}{
		"no unseal key":        {root: true},
		"no root token":        {unseal: true},
		"neither key":          {},
		"malformed root token": {unseal: true, malformed: "s.BAD TOKEN"},
		"one shared key file":  {root: true, unseal: true, malformed: "shared"},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeRaftData(t, paths["vaultDir"])
			writeKeys(t, paths, tc.root, tc.unseal)

			switch tc.malformed {
			case "":
			case "shared":
				paths["unsealKeysFile"] = paths["rootKeyFile"]
			default:
				require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte(tc.malformed+"\n"), 0o600))
			}

			before := treeDigest(t, blocDir(paths))
			fake := &fakeInception{probe: healthyRaftProbe(), session: true, target: true}

			err := runReconcile(t, paths, fake)
			require.ErrorIs(t, err, ErrInceptionKeysNotSaved)
			assert.Contains(t, err.Error(), "still running")
			assert.Contains(t, err.Error(), paths["logFile"])
			assert.NotContains(t, err.Error(), testSealKey)
			assert.NotContains(t, err.Error(), "ROOT-TOKEN-SENTINEL")
			assert.NotContains(t, fake.calls, "stop")
			assert.Equal(t, before, treeDigest(t, blocDir(paths)))
			assert.Empty(t, archivesOf(t, paths))
		})
	}
}

// The keys of a vault are printed once, on the start that created it, so after
// a few restarts they sit in an older log. The keys-not-saved error names
// every log that exists, newest first, so the operator knows where to look.
func TestReconcile_KeysNotSavedNamesEveryLog(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, false)
	require.NoError(t, os.MkdirAll(paths["logDir"], 0o700))

	logs := []string{
		paths["logFile"],
		paths["logFile"] + ".previous",
		paths["logFile"] + ".previous-20261006-110000-2",
		paths["logFile"] + ".previous-20261006-110000",
	}

	for _, logFile := range logs {
		require.NoError(t, os.WriteFile(logFile, []byte("Now targeting x\n"), 0o600))
	}

	fake := &fakeInception{probe: healthyRaftProbe(), session: true, target: true}

	err := runReconcile(t, paths, fake)
	require.ErrorIs(t, err, ErrInceptionKeysNotSaved)

	last := -1

	for _, logFile := range logs {
		at := strings.Index(err.Error(), logFile+",")
		if at < 0 {
			at = strings.Index(err.Error(), logFile+" ")
		}

		if at < 0 && strings.HasSuffix(err.Error(), logFile) {
			at = len(err.Error()) - len(logFile)
		}

		require.GreaterOrEqual(t, at, 0, "the error names %s", logFile)
		assert.Greater(t, at, last, "the logs are named newest first")
		last = at
	}
}

// When recovery finds the missing key, the healthy vault is left running and
// the run succeeds.
func TestReconcile_HealthyVaultRecoversMissingKeys(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, false)

	fake := &fakeInception{
		probe: healthyRaftProbe(), session: true, target: true,
		recover: func(paths map[string]string) { writeKeys(t, paths, false, true) },
	}

	require.NoError(t, runReconcile(t, paths, fake))
	assert.Equal(t, []string{"probe", "recover-keys"}, fake.calls)
	assert.Equal(t, testSealKey+"\n", readKey(t, paths["unsealKeysFile"]))
}

// A stopped vault with no unseal.keys is not archived while its log, or the
// log of the start before, still holds a whole unseal key. The newest log
// wins, and the vault restarts in place, or migrates when it is file-backed.
func TestReconcile_StoppedVaultRecoversTheUnsealKeyFromItsLogs(t *testing.T) {
	other := strings.Repeat("fedcba9876543210", 4)
	line := func(key string) string { return "Now targeting x\nYour OpenBao Seal Key is " + key + "\n" }

	for name, tc := range map[string]struct {
		log, previous string
		older         map[string]string // timestamp, and any -N, of each .previous-<ts> log
		file          bool
		want          string
	}{
		"in the log":          {log: line(testSealKey), want: testSealKey},
		"in the previous log": {log: "Now targeting x\n", previous: line(testSealKey), want: testSealKey},
		"newest log wins":     {log: line(testSealKey), previous: line(other), want: testSealKey},
		"file data migrates":  {log: line(testSealKey), file: true, want: testSealKey},
		"blank key file":      {previous: line(testSealKey), want: testSealKey},
		"in an older log": {
			log: "Now targeting x\n", previous: "Now targeting x\n",
			older: map[string]string{"20261006-110000": line(testSealKey)}, want: testSealKey,
		},
		"newest older log wins": {
			older: map[string]string{
				"20261006-090000":    line(other),
				"20261006-110000":    line(other),
				"20261006-110000-2":  line(other),
				"20261006-110000-10": line(testSealKey),
			},
			want: testSealKey,
		},
		"previous beats older": {
			previous: line(testSealKey), older: map[string]string{"20261006-110000": line(other)}, want: testSealKey,
		},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			require.NoError(t, os.MkdirAll(paths["logDir"], 0o700))

			if tc.file {
				writeFileData(t, paths["vaultDir"])
			} else {
				writeRaftData(t, paths["vaultDir"])
			}

			writeKeys(t, paths, true, false)

			if name == "blank key file" {
				require.NoError(t, os.WriteFile(paths["unsealKeysFile"], []byte("\n"), 0o600))
			}

			if tc.log != "" {
				require.NoError(t, os.WriteFile(paths["logFile"], []byte(tc.log), 0o600))
			}

			if tc.previous != "" {
				require.NoError(t, os.WriteFile(paths["logFile"]+".previous", []byte(tc.previous), 0o600))
			}

			for stamp, body := range tc.older {
				require.NoError(t, os.WriteFile(paths["logFile"]+".previous-"+stamp, []byte(body), 0o600))
			}

			fake := &fakeInception{probe: stoppedProbe(), migrate: fakeMigration(t)}

			require.NoError(t, runReconcile(t, paths, fake))
			assert.Equal(t, tc.want+"\n", readKey(t, paths["unsealKeysFile"]))
			assert.Equal(t, []string{"start restart", "finish restart"}, fake.calls[len(fake.calls)-2:])
			assert.NotContains(t, fake.calls, "start fresh")
			assert.Empty(t, archivesOf(t, paths))

			info, err := os.Stat(paths["unsealKeysFile"])
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		})
	}
}

// Only a whole key counts. A log with a cut-short key, or none, leaves the
// unseal key missing, so the run refuses with the data and keys in place.
func TestReconcile_StoppedVaultWithoutAKeyInItsLogsIsRefused(t *testing.T) {
	for name, logged := range map[string]string{
		"no log":        "",
		"no key":        "Now targeting x\n",
		"cut-short key": "Your OpenBao Seal Key is " + testSealKey[:57] + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			require.NoError(t, os.MkdirAll(paths["logDir"], 0o700))
			writeRaftData(t, paths["vaultDir"])
			writeKeys(t, paths, true, false)

			if logged != "" {
				require.NoError(t, os.WriteFile(paths["logFile"], []byte(logged), 0o600))
			}

			fake := &fakeInception{probe: stoppedProbe()}

			requireRefusedForMissingKeys(t, paths, fake, runReconcile(t, paths, fake))
		})
	}
}
