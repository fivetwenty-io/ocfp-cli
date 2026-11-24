package jobexec

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
)

// VerbChildEnv marks a process that job exec started to run a verb. The verb
// process clears it before the verb runs, so only that process holds it. A
// test binary that sees it without having served the verb has been started
// by a job exec among its own tests, and must not run them again.
const VerbChildEnv = "OCFP_JOB_VERB_CHILD"

// reexec starts each verb in a process of its own, from the binary that is
// running, as `ocfp job verb`, with VerbChildEnv set.
type reexec struct {
	// env is added to the environment the verb process inherits.
	env []string
}

// passedEnv returns the entries of a job request's env that a verb process
// gets, as NAME=value pairs in name order. Only the names that a verb reads
// pass, so that a request cannot set PATH or anything else a process of ours
// trusts.
func passedEnv(env map[string]string) []string {
	var out []string

	for _, name := range []string{envGenesisEnv} {
		if v, ok := env[name]; ok {
			out = append(out, name+"="+v)
		}
	}

	return out
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

// start starts the verb process, in a group of its own. The process gets
// only its three pipes: every file the wrapper holds, out and the owner
// record's lock file among them, is opened close-on-exec and so stays with
// the wrapper.
func (r reexec) start(s verbStart) (*verbProc, error) {
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

	cmd.Env = append(append(os.Environ(), r.env...), passedEnv(s.env)...)
	cmd.Env = append(cmd.Env, VerbChildEnv+"=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = child[0], child[1], child[2]

	g, err := startGroup(cmd)

	closeAll(child)

	if err != nil {
		closeAll(parent)

		return nil, err
	}

	return &verbProc{
		stdin:  parent[0],
		stdout: parent[1],
		stderr: parent[2],
		exited: g.exited,
		kill:   g.kill,
		reap:   g.reap,
	}, nil
}

// groupProc is a verb process that leads a group of its own: a process group
// on Unix and a Job Object on Windows. Every process the verb starts joins
// the group, so one kill ends them all.
//
// A Unix process group id can belong to another group once the leader has
// been reaped. So the group is signaled only while the wrapper holds the
// leader unreaped, and the mutex makes that so: a kill signals only before
// the reap, and the reap sweeps the group one last time before it reaps.
type groupProc struct {
	cmd *exec.Cmd

	// exited is closed once the leader has exited, before it is reaped.
	exited chan struct{}

	// exitErr is set, before exited is closed, when the wait for the
	// leader's exit failed.
	exitErr error

	// killGroup kills every process in the group. It is called only with
	// mu held and the leader unreaped.
	killGroup func() error

	// release frees what the group holds once the leader is reaped.
	release func()

	mu      sync.Mutex
	reaped  bool
	code    int
	reapErr error
}

// newGroupProc returns the group that cmd, which has started, leads. It
// waits for the leader's exit with waitExit, which must not reap it.
func newGroupProc(cmd *exec.Cmd, waitExit func() error, killGroup func() error, release func()) *groupProc {
	g := &groupProc{cmd: cmd, exited: make(chan struct{}), killGroup: killGroup, release: release}

	go func() {
		defer close(g.exited)

		g.exitErr = waitExit()
	}()

	return g
}

// kill kills every process in the group, unless the leader has been reaped.
func (g *groupProc) kill() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.reaped {
		return nil
	}

	return g.killGroup()
}

// reap waits for the leader to exit, kills what is left of its group, and
// reaps it. It returns the leader's exit code, which is negative when a
// signal ended it. A second call returns what the first did.
func (g *groupProc) reap() (int, error) {
	<-g.exited

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.reaped {
		return g.code, g.reapErr
	}

	// A wait that failed did not see the leader exit, so the sweep is what
	// ends it. Either way the leader is unreaped, so the group is still the
	// verb's own.
	sweepErr := g.killGroup()
	waitErr := g.cmd.Wait()

	g.reaped = true

	if g.release != nil {
		g.release()
	}

	var exitErr *exec.ExitError

	switch {
	case g.exitErr != nil:
		g.reapErr = fmt.Errorf("wait for the verb process: %w", g.exitErr)
	case waitErr != nil && !errors.As(waitErr, &exitErr):
		g.reapErr = fmt.Errorf("reap the verb process: %w", waitErr)
	case sweepErr != nil:
		g.reapErr = sweepErr
	}

	g.code = g.cmd.ProcessState.ExitCode()

	return g.code, g.reapErr
}
