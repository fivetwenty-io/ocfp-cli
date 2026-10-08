package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSafeScript answers the three safe commands the inception gate runs.
// 'safe targets --json' prints the JSON list on stdout, while 'safe targets'
// and 'safe target' print their reports on stderr, as safe does.
const fakeSafeScript = `#!/bin/sh
dir=$(dirname "$0")
if [ "$1" = targets ] && [ "${2:-}" = --json ]; then
  printf '['
  sep=''
  while IFS= read -r name; do
    [ -n "$name" ] || continue
    printf '%s\n  {\n    "name": "%s",\n    "url": "https://vault.example:8200"\n  }' "$sep" "$name"
    sep=','
  done < "$dir/targets"
  printf '\n]\n'
  exit 0
fi
if [ "$1" = targets ]; then
  while IFS= read -r name; do
    [ -n "$name" ] || continue
    printf '  %s\thttps://vault.example:8200\n' "$name" >&2
  done < "$dir/targets"
  exit 0
fi
if [ "$1" = target ] && [ $# -eq 1 ]; then
  if [ -e "$dir/target_fails" ]; then
    printf 'Current target not found in ~/.saferc\n' >&2
    exit 1
  fi
  if [ -n "${SAFE_TARGET:-}" ]; then
    printf 'SAFE_TARGET names an unknown target\n' >&2
    exit 1
  fi
  current=$(cat "$dir/current")
  if [ -n "$current" ]; then
    printf 'Currently targeting %s at https://vault.example:8200\n' "$current" >&2
  fi
  exit 0
fi
if [ "$1" = target ] && [ $# -eq 2 ]; then
  printf '%s\n' "$2" >> "$dir/calls"
  printf '%s' "$2" > "$dir/current"
  exit 0
fi
exit 1
`

// runInceptionGate runs the inception gate against a fake safe that knows the
// given targets, with current as safe's current target, and returns what the
// gate decided.
func runInceptionGate(t *testing.T, bloc string, targets []string, current string) (string, string) {
	t.Helper()

	return runInceptionGateWith(t, gateSetup{bloc: bloc, targets: targets, current: current})
}

// gateSetup describes the fake safe and the environment an inception gate
// run sees.
type gateSetup struct {
	bloc    string
	targets []string
	current string
	// targetFails makes a bare 'safe target' exit 1, as it does when the
	// rc file names a current target that no longer exists.
	targetFails bool
	// noSafe leaves safe off the PATH altogether, so running it exits 127.
	noSafe bool
	// env is added to the script's environment.
	env []string
	// restore also runs the restore snippet after the gate.
	restore bool
	// restoreOnly runs the restore snippet by itself, with the variables
	// the gate would have set, so the gate's definitions can't help it.
	restoreOnly bool
}

// gateRun is what a gate run decided and did.
type gateRun struct {
	active, target string
	// calls lists every 'safe target <name>' the script made, one name each.
	calls []string
}

func runInceptionGateWith(t *testing.T, gs gateSetup) (string, string) {
	t.Helper()

	run := runGate(t, gs)

	return run.active, run.target
}

func runGate(t *testing.T, gs gateSetup) gateRun {
	t.Helper()

	bloc, targets, current := gs.bloc, gs.targets, gs.current

	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}

	bin := t.TempDir()
	if !gs.noSafe {
		require.NoError(t, os.WriteFile(filepath.Join(bin, "safe"), []byte(fakeSafeScript), 0o700)) // #nosec G306 -- the fake safe must be executable
	}

	require.NoError(t, os.WriteFile(filepath.Join(bin, "targets"), []byte(strings.Join(targets, "\n")+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "current"), []byte(current), 0o600))

	if gs.targetFails {
		require.NoError(t, os.WriteFile(filepath.Join(bin, "target_fails"), nil, 0o600))
	}

	om := NewOCFPManager("pve", nil, nil)
	script := strings.Join([]string{
		"set -euo pipefail",
		`log_info() { :; }`,
		"OCFP_BLOC=" + shellSingleQuote(bloc),
		strings.Join(om.inceptionActiveSnippet(), "\n"),
		`log_success() { :; }`,
		`log_warning() { :; }`,
	}, "\n")
	if gs.restoreOnly {
		script = strings.Join([]string{
			"set -euo pipefail",
			`log_info() { :; }`,
			`log_success() { :; }`,
			`log_warning() { :; }`,
			"BLOC_VAULT_TARGET=" + shellSingleQuote(bloc+"-mgmt"),
			"INCEPTION_ACTIVE=no",
			`INCEPTION_TARGET=""`,
		}, "\n")
		gs.restore = true
	}

	if gs.restore {
		script += "\n" + strings.Join(om.restoreBlocVaultTargetSnippet(), "\n")
	}

	script += "\n" + `printf '%s %s\n' "$INCEPTION_ACTIVE" "$INCEPTION_TARGET"`

	cmd := exec.CommandContext(t.Context(), "bash", "-c", script)
	path := bin + string(os.PathListSeparator) + os.Getenv("PATH")

	if gs.noSafe {
		// Only the tools the snippets use, so a real safe on this machine
		// can't be found.
		tools := t.TempDir()

		for _, tool := range []string{"env", "grep", "sed", "head", "dirname", "cat"} {
			found, err := exec.LookPath(tool)
			require.NoError(t, err)
			require.NoError(t, os.Symlink(found, filepath.Join(tools, tool)))
		}

		path = tools
	}

	cmd.Env = []string{"PATH=" + path, "HOME=" + bin}
	cmd.Env = append(cmd.Env, gs.env...)

	out, err := cmd.Output()
	require.NoError(t, err, string(out))

	active, target, _ := strings.Cut(strings.TrimSpace(string(out)), " ")

	var calls []string

	if raw, err := os.ReadFile(filepath.Join(bin, "calls")); err == nil {
		calls = strings.Fields(string(raw))
	}

	return gateRun{active: active, target: target, calls: calls}
}

// The gate keys off the bloc's own targets rather than safe's current one.
// A bloc that is still being bootstrapped keeps its inception target until
// teardown, whichever target an operator or a put-back made current, and a
// bloc whose own vault has a target is never pointed back at inception.
func TestInceptionActiveSnippet_KeysOffTheBlocTargets(t *testing.T) {
	const bloc = "ocfp-lab-example"

	for name, tc := range map[string]struct {
		bloc       string
		targets    []string
		current    string
		wantActive string
		wantTarget string
	}{
		"bootstrapping, another bloc's target current": {
			bloc: bloc, targets: []string{"ops", bloc + "-inception"}, current: "ops",
			wantActive: "yes", wantTarget: bloc + "-inception",
		},
		"bootstrapping, no target current": {
			bloc: bloc, targets: []string{bloc + "-inception"},
			wantActive: "yes", wantTarget: bloc + "-inception",
		},
		"bootstrapping, inception current": {
			bloc: bloc, targets: []string{bloc + "-inception"}, current: bloc + "-inception",
			wantActive: "yes", wantTarget: bloc + "-inception",
		},
		"bloc vault up, inception still current": {
			bloc: bloc, targets: []string{bloc + "-inception", bloc + "-mgmt"}, current: bloc + "-inception",
			wantActive: "no",
		},
		"only a sibling's inception target, and it is current": {
			bloc: bloc, targets: []string{bloc + "2-inception"}, current: bloc + "2-inception",
			wantActive: "no",
		},
		"only a sibling's mgmt target, and it contains this bloc's": {
			bloc: "lab", targets: []string{"biglab-mgmt", "lab-inception"}, current: "biglab-mgmt",
			wantActive: "yes", wantTarget: "lab-inception",
		},
		"regex characters in the name do not match another target": {
			bloc: "lab.east", targets: []string{"lab-east-mgmt", "lab.east-inception"},
			wantActive: "yes", wantTarget: "lab.east-inception",
		},
		"a star in the name does not match another target": {
			bloc: "lab*", targets: []string{"lab-mgmt", "lab*-inception"},
			wantActive: "yes", wantTarget: "lab*-inception",
		},
		"regex characters in the name do not find a sibling's inception target": {
			bloc: "lab.east", targets: []string{"lab-east-inception"},
			wantActive: "no",
		},
		"a bloc vault target with the exact dotted name wins": {
			bloc: "lab.east", targets: []string{"lab.east-inception", "lab.east-mgmt"},
			wantActive: "no",
		},
		"no inception target": {
			bloc: bloc, targets: []string{"ops"}, current: "ops",
			wantActive: "no",
		},
		"no bloc named, the shared inception target current": {
			targets: []string{"inception"}, current: "inception",
			wantActive: "yes", wantTarget: "inception",
		},
	} {
		t.Run(name, func(t *testing.T) {
			active, target := runInceptionGate(t, tc.bloc, tc.targets, tc.current)
			assert.Equal(t, tc.wantActive, active)
			assert.Equal(t, tc.wantTarget, target)
		})
	}
}

// 'safe target' with no arguments exits non-zero when the rc file names a
// current target that no longer exists, when SAFE_TARGET names a target safe
// doesn't know, and when safe isn't installed. None of those may end the
// phase, which runs under set -e, before it has said anything.
func TestInceptionActiveSnippet_ASafeTargetFailureEndsNothing(t *testing.T) {
	const bloc = "ocfp-lab-example"

	t.Run("the bloc's targets still decide", func(t *testing.T) {
		active, target := runInceptionGateWith(t, gateSetup{
			bloc: bloc, targets: []string{bloc + "-inception"}, current: "ops", targetFails: true,
		})
		assert.Equal(t, "yes", active)
		assert.Equal(t, bloc+"-inception", target)
	})

	t.Run("a bloc vault target still wins", func(t *testing.T) {
		active, _ := runInceptionGateWith(t, gateSetup{
			bloc: bloc, targets: []string{bloc + "-inception", bloc + "-mgmt"}, targetFails: true,
		})
		assert.Equal(t, "no", active)
	})

	t.Run("no bloc named, a failing target means no inception vault", func(t *testing.T) {
		active, target := runInceptionGateWith(t, gateSetup{
			targets: []string{"inception"}, current: "inception", targetFails: true,
		})
		assert.Equal(t, "no", active)
		assert.Empty(t, target)
	})

	t.Run("safe isn't installed, and a named bloc has no inception vault", func(t *testing.T) {
		run := runGate(t, gateSetup{bloc: bloc, noSafe: true, restore: true})
		assert.Equal(t, "no", run.active)
		assert.Empty(t, run.target)
		assert.Empty(t, run.calls)
	})

	t.Run("safe isn't installed, and no bloc is named", func(t *testing.T) {
		run := runGate(t, gateSetup{noSafe: true})
		assert.Equal(t, "no", run.active)
		assert.Empty(t, run.target)
	})

	t.Run("no bloc named, SAFE_TARGET naming an unknown target is ignored", func(t *testing.T) {
		active, target := runInceptionGateWith(t, gateSetup{
			targets: []string{"inception"}, current: "inception", env: []string{"SAFE_TARGET=ghost"},
		})
		assert.Equal(t, "yes", active)
		assert.Equal(t, "inception", target)
	})
}

// The restore compares the exact name of safe's current target, so a
// sibling bloc's target that merely contains the bloc vault's name does not
// stand in for it, and a target already in place is not set again.
func TestRestoreBlocVaultTargetSnippet_MatchesTheCurrentTargetExactly(t *testing.T) {
	for name, tc := range map[string]struct {
		bloc, current string
		targets       []string
		wantCalls     []string
	}{
		"already current": {
			bloc: "lab", current: "lab-mgmt", targets: []string{"lab-mgmt"},
		},
		"a sibling's target contains the name": {
			bloc: "lab", current: "biglab-mgmt", targets: []string{"biglab-mgmt", "lab-mgmt"},
			wantCalls: []string{"lab-mgmt"},
		},
		"another target is current": {
			bloc: "lab", current: "ops", targets: []string{"ops", "lab-mgmt"},
			wantCalls: []string{"lab-mgmt"},
		},
		"no target is current": {
			bloc: "lab", targets: []string{"lab-mgmt"},
			wantCalls: []string{"lab-mgmt"},
		},
		"the bloc vault is not up, so nothing is restored": {
			bloc: "lab", current: "ops", targets: []string{"ops", "lab-inception"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			run := runGate(t, gateSetup{bloc: tc.bloc, targets: tc.targets, current: tc.current, restore: true})
			assert.Equal(t, tc.wantCalls, run.calls)
		})
	}
}

// The restore snippet is emitted next to the gate today, but it must not
// rely on anything the gate defined. Run on its own, with only the variables
// the gate would have set, it still compares the exact current target.
func TestRestoreBlocVaultTargetSnippet_StandsAlone(t *testing.T) {
	for name, tc := range map[string]struct {
		current   string
		wantCalls []string
	}{
		"already current":        {current: "lab-mgmt"},
		"a sibling's is current": {current: "biglab-mgmt", wantCalls: []string{"lab-mgmt"}},
		"none is current":        {wantCalls: []string{"lab-mgmt"}},
	} {
		t.Run(name, func(t *testing.T) {
			run := runGate(t, gateSetup{
				bloc: "lab", targets: []string{"lab-mgmt", "biglab-mgmt"}, current: tc.current, restoreOnly: true,
			})
			assert.Equal(t, tc.wantCalls, run.calls)
		})
	}

	t.Run("a failing safe target does not end the script", func(t *testing.T) {
		run := runGate(t, gateSetup{bloc: "lab", targets: []string{"lab-mgmt"}, targetFails: true, restoreOnly: true})
		assert.Equal(t, []string{"lab-mgmt"}, run.calls)
	})
}
