//go:build unix

package jobexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
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

// treeChildWait bounds how long the tree verb waits for its child to get
// ready.
const treeChildWait = 10 * time.Second

// runTreeVerb starts the lock helper as a child, which shares its group,
// waits until the child holds its lock, registers an undo hook, and then
// ignores the cancel, so that only a kill of the group ends it and its
// child.
func runTreeVerb(_ context.Context, j Job, _ func() (string, error)) (int, error) {
	err := j.Emit(progress.Event{Kind: progress.EventStepStarted, Step: verbNoop})
	if err != nil {
		return 0, err
	}

	cmd := exec.Command(os.Args[0]) //nolint:gosec,noctx // this test binary, which the group kill ends
	cmd.Env = append(os.Environ(), helperModeEnv+"=lock")

	err = cmd.Start()
	if err != nil {
		return 0, err
	}

	ready := os.Getenv(helperReadyFileEnv)
	deadline := time.Now().Add(treeChildWait)

	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}

		if time.Now().After(deadline) {
			return 0, errors.New("the tree verb's child did not get ready")
		}

		time.Sleep(time.Millisecond)
	}

	j.OnCancel(progress.WorkRef{Kind: "helper-undo", ID: "1"}, func(context.Context) error { return nil })

	err = j.Log("stdout", fmt.Sprintf("child %d", cmd.Process.Pid))
	if err != nil {
		return 0, err
	}

	time.Sleep(helperSelfTimeout)

	return 0, nil
}

// holdLock takes an exclusive lock on the file helperLockFileEnv names,
// writes its pid, its process group id, and its parent's pid to the file
// helperReadyFileEnv names, and then holds the lock until it is killed or
// ends itself.
func holdLock() int {
	f, err := os.OpenFile(os.Getenv(helperLockFileEnv), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return 1
	}

	if unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return 1
	}

	ready := os.Getenv(helperReadyFileEnv)
	tmp := ready + ".tmp"

	if os.WriteFile(tmp, fmt.Appendf(nil, "%d %d %d", os.Getpid(), unix.Getpgrp(), os.Getppid()), 0o600) != nil || os.Rename(tmp, ready) != nil {
		return 1
	}

	time.Sleep(helperSelfTimeout)

	return 0
}
