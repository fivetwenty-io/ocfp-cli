//go:build unix && !linux

package jobexec

import (
	"errors"
	"strconv"

	"golang.org/x/sys/unix"
)

// liveGroupMembers returns a non-empty list while group has a member, as
// far as signal 0 can tell. Signal 0 delivers nothing.
func liveGroupMembers(group int) ([]string, error) {
	err := unix.Kill(-group, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil, nil
	}

	return []string{"group " + strconv.Itoa(group) + ": " + errString(err)}, nil
}

func errString(err error) string {
	if err == nil {
		return "a member answers"
	}

	return err.Error()
}
