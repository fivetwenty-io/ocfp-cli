//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows)

package ssh

import "os"

// lockFile does nothing where no advisory file lock is available; writers
// there are not serialised.
func lockFile(_ *os.File) error { return nil }

func unlockFile(_ *os.File) error { return nil }
