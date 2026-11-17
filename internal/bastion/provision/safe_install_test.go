package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeReleaseCurl answers the GitHub release lookup with the version in
// $dir/latest and writes a safe that reports that version for a download.
// Each download is logged to $dir/downloads.
const fakeReleaseCurl = `#!/bin/sh
dir=$(dirname "$0")
out=''
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
latest=$(cat "$dir/latest")
if [ -z "$out" ]; then
  printf '{"tag_name":"v%s"}\n' "$latest"
  exit 0
fi
printf 'download\n' >> "$dir/downloads"
printf '#!/bin/sh\nprintf "safe v%s\\n  commit 0000000\\n"\n' "$latest" > "$out"
`

// fakeTagJQ reads the tag name out of the release lookup.
const fakeTagJQ = `#!/bin/sh
sed -n 's/.*"tag_name":"\([^"]*\)".*/\1/p'
`

// safeInstallRun is one run of the bastion's safe installation.
type safeInstallRun struct {
	out       string
	version   string
	downloads int
}

// runSafeInstall runs the bastion's safe installation with an installed safe
// that prints installed for --version, or with none when installed is "",
// and a release feed whose latest version is latest.
func runSafeInstall(t *testing.T, installed, latest string) safeInstallRun {
	t.Helper()

	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}

	bin := t.TempDir()
	dest := filepath.Join(t.TempDir(), "safe")

	require.NoError(t, os.WriteFile(filepath.Join(bin, "curl"), []byte(fakeReleaseCurl), 0o700)) // #nosec G306 -- the fake curl must be executable
	require.NoError(t, os.WriteFile(filepath.Join(bin, "jq"), []byte(fakeTagJQ), 0o700))         // #nosec G306 -- the fake jq must be executable
	require.NoError(t, os.WriteFile(filepath.Join(bin, "latest"), []byte(latest+"\n"), 0o600))

	if installed != "" {
		script := "#!/bin/sh\nprintf '%s\\n  commit 0000000\\n' '" + installed + "'\n"
		require.NoError(t, os.WriteFile(dest, []byte(script), 0o700)) // #nosec G306 -- the installed safe must be executable
	}

	tool := (&Config{}).getSafeTool()
	tool.Dest = dest
	tool.Sudo = false
	tool.Verify = "'" + dest + "' --version"

	script := NewScriptGenerator("pve", &config.Config{Name: "lab", Provider: "pve"}).GenerateBinaryToolScript([]BinaryTool{tool})
	full := strings.Join([]string{
		"set -euo pipefail",
		`log_info() { printf 'INFO %s\n' "$*"; }`,
		`log_success() { printf 'OK %s\n' "$*"; }`,
		`log_warning() { printf 'WARN %s\n' "$*"; }`,
		`log_error() { printf 'ERROR %s\n' "$*"; }`,
		script,
	}, "\n")

	cmd := exec.CommandContext(t.Context(), "bash", "-c", full)
	cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + bin}

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	run := safeInstallRun{out: string(out)}

	if raw, readErr := exec.CommandContext(t.Context(), dest, "--version").Output(); readErr == nil { // #nosec G204 -- runs the fake safe the test wrote
		run.version, _, _ = strings.Cut(string(raw), "\n")
	}

	if raw, readErr := os.ReadFile(filepath.Join(bin, "downloads")); readErr == nil {
		run.downloads = strings.Count(string(raw), "\n")
	}

	return run
}

// The bastion upgrades a safe older than the one ocfp needs rather than
// keep it because a binary is already in place. A safe at or past that
// version is kept, so running the provisioning again downloads nothing, and
// a safe whose version cannot be read is kept with a warning.
func TestSafeInstall_UpgradesAnOlderSafe(t *testing.T) {
	for name, tc := range map[string]struct {
		installed     string
		latest        string
		wantVersion   string
		wantDownloads int
		wantWarning   string
	}{
		"no safe yet":             {latest: "1.27.0", wantVersion: "safe v1.27.0", wantDownloads: 1},
		"an older release":        {installed: "safe v1.26.2", latest: "1.27.0", wantVersion: "safe v1.27.0", wantDownloads: 1},
		"the minimum release":     {installed: "safe v1.27.0", latest: "1.28.0", wantVersion: "safe v1.27.0"},
		"a newer release":         {installed: "safe v1.30.1", latest: "1.30.1", wantVersion: "safe v1.30.1"},
		"a development build":     {installed: "safe (development build)", latest: "1.27.0", wantVersion: "safe (development build)", wantWarning: "could not be read"},
		"no release new enough":   {installed: "safe v1.26.2", latest: "1.26.2", wantVersion: "safe v1.26.2", wantDownloads: 1, wantWarning: "older than 1.27.0"},
		"a pre-release of 1.27.0": {installed: "safe v1.27.0-rc.1", latest: "1.27.0", wantVersion: "safe v1.27.0-rc.1"},
	} {
		t.Run(name, func(t *testing.T) {
			run := runSafeInstall(t, tc.installed, tc.latest)

			assert.Equal(t, tc.wantVersion, run.version, run.out)
			assert.Equal(t, tc.wantDownloads, run.downloads, run.out)

			if tc.wantWarning == "" {
				assert.NotContains(t, run.out, "WARN", run.out)
			} else {
				assert.Contains(t, run.out, "WARN", run.out)
				assert.Contains(t, run.out, tc.wantWarning, run.out)
			}
		})
	}
}

// The safe installation, with its version check, passes shellcheck.
func TestSafeInstall_PassesShellcheck(t *testing.T) {
	t.Parallel()

	shellcheck, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skip("shellcheck is not available")
	}

	script := NewScriptGenerator("pve", &config.Config{Name: "lab", Provider: "pve"}).
		GenerateBinaryToolScript([]BinaryTool{(&Config{}).getSafeTool()})
	stubs := "#!/bin/bash\nset -euo pipefail\nlog_info() { :; }\nlog_success() { :; }\nlog_warning() { :; }\nlog_error() { :; }\n"

	path := filepath.Join(t.TempDir(), "script.sh")
	require.NoError(t, os.WriteFile(path, []byte(stubs+script+"\n"), 0o600))

	out, err := exec.CommandContext(t.Context(), shellcheck, "-s", "bash", "-S", "warning", "-f", "gcc", path).CombinedOutput()
	assert.NoError(t, err, string(out))
}
