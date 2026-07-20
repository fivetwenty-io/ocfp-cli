//go:build windows

package config

import (
	"fmt"
	"os"
	"time"
)

// staleStateLockAge is how long an existing lock file may sit untouched before
// it is treated as abandoned. Windows has no flock equivalent that the OS
// releases on process death, so a crashed holder is reclaimed by age instead.
const staleStateLockAge = 2 * time.Minute

// acquireStateLock creates path exclusively, retrying until timeout.
func acquireStateLock(path string, timeout time.Duration) (func(), error) {
	deadline := time.Now().Add(timeout)

	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, stateLockFileMode)
		if err == nil {
			return func() {
				_ = file.Close()
				_ = os.Remove(path)
			}, nil
		}

		if !os.IsExist(err) {
			return nil, fmt.Errorf("failed to create state lock file: %w", err)
		}

		reclaimStaleStateLock(path)

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w after %s: %s", ErrStateLockTimeout, timeout, path)
		}

		time.Sleep(stateLockRetryInterval)
	}
}

// reclaimStaleStateLock removes a lock file left behind by a dead process.
func reclaimStaleStateLock(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}

	if time.Since(info.ModTime()) > staleStateLockAge {
		_ = os.Remove(path)
	}
}
