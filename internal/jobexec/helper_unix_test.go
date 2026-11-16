//go:build unix

package jobexec

import (
	"context"
	"encoding/json"

	"golang.org/x/sys/unix"
)

// fileID names an open file by its device and inode.
type fileID struct {
	Dev uint64 `json:"dev"`
	Ino uint64 `json:"ino"`
}

// maxProbedFD is the highest descriptor runFDsVerb looks at.
const maxProbedFD = 1024

// runFDsVerb logs, on stdout, the device and inode of every regular file
// that the process has open on a descriptor above stderr.
func runFDsVerb(_ context.Context, j Job, _ func() (string, error)) (int, error) {
	ids := []fileID{}

	for fd := 3; fd < maxProbedFD; fd++ {
		var st unix.Stat_t

		if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
			continue
		}

		ids = append(ids, fileID{Dev: uint64(st.Dev), Ino: st.Ino}) //nolint:gosec,unconvert // the types differ by system
	}

	line, err := json.Marshal(ids)
	if err != nil {
		return 0, err
	}

	return 0, j.Log("stdout", string(line))
}
