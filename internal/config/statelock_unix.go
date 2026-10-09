//go:build !windows

package config

import (
	"errors"
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

	release, err := lockFile(file, path, timeout, syscall.Flock)
	if err != nil {
		_ = file.Close()

		return nil, err
	}

	return release, nil
}

// lockFile takes an exclusive lock on file with flock, and its release
// unlocks and closes the file.
//
// Only EWOULDBLOCK means that another process holds the lock, so only it is
// retried until timeout, and an interrupted call is simply made again. Any
// other error, such as ENOLCK from a filesystem that can't lock, won't clear
// by waiting, so it is returned at once rather than reported as a timeout
// that blames a holder that isn't there.
func lockFile(file *os.File, path string, timeout time.Duration, flock func(fd, how int) error) (func(), error) {
	fd := int(file.Fd())
	deadline := time.Now().Add(timeout)

	for {
		lockErr := flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)

		switch {
		case lockErr == nil:
			return func() {
				_ = flock(fd, syscall.LOCK_UN)
				_ = file.Close()
			}, nil
		case errors.Is(lockErr, syscall.EINTR):
			continue
		case !errors.Is(lockErr, syscall.EWOULDBLOCK):
			return nil, fmt.Errorf("failed to lock %s: %w", path, lockErr)
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w after %s: %s", ErrStateLockTimeout, timeout, path)
		}

		time.Sleep(stateLockRetryInterval)
	}
}
