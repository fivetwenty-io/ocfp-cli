//go:build !darwin && !linux

package commands

import (
	"fmt"
	"time"
)

// processStartTime has no portable implementation on this platform, so
// lock liveness falls back to the plain PID-exists check.
func processStartTime(pid int) (time.Time, error) {
	return time.Time{}, fmt.Errorf("%w: pid %d: unsupported platform", ErrProcessStartTimeUnavailable, pid)
}
