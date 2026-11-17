package commands

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// safeCurrentLine matches the current-target entry of ~/.saferc.
var safeCurrentLine = regexp.MustCompile(`(?m)^current:.*$`)

// safeCurrentEntry returns the bytes of the current-target entry in
// ~/.saferc, or nil when the file has none.
func safeCurrentEntry(t *testing.T) []byte {
	t.Helper()

	data, err := os.ReadFile(fakeSafeRCPath())
	require.NoError(t, err)

	return safeCurrentLine.Find(data)
}

// safeTargetHarness is a temp home with a fake safe, tmux, and the process
// tools on PATH, in which a bloc's inception vault can be started and torn
// down without anything real running.
type safeTargetHarness struct {
	bloc    string
	paths   map[string]string
	tools   inceptionTools
	safeLog string
	started string
}

func newSafeTargetHarness(t *testing.T, current string) *safeTargetHarness {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OCFP_HOME", filepath.Join(home, "ocfp"))
	t.Setenv("SAFE_TARGET", "")
	t.Setenv(inceptionPortEnvVar, "")
	require.NoError(t, os.Unsetenv("SAFE_TARGET"))

	bin := filepath.Join(home, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o700))

	safePath, safeLog := installFakeSafe(t, bin)

	// send-keys runs the typed line at once, as the pane would, and the
	// session never exists beforehand. kill-session shuts down the safe
	// local the pane runs, which cleans up after itself as safe does.
	writeFakeExecutable(t, bin, "tmux", `case "$1" in
  has-session) exit 1 ;;
  send-keys) sh -c "$4" ;;
  kill-session) safe `+fakeSafeLocalExit+` ;;
esac
exit 0
`)
	writeFakeExecutable(t, bin, "pkill", "exit 1\n")
	writeFakeExecutable(t, bin, "kill", "exit 0\n")
	writeFakeExecutable(t, bin, "lsof", "exit 1\n")
	baoPath := writeFakeExecutable(t, bin, "bao", "exit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")

	started := filepath.Join(home, "safe-local-started")
	t.Setenv(fakeSafeLocalStartedEnv, started)

	rc := &fakeSafeRC{Version: 1, Current: current, Vaults: map[string]*fakeSafeVault{
		"ops": {URL: "https://ops.example:8200", Token: "s.OPERATOR-OWN-TOKEN"},
	}}
	require.NoError(t, writeFakeSafeRCFile(rc))

	bloc := "ocfp-lab-drgao"

	return &safeTargetHarness{
		bloc:    bloc,
		paths:   mustInceptionPaths(t, bloc, false),
		tools:   inceptionTools{safe: safePath, engine: inceptionEngine{name: "bao", path: baoPath}},
		safeLog: safeLog,
		started: started,
	}
}

// useVaultOps runs the vault commands for real against the harness's fakes,
// with nothing listening on the port. When cancel is given, it is called at
// the first pause after safe local has registered its target, as when the
// operator stops ocfp part way.
func (h *safeTargetHarness) useVaultOps(t *testing.T, cancel context.CancelFunc) {
	t.Helper()

	orig := vaultOps
	vaultOps = inceptionVaultOps{
		run:      runVaultCommand,
		portOpen: func(context.Context, string) bool { return false },
		sleep: func(time.Duration) {
			if _, err := os.Stat(h.started); cancel != nil && err == nil {
				cancel()
			}
		},
	}

	t.Cleanup(func() { vaultOps = orig })
}

// startFreshVault runs the inception vault reconcile for the bloc, with the
// real start, target, and stop steps; with no vault on disk it starts a new
// one. When interrupt is
// set, the run's context is cancelled once safe local has registered its
// target, as when the operator stops ocfp part way.
func (h *safeTargetHarness) startFreshVault(t *testing.T, interrupt bool) error {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	if interrupt {
		h.useVaultOps(t, cancel)
	} else {
		h.useVaultOps(t, nil)
	}

	steps := newInceptionSteps(h.tools)
	steps.probe = func(context.Context, string) vaultProbe { return vaultProbe{state: vaultProbeStopped} }
	steps.hasSession = func(context.Context, map[string]string) bool { return false }
	steps.clusterPortFree = func(context.Context, string) error { return nil }

	return reconcileInceptionVault(ctx, &inceptionRun{
		paths: h.paths,
		tools: h.tools,
		steps: steps,
		log:   zap.NewNop().Sugar(),
	})
}

// startVaultInPlace runs what `ocfp vault start` runs for the bloc, with the
// real stop, reopen, and target steps.
func (h *safeTargetHarness) startVaultInPlace(t *testing.T) error {
	t.Helper()

	h.useVaultOps(t, nil)

	steps := newVaultStartSteps(h.tools)
	steps.probe = func(context.Context, string) vaultProbe { return vaultProbe{state: vaultProbeStopped} }
	steps.hasSession = func(context.Context, map[string]string) bool { return false }
	steps.clusterPortFree = func(context.Context, string) error { return nil }

	return restartInceptionVaultInPlace(t.Context(), &vaultStartRun{
		paths: h.paths,
		steps: steps,
		log:   zap.NewNop().Sugar(),
	})
}

// tearDownVault runs what `ocfp vault teardown` runs for the bloc, with the
// real stop and archive.
func (h *safeTargetHarness) tearDownVault(t *testing.T) error {
	t.Helper()

	h.useVaultOps(t, nil)

	steps := newTeardownSteps(h.bloc)
	steps.probe = func(context.Context, string) vaultProbe { return vaultProbe{state: vaultProbeStopped} }
	steps.disableBootUnit = func(context.Context, *zap.SugaredLogger) error { return nil }

	return tearDownInceptionVault(t.Context(), h.paths, steps, false, zap.NewNop().Sugar())
}

// writeSavedVault puts a raft vault and both of its keys on disk, so the
// next start reopens it.
func (h *safeTargetHarness) writeSavedVault(t *testing.T) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(h.paths["rootKeyFile"]), 0o700))
	writeRaftData(t, h.paths["vaultDir"])
	writeKeys(t, h.paths, true, true)
}

// selectOwnTarget registers the bloc's own inception target and makes it
// current, as every older ocfp did, with a token from that older vault.
func (h *safeTargetHarness) selectOwnTarget(t *testing.T) {
	t.Helper()

	rc, err := readFakeSafeRC()
	require.NoError(t, err)

	rc.Vaults[h.paths["vaultName"]] = &fakeSafeVault{URL: h.url(), Token: "s.OLDER-INCEPTION-TOKEN"}
	rc.Current = h.paths["vaultName"]
	require.NoError(t, writeFakeSafeRCFile(rc))
}

func (h *safeTargetHarness) url() string {
	return "http://127.0.0.1:" + h.paths["port"]
}

// safeSelectsOrUnpinned lists every call that reads or could move safe's
// current target, that reaches a vault without naming one, or that deletes
// a target other than the bloc's own two: its inception target and the one
// safe local registers while it runs.
func safeSelectsOrUnpinned(calls []fakeSafeCall, ownTarget string) []string {
	var bad []string

	for _, call := range calls {
		args := call.Args
		pinned := call.SafeTarget != ""

		for len(args) > 1 && (args[0] == "-T" || args[0] == "--target") {
			pinned = true
			args = args[2:]
		}

		if len(args) == 0 {
			continue
		}

		flags, positional := splitFlags(args[1:])
		keep := slices.Contains(flags, "--keep-current")
		line := strings.Join(call.Args, " ")

		switch args[0] {
		case "--version", "help", "targets":
		case "local":
			if !keep {
				bad = append(bad, "selects: "+line)
			}
		case "target":
			switch {
			case len(positional) > 0 && positional[0] == "delete":
				if !slices.Equal(positional[1:], []string{ownTarget}) &&
					!slices.Equal(positional[1:], []string{ownTarget + "-local"}) {
					bad = append(bad, "deletes another target: "+line)
				}
			case len(positional) == 0:
				bad = append(bad, "reads the current target: "+line)
			case len(positional) == 1 || (len(positional) == 2 && !keep):
				bad = append(bad, "selects: "+line)
			}
		default:
			if !pinned {
				bad = append(bad, "unpinned: "+line)
			}
		}
	}

	return bad
}

// The CLI never changes the operator's current safe target: the bytes of
// the current-target entry in ~/.saferc are the same after the bootstrap
// starts the bloc's inception vault, and again after `ocfp init bastion`
// tears it down, whether the run finishes or is interrupted and whether or
// not the operator had a target selected.
func TestSafeCurrentTargetSurvivesInceptionStartAndTeardown(t *testing.T) {
	tests := []struct {
		name      string
		current   string
		interrupt bool
	}{
		{name: "an operator target, run to the end", current: "ops"},
		{name: "an operator target, interrupted", current: "ops", interrupt: true},
		{name: "no current target, run to the end"},
		{name: "no current target, interrupted", interrupt: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newSafeTargetHarness(t, tc.current)
			before := safeCurrentEntry(t)
			require.NotNil(t, before)

			err := h.startFreshVault(t, tc.interrupt)
			if tc.interrupt {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.FileExists(t, h.started, "safe local must have run")
			assert.Equal(t, string(before), string(safeCurrentEntry(t)), "after the inception vault started")

			require.NoError(t, vault.TeardownLocalInception(context.Background(), h.bloc))
			assert.Equal(t, string(before), string(safeCurrentEntry(t)), "after the inception vault was torn down")

			rc, err := readFakeSafeRC()
			require.NoError(t, err)
			assert.NotContains(t, rc.Vaults, h.paths["vaultName"], "teardown removes the bloc's own target")
			assert.NotContains(t, rc.Vaults, h.paths["vaultName"]+"-local", "and the one safe local registered")
			assert.Contains(t, rc.Vaults, "ops", "the operator's own target stays")

			calls := readFakeSafeCalls(t, h.safeLog)
			require.NotEmpty(t, calls)
			assert.Empty(t, safeSelectsOrUnpinned(calls, h.paths["vaultName"]))
		})
	}
}

// Restarting the bloc's existing vault, as `ocfp vault inception` and
// `ocfp vault start` do after a reboot, leaves the current target alone
// too, and never even reads it. Each command restarts the vault on its own
// path, so both are driven.
func TestSafeCurrentTargetSurvivesInceptionRestart(t *testing.T) {
	restarts := map[string]func(*safeTargetHarness, *testing.T) error{
		"vault inception": func(h *safeTargetHarness, t *testing.T) error { return h.startFreshVault(t, false) },
		"vault start":     (*safeTargetHarness).startVaultInPlace,
	}

	for command, restart := range restarts {
		for name, current := range map[string]string{"an operator target": "ops", "no current target": ""} {
			t.Run(command+"/"+name, func(t *testing.T) {
				h := newSafeTargetHarness(t, current)
				require.NoError(t, os.MkdirAll(filepath.Dir(h.paths["rootKeyFile"]), 0o700))
				writeRaftData(t, h.paths["vaultDir"])
				writeKeys(t, h.paths, true, true)

				before := safeCurrentEntry(t)

				require.NoError(t, restart(h, t))
				require.FileExists(t, h.started, "safe local must have run")
				assert.Equal(t, string(before), string(safeCurrentEntry(t)))
				assert.Empty(t, safeSelectsOrUnpinned(readFakeSafeCalls(t, h.safeLog), h.paths["vaultName"]))
			})
		}
	}
}

// deletesTarget lists the calls that delete the named target.
func deletesTarget(calls []fakeSafeCall, name string) []string {
	var found []string

	for _, call := range calls {
		args := call.Args
		for len(args) > 1 && (args[0] == "-T" || args[0] == "--target") {
			args = args[2:]
		}

		if slices.Equal(args, []string{"target", "delete", name}) {
			found = append(found, strings.Join(call.Args, " "))
		}
	}

	return found
}

// Every older ocfp selected the bloc's own inception target, so on a host
// it ran on, current names that target. Starting, restarting, and tearing
// down the vault leave current exactly as it was then too. The target is
// updated in place rather than deleted and made again, so its entry is
// never gone while it is current, and a teardown leaves both the entry and
// the selection for the operator to change.
func TestSafeCurrentTargetSurvivesWhenItIsTheBlocsOwn(t *testing.T) {
	localTeardown := func(h *safeTargetHarness, t *testing.T) error {
		t.Helper()

		return vault.TeardownLocalInception(context.Background(), h.bloc)
	}
	vaultTeardown := func(h *safeTargetHarness, t *testing.T) error {
		t.Helper()

		return h.tearDownVault(t)
	}

	tests := []struct {
		name      string
		saved     bool
		interrupt bool
		inPlace   bool
		teardown  func(h *safeTargetHarness, t *testing.T) error
	}{
		{name: "a fresh vault, then init bastion's teardown", teardown: localTeardown},
		{name: "a fresh vault, interrupted, then init bastion's teardown", interrupt: true, teardown: localTeardown},
		{name: "a restart by vault inception, then vault teardown", saved: true, teardown: vaultTeardown},
		{name: "a restart by vault inception, then init bastion's teardown", saved: true, teardown: localTeardown},
		{name: "a restart by vault start, then vault teardown", saved: true, inPlace: true, teardown: vaultTeardown},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newSafeTargetHarness(t, "")
			h.selectOwnTarget(t)

			if tc.saved {
				h.writeSavedVault(t)
			}

			before := safeCurrentEntry(t)
			require.Equal(t, "current: "+h.paths["vaultName"], string(before))

			var err error

			switch {
			case tc.inPlace:
				err = h.startVaultInPlace(t)
			default:
				err = h.startFreshVault(t, tc.interrupt)
			}

			if tc.interrupt {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.FileExists(t, h.started, "safe local must have run")
			assert.Equal(t, string(before), string(safeCurrentEntry(t)), "after the inception vault started")

			rc, err := readFakeSafeRC()
			require.NoError(t, err)
			require.Contains(t, rc.Vaults, h.paths["vaultName"], "the current target is never deleted")

			if !tc.interrupt {
				own := rc.Vaults[h.paths["vaultName"]]
				assert.Equal(t, h.url(), own.URL)

				want := fakeSafeRootToken
				if tc.saved {
					want = "s.ROOT-TOKEN-SENTINEL"
				}

				assert.Equal(t, want, own.Token, "the target holds the running vault's root token")
			}

			require.NoError(t, tc.teardown(h, t))
			assert.Equal(t, string(before), string(safeCurrentEntry(t)), "after the inception vault was torn down")

			rc, err = readFakeSafeRC()
			require.NoError(t, err)
			assert.Contains(t, rc.Vaults, h.paths["vaultName"], "teardown leaves the current target for the operator")
			assert.NotContains(t, rc.Vaults, h.paths["vaultName"]+"-local", "teardown removes the one safe local registered")
			assert.Contains(t, rc.Vaults, "ops", "the operator's own target stays")

			calls := readFakeSafeCalls(t, h.safeLog)
			assert.Empty(t, safeSelectsOrUnpinned(calls, h.paths["vaultName"]))
			assert.Empty(t, deletesTarget(calls, h.paths["vaultName"]), "the current target is never deleted")
		})
	}
}
