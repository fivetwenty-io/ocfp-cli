//go:build !windows

package config

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// acquireStateLock takes an exclusive flock on path, retrying until timeout.
//
// flock is used rather than a lock file that must be deleted: the kernel drops
// it when the holding process exits, so an agent killed mid-bootstrap cannot
// wedge the other five behind a stale lock.
func acquireStateLock(path string, timeout time.Duration) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, stateLockFileMode) // #nosec G304 -- path is derived from OcfpHome
	if err != nil {
		return nil, fmt.Errorf("failed to open state lock file: %w", err)
	}

	deadline := time.Now().Add(timeout)

	for {
		lockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if lockErr == nil {
			return func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}, nil
		}

		if time.Now().After(deadline) {
			_ = file.Close()

			return nil, fmt.Errorf("%w after %s: %s", ErrStateLockTimeout, timeout, path)
		}

		time.Sleep(stateLockRetryInterval)
	}
}
