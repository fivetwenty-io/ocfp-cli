package commands

import (
	"errors"
	"io"
	"os/exec"
	"time"

	bastionssh "github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
)

// stderrTailLimit bounds how much of a command's stderr is kept for the
// host key check. ssh prints its refusal last, so the tail is enough.
const stderrTailLimit = 8 << 10

// stderrPipeDrain bounds how long a finished command's stderr may stay
// open, for example held by a background ssh it started, before the copy
// to the terminal is cut off.
const stderrPipeDrain = 2 * time.Second

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
// and the tail shows ssh refusing the host key, the error is the mismatch
// error for bastionHost, with how to clear the stale entry. An empty
// bastionHost skips the check. cmd's stdin and stdout are left as the
// caller set them, so an interactive session still has its terminal.
func runShowingStderr(cmd *exec.Cmd, terminal io.Writer, bastionHost string, refusalExit func(code int) bool) error {
	tail := newStderrTail()

	cmd.Stderr = io.MultiWriter(terminal, tail)
	cmd.WaitDelay = stderrPipeDrain

	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		// The command succeeded and only its stderr stayed open.
		return nil
	}

	if err == nil || bastionHost == "" {
		return err
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || !refusalExit(exitErr.ExitCode()) {
		return err
	}

	hostKeyErr := bastionssh.SystemSSHHostKeyFailure(tail.String(), bastionHost, bastionssh.DefaultSSHPort, err)
	if hostKeyErr != nil {
		return hostKeyErr
	}

	return err
}
