//go:build unix

package ownerrecord

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// tryLock takes the lock on f without waiting and reports whether it was
// granted.
func tryLock(f *os.File, kind lockKind) (bool, error) {
	err := flock(f, kind.how()|unix.LOCK_NB)

	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.EWOULDBLOCK):
		return false, nil
	default:
		return false, err
	}
}

// waitLock takes the lock on f, waiting for as long as it is held elsewhere.
func waitLock(f *os.File, kind lockKind) error {
	return flock(f, kind.how())
}

// unlock gives back the lock on f.
func unlock(f *os.File) error {
	return flock(f, unix.LOCK_UN)
}

// syncDir makes a rename in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // dir is the store's own directory
	if err != nil {
		return fmt.Errorf("open directory: %w", err)
	}

	serr := d.Sync()
	cerr := d.Close()

	if serr != nil {
		return fmt.Errorf("sync directory: %w", serr)
	}

	if cerr != nil {
		return fmt.Errorf("close directory: %w", cerr)
	}

	return nil
}

func (k lockKind) how() int {
	if k == lockExclusive {
		return unix.LOCK_EX
	}

	return unix.LOCK_SH
}

// flock applies how to f's descriptor, retrying a call that a signal broke
// off.
func flock(f *os.File, how int) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return fmt.Errorf("lock file descriptor: %w", err)
	}

	var ferr error

	cerr := rc.Control(func(fd uintptr) {
		for {
			ferr = unix.Flock(int(fd), how) //nolint:gosec // a descriptor always fits in an int
			if !errors.Is(ferr, unix.EINTR) {
				return
			}
		}
	})
	if cerr != nil {
		return fmt.Errorf("lock file descriptor: %w", cerr)
	}

	if ferr != nil {
		return fmt.Errorf("flock: %w", ferr)
	}

	return nil
}
