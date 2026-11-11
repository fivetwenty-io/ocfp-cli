//go:build windows

package ownerrecord

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// The lock covers the whole of the lock file's byte range, which is the
// same range for every caller.
const (
	lockLow  = ^uint32(0)
	lockHigh = ^uint32(0)
)

// tryLock takes the lock on f without waiting and reports whether it was
// granted.
func tryLock(f *os.File, kind lockKind) (bool, error) {
	err := lockFileEx(f, kind.flags()|windows.LOCKFILE_FAIL_IMMEDIATELY)

	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return false, nil
	default:
		return false, err
	}
}

// waitLock takes the lock on f, waiting for as long as it is held elsewhere.
func waitLock(f *os.File, kind lockKind) error {
	return lockFileEx(f, kind.flags())
}

// unlock gives back the lock on f.
func unlock(f *os.File) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return fmt.Errorf("lock file handle: %w", err)
	}

	var uerr error

	cerr := rc.Control(func(fd uintptr) {
		uerr = windows.UnlockFileEx(windows.Handle(fd), 0, lockLow, lockHigh, new(windows.Overlapped))
	})
	if cerr != nil {
		return fmt.Errorf("lock file handle: %w", cerr)
	}

	if uerr != nil {
		return fmt.Errorf("UnlockFileEx: %w", uerr)
	}

	return nil
}

// syncDir does nothing on Windows, which cannot open a directory to flush
// it and makes a rename durable with the file system's own journal.
func syncDir(string) error {
	return nil
}

func (k lockKind) flags() uint32 {
	if k == lockExclusive {
		return windows.LOCKFILE_EXCLUSIVE_LOCK
	}

	return 0
}

func lockFileEx(f *os.File, flags uint32) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return fmt.Errorf("lock file handle: %w", err)
	}

	var lerr error

	cerr := rc.Control(func(fd uintptr) {
		lerr = windows.LockFileEx(windows.Handle(fd), flags, 0, lockLow, lockHigh, new(windows.Overlapped))
	})
	if cerr != nil {
		return fmt.Errorf("lock file handle: %w", cerr)
	}

	if lerr != nil {
		return fmt.Errorf("LockFileEx: %w", lerr)
	}

	return nil
}
