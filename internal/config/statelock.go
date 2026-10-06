package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// stateLockTimeout bounds how long a command waits for the state file lock.
	// The critical section is a small read-modify-write, so anything near this
	// means a stuck or crashed holder. Failing is deliberate: six agents wedged
	// forever is worse than one clean error.
	stateLockTimeout = 10 * time.Second

	// stateLockRetryInterval is the poll interval while waiting for the lock.
	stateLockRetryInterval = 25 * time.Millisecond

	// stateLockFileMode is the permission for the lock file.
	stateLockFileMode os.FileMode = 0o600

	// fileLockDirMode is the permission for a directory WithFileLock creates
	// to hold its lock file.
	fileLockDirMode os.FileMode = 0o700
)

var (
	// ErrStateLockTimeout indicates the state file lock could not be acquired.
	ErrStateLockTimeout = errors.New("timed out waiting for the OCFP state file lock")

	// ErrFileLockTimeout indicates that another ocfp run held a WithFileLock
	// lock for longer than the caller was willing to wait.
	ErrFileLockTimeout = errors.New("timed out waiting for another ocfp run to release its lock")
)

// stateLockPath returns the path of the lock file guarding state.yml. It
// always sits alongside the resolved state.yml itself (see
// StateFilePath), so the lock and the file it guards never land in
// different directories, whichever side of the dual-read fallback
// StateFilePath resolved to.
func stateLockPath() string {
	statePath := StateFilePath()
	if statePath == "" {
		return ""
	}

	return statePath + ".lock"
}

// withStateLock runs fn while holding an exclusive lock on the state file.
//
// state.yml is a single file shared by every bloc, and each mutation is a
// load-modify-save. Without this lock, concurrent bootstraps for different
// blocs interleave inside that window and one bloc's entry — its SSH keys —
// is silently dropped, leaving that bloc keyless for every later command.
//
// The whole load-modify-save must run inside fn; locking only the write would
// not prevent the lost update.
func withStateLock(fn func() error) error {
	path := stateLockPath()
	if path == "" {
		// No OCFP home means no shared file to contend over.
		return fn()
	}

	err := os.MkdirAll(filepath.Dir(path), stateDirMode)
	if err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	release, err := acquireStateLock(path, stateLockTimeout)
	if err != nil {
		return err
	}

	defer release()

	return fn()
}

// WithFileLock runs fn while holding an exclusive lock on path, waiting up to
// timeout for another holder to release it. It creates the lock file and its
// directory when they are missing. The lock is the same one withStateLock
// takes, so on Unix the kernel releases it when its process exits and a
// killed ocfp never leaves a stale lock behind.
func WithFileLock(path string, timeout time.Duration, fn func() error) error {
	err := os.MkdirAll(filepath.Dir(path), fileLockDirMode)
	if err != nil {
		return fmt.Errorf("failed to create the directory for lock %s: %w", path, err)
	}

	release, err := acquireStateLock(path, timeout)
	if errors.Is(err, ErrStateLockTimeout) {
		return fmt.Errorf("%w after %s: %s", ErrFileLockTimeout, timeout, path)
	}

	if err != nil {
		return err
	}

	defer release()

	return fn()
}
