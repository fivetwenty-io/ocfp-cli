package commands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	bastionssh "github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
)

// stderrTailLimit bounds how much of a command's stderr is kept for the
// host key check. ssh prints its refusal last, so the tail is enough.
const stderrTailLimit = 8 << 10

// stderrDrainGrace is how long the copy of a finished command's stderr
// waits, idle, for more output before the run returns. Everything the
// command wrote before it exited is already in the pipe, so this only has
// to outlast scheduling. A background process can hold the pipe open, as
// "ssh -f" and a ProxyCommand do, and the copy carries on for it after
// the run returns.
const stderrDrainGrace = 200 * time.Millisecond

// stderrCopyBuffer is the size of each read from the stderr pipe.
const stderrCopyBuffer = 32 << 10

// stderrTail keeps the last stderrTailLimit bytes written to it.
type stderrTail struct {
	buf []byte
}

func newStderrTail() *stderrTail {
	return &stderrTail{buf: make([]byte, 0, stderrTailLimit)}
}

// Write keeps the newest bytes and never fails.
func (t *stderrTail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > stderrTailLimit {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-stderrTailLimit:]...)
	}

	return len(p), nil
}

func (t *stderrTail) String() string {
	return string(t.buf)
}

// sshRefusalExit reports whether an ssh exit status can be ssh's own
// refusal. ssh exits 255 for its own errors, and any other status is the
// remote command's, whose output may hold the same words.
func sshRefusalExit(code int) bool {
	return code == 255 //nolint:mnd // ssh's own error status
}

// transferRefusalExit reports whether an scp or rsync exit status can
// follow ssh's refusal. Both exit 255 when ssh fails, except scp in its
// legacy protocol, which exits 1.
func transferRefusalExit(code int) bool {
	return code == 255 || code == 1 //nolint:mnd // the statuses scp and rsync use when ssh fails
}

// runShowingStderr runs cmd with its stderr copied to terminal as it comes
// and its tail kept. When the run fails with a status refusalExit accepts
// and the tail shows ssh refusing the bastion's host key, the error is the
// mismatch error, with how to clear the stale entry. An empty bastionHost
// skips the check. cmd's stdin and stdout are left as the caller set them,
// so an interactive session still has its terminal.
func runShowingStderr(cmd *exec.Cmd, terminal io.Writer, bastionHost string, refusalExit func(code int) bool) error {
	tail := newStderrTail()

	err := runCopyingStderr(cmd, terminal, tail)
	if err == nil || bastionHost == "" {
		return err
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || !refusalExit(exitErr.ExitCode()) {
		return err
	}

	hostKeyErr := bastionssh.SystemSSHHostKeyFailure(tail.String(), bastionHost, err)
	if hostKeyErr != nil {
		return hostKeyErr
	}

	return err
}

// runCopyingStderr runs cmd with its stderr on a pipe of our own, copied
// to terminal and tail. It returns once cmd has exited and the pipe has
// been idle for stderrDrainGrace. The grace counts only while the copy
// waits to read, so a terminal slow to take output never loses any. If a
// background process still holds the pipe, the copy carries on to the
// terminal alone after the return, until that process closes it.
func runCopyingStderr(cmd *exec.Cmd, terminal, tail io.Writer) error {
	reader, writer, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("cannot capture stderr: %w", err)
	}

	cmd.Stderr = writer

	err = cmd.Start()

	// The child has its own copy of the write end now. Ours must go, or the
	// pipe never reaches end of file.
	_ = writer.Close()

	if err != nil {
		_ = reader.Close()

		return err //nolint:wrapcheck // the caller wraps the command's error
	}

	copier := &stderrCopier{src: reader, terminal: terminal, tail: tail} //nolint:exhaustruct // the flags start false
	drained := make(chan struct{})

	go copier.run(drained)

	err = cmd.Wait()

	copier.markExited()
	<-drained

	return err //nolint:wrapcheck // the caller wraps the command's error
}

// stderrCopier copies a command's stderr from a pipe. Once the command has
// exited, each read may wait at most stderrDrainGrace. A write to the
// terminal is never cut short.
type stderrCopier struct {
	src            *os.File
	terminal, tail io.Writer

	mu      sync.Mutex
	exited  bool
	reading bool
}

// markExited records that the command has exited and, if the copy is
// waiting to read, starts the grace for that read.
func (c *stderrCopier) markExited() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.exited = true
	if c.reading {
		_ = c.src.SetReadDeadline(time.Now().Add(stderrDrainGrace))
	}
}

func (c *stderrCopier) read(buf []byte) (int, error) {
	c.mu.Lock()
	c.reading = true

	if c.exited {
		_ = c.src.SetReadDeadline(time.Now().Add(stderrDrainGrace))
	}
	c.mu.Unlock()

	n, err := c.src.Read(buf)

	c.mu.Lock()
	c.reading = false
	c.mu.Unlock()

	return n, err //nolint:wrapcheck // read errors are only compared, never returned
}

// run copies until end of file, or until the command has exited and the
// pipe stays idle for the grace, and then closes drained. In the second
// case a background process still holds the pipe, so the copy carries on
// to the terminal alone, keeping the pipe open for that process's writes.
func (c *stderrCopier) run(drained chan<- struct{}) {
	buf := make([]byte, stderrCopyBuffer)

	for {
		n, err := c.read(buf)
		if n > 0 {
			// A failing terminal must not stop the copy, or the command
			// would block on a full pipe, and the tail is still needed.
			_, _ = c.terminal.Write(buf[:n])
			_, _ = c.tail.Write(buf[:n])
		}

		if err == nil {
			continue
		}

		close(drained)

		if errors.Is(err, os.ErrDeadlineExceeded) {
			_ = c.src.SetReadDeadline(time.Time{})
			_, _ = io.Copy(c.terminal, c.src)
		}

		_ = c.src.Close()

		return
	}
}
