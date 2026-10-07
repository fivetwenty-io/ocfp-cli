package provision

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	// fakeExecBusyTimeout bounds how long a freshly written fake executable
	// may keep failing to exec with "text file busy".
	fakeExecBusyTimeout = 2 * time.Second

	// fakeExecBusyInterval is the pause between those attempts.
	fakeExecBusyInterval = 2 * time.Millisecond

	// fakeProbeGuard is inserted after a fake's shebang so the probe run in
	// writeFakeExecutableFile returns before the fake's own body, leaving
	// every file and log the body would touch alone.
	fakeProbeGuard = "[ \"${OCFP_FAKE_PROBE:-}\" = 1 ] && exit 0\n"
)

// retryWhileTextFileBusy calls run until it stops failing with ETXTBSY,
// pausing interval between attempts and giving up after timeout. Parallel
// tests fork while another test still holds the write descriptor of a script
// it has just written, and exec of that script then fails with ETXTBSY until
// the forked child execs and drops its copy of the descriptor. Any other
// outcome, including a non-zero exit or a different start error, means the
// exec got past the busy check, so it returns nil and leaves the real failure
// for the test's own run to report. It returns the last ETXTBSY error only
// when the deadline passes first.
func retryWhileTextFileBusy(run func() error, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)

	for {
		err := run()
		if !errors.Is(err, syscall.ETXTBSY) {
			return nil
		}

		if !time.Now().Before(deadline) {
			return err
		}

		time.Sleep(interval)
	}
}

// writeFakeExecutableFile writes content, which must start with a shebang
// line, to path as an executable and then runs it once, retrying while the
// exec reports "text file busy". The run sets OCFP_FAKE_PROBE=1 in an
// otherwise minimal environment and starts in an empty directory, and the
// guard added after the shebang makes the fake exit before its body, so the
// run has no side effects.
func writeFakeExecutableFile(t *testing.T, path, content string) {
	t.Helper()

	shebang, rest, found := strings.Cut(content, "\n")
	if !strings.HasPrefix(shebang, "#!") || !found {
		t.Fatalf("fake executable %s must start with a shebang line", path)
	}

	script := shebang + "\n" + fakeProbeGuard + rest

	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatalf("write fake executable %s: %v", path, err)
	}

	workDir := t.TempDir()

	err := retryWhileTextFileBusy(func() error {
		cmd := exec.Command(path)
		cmd.Dir = workDir
		cmd.Env = []string{"OCFP_FAKE_PROBE=1", "PATH=/usr/bin:/bin"}

		return cmd.Run()
	}, fakeExecBusyTimeout, fakeExecBusyInterval)
	if err != nil {
		t.Fatalf("fake executable %s still reports text file busy after %s: %v", path, fakeExecBusyTimeout, err)
	}
}
