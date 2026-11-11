//go:build windows

package ssh

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockedRange is the byte range locked in the lock file. Its size does not
// matter, as long as every writer locks the same range.
const lockedRange = 1

func lockFile(file *os.File) error {
	var overlapped windows.Overlapped

	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, lockedRange, 0, &overlapped)
}

func unlockFile(file *os.File) error {
	var overlapped windows.Overlapped

	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, lockedRange, 0, &overlapped)
}
