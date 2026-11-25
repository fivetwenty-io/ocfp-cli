//go:build unix

package enroll

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReadNoFollow(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	outside := t.TempDir()

	must := func(err error) {
		t.Helper()

		if err != nil {
			t.Fatal(err)
		}
	}

	must(os.WriteFile(filepath.Join(dir, "ok"), []byte("bytes"), 0o644))
	must(os.Mkdir(filepath.Join(dir, "staging"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "staging", "ready"), []byte("1"), 0o644))
	must(os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600))
	must(os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "link")))
	must(os.Symlink(outside, filepath.Join(dir, "linkdir")))
	must(os.Symlink("ok", filepath.Join(dir, "inside-link")))
	must(os.WriteFile(filepath.Join(dir, "big"), bytes.Repeat([]byte("x"), MaxSmallFile+1), 0o644))
	must(syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o644))
	must(os.Mkdir(filepath.Join(dir, "adir"), 0o755))

	if b, err := ReadNoFollow(dir, "ok", MaxSmallFile); err != nil || string(b) != "bytes" {
		t.Errorf("ReadNoFollow(ok) = %q, %v", b, err)
	}

	if b, err := ReadNoFollow(dir, "staging/ready", MaxSmallFile); err != nil || string(b) != "1" {
		t.Errorf("ReadNoFollow(staging/ready) = %q, %v", b, err)
	}

	for _, name := range []string{
		"link", "inside-link", "linkdir/secret", "big", "fifo", "adir", "missing", "../x", "/etc/passwd", "", "staging/../ok", "./ok",
	} {
		if b, err := ReadNoFollow(dir, name, MaxSmallFile); !errors.Is(err, ErrFileRule) {
			t.Errorf("ReadNoFollow(%q) = %q, %v", name, b, err)
		} else if name != "" && !bytes.Contains([]byte(err.Error()), []byte(dir)) {
			t.Errorf("the refusal of %q does not name the path: %v", name, err)
		}
	}

	if _, err := ReadNoFollow(filepath.Join(dir, "linkdir"), "secret", MaxSmallFile); !errors.Is(err, ErrFileRule) {
		t.Errorf("a linked base directory: %v", err)
	}

	// The rules never touch a file they refuse.
	if fi, err := os.Lstat(filepath.Join(dir, "link")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Error("a refused link was moved")
	}
}

func TestCreateExclusive(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	outside := t.TempDir()

	if err := CreateExclusive(dir, "ca.pem", []byte("pem"), -1, -1, 0o640); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Lstat(filepath.Join(dir, "ca.pem"))
	if err != nil || fi.Mode().Perm() != 0o640 || !fi.Mode().IsRegular() {
		t.Fatalf("ca.pem = %v, %v", fi, err)
	}

	if err := CreateExclusive(dir, "ca.pem", []byte("other"), -1, -1, 0o600); !errors.Is(err, ErrFileRule) {
		t.Errorf("a second create = %v", err)
	}

	if b, _ := os.ReadFile(filepath.Join(dir, "ca.pem")); string(b) != "pem" {
		t.Error("a refused create changed the file")
	}

	if err := os.Symlink(filepath.Join(outside, "target"), filepath.Join(dir, "planted")); err != nil {
		t.Fatal(err)
	}

	if err := CreateExclusive(dir, "planted", []byte("x"), -1, -1, 0o600); !errors.Is(err, ErrFileRule) {
		t.Errorf("a create over a planted link = %v", err)
	}

	if _, err := os.Lstat(filepath.Join(outside, "target")); !os.IsNotExist(err) {
		t.Error("a create followed a planted link")
	}

	if err := CreateExclusive(dir, "../escape", []byte("x"), -1, -1, 0o600); !errors.Is(err, ErrFileRule) {
		t.Errorf("a create outside the directory = %v", err)
	}

	// Setting an owner the process may not take fails after the create,
	// and the file it made is gone.
	if os.Geteuid() != 0 {
		if err := CreateExclusive(dir, "owned", []byte("x"), 0, 0, 0o600); !errors.Is(err, ErrFileRule) {
			t.Errorf("a chown to root without root = %v", err)
		}

		if _, err := os.Lstat(filepath.Join(dir, "owned")); !os.IsNotExist(err) {
			t.Error("a failed create left its file")
		}
	}
}
