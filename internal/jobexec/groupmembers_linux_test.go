//go:build linux

package jobexec

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// liveGroupMembers returns the processes in group that have not exited, as
// "pid state" pairs. A zombie has exited and only waits for its parent to
// reap it. Once an orphan is adopted, that parent is whatever the host
// runs as its reaper, and the run cannot hurry it, so a zombie does not
// count.
func liveGroupMembers(group int) ([]string, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}

	var live []string

	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}

		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat")) //nolint:gosec // a fixed /proc path
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			continue
		}

		if err != nil {
			return nil, err
		}

		// The command name sits in parentheses and may hold spaces, so
		// the fields that follow start after the last ")". They are the
		// state, the parent's pid, and the group.
		s := string(b)

		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}

		f := strings.Fields(s[i+1:])
		if len(f) < 3 || f[2] != strconv.Itoa(group) {
			continue
		}

		if f[0] != "Z" && f[0] != "X" {
			live = append(live, e.Name()+" "+f[0])
		}
	}

	return live, nil
}
