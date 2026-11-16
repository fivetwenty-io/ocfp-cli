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
	// session never exists beforehand.
	writeFakeExecutable(t, bin, "tmux", `case "$1" in
  has-session) exit 1 ;;
  send-keys) sh -c "$4" ;;
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

// startFreshVault runs the inception vault reconcile for a bloc that has no
// vault yet, with the real start, target, and stop steps. When interrupt is
// set, the run's context is cancelled once safe local has registered its
// target, as when the operator stops ocfp part way.
func (h *safeTargetHarness) startFreshVault(t *testing.T, interrupt bool) error {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	orig := vaultOps
	vaultOps = inceptionVaultOps{
		run:      runVaultCommand,
		portOpen: func(context.Context, string) bool { return false },
		sleep: func(time.Duration) {
			if _, err := os.Stat(h.started); interrupt && err == nil {
				cancel()
			}
		},
	}

	t.Cleanup(func() { vaultOps = orig })

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

// safeSelectsOrUnpinned lists every call that could move safe's current
// target, or that reaches a vault without naming one.
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
				if !slices.Equal(positional[1:], []string{ownTarget}) {
					bad = append(bad, "deletes another target: "+line)
				}
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
			assert.Contains(t, rc.Vaults, "ops", "the operator's own target stays")

			calls := readFakeSafeCalls(t, h.safeLog)
			require.NotEmpty(t, calls)
			assert.Empty(t, safeSelectsOrUnpinned(calls, h.paths["vaultName"]))
		})
	}
}
