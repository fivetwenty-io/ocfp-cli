package provision

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
)

// runEtcEnvironmentPathOrder runs the generated /etc/environment PATH
// transform against file under bash with set -euo pipefail and returns its
// output.
func runEtcEnvironmentPathOrder(t *testing.T, brewPrefix, file string) string {
	t.Helper()

	script := "set -euo pipefail\n" +
		"log_warning() { echo \"WARN $1\"; }\n" +
		etcEnvironmentPathFunction(brewPrefix) + "\n" +
		etcEnvironmentPathFunctionName + ` "$1" /etc/environment` + "\n"
	cmd := exec.Command(bashShell(t), "-c", script, "etc-environment", file) // #nosec G204 -- test fixture
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + t.TempDir()}

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("transform failed: %v\n%s", err, out)
	}

	return string(out)
}

// brewFixture makes a Linuxbrew prefix with a bin dir and returns the
// prefix and its bin and sbin paths.
func brewFixture(t *testing.T) (string, string, string) {
	t.Helper()

	brew := filepath.Join(t.TempDir(), "linuxbrew")
	bbin, bsbin := filepath.Join(brew, "bin"), filepath.Join(brew, "sbin")

	if err := os.MkdirAll(bbin, 0o755); err != nil {
		t.Fatal(err)
	}

	return brew, bbin, bsbin
}

func TestEtcEnvironmentPath_Transforms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input func(bbin, bsbin string) string
		want  func(bbin, bsbin string) string
	}{
		{
			name: "quoted ubuntu default",
			input: func(_, _ string) string {
				return "OCFP_BLOC=x\nPATH=\"" + ubuntuDefaultPath + "\"\nOCFP_PROVIDER=pve\n"
			},
			want: func(bbin, bsbin string) string {
				return "OCFP_BLOC=x\nPATH=\"" + bbin + ":" + bsbin + ":" + ubuntuDefaultPath + "\"\nOCFP_PROVIDER=pve\n"
			},
		},
		{
			name:  "unquoted value stays unquoted",
			input: func(_, _ string) string { return "PATH=/usr/bin:/bin\n" },
			want: func(bbin, bsbin string) string {
				return "PATH=" + bbin + ":" + bsbin + ":/usr/bin:/bin\n"
			},
		},
		{
			name:  "single-quoted value stays single-quoted",
			input: func(_, _ string) string { return "PATH='/usr/bin:/bin'\n" },
			want: func(bbin, bsbin string) string {
				return "PATH='" + bbin + ":" + bsbin + ":/usr/bin:/bin'\n"
			},
		},
		{
			name:  "indented line keeps its indent",
			input: func(_, _ string) string { return "  \tPATH=\"/opt/site/bin:/usr/bin:/bin\"\n" },
			want: func(bbin, bsbin string) string {
				return "  \tPATH=\"" + bbin + ":" + bsbin + ":/opt/site/bin:/usr/bin:/bin\"\n"
			},
		},
		{
			name:  "export line keeps its export",
			input: func(_, _ string) string { return "export PATH=/opt/site/bin:/usr/bin:/bin\n" },
			want: func(bbin, bsbin string) string {
				return "export PATH=" + bbin + ":" + bsbin + ":/opt/site/bin:/usr/bin:/bin\n"
			},
		},
		{
			name: "plain line followed by an export line",
			input: func(_, _ string) string {
				return "PATH=\"/usr/bin:/bin\"\nOCFP_BLOC=x\nexport PATH=/opt/site/bin:/usr/bin:/bin\n"
			},
			want: func(bbin, bsbin string) string {
				return "PATH=\"" + bbin + ":" + bsbin + ":/usr/bin:/bin\"\nOCFP_BLOC=x\n" +
					"export PATH=" + bbin + ":" + bsbin + ":/opt/site/bin:/usr/bin:/bin\n"
			},
		},
		{
			name:  "trailing whitespace and CR are kept",
			input: func(_, _ string) string { return "PATH=\"/usr/bin:/bin\" \r\n" },
			want: func(bbin, bsbin string) string {
				return "PATH=\"" + bbin + ":" + bsbin + ":/usr/bin:/bin\" \r\n"
			},
		},
		{
			name:  "missing PATH line starts from the ubuntu default",
			input: func(_, _ string) string { return "OCFP_BLOC=x\n" },
			want: func(bbin, bsbin string) string {
				return "OCFP_BLOC=x\nPATH=\"" + bbin + ":" + bsbin + ":" + ubuntuDefaultPath + "\"\n"
			},
		},
		{
			name:  "commented-out PATH line does not count",
			input: func(_, _ string) string { return "# PATH=/usr/bin\n" },
			want: func(bbin, bsbin string) string {
				return "# PATH=/usr/bin\nPATH=\"" + bbin + ":" + bsbin + ":" + ubuntuDefaultPath + "\"\n"
			},
		},
		{
			name: "brew already present, duplicates and empty entries",
			input: func(bbin, bsbin string) string {
				return "PATH=\"/usr/bin:" + bsbin + "::/usr/bin:" + bbin + ":/bin:\"\n"
			},
			want: func(bbin, bsbin string) string {
				return "PATH=\"" + bbin + ":" + bsbin + ":/usr/bin:/bin\"\n"
			},
		},
		{
			name:  "empty file",
			input: func(_, _ string) string { return "" },
			want: func(bbin, bsbin string) string {
				return "PATH=\"" + bbin + ":" + bsbin + ":" + ubuntuDefaultPath + "\"\n"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			brew, bbin, bsbin := brewFixture(t)

			file := filepath.Join(t.TempDir(), "environment")
			writePathFile(t, file, tc.input(bbin, bsbin), 0o644)

			if out := runEtcEnvironmentPathOrder(t, brew, file); strings.Contains(out, "WARN") {
				t.Fatalf("unexpected warning:\n%s", out)
			}

			got := readFile(t, file)
			if want := tc.want(bbin, bsbin); got != want {
				t.Fatalf("first run:\n got %q\nwant %q", got, want)
			}

			runEtcEnvironmentPathOrder(t, brew, file)

			if again := readFile(t, file); again != got {
				t.Fatalf("second run changed the file:\n got %q\nwant %q", again, got)
			}
		})
	}
}

func TestEtcEnvironmentPath_UnrecognizedPathLeavesFileAlone(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ content, reason string }{
		"trailing comment":            {"PATH=\"/opt/site/bin:/usr/bin:/bin\"#standard\n", "#"},
		"mismatched quotes":           {"PATH=\"/usr/bin:/bin'\n", "quote"},
		"unterminated double quote":   {"PATH=\"/usr/bin:/bin\n", "quote"},
		"stray quote in a bare value": {"PATH=/usr/bin:/b\"in\n", "quote"},
		"embedded quote of same kind": {"PATH='/usr/bin:'/bin'\n", "quote"},
		"whitespace in a bare value":  {"PATH=/usr/bin /bin\n", "whitespace"},
		"line continuation":           {"PATH=\"/usr/bin:\\\n/bin\"\n", "backslash"},
		"space before the equals":     {"PATH =/usr/bin\n", "form"},
		"two spaces after export":     {"export  PATH=/usr/bin\n", "form"},
		"no entries":                  {"PATH=\"\"\n", "no entries"},
		"good line then a bad one":    {"PATH=/usr/bin\nexport PATH=\"/opt/bin\" # site\n", "#"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			brew, _, _ := brewFixture(t)
			file := filepath.Join(t.TempDir(), "environment")
			content := "OCFP_BLOC=x\n" + tc.content
			writePathFile(t, file, content, 0o644)

			out := runEtcEnvironmentPathOrder(t, brew, file)

			if got := readFile(t, file); got != content {
				t.Fatalf("file changed:\n got %q\nwant %q", got, content)
			}

			warnings := warningsAbout(out, "/etc/environment")
			if len(warnings) != 1 || !strings.Contains(warnings[0], tc.reason) {
				t.Fatalf("expected one warning naming /etc/environment and %q, got:\n%s", tc.reason, out)
			}
		})
	}
}

// runEtcEnvironmentPathCheck runs the post-install PATH check against file
// and returns its output.
func runEtcEnvironmentPathCheck(t *testing.T, brewPrefix, file string) string {
	t.Helper()

	script := "set -euo pipefail\n" +
		"log_success() { echo \"OK $1\"; }\n" +
		"log_warning() { echo \"WARN $1\"; }\n" +
		etcEnvironmentPathCheck(brewPrefix, file) + "\n"
	cmd := exec.Command(bashShell(t), "-c", script) // #nosec G204 -- test fixture
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("check failed: %v\n%s", err, out)
	}

	return string(out)
}

func TestEtcEnvironmentPath_CheckReadsTheLastPathLine(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		content func(bbin, bsbin string) string
		ok      bool
	}{
		"double-quoted": {func(b, s string) string { return "PATH=\"" + b + ":" + s + ":/usr/bin\"\n" }, true},
		"single-quoted": {func(b, s string) string { return "PATH='" + b + ":" + s + ":/usr/bin'\n" }, true},
		"bare export":   {func(b, s string) string { return "  export PATH=" + b + ":" + s + ":/usr/bin\n" }, true},
		"only brew":     {func(b, s string) string { return "PATH=" + b + ":" + s + "\n" }, true},
		"not first":     {func(b, s string) string { return "PATH=\"/usr/bin:" + b + ":" + s + "\"\n" }, false},
		"later override": {func(b, s string) string {
			return "PATH=\"" + b + ":" + s + ":/usr/bin\"\nexport PATH=/usr/bin\n"
		}, false},
		"prefix of a longer dir": {func(b, s string) string { return "PATH=" + b + ":" + s + "x:/usr/bin\n" }, false},
		"no PATH line":           {func(_, _ string) string { return "OCFP_BLOC=x\n" }, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			brew, bbin, bsbin := brewFixture(t)
			file := filepath.Join(t.TempDir(), "environment")
			writePathFile(t, file, tc.content(bbin, bsbin), 0o644)

			out := runEtcEnvironmentPathCheck(t, brew, file)
			if got := strings.HasPrefix(out, "OK "); got != tc.ok {
				t.Fatalf("check said %q for %q, want ok=%v", out, readFile(t, file), tc.ok)
			}
		})
	}
}

func TestEtcEnvironmentPath_NoBrewLeavesFileAlone(t *testing.T) {
	t.Parallel()

	brew := filepath.Join(t.TempDir(), "linuxbrew")
	file := filepath.Join(t.TempDir(), "environment")
	content := "PATH=/usr/bin:/usr/bin\nOCFP_BLOC=x"
	writePathFile(t, file, content, 0o644)

	runEtcEnvironmentPathOrder(t, brew, file)

	if got := readFile(t, file); got != content {
		t.Fatalf("file changed without a brew dir: %q", got)
	}
}

func TestSystemEnvironment_OrdersEtcEnvironmentPathBeforeInstall(t *testing.T) {
	t.Parallel()

	em := NewEnvironmentManager("aws", &config.Config{Name: "test-bloc"})
	script := em.GenerateSystemEnvironmentScript(context.Background())

	if !strings.Contains(script, etcEnvironmentPathFunction(linuxbrewPrefix)) {
		t.Fatalf("system script does not define the PATH transform for %s:\n%s", linuxbrewPrefix, script)
	}

	call := strings.Index(script, etcEnvironmentPathFunctionName+` "$TMP_ENV" /etc/environment`)
	install := strings.Index(script, `sudo install -m 0644 "$TMP_ENV" /etc/environment`)
	lastKey := strings.LastIndex(script, `>> "$TMP_ENV"`)

	if call < 0 || install < 0 || call > install || call < lastKey {
		t.Fatalf("PATH transform must run on $TMP_ENV after the key updates and before install:\n%s", script)
	}

	if !strings.Contains(script, etcEnvironmentPathCheck(linuxbrewPrefix, "/etc/environment")) {
		t.Fatalf("system script does not check the installed PATH line:\n%s", script)
	}

	for _, key := range []string{"OCFP_BLOC", "OCFP_PROVIDER", "GENESIS_ENVIRONMENT"} {
		if !strings.Contains(script, "log_success '"+key+" set in /etc/environment'") {
			t.Errorf("verification line for %s is gone", key)
		}
	}
}
