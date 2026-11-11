//go:build unix

package ssh

import (
	"os"
	"syscall"
)

// keepOwner gives f the owner and group recorded in info. Only root can
// give a file away, so for anyone else this succeeds only when the owner is
// already theirs; a failure leaves the file owned by the caller, which is
// the best that can be done.
func keepOwner(f *os.File, info os.FileInfo) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}

	_ = f.Chown(int(stat.Uid), int(stat.Gid))
}
