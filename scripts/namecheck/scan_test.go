package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// setupConfig copies testdata/terms.txt into a fresh config tree and
// returns a getenv function that points XDG_CONFIG_HOME at it.
func setupConfig(t *testing.T, content []byte, mode os.FileMode) func(string) string {
	t.Helper()
	cfg := t.TempDir()
	dir := filepath.Join(cfg, "fivetwenty")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "client-terms")
	if err := os.WriteFile(p, content, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return cfg
		}
		return ""
	}
}

func testTerms(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/terms.txt")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func runScan(t *testing.T, getenv func(string) string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, getenv, &out, &errb)
	return code, out.String(), errb.String()
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScanFindsTermsAsWordsAndParts(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not meaningful on windows")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)

	cases := []struct {
		name, text string
		wantTerm   int // 0 means clean
		wantCol    int
	}{
		{"word whole", "the zebracorp lab", 1, 5},
		{"word inside longer word", "the zebracorpus lab", 0, 0},
		{"word prefix glued", "xzebracorp", 0, 0},
		{"part inside word", "myquaggaworksapp", 2, 3},
		{"ignores case", "The ZebraCorp lab", 1, 5},
		{"nfkc folds fullwidth", "the ｚｅｂｒａｃｏｒｐ lab", 1, 5},
		{"dots between single letters", "the z.e.b.r.a.c.o.r.p lab", 1, 5},
		{"hyphens between single letters", "the z-e-b-r-a-c-o-r-p lab", 1, 5},
		{"underscores between single letters", "the z_e_b_r_a_c_o_r_p lab", 1, 5},
		{"spaces between single letters", "the z e b r a c o r p lab", 1, 5},
		{"dot inside a real word is kept", "zebra.corp", 0, 0},
		{"decomposed letter and mark match precomposed term", "the cafe\u0301zebra lab", 3, 5},
		{"clean line", "nothing to see", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p := writeFile(t, "f.txt", "first line\n"+c.text+"\n")
			code, out, errs := runScan(t, getenv, p)
			if c.wantTerm == 0 {
				if code != 0 || out != "" {
					t.Fatalf("code %d out %q err %q, want clean", code, out, errs)
				}
				return
			}
			if code != 1 {
				t.Fatalf("code %d err %q, want 1", code, errs)
			}
			want := "CLIENT_NAME_FOUND " + p + ":2:" + itoa(c.wantCol) + " term " + itoa(c.wantTerm) + "\n"
			if out != want {
				t.Fatalf("out %q, want %q", out, want)
			}
			for _, leak := range []string{"zebracorp", "quaggaworks", c.text} {
				if strings.Contains(strings.ToLower(out+errs), leak) {
					t.Fatalf("output leaks %q: %q %q", leak, out, errs)
				}
			}
		})
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestScanMasksPathsHoldingTerms(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not meaningful on windows")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)

	t.Run("file name holds a term, clean body", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		p := filepath.Join(dir, "notes-zebracorp.txt")
		if err := os.WriteFile(p, []byte("clean\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		code, out, errs := runScan(t, getenv, p)
		want := "CLIENT_NAME_FOUND path " + dir + "/*** term 1\n"
		if code != 1 || out != want {
			t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
		}
		noLeak(t, out+errs)
	})

	t.Run("directory name holds a term, clean body", func(t *testing.T) {
		t.Parallel()
		base := t.TempDir()
		sub := filepath.Join(base, "zebracorp-dir")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(sub, "f.txt")
		if err := os.WriteFile(p, []byte("clean\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		code, out, errs := runScan(t, getenv, p)
		want := "CLIENT_NAME_FOUND path " + base + "/***/f.txt term 1\n"
		if code != 1 || out != want {
			t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
		}
		noLeak(t, out+errs)
	})

	t.Run("content finding in a file whose name holds a term", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		p := filepath.Join(dir, "zebracorp.txt")
		if err := os.WriteFile(p, []byte("zebracorp\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		code, out, errs := runScan(t, getenv, p)
		want := "CLIENT_NAME_FOUND path " + dir + "/*** term 1\n" +
			"CLIENT_NAME_FOUND " + dir + "/***:1:1 term 1\n"
		if code != 1 || out != want {
			t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
		}
		noLeak(t, out+errs)
	})

	t.Run("unreadable path error is masked", func(t *testing.T) {
		t.Parallel()
		p := filepath.Join(t.TempDir(), "zebracorp-missing")
		code, out, errs := runScan(t, getenv, p)
		if code != 2 {
			t.Fatalf("code %d out %q err %q, want 2", code, out, errs)
		}
		noLeak(t, out+errs)
	})
}

func noLeak(t *testing.T, s string) {
	t.Helper()
	low := strings.ToLower(s)
	for _, leak := range []string{"zebracorp", "quaggaworks", "cafézebra"} {
		if strings.Contains(low, leak) {
			t.Fatalf("output leaks a term: %q", s)
		}
	}
}

func TestMaskPath(t *testing.T) {
	t.Parallel()
	terms := []term{{n: 1, text: []rune("zebracorp")}, {n: 2, text: []rune("quagga"), part: true}}
	cases := map[string]string{
		"a/b/c.txt":               "a/b/c.txt",
		"a/zebracorp/c.txt":       "a/***/c.txt",
		"zebracorp.txt":           "***",
		"x/myquaggaapp/zebracorp": "x/***/***",
		"/abs/zebracorp/x":        "/abs/***/x",
		"zebracorpus/ok":          "zebracorpus/ok",
	}
	for in, want := range cases {
		if got := maskPath(terms, in); got != want {
			t.Errorf("maskPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScanTreatsNULAsLineBreak(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not meaningful on windows")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	p := writeFile(t, "bin.dat", "a\x00zebracorp\x00b\x00\x01\x02")
	code, out, errs := runScan(t, getenv, p)
	want := "CLIENT_NAME_FOUND " + p + ":2:1 term 1\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

func TestScanUsageAndTermListErrors(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not meaningful on windows")
	}
	good := setupConfig(t, testTerms(t), 0o600)
	target := writeFile(t, "f.txt", "zebracorp\n")

	for _, args := range [][]string{{"--msg", ""}, {"--range", ""}, {"--msg"}, {"--staged", "--history"}} {
		code, out, errs := runScan(t, good, args...)
		if code != 2 || out != "" || errs == "" {
			t.Errorf("args %q: code %d out %q err %q, want usage error with exit 2", args, code, out, errs)
		}
	}

	t.Run("no HOME and no XDG_CONFIG_HOME", func(t *testing.T) {
		t.Parallel()
		code, out, errs := runScan(t, func(string) string { return "" }, target)
		if code != 2 || out != "" || !strings.Contains(errs, "CLIENT_TERMS_UNAVAILABLE") {
			t.Fatalf("code %d out %q err %q", code, out, errs)
		}
	})

	t.Run("term line that folds to nothing", func(t *testing.T) {
		t.Parallel()
		getenv := setupConfig(t, []byte("zebracorp\n...\nquaggaworks part\n"), 0o600)
		code, out, errs := runScan(t, getenv, target)
		if code != 2 || out != "" || !strings.Contains(errs, "CLIENT_TERMS_UNAVAILABLE") {
			t.Fatalf("code %d out %q err %q", code, out, errs)
		}
	})

	t.Run("comments and blanks do not shift term numbers", func(t *testing.T) {
		t.Parallel()
		getenv := setupConfig(t, []byte("# note\n\nalpha\nzebracorp\n"), 0o600)
		code, out, _ := runScan(t, getenv, target)
		if code != 1 || out != "CLIENT_NAME_FOUND "+target+":1:1 term 2\n" {
			t.Fatalf("code %d out %q", code, out)
		}
	})
}

func TestScanMessageFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not meaningful on windows")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	p := writeFile(t, "MSG", "Subject\n\nfor zebracorp\n")
	code, out, _ := runScan(t, getenv, "--msg", p)
	if code != 1 || out != "CLIENT_NAME_FOUND "+p+":3:5 term 1\n" {
		t.Fatalf("code %d out %q", code, out)
	}
}

func TestScanExitsTwoWithoutTerms(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not meaningful on windows")
	}
	target := writeFile(t, "f.txt", "clean\n")

	cases := []struct {
		name   string
		getenv func(string) string
	}{
		{"missing", func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return filepath.Join(t.TempDir(), "nothing")
			}
			return ""
		}},
		{"empty", setupConfig(t, []byte("\n\n"), 0o600)},
		{"group and world readable", setupConfig(t, testTerms(t), 0o644)},
		{"group readable", setupConfig(t, testTerms(t), 0o640)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			code, out, errs := runScan(t, c.getenv, target)
			if code != 2 {
				t.Fatalf("code %d, want 2", code)
			}
			if !strings.Contains(errs, "CLIENT_TERMS_UNAVAILABLE") {
				t.Fatalf("stderr %q lacks CLIENT_TERMS_UNAVAILABLE", errs)
			}
			if out != "" {
				t.Fatalf("stdout %q, want empty", out)
			}
		})
	}
}

func TestScanHomeFallback(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not meaningful on windows")
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "fivetwenty")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "client-terms"), testTerms(t), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}
	p := writeFile(t, "f.txt", "zebracorp\n")
	code, _, errs := runScan(t, getenv, p)
	if code != 1 {
		t.Fatalf("code %d err %q, want 1", code, errs)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return gitInEnv(t, dir, nil, args...)
}

// gitInEnv runs git with extra environment entries that override the fixed
// identity.
func gitInEnv(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestScanGitModes(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not meaningful on windows")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)

	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "clean\n")
	gitIn(t, repo, "add", "a.txt")
	gitIn(t, repo, "commit", "-q", "-m", "First")
	base := gitIn(t, repo, "rev-parse", "HEAD")

	write("b.txt", "ok\nhello zebracorp\n")
	gitIn(t, repo, "add", "b.txt")
	// Staged scan reads the index. Change the worktree copy to clean text
	// afterwards to prove the index is what gets scanned.
	write("b.txt", "all clean now\n")

	code, out, errs := runIn(t, repo, getenv, "--staged")
	if code != 1 || out != "CLIENT_NAME_FOUND b.txt:2:7 term 1\n" {
		t.Fatalf("staged: code %d out %q err %q", code, out, errs)
	}

	gitIn(t, repo, "commit", "-q", "-m", "Mention zebracorp here")
	head := gitIn(t, repo, "rev-parse", "HEAD")

	t.Run("range finds message", func(t *testing.T) {
		t.Parallel()
		code, out, errs := runIn(t, repo, getenv, "--range", base+".."+head)
		want := "CLIENT_NAME_FOUND commit " + head + " message term 1\n" +
			"CLIENT_NAME_FOUND b.txt:2:7 term 1\n"
		if code != 1 || out != want {
			t.Fatalf("code %d out %q err %q", code, out, errs)
		}
	})

	t.Run("history finds message", func(t *testing.T) {
		t.Parallel()
		code, out, errs := runIn(t, repo, getenv, "--history")
		want := "CLIENT_NAME_FOUND commit " + head + " message term 1\n" +
			"CLIENT_NAME_FOUND b.txt:2:7 term 1\n"
		if code != 1 || out != want {
			t.Fatalf("code %d out %q err %q", code, out, errs)
		}
	})

	t.Run("range with clean history", func(t *testing.T) {
		t.Parallel()
		code, out, errs := runIn(t, repo, getenv, "--range", base+".."+base)
		if code != 0 || out != "" {
			t.Fatalf("code %d out %q err %q", code, out, errs)
		}
	})
}

// runIn runs the scan with the process-independent working directory
// supplied to the git-backed modes through the --C test hook.
func runIn(t *testing.T, dir string, getenv func(string) string, args ...string) (int, string, string) {
	t.Helper()
	return runScan(t, getenv, append([]string{"--C", dir}, args...)...)
}

func newRepo(t *testing.T) (string, func(name, body string)) {
	t.Helper()
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	return repo, func(name, body string) {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, repo, "add", name)
	}
}

func TestScanDiffAddedLineStartingWithPlusPlus(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	add("a.txt", "clean\n")
	gitIn(t, repo, "commit", "-q", "-m", "First")
	base := gitIn(t, repo, "rev-parse", "HEAD")
	add("a.txt", "clean\n++ one zebracorp\n+++ two\nthree zebracorp\n")
	gitIn(t, repo, "commit", "-q", "-m", "Second")
	head := gitIn(t, repo, "rev-parse", "HEAD")

	code, out, errs := runIn(t, repo, getenv, "--range", base+".."+head)
	want := "CLIENT_NAME_FOUND a.txt:2:8 term 1\nCLIENT_NAME_FOUND a.txt:4:7 term 1\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

func TestScanLogMessageWithControlBytes(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	add("a.txt", "clean\n")
	gitIn(t, repo, "commit", "-q", "--cleanup=verbatim", "-m", "x\x01y\x02z\x03 then zebracorp")
	head := gitIn(t, repo, "rev-parse", "HEAD")

	code, out, errs := runIn(t, repo, getenv, "--history")
	want := "CLIENT_NAME_FOUND commit " + head + " message term 1\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

func TestScanGitFailureNamesOnlySubcommandAndStatus(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	add("a.txt", "clean\n")
	gitIn(t, repo, "commit", "-q", "-m", "First")
	code, out, errs := runIn(t, repo, getenv, "--range", "nosuchrev..HEAD")
	if code != 2 || strings.TrimSpace(errs) != "namecheck: git rev-list failed with exit status 128" {
		t.Fatalf("code %d err %q, want exit 2 and the subcommand with its status", code, errs)
	}
	code, out, errs = runIn(t, repo, getenv, "--range", "nosuch-zebracorp..HEAD")
	if code != 2 {
		t.Fatalf("code %d, want 2", code)
	}
	noLeak(t, out+errs)
}

func haveGit() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func TestScanHistoryFilesWithNULBytes(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	add("bin.dat", "a\x00zebracorp\x00b\n")
	gitIn(t, repo, "commit", "-q", "-m", "First")
	forged := "q\x00" + strings.Repeat("ab12", 10) + "\x00\nzebracorp\n"
	add("forge.dat", forged)
	gitIn(t, repo, "commit", "-q", "-m", "Second")

	code, out, errs := runIn(t, repo, getenv, "--history")
	want := "CLIENT_NAME_FOUND bin.dat:1:1 term 1\nCLIENT_NAME_FOUND forge.dat:2:1 term 1\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

func TestScanHistoryBinaryAttributeAndMerge(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	add(".gitattributes", "*.dat binary\n")
	add("x.dat", "zebracorp\n")
	gitIn(t, repo, "commit", "-q", "-m", "First")
	code, out, errs := runIn(t, repo, getenv, "--history")
	want := "CLIENT_NAME_FOUND x.dat:1:1 term 1\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

func TestScanSecondHunkLineNumbers(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	var lines []string
	for i := 1; i <= 12; i++ {
		lines = append(lines, "line "+strconv.Itoa(i))
	}
	add("a.txt", strings.Join(lines, "\n")+"\n")
	gitIn(t, repo, "commit", "-q", "-m", "First")
	base := gitIn(t, repo, "rev-parse", "HEAD")
	lines[1] = "two zebracorp"
	lines[10] = "eleven zebracorp"
	add("a.txt", strings.Join(lines, "\n")+"\n")
	gitIn(t, repo, "commit", "-q", "-m", "Second")
	head := gitIn(t, repo, "rev-parse", "HEAD")
	code, out, errs := runIn(t, repo, getenv, "--range", base+".."+head)
	want := "CLIENT_NAME_FOUND a.txt:2:5 term 1\nCLIENT_NAME_FOUND a.txt:11:8 term 1\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

func TestScanRenameToNameHoldingTerm(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	add("old.txt", "one\ntwo\nthree\nfour\nfive\n")
	gitIn(t, repo, "commit", "-q", "-m", "First")
	base := gitIn(t, repo, "rev-parse", "HEAD")
	gitIn(t, repo, "mv", "old.txt", "zebracorp-new.txt")
	gitIn(t, repo, "commit", "-q", "-m", "Second")
	head := gitIn(t, repo, "rev-parse", "HEAD")
	code, out, errs := runIn(t, repo, getenv, "--range", base+".."+head)
	want := "CLIENT_NAME_FOUND path *** term 1\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

func TestScanNameWithSpaceIsReportedOnce(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	add("a zebracorp.txt", "zebracorp\n")
	gitIn(t, repo, "commit", "-q", "-m", "First")
	code, out, errs := runIn(t, repo, getenv, "--history")
	want := "CLIENT_NAME_FOUND path *** term 1\nCLIENT_NAME_FOUND ***:1:1 term 1\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

func TestScanSymlinks(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)

	t.Run("working tree link target is scanned and not followed", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		link := filepath.Join(dir, "link")
		if err := os.Symlink("sub-zebracorp-target", link); err != nil {
			t.Fatal(err)
		}
		code, out, errs := runScan(t, getenv, dir)
		want := "CLIENT_NAME_FOUND " + link + ":1:5 term 1\n"
		if code != 1 || out != want {
			t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
		}
	})

	t.Run("staged type change from link to file", func(t *testing.T) {
		t.Parallel()
		repo, add := newRepo(t)
		if err := os.Symlink("clean-target", filepath.Join(repo, "x")); err != nil {
			t.Fatal(err)
		}
		gitIn(t, repo, "add", "x")
		gitIn(t, repo, "commit", "-q", "-m", "First")
		if err := os.Remove(filepath.Join(repo, "x")); err != nil {
			t.Fatal(err)
		}
		add("x", "zebracorp\n")
		code, out, errs := runIn(t, repo, getenv, "--staged")
		want := "CLIENT_NAME_FOUND x:1:1 term 1\n"
		if code != 1 || out != want {
			t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
		}
	})
}

func TestTermsFileBOMAndRelativeXDG(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not meaningful on windows")
	}
	target := writeFile(t, "f.txt", "zebracorp\n")

	t.Run("leading byte order mark is ignored", func(t *testing.T) {
		t.Parallel()
		getenv := setupConfig(t, append([]byte("\ufeff"), testTerms(t)...), 0o600)
		code, out, errs := runScan(t, getenv, target)
		if code != 1 || out != "CLIENT_NAME_FOUND "+target+":1:1 term 1\n" {
			t.Fatalf("code %d out %q err %q", code, out, errs)
		}
	})

	t.Run("relative XDG_CONFIG_HOME counts as unset", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "fivetwenty")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "client-terms"), testTerms(t), 0o600); err != nil {
			t.Fatal(err)
		}
		getenv := func(k string) string {
			switch k {
			case "XDG_CONFIG_HOME":
				return "relative/dir"
			case "HOME":
				return home
			}
			return ""
		}
		if code, _, errs := runScan(t, getenv, target); code != 1 {
			t.Fatalf("code %d err %q, want 1 through the HOME fallback", code, errs)
		}
		only := func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return "relative/dir"
			}
			return ""
		}
		code, _, errs := runScan(t, only, target)
		if code != 2 || !strings.Contains(errs, "CLIENT_TERMS_UNAVAILABLE") {
			t.Fatalf("code %d err %q, want 2", code, errs)
		}
	})
}

func TestScanQuotedNamesInHistory(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)

	for _, c := range []struct{ name, body, want string }{
		{"with content", "zebracorp\n", "CLIENT_NAME_FOUND path *** term 1\nCLIENT_NAME_FOUND ***:1:1 term 1\n"},
		{"empty file", "", "CLIENT_NAME_FOUND path *** term 1\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			repo, add := newRepo(t)
			add("\nzebracorp", c.body)
			gitIn(t, repo, "commit", "-q", "-m", "First")
			code, out, errs := runIn(t, repo, getenv, "--history")
			if code != 1 || out != c.want {
				t.Fatalf("code %d out %q err %q, want %q", code, out, errs, c.want)
			}
			noLeak(t, out+errs)
		})
	}
}

func TestUnquoteName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`b/plain.txt`:                  "plain.txt",
		"b/with space\t":               "with space",
		`"b/\nline"`:                   "\nline",
		`"b/q\"uote\\back"`:            `q"uote\back`,
		"\"b/say \\\"hi\\\" k.txt\"\t": `say "hi" k.txt`,
		`"b/tail\t"`:                   "tail\t",
		`"b/\377\tbyte"`:               "\xff\tbyte",
		`"b/\303\251"`:                 "\u00e9",
		`"b/\001end"`:                  "\x01end",
	}
	for in, want := range cases {
		got, err := unquoteName(in)
		if err != nil || got != want {
			t.Errorf("unquoteName(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
	if _, err := unquoteName(`"b/unterminated`); err == nil {
		t.Error("unterminated quote accepted")
	}
}

func TestScanStagedStagePrefixNameAndSubmodule(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)

	t.Run("name that looks like a stage prefix", func(t *testing.T) {
		t.Parallel()
		repo, add := newRepo(t)
		add("0:x.txt", "zebracorp\n")
		code, out, errs := runIn(t, repo, getenv, "--staged")
		want := "CLIENT_NAME_FOUND 0:x.txt:1:1 term 1\n"
		if code != 1 || out != want {
			t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
		}
	})

	t.Run("staged submodule entry", func(t *testing.T) {
		t.Parallel()
		repo, add := newRepo(t)
		add("a.txt", "clean\n")
		gitIn(t, repo, "commit", "-q", "-m", "First")
		head := gitIn(t, repo, "rev-parse", "HEAD")
		gitIn(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+head+",sub")
		code, out, errs := runIn(t, repo, getenv, "--staged")
		if code != 0 || out != "" {
			t.Fatalf("code %d out %q err %q, want clean", code, out, errs)
		}
	})

	t.Run("staged submodule name is scanned", func(t *testing.T) {
		t.Parallel()
		repo, add := newRepo(t)
		add("a.txt", "clean\n")
		gitIn(t, repo, "commit", "-q", "-m", "First")
		head := gitIn(t, repo, "rev-parse", "HEAD")
		gitIn(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+head+",zebracorp-sub")
		code, out, errs := runIn(t, repo, getenv, "--staged")
		if code != 1 || out != "CLIENT_NAME_FOUND path *** term 1\n" {
			t.Fatalf("code %d out %q err %q", code, out, errs)
		}
	})
}

func TestScanCommitAuthorLine(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	add("a.txt", "clean\n")
	gitInEnv(t, repo, []string{"GIT_AUTHOR_NAME=zebracorp Person"}, "commit", "-q", "-m", "First")
	head := gitIn(t, repo, "rev-parse", "HEAD")
	code, out, errs := runIn(t, repo, getenv, "--history")
	want := "CLIENT_NAME_FOUND commit " + head + " message term 1\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

func TestScanHistoryMergeResolution(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	add("base.txt", "clean\n")
	gitIn(t, repo, "commit", "-q", "-m", "Base")
	gitIn(t, repo, "checkout", "-q", "-b", "side")
	add("side.txt", "clean side\n")
	gitIn(t, repo, "commit", "-q", "-m", "Side")
	gitIn(t, repo, "checkout", "-q", "main")
	add("main.txt", "clean main\n")
	gitIn(t, repo, "commit", "-q", "-m", "Main")
	gitIn(t, repo, "merge", "-q", "--no-ff", "--no-commit", "side")
	add("res.txt", "resolution zebracorp\n")
	gitIn(t, repo, "commit", "-q", "-m", "Merge")
	code, out, errs := runIn(t, repo, getenv, "--history")
	want := "CLIENT_NAME_FOUND res.txt:1:12 term 1\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

func TestScanDiffStopsWhenNameIsNotListed(t *testing.T) {
	t.Parallel()
	s := &scanner{terms: []term{{n: 1, text: []rune("zebracorp")}}, out: func(string) {}}
	patch := "diff --git a/x b/x\n--- /dev/null\n+++ b/x\n@@ -0,0 +1 @@\n+zebracorp\n"
	if err := s.scanDiff(patch, map[string]bool{}); err == nil {
		t.Fatal("scanDiff accepted a name that is not in the list")
	}
	if s.failed {
		t.Fatal("scanDiff reported findings before stopping")
	}
	if err := s.scanDiff(patch, map[string]bool{"x": true}); err != nil || !s.failed {
		t.Fatalf("listed name: err %v failed %v, want a finding", err, s.failed)
	}
}

func TestScanCommitBodyAfterNUL(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	add("a.txt", "clean\n")
	gitIn(t, repo, "commit", "-q", "-m", "First")
	tree := gitIn(t, repo, "rev-parse", "HEAD^{tree}")
	obj := "tree " + tree + "\nauthor t <t@example.com> 1700000000 +0000\n" +
		"committer t <t@example.com> 1700000000 +0000\n\nok\x00zebracorp\n"
	objFile := writeFile(t, "commit.obj", obj)
	hash := gitIn(t, repo, "hash-object", "-t", "commit", "-w", "--literally", objFile)
	gitIn(t, repo, "update-ref", "refs/heads/main", hash)
	code, out, errs := runIn(t, repo, getenv, "--history")
	want := "CLIENT_NAME_FOUND commit " + hash + " message term 1\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

func TestScanIgnoresReplaceRefs(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	add("a.txt", "clean\n")
	gitIn(t, repo, "commit", "-q", "-m", "innocent")
	a := gitIn(t, repo, "rev-parse", "HEAD")
	add("b.txt", "clean\n")
	gitIn(t, repo, "commit", "-q", "-m", "mentions zebracorp")
	b := gitIn(t, repo, "rev-parse", "HEAD")
	gitIn(t, repo, "replace", "-f", b, a)
	code, out, errs := runIn(t, repo, getenv, "--history")
	want := "CLIENT_NAME_FOUND commit " + b + " message term 1\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

func TestScanSubmoduleIgnoreSettingDoesNotHideName(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	add("a.txt", "clean\n")
	gitIn(t, repo, "commit", "-q", "-m", "First")
	head := gitIn(t, repo, "rev-parse", "HEAD")
	gitIn(t, repo, "config", "diff.ignoreSubmodules", "all")
	gitIn(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+head+",zebracorp-sub")
	code, out, errs := runIn(t, repo, getenv, "--staged")
	if code != 1 || out != "CLIENT_NAME_FOUND path *** term 1\n" {
		t.Fatalf("staged: code %d out %q err %q", code, out, errs)
	}
	gitIn(t, repo, "commit", "-q", "-m", "Second")
	code, out, errs = runIn(t, repo, getenv, "--history")
	if code != 1 || out != "CLIENT_NAME_FOUND path *** term 1\n" {
		t.Fatalf("history: code %d out %q err %q", code, out, errs)
	}
}

func TestScanCommitLineKeywordsAreNotScanned(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, []byte("author\ncommitter part\n1700000000 part\n"), 0o600)
	repo, add := newRepo(t)
	add("a.txt", "clean\n")
	gitInEnv(t, repo, []string{"GIT_AUTHOR_DATE=1700000000 +0000", "GIT_COMMITTER_DATE=1700000000 +0000"},
		"commit", "-q", "-m", "First")
	code, out, errs := runIn(t, repo, getenv, "--history")
	if code != 0 || out != "" {
		t.Fatalf("code %d out %q err %q, want clean", code, out, errs)
	}
}

func TestScanNameWithInvalidUTF8(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, _ := newRepo(t)
	blob := gitIn(t, repo, "hash-object", "-w", writeFile(t, "blob", "zebracorp\n"))
	gitIn(t, repo, "update-index", "--add", "--cacheinfo", "100644,"+blob+",\xff\tx zebracorp")
	gitIn(t, repo, "commit", "-q", "-m", "First")
	code, out, errs := runIn(t, repo, getenv, "--history")
	want := "CLIENT_NAME_FOUND path *** term 1\nCLIENT_NAME_FOUND ***:1:1 term 1\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

func lineDigest(line string) string {
	d := digestOf([]byte(line))
	return hex.EncodeToString(d[:])
}

// TestAllowFileClearsOneLine checks the .namecheck-allow rules: an entry
// clears the one line it names, a changed line is found again, an entry for
// another path clears nothing, and the program prints no digest it was not
// given.
func TestAllowFileClearsOneLine(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	const line = "hello zebracorp"

	cases := []struct {
		name     string
		content  string // the staged line
		allow    string // the allow file, empty for none
		wantCode int
	}{
		{"entry clears the line", line, "b.txt " + lineDigest(line) + " a harmless mention\n", 0},
		{"changed line is found again", line + "!", "b.txt " + lineDigest(line) + " a harmless mention\n", 1},
		{"entry for another path clears nothing", line, "c.txt " + lineDigest(line) + " a harmless mention\n", 1},
		{"no allow file", line, "", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo, write := newRepo(t)
			write("b.txt", tc.content+"\n")
			if tc.allow != "" {
				if err := os.WriteFile(filepath.Join(repo, ".namecheck-allow"), []byte(tc.allow), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			code, out, errs := runIn(t, repo, getenv, "--staged")
			if code != tc.wantCode {
				t.Fatalf("code %d, want %d; out %q err %q", code, tc.wantCode, out, errs)
			}
			if d := lineDigest(tc.content); strings.Contains(out+errs, d) {
				t.Fatalf("output printed a digest: %q %q", out, errs)
			}
		})
	}

	t.Run("malformed entry stops the run", func(t *testing.T) {
		t.Parallel()
		repo, write := newRepo(t)
		write("b.txt", "clean\n")
		if err := os.WriteFile(filepath.Join(repo, ".namecheck-allow"), []byte("b.txt nothex a reason\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		code, out, errs := runIn(t, repo, getenv, "--staged")
		if code != 2 {
			t.Fatalf("code %d, want 2; out %q err %q", code, out, errs)
		}
	})
}

func TestScanNamesMode(t *testing.T) {
	t.Parallel()
	getenv := setupConfig(t, testTerms(t), 0o600)

	t.Run("clean names pass", func(t *testing.T) {
		t.Parallel()
		code, out, errs := runScan(t, getenv, "--names", "refs/heads/work", "refs/tags/v1")
		if code != 0 || out != "" {
			t.Fatalf("code %d out %q err %q", code, out, errs)
		}
	})

	t.Run("a name holding a term is masked", func(t *testing.T) {
		t.Parallel()
		code, out, errs := runScan(t, getenv, "--names", "refs/heads/work", "refs/tags/zebracorp-1")
		if code != 1 || strings.Contains(out+errs, "zebracorp") || !strings.Contains(out, "CLIENT_NAME_FOUND") {
			t.Fatalf("code %d out %q err %q", code, out, errs)
		}
	})
}

// TestScanRangeTakesSeveralRevisions checks that --range reads a list of
// revisions, so that a hook can exclude what a remote already holds.
func TestScanRangeTakesSeveralRevisions(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, write := newRepo(t)
	write("a.txt", "clean\n")
	gitIn(t, repo, "commit", "-q", "-m", "First")
	first := gitIn(t, repo, "rev-parse", "HEAD")
	write("b.txt", "hello zebracorp\n")
	gitIn(t, repo, "commit", "-q", "-m", "Second")
	second := gitIn(t, repo, "rev-parse", "HEAD")

	code, out, errs := runIn(t, repo, getenv, "--range", second+" ^"+first)
	if code != 1 || !strings.Contains(out, "b.txt:1:7") {
		t.Fatalf("excluding the first commit: code %d out %q err %q", code, out, errs)
	}
	code, out, errs = runIn(t, repo, getenv, "--range", second+" ^"+second)
	if code != 0 || out != "" {
		t.Fatalf("excluding everything: code %d out %q err %q", code, out, errs)
	}
}

// tagRepo makes a repository with one commit and returns it.
func tagRepo(t *testing.T) string {
	t.Helper()
	repo, add := newRepo(t)
	add("a.txt", "clean\n")
	gitIn(t, repo, "commit", "-q", "-m", "First")
	return repo
}

func TestScanTagMessage(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)

	t.Run("term in the message", func(t *testing.T) {
		t.Parallel()
		repo := tagRepo(t)
		gitIn(t, repo, "tag", "-a", "-m", "release for zebracorp", "v1")
		sha := gitIn(t, repo, "rev-parse", "refs/tags/v1")
		code, out, errs := runIn(t, repo, getenv, "--tag", "refs/tags/v1", sha)
		want := "CLIENT_NAME_FOUND tag refs/tags/v1 message term 1\n"
		if code != 1 || out != want {
			t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
		}
	})

	t.Run("term in the tagger", func(t *testing.T) {
		t.Parallel()
		repo := tagRepo(t)
		gitInEnv(t, repo, []string{"GIT_COMMITTER_NAME=zebracorp"}, "tag", "-a", "-m", "clean", "v1")
		sha := gitIn(t, repo, "rev-parse", "refs/tags/v1")
		code, out, errs := runIn(t, repo, getenv, "--tag", "refs/tags/v1", sha)
		if code != 1 || !strings.HasPrefix(out, "CLIENT_NAME_FOUND tag refs/tags/v1 message term 1") {
			t.Fatalf("code %d out %q err %q", code, out, errs)
		}
	})

	t.Run("clean message", func(t *testing.T) {
		t.Parallel()
		repo := tagRepo(t)
		gitIn(t, repo, "tag", "-a", "-m", "release", "v1")
		sha := gitIn(t, repo, "rev-parse", "refs/tags/v1")
		code, out, errs := runIn(t, repo, getenv, "--tag", "refs/tags/v1", sha)
		if code != 0 || out != "" {
			t.Fatalf("code %d out %q err %q", code, out, errs)
		}
	})

	t.Run("lightweight tag", func(t *testing.T) {
		t.Parallel()
		repo := tagRepo(t)
		sha := gitIn(t, repo, "rev-parse", "HEAD")
		code, out, errs := runIn(t, repo, getenv, "--tag", "refs/tags/v1", sha)
		if code != 0 || out != "" {
			t.Fatalf("code %d out %q err %q", code, out, errs)
		}
	})

	t.Run("tag of a tag", func(t *testing.T) {
		t.Parallel()
		repo := tagRepo(t)
		gitIn(t, repo, "tag", "-a", "-m", "inner zebracorp", "inner")
		gitIn(t, repo, "tag", "-a", "-m", "outer", "outer", "refs/tags/inner")
		sha := gitIn(t, repo, "rev-parse", "refs/tags/outer")
		code, out, errs := runIn(t, repo, getenv, "--tag", "refs/tags/outer", sha)
		want := "CLIENT_NAME_FOUND tag refs/tags/outer message term 1\n"
		if code != 1 || out != want {
			t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
		}
	})

	t.Run("object and type lines are not scanned", func(t *testing.T) {
		t.Parallel()
		// Every hex digit is a term, so a scan of the object line would find
		// the hash. The message and tagger hold none of them.
		hex := setupConfig(t, []byte("a PART\nb PART\nc PART\nd PART\ne PART\nf PART\n"), 0o600)
		repo := tagRepo(t)
		gitInEnv(t, repo, []string{"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x.io"}, "tag", "-a", "-m", "ok", "v1")
		sha := gitIn(t, repo, "rev-parse", "refs/tags/v1")
		code, out, errs := runIn(t, repo, hex, "--tag", "refs/tags/v1", sha)
		if code != 0 || out != "" {
			t.Fatalf("code %d out %q err %q", code, out, errs)
		}
	})

	t.Run("fake signature marker before a term", func(t *testing.T) {
		t.Parallel()
		repo := tagRepo(t)
		gitIn(t, repo, "tag", "-a", "--cleanup=verbatim", "-m", "-----BEGIN PGP SIGNATURE-----\nzebracorp\n", "v1")
		sha := gitIn(t, repo, "rev-parse", "refs/tags/v1")
		code, out, errs := runIn(t, repo, getenv, "--tag", "refs/tags/v1", sha)
		if code != 1 || !strings.Contains(out, "CLIENT_NAME_FOUND tag refs/tags/v1") {
			t.Fatalf("code %d out %q err %q", code, out, errs)
		}
	})

	// rawTag writes a tag object with the given name line and body, points
	// refs/tags/raw at it, and returns its hash.
	rawTag := func(t *testing.T, repo, name, body string) string {
		t.Helper()
		commit := gitIn(t, repo, "rev-parse", "HEAD")
		obj := "object " + commit + "\ntype commit\ntag " + name + "\ntagger t <t@x.io> 1 +0000\n\n" + body
		sha := gitInput(t, repo, obj, "mktag")
		gitIn(t, repo, "update-ref", "refs/tags/raw", sha)
		return sha
	}

	// A block that only looks like a signature must not hide its contents.
	for _, kind := range []string{"PGP SIGNATURE", "SSH SIGNATURE", "SIGNED MESSAGE"} {
		t.Run("fake trailing "+kind+" block", func(t *testing.T) {
			t.Parallel()
			repo := tagRepo(t)
			sha := rawTag(t, repo, "v1", "release\n-----BEGIN "+kind+"-----\nzebracorp is here\n-----END "+kind+"-----\n")
			code, out, errs := runIn(t, repo, getenv, "--tag", "refs/tags/v1", sha)
			if code != 1 || !strings.Contains(out, "CLIENT_NAME_FOUND tag refs/tags/v1") {
				t.Fatalf("code %d out %q err %q", code, out, errs)
			}
		})
	}

	t.Run("term spelled in base64 characters inside a well-formed block", func(t *testing.T) {
		t.Parallel()
		// The safe choice is to scan the lines of a block too, so a term
		// written in base64 characters is found.
		repo := tagRepo(t)
		sha := rawTag(t, repo, "v1", "release\n-----BEGIN PGP SIGNATURE-----\n\niQEzBAABCAAdFiEE+zebracorp/Q0WfM+9cA7pQ\n=AbCd\n-----END PGP SIGNATURE-----\n")
		code, out, errs := runIn(t, repo, getenv, "--tag", "refs/tags/v1", sha)
		if code != 1 || !strings.Contains(out, "CLIENT_NAME_FOUND tag refs/tags/v1") {
			t.Fatalf("code %d out %q err %q", code, out, errs)
		}
	})

	t.Run("well-formed block with no term is clean", func(t *testing.T) {
		t.Parallel()
		repo := tagRepo(t)
		sha := rawTag(t, repo, "v1", "release\n-----BEGIN PGP SIGNATURE-----\n\niQEzBAABCAAdFiEEQ0WfM+/9cA7pQ\n=AbCd\n-----END PGP SIGNATURE-----\n")
		code, out, errs := runIn(t, repo, getenv, "--tag", "refs/tags/v1", sha)
		if code != 0 || out != "" {
			t.Fatalf("code %d out %q err %q", code, out, errs)
		}
	})

	t.Run("term in the tag's own name", func(t *testing.T) {
		t.Parallel()
		repo := tagRepo(t)
		sha := rawTag(t, repo, "zebracorp-1", "release\n")
		// Pushed by hash under another name, so only the object holds it.
		code, out, errs := runIn(t, repo, getenv, "--tag", "refs/tags/x", sha)
		if code != 1 || !strings.Contains(out, "CLIENT_NAME_FOUND tag refs/tags/x") {
			t.Fatalf("code %d out %q err %q", code, out, errs)
		}
	})

	t.Run("chain of exactly the cap passes", func(t *testing.T) {
		t.Parallel()
		repo := tagRepo(t)
		sha := tagChain(t, repo, maxTagChain)
		code, out, errs := runIn(t, repo, getenv, "--tag", "refs/tags/deep", sha)
		if code != 0 || out != "" {
			t.Fatalf("code %d out %q err %q", code, out, errs)
		}
	})

	t.Run("chain one past the cap fails closed", func(t *testing.T) {
		t.Parallel()
		repo := tagRepo(t)
		sha := tagChain(t, repo, maxTagChain+1)
		code, out, errs := runIn(t, repo, getenv, "--tag", "refs/tags/deep", sha)
		if code != 2 || out != "" || errs == "" {
			t.Fatalf("code %d out %q err %q, want exit 2", code, out, errs)
		}
	})

	t.Run("name with a term is masked", func(t *testing.T) {
		t.Parallel()
		repo := tagRepo(t)
		gitIn(t, repo, "tag", "-a", "-m", "zebracorp", "zebracorp-1")
		sha := gitIn(t, repo, "rev-parse", "refs/tags/zebracorp-1")
		code, out, _ := runIn(t, repo, getenv, "--tag", "refs/tags/zebracorp-1", sha)
		if code != 1 || strings.Contains(out, "zebracorp") {
			t.Fatalf("code %d out %q", code, out)
		}
	})
}

// gitInput runs git with text on standard input and returns its output.
func gitInput(t *testing.T, dir, input string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// tagChain makes n annotated tags, each pointing at the one before and the
// first at HEAD, and returns the hash of the last.
func tagChain(t *testing.T, repo string, n int) string {
	t.Helper()
	gitIn(t, repo, "tag", "-a", "-m", "m", "t1")
	for i := 2; i <= n; i++ {
		gitIn(t, repo, "tag", "-a", "-m", "m", fmt.Sprintf("t%d", i), fmt.Sprintf("refs/tags/t%d", i-1))
	}
	return gitIn(t, repo, "rev-parse", fmt.Sprintf("refs/tags/t%d", n))
}

func TestScanDecodesDeclaredEncoding(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	add("a.txt", "clean\n")
	// The message is Latin-1 text. The byte 0xE9 is an e with an acute
	// accent there, and it is not valid UTF-8 on its own.
	msg := writeFile(t, "msg.txt", "Visit caf\xe9zebra today\n")
	gitIn(t, repo, "-c", "i18n.commitEncoding=ISO-8859-1", "commit", "-q", "-F", msg)
	head := gitIn(t, repo, "rev-parse", "HEAD")
	if enc := gitIn(t, repo, "cat-file", "commit", "HEAD"); !strings.Contains(enc, "\nencoding ISO-8859-1\n") {
		t.Fatalf("the test commit lacks an encoding header: %q", enc)
	}

	code, out, errs := runIn(t, repo, getenv, "--history")
	want := "CLIENT_NAME_FOUND commit " + head + " message term 3\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

func TestScanReadsMergetagHeaders(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	add("a.txt", "clean\n")
	gitIn(t, repo, "commit", "-q", "-m", "First")
	gitIn(t, repo, "checkout", "-q", "-b", "side")
	add("b.txt", "side\n")
	gitIn(t, repo, "commit", "-q", "-m", "Side work")
	side := gitIn(t, repo, "rev-parse", "HEAD")
	gitIn(t, repo, "checkout", "-q", "main")
	add("c.txt", "main\n")
	gitIn(t, repo, "commit", "-q", "-m", "Main work")
	main := gitIn(t, repo, "rev-parse", "HEAD")
	tree := gitIn(t, repo, "rev-parse", "HEAD^{tree}")

	// Git writes a mergetag header when a merge takes in a signed tag. The
	// header carries the tag object, one space in front of each line. The
	// test writes the same shape by hand, with a tag message that holds a
	// term.
	obj := "tree " + tree + "\nparent " + main + "\nparent " + side + "\n" +
		"author t <t@example.com> 1700000000 +0000\n" +
		"committer t <t@example.com> 1700000000 +0000\n" +
		"mergetag object " + side + "\n" +
		" type commit\n tag v1\n tagger t <t@example.com> 1700000000 +0000\n" +
		" \n release for zebracorp\n" +
		"\nJoin the branches\n"
	objFile := writeFile(t, "merge-object.txt", obj)
	head := gitIn(t, repo, "hash-object", "-t", "commit", "-w", objFile)
	gitIn(t, repo, "update-ref", "refs/heads/main", head)

	code, out, errs := runIn(t, repo, getenv, "--history")
	want := "CLIENT_NAME_FOUND commit " + head + " message term 1\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

// koi8r is a name git accepts for a commit encoding and the scanner does not
// know.
const koi8r = "KOI8R"

func TestScanFailsOnUnknownEncoding(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	add("a.txt", "clean\n")
	msg := writeFile(t, "msg.txt", "Visit caf\xe9zebra today\n")
	gitIn(t, repo, "-c", "i18n.commitEncoding="+koi8r, "commit", "-q", "-F", msg)
	head := gitIn(t, repo, "rev-parse", "HEAD")

	code, out, errs := runIn(t, repo, getenv, "--history")
	want := "CLIENT_NAME_UNREADABLE commit " + head + " encoding <withheld, 5 bytes>\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

func TestScanUnknownEncodingStillScansRawText(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	add("a.txt", "clean\n")
	msg := writeFile(t, "msg.txt", "Mention zebracorp here\n")
	gitIn(t, repo, "-c", "i18n.commitEncoding="+koi8r, "commit", "-q", "-F", msg)
	head := gitIn(t, repo, "rev-parse", "HEAD")

	code, out, errs := runIn(t, repo, getenv, "--history")
	want := "CLIENT_NAME_FOUND commit " + head + " message term 1\n" +
		"CLIENT_NAME_UNREADABLE commit " + head + " encoding <withheld, 5 bytes>\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

func TestScanKeepsRawTextBesideDecodedText(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	add("a.txt", "clean\n")
	// The message is UTF-8 text under a Latin-1 header. Decoded as Latin-1,
	// the accented letter turns into two other characters, so only the raw
	// text still holds the term.
	msg := writeFile(t, "msg.txt", "Visit caf\u00e9zebra today\n")
	gitIn(t, repo, "-c", "i18n.commitEncoding=ISO-8859-1", "commit", "-q", "-F", msg)
	head := gitIn(t, repo, "rev-parse", "HEAD")

	code, out, errs := runIn(t, repo, getenv, "--history")
	want := "CLIENT_NAME_FOUND commit " + head + " message term 3\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

// commitWithHeader writes a root commit by hand, so that the encoding header
// can hold any bytes. An empty header leaves the commit without one.
func commitWithHeader(t *testing.T, repo, header, message string) string {
	t.Helper()
	tree := gitIn(t, repo, "write-tree")
	obj := "tree " + tree + "\n" +
		"author t <t@example.com> 1700000000 +0000\n" +
		"committer t <t@example.com> 1700000000 +0000\n"
	if header != "" {
		obj += "encoding " + header + "\n"
	}
	obj += "\n" + message
	hash := gitIn(t, repo, "hash-object", "-t", "commit", "-w", writeFile(t, "commit-object.txt", obj))
	gitIn(t, repo, "update-ref", "refs/heads/main", hash)

	return hash
}

// scanOneCommit scans the history of a repository with one staged file and
// one hand-written commit, and returns the exit code and the output.
func scanOneCommit(t *testing.T, header, message string) (string, int, string) {
	t.Helper()
	if runtime.GOOS == "windows" || !haveGit() {
		t.Skip("needs unix modes and git")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	repo, add := newRepo(t)
	add("a.txt", "clean\n")
	hash := commitWithHeader(t, repo, header, message)
	code, out, errs := runIn(t, repo, getenv, "--history")

	return hash, code, out + errs
}

func TestScanFailsOnEncodingsThatLoseBytes(t *testing.T) {
	t.Parallel()
	// The library knows a decoder for each name, and none of them is
	// trusted. The first four turn every byte into a replacement character.
	for _, name := range []string{"replacement", "iso-2022-kr", "hz-gb-2312", "x-user-defined", "KOI8-R", "UTF-16"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			hash, code, out := scanOneCommit(t, name, "Visit caf\xe9zebra today\n")
			want := "CLIENT_NAME_UNREADABLE commit " + hash + " encoding <withheld, " + strconv.Itoa(len(name)) + " bytes>\n"
			if code != 1 || out != want {
				t.Fatalf("code %d out %q, want %q", code, out, want)
			}
		})
	}
}

func TestScanFailsOnInvalidUTF8UnderUTF8OrNoHeader(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ header, want string }{
		"declared": {"UTF-8", "<withheld, 5 bytes>"},
		"absent":   {"", "<absent>"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			hash, code, out := scanOneCommit(t, c.header, "Visit caf\xe9zebra today\n")
			want := "CLIENT_NAME_UNREADABLE commit " + hash + " encoding " + c.want + "\n"
			if code != 1 || out != want {
				t.Fatalf("code %d out %q, want %q", code, out, want)
			}
		})
	}
}

func TestScanPassesValidTextUnderTrustedEncodings(t *testing.T) {
	t.Parallel()
	for _, header := range []string{"", "UTF-8", "windows-1252", "ISO-8859-15", "US-ASCII"} {
		t.Run("header "+header, func(t *testing.T) {
			t.Parallel()
			_, code, out := scanOneCommit(t, header, "A plain message\n")
			if code != 0 || out != "" {
				t.Fatalf("code %d out %q, want a clean pass", code, out)
			}
		})
	}
}

func TestScanFailureLineNeverEchoesTheHeader(t *testing.T) {
	t.Parallel()
	// Each header holds a made-up term, spelled in UTF-8, in Latin-1 bytes,
	// and plain. None may reach the output.
	for name, header := range map[string]string{
		"utf8":   "caf\u00e9zebra",
		"latin1": "caf\xe9zebra",
		"plain":  "zebracorp",
		"escape": "\x1b[31mred",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			hash, code, out := scanOneCommit(t, header, "A plain message\n")
			want := "CLIENT_NAME_UNREADABLE commit " + hash + " encoding <withheld, " + strconv.Itoa(len(header)) + " bytes>\n"
			if code != 1 || out != want {
				t.Fatalf("code %d out %q, want %q", code, out, want)
			}
		})
	}
}

func TestScanRetriesEncodingNameWithHyphens(t *testing.T) {
	t.Parallel()
	hash, code, out := scanOneCommit(t, "ISO_8859_1", "Visit caf\xe9zebra today\n")
	want := "CLIENT_NAME_FOUND commit " + hash + " message term 3\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q, want %q", code, out, want)
	}
}

func TestScanFailsOnReplacementCharacterFromTrustedDecoder(t *testing.T) {
	t.Parallel()
	// Byte 0xAA has no meaning in windows-1253, so its decoder writes a
	// replacement character in its place.
	hash, code, out := scanOneCommit(t, "windows-1253", "Visit caf\xaa today\n")
	want := "CLIENT_NAME_UNREADABLE commit " + hash + " encoding <withheld, 12 bytes>\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q, want %q", code, out, want)
	}
}

func TestScanFindsTermsSplitByInvisibleCharacters(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not meaningful on windows")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)

	splitters := map[string]string{
		"zero width space":          "\u200b",
		"zero width non-joiner":     "\u200c",
		"zero width joiner":         "\u200d",
		"word joiner":               "\u2060",
		"soft hyphen":               "\u00ad",
		"byte order mark":           "\ufeff",
		"left-to-right mark":        "\u200e",
		"right-to-left mark":        "\u200f",
		"left-to-right embed":       "\u202a",
		"right-to-left override":    "\u202e",
		"left-to-right isolate":     "\u2066",
		"c0 escape":                 "\x1b",
		"c0 bell":                   "\x07",
		"c1 control 0x81":           "\u0081",
		"c1 control 0x9f":           "\u009f",
		"c1 next line":              "\u0085",
		"variation selector 1":      "\ufe00",
		"variation selector 16":     "\ufe0f",
		"variation selector 17":     "\U000e0100",
		"variation selector 256":    "\U000e01ef",
		"combining grapheme joiner": "\u034f",
	}
	for name, sep := range splitters {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := writeFile(t, "f.txt", "first line\nthe zebra"+sep+"corp lab\n")
			code, out, errs := runScan(t, getenv, p)
			want := "CLIENT_NAME_FOUND " + p + ":2:5 term 1\n"
			if code != 1 || out != want {
				t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
			}
			for _, leak := range []string{"zebracorp", "zebra", "corp"} {
				if strings.Contains(strings.ToLower(out+errs), leak) {
					t.Fatalf("output leaks %q: %q %q", leak, out, errs)
				}
			}
		})
	}
}

func TestScanFindsTermSplitAtEveryPosition(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not meaningful on windows")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	text := "z\u200be\u200bb\u200dr\u0081a\u00adc\u200co\ufeffr\u2060p"
	p := writeFile(t, "g.txt", text+"\n")
	code, out, errs := runScan(t, getenv, p)
	want := "CLIENT_NAME_FOUND " + p + ":1:1 term 1\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

func TestScanFindsTermsSplitByInvisibleBytesUnderLatin1(t *testing.T) {
	t.Parallel()
	// ISO-8859-1 defines every byte, so 0x81 and 0xAD decode to a C1
	// control and a soft hyphen instead of failing the scan.
	for name, b := range map[string]string{"0x81": "\x81", "0xad": "\xad"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			hash, code, out := scanOneCommit(t, "ISO-8859-1", "Visit zebra"+b+"corp today\n")
			want := "CLIENT_NAME_FOUND commit " + hash + " message term 1\n"
			if code != 1 || out != want {
				t.Fatalf("code %d out %q, want %q", code, out, want)
			}
		})
	}
}

func TestScanInvisibleCharactersInTermListAreDropped(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not meaningful on windows")
	}
	// The term itself carries a zero-width space, so it must fold to the
	// same runes as the plain text.
	getenv := setupConfig(t, []byte("zebra\u200bcorp\n"), 0o600)
	p := writeFile(t, "f.txt", "the zebracorp lab\n")
	code, out, errs := runScan(t, getenv, p)
	want := "CLIENT_NAME_FOUND " + p + ":1:5 term 1\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
}

func TestNormalizeKeepsTabsAndLineBreaks(t *testing.T) {
	t.Parallel()
	got, cols := normalize("a\tb\r\nc\nd")
	if string(got) != "a\tb\r\nc\nd" {
		t.Fatalf("got %q", string(got))
	}
	if len(cols) != len(got) {
		t.Fatalf("cols %d runes %d", len(cols), len(got))
	}
	got, _ = normalize("a\u200bb\u00adc\u0085d\x1be\ufe0ff\u034fg")
	if string(got) != "abcdefg" {
		t.Fatalf("got %q, want abcdefg", string(got))
	}
}

func TestScanOrdinaryTextWithTabsAndNewlinesBehavesAsBefore(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not meaningful on windows")
	}
	getenv := setupConfig(t, testTerms(t), 0o600)
	p := writeFile(t, "f.txt", "clean\tline\r\n\tzebracorp\there\nzebra\tcorp\n")
	code, out, errs := runScan(t, getenv, p)
	want := "CLIENT_NAME_FOUND " + p + ":2:2 term 1\n"
	if code != 1 || out != want {
		t.Fatalf("code %d out %q err %q, want %q", code, out, errs, want)
	}
	clean := writeFile(t, "c.txt", "plain\ttext\r\nwith\nbreaks\n")
	if code, out, errs := runScan(t, getenv, clean); code != 0 || out != "" || errs != "" {
		t.Fatalf("code %d out %q err %q, want clean", code, out, errs)
	}
}
