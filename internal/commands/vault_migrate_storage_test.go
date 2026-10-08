package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// migrateStorageSteps narrows the scripted reconcile steps to the ones
// migrate-storage may take. Its restart is vault start's, wired the way
// production wires it, so every start the fake records carries the mode
// production chose. The port answers each of probes in turn and then
// f.probe, so a test can show a running vault that the stop takes down.
func (f *fakeInception) migrateStorageSteps(canMigrateErr error, probes ...vaultProbe) migrateStorageSteps {
	scripted := f.steps()
	start := f.startSteps()

	start.probe = func(context.Context, string) vaultProbe {
		f.record("probe")

		if len(probes) == 0 {
			return f.probe
		}

		next := probes[0]
		probes = probes[1:]

		return next
	}

	return migrateStorageSteps{
		vaultStart: start,
		migrate: func(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) (string, error) {
			return scripted.migrate(ctx, paths, testTools(), log)
		},
		canMigrate: func(context.Context) error {
			f.record("can-migrate")

			return canMigrateErr
		},
		engine:         testTools().engine,
		engineVersion:  func(context.Context) string { return "OpenBao v2.7.0 (test build)" },
		rootTokenWorks: f.rootTokenWorks,
	}
}

func newMigrateStorageRun(paths map[string]string, steps migrateStorageSteps, out *bytes.Buffer) *migrateStorageRun {
	return &migrateStorageRun{paths: paths, steps: steps, log: zap.NewNop().Sugar(), out: out}
}

// runMigrateStorage runs migrate-storage over paths with fake's steps and
// returns its error and what it told the operator.
func runMigrateStorage(
	t *testing.T, paths map[string]string, fake *fakeInception, canMigrateErr error, probes ...vaultProbe,
) (string, error) {
	t.Helper()

	var out bytes.Buffer

	err := migrateInceptionVaultStorage(context.Background(),
		newMigrateStorageRun(paths, fake.migrateStorageSteps(canMigrateErr, probes...), &out))

	return out.String(), err
}

// runMigrateStorageDryRun runs migrate-storage's dry run and returns its
// report and its error.
func runMigrateStorageDryRun(
	t *testing.T, paths map[string]string, fake *fakeInception, canMigrateErr error,
) (string, error) {
	t.Helper()

	var out bytes.Buffer

	err := reportInceptionVaultStorage(context.Background(),
		newMigrateStorageRun(paths, fake.migrateStorageSteps(canMigrateErr), &out))

	return out.String(), err
}

// assertNeverReplaced checks that migrate-storage neither archived the
// vault nor started a new one, whatever else it did.
func assertNeverReplaced(t *testing.T, paths map[string]string, fake *fakeInception) {
	t.Helper()

	assert.Empty(t, archivesOf(t, paths), "migrate-storage must never archive")
	assert.NotContains(t, fake.calls, "start fresh", "migrate-storage must never start a new vault")
	assert.NotContains(t, fake.calls, "recover-keys", "migrate-storage must never recover a key")
}

func sealedFileProbe() vaultProbe {
	return vaultProbe{state: vaultProbeVault, initialized: true, sealed: true, storageType: "file"}
}

// A stopped file vault is migrated with the engine and then reopened by vault
// start's own restart, which is what a migration by hand followed by 'ocfp
// vault start' does.
func TestMigrateStorage_StoppedFileVaultMigratesThenRestarts(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{probe: stoppedProbe(), migrate: fakeMigration(t)}

	_, err := runMigrateStorage(t, paths, fake, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{
		"probe", "can-migrate", "cluster-port " + paths["clusterPort"],
		"probe", "stop", "migrate",
		"probe", "cluster-port " + paths["clusterPort"], "probe", "stop", "start restart", "finish restart",
	}, fake.calls)
	assertNeverReplaced(t, paths, fake)
	assert.Equal(t, vaultDataRaft, classifyVaultData(paths["vaultDir"]))
	assert.DirExists(t, filepath.Join(paths["vaultDir"]+".file-backup-20261006-120000", "core"))
	assert.Equal(t, "s.ROOT-TOKEN-SENTINEL\n", readKey(t, paths["rootKeyFile"]), "the keys stay where they were")
	assert.Equal(t, testSealKey+"\n", readKey(t, paths["unsealKeysFile"]))
}

// This bloc's own running vault, sealed or open, is stopped once a fresh
// probe shows it is still this bloc's, and the cluster port is judged only
// after the stop, because the running vault holds it.
func TestMigrateStorage_RunningOwnFileVaultIsStoppedThenMigrated(t *testing.T) {
	for name, probe := range map[string]vaultProbe{
		"sealed": sealedFileProbe(),
		"open":   runningFileProbe(),
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeFileData(t, paths["vaultDir"])
			writeKeys(t, paths, true, true)

			fake := &fakeInception{probe: stoppedProbe(), session: true, migrate: fakeMigration(t)}

			_, err := runMigrateStorage(t, paths, fake, nil, probe, probe)
			require.NoError(t, err)

			assert.Equal(t, []string{
				"probe", "can-migrate",
				"probe", "stop", "cluster-port " + paths["clusterPort"], "migrate",
				"probe", "cluster-port " + paths["clusterPort"], "probe", "stop", "start restart", "finish restart",
			}, fake.calls)
			assertNeverReplaced(t, paths, fake)
		})
	}
}

// The safe target may hold the only copy of the root token, and the stop
// deletes it, so the token is kept in root.key first, as vault start keeps
// it.
func TestMigrateStorage_KeepsTheTargetTokenBeforeTheStop(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{probe: stoppedProbe(), session: true, migrate: fakeMigration(t), targetToken: safeRCToken}

	_, err := runMigrateStorage(t, paths, fake, nil, sealedFileProbe(), sealedFileProbe())
	require.NoError(t, err)

	kept := tokenSidecars(t, paths)
	require.Len(t, kept, 1)
	assert.Equal(t, safeRCToken+"\n", readKey(t, kept[0]))
}

// Only an open vault can say whether it takes the token in root.key, and the
// restart after the migration needs that token, so an open vault is asked
// before the stop and a sealed one is not.
func TestMigrateStorage_ChecksTheRootTokenOfAnOpenVaultOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		probe  vaultProbe
		checks int
	}{
		"open":   {probe: runningFileProbe(), checks: 1},
		"sealed": {probe: sealedFileProbe(), checks: 0},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeFileData(t, paths["vaultDir"])
			writeKeys(t, paths, true, true)

			fake := &fakeInception{probe: stoppedProbe(), session: true, migrate: fakeMigration(t)}

			_, err := runMigrateStorage(t, paths, fake, nil, tc.probe, tc.probe)
			require.NoError(t, err)
			assert.Equal(t, tc.checks, fake.rootTokenChecks)
		})
	}
}

// An open vault that refuses the token in root.key could not be reopened
// after the migration, so it is left running and nothing on disk changes.
// The error names root.key, safe's target, and any copy of a token kept
// beside root.key, and quotes no token.
func TestMigrateStorage_OpenVaultThatRefusesRootKeyIsLeftRunning(t *testing.T) {
	for name, tokenErr := range map[string]error{
		"refused":      fmt.Errorf("%w: test", ErrInceptionRootTokenRefused),
		"cannot check": errors.New("the vault did not answer the lookup"),
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeFileData(t, paths["vaultDir"])
			writeKeys(t, paths, true, true)

			earlier := paths["rootKeyFile"] + ".saferc-20261001-090000"
			require.NoError(t, os.WriteFile(earlier, []byte("s.EARLIER-TOKEN-SENTINEL\n"), 0o600))

			before := treeDigest(t, blocDir(paths))
			fake := &fakeInception{
				probe: stoppedProbe(), session: true, migrate: fakeMigration(t), targetToken: safeRCToken,
				rootTokenErr: tokenErr,
			}

			_, err := runMigrateStorage(t, paths, fake, nil, runningFileProbe(), runningFileProbe())
			require.ErrorIs(t, err, tokenErr)
			assert.NotContains(t, fake.calls, "stop")
			assert.NotContains(t, fake.calls, "migrate")
			assertNeverReplaced(t, paths, fake)
			assert.Equal(t, before, treeDigest(t, blocDir(paths)), "nothing is written, not even a token copy")

			msg := err.Error()
			assert.Contains(t, msg, paths["rootKeyFile"])
			assert.Contains(t, msg, "stopped nothing")
			assert.Contains(t, msg, "safe's target "+paths["vaultName"])
			assert.Contains(t, msg, earlier)
			assert.NotContains(t, msg, "SENTINEL")
		})
	}
}

// The dry run asks an open vault about root.key too, and reports a refusal
// the way the real run would refuse.
func TestMigrateStorage_DryRunReportsARefusedRootToken(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{
		probe: runningFileProbe(), session: true, rootTokenErr: fmt.Errorf("%w: test", ErrInceptionRootTokenRefused),
	}

	out, err := runMigrateStorageDryRun(t, paths, fake, nil)
	require.ErrorIs(t, err, ErrInceptionRootTokenRefused)
	assert.Contains(t, out, "the running vault refuses it")
	assert.Contains(t, out, "would refuse")
	assert.NotContains(t, fake.calls, "stop")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
}

// The dry run sends the token in root.key only to a vault proven to be the
// bloc's own. Whatever else answers on the port, including a vault whose
// owner cannot be told, is never handed the token, whatever the data holds.
func TestMigrateStorage_DryRunAsksOnlyTheBlocsOwnVaultAboutRootKey(t *testing.T) {
	data := map[string]func(t *testing.T, dir string){
		"file": writeFileData,
		"raft": writeRaftData,
		"mixed": func(t *testing.T, dir string) {
			t.Helper()
			writeFileData(t, dir)
			writeRaftData(t, dir)
		},
	}

	for dataName, write := range data {
		for ownerName, tc := range map[string]struct {
			fake   *fakeInception
			checks int
		}{
			"foreign owner": {fake: &fakeInception{probe: runningFileProbe(), session: true, notOurs: true}},
			"unknown owner": {fake: &fakeInception{
				probe: runningFileProbe(), session: true, ownsErr: ErrVaultOwnerUnknown,
			}},
		} {
			t.Run(dataName+" data, "+ownerName, func(t *testing.T) {
				paths := reconcilePaths(t)
				write(t, paths["vaultDir"])
				writeKeys(t, paths, true, true)

				out, _ := runMigrateStorageDryRun(t, paths, tc.fake, nil)
				assert.Zero(t, tc.fake.rootTokenChecks, "the token went to a vault not proven to be the bloc's")
				assert.Contains(t, out, "not proven to be this bloc's")
				assert.NotContains(t, out, "SENTINEL")
			})
		}
	}

	t.Run("the bloc's own open vault is asked", func(t *testing.T) {
		paths := reconcilePaths(t)
		writeFileData(t, paths["vaultDir"])
		writeKeys(t, paths, true, true)

		fake := &fakeInception{probe: runningFileProbe(), session: true}

		out, err := runMigrateStorageDryRun(t, paths, fake, nil)
		require.NoError(t, err)
		assert.Equal(t, 1, fake.rootTokenChecks)
		assert.Contains(t, out, "the running vault takes it")
	})
}

// An open vault serves its secrets, and once it stops it can be reopened only
// with both keys, so keys that are present but not the shape of a key leave
// it running. migrate-storage never recovers a key.
func TestMigrateStorage_OpenVaultWithMalformedKeyIsLeftRunning(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, false, true)
	require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte("not a token\n"), 0o600))

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: stoppedProbe(), session: true, migrate: fakeMigration(t)}

	_, err := runMigrateStorage(t, paths, fake, nil, runningFileProbe(), runningFileProbe())
	require.ErrorIs(t, err, ErrInceptionKeysNotSaved)
	assert.Contains(t, err.Error(), "still running")
	assert.NotContains(t, fake.calls, "stop")
	assert.NotContains(t, fake.calls, "migrate")
	assertNeverReplaced(t, paths, fake)
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
}

// A vault that answers on the port but does not run under the bloc's tmux
// session, such as one started by hand, cannot be proven to be this bloc's.
// It is left running, and the error says to stop it first.
func TestMigrateStorage_VaultStartedByHandIsRefused(t *testing.T) {
	for name, fake := range map[string]*fakeInception{
		"not under the session": {probe: sealedFileProbe(), session: true, notOurs: true},
		"owner unknown":         {probe: sealedFileProbe(), session: true, ownsErr: ErrVaultOwnerUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeFileData(t, paths["vaultDir"])
			writeKeys(t, paths, true, true)

			fake.migrate = fakeMigration(t)
			before := treeDigest(t, blocDir(paths))

			_, err := runMigrateStorage(t, paths, fake, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), paths["tmuxSession"])
			assert.Contains(t, err.Error(), "stop it yourself")
			assert.Contains(t, err.Error(), "stopped nothing")
			assert.NotContains(t, fake.calls, "stop")
			assert.NotContains(t, fake.calls, "migrate")
			assertNeverReplaced(t, paths, fake)
			assert.Equal(t, before, treeDigest(t, blocDir(paths)))

			if fake.notOurs {
				require.ErrorIs(t, err, ErrInceptionPortTaken)
			} else {
				require.ErrorIs(t, err, ErrVaultOwnerUnknown)
			}
		})
	}
}

// What holds the port can change between the checks and the stop. When the
// fresh probe finds a vault this bloc cannot prove is its own, nothing is
// stopped or written.
func TestMigrateStorage_PortThatChangesHandsBeforeTheStopIsRefused(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: stoppedProbe(), notOurs: true, migrate: fakeMigration(t)}

	_, err := runMigrateStorage(t, paths, fake, nil, stoppedProbe(), sealedFileProbe())
	require.ErrorIs(t, err, ErrInceptionPortTaken)
	assert.Contains(t, err.Error(), "migrate-storage stopped nothing")
	assert.NotContains(t, fake.calls, "stop")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
}

func TestMigrateStorage_StrangerOnThePortIsRefused(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: vaultProbe{state: vaultProbeStranger}}

	_, err := runMigrateStorage(t, paths, fake, nil)
	require.ErrorIs(t, err, ErrInceptionPortTaken)
	assert.NotContains(t, fake.calls, "stop")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
}

// A vault already on raft has nothing to migrate. The command says so and
// changes nothing, whether the vault runs or not, and points a stopped one
// at vault start.
func TestMigrateStorage_RaftVaultIsAlreadyMigrated(t *testing.T) {
	for name, tc := range map[string]struct {
		probe vaultProbe
		start bool
	}{
		"stopped": {probe: stoppedProbe(), start: true},
		"running": {probe: healthyRaftProbe()},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeRaftData(t, paths["vaultDir"])
			writeKeys(t, paths, true, true)

			before := treeDigest(t, blocDir(paths))
			fake := &fakeInception{probe: tc.probe, session: true}

			out, err := runMigrateStorage(t, paths, fake, nil)
			require.NoError(t, err)
			assert.Equal(t, []string{"probe"}, fake.calls)
			assert.Equal(t, before, treeDigest(t, blocDir(paths)))
			assert.Contains(t, out, "already on raft storage")
			assert.Contains(t, out, paths["vaultDir"])

			if tc.start {
				assert.Contains(t, out, "ocfp vault start")
			} else {
				assert.NotContains(t, out, "ocfp vault start")
			}
		})
	}
}

// migrate-storage never creates a vault, so a bloc with no data is refused.
func TestMigrateStorage_NoDataIsRefused(t *testing.T) {
	paths := reconcilePaths(t)
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: stoppedProbe()}

	_, err := runMigrateStorage(t, paths, fake, nil)
	require.ErrorIs(t, err, ErrMigrateStorageNoData)
	assert.Contains(t, err.Error(), paths["vaultDir"])
	assert.Equal(t, []string{"probe"}, fake.calls)
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
}

func TestMigrateStorage_MixedDataIsRefused(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: stoppedProbe()}

	_, err := runMigrateStorage(t, paths, fake, nil)
	require.ErrorIs(t, err, ErrVaultDataMixed)
	assert.NotContains(t, fake.calls, "stop")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
}

// A file vault without both keys could not be reopened after a migration, so
// it is refused before anything is stopped. migrate-storage never recovers a
// key, so the error points at a backup, the vault logs, or 'ocfp vault
// inception'.
func TestMigrateStorage_MissingKeyIsRefusedBeforeAnyStop(t *testing.T) {
	for name, setup := range map[string]func(paths map[string]string){
		"root.key missing":    func(paths map[string]string) { writeKeys(t, paths, false, true) },
		"unseal.keys missing": func(paths map[string]string) { writeKeys(t, paths, true, false) },
		"root.key blank": func(paths map[string]string) {
			writeKeys(t, paths, false, true)
			require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte(" \n"), 0o600))
		},
		"one shared file": func(paths map[string]string) {
			writeKeys(t, paths, true, false)
			paths["unsealKeysFile"] = paths["rootKeyFile"]
		},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeFileData(t, paths["vaultDir"])
			setup(paths)

			before := treeDigest(t, blocDir(paths))
			fake := &fakeInception{probe: sealedFileProbe(), session: true, migrate: fakeMigration(t)}

			_, err := runMigrateStorage(t, paths, fake, nil)
			require.ErrorIs(t, err, ErrVaultStartKeyMissing)
			assert.Contains(t, err.Error(), "ocfp vault inception")
			assert.NotContains(t, fake.calls, "stop")
			assert.NotContains(t, fake.calls, "migrate")
			assertNeverReplaced(t, paths, fake)
			assert.Equal(t, before, treeDigest(t, blocDir(paths)))
		})
	}
}

// An engine without 'operator migrate' is found out before the vault stops.
func TestMigrateStorage_EngineThatCannotMigrateIsRefusedBeforeAnyStop(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: sealedFileProbe(), session: true, migrate: fakeMigration(t)}

	_, err := runMigrateStorage(t, paths, fake, fmt.Errorf("%w: no operator migrate", ErrEngineCannotMigrate))
	require.ErrorIs(t, err, ErrEngineCannotMigrate)
	assert.NotContains(t, fake.calls, "stop")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
}

func TestMigrateStorage_ClusterPortTaken(t *testing.T) {
	t.Run("stopped vault, refused before anything", func(t *testing.T) {
		paths := reconcilePaths(t)
		writeFileData(t, paths["vaultDir"])
		writeKeys(t, paths, true, true)

		before := treeDigest(t, blocDir(paths))
		fake := &fakeInception{probe: stoppedProbe(), clusterPortErr: errors.New("address already in use")}

		_, err := runMigrateStorage(t, paths, fake, nil)
		require.ErrorIs(t, err, ErrInceptionClusterPortTaken)
		assert.NotContains(t, fake.calls, "stop")
		assert.Equal(t, before, treeDigest(t, blocDir(paths)))
	})

	t.Run("running vault, found after the stop, nothing migrated", func(t *testing.T) {
		paths := reconcilePaths(t)
		writeFileData(t, paths["vaultDir"])
		writeKeys(t, paths, true, true)

		fake := &fakeInception{
			probe: stoppedProbe(), session: true, migrate: fakeMigration(t),
			clusterPortErr: errors.New("address already in use"),
		}

		_, err := runMigrateStorage(t, paths, fake, nil, sealedFileProbe(), sealedFileProbe())
		require.ErrorIs(t, err, ErrInceptionClusterPortTaken)
		assert.Contains(t, err.Error(), "stopped and not migrated")
		assert.NotContains(t, fake.calls, "migrate")
		assert.Equal(t, vaultDataFile, classifyVaultData(paths["vaultDir"]))
	})
}

// A failed copy leaves the file store where it was, and the vault is not
// started on anything.
func TestMigrateStorage_FailedMigrationStartsNothing(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{
		probe:   stoppedProbe(),
		migrate: func(map[string]string) (string, error) { return "", ErrEngineCannotReadFile },
	}

	_, err := runMigrateStorage(t, paths, fake, nil)
	require.ErrorIs(t, err, ErrEngineCannotReadFile)
	assert.Contains(t, err.Error(), "not restarted")
	assert.NotContains(t, strings.Join(fake.calls, "\n"), "start")
	assertNeverReplaced(t, paths, fake)
	assert.Equal(t, vaultDataFile, classifyVaultData(paths["vaultDir"]))
}

// The engine refusing a key right after the migration is a refusal, never an
// archive. The error names both stores and how to roll back to the file one.
func TestMigrateStorage_RejectedKeyAfterMigrationRefusesAndNeverArchives(t *testing.T) {
	for name, startErr := range map[string]error{
		"root token": tokenRejected(),
		"unseal key": unsealKeyRejected(),
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeFileData(t, paths["vaultDir"])
			writeKeys(t, paths, true, true)

			fake := &fakeInception{
				probe: stoppedProbe(), migrate: fakeMigration(t), targetToken: safeRCToken,
				startErrs: []error{startErr},
			}

			_, err := runMigrateStorage(t, paths, fake, nil)
			require.ErrorIs(t, err, ErrRestartAfterMigrationFailed)
			require.ErrorIs(t, err, ErrVaultKeysRejected)

			backup := paths["vaultDir"] + ".file-backup-20261006-120000"
			assert.Equal(t, "stop", fake.calls[len(fake.calls)-1], "what was started is stopped")
			assert.Equal(t, 1, strings.Count(strings.Join(fake.calls, "\n"), "start restart"),
				"the restart is not retried with another token")
			assertNeverReplaced(t, paths, fake)
			assert.Equal(t, vaultDataRaft, classifyVaultData(paths["vaultDir"]))
			assert.DirExists(t, filepath.Join(backup, "core"))
			assert.Contains(t, err.Error(), backup)
			assert.Contains(t, err.Error(), "rename "+backup+" to "+paths["vaultDir"])
			assert.Contains(t, err.Error(), "'ocfp vault teardown' and then 'ocfp vault inception'")
			assert.NotContains(t, err.Error(), testSealKey)
			assert.NotContains(t, err.Error(), "ROOT-TOKEN-SENTINEL")
		})
	}
}

// A copy that an earlier run left part way never touched the data, so its
// staging directory is set aside and the migration runs again.
func TestMigrateStorage_ResumesAnUnfinishedCopy(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)
	writeRaftData(t, paths["vaultDir"]+migrationStagingSuffix)
	writeJournal(t, paths, "migrating", paths["vaultDir"]+".file-backup-20261006-110000")

	fake := &fakeInception{probe: stoppedProbe(), migrate: fakeMigration(t)}

	_, err := runMigrateStorage(t, paths, fake, nil)
	require.NoError(t, err)
	assert.Contains(t, fake.calls, "migrate")
	assert.Equal(t, "finish restart", fake.calls[len(fake.calls)-1])
	globOne(t, paths["vaultDir"]+".raft-partial-*")
	assert.NoFileExists(t, paths["vaultDir"]+migrationJournalSuffix)
	assertNeverReplaced(t, paths, fake)
}

// A swap that an earlier run left part way is finished, never migrated
// again, and the vault is restarted on the raft store.
func TestMigrateStorage_FinishesAnUnfinishedSwap(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	backup := paths["vaultDir"] + ".file-backup-20261006-110000"
	writeRaftData(t, paths["vaultDir"]+migrationStagingSuffix)
	require.NoError(t, os.Rename(paths["vaultDir"], backup))
	writeJournal(t, paths, "swapping", backup)

	fake := &fakeInception{probe: stoppedProbe()}

	_, err := runMigrateStorage(t, paths, fake, nil)
	require.NoError(t, err)
	assert.NotContains(t, fake.calls, "migrate")
	assert.NotContains(t, fake.calls, "can-migrate", "finishing a swap needs no engine that can migrate")
	assert.Equal(t, "finish restart", fake.calls[len(fake.calls)-1])
	assert.Equal(t, vaultDataRaft, classifyVaultData(paths["vaultDir"]))
	assert.DirExists(t, filepath.Join(backup, "core"))
	assertNeverReplaced(t, paths, fake)
}

// The dry run reports what the command found and what it would do. It
// stops, starts, migrates, and writes nothing, and it never prints a key.
func TestMigrateStorage_DryRunReportsAndChangesNothing(t *testing.T) {
	paths := reconcilePaths(t)
	writeFileData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: sealedFileProbe(), session: true, migrate: fakeMigration(t), targetToken: safeRCToken}

	out, err := runMigrateStorageDryRun(t, paths, fake, nil)
	require.NoError(t, err)

	for _, call := range fake.calls {
		assert.Contains(t, []string{"probe", "can-migrate"}, call, "a dry run only looks")
	}

	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
	assert.Empty(t, tokenSidecars(t, paths))

	for _, want := range []string{
		"dry run",
		paths["vaultDir"], "file storage",
		paths["rootKeyFile"], paths["unsealKeysFile"], "present",
		"bao", testTools().engine.path, "OpenBao v2.7.0",
		"operator migrate",
		paths["port"], "this bloc's own vault, sealed",
		paths["tmuxSession"],
		paths["clusterPort"],
		paths["vaultDir"] + migrationBackupSuffix,
		"would stop",
	} {
		assert.Contains(t, out, want)
	}

	assert.NotContains(t, out, testSealKey)
	assert.NotContains(t, out, "ROOT-TOKEN-SENTINEL")
	assert.NotContains(t, out, safeRCToken)
}

// A dry run that finds a reason to refuse reports it and fails the same way
// the real run would, still without changing anything.
func TestMigrateStorage_DryRunReportsARefusal(t *testing.T) {
	for name, tc := range map[string]struct {
		fake       *fakeInception
		setup      func(paths map[string]string)
		canMigrate error
		want       error
		say        string
	}{
		"missing key": {
			fake:  &fakeInception{probe: stoppedProbe()},
			setup: func(paths map[string]string) { writeKeys(t, paths, true, false) },
			want:  ErrVaultStartKeyMissing, say: "missing or empty",
		},
		"vault started by hand": {
			fake:  &fakeInception{probe: sealedFileProbe(), notOurs: true},
			setup: func(paths map[string]string) { writeKeys(t, paths, true, true) },
			want:  ErrInceptionPortTaken, say: "cannot prove is its own",
		},
		"engine cannot migrate": {
			fake:       &fakeInception{probe: stoppedProbe()},
			setup:      func(paths map[string]string) { writeKeys(t, paths, true, true) },
			canMigrate: ErrEngineCannotMigrate, want: ErrEngineCannotMigrate, say: "can migrate: no",
		},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeFileData(t, paths["vaultDir"])
			tc.setup(paths)

			before := treeDigest(t, blocDir(paths))

			out, err := runMigrateStorageDryRun(t, paths, tc.fake, tc.canMigrate)
			require.ErrorIs(t, err, tc.want)
			assert.Contains(t, out, tc.say)
			assert.Contains(t, out, "would refuse")
			if name != "vault started by hand" {
				assert.NotContains(t, out, "not checked", "a dry run reports every check, even after a refusal")
			}

			assert.NotContains(t, tc.fake.calls, "stop")
			assert.Equal(t, before, treeDigest(t, blocDir(paths)))
		})
	}
}

func TestMigrateStorage_DryRunOfARaftVaultSaysItIsDone(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{probe: stoppedProbe()}

	out, err := runMigrateStorageDryRun(t, paths, fake, nil)
	require.NoError(t, err)
	assert.Contains(t, out, "raft storage")
	assert.Contains(t, out, "already on raft storage")
	assert.NotContains(t, fake.calls, "can-migrate", "a raft vault needs no engine that can migrate")
}

func TestNewVaultCmd_HasMigrateStorage(t *testing.T) {
	t.Parallel()

	cmd, _, err := NewVaultCmd().Find([]string{"migrate-storage"})
	require.NoError(t, err)
	assert.Equal(t, "migrate-storage", cmd.Name())
	assert.NotNil(t, cmd.RunE)
	assert.NotNil(t, cmd.Flags().Lookup("dry-run"))
}

// migrateStorageForbidden names the code that archives a vault, starts a new
// one, recovers or saves a key, or runs reconcile, which may do any of that.
//
//nolint:gochecknoglobals // fixed list, read-only
var migrateStorageForbidden = []string{
	"archiveAndForgetVault",
	"archiveAndStartFresh",
	"startWithoutData",
	"fresh",
	"reconcileInceptionVault",
	"ensureInceptionVault",
	"ensureInceptionVaultLocked",
	"startFromDisk",
	"resumeMigration",
	"migrateAndRestart",
	"cleanupExistingVault",
	"safeLocalFresh",
	"saveVaultKeys",
	"recoverInceptionKeys",
	"recoverUnsealKeyFromLogs",
	"promoteTargetToken",
}

// TestMigrateStorage_CannotReachArchiveOrFreshStart walks every function in
// this package that migrate-storage's entry point can reach, as vault
// start's test does, and fails when any of them names the archive,
// fresh-start, or key-recovery code. It must reach the migration and vault
// start's restart, which are the two things the command does.
func TestMigrateStorage_CannotReachArchiveOrFreshStart(t *testing.T) {
	t.Parallel()

	funcs := packageFuncs(t)

	require.Contains(t, funcs, "runVaultMigrateStorage")

	reached := map[string]bool{}
	named := map[string]bool{}
	queue := []string{"runVaultMigrateStorage"}

	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]

		if reached[name] {
			continue
		}

		reached[name] = true

		for _, decl := range funcs[name] {
			ast.Inspect(decl, func(node ast.Node) bool {
				var called string

				switch n := node.(type) {
				case *ast.Ident:
					called = n.Name
				case *ast.SelectorExpr:
					called = n.Sel.Name
				default:
					return true
				}

				named[called] = true

				if _, ok := funcs[called]; ok && !reached[called] {
					queue = append(queue, called)
				}

				return true
			})
		}
	}

	for _, want := range []string{
		"migrateInceptionVaultStorage", "reportInceptionVaultStorage", "migrateFileVaultToRaft",
		"abandonInterruptedCopy", "finishInterruptedSwap", "restartInceptionVaultInPlace", "startInceptionVault",
	} {
		require.True(t, reached[want], "the walk must reach %s", want)
	}

	for _, name := range migrateStorageForbidden {
		assert.False(t, reached[name] || named[name], "migrate-storage can reach %s", name)
	}
}
