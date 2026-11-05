package provision

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
)

// posixShell returns dash when the host has it, since Ubuntu sources
// ~/.profile and /etc/profile.d with dash, and /bin/sh otherwise.
func posixShell(t *testing.T) string {
	t.Helper()

	for _, candidate := range []string{"/bin/dash", "/usr/bin/dash"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	return "/bin/sh"
}

func bashShell(t *testing.T) string {
	t.Helper()

	path, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	return path
}

// snippetShells lists every shell the PATH snippet must work under.
func snippetShells(t *testing.T) map[string]string {
	t.Helper()

	return map[string]string{"sh": posixShell(t), "bash": bashShell(t)}
}

// sourceSnippet writes script to a file, sources it under shell with set -eu
// in an environment holding only the given variables, and returns the PATH
// it leaves behind plus the shell's variable listing. The environment has no
// usable PATH for external tools, so the snippet must stick to builtins.
func sourceSnippet(t *testing.T, shell, script string, env []string) (string, string) {
	t.Helper()

	file := filepath.Join(t.TempDir(), "snippet.sh")
	if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	runner := `set -eu; . "$1"; printf '%s\n' "$PATH"; set`
	cmd := exec.Command(shell, "-c", runner, "snippet", file) // #nosec G204 -- test fixture
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed sourcing snippet: %v\n%s", shell, err, out)
	}

	first, rest, _ := strings.Cut(string(out), "\n")

	return first, rest
}

type snippetFixture struct {
	home  string
	brew  string
	hbin  string
	bbin  string
	bsbin string
}

func newSnippetFixture(t *testing.T, withHomeBin, withBrew bool) snippetFixture {
	t.Helper()

	fx := snippetFixture{home: t.TempDir(), brew: filepath.Join(t.TempDir(), "linuxbrew")}
	fx.hbin = filepath.Join(fx.home, "bin")
	fx.bbin = filepath.Join(fx.brew, "bin")
	fx.bsbin = filepath.Join(fx.brew, "sbin")

	if withHomeBin {
		if err := os.MkdirAll(fx.hbin, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if withBrew {
		if err := os.MkdirAll(fx.bbin, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	return fx
}

func TestPathOrderSnippet_Orders(t *testing.T) {
	t.Parallel()

	bun := "/home/u/.bun/bin"
	bunCopies := strings.TrimSuffix(strings.Repeat(bun+":", 36), ":")

	cases := []struct {
		name        string
		withHomeBin bool
		withBrew    bool
		path        func(fx snippetFixture) string
		want        func(fx snippetFixture) string
	}{
		{
			name:        "duplicates and empty entries",
			withHomeBin: true,
			withBrew:    true,
			path:        func(_ snippetFixture) string { return ":/usr/bin::/bin:/usr/bin:" },
			want: func(fx snippetFixture) string {
				return strings.Join([]string{fx.hbin, fx.bbin, fx.bsbin, "/usr/bin", "/bin"}, ":")
			},
		},
		{
			name:        "brew already in the middle",
			withHomeBin: true,
			withBrew:    true,
			path: func(fx snippetFixture) string {
				return strings.Join([]string{"/home/u/.local/bin", fx.bsbin, "/usr/bin", fx.bbin, fx.hbin, "/bin"}, ":")
			},
			want: func(fx snippetFixture) string {
				return strings.Join([]string{fx.hbin, fx.bbin, fx.bsbin, "/home/u/.local/bin", "/usr/bin", "/bin"}, ":")
			},
		},
		{
			name:        "thirty-six bun entries collapse to one",
			withHomeBin: true,
			withBrew:    true,
			path: func(fx snippetFixture) string {
				return strings.Join([]string{bunCopies, "/home/u/.local/bin", fx.hbin, fx.bbin, fx.bsbin, "/usr/bin"}, ":")
			},
			want: func(fx snippetFixture) string {
				return strings.Join([]string{fx.hbin, fx.bbin, fx.bsbin, bun, "/home/u/.local/bin", "/usr/bin"}, ":")
			},
		},
		{
			name:        "missing home bin is not added and stays in place",
			withHomeBin: false,
			withBrew:    true,
			path: func(fx snippetFixture) string {
				return strings.Join([]string{"/usr/bin", fx.hbin, "/bin"}, ":")
			},
			want: func(fx snippetFixture) string {
				return strings.Join([]string{fx.bbin, fx.bsbin, "/usr/bin", fx.hbin, "/bin"}, ":")
			},
		},
		{
			name:        "no brew dir leaves brew entries in place",
			withHomeBin: true,
			withBrew:    false,
			path: func(fx snippetFixture) string {
				return strings.Join([]string{"/usr/bin", fx.bsbin, "/usr/bin", "/bin"}, ":")
			},
			want: func(fx snippetFixture) string {
				return strings.Join([]string{fx.hbin, "/usr/bin", fx.bsbin, "/bin"}, ":")
			},
		},
		{
			name:        "glob characters in an entry match literally",
			withHomeBin: false,
			withBrew:    false,
			path:        func(_ snippetFixture) string { return "/a*:/a:/[a]:/a*:/a" },
			want:        func(_ snippetFixture) string { return "/a*:/a:/[a]" },
		},
	}

	for _, tc := range cases {
		for shellName, shell := range snippetShells(t) {
			t.Run(tc.name+"/"+shellName, func(t *testing.T) {
				t.Parallel()

				fx := newSnippetFixture(t, tc.withHomeBin, tc.withBrew)
				env := []string{"HOME=" + fx.home, "PATH=" + tc.path(fx)}

				got, vars := sourceSnippet(t, shell, pathOrderSnippet(fx.brew), env)
				if want := tc.want(fx); got != want {
					t.Fatalf("PATH:\n got %s\nwant %s", got, want)
				}

				for _, line := range strings.Split(vars, "\n") {
					if strings.HasPrefix(line, "_ocfp") {
						t.Errorf("snippet left a temporary variable set: %s", line)
					}
				}

				again, _ := sourceSnippet(t, shell, pathOrderSnippet(fx.brew), []string{"HOME=" + fx.home, "PATH=" + got})
				if again != got {
					t.Fatalf("second pass changed PATH:\n got %s\nwant %s", again, got)
				}
			})
		}
	}
}

func TestPathOrderSnippet_UnsetHome(t *testing.T) {
	t.Parallel()

	for shellName, shell := range snippetShells(t) {
		t.Run(shellName, func(t *testing.T) {
			t.Parallel()

			fx := newSnippetFixture(t, false, true)

			got, _ := sourceSnippet(t, shell, pathOrderSnippet(fx.brew), []string{"PATH=/usr/bin:/bin"})
			if want := fx.bbin + ":" + fx.bsbin + ":/usr/bin:/bin"; got != want {
				t.Fatalf("PATH:\n got %s\nwant %s", got, want)
			}
		})
	}
}

func TestPathOrderSnippet_EmptyPathGetsDefault(t *testing.T) {
	t.Parallel()

	for shellName, shell := range snippetShells(t) {
		for name, withHome := range map[string]bool{"no HOME": false, "HOME without bin": true} {
			t.Run(name+"/"+shellName, func(t *testing.T) {
				t.Parallel()

				fx := newSnippetFixture(t, false, false)
				env := []string{"PATH=::"}

				if withHome {
					env = append(env, "HOME="+fx.home)
				}

				got, vars := sourceSnippet(t, shell, pathOrderSnippet(fx.brew), env)
				if got != fallbackShellPath {
					t.Fatalf("PATH:\n got %q\nwant %q", got, fallbackShellPath)
				}

				for _, line := range strings.Split(vars, "\n") {
					if strings.HasPrefix(line, "_ocfp") {
						t.Errorf("snippet left a temporary variable set: %s", line)
					}
				}
			})
		}
	}
}

func TestPathOrderSnippet_UsesLinuxbrewPrefix(t *testing.T) {
	t.Parallel()

	block := pathOrderBlock()
	if !strings.HasPrefix(block, pathBlockBegin+"\n") || !strings.HasSuffix(block, pathBlockEnd+"\n") {
		t.Fatalf("block is not wrapped in its markers:\n%s", block)
	}

	if !strings.Contains(block, linuxbrewPrefix+"/bin") || !strings.Contains(block, linuxbrewPrefix+"/sbin") {
		t.Fatalf("block does not use the linuxbrew prefix:\n%s", block)
	}
}

const skelBashrc = `# ~/.bashrc: executed by bash(1) for non-login shells.

# If not running interactively, don't do anything
case $- in
    *i*) ;;
      *) return;;
esac

HISTCONTROL=ignoreboth
alias ls='ls --color=auto'
`

const skelProfile = `# ~/.profile: executed by the command interpreter for login shells.
if [ -n "$BASH_VERSION" ]; then
    if [ -f "$HOME/.bashrc" ]; then
	. "$HOME/.bashrc"
    fi
fi

if [ -d "$HOME/bin" ] ; then
    PATH="$HOME/bin:$PATH"
fi

if [ -d "$HOME/.local/bin" ] ; then
    PATH="$HOME/.local/bin:$PATH"
fi
`

// shellEnvironmentCommand builds the generated shell environment phase the
// way the bastion runs it, under bash with set -euo pipefail, against home,
// with tmp as TMPDIR and lockWait as the rc lock timeout in seconds.
func shellEnvironmentCommand(t *testing.T, home, tmp string, lockWait int) *exec.Cmd {
	t.Helper()

	em := NewEnvironmentManager("aws", &config.Config{Name: "test-bloc"})
	script := "set -euo pipefail\n" +
		"log_info() { echo \"INFO $1\"; }\n" +
		"log_success() { echo \"OK $1\"; }\n" +
		"log_warning() { echo \"WARN $1\"; }\n" +
		"log_error() { echo \"ERR $1\"; }\n" +
		em.shellEnvironmentScript(lockWait)

	cmd := exec.Command(bashShell(t), "-c", script) // #nosec G204 -- test fixture
	cmd.Env = []string{"HOME=" + home, "TMPDIR=" + tmp, "PATH=" + os.Getenv("PATH")}

	return cmd
}

// runShellEnvironmentScript runs the generated shell environment phase
// against home and checks that it left no temporary files behind, either in
// TMPDIR or next to the rc files in home.
func runShellEnvironmentScript(t *testing.T, home string) string {
	t.Helper()

	tmp := t.TempDir()

	out, err := shellEnvironmentCommand(t, home, tmp, rcLockWaitSeconds).CombinedOutput()
	if err != nil {
		t.Fatalf("shell environment script failed: %v\n%s", err, out)
	}

	assertNoTempFiles(t, tmp, home)

	return string(out)
}

// assertNoTempFiles fails when tmp holds any entry or when any of dirs holds
// a staged rc replacement.
func assertNoTempFiles(t *testing.T, tmp string, dirs ...string) {
	t.Helper()

	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		t.Errorf("temporary file left in TMPDIR: %s", entry.Name())
	}

	for _, dir := range dirs {
		staged, err := filepath.Glob(filepath.Join(dir, ".ocfp-rc.*"))
		if err != nil {
			t.Fatal(err)
		}

		for _, name := range staged {
			t.Errorf("staged rc file left behind: %s", name)
		}
	}
}

// warningsAbout returns the warning lines in out that mention name.
func warningsAbout(out, name string) []string {
	var found []string

	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "WARN ") && strings.Contains(line, name) {
			found = append(found, line)
		}
	}

	return found
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func writePathFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertSingleTrailingBlock(t *testing.T, path string) string {
	t.Helper()

	content := readFile(t, path)
	if !strings.HasSuffix(content, "\n"+pathOrderBlock()) && content != pathOrderBlock() {
		t.Fatalf("%s does not end with the PATH block:\n%s", path, content)
	}

	if n := strings.Count(content, pathBlockBegin); n != 1 {
		t.Fatalf("%s holds %d begin markers, want 1:\n%s", path, n, content)
	}

	if n := strings.Count(content, pathBlockEnd); n != 1 {
		t.Fatalf("%s holds %d end markers, want 1:\n%s", path, n, content)
	}

	return content
}

func TestShellEnvironment_ConvergesRcFiles(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	bashrc := filepath.Join(home, ".bashrc")
	profile := filepath.Join(home, ".profile")

	writePathFile(t, bashrc, skelBashrc, 0o600)
	writePathFile(t, profile, skelProfile, 0o640)

	runShellEnvironmentScript(t, home)

	first := assertSingleTrailingBlock(t, bashrc)
	if !strings.HasPrefix(first, skelBashrc) {
		t.Fatalf("skel content was not kept at the top of .bashrc:\n%s", first)
	}

	if ocfp := strings.Index(first, "# OCFP Environment Configuration"); ocfp < 0 || ocfp > strings.Index(first, pathBlockBegin) {
		t.Fatalf("OCFP configuration block must precede the PATH block:\n%s", first)
	}

	if got := readFile(t, profile); got != skelProfile+pathOrderBlock() {
		t.Fatalf(".profile:\n%s", got)
	}

	for _, name := range []string{".bash_profile", ".bash_login"} {
		if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Errorf("%s was created, want it left absent (err=%v)", name, err)
		}
	}

	for path, mode := range map[string]os.FileMode{bashrc: 0o600, profile: 0o640} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}

		if info.Mode().Perm() != mode {
			t.Errorf("%s mode %v, want %v", path, info.Mode().Perm(), mode)
		}
	}

	// A second run must leave both files byte-identical and unwritten.
	old := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, path := range []string{bashrc, profile} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	runShellEnvironmentScript(t, home)

	if got := readFile(t, bashrc); got != first {
		t.Fatalf("second run changed .bashrc:\n%s", got)
	}

	if got := readFile(t, profile); got != skelProfile+pathOrderBlock() {
		t.Fatalf("second run changed .profile:\n%s", got)
	}

	for _, path := range []string{bashrc, profile} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}

		if !info.ModTime().Equal(old) {
			t.Errorf("%s was rewritten on the second run (mtime %v)", path, info.ModTime())
		}
	}
}

func TestShellEnvironment_CreatesMissingBashrc(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	runShellEnvironmentScript(t, home)
	assertSingleTrailingBlock(t, filepath.Join(home, ".bashrc"))
}

func TestShellEnvironment_MovesStaleBlocksToTheEnd(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	bashProfile := filepath.Join(home, ".bash_profile")
	stale := pathBlockBegin + "\nPATH=/old\n" + pathBlockEnd + "\n"
	bun := "export PATH=\"$HOME/.bun/bin:$PATH\"\n"
	original := "# top\n" + stale + bun + stale + bun + "# bottom\n"

	writePathFile(t, bashProfile, original, 0o644)
	runShellEnvironmentScript(t, home)

	got := assertSingleTrailingBlock(t, bashProfile)
	if want := "# top\n" + bun + bun + "# bottom\n" + pathOrderBlock(); got != want {
		t.Fatalf(".bash_profile:\n got %q\nwant %q", got, want)
	}
}

func TestShellEnvironment_AppendsAfterMissingTrailingNewline(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	bashLogin := filepath.Join(home, ".bash_login")
	writePathFile(t, bashLogin, "echo hi", 0o644)

	runShellEnvironmentScript(t, home)

	if got := readFile(t, bashLogin); got != "echo hi\n"+pathOrderBlock() {
		t.Fatalf(".bash_login: %q", got)
	}
}

func TestShellEnvironment_SkipsFileWithUnterminatedBlock(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"begin without end": skelProfile + pathBlockBegin + "\nexport KEEP=1\n",
		"end without begin": skelProfile + pathBlockEnd + "\nexport KEEP=1\n",
		"nested begin":      skelProfile + pathBlockBegin + "\n" + pathBlockBegin + "\n" + pathBlockEnd + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			profile := filepath.Join(home, ".profile")
			writePathFile(t, profile, content, 0o644)

			out := runShellEnvironmentScript(t, home)

			if got := readFile(t, profile); got != content {
				t.Fatalf(".profile was changed:\n%s", got)
			}

			if !strings.Contains(out, "WARN") || !strings.Contains(out, ".profile") {
				t.Fatalf("expected a warning naming .profile, got:\n%s", out)
			}

			assertSingleTrailingBlock(t, filepath.Join(home, ".bashrc"))
		})
	}
}

func TestShellEnvironment_SkipsMalformedBashrcEntirely(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	bashrc := filepath.Join(home, ".bashrc")
	profile := filepath.Join(home, ".profile")
	content := skelBashrc + pathBlockBegin + "\nexport KEEP=1\n"

	writePathFile(t, bashrc, content, 0o644)
	writePathFile(t, profile, skelProfile, 0o644)

	out := runShellEnvironmentScript(t, home)

	if got := readFile(t, bashrc); got != content {
		t.Fatalf(".bashrc was changed:\n%s", got)
	}

	if len(warningsAbout(out, ".bashrc")) == 0 {
		t.Fatalf("expected a warning naming .bashrc, got:\n%s", out)
	}

	if strings.Contains(out, "Shell environment configured") {
		t.Fatalf("one-time configuration reported as applied to a skipped .bashrc:\n%s", out)
	}

	if got := readFile(t, profile); got != skelProfile+pathOrderBlock() {
		t.Fatalf(".profile was not converged:\n%s", got)
	}
}

func TestShellEnvironment_ReplacesRcFilesAtomically(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	bashrc := filepath.Join(home, ".bashrc")
	profile := filepath.Join(home, ".profile")

	writePathFile(t, bashrc, skelBashrc, 0o600)
	writePathFile(t, profile, skelProfile, 0o640)

	before := map[string]os.FileInfo{}

	for _, path := range []string{bashrc, profile} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}

		before[path] = info
	}

	runShellEnvironmentScript(t, home)

	after := map[string]os.FileInfo{}

	for path, mode := range map[string]os.FileMode{bashrc: 0o600, profile: 0o640} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}

		if os.SameFile(before[path], info) {
			t.Errorf("%s was rewritten in place, want it replaced by rename", path)
		}

		if info.Mode().Perm() != mode {
			t.Errorf("%s mode %v, want %v", path, info.Mode().Perm(), mode)
		}

		after[path] = info
	}

	runShellEnvironmentScript(t, home)

	for _, path := range []string{bashrc, profile} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}

		if !os.SameFile(after[path], info) {
			t.Errorf("%s was replaced on a run that had nothing to change", path)
		}
	}
}

func TestShellEnvironment_UpdatesSymlinkTarget(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dotfiles := filepath.Join(home, "dotfiles")

	if err := os.MkdirAll(dotfiles, 0o755); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(dotfiles, "profile")
	link := filepath.Join(home, ".profile")
	writePathFile(t, target, skelProfile, 0o640)

	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	runShellEnvironmentScript(t, home)
	assertNoTempFiles(t, t.TempDir(), dotfiles)

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".profile is no longer a symlink (mode %v)", info.Mode())
	}

	if dest, err := os.Readlink(link); err != nil || dest != target {
		t.Fatalf(".profile points at %q (err=%v), want %q", dest, err, target)
	}

	if got := readFile(t, target); got != skelProfile+pathOrderBlock() {
		t.Fatalf("symlink target was not converged:\n%s", got)
	}

	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("symlink target mode changed (info=%v, err=%v)", info, err)
	}
}

// profileDContent extracts the /etc/profile.d/ocfp.sh body from the system
// environment script.
func profileDContent(t *testing.T, script string) string {
	t.Helper()

	_, rest, found := strings.Cut(script, "<< 'PROFILE_EOF'\n")
	if !found {
		t.Fatalf("no profile.d heredoc in script:\n%s", script)
	}

	body, _, found := strings.Cut(rest, "\nPROFILE_EOF\n")
	if !found {
		t.Fatalf("unterminated profile.d heredoc:\n%s", rest)
	}

	return body + "\n"
}

func TestProfileD_AppliesPathOrderAfterBrewShellenv(t *testing.T) {
	t.Parallel()

	em := NewEnvironmentManager("aws", &config.Config{Name: "test-bloc"})
	body := profileDContent(t, em.GenerateSystemEnvironmentScript(context.Background()))

	shellenv := strings.Index(body, "brew shellenv")
	snippet := strings.Index(body, pathOrderSnippet(linuxbrewPrefix))

	if shellenv < 0 || snippet < 0 || snippet < shellenv {
		t.Fatalf("PATH snippet must follow the brew shellenv eval:\n%s", body)
	}

	home := t.TempDir()
	hbin := filepath.Join(home, "bin")

	if err := os.MkdirAll(hbin, 0o755); err != nil {
		t.Fatal(err)
	}

	got, _ := sourceSnippet(t, posixShell(t), body, []string{"HOME=" + home, "PATH=/usr/bin:/bin:/usr/bin:" + hbin})
	if !strings.HasPrefix(got, hbin+":") || strings.Count(got, "/usr/bin") != 1 {
		t.Fatalf("profile.d PATH not reordered under sh: %s", got)
	}
}

// seamScript returns the shell environment phase with commands in place of
// the seam that runs right before an rc file is replaced.
func seamScript(t *testing.T, commands string) string {
	t.Helper()

	em := NewEnvironmentManager("aws", &config.Config{Name: "test-bloc"})
	script := em.shellEnvironmentScript(rcLockWaitSeconds)

	if !strings.Contains(script, rcSeamBeforeReplace) {
		t.Fatalf("script has no %q seam", rcSeamBeforeReplace)
	}

	return strings.Replace(script, rcSeamBeforeReplace, commands, 1)
}

func runSeamScript(t *testing.T, home, script string) string {
	t.Helper()

	tmp := t.TempDir()
	full := "set -euo pipefail\n" +
		"log_info() { echo \"INFO $1\"; }\n" +
		"log_success() { echo \"OK $1\"; }\n" +
		"log_warning() { echo \"WARN $1\"; }\n" +
		"log_error() { echo \"ERR $1\"; }\n" + script

	cmd := exec.Command(bashShell(t), "-c", full) // #nosec G204 -- test fixture
	cmd.Env = []string{"HOME=" + home, "TMPDIR=" + tmp, "PATH=" + os.Getenv("PATH")}

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}

	assertNoTempFiles(t, tmp, home)

	return string(out)
}

// A line an installer appends between the read and the replace must survive,
// and the block must still end up last.
func TestShellEnvironment_KeepsAppendMadeBeforeReplace(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	bashrc := filepath.Join(home, ".bashrc")
	writePathFile(t, bashrc, skelBashrc, 0o600)

	hook := `if [ ! -e "$HOME/.seam-done" ]; then
            printf '%s\n' 'export INSTALLER_LINE=late' >> "$HOME/.bashrc"
            : > "$HOME/.seam-done"
        fi`

	out := runSeamScript(t, home, seamScript(t, hook))

	content := assertSingleTrailingBlock(t, bashrc)
	if !strings.Contains(content, "export INSTALLER_LINE=late") {
		t.Fatalf("the appended line was lost:\n%s\n%s", content, out)
	}

	if got := warningsAbout(out, ".bashrc"); len(got) != 0 {
		t.Fatalf("unexpected warnings: %v", got)
	}
}

// A file that keeps changing is skipped with a warning after three tries,
// and nothing that was appended is lost.
func TestShellEnvironment_GivesUpWhenRcKeepsChanging(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	bashrc := filepath.Join(home, ".bashrc")
	writePathFile(t, bashrc, skelBashrc, 0o600)

	hook := `echo "export CHURN_$RANDOM=1" >> "$HOME/.bashrc"; echo attempt >> "$HOME/.attempts"`

	out := runSeamScript(t, home, seamScript(t, hook))

	if n := strings.Count(readFile(t, filepath.Join(home, ".attempts")), "attempt"); n != 3 {
		t.Fatalf("made %d attempts, want 3\n%s", n, out)
	}

	if got := warningsAbout(out, ".bashrc"); len(got) != 1 {
		t.Fatalf("want one warning about .bashrc, got %v\n%s", got, out)
	}

	content := readFile(t, bashrc)
	if strings.Count(content, "CHURN_") != 3 || !strings.HasPrefix(content, skelBashrc) {
		t.Fatalf("appended lines were lost:\n%s", content)
	}
}
