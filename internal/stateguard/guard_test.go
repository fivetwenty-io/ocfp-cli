package stateguard

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, content string) {
	t.Helper()

	err := os.MkdirAll(filepath.Dir(path), 0o750)
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatal(err)
	}
}

func TestGuardPassesWhenNothingChanges(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "ocfp")
	write(t, filepath.Join(root, "bloc", "state", "bloc.json"), "{}")

	var report bytes.Buffer

	if code := guard([]string{root}, &report, func() int { return 0 }); code != 0 || report.Len() != 0 {
		t.Errorf("got code %d and report %q, want 0 and nothing", code, report.String())
	}
}

func TestGuardPassesWhenTheDirectoryIsAbsentThroughout(t *testing.T) {
	t.Parallel()

	var report bytes.Buffer

	root := filepath.Join(t.TempDir(), "never-made")

	if code := guard([]string{root}, &report, func() int { return 0 }); code != 0 || report.Len() != 0 {
		t.Errorf("got code %d and report %q, want 0 and nothing", code, report.String())
	}
}

func TestGuardFailsWhenTheTestsChangeTheDirectory(t *testing.T) {
	t.Parallel()

	runs := map[string]struct {
		setup  func(root string)
		change func(root string)
		path   string
		kind   string
	}{
		"created file": {
			setup: func(string) {},
			change: func(root string) {
				_ = os.MkdirAll(root, 0o750)
				_ = os.WriteFile(filepath.Join(root, "new.json"), []byte("{}"), 0o600)
			},
			path: "new.json",
			kind: "added",
		},
		"created directory": {
			setup:  func(root string) { _ = os.MkdirAll(root, 0o750) },
			change: func(root string) { _ = os.MkdirAll(filepath.Join(root, "env-bloc-name", "state"), 0o750) },
			path:   "env-bloc-name",
			kind:   "added",
		},
		"rewritten file": {
			setup: func(root string) {
				_ = os.MkdirAll(root, 0o750)
				_ = os.WriteFile(filepath.Join(root, "a.json"), []byte("one"), 0o600)
				_ = os.Chtimes(filepath.Join(root, "a.json"), time.Unix(1_000_000_000, 0), time.Unix(1_000_000_000, 0))
			},
			// Same size, so only the modification time tells the two apart.
			change: func(root string) { _ = os.WriteFile(filepath.Join(root, "a.json"), []byte("two"), 0o600) },
			path:   "a.json",
			kind:   "changed",
		},
		"removed file": {
			setup: func(root string) {
				_ = os.MkdirAll(root, 0o750)
				_ = os.WriteFile(filepath.Join(root, "a.json"), []byte("one"), 0o600)
			},
			change: func(root string) { _ = os.Remove(filepath.Join(root, "a.json")) },
			path:   "a.json",
			kind:   "removed",
		},
	}

	for name, run := range runs {
		root := filepath.Join(t.TempDir(), "ocfp")
		run.setup(root)

		var report bytes.Buffer

		code := guard([]string{root}, &report, func() int {
			run.change(root)

			return 0
		})
		if code == 0 {
			t.Errorf("%s: got code 0, want a failure", name)
		}

		if !strings.Contains(report.String(), filepath.Join(root, run.path)) || !strings.Contains(report.String(), run.kind) {
			t.Errorf("%s: report %q does not say %s %s", name, report.String(), run.kind, filepath.Join(root, run.path))
		}
	}
}

func TestGuardKeepsTheTestsFailure(t *testing.T) {
	t.Parallel()

	var report bytes.Buffer

	if code := guard([]string{t.TempDir()}, &report, func() int { return 3 }); code != 3 {
		t.Errorf("got code %d, want the tests' own 3", code)
	}
}

// The report names paths and digests. It never carries what a file holds,
// because the state directory holds keys and credentials.
func TestGuardReportsNoFileContents(t *testing.T) {
	t.Parallel()

	const secret = "PRIVATE-KEY-MATERIAL-9f3a"

	root := filepath.Join(t.TempDir(), "ocfp")
	write(t, filepath.Join(root, "key"), secret)

	var report bytes.Buffer

	guard([]string{root}, &report, func() int {
		write(t, filepath.Join(root, "key"), secret+"-rotated")
		write(t, filepath.Join(root, "other"), secret)

		return 0
	})

	if report.Len() == 0 {
		t.Fatal("want a report")
	}

	if strings.Contains(report.String(), "PRIVATE-KEY") {
		t.Errorf("report %q carries file contents", report.String())
	}
}

// Looking at a tree changes nothing in it, including through a link that
// leaves it.
func TestSnapshotOnlyReadsAndDoesNotFollowLinks(t *testing.T) {
	t.Parallel()

	outside := filepath.Join(t.TempDir(), "outside")
	write(t, filepath.Join(outside, "file"), "x")

	root := filepath.Join(t.TempDir(), "ocfp")
	write(t, filepath.Join(root, "a"), "a")

	err := os.Symlink(outside, filepath.Join(root, "link"))
	if err != nil {
		t.Fatal(err)
	}

	first := snapshot(root)
	second := snapshot(root)

	if len(first) != len(second) {
		t.Fatalf("snapshots differ: %v vs %v", first, second)
	}

	for path := range first {
		if strings.HasPrefix(path, filepath.Join(root, "link")+string(filepath.Separator)) {
			t.Errorf("followed the link into %s", path)
		}
	}
}

func TestRootsNamePassdHomeStateAndAnyOverride(t *testing.T) {
	roots := roots("/home/dev", map[string]string{"XDG_STATE_HOME": "/xdg/state", "OCFP_HOME": "/opt/ocfp"})

	for _, want := range []string{
		"/home/dev/.local/state/ocfp", "/home/dev/.local/share/ocfp", "/home/dev/.config/ocfp", "/home/dev/.ocfp",
		"/xdg/state/ocfp", "/opt/ocfp",
	} {
		found := false

		for _, root := range roots {
			found = found || root == want
		}

		if !found {
			t.Errorf("roots %v lack %s", roots, want)
		}
	}
}

// The guard never opens a file, so a file it cannot read is no different
// from one it can, and an unchanged one passes.
func TestSnapshotDoesNotOpenFiles(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "ocfp")
	write(t, filepath.Join(root, "locked"), "secret")

	err := os.Chmod(filepath.Join(root, "locked"), 0)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "locked"), 0o600) })

	entry := snapshot(root)[filepath.Join(root, "locked")]
	if !strings.HasPrefix(entry, "file ") {
		t.Errorf("entry %q for a file with no read permission, want a file entry", entry)
	}
}
