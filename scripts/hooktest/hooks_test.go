// Package hooktest runs each git hook of the repository inside a throwaway
// repository and checks what it accepts and refuses.
package hooktest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The terms below are made up for these tests. The tests never read the real
// term list, and each one builds its own under a temporary directory.
const fakeTerms = "quillmark\n"

var (
	buildOnce sync.Once
	binDir    string
	buildErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if binDir != "" {
		_ = os.RemoveAll(binDir)
	}
	os.Exit(code)
}

// namecheckBin builds the name check once and returns the path to the binary.
// The hooks run it through OCFP_NAMECHECK, because a throwaway repository
// holds no source to run.
func namecheckBin(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "hooktest-bin")
		if err != nil {
			buildErr = err
			return
		}
		binDir = dir
		out, err := exec.Command("go", "build", "-o", filepath.Join(dir, "namecheck"), "../namecheck").CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("build namecheck: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return filepath.Join(binDir, "namecheck")
}

func hookPath(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", ".githooks", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// env is one test's private world.
type env struct {
	repo  string
	vars  []string
	hooks string
	// makeLog records each call of the fake make.
	makeLog string
}

// newEnv makes a repository with one commit on main, a term list holding
// fakeTerms, and a child environment that points the hooks at both.
func newEnv(t *testing.T) *env {
	t.Helper()
	cfg := t.TempDir()
	dir := filepath.Join(cfg, "fivetwenty")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	terms := filepath.Join(dir, "client-terms")
	if err := os.WriteFile(terms, []byte(fakeTerms), 0o600); err != nil {
		t.Fatal(err)
	}
	e := &env{repo: t.TempDir()}
	// A fake make comes first on the path and records its arguments, so a
	// push to main or a tag never starts the real preflight.
	makeDir := t.TempDir()
	e.makeLog = filepath.Join(makeDir, "make.log")
	fake(t, makeDir, "make", e.makeLog, 0)
	e.vars = []string{
		"PATH=" + makeDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + cfg,
		"XDG_CONFIG_HOME=" + cfg,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"OCFP_NAMECHECK=" + namecheckBin(t),
	}
	e.git(t, "init", "-q", "-b", "main")
	e.write(t, "a.txt", "clean\n")
	e.git(t, "add", "a.txt")
	e.git(t, "commit", "-q", "-m", "First")
	return e
}

func (e *env) write(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.repo, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *env) git(t *testing.T, args ...string) string {
	t.Helper()
	code, out := e.run(nil, "git", args...)
	if code != 0 {
		t.Fatalf("git %v exited %d\n%s", args, code, out)
	}
	return strings.TrimSpace(out)
}

// run runs a command in the repository and returns its exit code and its
// combined output.
func (e *env) run(stdin *strings.Reader, name string, args ...string) (int, string) {
	return e.runIn(e.repo, stdin, name, args...)
}

// runIn is run from another directory.
func (e *env) runIn(dir string, stdin *strings.Reader, name string, args ...string) (int, string) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = e.vars
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, buf.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), buf.String()
	default:
		return -1, err.Error()
	}
}

// commitMsg runs the commit-msg hook over a message and returns its exit code
// and output.
func commitMsg(t *testing.T, msg string) (int, string) {
	t.Helper()
	e := newEnv(t)
	f := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
	if err := os.WriteFile(f, []byte(msg), 0o644); err != nil {
		t.Fatal(err)
	}
	return e.run(nil, "sh", hookPath(t, "commit-msg"), f)
}

func TestCommitMsgAcceptsGoodMessage(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("a", 72)
	code, out := commitMsg(t, strings.Repeat("s", 48)+"\n\n"+body+"\n# a comment line that is very long "+strings.Repeat("x", 80)+"\n")
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
}

func TestCommitMsgRefusesLongSubject(t *testing.T) {
	t.Parallel()
	code, out := commitMsg(t, strings.Repeat("s", 49)+"\n")
	if code == 0 {
		t.Fatalf("a 49-character subject was accepted\n%s", out)
	}
}

func TestCommitMsgRefusesTypePrefix(t *testing.T) {
	t.Parallel()
	for _, msg := range []string{"fix: x\n", "feat(api): add x\n", "chore!: drop x\n"} {
		msg := msg
		t.Run(strings.TrimSpace(msg), func(t *testing.T) {
			t.Parallel()
			code, out := commitMsg(t, msg)
			if code == 0 {
				t.Fatalf("%q was accepted\n%s", msg, out)
			}
		})
	}
	t.Run("a colon later in the subject is fine", func(t *testing.T) {
		t.Parallel()
		code, out := commitMsg(t, "Handle the case of a: b\n")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
}

func TestCommitMsgRefusesWideBody(t *testing.T) {
	t.Parallel()
	code, out := commitMsg(t, "Subject\n\n"+strings.Repeat("b", 73)+"\n")
	if code == 0 {
		t.Fatalf("a 73-column body line was accepted\n%s", out)
	}
}

func TestCommitMsgRefusesProcessWords(t *testing.T) {
	t.Parallel()
	for _, w := range []string{"wave", "Lane", "phase", "task", "waves", "P9z-T99", "P8y-T88"} {
		w := w
		t.Run(w, func(t *testing.T) {
			t.Parallel()
			code, out := commitMsg(t, "Add the thing\n\nThis comes from "+w+" notes.\n")
			if code == 0 {
				t.Fatalf("a message holding %q was accepted\n%s", w, out)
			}
		})
	}
	t.Run("words that only contain one are fine", func(t *testing.T) {
		t.Parallel()
		code, out := commitMsg(t, "Add the airlane and the phaser\n")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
}

func TestCommitMsgCrlfAndTypeVariants(t *testing.T) {
	t.Parallel()
	t.Run("crlf message at the limits is fine", func(t *testing.T) {
		t.Parallel()
		code, out := commitMsg(t, strings.Repeat("s", 48)+"\r\n\r\n"+strings.Repeat("b", 72)+"\r\n")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
	t.Run("crlf message with a type prefix is refused", func(t *testing.T) {
		t.Parallel()
		if code, out := commitMsg(t, "fix: x\r\n"); code == 0 {
			t.Fatalf("accepted\n%s", out)
		}
	})
	for _, msg := range []string{"fix:x\n", "build-fix: x\n"} {
		msg := msg
		t.Run(strings.TrimSpace(msg), func(t *testing.T) {
			t.Parallel()
			if code, out := commitMsg(t, msg); code == 0 {
				t.Fatalf("%q was accepted\n%s", msg, out)
			}
		})
	}
}

func TestCommitMsgIgnoresVerboseDiff(t *testing.T) {
	t.Parallel()
	diff := "# ------------------------ >8 ------------------------\n" +
		"# Do not modify or remove the line above.\n" +
		"diff --git a/f b/f\n+" + strings.Repeat("w", 90) + " task P9z-T99 quillmark\n"
	t.Run("clean message", func(t *testing.T) {
		t.Parallel()
		code, out := commitMsg(t, "Add a thing\n\nIt does one job.\n"+diff)
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
	t.Run("a term above the line is still found", func(t *testing.T) {
		t.Parallel()
		if code, out := commitMsg(t, "Mention quillmark\n"+diff); code == 0 {
			t.Fatalf("accepted\n%s", out)
		}
	})
}

func TestCommitMsgRunsNameCheck(t *testing.T) {
	t.Parallel()
	code, out := commitMsg(t, "Mention quillmark here\n")
	if code == 0 {
		t.Fatalf("a message holding a term was accepted\n%s", out)
	}
	if strings.Contains(out, "quillmark") {
		t.Fatalf("the hook printed the term\n%s", out)
	}
}

func TestPreCommitRunsNameCheck(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.git(t, "config", "core.hooksPath", filepath.Dir(hookPath(t, "pre-commit")))

	e.write(t, "ok.txt", "nothing to see\n")
	e.git(t, "add", "ok.txt")
	if code, out := e.run(nil, "git", "commit", "-q", "-m", "Add a clean file"); code != 0 {
		t.Fatalf("a clean commit was refused (exit %d)\n%s", code, out)
	}

	e.write(t, "bad.txt", "this line holds quillmark\n")
	e.git(t, "add", "bad.txt")
	code, out := e.run(nil, "git", "commit", "-q", "-m", "Add a second file")
	if code == 0 {
		t.Fatalf("a staged term was accepted\n%s", out)
	}
	if strings.Contains(out, "quillmark") {
		t.Fatalf("the hook printed the term\n%s", out)
	}
}

// fake writes an executable that appends its name and arguments to log.
func fake(t *testing.T, dir, name, log string, exit int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	body := fmt.Sprintf("#!/bin/sh\necho \"%s $*\" >> '%s'\nexit %d\n", name, log, exit)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

const zeros = "0000000000000000000000000000000000000000"

// pushEnv makes a branch dev at the first commit and a branch work with one
// more commit holding body, and returns the stdin git would feed pre-push.
func pushEnv(t *testing.T, body string) (*env, *strings.Reader) {
	t.Helper()
	e := newEnv(t)
	e.git(t, "branch", "dev")
	e.git(t, "checkout", "-q", "-b", "work")
	e.write(t, "b.txt", body)
	e.git(t, "add", "b.txt")
	e.git(t, "commit", "-q", "-m", "Add b")
	sha := e.git(t, "rev-parse", "HEAD")
	return e, strings.NewReader("refs/heads/work " + sha + " refs/heads/work " + zeros + "\n")
}

func TestPrePushRefusesTermInPushedRange(t *testing.T) {
	t.Parallel()
	e, stdin := pushEnv(t, "a line with quillmark\n")
	code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url")
	if code == 0 {
		t.Fatalf("a pushed term was accepted\n%s", out)
	}
	if strings.Contains(out, "quillmark") {
		t.Fatalf("the hook printed the term\n%s", out)
	}
}

// logTools points the name check at a recording fake and returns the log.
func logTools(t *testing.T, e *env) string {
	t.Helper()
	tools := t.TempDir()
	log := filepath.Join(tools, "log")
	e.vars = append(e.vars, "OCFP_NAMECHECK="+fake(t, tools, "namecheck", log, 0))
	return log
}

func readLog(t *testing.T, log string) []string {
	t.Helper()
	got, err := os.ReadFile(log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(got)), "\n") {
		lines = append(lines, strings.TrimSpace(l))
	}
	return lines
}

func wantCalls(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestPrePushRunsNameCheckOverNamesAndRange(t *testing.T) {
	t.Parallel()
	e, _ := pushEnv(t, "clean\n")
	log := logTools(t, e)
	first := e.git(t, "rev-parse", "main")
	sha := e.git(t, "rev-parse", "HEAD")
	e.git(t, "update-ref", "refs/remotes/origin/main", first)
	stdin := strings.NewReader("refs/heads/work " + sha + " refs/heads/work " + zeros + "\n")
	if code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	wantCalls(t, readLog(t, log), []string{
		"namecheck --names refs/heads/work refs/heads/work",
		"namecheck --range " + sha + " ^refs/remotes/origin/main",
	})
}

func TestPrePushScansBranchContainedInDirtyDev(t *testing.T) {
	t.Parallel()
	for _, tracking := range []bool{true, false} {
		tracking := tracking
		name := "empty remote"
		if tracking {
			name = "remote with tracking refs"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			first := e.git(t, "rev-parse", "HEAD")
			e.git(t, "checkout", "-q", "-b", "dev")
			e.write(t, "b.txt", "a line with quillmark\n")
			e.git(t, "add", "b.txt")
			e.git(t, "commit", "-q", "-m", "Add b")
			sha := e.git(t, "rev-parse", "HEAD")
			if tracking {
				e.git(t, "update-ref", "refs/remotes/origin/main", first)
			}
			stdin := strings.NewReader("refs/heads/dev " + sha + " refs/heads/dev " + zeros + "\n")
			code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url")
			if code == 0 {
				t.Fatalf("a term inside dev was accepted\n%s", out)
			}
			if strings.Contains(out, "quillmark") {
				t.Fatalf("the hook printed the term\n%s", out)
			}
		})
	}
}

func TestPrePushCleanFirstPushToEmptyRemote(t *testing.T) {
	t.Parallel()
	e, stdin := pushEnv(t, "clean\n")
	log := logTools(t, e)
	sha := e.git(t, "rev-parse", "HEAD")
	if code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	calls := readLog(t, log)
	if len(calls) < 2 || calls[1] != "namecheck --range "+sha {
		t.Fatalf("got calls %q, want the whole history of %s", calls, sha)
	}
}

func TestPrePushExistingRemoteRef(t *testing.T) {
	t.Parallel()
	e, _ := pushEnv(t, "clean\n")
	log := logTools(t, e)
	first := e.git(t, "rev-parse", "main")
	sha := e.git(t, "rev-parse", "HEAD")
	stdin := strings.NewReader("refs/heads/work " + sha + " refs/heads/work " + first + "\n")
	if code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if calls := readLog(t, log); len(calls) < 2 || calls[1] != "namecheck --range "+first+".."+sha {
		t.Fatalf("got calls %q", calls)
	}
}

func TestPrePushRemoteAheadOfClone(t *testing.T) {
	t.Parallel()
	e, _ := pushEnv(t, "a line with quillmark\n")
	sha := e.git(t, "rev-parse", "HEAD")
	unknown := "1111111111111111111111111111111111111111"
	stdin := strings.NewReader("refs/heads/work " + sha + " refs/heads/work " + unknown + "\n")
	code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url")
	if code == 0 {
		t.Fatalf("a pushed term was accepted\n%s", out)
	}
	if !strings.Contains(out, "fetch") {
		t.Fatalf("the hook did not say to fetch\n%s", out)
	}
}

func TestPrePushDeletionNeedsNothing(t *testing.T) {
	t.Parallel()
	e := newEnv(t) // no dev branch
	log := logTools(t, e)
	stdin := strings.NewReader("(delete) " + zeros + " refs/heads/old 2222222222222222222222222222222222222222\n")
	if code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if calls := readLog(t, log); len(calls) != 0 {
		t.Fatalf("a deletion ran %q", calls)
	}
}

func TestPrePushScansEveryRef(t *testing.T) {
	t.Parallel()
	e, _ := pushEnv(t, "clean\n")
	first := e.git(t, "rev-parse", "main")
	clean := e.git(t, "rev-parse", "HEAD")
	e.git(t, "checkout", "-q", "-b", "other", first)
	e.write(t, "c.txt", "a line with quillmark\n")
	e.git(t, "add", "c.txt")
	e.git(t, "commit", "-q", "-m", "Add c")
	dirty := e.git(t, "rev-parse", "HEAD")
	stdin := strings.NewReader(
		"refs/heads/work " + clean + " refs/heads/work " + zeros + "\n" +
			"refs/heads/other " + dirty + " refs/heads/other " + zeros + "\n")
	if code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url"); code == 0 {
		t.Fatalf("the second ref's term was accepted\n%s", out)
	}

	log := logTools(t, e)
	stdin = strings.NewReader(
		"refs/heads/work " + clean + " refs/heads/work " + zeros + "\n" +
			"refs/heads/other " + dirty + " refs/heads/other " + zeros + "\n")
	if code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	var ranges int
	for _, c := range readLog(t, log) {
		if strings.HasPrefix(c, "namecheck --range ") {
			ranges++
		}
	}
	if ranges != 2 {
		t.Fatalf("got %d range checks, want 2", ranges)
	}
}

func TestPrePushScansBranchNames(t *testing.T) {
	t.Parallel()
	e, _ := pushEnv(t, "clean\n")
	sha := e.git(t, "rev-parse", "HEAD")
	stdin := strings.NewReader("refs/heads/work " + sha + " refs/heads/quillmark-fix " + zeros + "\n")
	code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url")
	if code == 0 {
		t.Fatalf("a branch name holding a term was accepted\n%s", out)
	}
	if strings.Contains(out, "quillmark") {
		t.Fatalf("the hook printed the term\n%s", out)
	}
}

func TestPrePushFailsClosedWithoutChecker(t *testing.T) {
	t.Parallel()
	t.Run("missing program", func(t *testing.T) {
		t.Parallel()
		e, stdin := pushEnv(t, "clean\n")
		e.vars = append(e.vars, "OCFP_NAMECHECK="+filepath.Join(t.TempDir(), "absent"))
		if code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url"); code == 0 {
			t.Fatalf("a missing checker was accepted\n%s", out)
		}
	})
	t.Run("missing term list", func(t *testing.T) {
		t.Parallel()
		e, stdin := pushEnv(t, "clean\n")
		if err := os.Remove(filepath.Join(termsDir(e), "client-terms")); err != nil {
			t.Fatal(err)
		}
		code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url")
		if code == 0 || !strings.Contains(out, "CLIENT_TERMS_UNAVAILABLE") {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
}

// termsDir returns the directory that holds the test's term list.
func termsDir(e *env) string {
	for _, v := range e.vars {
		if d, ok := strings.CutPrefix(v, "XDG_CONFIG_HOME="); ok {
			return filepath.Join(d, "fivetwenty")
		}
	}
	return ""
}

func TestHooksAreExecutable(t *testing.T) {
	t.Parallel()
	for _, h := range []string{"pre-commit", "commit-msg", "pre-push"} {
		info, err := os.Stat(hookPath(t, h))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&0o111 == 0 {
			t.Errorf("%s is not executable", h)
		}
	}
}

func TestHooksNoticeOverrides(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.write(t, "ok.txt", "fine\n")
	e.git(t, "add", "ok.txt")
	code, out := e.run(nil, "sh", hookPath(t, "pre-commit"))
	if code != 0 || !strings.Contains(out, "OCFP_NAMECHECK") {
		t.Fatalf("exit %d, want a notice naming the override\n%s", code, out)
	}
}

func TestPreCommitFromSubdirectoryAndLinkedWorktree(t *testing.T) {
	t.Parallel()
	t.Run("sub-directory", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		if err := os.Mkdir(filepath.Join(e.repo, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		e.write(t, "sub/bad.txt", "this line holds quillmark\n")
		e.git(t, "add", "sub/bad.txt")
		if code, out := e.runIn(filepath.Join(e.repo, "sub"), nil, "sh", hookPath(t, "pre-commit")); code == 0 {
			t.Fatalf("a staged term was accepted\n%s", out)
		}
	})
	t.Run("linked worktree", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		wt := filepath.Join(t.TempDir(), "wt")
		e.git(t, "worktree", "add", "-q", "-b", "side", wt)
		if err := os.WriteFile(filepath.Join(wt, "bad.txt"), []byte("this line holds quillmark\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if code, out := e.runIn(wt, nil, "git", "add", "bad.txt"); code != 0 {
			t.Fatalf("add: %d\n%s", code, out)
		}
		if code, out := e.runIn(wt, nil, "sh", hookPath(t, "pre-commit")); code == 0 {
			t.Fatalf("a staged term was accepted\n%s", out)
		}
	})
}

func TestPrePushRefusesTermInAnnotatedTagMessage(t *testing.T) {
	t.Parallel()
	e, _ := pushEnv(t, "clean\n")
	e.git(t, "tag", "-a", "-m", "a line with quillmark", "v1")
	tag := e.git(t, "rev-parse", "refs/tags/v1")
	stdin := strings.NewReader("refs/tags/v1 " + tag + " refs/tags/v1 " + zeros + "\n")
	code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url")
	if code == 0 {
		t.Fatalf("a term in a tag message was accepted\n%s", out)
	}
	if strings.Contains(out, "quillmark") {
		t.Fatalf("the hook printed the term\n%s", out)
	}
}

func TestPrePushAcceptsCleanAnnotatedTag(t *testing.T) {
	t.Parallel()
	e, _ := pushEnv(t, "clean\n")
	e.git(t, "tag", "-a", "-m", "release", "v1")
	tag := e.git(t, "rev-parse", "refs/tags/v1")
	stdin := strings.NewReader("refs/tags/v1 " + tag + " refs/tags/v1 " + zeros + "\n")
	if code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestPrePushScansTagObjectsOnly(t *testing.T) {
	t.Parallel()
	e, _ := pushEnv(t, "clean\n")
	log := logTools(t, e)
	e.git(t, "tag", "light")
	e.git(t, "tag", "-a", "-m", "release", "heavy")
	light := e.git(t, "rev-parse", "refs/tags/light")
	heavy := e.git(t, "rev-parse", "refs/tags/heavy")
	stdin := strings.NewReader(
		"refs/tags/light " + light + " refs/tags/light " + zeros + "\n" +
			"refs/tags/heavy " + heavy + " refs/tags/heavy " + zeros + "\n")
	if code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	var tags []string
	for _, c := range readLog(t, log) {
		if strings.HasPrefix(c, "namecheck --tag ") {
			tags = append(tags, c)
		}
	}
	wantCalls(t, tags, []string{"namecheck --tag refs/tags/heavy " + heavy})
}

func TestPrePushScansTagObjectPushedByHash(t *testing.T) {
	t.Parallel()
	e, _ := pushEnv(t, "clean\n")
	e.git(t, "tag", "-a", "-m", "a line with quillmark", "v1")
	tag := e.git(t, "rev-parse", "refs/tags/v1")
	stdin := strings.NewReader(tag + " " + tag + " refs/tags/x " + zeros + "\n")
	code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url")
	if code == 0 {
		t.Fatalf("a term in a tag pushed by hash was accepted\n%s", out)
	}
	if strings.Contains(out, "quillmark") {
		t.Fatalf("the hook printed the term\n%s", out)
	}
}

func TestPrePushRefusesTagChainBeyondTheCap(t *testing.T) {
	t.Parallel()
	e, _ := pushEnv(t, "clean\n")
	e.git(t, "tag", "-a", "-m", "m", "t0")
	const depth = 40
	for i := 1; i <= depth; i++ {
		e.git(t, "tag", "-a", "-m", "m", fmt.Sprintf("t%d", i), fmt.Sprintf("refs/tags/t%d", i-1))
	}
	tag := e.git(t, "rev-parse", fmt.Sprintf("refs/tags/t%d", depth))
	stdin := strings.NewReader("refs/tags/deep " + tag + " refs/tags/deep " + zeros + "\n")
	if code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url"); code == 0 {
		t.Fatalf("a tag chain past the cap was accepted\n%s", out)
	}
}

func TestPrePushKeepsPreflightForMain(t *testing.T) {
	t.Parallel()
	push := func(t *testing.T, ref string) (*env, int, string) {
		t.Helper()
		e, _ := pushEnv(t, "clean\n")
		sha := e.git(t, "rev-parse", "HEAD")
		stdin := strings.NewReader("refs/heads/work " + sha + " " + ref + " " + zeros + "\n")
		code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url")
		return e, code, out
	}
	for _, ref := range []string{"refs/heads/main", "refs/tags/v1"} {
		ref := ref
		t.Run(ref, func(t *testing.T) {
			t.Parallel()
			e, code, out := push(t, ref)
			if code != 0 {
				t.Fatalf("exit %d\n%s", code, out)
			}
			wantCalls(t, readLog(t, e.makeLog), []string{"make preflight"})
		})
	}
	t.Run("a feature branch skips preflight", func(t *testing.T) {
		t.Parallel()
		e, code, out := push(t, "refs/heads/feature")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if calls := readLog(t, e.makeLog); len(calls) != 0 {
			t.Fatalf("a feature push ran %q", calls)
		}
	})
	t.Run("a term still refuses a push to main", func(t *testing.T) {
		t.Parallel()
		e, _ := pushEnv(t, "a line with quillmark\n")
		sha := e.git(t, "rev-parse", "HEAD")
		stdin := strings.NewReader("refs/heads/work " + sha + " refs/heads/main " + zeros + "\n")
		if code, out := e.run(stdin, "bash", hookPath(t, "pre-push"), "origin", "url"); code == 0 {
			t.Fatalf("a pushed term was accepted\n%s", out)
		}
		if calls := readLog(t, e.makeLog); len(calls) != 0 {
			t.Fatalf("preflight ran after the name check refused: %q", calls)
		}
	})
}
