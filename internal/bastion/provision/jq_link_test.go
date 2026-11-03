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

// runJQLinkScript runs the generated script with the Linuxbrew prefix and the
// link path pointed into a temp tree (linkDir names the link's directories under
// it), and returns the script's output, the
// link path, and the Homebrew jq path. The log helpers the bastion wrapper
// normally supplies are stubbed to echo.
func runJQLinkScript(t *testing.T, setup func(brewJQ, link, other string), linkDir ...string) (string, string, string) {
	t.Helper()

	// Resolve the temp dir so readlink -f agrees with the Linuxbrew prefix on
	// hosts where the temp dir is itself behind a symlink.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	brewRoot := filepath.Join(dir, "linuxbrew")
	brewJQ := filepath.Join(brewRoot, "bin", "jq")
	link := filepath.Join(append(append([]string{dir}, linkDir...), "jq")...)
	other := filepath.Join(dir, "elsewhere", "jq")

	for _, d := range []string{filepath.Dir(brewJQ), filepath.Dir(other)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	setup(brewJQ, link, other)

	script := NewBrewManager("aws", nil).GenerateJQBrewLinkScript(context.Background())
	script = strings.NewReplacer("sudo ", "", jqBrewRoot, brewRoot, jqLinkPath, link, jqLinkDir, filepath.Dir(link)).Replace(script)
	// The provision runner starts every script with set -euo pipefail.
	script = "set -euo pipefail\nlog_info() { echo \"INFO $1\"; }\nlog_success() { echo \"OK $1\"; }\n" +
		"log_warning() { echo \"WARN $1\"; }\nlog_error() { echo \"ERR $1\"; }\n" + script

	cmd := exec.CommandContext(context.Background(), "bash", "-c", script)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script must always exit 0, got %v\n%s", err, out)
	}

	return string(out), link, brewJQ
}

func writeFile(t *testing.T, path string) {
	t.Helper()

	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// writeBrewJQ makes the Homebrew jq the way brew does, as a symlink into the
// Cellar, so a test can tell the link to it from a link to its target.
func writeBrewJQ(t *testing.T, brewJQ string) {
	t.Helper()

	cellar := filepath.Join(filepath.Dir(filepath.Dir(brewJQ)), "Cellar", "jq", "1.8.1", "bin", "jq")
	if err := os.MkdirAll(filepath.Dir(cellar), 0o755); err != nil {
		t.Fatal(err)
	}

	writeFile(t, cellar)

	if err := os.Symlink(cellar, brewJQ); err != nil {
		t.Fatal(err)
	}
}

func requireLinkTo(t *testing.T, link, want, out string) {
	t.Helper()

	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("expected a symlink at %s: %v\n%s", link, err, out)
	}

	if got != want {
		t.Errorf("link points at %s, want %s (the path itself, not its target)\n%s", got, want, out)
	}
}

func requireWarning(t *testing.T, out string) {
	t.Helper()

	if !strings.Contains(out, "WARN") {
		t.Errorf("expected a warning\n%s", out)
	}
}

// Homebrew's jq is the only reason to skip.
func TestJQBrewLink_WarnsAndSkipsWithoutBrewJQ(t *testing.T) {
	out, link, _ := runJQLinkScript(t, func(_, _, _ string) {})

	if _, err := os.Lstat(link); err == nil {
		t.Errorf("link must not be created without Homebrew jq\n%s", out)
	}

	requireWarning(t, out)
}

func TestJQBrewLink_CreatesLinkToBrewPathNotItsTarget(t *testing.T) {
	out, link, brewJQ := runJQLinkScript(t, func(brewJQ, _, _ string) {
		writeBrewJQ(t, brewJQ)
	})

	requireLinkTo(t, link, brewJQ, out)
}

// apt's jq, or one anywhere else, no longer stops the link. The script never
// consults them, so the test only has to show the link appears with one around.
func TestJQBrewLink_CreatesLinkEvenWhenOtherJQExists(t *testing.T) {
	out, link, brewJQ := runJQLinkScript(t, func(brewJQ, _, other string) {
		writeBrewJQ(t, brewJQ)
		writeFile(t, other)
	})

	requireLinkTo(t, link, brewJQ, out)
}

func TestJQBrewLink_LeavesCorrectLinkAndLogsInfo(t *testing.T) {
	out, link, brewJQ := runJQLinkScript(t, func(brewJQ, link, _ string) {
		writeBrewJQ(t, brewJQ)

		if err := os.Symlink(brewJQ, link); err != nil {
			t.Fatal(err)
		}
	})

	requireLinkTo(t, link, brewJQ, out)

	if !strings.Contains(out, "INFO") || strings.Contains(out, "WARN") {
		t.Errorf("expected an info log and no warning\n%s", out)
	}
}

// A link straight into the versioned Cellar path is ours but stale, because a
// brew upgrade would leave it on the old version.
func TestJQBrewLink_RepointsDirectCellarLink(t *testing.T) {
	out, link, brewJQ := runJQLinkScript(t, func(brewJQ, link, _ string) {
		writeBrewJQ(t, brewJQ)

		target, err := filepath.EvalSymlinks(brewJQ)
		if err != nil {
			t.Fatal(err)
		}

		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	})

	requireLinkTo(t, link, brewJQ, out)
}

// A relative link into the Linuxbrew tree is ours, not foreign.
func TestJQBrewLink_RepointsRelativeLinkIntoLinuxbrewTree(t *testing.T) {
	out, link, brewJQ := runJQLinkScript(t, func(brewJQ, link, _ string) {
		writeBrewJQ(t, brewJQ)

		old := filepath.Join(filepath.Dir(filepath.Dir(brewJQ)), "Cellar", "jq", "1.7.0", "bin", "jq")
		writeFileAll(t, old)

		rel, err := filepath.Rel(filepath.Dir(link), old)
		if err != nil {
			t.Fatal(err)
		}

		if err := os.Symlink(rel, link); err != nil {
			t.Fatal(err)
		}
	})

	requireLinkTo(t, link, brewJQ, out)
}

func TestJQBrewLink_RepointsLinkToLinkIntoLinuxbrewTree(t *testing.T) {
	out, link, brewJQ := runJQLinkScript(t, func(brewJQ, link, other string) {
		writeBrewJQ(t, brewJQ)

		if err := os.Symlink(brewJQ, other); err != nil {
			t.Fatal(err)
		}

		if err := os.Symlink(other, link); err != nil {
			t.Fatal(err)
		}
	})

	requireLinkTo(t, link, brewJQ, out)
}

// ln must replace a stale link that points at a directory, not follow it.
func TestJQBrewLink_ReplacesStaleLinkToDirectory(t *testing.T) {
	out, link, brewJQ := runJQLinkScript(t, func(brewJQ, link, _ string) {
		writeBrewJQ(t, brewJQ)

		if err := os.Symlink(filepath.Dir(brewJQ), link); err != nil {
			t.Fatal(err)
		}
	})

	requireLinkTo(t, link, brewJQ, out)

	if _, err := os.Lstat(filepath.Join(filepath.Dir(brewJQ), "jq.jq")); err == nil {
		t.Errorf("ln followed the directory link\n%s", out)
	}

	if !strings.Contains(out, "OK") {
		t.Errorf("expected a success log\n%s", out)
	}
}

func TestJQBrewLink_CreatesMissingLinkDirectory(t *testing.T) {
	out, link, brewJQ := runJQLinkScript(t, func(brewJQ, link, _ string) {
		writeBrewJQ(t, brewJQ)
	}, "sub", "bin")

	requireLinkTo(t, link, brewJQ, out)
}

// Homebrew's jq that cannot run must not be linked ahead of apt's jq.
func TestJQBrewLink_SkipsBrewJQThatDoesNotRun(t *testing.T) {
	out, link, _ := runJQLinkScript(t, func(brewJQ, _, _ string) {
		writeBrokenFile(t, brewJQ)
	})

	if _, err := os.Lstat(link); err == nil {
		t.Errorf("link must not be created for a jq that does not run\n%s", out)
	}

	requireWarning(t, out)
}

func TestJQBrewLink_LeavesOurLinkWhenBrewJQDoesNotRun(t *testing.T) {
	out, link, brewJQ := runJQLinkScript(t, func(brewJQ, link, _ string) {
		writeBrokenFile(t, brewJQ)

		if err := os.Symlink(brewJQ, link); err != nil {
			t.Fatal(err)
		}
	})

	requireLinkTo(t, link, brewJQ, out)
	requireWarning(t, out)
}

func writeBrokenFile(t *testing.T, path string) {
	t.Helper()

	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestJQBrewLink_RepointsDanglingLink(t *testing.T) {
	out, link, brewJQ := runJQLinkScript(t, func(brewJQ, link, other string) {
		writeBrewJQ(t, brewJQ)

		if err := os.Symlink(filepath.Join(filepath.Dir(other), "gone"), link); err != nil {
			t.Fatal(err)
		}
	})

	requireLinkTo(t, link, brewJQ, out)
}

func TestJQBrewLink_RepointsLinkIntoLinuxbrewTree(t *testing.T) {
	out, link, brewJQ := runJQLinkScript(t, func(brewJQ, link, _ string) {
		writeBrewJQ(t, brewJQ)

		old := filepath.Join(filepath.Dir(filepath.Dir(brewJQ)), "Cellar", "jq", "1.7.0", "bin", "jq")
		writeFileAll(t, old)

		if err := os.Symlink(old, link); err != nil {
			t.Fatal(err)
		}
	})

	requireLinkTo(t, link, brewJQ, out)
}

func TestJQBrewLink_RepointsLinkToGraft(t *testing.T) {
	out, link, brewJQ := runJQLinkScript(t, func(brewJQ, link, other string) {
		writeBrewJQ(t, brewJQ)

		graft := filepath.Join(filepath.Dir(other), "graft")
		writeFile(t, graft)

		if err := os.Symlink(graft, link); err != nil {
			t.Fatal(err)
		}
	})

	requireLinkTo(t, link, brewJQ, out)
}

func TestJQBrewLink_LeavesRealBinaryAndWarns(t *testing.T) {
	out, link, _ := runJQLinkScript(t, func(brewJQ, link, _ string) {
		writeBrewJQ(t, brewJQ)
		writeFile(t, link)
	})

	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("a real binary must be left in place\n%s", out)
	}

	requireWarning(t, out)
}

func TestJQBrewLink_LeavesForeignSymlinkAndWarns(t *testing.T) {
	var foreign string

	out, link, _ := runJQLinkScript(t, func(brewJQ, link, other string) {
		writeBrewJQ(t, brewJQ)
		writeFile(t, other)

		foreign = other
		if err := os.Symlink(other, link); err != nil {
			t.Fatal(err)
		}
	})

	if got, _ := os.Readlink(link); got != foreign {
		t.Errorf("foreign symlink was changed to %q\n%s", got, out)
	}

	requireWarning(t, out)
}

func TestJQBrewLink_FailedLinkWarnsAndExitsZero(t *testing.T) {
	out, _, _ := runJQLinkScript(t, func(brewJQ, link, _ string) {
		writeBrewJQ(t, brewJQ)

		// A file where the link's directory belongs makes mkdir fail.
		writeFile(t, filepath.Dir(link))
	}, "blocked")

	requireWarning(t, out)
}

func writeFileAll(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	writeFile(t, path)
}

func TestGenerateJQBrewLinkScript_Decisions(t *testing.T) {
	t.Parallel()

	script := NewBrewManager("aws", nil).GenerateJQBrewLinkScript(context.Background())

	for _, want := range []string{
		"/home/linuxbrew/.linuxbrew/bin/jq",
		"sudo ln -sfn /home/linuxbrew/.linuxbrew/bin/jq /usr/local/bin/jq",
		"sudo mkdir -p /usr/local/bin",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("expected script to contain %q\ngot:\n%s", want, script)
		}
	}

	for _, bad := range []string{"command -v graft", "/usr/bin/jq", "Caskroom"} {
		if strings.Contains(script, bad) {
			t.Errorf("script must not contain %q\ngot:\n%s", bad, script)
		}
	}
}

// The offline provision script carries the same link, after the spruce one.
func TestProvisionScript_JQBrewLinkAfterSpruceLink(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	sg := NewScriptGenerator("aws", cfg)
	var parts []string

	sg.addPackageManagementSections(context.Background(), NewConfig("aws", cfg, nil), &parts)

	got := strings.Join(parts, "\n")
	spruceIdx := strings.Index(got, "Link graft as spruce")
	jqIdx := strings.Index(got, "/usr/local/bin/jq")

	if spruceIdx == -1 || jqIdx == -1 || jqIdx < spruceIdx {
		t.Errorf("jq link must follow spruce link (spruce=%d jq=%d)", spruceIdx, jqIdx)
	}
}
