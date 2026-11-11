//go:build windows

package jobexec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

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
