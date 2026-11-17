package provision

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinnedFakeSafeScript is a safe that answers reads only through the
// targets listed in its allowed file, named with -T. Like safe, it refuses
// to list targets when SAFE_TARGET names one it does not know. A bare read, which safe
// would send to the operator's current target, fails. Every call is logged,
// one line each, to the calls file.
const pinnedFakeSafeScript = `#!/bin/sh
dir=$(dirname "$0")
printf '%s\n' "$*" >> "$dir/calls"
if [ "$1" = targets ] && [ -n "${SAFE_TARGET:-}" ] && ! grep -Fx -- "$SAFE_TARGET" "$dir/targets" >/dev/null; then
  printf '!! unknown target %s\n' "$SAFE_TARGET" >&2
  exit 1
fi
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
if [ "$1" != -T ] || ! grep -Fx -- "$2" "$dir/allowed" >/dev/null; then
  printf '!! read went to the wrong vault\n' >&2
  exit 1
fi
shift 2
case "$1" in
  exists) exit 0 ;;
  tree) printf 'secret/config/lab\n'; exit 0 ;;
  get)
    case "$2" in
      *:host) printf 'pve.example\n' ;;
      *:node) printf 'pve1\n' ;;
      *:token_id) printf 'root@pam!ocfp\n' ;;
      *:token_secret) printf 's3cret\n' ;;
      *) printf '\n' ;;
    esac
    exit 0
    ;;
esac
exit 1
`

// fakePMXScript is a pmx whose context list always holds the bloc's context.
const fakePMXScript = `#!/bin/sh
if [ "$1" = ctx ] && [ "$2" = list ]; then
  printf 'lab-cpi\n'
fi
exit 0
`

// safeScriptRun is one run of a bastion script against the pinned fake safe.
type safeScriptRun struct {
	out   string
	err   error
	calls []string
}

// runWithPinnedSafe runs script under bash with OCFP_BLOC=lab, a fake safe
// that knows targets and answers only through allowed, a fake pmx, and env
// added to the environment.
func runWithPinnedSafe(t *testing.T, script string, targets, allowed []string, env ...string) safeScriptRun {
	t.Helper()

	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}

	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "safe"), []byte(pinnedFakeSafeScript), 0o700)) // #nosec G306 -- the fake safe must be executable
	require.NoError(t, os.WriteFile(filepath.Join(bin, "pmx"), []byte(fakePMXScript), 0o700))         // #nosec G306 -- the fake pmx must be executable
	require.NoError(t, os.WriteFile(filepath.Join(bin, "targets"), []byte(strings.Join(targets, "\n")+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "allowed"), []byte(strings.Join(allowed, "\n")+"\n"), 0o600))

	full := strings.Join([]string{
		"set -euo pipefail",
		`log_info() { :; }`,
		`log_success() { :; }`,
		`log_warning() { printf 'WARN %s\n' "$*"; }`,
		`log_error() { printf 'ERROR %s\n' "$*"; }`,
		"OCFP_BLOC=lab",
		script,
		`printf 'BLOC_SAFE_TARGET=%s\n' "$BLOC_SAFE_TARGET"`,
	}, "\n")

	cmd := exec.CommandContext(t.Context(), "bash", "-c", full)
	cmd.Env = []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + bin,
		"XDG_CONFIG_HOME=" + filepath.Join(bin, "config"),
	}
	cmd.Env = append(cmd.Env, env...)

	out, err := cmd.CombinedOutput()

	var calls []string
	if raw, readErr := os.ReadFile(filepath.Join(bin, "calls")); readErr == nil {
		calls = strings.Split(strings.TrimSpace(string(raw)), "\n")
	}

	return safeScriptRun{out: string(out), err: err, calls: calls}
}

// vaultReads returns the logged calls that reach a vault, leaving out the
// target listing, which reads only ~/.saferc.
func vaultReads(calls []string) []string {
	var reads []string

	for _, call := range calls {
		if call != "" && call != "targets --json" {
			reads = append(reads, call)
		}
	}

	return reads
}

// The bastion's safe reads name the bloc's own target with -T. The bloc
// vault's mgmt target wins once it is registered, the inception target
// stands in until then, and a sibling's target that merely contains the
// bloc's name never counts. With no target of its own the read refuses
// rather than go to the operator's current target.
func TestBlocSafeTargetSnippet_PinsReadsToTheBlocsOwnTarget(t *testing.T) {
	for name, tc := range map[string]struct {
		targets    []string
		wantTarget string
	}{
		"the bloc vault is up":      {targets: []string{"ops", "lab-inception", "lab-mgmt"}, wantTarget: "lab-mgmt"},
		"the bloc is bootstrapping": {targets: []string{"ops", "lab-inception"}, wantTarget: "lab-inception"},
		"only siblings' targets":    {targets: []string{"ops", "biglab-mgmt", "lab-east-inception"}},
		"no targets at all":         {},
	} {
		t.Run(name, func(t *testing.T) {
			script := strings.Join(blocSafeTargetSnippet(""), "\n") + "\n" +
				`if VALUE="$(ocfp_bloc_safe get secret/config/lab:key)"; then printf 'READ ok\n'; else printf 'READ refused\n'; fi`

			run := runWithPinnedSafe(t, script, tc.targets, []string{tc.wantTarget})
			require.NoError(t, run.err, run.out)
			assert.Contains(t, run.out, "BLOC_SAFE_TARGET="+tc.wantTarget+"\n")

			if tc.wantTarget == "" {
				assert.Contains(t, run.out, "READ refused")
				assert.Empty(t, vaultReads(run.calls), "a refused read never runs safe")

				return
			}

			assert.Contains(t, run.out, "READ ok")
			assert.Equal(t, []string{"-T " + tc.wantTarget + " get secret/config/lab:key"}, vaultReads(run.calls))
		})
	}
}

// An operator's exported SAFE_TARGET, known to safe or not, never hides the
// bloc's own targets, because they are listed without it.
func TestBlocSafeTargetSnippet_IgnoresSafeTargetFromTheEnvironment(t *testing.T) {
	for _, safeTarget := range []string{"ghost", "ops"} {
		t.Run(safeTarget, func(t *testing.T) {
			script := strings.Join(blocSafeTargetSnippet(""), "\n")
			run := runWithPinnedSafe(t, script, []string{"ops", "lab-inception", "lab-mgmt"}, nil, "SAFE_TARGET="+safeTarget)
			require.NoError(t, run.err, run.out)
			assert.Contains(t, run.out, "BLOC_SAFE_TARGET=lab-mgmt\n")
		})
	}
}

// safe being absent must not end a set -e script while the target is
// resolved; the bloc simply has no target.
func TestBlocSafeTargetSnippet_NoSafeEndsNothing(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}

	tools := t.TempDir()

	for _, tool := range []string{"grep", "sed", "printf"} {
		found, err := exec.LookPath(tool)
		require.NoError(t, err)
		require.NoError(t, os.Symlink(found, filepath.Join(tools, tool)))
	}

	script := "set -euo pipefail\nOCFP_BLOC=lab\n" + strings.Join(blocSafeTargetSnippet(""), "\n") +
		"\nprintf 'BLOC_SAFE_TARGET=%s\\n' \"$BLOC_SAFE_TARGET\"\n"

	cmd := exec.CommandContext(t.Context(), "bash", "-c", script)
	cmd.Env = []string{"PATH=" + tools, "HOME=" + tools}

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "BLOC_SAFE_TARGET=\n")
}

// pmx_context reads the PVE CPI credentials through the bloc's own target
// on a fresh bastion, where nothing selects that target any more, and on an
// established one, where the operator's current target may be any vault.
func TestGeneratePMXContextScript_ReadsThroughTheBlocsTarget(t *testing.T) {
	script := NewOCFPManager("pve", &config.Config{Name: "lab", Provider: "pve"}, nil).
		GeneratePMXContextScript(context.Background())

	for name, tc := range map[string]struct {
		targets []string
		want    string
	}{
		"bootstrapping": {targets: []string{"ops", "lab-inception"}, want: "lab-inception"},
		"bloc vault up": {targets: []string{"ops", "lab-inception", "lab-mgmt"}, want: "lab-mgmt"},
	} {
		t.Run(name, func(t *testing.T) {
			run := runWithPinnedSafe(t, script, tc.targets, []string{tc.want})
			require.NoError(t, run.err, run.out)

			reads := vaultReads(run.calls)
			require.NotEmpty(t, reads)

			for _, read := range reads {
				assert.True(t, strings.HasPrefix(read, "-T "+tc.want+" "), "unpinned or misdirected: %s", read)
			}
		})
	}

	t.Run("no target of the bloc's own", func(t *testing.T) {
		run := runWithPinnedSafe(t, script, []string{"ops"}, nil)
		require.Error(t, run.err)
		assert.Contains(t, run.out, "no safe target is registered for bloc lab")
		assert.Empty(t, vaultReads(run.calls))
	})
}

// No bastion script reads a vault through safe's current target. Each read
// goes through ocfp_bloc_safe, and the vault checks ask whether the bloc's
// target is registered rather than which target is current.
func TestBastionScripts_NeverReadThroughTheCurrentTarget(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "lab", Provider: "pve"}
	om := NewOCFPManager("pve", cfg, nil)
	vm := NewVerificationManager("pve", cfg)

	// safe in command position: at the start of a line, in a command
	// substitution, or after !, a pipe, && or ;.
	bare := regexp.MustCompile(`(^\s*|\$\(\s*|!\s+|\|\s*|&&\s*|;\s*)safe (get|exists|tree|paths|read|target)\b`)

	for name, script := range map[string]string{
		"pmx context":    om.GeneratePMXContextScript(context.Background()),
		"vault populate": om.GenerateVaultPopulateScript(context.Background()),
		"health check":   vm.GenerateHealthCheckScript(context.Background()),
	} {
		for line := range strings.SplitSeq(script, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}

			assert.NotRegexp(t, bare, line, "%s reads through the current target", name)
		}

		assert.Contains(t, script, "ocfp_bloc_safe() {", name)
		assert.Contains(t, script, `safe -T "$BLOC_SAFE_TARGET" "$@"`, name)
	}
}

// The scripts that resolve or select the bloc's target pass shellcheck,
// alone and joined into one provisioning script, where the helper is defined
// twice.
func TestBastionScripts_BlocSafeTargetPassesShellcheck(t *testing.T) {
	t.Parallel()

	shellcheck, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skip("shellcheck is not available")
	}

	cfg := &config.Config{Name: "lab", Provider: "pve"}
	om := NewOCFPManager("pve", cfg, nil)
	vm := NewVerificationManager("pve", cfg)

	pmx := om.GeneratePMXContextScript(context.Background())
	populate := om.GenerateVaultPopulateScript(context.Background())
	health := vm.GenerateHealthCheckScript(context.Background())
	inception := om.GenerateVaultInceptionScript(context.Background())

	stubs := "#!/bin/bash\nset -euo pipefail\nlog_info() { :; }\nlog_success() { :; }\n" +
		"log_warning() { :; }\nlog_error() { :; }\nexport OCFP_BLOC=lab OCFP_PROVIDER=pve\n"

	for name, script := range map[string]string{
		"pmx context":     pmx,
		"vault populate":  populate,
		"health check":    health,
		"vault inception": inception,
		"all of them":     inception + "\n" + populate + "\n" + health + "\n" + pmx,
	} {
		path := filepath.Join(t.TempDir(), "script.sh")
		require.NoError(t, os.WriteFile(path, []byte(stubs+script+"\n"), 0o600))

		out, err := exec.CommandContext(t.Context(), shellcheck, "-s", "bash", "-S", "warning", "-f", "gcc", path).CombinedOutput()
		assert.NoError(t, err, "%s:\n%s", name, out)
	}
}
