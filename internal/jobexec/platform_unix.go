//go:build unix

package jobexec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// checkOwnedPrivate checks that info, the result of an Lstat of path, is
// owned by this process's effective user and not writable by group or other.
func checkOwnedPrivate(info fs.FileInfo, what, path string) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("spool: cannot read the owner of %s %s", what, path)
	}

	if uid := os.Geteuid(); int64(st.Uid) != int64(uid) {
		return fmt.Errorf("spool: %s %s is owned by uid %d, not this user's uid %d", what, path, st.Uid, uid)
	}

	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("spool: %s %s has mode %04o and is writable by group or other", what, path, perm)
	}

	return nil
}

// syncDir makes a rename in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // dir is the job's own spool directory
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

// lockOut takes the exclusive lock on out that marks its run as live. It does
// not wait, because the run has only just made out and nothing else can hold
// the lock yet.
func lockOut(f *os.File) error {
	err := flock(f, unix.LOCK_EX|unix.LOCK_NB)
	if err != nil {
		return fmt.Errorf("spool: lock %s: %w", outFile, err)
	}

	return nil
}

// outHeld reports whether another open file holds the lock on out, which f
// is open on. It takes a shared lock without waiting, and gives it back at
// once when it gets it.
func outHeld(f *os.File) (bool, error) {
	err := flock(f, unix.LOCK_SH|unix.LOCK_NB)

	switch {
	case err == nil:
	case errors.Is(err, unix.EWOULDBLOCK):
		return true, nil
	default:
		return false, fmt.Errorf("spool: check the lock on %s: %w", outFile, err)
	}

	err = flock(f, unix.LOCK_UN)
	if err != nil {
		return false, fmt.Errorf("spool: unlock %s: %w", outFile, err)
	}

	return false, nil
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

	return ferr
}
