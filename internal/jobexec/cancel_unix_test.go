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

// procIDs are the ids of the test's own process, its group, its parent, and
// its parent's group, none of which a cleanup may signal.
type procIDs struct {
	pid, pgrp, ppid, parentPgrp int
}

// ourIDs returns the test process's own ids.
func ourIDs() procIDs {
	parentPgrp, err := unix.Getpgid(os.Getppid())
	if err != nil {
		parentPgrp = -1
	}

	return procIDs{pid: os.Getpid(), pgrp: unix.Getpgrp(), ppid: os.Getppid(), parentPgrp: parentPgrp}
}

// cleanupTarget returns what a cleanup signals to end the verb's child, and
// false when nothing is safe to signal. That is the negated group when group
// is the one the run made, which the verb leads, and which is none of init's,
// ours, or our parent's. Otherwise it is the child's pid alone, when that is
// none of init, us, or our parent.
func cleanupTarget(child, group, verb int, us procIDs) (int, bool) {
	if group > 1 && group == verb && group != us.pid && group != us.pgrp && group != us.parentPgrp {
		return -group, true
	}

	if child > 1 && child != us.pid && child != us.ppid {
		return child, true
	}

	return 0, false
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

	// The ready file names the child's pid, its group, and its parent,
	// which is the verb. The run made that group for this test, led by the
	// verb. A child that still holds its lock is still alive, so the
	// cleanup signals only then, and only what cleanupTarget allows, so the
	// test leaves nothing behind when it fails and never signals wider.
	var childPID, group, verbPID int

	t.Cleanup(func() {
		if childPID <= 0 || lockFree(t, lock) {
			return
		}

		if target, ok := cleanupTarget(childPID, group, verbPID, ourIDs()); ok {
			_ = unix.Kill(target, unix.SIGKILL)
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

			_, err = fmt.Sscanf(string(b), "%d %d %d", &childPID, &group, &verbPID)

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

	if _, ok := cleanupTarget(childPID, group, verbPID, ourIDs()); !ok || group != verbPID || group == spared.g.cmd.Process.Pid {
		t.Fatalf("the verb's child is in group %d, which is not the verb's own, %d", group, verbPID)
	}

	waitFor(t, func() bool { return lockFree(t, lock) })

	if !lockFree(t, lock) {
		t.Fatalf("the verb's child, process %d, still holds its lock", childPID)
	}

	// Every process in the group the run made has exited. A member that
	// has exited but is not yet reaped does not count, since its reaper
	// is the host's and not the run's.
	var (
		live    []string
		liveErr error
	)

	waitFor(t, func() bool {
		live, liveErr = liveGroupMembers(group)

		return liveErr == nil && len(live) == 0
	})

	if liveErr != nil || len(live) > 0 {
		t.Fatalf("group %d still has live members %v (%v)", group, live, liveErr)
	}

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

// The cleanup signals the group the run made for the verb, which the verb
// leads, and nothing wider. A group that is init's, ours, or our parent's,
// or that the verb does not lead, is refused, and the cleanup falls back to
// the child alone when that is safe.
func TestTheCleanupSignalsOnlyWhatTheRunMade(t *testing.T) {
	t.Parallel()

	us := procIDs{pid: 500, pgrp: 400, ppid: 300, parentPgrp: 200}

	for name, tc := range map[string]struct {
		child, group, verb int
		want               int
		ok                 bool
	}{
		"the verb's group":         {child: 702, group: 701, verb: 701, want: -701, ok: true},
		"no group":                 {child: 702, group: 0, verb: 701, want: 702, ok: true},
		"init's group":             {child: 702, group: 1, verb: 1, want: 702, ok: true},
		"our group":                {child: 702, group: 400, verb: 400, want: 702, ok: true},
		"our parent's group":       {child: 702, group: 200, verb: 200, want: 702, ok: true},
		"our pid as a group":       {child: 702, group: 500, verb: 500, want: 702, ok: true},
		"a group the verb not led": {child: 702, group: 650, verb: 701, want: 702, ok: true},
		"no child":                 {child: 0, group: 400, verb: 400},
		"init as the child":        {child: 1, group: 400, verb: 400},
		"us as the child":          {child: 500, group: 400, verb: 400},
		"our parent as the child":  {child: 300, group: 400, verb: 400},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, ok := cleanupTarget(tc.child, tc.group, tc.verb, us)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("cleanupTarget(%d, %d, %d) = %d, %v; want %d, %v", tc.child, tc.group, tc.verb, got, ok, tc.want, tc.ok)
			}
		})
	}
}
