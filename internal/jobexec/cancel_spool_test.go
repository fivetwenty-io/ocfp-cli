package jobexec

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress/progresstest"
)

func TestWriteCancelIsExclusive(t *testing.T) {
	t.Parallel()

	s, _, err := CreateSpool(spoolRoot(t), "job-0001")
	if err != nil {
		t.Fatal(err)
	}

	if _, ok, err := s.readCancel(); err != nil || ok {
		t.Fatalf("readCancel before a cancel = %v, %v; want none", ok, err)
	}

	first := Cancel{RequestedAt: time.Date(2026, 11, 10, 9, 0, 1, 0, time.UTC)}

	created, err := s.WriteCancel(first)
	if err != nil || !created {
		t.Fatalf("first WriteCancel = %v, %v; want created", created, err)
	}

	created, err = s.WriteCancel(Cancel{RequestedAt: first.RequestedAt.Add(time.Hour)})
	if err != nil || created {
		t.Fatalf("second WriteCancel = %v, %v; want an existing marker and no error", created, err)
	}

	got, ok, err := s.readCancel()
	if err != nil || !ok {
		t.Fatalf("readCancel = %v, %v", ok, err)
	}

	if !got.RequestedAt.Equal(first.RequestedAt) {
		t.Fatalf("marker holds %s, want the first cancel's %s", got.RequestedAt, first.RequestedAt)
	}

	if want := `{"requestedAt":"2026-11-10T09:00:01Z"}` + "\n"; string(readFile(t, filepath.Join(s.Dir, cancelFile))) != want {
		t.Fatalf("cancel.json holds %q, want %q", readFile(t, filepath.Join(s.Dir, cancelFile)), want)
	}

	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 1 || entries[0].Name() != cancelFile {
		t.Fatalf("the spool holds %v, want only %s and no temporary file", entries, cancelFile)
	}
}

func TestWriteCancelNeverTouchesTheRunsFiles(t *testing.T) {
	t.Parallel()

	s := spoolWith(t, exitFile, "exit-v1.json")
	before := readFile(t, filepath.Join(s.Dir, exitFile))

	if _, err := s.WriteCancel(Cancel{RequestedAt: fixtureStart}); err != nil {
		t.Fatal(err)
	}

	if string(readFile(t, filepath.Join(s.Dir, exitFile))) != string(before) {
		t.Fatal("WriteCancel changed exit.json")
	}
}

func TestReadCancelRefusesAnUnknownField(t *testing.T) {
	t.Parallel()

	s := spoolWith(t, cancelFile, "exit-v1.json")

	if _, _, err := s.readCancel(); err == nil {
		t.Fatal("readCancel accepted a marker with unknown fields")
	}
}

func TestOpenSpoolNeverCreates(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)

	_, err := OpenSpool(root, "job-0001")
	if !errors.Is(err, ErrSpoolNotFound) {
		t.Fatalf("OpenSpool of a missing spool = %v, want ErrSpoolNotFound", err)
	}

	entries, rerr := os.ReadDir(root)
	if rerr != nil {
		t.Fatal(rerr)
	}

	if len(entries) != 0 {
		t.Fatalf("OpenSpool made %d entries", len(entries))
	}

	missing := filepath.Join(t.TempDir(), "no-root")

	_, err = OpenSpool(missing, "job-0001")
	if !errors.Is(err, ErrSpoolNotFound) {
		t.Fatalf("OpenSpool under a missing root = %v, want ErrSpoolNotFound", err)
	}

	if _, serr := os.Lstat(missing); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("OpenSpool made the root: %v", serr)
	}

	made, created, err := CreateSpool(root, "job-0001")
	if err != nil || !created {
		t.Fatalf("CreateSpool = %v, %v", created, err)
	}

	opened, err := OpenSpool(root, "job-0001")
	if err != nil || opened != made {
		t.Fatalf("OpenSpool = %+v, %v; want %+v", opened, err, made)
	}
}

// TestOpenSpoolRunsTheSpoolChecks checks that OpenSpool refuses what
// CreateSpool refuses, with an error other than ErrSpoolNotFound.
func TestOpenSpoolRunsTheSpoolChecks(t *testing.T) {
	t.Parallel()

	t.Run("job ids that leave the root", func(t *testing.T) {
		t.Parallel()

		root := spoolRoot(t)

		for _, id := range []string{"", "..", "../x", "a/b", "Job", "-x"} {
			_, err := OpenSpool(root, id)
			if err == nil || errors.Is(err, ErrSpoolNotFound) {
				t.Errorf("OpenSpool(%q) = %v; want a refusal", id, err)
			}
		}
	})

	t.Run("symlink root", func(t *testing.T) {
		t.Parallel()
		skipOnWindows(t, "symbolic links need a privilege on Windows")

		real := spoolRoot(t)
		link := filepath.Join(t.TempDir(), "spool-link")

		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}

		_, err := OpenSpool(link, "job-0001")
		if err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("OpenSpool through a symlinked root = %v", err)
		}
	})

	t.Run("root others can write", func(t *testing.T) {
		t.Parallel()
		skipOnWindows(t, "Windows has no group and other write bits")

		root := spoolRoot(t)
		if err := os.Chmod(root, 0o777); err != nil {
			t.Fatal(err)
		}

		_, err := OpenSpool(root, "job-0001")
		if err == nil || !strings.Contains(err.Error(), "writable") {
			t.Fatalf("OpenSpool under a writable root = %v", err)
		}
	})

	t.Run("spool that is a file", func(t *testing.T) {
		t.Parallel()

		root := spoolRoot(t)
		if err := os.WriteFile(filepath.Join(root, "job-0001"), nil, 0o600); err != nil {
			t.Fatal(err)
		}

		_, err := OpenSpool(root, "job-0001")
		if err == nil || errors.Is(err, ErrSpoolNotFound) {
			t.Fatalf("OpenSpool of a file = %v; want a refusal", err)
		}
	})

	t.Run("spool others can write", func(t *testing.T) {
		t.Parallel()
		skipOnWindows(t, "Windows has no group and other write bits")

		root := spoolRoot(t)
		dir := filepath.Join(root, "job-0001")

		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}

		_, err := OpenSpool(root, "job-0001")
		if err == nil || errors.Is(err, ErrSpoolNotFound) {
			t.Fatalf("OpenSpool of a writable spool = %v; want a refusal", err)
		}
	})

	t.Run("spool that is a link", func(t *testing.T) {
		t.Parallel()
		skipOnWindows(t, "symbolic links need a privilege on Windows")

		root := spoolRoot(t)
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "job-0001")); err != nil {
			t.Fatal(err)
		}

		_, err := OpenSpool(root, "job-0001")
		if err == nil || errors.Is(err, ErrSpoolNotFound) {
			t.Fatalf("OpenSpool of a link = %v; want a refusal", err)
		}
	})
}

func TestExitRefusesAStoredAlreadyFinished(t *testing.T) {
	t.Parallel()

	for _, cancel := range []string{"already_finished", "stoppped", "STOPPED"} {
		s, _, err := CreateSpool(spoolRoot(t), "job-0001")
		if err != nil {
			t.Fatal(err)
		}

		body := `{"exitCode":1,"finishedAt":"2026-11-10T09:00:03Z","cancel":"` + cancel + `"}` + "\n"
		if err := os.WriteFile(filepath.Join(s.Dir, exitFile), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, _, err := s.readExit(); err == nil || !strings.Contains(err.Error(), "cancel") {
			t.Errorf("readExit of cancel %q = %v; want a refusal that names the cancel", cancel, err)
		}
	}
}

func TestCanceledExitFixtureLoads(t *testing.T) {
	t.Parallel()

	s := spoolWith(t, exitFile, "exit-canceled-v1.json")

	e, ok, err := s.readExit()
	if err != nil || !ok {
		t.Fatalf("readExit = %v, %v", ok, err)
	}

	seen := fixtureStart.Add(1200 * time.Millisecond)

	if e.ExitCode != 1 || e.Cancel != progress.CancelStopped || e.CancelSeenAt == nil || !e.CancelSeenAt.Equal(seen) ||
		!e.FinishedAt.Equal(fixtureStart.Add(3*time.Second)) {
		t.Fatalf("exit = %+v, want code 1, stopped, seen at %s", e, seen)
	}

	// The fixture is what the real writer produces for the same value.
	w, _, err := CreateSpool(spoolRoot(t), "job-fixture-01")
	if err != nil {
		t.Fatal(err)
	}

	if err := w.WriteExit(e); err != nil {
		t.Fatal(err)
	}

	if got, want := readFile(t, filepath.Join(w.Dir, exitFile)), progresstest.Fixture("exit-canceled-v1.json"); string(got) != string(want) {
		t.Fatalf("WriteExit wrote\n%s\nthe fixture holds\n%s", got, want)
	}
}

func TestAnUncanceledExitWritesAsBefore(t *testing.T) {
	t.Parallel()

	s, _, err := CreateSpool(spoolRoot(t), "job-0001")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.WriteExit(Exit{ExitCode: 0, FinishedAt: fixtureStart.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}

	if got, want := readFile(t, filepath.Join(s.Dir, exitFile)), progresstest.Fixture("exit-v1.json"); string(got) != string(want) {
		t.Fatalf("WriteExit wrote\n%s\nthe fixture holds\n%s", got, want)
	}
}

func TestStatusRecordsTheWireContract(t *testing.T) {
	t.Parallel()

	s, _, err := CreateSpool(spoolRoot(t), "job-0001")
	if err != nil {
		t.Fatal(err)
	}

	in := Status{State: StateRunning, PID: 7, StartedAt: fixtureStart, WireContract: progress.WireContract}
	if err := s.WriteStatus(in); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(readFile(t, filepath.Join(s.Dir, statusFile))), `"wireContract":3`) {
		t.Fatalf("status.json lacks the wire contract: %s", readFile(t, filepath.Join(s.Dir, statusFile)))
	}

	out, ok, err := s.readStatus()
	if err != nil || !ok || out.WireContract != progress.WireContract {
		t.Fatalf("readStatus = %+v, %v, %v", out, ok, err)
	}
}

func TestARunRecordsItsWireContractInStatus(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))

	if err := rc.run(t); err != nil {
		t.Fatalf("Run = %v", err)
	}

	s, err := OpenSpool(root, "job-0001")
	if err != nil {
		t.Fatal(err)
	}

	st, ok, err := s.readStatus()
	if err != nil || !ok || st.WireContract != progress.WireContract {
		t.Fatalf("status = %+v, %v, %v; want wire contract %d", st, ok, err, progress.WireContract)
	}
}

// Only a marker that is there counts as a cancel. One that opens and reads
// but does not decode, or is too large, counts, because only a cancel writes
// one. One that cannot be opened or read, as when the process is out of
// descriptors, is not seen yet, and the run looks again at its next poll.
func TestOnlyAMarkerThatIsThereCountsAsACancel(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("the spool does not run on Windows")
	}

	for name, tc := range map[string]struct {
		make func(t *testing.T, path string)
		want bool
	}{
		"none": {func(*testing.T, string) {}, false},
		"good": {func(t *testing.T, path string) {
			t.Helper()
			must(t, os.WriteFile(path, []byte(`{"requestedAt":"2026-11-11T12:00:00Z"}`), 0o600))
		}, true},
		"not json": {func(t *testing.T, path string) {
			t.Helper()
			must(t, os.WriteFile(path, []byte("not json"), 0o600))
		}, true},
		"too large": {func(t *testing.T, path string) {
			t.Helper()
			must(t, os.WriteFile(path, bytes.Repeat([]byte(" "), maxSpoolJSON+1), 0o600))
		}, true},
		"cannot be opened": {func(t *testing.T, path string) {
			t.Helper()
			must(t, os.Symlink(filepath.Base(path), path))
		}, false},
		"cannot be read": {func(t *testing.T, path string) {
			t.Helper()
			must(t, os.Mkdir(path, 0o700))
		}, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, _, err := CreateSpool(spoolRoot(t), "job-0001")
			must(t, err)

			tc.make(t, filepath.Join(s.Dir, cancelFile))

			if got := s.canceled(); got != tc.want {
				t.Fatalf("canceled = %v, want %v", got, tc.want)
			}
		})
	}
}
