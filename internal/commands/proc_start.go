package commands

import (
	"errors"
	"time"
)

// ErrProcessStartTimeUnavailable is returned by processStartTime when the
// platform offers no way to read a process's start time, or when the
// kernel refuses to report it for the given PID.
var ErrProcessStartTimeUnavailable = errors.New("process start time unavailable")

// lockStartSlack is the tolerance applied when comparing a lock file's
// timestamp with the start time of the process now holding that PID.
// The lock is written by the ocfp process itself shortly after it starts,
// so the genuine holder always started before its lock's timestamp; the
// slack only absorbs clock resolution differences (Linux reports process
// start in whole clock ticks against a boot time rounded to the second).
const lockStartSlack = 2 * time.Second

// startedAfterLock reports whether the process now holding the PID
// started after the lock was written, which means the PID was recycled
// by an unrelated process and the lock's owner is gone. A missing lock
// timestamp or an unreadable start time both resolve to false: the
// caller falls back to plain liveness, the conservative answer.
func startedAfterLock(pid int, lockedAt time.Time) bool {
	if lockedAt.IsZero() {
		return false
	}

	started, err := processStartTime(pid)
	if err != nil {
		return false
	}

	return started.After(lockedAt.Add(lockStartSlack))
}
