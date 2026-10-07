package commands

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// startSteps narrows the scripted reconcile steps to the ones vault start
// may take. Its reopen and finish come from wireVaultStartSteps, the wiring
// production uses, so a start the fake records carries the mode production
// chose, and every one must be "start restart".
func (f *fakeInception) startSteps() vaultStartSteps {
	scripted := f.steps()

	steps := wireVaultStartSteps(testTools(), scripted.start,
		func(ctx context.Context, _ string, paths map[string]string, log *zap.SugaredLogger) error {
			// A reopened vault's finish takes no mode, so it is recorded
			// under the only one it can stand for.
			return scripted.finish(ctx, paths, safeLocalRestart, log)
		})

	steps.probe = scripted.probe
	steps.hasSession = scripted.hasSession
	steps.ownsVault = scripted.ownsVault
	steps.stop = scripted.stop
	steps.clusterPortFree = scripted.clusterPortFree
	steps.targetToken = scripted.targetToken
	steps.now = scripted.now

	return steps
}

func runVaultStartWith(t *testing.T, paths map[string]string, fake *fakeInception) error {
	t.Helper()

	return restartInceptionVaultInPlace(context.Background(), &vaultStartRun{
		paths: paths,
		steps: fake.startSteps(),
		log:   zap.NewNop().Sugar(),
	})
}

// runVaultStartWithProbes runs vault start with the port answering each
// probe with the next of probes, and with fake.probe once they run out, so a
// test can change what holds the port between vault start's checks and its
// stops.
func runVaultStartWithProbes(t *testing.T, paths map[string]string, fake *fakeInception, probes ...vaultProbe) error {
	t.Helper()

	steps := fake.startSteps()
	steps.probe = func(context.Context, string) vaultProbe {
		fake.record("probe")

		if len(probes) == 0 {
			return fake.probe
		}

		next := probes[0]
		probes = probes[1:]

		return next
	}

	return restartInceptionVaultInPlace(context.Background(), &vaultStartRun{
		paths: paths,
		steps: steps,
		log:   zap.NewNop().Sugar(),
	})
}

// assertNothingReplaced checks that a run neither archived the vault nor
// started a new one, whatever else it did.
func assertNothingReplaced(t *testing.T, paths map[string]string, fake *fakeInception) {
	t.Helper()

	assert.Empty(t, archivesOf(t, paths), "vault start must never archive")
	assert.NotContains(t, fake.calls, "start fresh", "vault start must never start a new vault")
	assert.NotContains(t, fake.calls, "migrate", "vault start must never migrate")

	for _, call := range fake.calls {
		if strings.HasPrefix(call, "start ") {
			assert.Equal(t, "start restart", call)
		}
	}
}

func TestVaultStart_HealthyVaultIsLeftAlone(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: healthyRaftProbe(), session: true}

	require.NoError(t, runVaultStartWith(t, paths, fake))
	assert.Equal(t, []string{"probe"}, fake.calls, "a healthy vault is neither stopped nor started")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
	assertNothingReplaced(t, paths, fake)
}

// A healthy vault is left running even when a key file is gone, because
// stopping it is what would lose the vault. The run only warns.
func TestVaultStart_HealthyVaultWithAMissingKeyIsLeftAlone(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, false)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: healthyRaftProbe(), session: true}

	require.NoError(t, runVaultStartWith(t, paths, fake))
	assert.Equal(t, []string{"probe"}, fake.calls)
	assert.Equal(t, before, treeDigest(t, blocDir(paths)), "no key is recovered or written")
	assertNothingReplaced(t, paths, fake)
}

func TestVaultStart_StoppedVaultRestartsInPlace(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: stoppedProbe()}

	require.NoError(t, runVaultStartWith(t, paths, fake))
	assert.Equal(t, []string{
		"probe", "cluster-port " + paths["clusterPort"], "probe", "stop", "start restart", "finish restart",
	}, fake.calls)
	assert.Equal(t, []string{paths["rootKeyFile"]}, fake.tokenFiles, "the restart reads root.key")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)), "the data and keys stay exactly where they were")
	assertNothingReplaced(t, paths, fake)
}

// This bloc's own vault that answers sealed holds nothing in memory that its
// files do not. It is stopped and reopened in place with its saved keys.
func TestVaultStart_OwnSealedVaultRestartsInPlace(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{
		probe:   vaultProbe{state: vaultProbeVault, initialized: true, sealed: true, storageType: "raft"},
		session: true,
	}

	require.NoError(t, runVaultStartWith(t, paths, fake))
	assert.Equal(t, []string{
		"probe", "probe", "stop", "cluster-port " + paths["clusterPort"], "start restart", "finish restart",
	}, fake.calls)
	assertNothingReplaced(t, paths, fake)
}

// vault start only starts a stopped vault. This bloc's own vault that is
// open and serving is left running even when its tmux session is gone,
// because stopping it could lose a vault whose saved keys no longer open it.
func TestVaultStart_OpenVaultWithoutItsSessionIsLeftRunning(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: healthyRaftProbe()}

	require.NoError(t, runVaultStartWith(t, paths, fake))
	assert.Equal(t, []string{"probe"}, fake.calls, "an open vault is neither stopped nor started")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
	assertNothingReplaced(t, paths, fake)
}

func TestVaultStart_MissingKeyFileIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		root, unseal bool
		blank        string
	}{
		"no root token":     {unseal: true},
		"no unseal key":     {root: true},
		"neither key":       {},
		"blank root token":  {root: true, unseal: true, blank: "rootKeyFile"},
		"blank unseal keys": {root: true, unseal: true, blank: "unsealKeysFile"},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeRaftData(t, paths["vaultDir"])
			writeKeys(t, paths, tc.root, tc.unseal)

			if tc.blank != "" {
				require.NoError(t, os.WriteFile(paths[tc.blank], []byte("\n"), 0o600))
			}

			// A log holding the unseal key must not be used to fill a missing
			// key file: vault start writes nothing when it refuses.
			require.NoError(t, os.MkdirAll(paths["logDir"], 0o700))
			require.NoError(t, os.WriteFile(paths["logFile"],
				[]byte("Your OpenBao Seal Key is "+testSealKey+"\n"), 0o600))

			before := treeDigest(t, blocDir(paths))
			fake := &fakeInception{probe: stoppedProbe()}

			err := runVaultStartWith(t, paths, fake)
			require.ErrorIs(t, err, ErrVaultStartRefused)
			require.ErrorIs(t, err, ErrVaultStartKeyMissing)
			assert.NotContains(t, err.Error(), testSealKey)
			assert.Equal(t, []string{"probe"}, fake.calls, "nothing is stopped or started")
			assert.Equal(t, before, treeDigest(t, blocDir(paths)))
			assertNothingReplaced(t, paths, fake)
		})
	}
}

func TestVaultStart_MissingRaftDataIsRefused(t *testing.T) {
	paths := reconcilePaths(t)
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: stoppedProbe()}

	err := runVaultStartWith(t, paths, fake)
	require.ErrorIs(t, err, ErrVaultStartRefused)
	require.ErrorIs(t, err, ErrVaultStartNoData)
	assert.Contains(t, err.Error(), paths["vaultDir"])
	assert.Equal(t, []string{"probe"}, fake.calls)
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
	assertNothingReplaced(t, paths, fake)
}

// File storage needs a migration, and a migration journal needs one resumed.
// Both are left for an operator to run 'ocfp vault inception' by hand, and
// mixed storage is left for a person to inspect.
func TestVaultStart_DataThatNeedsAnOperatorIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(t *testing.T, paths map[string]string)
		want  error
	}{
		"file storage": {
			setup: func(t *testing.T, paths map[string]string) { t.Helper(); writeFileData(t, paths["vaultDir"]) },
			want:  ErrVaultStartNeedsInception,
		},
		"unfinished migration": {
			setup: func(t *testing.T, paths map[string]string) {
				t.Helper()
				writeRaftData(t, paths["vaultDir"])
				writeJournal(t, paths, migratePhaseSwapping, paths["vaultDir"]+".file-backup-20261006-110000")
			},
			want: ErrVaultStartNeedsInception,
		},
		"mixed storage": {
			setup: func(t *testing.T, paths map[string]string) {
				t.Helper()
				writeRaftData(t, paths["vaultDir"])
				writeFileData(t, paths["vaultDir"])
			},
			want: ErrVaultDataMixed,
		},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeKeys(t, paths, true, true)
			tc.setup(t, paths)

			before := treeDigest(t, blocDir(paths))
			fake := &fakeInception{probe: stoppedProbe()}

			err := runVaultStartWith(t, paths, fake)
			require.ErrorIs(t, err, ErrVaultStartRefused)
			require.ErrorIs(t, err, tc.want)
			assert.Equal(t, []string{"probe"}, fake.calls)
			assert.Equal(t, before, treeDigest(t, blocDir(paths)))
			assertNothingReplaced(t, paths, fake)
		})
	}
}

func TestVaultStart_ForeignPortHolderIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		fake  *fakeInception
		want  error
		calls func(paths map[string]string) []string
	}{
		"a stranger on the API port": {
			fake:  &fakeInception{probe: vaultProbe{state: vaultProbeStranger}},
			want:  ErrInceptionPortTaken,
			calls: func(map[string]string) []string { return []string{"probe"} },
		},
		"another bloc's vault on the API port": {
			fake:  &fakeInception{probe: healthyRaftProbe(), session: true, notOurs: true},
			want:  ErrInceptionPortTaken,
			calls: func(map[string]string) []string { return []string{"probe"} },
		},
		"an owner that cannot be told": {
			fake:  &fakeInception{probe: healthyRaftProbe(), session: true, ownsErr: ErrVaultOwnerUnknown},
			want:  ErrVaultOwnerUnknown,
			calls: func(map[string]string) []string { return []string{"probe"} },
		},
		"something on the cluster port": {
			fake: &fakeInception{probe: stoppedProbe(), clusterPortErr: errors.New("address already in use")},
			want: ErrInceptionClusterPortTaken,
			calls: func(paths map[string]string) []string {
				return []string{"probe", "cluster-port " + paths["clusterPort"]}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeRaftData(t, paths["vaultDir"])
			writeKeys(t, paths, true, true)

			before := treeDigest(t, blocDir(paths))

			err := runVaultStartWith(t, paths, tc.fake)
			require.ErrorIs(t, err, ErrVaultStartRefused)
			require.ErrorIs(t, err, tc.want)
			assert.Equal(t, tc.calls(paths), tc.fake.calls, "nothing is stopped or started")
			assert.Equal(t, before, treeDigest(t, blocDir(paths)))
			assertNothingReplaced(t, paths, tc.fake)
		})
	}
}

// When the engine refuses a saved key, vault start stops what it started and
// refuses. It never archives, never starts a new vault, and never retries
// with another token, so the data and both keys stay where they were.
func TestVaultStart_RejectedKeyStopsAndRefuses(t *testing.T) {
	for name, startErr := range map[string]error{
		"unseal key": fmt.Errorf("%w: !! Unable to unseal: invalid key", ErrVaultKeysRejected),
		"root token": fmt.Errorf("%w: the token in root.key", ErrVaultRootTokenRejected),
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeRaftData(t, paths["vaultDir"])
			writeKeys(t, paths, true, true)

			before := treeDigest(t, blocDir(paths))
			fake := &fakeInception{probe: stoppedProbe(), startErrs: []error{startErr}}

			err := runVaultStartWith(t, paths, fake)
			require.ErrorIs(t, err, ErrVaultStartRefused)
			require.ErrorIs(t, err, ErrVaultStartKeysRejected)
			require.ErrorIs(t, err, ErrVaultKeysRejected)
			assert.Contains(t, err.Error(), "ocfp vault inception")
			assert.Equal(t, []string{
				"probe", "cluster-port " + paths["clusterPort"], "probe", "stop", "start restart", "probe", "stop",
			}, fake.calls, "what was started is stopped, and nothing else runs")
			assert.Equal(t, before, treeDigest(t, blocDir(paths)))
			assertNothingReplaced(t, paths, fake)
		})
	}
}

// Two blocs whose ports collide can both find the port free at boot. A stop
// kills whatever listens on the port, so vault start probes the port again
// right before each stop, and when the vault there is not this bloc's own,
// it stops nothing and leaves that vault running.
func TestVaultStart_NeverStopsAVaultThatIsNotItsOwn(t *testing.T) {
	otherBlocs := healthyRaftProbe()
	stranger := vaultProbe{state: vaultProbeStranger}

	for name, tc := range map[string]struct {
		probes    []vaultProbe
		startErrs []error
		calls     func(paths map[string]string) []string
		want      []error
	}{
		"another bloc's vault takes the port before the first stop": {
			probes: []vaultProbe{stoppedProbe(), otherBlocs},
			calls: func(paths map[string]string) []string {
				return []string{"probe", "cluster-port " + paths["clusterPort"], "probe"}
			},
			want: []error{ErrVaultStartRefused, ErrInceptionPortTaken},
		},
		"something that is not a vault takes the port before the first stop": {
			probes: []vaultProbe{stoppedProbe(), stranger},
			calls: func(paths map[string]string) []string {
				return []string{"probe", "cluster-port " + paths["clusterPort"], "probe"}
			},
			want: []error{ErrVaultStartRefused, ErrInceptionPortTaken},
		},
		"another bloc's vault wins the port while this one starts": {
			probes:    []vaultProbe{stoppedProbe(), stoppedProbe(), otherBlocs},
			startErrs: []error{ErrVaultNotReady},
			calls: func(paths map[string]string) []string {
				return []string{"probe", "cluster-port " + paths["clusterPort"], "probe", "stop", "start restart", "probe"}
			},
			want: []error{ErrVaultNotReady, ErrInceptionPortTaken},
		},
		"another bloc's vault wins the port and this one's key is refused": {
			probes:    []vaultProbe{stoppedProbe(), stoppedProbe(), otherBlocs},
			startErrs: []error{ErrVaultKeysRejected},
			calls: func(paths map[string]string) []string {
				return []string{"probe", "cluster-port " + paths["clusterPort"], "probe", "stop", "start restart", "probe"}
			},
			want: []error{ErrVaultStartKeysRejected, ErrInceptionPortTaken},
		},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeRaftData(t, paths["vaultDir"])
			writeKeys(t, paths, true, true)

			before := treeDigest(t, blocDir(paths))
			fake := &fakeInception{probe: otherBlocs, notOurs: true, startErrs: tc.startErrs}

			err := runVaultStartWithProbes(t, paths, fake, tc.probes...)
			for _, want := range tc.want {
				require.ErrorIs(t, err, want)
			}

			assert.Equal(t, tc.calls(paths), fake.calls, "nothing that is not this bloc's is stopped")
			assert.Equal(t, before, treeDigest(t, blocDir(paths)))
			assertNothingReplaced(t, paths, fake)
		})
	}
}

// safe's target can hold a root token other than the one in root.key, which
// reconcile would try next. vault start keeps that token, as before every
// stop, but never restarts with it.
func TestVaultStart_RejectedRootTokenDoesNotRetryWithTheTargetToken(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{
		probe:       stoppedProbe(),
		targetToken: "s.TARGET-TOKEN-SENTINEL",
		startErrs:   []error{fmt.Errorf("%w: refused", ErrVaultRootTokenRejected)},
	}

	err := runVaultStartWith(t, paths, fake)
	require.ErrorIs(t, err, ErrVaultStartKeysRejected)
	assert.NotContains(t, err.Error(), "SENTINEL")
	assert.Equal(t, []string{paths["rootKeyFile"]}, fake.tokenFiles, "only root.key is ever tried")

	root, readErr := os.ReadFile(paths["rootKeyFile"])
	require.NoError(t, readErr)
	assert.Equal(t, "s.ROOT-TOKEN-SENTINEL\n", string(root), "root.key is never replaced")
	assertNothingReplaced(t, paths, fake)
}

// A refused key whose vault then will not stop is still a refusal, and the
// error says the vault may be running.
func TestVaultStart_RejectedKeyAndAStuckStopArchivesNothing(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{
		probe:     stoppedProbe(),
		startErrs: []error{ErrVaultKeysRejected},
		stopErrs:  []error{nil, ErrVaultWouldNotStop},
	}

	err := runVaultStartWith(t, paths, fake)
	require.ErrorIs(t, err, ErrVaultStartKeysRejected)
	require.ErrorIs(t, err, ErrVaultWouldNotStop)
	assert.Contains(t, err.Error(), "may still be running")
	assertNothingReplaced(t, paths, fake)
}

// A start that fails for any reason other than a refused key says nothing
// about the keys. The vault is stopped and the run fails without a refusal.
func TestVaultStart_OtherStartFailureStopsAndFails(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: stoppedProbe(), startErrs: []error{ErrVaultNotReady}}

	err := runVaultStartWith(t, paths, fake)
	require.ErrorIs(t, err, ErrVaultNotReady)
	require.NotErrorIs(t, err, ErrVaultStartKeysRejected)
	assert.Equal(t, []string{
		"probe", "cluster-port " + paths["clusterPort"], "probe", "stop", "start restart", "probe", "stop",
	}, fake.calls)
	assert.Equal(t, before, treeDigest(t, blocDir(paths)))
	assertNothingReplaced(t, paths, fake)
}

// An open vault is left running before vault start checks or touches a key
// file, so one whose keys need a repair, or are not saved at all, is left
// exactly as it was, and the run only warns.
func TestVaultStart_OpenVaultWithMalformedKeysIsLeftRunning(t *testing.T) {
	for name, writeKeyFiles := range map[string]func(t *testing.T, paths map[string]string){
		"keys that need a repair": func(t *testing.T, paths map[string]string) {
			t.Helper()
			require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte("not a token!\n"), 0o600))
			require.NoError(t, os.WriteFile(paths["unsealKeysFile"], []byte("  "+testSealKey+"  \n\n"), 0o600))
		},
		"no root token": func(t *testing.T, paths map[string]string) {
			t.Helper()
			writeKeys(t, paths, false, true)
		},
	} {
		t.Run(name, func(t *testing.T) {
			paths := reconcilePaths(t)
			writeRaftData(t, paths["vaultDir"])
			writeKeyFiles(t, paths)

			before := treeDigest(t, blocDir(paths))
			fake := &fakeInception{probe: healthyRaftProbe()}

			require.NoError(t, runVaultStartWith(t, paths, fake))
			assert.Equal(t, []string{"probe"}, fake.calls, "neither repaired, stopped, nor started")
			assert.Equal(t, before, treeDigest(t, blocDir(paths)), "no key file is rewritten")
			assertNothingReplaced(t, paths, fake)
		})
	}
}

// Before it restarts a stopped vault, vault start gives the key files the
// same repairs reconcile does. Each one keeps the key's value: here it
// strips the whitespace that safe would otherwise hand the engine.
func TestVaultStart_StoppedVaultKeyFilesAreCanonicalised(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, false)
	require.NoError(t, os.WriteFile(paths["unsealKeysFile"], []byte("  "+testSealKey+"  \n\n"), 0o600))

	fake := &fakeInception{probe: stoppedProbe()}

	require.NoError(t, runVaultStartWith(t, paths, fake))

	unseal, err := os.ReadFile(paths["unsealKeysFile"])
	require.NoError(t, err)
	assert.Equal(t, testSealKey+"\n", string(unseal), "the same key, without the whitespace")
	assertNothingReplaced(t, paths, fake)
}

// The port is probed again before any key file is repaired, so a refusal
// because the port changed hands follows no write.
func TestVaultStart_PortRefusalComesBeforeAnyKeyRepair(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, false)
	require.NoError(t, os.WriteFile(paths["unsealKeysFile"], []byte("  "+testSealKey+"  \n\n"), 0o600))

	before := treeDigest(t, blocDir(paths))
	fake := &fakeInception{probe: healthyRaftProbe(), notOurs: true}

	err := runVaultStartWithProbes(t, paths, fake, stoppedProbe(), healthyRaftProbe())
	require.ErrorIs(t, err, ErrInceptionPortTaken)
	assert.NotContains(t, fake.calls, "stop")
	assert.Equal(t, before, treeDigest(t, blocDir(paths)), "the key file keeps its whitespace")
}

// The reopen that production wires must hand safe local the restart mode,
// whatever the rest of a run does, and the finish it wires takes no mode at
// all, so it can never save keys as if the vault were new.
func TestVaultStart_ProductionWiringOnlyRestarts(t *testing.T) {
	t.Parallel()

	var (
		modes    []safeLocalMode
		finished []string
	)

	steps := wireVaultStartSteps(testTools(),
		func(_ context.Context, _ map[string]string, tools inceptionTools, mode safeLocalMode, _ *zap.SugaredLogger) error {
			assert.Equal(t, testTools(), tools, "the reopen runs the tools the prerequisite check validated")

			modes = append(modes, mode)

			return nil
		},
		func(_ context.Context, safePath string, _ map[string]string, _ *zap.SugaredLogger) error {
			finished = append(finished, safePath)

			return nil
		})

	paths := map[string]string{"port": "18234"}

	require.NoError(t, steps.reopen(context.Background(), paths, zap.NewNop().Sugar()))
	require.NoError(t, steps.finish(context.Background(), paths, zap.NewNop().Sugar()))
	assert.Equal(t, []safeLocalMode{safeLocalRestart}, modes, "vault start may only ever reopen")
	assert.Equal(t, []string{testTools().safe}, finished)
}

// Bastion init runs 'ocfp vault start --check-support' to learn whether the
// installed ocfp has vault start at all, so the flag must succeed without
// touching any vault. An ocfp that predates vault start rejects the flag.
func TestVaultStartCmd_CheckSupportTouchesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OCFP_HOME", filepath.Join(home, "ocfp"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))

	start := newVaultStartCmd()

	flag := start.Flags().Lookup("check-support")
	require.NotNil(t, flag)
	assert.True(t, flag.Hidden, "the flag is for bastion init, not for people")

	require.NoError(t, start.Flags().Set("check-support", "true"))
	require.NoError(t, start.RunE(start, nil))

	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	assert.Empty(t, entries, "the check takes no lock and writes nothing")
}

func TestNewVaultCmd_HasStart(t *testing.T) {
	t.Parallel()

	start, _, err := NewVaultCmd().Find([]string{"start"})
	require.NoError(t, err)
	assert.Equal(t, "start", start.Name())
	assert.NotNil(t, start.RunE)
}

// vaultStartForbidden names the code that archives a vault, starts a new one,
// migrates one, or recovers, saves, or replaces a key. No function vault start
// can reach may name any of it, so a later change to reconcile cannot route
// vault start there. safeLocalFresh is the mode that asks safe for a new
// vault, and saveVaultKeys is what writes a new vault's keys.
//
//nolint:gochecknoglobals // fixed list, read-only
var vaultStartForbidden = []string{
	"archiveAndForgetVault",
	"archiveAndStartFresh",
	"fresh",
	"migrateAndRestart",
	"migrateFileVaultToRaft",
	"reconcileInceptionVault",
	"ensureInceptionVault",
	"ensureInceptionVaultLocked",
	"startFromDisk",
	"resumeMigration",
	"cleanupExistingVault",
	"safeLocalFresh",
	"saveVaultKeys",
	"recoverInceptionKeys",
	"recoverUnsealKeyFromLogs",
	"promoteTargetToken",
}

// TestVaultStart_CannotReachArchiveOrFreshStart walks every function in this
// package that vault start's entry point can reach, following calls by name
// and so over-approximating the real call graph, and fails when any of them
// names the archive, fresh-start, migration, or key-writing code.
func TestVaultStart_CannotReachArchiveOrFreshStart(t *testing.T) {
	t.Parallel()

	funcs := packageFuncs(t)

	for _, name := range []string{"runVaultStart", "restartInceptionVaultInPlace", "newVaultStartSteps"} {
		require.Contains(t, funcs, name, "the entry point %s must exist", name)
	}

	reached := map[string]bool{}
	named := map[string]bool{}
	queue := []string{"runVaultStart"}

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

	require.True(t, reached["restartInceptionVaultInPlace"], "the walk must reach the state machine")
	require.True(t, reached["startInceptionVault"], "the walk must reach the real restart")

	for _, name := range vaultStartForbidden {
		assert.False(t, reached[name] || named[name], "vault start can reach %s", name)
	}

	// The steps vault start runs with carry no mode, so they cannot ask
	// safe for a fresh vault.
	assert.False(t, structHasField(t, "vaultStartSteps", "migrate"))
	assert.False(t, structHasField(t, "vaultStartSteps", "start"))
}

// packageFuncs maps every function and method name declared in this
// package's non-test files to its declarations.
func packageFuncs(t *testing.T) map[string][]*ast.FuncDecl {
	t.Helper()

	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	funcs := map[string][]*ast.FuncDecl{}
	fset := token.NewFileSet()

	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}

		parsed, parseErr := parser.ParseFile(fset, file, nil, 0)
		require.NoError(t, parseErr)

		for _, decl := range parsed.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				funcs[fn.Name.Name] = append(funcs[fn.Name.Name], fn)
			}
		}
	}

	return funcs
}

// structHasField reports whether the named struct type in this package has
// a field of the given name.
func structHasField(t *testing.T, typeName, field string) bool {
	t.Helper()

	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	fset := token.NewFileSet()

	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}

		parsed, parseErr := parser.ParseFile(fset, file, nil, 0)
		require.NoError(t, parseErr)

		obj := parsed.Scope.Lookup(typeName) //nolint:staticcheck // the file scope is enough to find a top-level type
		if obj == nil {
			continue
		}

		spec, ok := obj.Decl.(*ast.TypeSpec)
		if !ok {
			continue
		}

		fields := spec.Type.(*ast.StructType).Fields.List //nolint:forcetypeassert // the named type is a struct

		return slices.ContainsFunc(fields, func(f *ast.Field) bool {
			return slices.ContainsFunc(f.Names, func(n *ast.Ident) bool { return n.Name == field })
		})
	}

	t.Fatalf("type %s not found", typeName)

	return false
}
