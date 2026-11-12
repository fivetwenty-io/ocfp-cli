package ownerrecord

import (
	"fmt"
	"os"
)

// SetLockWaitHook makes s call f each time it finds its lock taken and is
// about to wait for it.
func SetLockWaitHook(s *DirStore, f func()) {
	s.onWait = f
}

// SetTempWriter makes s write the temporary record file with w.
func SetTempWriter(s *DirStore, w func(f *os.File, b []byte) (int, error)) {
	s.writeTemp = w
}

// TryLockExclusive tries once, without waiting, to take an exclusive lock on
// the lock file at path. It reports whether the lock was granted and, when it
// was, returns its release.
func TryLockExclusive(path string) (func() error, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, fmt.Errorf("open lock file: %w", err)
	}

	ok, err := tryLock(f, lockExclusive)
	if err != nil || !ok {
		_ = f.Close()

		return nil, false, err
	}

	return func() error {
		uerr := unlock(f)
		cerr := f.Close()

		if uerr != nil {
			return uerr
		}

		return cerr
	}, true, nil
}

// TryLockShared tries once, without waiting, to take a shared lock on the
// lock file at path, gives it straight back when it is granted, and reports
// whether it was.
func TryLockShared(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("open lock file: %w", err)
	}

	defer func() { _ = f.Close() }()

	ok, err := tryLock(f, lockShared)
	if ok {
		err = unlock(f)
	}

	return ok, err
}

// SetDirSyncer makes s flush a directory with f in place of syncDir.
func SetDirSyncer(s *DirStore, f func(dir string) error) {
	s.dirSync = f
}

// SyncDir is the platform's directory flush, for a test's syncer to call.
func SyncDir(dir string) error {
	return syncDir(dir)
}

// SetEUID makes s check ownership as if it ran as the user euid.
func SetEUID(s *DirStore, euid int) {
	s.euid = func() int { return euid }
}
