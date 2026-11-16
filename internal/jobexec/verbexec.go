package jobexec

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
)

// reexec starts each verb in a process of its own, from the binary that is
// running, as `ocfp job verb`.
type reexec struct {
	// env is added to the environment the verb process inherits.
	env []string
}

// selfExecutable is the path of the binary that is running. On Linux it is
// /proc/self/exe, which names the binary this process runs even when its
// path has since been replaced or removed.
func selfExecutable() (string, error) {
	if runtime.GOOS == "linux" {
		return "/proc/self/exe", nil
	}

	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find this binary: %w", err)
	}

	return path, nil
}

// start starts the verb process. The process gets only its three pipes:
// every file the wrapper holds, out and the owner record's lock file among
// them, is opened close-on-exec and so stays with the wrapper.
func (r reexec) start(verbStart) (*verbProc, error) {
	path, err := selfExecutable()
	if err != nil {
		return nil, err
	}

	var parent, child []*os.File

	closeAll := func(fs []*os.File) {
		for _, f := range fs {
			_ = f.Close()
		}
	}

	for range 3 {
		pr, pw, err := os.Pipe()
		if err != nil {
			closeAll(parent)
			closeAll(child)

			return nil, fmt.Errorf("make a pipe for the verb: %w", err)
		}

		if len(parent) == 0 {
			parent, child = append(parent, pw), append(child, pr)
		} else {
			parent, child = append(parent, pr), append(child, pw)
		}
	}

	cmd := exec.Command(path, "job", "verb") //nolint:gosec,noctx // this binary; the wrapper ends the process itself

	cmd.Env = append(os.Environ(), r.env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = child[0], child[1], child[2]

	err = cmd.Start()

	closeAll(child)

	if err != nil {
		closeAll(parent)

		return nil, fmt.Errorf("start the verb process: %w", err)
	}

	return verbProcOf(cmd, parent[0], parent[1], parent[2]), nil
}

// waitedProc is a started verb process that a goroutine waits for.
type waitedProc struct {
	cmd     *exec.Cmd
	exited  chan struct{}
	waitErr error
}

// verbProcOf waits for cmd on a goroutine of its own and returns the verb
// process that the wrapper's end of each pipe and cmd make.
func verbProcOf(cmd *exec.Cmd, stdin io.WriteCloser, stdout, stderr io.ReadCloser) *verbProc {
	w := &waitedProc{cmd: cmd, exited: make(chan struct{})}

	go func() {
		defer close(w.exited)

		w.waitErr = cmd.Wait()
	}()

	return &verbProc{
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
		exited: w.exited,
		kill:   w.kill,
		reap:   w.reap,
	}
}

func (w *waitedProc) kill() error {
	err := w.cmd.Process.Kill()
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill the verb process: %w", err)
	}

	return nil
}

func (w *waitedProc) reap() (int, error) {
	<-w.exited

	var exitErr *exec.ExitError

	if w.waitErr != nil && !errors.As(w.waitErr, &exitErr) {
		return 0, fmt.Errorf("wait for the verb process: %w", w.waitErr)
	}

	return w.cmd.ProcessState.ExitCode(), nil
}
