//go:build windows

package jobexec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

// checkOwnedPrivate refuses every spool path on Windows, which has no group
// and other mode bits to check. Until the spool can check a directory's
// access list, the job spool runs only on Unix.
func checkOwnedPrivate(_ fs.FileInfo, what, path string) error {
	return fmt.Errorf("spool: cannot check who may write %s %s, because the job spool runs only on Unix", what, path)
}

// syncDir does nothing on Windows, which cannot open a directory to flush
// it and makes a rename durable with the file system's own journal.
func syncDir(string) error {
	return nil
}

// The lock on out covers one byte far past any data out can hold, because a
// Windows lock also stops other handles from reading the bytes it covers.
const (
	outLockLow  = ^uint32(0)
	outLockHigh = ^uint32(0) >> 1
)

// lockOut takes the exclusive lock on out that marks its run as live. It does
// not wait, because the run has only just made out and nothing else can hold
// the lock yet.
func lockOut(f *os.File) error {
	err := lockFileEx(f, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY)
	if err != nil {
		return fmt.Errorf("spool: lock %s: %w", outFile, err)
	}

	return nil
}

// outHeld reports whether another handle holds the lock on out, which f is
// open on. It takes a shared lock without waiting, and gives it back at once
// when it gets it.
func outHeld(f *os.File) (bool, error) {
	err := lockFileEx(f, windows.LOCKFILE_FAIL_IMMEDIATELY)

	switch {
	case err == nil:
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return true, nil
	default:
		return false, fmt.Errorf("spool: check the lock on %s: %w", outFile, err)
	}

	err = withHandle(f, func(h windows.Handle) error {
		return windows.UnlockFileEx(h, 0, 1, 0, outLockRange())
	})
	if err != nil {
		return false, fmt.Errorf("spool: unlock %s: %w", outFile, err)
	}

	return false, nil
}

func lockFileEx(f *os.File, flags uint32) error {
	return withHandle(f, func(h windows.Handle) error {
		return windows.LockFileEx(h, flags, 0, 1, 0, outLockRange())
	})
}

func outLockRange() *windows.Overlapped {
	return &windows.Overlapped{Offset: outLockLow, OffsetHigh: outLockHigh}
}

// withHandle calls fn with f's handle.
func withHandle(f *os.File, fn func(windows.Handle) error) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return fmt.Errorf("lock file handle: %w", err)
	}

	var ferr error

	cerr := rc.Control(func(fd uintptr) {
		ferr = fn(windows.Handle(fd))
	})
	if cerr != nil {
		return fmt.Errorf("lock file handle: %w", cerr)
	}

	return ferr
}

// startGroup starts cmd in a Job Object of its own. Every process the verb
// starts after it joins the job joins it too, and the kill terminates the
// job, which acts on a handle the wrapper owns and so cannot reach another
// process. The job may neither be broken away from nor end when its last
// handle closes: a wrapper that dies leaves its verb to see the end of
// stdin and stop at its next safe point, as on Unix.
//
// The verb reads its start line before it does anything, and the wrapper
// writes that line only once startGroup has returned, so the verb starts no
// process before it is in the job, and it cannot exit and give up its pid
// between the start and the open of its handle.
func startGroup(cmd *exec.Cmd) (*groupProc, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("start the verb process: create a job object: %w", err)
	}

	err = cmd.Start()
	if err != nil {
		_ = windows.CloseHandle(job)

		return nil, fmt.Errorf("start the verb process: %w", err)
	}

	proc, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE,
		false,
		uint32(cmd.Process.Pid), //nolint:gosec // a pid fits in a DWORD
	)
	if err == nil {
		err = windows.AssignProcessToJobObject(job, proc)
		if err != nil {
			_ = windows.CloseHandle(proc)
		}
	}

	if err != nil {
		// The verb is still waiting for its start line and has started
		// nothing, so ending it alone ends the verb.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = windows.CloseHandle(job)

		return nil, fmt.Errorf("start the verb process: put it in its job object: %w", err)
	}

	killGroup := func() error {
		err := windows.TerminateJobObject(job, 1)
		if err != nil {
			return fmt.Errorf("terminate the verb's job object: %w", err)
		}

		return nil
	}

	waitExit := func() error {
		event, err := windows.WaitForSingleObject(proc, windows.INFINITE)
		if err != nil {
			return fmt.Errorf("wait for the verb process: %w", err)
		}

		if event != windows.WAIT_OBJECT_0 {
			return fmt.Errorf("wait for the verb process: wait returned %#x", event)
		}

		return nil
	}

	release := func() {
		_ = windows.CloseHandle(proc)
		_ = windows.CloseHandle(job)
	}

	return newGroupProc(cmd, waitExit, killGroup, release), nil
}
