package jobexec

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// groupChild is a helper process that startGroup started, with the test's
// ends of its stdin and stdout.
type groupChild struct {
	g     *groupProc
	stdin *os.File
	out   *bufio.Reader
	outR  *os.File
}

// startGroupChild starts this test binary as the plain helper mode names, in
// a group of its own. The test's cleanup kills that group, which is one the
// test made, and reaps its leader, even when the test fails.
func startGroupChild(t *testing.T, mode string) *groupChild {
	t.Helper()

	inR, inW, err := os.Pipe()
	must(t, err)

	outR, outW, err := os.Pipe()
	must(t, err)

	cmd := exec.Command(os.Args[0]) //nolint:gosec,noctx // this test binary, reaped below
	cmd.Env = append(os.Environ(), helperModeEnv+"="+mode)
	cmd.Stdin, cmd.Stdout = inR, outW

	g, err := startGroup(cmd)

	_ = inR.Close()
	_ = outW.Close()

	if err != nil {
		_ = inW.Close()
		_ = outR.Close()

		t.Fatal(err)
	}

	c := &groupChild{g: g, stdin: inW, out: bufio.NewReader(outR), outR: outR}

	t.Cleanup(func() {
		_ = c.g.kill()
		_ = c.stdin.Close()
		_, _ = c.g.reap()
		_ = c.outR.Close()
	})

	return c
}

// echo writes line to the helper and reads it back, which shows that the
// echo helper at the end of the pipes is running.
func (c *groupChild) echo(t *testing.T, line string) {
	t.Helper()

	if _, err := c.stdin.WriteString(line + "\n"); err != nil {
		t.Fatalf("write to the helper: %v", err)
	}

	got, err := readLineWithin(c.out, childExitTimeout)
	if err != nil || got != line+"\n" {
		t.Fatalf("the helper echoed %q (%v), want %q", got, err, line)
	}
}

// wantEOF waits for the helper's stdout to end, which happens only once every
// process that holds its write end has gone.
func (c *groupChild) wantEOF(t *testing.T) {
	t.Helper()

	got, err := readLineWithin(c.out, childExitTimeout)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("the helper's stdout gave %q (%v), want its end", got, err)
	}
}

var errReadTimedOut = errors.New("timed out")

// readLineWithin reads one line from br, and gives up after d.
func readLineWithin(br *bufio.Reader, d time.Duration) (string, error) {
	type result struct {
		line string
		err  error
	}

	ch := make(chan result, 1)

	go func() {
		line, err := br.ReadString('\n')
		ch <- result{line, err}
	}()

	select {
	case r := <-ch:
		return r.line, r.err
	case <-time.After(d):
		return "", errReadTimedOut
	}
}

// killsTheTree kills the group of a helper that has a child of its own, and
// checks that both have gone.
func killsTheTree(t *testing.T) {
	t.Helper()

	c := startGroupChild(t, "tree")
	c.echo(t, "ping")

	// stdin stays open, so only the kill can end the echo at the end of
	// the tree.
	must(t, c.g.kill())

	c.wantEOF(t)

	code, err := c.g.reap()
	must(t, err)

	// A signal ends a Unix verb, and a terminated Job Object gives its
	// processes the code 1.
	if want := runtime.GOOS == "windows"; (code == 1) != want || (code < 0) == want {
		t.Fatalf("reap = %d, want the code of a process that a kill ended", code)
	}
}

// Once the leader has been reaped, the group id it held can belong to
// another process, so neither a kill nor a second reap signals the group.
// The reap itself sweeps the group once, while it still holds the leader.
func TestNoGroupSignalAfterTheReap(t *testing.T) {
	t.Parallel()

	c := startGroupChild(t, "echo")
	c.echo(t, "ping")

	var signals atomic.Int32

	killGroup := c.g.killGroup
	c.g.killGroup = func() error {
		signals.Add(1)

		return killGroup()
	}

	must(t, c.stdin.Close())

	code, err := c.g.reap()
	must(t, err)

	if code != 0 {
		t.Fatalf("reap = %d, want 0", code)
	}

	if n := signals.Load(); n != 1 {
		t.Fatalf("the reap signalled the group %d times, want one sweep", n)
	}

	must(t, c.g.kill())
	must(t, c.g.kill())

	if _, err := c.g.reap(); err != nil {
		t.Fatalf("a second reap = %v", err)
	}

	if n := signals.Load(); n != 1 {
		t.Fatalf("the group was signalled %d times after the reap", n-1)
	}
}

// A kill before the reap signals the group, and a kill after it does not.
func TestAKillSignalsOnlyAnUnreapedGroup(t *testing.T) {
	t.Parallel()

	c := startGroupChild(t, "echo")

	var signals atomic.Int32

	killGroup := c.g.killGroup
	c.g.killGroup = func() error {
		signals.Add(1)

		return killGroup()
	}

	must(t, c.g.kill())

	if n := signals.Load(); n != 1 {
		t.Fatalf("a kill before the reap signalled the group %d times, want 1", n)
	}

	c.wantEOF(t)

	if _, err := c.g.reap(); err != nil {
		t.Fatal(err)
	}

	before := signals.Load()

	must(t, c.g.kill())

	if n := signals.Load(); n != before {
		t.Fatal("a kill after the reap signalled the group")
	}
}
