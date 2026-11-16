//go:build unix

package jobexec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"golang.org/x/sys/unix"
)

// lockFree reports whether nothing else holds a lock on path, by taking one
// and giving it back.
func lockFree(t *testing.T, path string) bool {
	t.Helper()

	f, err := os.OpenFile(path, os.O_RDWR, 0o600) //nolint:gosec // a file in the test's own directory
	must(t, err)

	defer func() { _ = f.Close() }()

	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false
	}

	must(t, err)
	must(t, unix.Flock(int(f.Fd()), unix.LOCK_UN))

	return true
}

// A cancel of a real job whose verb ignores it, run through the binary
// that serves job exec, kills the verb and the child it started once the
// cancel budget passes. A process in another group, which the test
// started, lives on.
func TestJobExecCancelKillsTheProcessGroup(t *testing.T) {
	t.Parallel()

	spared := startGroupChild(t, "echo")
	spared.echo(t, "ping")

	dir := t.TempDir()
	lock := filepath.Join(dir, "child.lock")
	ready := filepath.Join(dir, "child.ready")

	// The ready file names the child's pid and the verb's group, which the
	// run made for this test. A child that still holds its lock is still in
	// that group, so the cleanup kills the group only then, and the test
	// leaves nothing behind when it fails.
	var childPID, group int

	t.Cleanup(func() {
		if group > 0 && !lockFree(t, lock) {
			_ = unix.Kill(-group, unix.SIGKILL)
		}
	})

	root := spoolRoot(t)
	store := storeWith(t, liveRecord())
	s := Spool{Dir: filepath.Join(root, "job-0001")}

	rc := newRun(t, store, root, signedRequest(t, goodRequest()))
	rc.opts.start = reexec{env: []string{
		helperVerbEnv + "=tree",
		helperLockFileEnv + "=" + lock,
		helperReadyFileEnv + "=" + ready,
	}}.start

	oc := &outcomes{}
	cancel := cancelRun(t, store, root)
	cancel.opts.CancelOutcome = oc.report

	cancelled := make(chan error, 1)

	rc.clock.onWait = func(n int) {
		if n != 1 {
			return
		}

		waitFor(t, func() bool {
			b, err := os.ReadFile(ready) //nolint:gosec // a file in the test's own directory
			if err != nil {
				return false
			}

			_, err = fmt.Sscanf(string(b), "%d %d", &childPID, &group)

			return err == nil
		})

		go func() { cancelled <- cancel.run(t) }()

		waitFor(t, func() bool {
			_, ok, _ := s.readCancel()

			return ok
		})
	}

	if err := rc.run(t); err == nil {
		t.Fatal("the killed job reported success")
	}

	if group <= 0 || childPID <= 0 {
		t.Fatal("the verb's child never got ready")
	}

	if group == os.Getpid() || group == unix.Getpgrp() || group == spared.g.cmd.Process.Pid {
		t.Fatalf("the verb's child is in group %d, which is not the verb's own", group)
	}

	waitFor(t, func() bool { return lockFree(t, lock) })

	if !lockFree(t, lock) {
		t.Fatalf("the verb's child, process %d, still holds its lock", childPID)
	}

	// Signal 0 delivers nothing. It only asks whether the group the run
	// made still has a member.
	waitFor(t, func() bool { return errors.Is(unix.Kill(-group, 0), unix.ESRCH) })

	spared.echo(t, "still here")

	recs := records(t, rc.stdout.Bytes())

	want := []progress.Kind{progress.KindHeader, progress.KindEvent, progress.KindLog, progress.KindEvent, progress.KindError, progress.KindResult}
	if got := streamKindsOf(t, recs); !slices.Equal(got, want) {
		t.Fatalf("record kinds = %v, want %v:\n%s", got, want, rc.stdout)
	}

	if w := bodyOf[progress.Event](t, recs[3]); w.Kind != progress.EventWarning || !slices.Equal(w.Undone, []progress.UndoneWork{{WorkRef: progress.WorkRef{Kind: "helper-undo", ID: "1"}, State: progress.UndoNotRun}}) {
		t.Fatalf("warning = %+v, want one that lists the undo hook that did not run", w)
	}

	if e := bodyOf[progress.ErrorBody](t, recs[4]); e.Code != progress.CodeJobCancelled {
		t.Fatalf("error = %+v, want %s", e, progress.CodeJobCancelled)
	}

	if r := bodyOf[progress.ResultBody](t, recs[5]); r.Cancel != progress.CancelKilled || r.ExitCode == 0 {
		t.Fatalf("result = %+v, want a failure whose cancel is killed", r)
	}

	select {
	case err := <-cancelled:
		if err == nil {
			t.Fatal("the cancel of a killed job reported success")
		}
	case <-time.After(verbWait):
		t.Fatal("the cancel did not end")
	}

	if got, want := oc.all(), []string{"job-0001 killed"}; !slices.Equal(got, want) {
		t.Fatalf("outcomes = %q, want %q", got, want)
	}
}
