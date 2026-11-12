//go:build unix

package jobexec

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
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

func wantNoRoot(t *testing.T, root string) {
	t.Helper()

	if _, err := os.Lstat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the spool root was made under an unsafe ancestor: %v", err)
	}
}

// Another user who can write an ancestor of the spool root can rename the
// spool away and put their own in its place, so the root is refused before
// anything is made in it.
func TestPrepareRootRefusesAWritableAncestor(t *testing.T) {
	t.Parallel()

	for _, mode := range []fs.FileMode{0o777, 0o770, 0o722} {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()

			shared := filepath.Join(t.TempDir(), "shared")
			mkdirMode(t, shared, mode)

			root := filepath.Join(shared, "spool")

			if err := prepareRoot(root); err == nil {
				t.Fatalf("prepareRoot accepted a root under a %s directory", mode)
			}

			wantNoRoot(t, root)

			mine := filepath.Join(shared, "mine")
			mkdirMode(t, mine, 0o700)

			deeper := filepath.Join(mine, "spool")

			if err := prepareRoot(deeper); err == nil {
				t.Fatalf("prepareRoot accepted a root two levels under a %s directory", mode)
			}

			wantNoRoot(t, deeper)
		})
	}
}

// A sticky directory lets only an entry's owner rename or remove it, so a
// shared sticky directory such as /tmp is a safe ancestor.
func TestPrepareRootAcceptsAStickyAncestor(t *testing.T) {
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

	root := filepath.Join(shared, "spool")

	if err := prepareRoot(root); err != nil {
		t.Fatalf("prepareRoot refused a root under a sticky directory: %v", err)
	}
}

func TestPrepareRootFollowsAncestorLinks(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	shared := filepath.Join(base, "shared")
	mkdirMode(t, shared, 0o777)
	mkdirMode(t, filepath.Join(shared, "mine"), 0o700)

	unsafe := filepath.Join(base, "unsafe-link")
	if err := os.Symlink(filepath.Join("shared", "mine"), unsafe); err != nil {
		t.Fatal(err)
	}

	if err := prepareRoot(filepath.Join(unsafe, "spool")); err == nil {
		t.Fatal("prepareRoot accepted a root whose link resolves through a writable directory")
	}

	wantNoRoot(t, filepath.Join(shared, "mine", "spool"))

	real := filepath.Join(base, "real")
	mkdirMode(t, real, 0o700)

	safe := filepath.Join(base, "safe-link")
	if err := os.Symlink(real, safe); err != nil {
		t.Fatal(err)
	}

	if err := prepareRoot(filepath.Join(safe, "spool")); err != nil {
		t.Fatalf("prepareRoot refused a root whose link resolves through private directories: %v", err)
	}

	up := filepath.Join(real, "up")
	if err := os.Symlink(filepath.Join("..", "shared", "mine"), up); err != nil {
		t.Fatal(err)
	}

	if err := prepareRoot(filepath.Join(up, "spool")); err == nil {
		t.Fatal("prepareRoot accepted a root whose link climbs into a writable directory")
	}
}

func TestPrepareRootRefusesALinkLoop(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	if err := os.Symlink("b", filepath.Join(base, "a")); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink("a", filepath.Join(base, "b")); err != nil {
		t.Fatal(err)
	}

	if err := prepareRoot(filepath.Join(base, "a", "spool")); err == nil {
		t.Fatal("prepareRoot accepted a root under a link loop")
	}
}

// A root spelled with ".." after a link names the same directory for the
// checks, the mkdir, and every job beneath it.
func TestCreateSpoolCleansTheRoot(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	shared := filepath.Join(base, "shared")
	mkdirMode(t, shared, 0o777)
	mkdirMode(t, filepath.Join(shared, "mine"), 0o700)

	if err := os.Symlink(filepath.Join("shared", "mine"), filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}

	// The root the path names once it is cleaned, writable by others.
	mkdirMode(t, filepath.Join(base, "spool"), 0o777)

	root := filepath.Join(base, "link") + "/../spool"

	if _, _, err := CreateSpool(root, "job-clean"); err == nil {
		t.Fatal("CreateSpool accepted a root whose clean form others can write")
	}

	if _, err := os.Lstat(filepath.Join(shared, "spool")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("CreateSpool made a root where the link leads: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(base, "spool", "job-clean")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("CreateSpool made a job under a root others can write: %v", err)
	}
}
