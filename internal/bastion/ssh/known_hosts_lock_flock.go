//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package ssh

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockFile(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX) //nolint:gosec // a file descriptor fits in an int
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func unlockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN) //nolint:gosec // a file descriptor fits in an int
}
