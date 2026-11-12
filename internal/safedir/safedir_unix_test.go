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

// realPath is path with every symbolic link in it resolved. On macOS this
// moves a temporary directory from /var to /private/var.
func realPath(t *testing.T, path string) string {
	t.Helper()

	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}

	return real
}

func symlink(t *testing.T, target, link string) {
	t.Helper()

	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// wantResolved checks that Resolve accepts path and returns want, and that no
// directory above what it returns is a symbolic link.
func wantResolved(t *testing.T, path, want string) {
	t.Helper()

	got, err := safedir.Resolve(path)
	if err != nil {
		t.Fatalf("Resolve(%s) refused: %v", path, err)
	}

	if got != want {
		t.Fatalf("Resolve(%s) = %s, want %s", path, got, want)
	}

	if parent := filepath.Dir(got); realPath(t, parent) != parent {
		t.Fatalf("Resolve(%s) = %s, whose parent still holds a symbolic link", path, got)
	}
}

// A home directory or a data directory reached through a link that this user
// owns must keep working, and the caller must get the directory the link
// leads to, so that what was checked is what it uses.
func TestResolveFollowsALinkToAPrivateDirectory(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	data := filepath.Join(base, "data")
	mkdirMode(t, data, 0o700)
	mkdirMode(t, filepath.Join(data, "sub"), 0o750)

	symlink(t, data, filepath.Join(base, "abs"))
	symlink(t, "data", filepath.Join(base, "rel"))
	symlink(t, filepath.Join("..", "data", "sub"), filepath.Join(data, "up"))

	want := filepath.Join(realPath(t, data), "sub", "x")

	wantResolved(t, filepath.Join(base, "abs", "sub", "x"), want)
	wantResolved(t, filepath.Join(base, "rel", "sub", "x"), want)
	wantResolved(t, filepath.Join(base, "abs", "up", "x"), want)
	wantResolved(t, filepath.Join(data, "sub", "x"), want)
}

// A link that leads into a directory another user can write gives that user
// a way to swap what lies below it, so it is refused however the link is
// written.
func TestResolveRefusesALinkIntoAWritableDirectory(t *testing.T) {
	t.Parallel()

	for _, mode := range []fs.FileMode{0o777, 0o770, 0o722} {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()

			base := t.TempDir()

			shared := filepath.Join(base, "shared")
			mkdirMode(t, shared, mode)
			mkdirMode(t, filepath.Join(shared, "mine"), 0o700)

			mine := filepath.Join(base, "mine")
			mkdirMode(t, mine, 0o700)

			symlink(t, shared, filepath.Join(base, "into"))
			symlink(t, filepath.Join(shared, "mine"), filepath.Join(base, "below"))
			symlink(t, filepath.Join("..", "shared", "mine"), filepath.Join(mine, "up"))

			for _, path := range []string{
				filepath.Join(base, "into", "x"),
				filepath.Join(base, "below", "x"),
				filepath.Join(mine, "up", "x"),
			} {
				got, err := safedir.Resolve(path)
				if err == nil {
					t.Errorf("Resolve(%s) accepted a link into a %s directory and returned %s", path, mode, got)
				}

				if got != "" {
					t.Errorf("Resolve(%s) refused but returned %s", path, got)
				}
			}
		})
	}
}

// Resolve follows links only above the last name. A link at the last name is
// kept as written, so that Entry still refuses it.
func TestResolveKeepsALinkAtTheLastName(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	data := filepath.Join(base, "data")
	mkdirMode(t, data, 0o700)
	mkdirMode(t, filepath.Join(data, "real"), 0o700)

	symlink(t, data, filepath.Join(base, "home"))
	symlink(t, filepath.Join(data, "real"), filepath.Join(data, "leaf"))

	want := filepath.Join(realPath(t, data), "leaf")
	wantResolved(t, filepath.Join(base, "home", "leaf"), want)

	if err := safedir.Entry(want, true); err == nil {
		t.Fatal("Entry accepted the link that Resolve kept")
	}
}

// A last name that does not exist yet is fine, as it is for Ancestors, so
// that a caller can make it. A missing directory above it is not.
func TestResolveWithAMissingName(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	data := filepath.Join(base, "data")
	mkdirMode(t, data, 0o700)
	symlink(t, data, filepath.Join(base, "home"))
	symlink(t, filepath.Join(base, "nowhere"), filepath.Join(base, "dangling"))

	wantResolved(t, filepath.Join(base, "home", "missing"), filepath.Join(realPath(t, data), "missing"))

	for _, path := range []string{
		filepath.Join(base, "home", "missing", "x"),
		filepath.Join(base, "dangling", "x"),
	} {
		if _, err := safedir.Resolve(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Resolve(%s) = %v, want fs.ErrNotExist", path, err)
		}
	}
}

func TestResolveCleansThePath(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	data := filepath.Join(base, "data")
	mkdirMode(t, data, 0o700)

	// filepath.Join would clean the path, so build it by hand.
	wantResolved(t, data+"/./gone/../x", filepath.Join(realPath(t, data), "x"))
	wantResolved(t, "/", "/")
}

func TestResolveRefusesALinkLoop(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	symlink(t, "b", filepath.Join(base, "a"))
	symlink(t, "a", filepath.Join(base, "b"))

	if _, err := safedir.Resolve(filepath.Join(base, "a", "x")); err == nil {
		t.Fatal("Resolve accepted a link loop")
	}
}

func TestResolveAsRefusesAnotherOwner(t *testing.T) {
	t.Parallel()

	euid := os.Geteuid()
	if euid == 0 {
		t.Skip("every parent is owned by root or by this user")
	}

	path := filepath.Join(t.TempDir(), "x")

	if _, err := safedir.ResolveAs(path, euid); err != nil {
		t.Fatalf("ResolveAs refused this user's own parents: %v", err)
	}

	if _, err := safedir.ResolveAs(path, euid+1); err == nil {
		t.Fatal("ResolveAs accepted parents owned by another user")
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

// A link above the path that is moved between the walk and the second
// resolution is refused, even when it is moved to another safe directory.
func TestResolveRefusesALinkMovedWhileItWasChecked(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")
	mkdirMode(t, first, 0o700)
	mkdirMode(t, second, 0o700)

	link := filepath.Join(base, "current")
	symlink(t, first, link)

	moved := func() {
		if err := os.Remove(link); err != nil {
			t.Error(err)
		}

		if err := os.Symlink(second, link); err != nil {
			t.Error(err)
		}
	}

	got, err := safedir.ResolveAsWithHooks(filepath.Join(link, "name"), os.Geteuid(), moved, nil)
	if err == nil || got != "" {
		t.Fatalf("ResolveAs with a link moved after the walk = %q, %v; want a refusal", got, err)
	}
}

// A directory of the resolved parent that changes after the second
// resolution is checked again and refused.
func TestResolveChecksTheResolvedParentAgain(t *testing.T) {
	t.Parallel()

	t.Run("made writable by others", func(t *testing.T) {
		t.Parallel()

		dir := filepath.Join(t.TempDir(), "dir")
		mkdirMode(t, dir, 0o700)

		opened := func() {
			if err := os.Chmod(dir, 0o777); err != nil {
				t.Error(err)
			}
		}

		got, err := safedir.ResolveAsWithHooks(filepath.Join(dir, "name"), os.Geteuid(), nil, opened)
		if err == nil || got != "" {
			t.Fatalf("ResolveAs with a parent opened after resolution = %q, %v; want a refusal", got, err)
		}
	})

	t.Run("replaced by a link", func(t *testing.T) {
		t.Parallel()

		base := t.TempDir()
		dir := filepath.Join(base, "dir")
		mkdirMode(t, dir, 0o700)

		swapped := func() {
			if err := os.Rename(dir, filepath.Join(base, "moved")); err != nil {
				t.Error(err)
			}

			if err := os.Symlink(filepath.Join(base, "moved"), dir); err != nil {
				t.Error(err)
			}
		}

		got, err := safedir.ResolveAsWithHooks(filepath.Join(dir, "name"), os.Geteuid(), nil, swapped)
		if err == nil || got != "" {
			t.Fatalf("ResolveAs with a parent replaced by a link = %q, %v; want a refusal", got, err)
		}
	})
}
