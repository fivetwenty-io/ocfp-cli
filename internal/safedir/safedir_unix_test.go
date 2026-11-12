//go:build unix

package safedir_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/safedir"
)

// mkdirMode makes dir and gives it mode, whatever the umask.
func mkdirMode(t *testing.T, dir string, mode fs.FileMode) {
	t.Helper()

	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
}

func TestAncestorsAcceptsPrivateParents(t *testing.T) {
	t.Parallel()

	if err := safedir.Ancestors(filepath.Join(t.TempDir(), "x")); err != nil {
		t.Fatalf("Ancestors refused a test directory's own parents: %v", err)
	}
}

func TestAncestorsRefusesAWritableParent(t *testing.T) {
	t.Parallel()

	for _, mode := range []fs.FileMode{0o777, 0o770, 0o722} {
		shared := filepath.Join(t.TempDir(), "shared")
		mkdirMode(t, shared, mode)
		mkdirMode(t, filepath.Join(shared, "mine"), 0o700)

		if err := safedir.Ancestors(filepath.Join(shared, "x")); err == nil {
			t.Errorf("Ancestors accepted a %s parent", mode)
		}

		if err := safedir.Ancestors(filepath.Join(shared, "mine", "x")); err == nil {
			t.Errorf("Ancestors accepted a %s grandparent", mode)
		}
	}
}

func TestAncestorsAcceptsAStickyParent(t *testing.T) {
	t.Parallel()

	shared := filepath.Join(t.TempDir(), "shared")
	mkdirMode(t, shared, 0o777|fs.ModeSticky)

	info, err := os.Stat(shared)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode()&fs.ModeSticky == 0 {
		t.Skip("this file system does not keep the sticky bit")
	}

	if err := safedir.Ancestors(filepath.Join(shared, "x")); err != nil {
		t.Fatalf("Ancestors refused a sticky parent: %v", err)
	}
}

func TestAncestorsFollowsLinks(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	shared := filepath.Join(base, "shared")
	mkdirMode(t, shared, 0o777)
	mkdirMode(t, filepath.Join(shared, "mine"), 0o700)

	if err := os.Symlink(filepath.Join("shared", "mine"), filepath.Join(base, "unsafe")); err != nil {
		t.Fatal(err)
	}

	if err := safedir.Ancestors(filepath.Join(base, "unsafe", "x")); err == nil {
		t.Fatal("Ancestors accepted a link that resolves through a writable directory")
	}

	real := filepath.Join(base, "real")
	mkdirMode(t, real, 0o700)

	if err := os.Symlink(real, filepath.Join(base, "safe")); err != nil {
		t.Fatal(err)
	}

	if err := safedir.Ancestors(filepath.Join(base, "safe", "x")); err != nil {
		t.Fatalf("Ancestors refused a link through private directories: %v", err)
	}

	if err := os.Symlink(filepath.Join("..", "shared", "mine"), filepath.Join(real, "up")); err != nil {
		t.Fatal(err)
	}

	if err := safedir.Ancestors(filepath.Join(real, "up", "x")); err == nil {
		t.Fatal("Ancestors accepted a link that climbs into a writable directory")
	}
}

func TestAncestorsRefusesALinkLoop(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	if err := os.Symlink("b", filepath.Join(base, "a")); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink("a", filepath.Join(base, "b")); err != nil {
		t.Fatal(err)
	}

	if err := safedir.Ancestors(filepath.Join(base, "a", "x")); err == nil {
		t.Fatal("Ancestors accepted a link loop")
	}
}

func TestAncestorsRefusesAFileParent(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := safedir.Ancestors(filepath.Join(file, "x")); err == nil {
		t.Fatal("Ancestors accepted a file as a parent")
	}
}

func TestAncestorsReportsAMissingParent(t *testing.T) {
	t.Parallel()

	err := safedir.Ancestors(filepath.Join(t.TempDir(), "missing", "x"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Ancestors with a missing parent = %v, want fs.ErrNotExist", err)
	}
}

// A parent owned by another user who is not root can be replaced by that
// user whatever its mode.
func TestAncestorsRefusesAnotherOwner(t *testing.T) {
	t.Parallel()

	euid := os.Geteuid()
	if euid == 0 {
		t.Skip("every parent is owned by root or by this user")
	}

	path := filepath.Join(t.TempDir(), "x")

	if err := safedir.AncestorsAs(path, euid); err != nil {
		t.Fatalf("AncestorsAs refused this user's own parents: %v", err)
	}

	if err := safedir.AncestorsAs(path, euid+1); err == nil {
		t.Fatal("AncestorsAs accepted parents owned by another user")
	}
}

func TestEntry(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	dir := filepath.Join(base, "dir")
	mkdirMode(t, dir, 0o750)

	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o640); err != nil {
		t.Fatal(err)
	}

	if err := safedir.Entry(dir, true); err != nil {
		t.Errorf("Entry refused a private directory: %v", err)
	}

	if err := safedir.Entry(file, false); err != nil {
		t.Errorf("Entry refused a private file: %v", err)
	}

	if err := safedir.Entry(file, true); err == nil {
		t.Error("Entry accepted a file as a directory")
	}

	if err := safedir.Entry(dir, false); err == nil {
		t.Error("Entry accepted a directory as a file")
	}

	if err := safedir.Entry(filepath.Join(base, "missing"), false); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Entry on a missing path = %v, want fs.ErrNotExist", err)
	}
}

func TestEntryRefuses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		dir  bool
		make func(t *testing.T, path string)
	}{
		{"a linked directory", true, func(t *testing.T, path string) {
			real := path + "-real"
			mkdirMode(t, real, 0o700)

			if err := os.Symlink(real, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"a linked file", false, func(t *testing.T, path string) {
			if err := os.WriteFile(path+"-real", nil, 0o600); err != nil {
				t.Fatal(err)
			}

			if err := os.Symlink(path+"-real", path); err != nil {
				t.Fatal(err)
			}
		}},
		{"a group-writable directory", true, func(t *testing.T, path string) { mkdirMode(t, path, 0o770) }},
		{"an other-writable directory", true, func(t *testing.T, path string) { mkdirMode(t, path, 0o707) }},
		{"a sticky other-writable directory", true, func(t *testing.T, path string) { mkdirMode(t, path, 0o777|fs.ModeSticky) }},
		{"a group-writable file", false, func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}

			if err := os.Chmod(path, 0o660); err != nil {
				t.Fatal(err)
			}
		}},
		{"an other-writable file", false, func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}

			if err := os.Chmod(path, 0o602); err != nil {
				t.Fatal(err)
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "entry")
			tc.make(t, path)

			if err := safedir.Entry(path, tc.dir); err == nil {
				t.Fatalf("Entry accepted %s", tc.name)
			}
		})
	}
}

func TestEntryRefusesAnotherOwner(t *testing.T) {
	t.Parallel()

	euid := os.Geteuid()
	if euid == 0 {
		t.Skip("root owns whatever this test makes")
	}

	dir := t.TempDir()

	if err := safedir.EntryAs(dir, true, euid); err != nil {
		t.Fatalf("EntryAs refused this user's own directory: %v", err)
	}

	if err := safedir.EntryAs(dir, true, euid+1); err == nil {
		t.Fatal("EntryAs accepted a directory owned by another user")
	}

	if err := safedir.EntryAs("/", true, euid+1); err != nil {
		t.Fatalf("EntryAs refused a directory owned by root: %v", err)
	}
}
