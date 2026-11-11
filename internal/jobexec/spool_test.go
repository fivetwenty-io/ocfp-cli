package jobexec

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// spoolRoot returns a spool root that only the test's user can write.
func spoolRoot(t *testing.T) string {
	t.Helper()

	root := filepath.Join(t.TempDir(), "spool")

	err := os.Mkdir(root, 0o700)
	if err != nil {
		t.Fatal(err)
	}

	return root
}

func skipOnWindows(t *testing.T, why string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip(why)
	}
}

func TestCreateSpoolOnce(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)

	s, created, err := CreateSpool(root, "job-0001")
	if err != nil || !created {
		t.Fatalf("first CreateSpool = %v, %v; want created", created, err)
	}

	if want := filepath.Join(root, "job-0001"); s.Dir != want {
		t.Fatalf("spool dir = %q, want %q", s.Dir, want)
	}

	again, created, err := CreateSpool(root, "job-0001")
	if err != nil || created {
		t.Fatalf("second CreateSpool = %v, %v; want an existing spool", created, err)
	}

	if again.Dir != s.Dir {
		t.Fatalf("second spool dir = %q, want %q", again.Dir, s.Dir)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Lstat(s.Dir)
		if err != nil {
			t.Fatal(err)
		}

		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Fatalf("spool dir mode = %v, want no access for group or other", perm)
		}
	}
}

// Many runs that create the same spool at the same instant must see exactly
// one creator.
func TestCreateSpoolHasOneCreator(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)

	const racers = 16

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
		errs    []error
	)

	start := make(chan struct{})

	for range racers {
		wg.Go(func() {
			<-start

			_, c, err := CreateSpool(root, "job-race")

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				errs = append(errs, err)
			}

			if c {
				created++
			}
		})
	}

	close(start)
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("CreateSpool failed: %v", errors.Join(errs...))
	}

	if created != 1 {
		t.Fatalf("%d runs created the spool, want exactly 1", created)
	}
}

func TestCreateSpoolRefusesJobIDsThatLeaveTheRoot(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)

	for _, id := range []string{
		"", ".", "..", "../x", "x/../../y", "a/b", `a\b`, "/abs", "-x", ".x",
		"Job", "job\x00x", "job\nx", strings.Repeat("a", 65),
	} {
		_, created, err := CreateSpool(root, id)
		if err == nil || created {
			t.Errorf("CreateSpool(%q) = %v, %v; want a refusal", id, created, err)
		}
	}

	entries, err := os.ReadDir(filepath.Dir(root))
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 1 {
		t.Fatalf("the root's parent holds %d entries, want only the root", len(entries))
	}

	entries, err = os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 0 {
		t.Fatalf("the root holds %d entries after refused job ids, want none", len(entries))
	}
}

func TestCreateSpoolMakesAMissingRoot(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "spool")

	_, created, err := CreateSpool(root, "job-0001")
	if err != nil || !created {
		t.Fatalf("CreateSpool = %v, %v; want created", created, err)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Lstat(root)
		if err != nil {
			t.Fatal(err)
		}

		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Fatalf("root mode = %v, want no access for group or other", perm)
		}
	}

	// Only the last part of the root is made. A missing parent is an error.
	_, _, err = CreateSpool(filepath.Join(t.TempDir(), "missing", "spool"), "job-0001")
	if err == nil {
		t.Fatal("CreateSpool made a root whose parent does not exist")
	}
}

func TestCreateSpoolRefusesASymlinkRoot(t *testing.T) {
	t.Parallel()
	skipOnWindows(t, "symbolic links need a privilege on Windows")

	real := spoolRoot(t)
	link := filepath.Join(t.TempDir(), "spool-link")

	err := os.Symlink(real, link)
	if err != nil {
		t.Fatal(err)
	}

	_, created, err := CreateSpool(link, "job-0001")
	if err == nil || created {
		t.Fatalf("CreateSpool through a symlinked root = %v, %v; want an error", created, err)
	}

	if !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("error %q does not say the root is a symbolic link", err)
	}

	if _, err := os.Lstat(filepath.Join(real, "job-0001")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a spool was made behind the link: %v", err)
	}
}

func TestCreateSpoolRefusesARootOthersCanWrite(t *testing.T) {
	t.Parallel()
	skipOnWindows(t, "Windows has no group and other write bits")

	for _, mode := range []os.FileMode{0o777, 0o770, 0o702} {
		root := spoolRoot(t)

		err := os.Chmod(root, mode)
		if err != nil {
			t.Fatal(err)
		}

		_, created, err := CreateSpool(root, "job-0001")
		if err == nil || created {
			t.Errorf("CreateSpool with root mode %v = %v, %v; want an error", mode, created, err)

			continue
		}

		if !strings.Contains(err.Error(), "writable") {
			t.Errorf("error %q does not say others can write the root", err)
		}
	}
}

func TestCreateSpoolRefusesARootThatIsAFile(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "spool")

	err := os.WriteFile(root, nil, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	_, created, err := CreateSpool(root, "job-0001")
	if err == nil || created {
		t.Fatalf("CreateSpool on a file root = %v, %v; want an error", created, err)
	}
}

// A spool path that already exists counts as an existing spool only when it is
// a private directory of this user's.
func TestCreateSpoolRefusesAForeignSpoolPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		windows bool
		make    func(t *testing.T, path string)
	}{
		{"file", true, func(t *testing.T, path string) {
			t.Helper()

			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink to a directory", false, func(t *testing.T, path string) {
			t.Helper()

			if err := os.Symlink(t.TempDir(), path); err != nil {
				t.Fatal(err)
			}
		}},
		{"directory others can write", false, func(t *testing.T, path string) {
			t.Helper()

			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}

			if err := os.Chmod(path, 0o777); err != nil {
				t.Fatal(err)
			}
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if !c.windows {
				skipOnWindows(t, "needs symbolic links or mode bits")
			}

			root := spoolRoot(t)
			c.make(t, filepath.Join(root, "job-0001"))

			_, created, err := CreateSpool(root, "job-0001")
			if err == nil || created {
				t.Fatalf("CreateSpool = %v, %v; want an error", created, err)
			}
		})
	}
}

func TestSpoolWritesStatusAndExit(t *testing.T) {
	t.Parallel()

	s, _, err := CreateSpool(spoolRoot(t), "job-0001")
	if err != nil {
		t.Fatal(err)
	}

	code := 0
	st := Status{State: StateExited, PID: 77, StartedAt: testNow, ExitCode: &code}

	err = s.WriteStatus(Status{State: StateRunning, PID: 77, StartedAt: testNow})
	if err != nil {
		t.Fatal(err)
	}

	err = s.WriteStatus(st)
	if err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.readStatus()
	if err != nil || !ok {
		t.Fatalf("readStatus = %v, %v", ok, err)
	}

	if got.State != StateExited || got.PID != 77 || !got.StartedAt.Equal(testNow) || got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("status = %+v, want %+v", got, st)
	}

	err = s.WriteExit(Exit{ExitCode: 0, FinishedAt: testNow.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}

	e, ok, err := s.readExit()
	if err != nil || !ok || e.ExitCode != 0 || !e.FinishedAt.Equal(testNow.Add(time.Second)) {
		t.Fatalf("readExit = %+v, %v, %v", e, ok, err)
	}

	// No temporary file is left behind.
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}

	if strings.Join(names, ",") != "exit.json,status.json" {
		t.Fatalf("spool holds %v, want exit.json and status.json", names)
	}
}

// The lock on out is what tells an attach that the run which made the spool
// lives. A check of it must see a lock held through another open file, must
// leave no lock of its own behind, and must see the lock go when the file
// that held it is closed.
func TestOutLock(t *testing.T) {
	t.Parallel()

	s, _, err := CreateSpool(spoolRoot(t), "job-0001")
	if err != nil {
		t.Fatal(err)
	}

	run, err := s.createOut()
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = run.Close() }()

	must(t, lockOut(run))

	reader, ok, err := s.openOut()
	if err != nil || !ok {
		t.Fatalf("openOut = %v, %v", ok, err)
	}

	defer func() { _ = reader.Close() }()

	for i := range 2 {
		held, err := outHeld(reader)
		if err != nil || !held {
			t.Fatalf("check %d while the run holds out = %v, %v; want held", i, held, err)
		}
	}

	other, ok, err := s.openOut()
	if err != nil || !ok {
		t.Fatalf("openOut = %v, %v", ok, err)
	}

	defer func() { _ = other.Close() }()

	if lockOut(other) == nil {
		t.Fatal("a second exclusive lock on out was granted")
	}

	must(t, run.Close())

	held, err := outHeld(reader)
	if err != nil || held {
		t.Fatalf("check after the run closed out = %v, %v; want free", held, err)
	}

	// The check gave back the shared lock it took, so an exclusive lock
	// is free to take.
	must(t, lockOut(other))
}
