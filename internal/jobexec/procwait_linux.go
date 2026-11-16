//go:build linux

package jobexec

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// canWaitWithoutReaping reports whether waitWithoutReaping works here.
const canWaitWithoutReaping = true

// waitWithoutReaping waits for the child pid to exit and leaves it a zombie,
// so that its pid, and the group id it leads, stay its own until the reap.
func waitWithoutReaping(pid int) error {
	for {
		var info unix.Siginfo

		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}

		if err != nil {
			return fmt.Errorf("wait for process %d: %w", pid, err)
		}

		return nil
	}
}
