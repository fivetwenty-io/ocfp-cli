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
  current=$(cat "$dir/current")
  if [ -n "$current" ]; then
    printf 'Currently targeting %s at https://vault.example:8200\n' "$current" >&2
  fi
  exit 0
fi
exit 1
`

// runInceptionGate runs the inception gate against a fake safe that knows the
// given targets, with current as safe's current target, and returns what the
// gate decided.
func runInceptionGate(t *testing.T, bloc string, targets []string, current string) (string, string) {
	t.Helper()

	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}

	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "safe"), []byte(fakeSafeScript), 0o700)) // #nosec G306 -- the fake safe must be executable
	require.NoError(t, os.WriteFile(filepath.Join(bin, "targets"), []byte(strings.Join(targets, "\n")+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "current"), []byte(current), 0o600))

	om := NewOCFPManager("pve", nil, nil)
	script := strings.Join([]string{
		"set -euo pipefail",
		`log_info() { :; }`,
		"OCFP_BLOC=" + shellSingleQuote(bloc),
		strings.Join(om.inceptionActiveSnippet(), "\n"),
		`printf '%s %s\n' "$INCEPTION_ACTIVE" "$INCEPTION_TARGET"`,
	}, "\n")

	cmd := exec.CommandContext(t.Context(), "bash", "-c", script)
	cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + bin}

	out, err := cmd.Output()
	require.NoError(t, err, string(out))

	active, target, _ := strings.Cut(strings.TrimSpace(string(out)), " ")

	return active, target
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
