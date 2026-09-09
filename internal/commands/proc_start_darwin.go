//go:build darwin

package commands

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// processStartTime returns the wall-clock time at which the process with
// the given PID started, read from the kernel's kinfo_proc record.
func processStartTime(pid int) (time.Time, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: pid %d: %w", ErrProcessStartTimeUnavailable, pid, err)
	}

	tv := kp.Proc.P_starttime

	return time.Unix(tv.Sec, int64(tv.Usec)*int64(time.Microsecond)), nil
}
