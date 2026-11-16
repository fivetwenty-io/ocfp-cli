package jobexec

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/strictjson"
	"github.com/fivetwenty-io/ocfp-cli/internal/version"
)

// childExitTimeout bounds every wait for a helper process to exit.
const childExitTimeout = 10 * time.Second

// helperChild is this test binary started as `job verb`.
type helperChild struct {
	cmd    *exec.Cmd
	stdin  *os.File
	stdout *os.File
	lines  *bufio.Reader
	stderr *lockedBuffer
	done   chan struct{}
}

// lockedBuffer is a buffer that the process's stderr copier and the test
// can share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// startHelperChild starts this test binary as `job verb` with env added to
// its environment. The test's cleanup kills it if it is still running and
// always waits for it.
func startHelperChild(t *testing.T, env ...string) *helperChild {
	t.Helper()

	inR, inW, err := os.Pipe()
	must(t, err)

	outR, outW, err := os.Pipe()
	must(t, err)

	c := &helperChild{stdin: inW, stdout: outR, lines: bufio.NewReader(outR), stderr: &lockedBuffer{}, done: make(chan struct{})}

	c.cmd = exec.Command(os.Args[0], "job", "verb") //nolint:gosec,noctx // this test binary, waited for below
	c.cmd.Env = append(os.Environ(), env...)
	c.cmd.Stdin = inR
	c.cmd.Stdout = outW
	c.cmd.Stderr = c.stderr

	err = c.cmd.Start()

	_ = inR.Close()
	_ = outW.Close()

	if err != nil {
		t.Fatal(err)
	}

	go func() {
		_ = c.cmd.Wait()
		close(c.done)
	}()

	t.Cleanup(func() {
		select {
		case <-c.done:
		default:
			_ = c.cmd.Process.Kill()
			<-c.done
		}

		_ = c.stdin.Close()
		_ = c.stdout.Close()
	})

	return c
}

func (c *helperChild) send(t *testing.T, line string) {
	t.Helper()

	if _, err := c.stdin.WriteString(line + "\n"); err != nil {
		t.Fatalf("write %s: %v", line, err)
	}
}

// next reads one envelope line.
func (c *helperChild) next(t *testing.T) string {
	t.Helper()

	line, err := c.lines.ReadString('\n')
	if err != nil {
		t.Fatalf("read an envelope: %v (stderr %q)", err, c.stderr.String())
	}

	return strings.TrimSuffix(line, "\n")
}

// rest reads envelope lines to the end of stdout.
func (c *helperChild) rest(t *testing.T) []string {
	t.Helper()

	b, err := io.ReadAll(c.lines)
	if err != nil {
		t.Fatal(err)
	}

	return slices.Collect(func(yield func(string) bool) {
		for line := range strings.Lines(string(b)) {
			if !yield(strings.TrimSuffix(line, "\n")) {
				return
			}
		}
	})
}

// exitCode waits for the process and returns its exit code, which is -1 when
// a signal ended it.
func (c *helperChild) exitCode(t *testing.T) int {
	t.Helper()

	select {
	case <-c.done:
	case <-time.After(childExitTimeout):
		t.Fatalf("the verb process did not exit (stderr %q)", c.stderr.String())
	}

	return c.cmd.ProcessState.ExitCode()
}

func startLine(t *testing.T, c control) string {
	t.Helper()

	return string(mustMarshal(t, c))
}

func goodStart() control {
	return control{
		Type:           controlStart,
		Protocol:       verbProtocol,
		Version:        version.Get().Version,
		JobID:          "job-0001",
		Verb:           verbNoop,
		CancelBudgetMs: verbs[verbNoop].cancelBudget.Milliseconds(),
	}
}

func TestVerbChildSpeaksTheProtocol(t *testing.T) {
	t.Parallel()

	t.Run("noop", func(t *testing.T) {
		t.Parallel()

		c := startHelperChild(t)
		c.send(t, startLine(t, goodStart()))

		got := c.rest(t)

		if code := c.exitCode(t); code != 0 {
			t.Fatalf("exit code = %d, want 0 (stderr %q)", code, c.stderr.String())
		}

		if len(got) != 3 {
			t.Fatalf("envelopes = %q, want three records", got)
		}

		if want := `{"type":"record","kind":"event","body":{"kind":"step_started","step":"noop"}}`; got[0] != want {
			t.Errorf("envelope 0 = %s, want %s", got[0], want)
		}

		if want := `{"type":"record","kind":"event","body":{"kind":"step_finished","step":"noop","outcome":"succeeded"}}`; got[2] != want {
			t.Errorf("envelope 2 = %s, want %s", got[2], want)
		}

		var env envelope

		if err := strictjson.Decode([]byte(got[1]), &env); err != nil || env.Type != envelopeRecord || env.Kind != progress.KindLog {
			t.Fatalf("envelope 1 = %s (%v), want a log record", got[1], err)
		}

		var l progress.LogBody

		if err := strictjson.Decode(env.Body, &l); err != nil || l.Stream != "stdout" {
			t.Fatalf("log = %s (%v), want a line on stdout", env.Body, err)
		}

		var out noopOutput

		if err := strictjson.Decode([]byte(l.Line), &out); err != nil {
			t.Fatal(err)
		}

		host, err := os.Hostname()
		must(t, err)

		if out.Hostname != host || out.Version != version.Get().Version {
			t.Fatalf("noop output = %+v, want host %q and version %q", out, host, version.Get().Version)
		}
	})

	t.Run("a cancel line", func(t *testing.T) {
		t.Parallel()

		c := startHelperChild(t, helperVerbEnv+"=wait")
		c.send(t, startLine(t, goodStart()))

		if got := c.next(t); !strings.Contains(got, `"step_started","step":"noop"`) {
			t.Fatalf("first envelope = %s, want the step's start", got)
		}

		if got, want := c.next(t), `{"type":"undo","work":{"kind":"helper-undo","id":"1"},"state":"registered"}`; got != want {
			t.Fatalf("second envelope = %s, want %s", got, want)
		}

		c.send(t, `{"type":"cancel","remainingMs":3000}`)

		want := []string{
			`{"type":"record","kind":"event","body":{"kind":"step_started","step":"undo","message":"helper-undo 1"}}`,
			`{"type":"record","kind":"event","body":{"kind":"step_finished","step":"undo","message":"helper-undo 1","outcome":"succeeded"}}`,
			`{"type":"undo","work":{"kind":"helper-undo","id":"1"},"state":"done"}`,
			`{"type":"stopped"}`,
		}

		if got := c.rest(t); !slices.Equal(got, want) {
			t.Fatalf("envelopes after the cancel =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}

		if code := c.exitCode(t); code != 1 {
			t.Fatalf("exit code = %d, want 1 (stderr %q)", code, c.stderr.String())
		}
	})

	refusals := map[string]struct {
		line string
		why  string
	}{
		"another protocol": {line: startLine(t, func() control { c := goodStart(); c.Protocol = 2; return c }()), why: "protocol"},
		"an unknown verb":  {line: startLine(t, func() control { c := goodStart(); c.Verb = "rm"; return c }()), why: `"rm"`},
		"no budget":        {line: startLine(t, func() control { c := goodStart(); c.CancelBudgetMs = 0; return c }()), why: "budget"},
		"a cancel first":   {line: `{"type":"cancel","remainingMs":10}`, why: "start"},
		"not json":         {line: `start`, why: "start line"},
		"no start line":    {why: "start line"},
	}

	for name, tc := range refusals {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			c := startHelperChild(t)
			if tc.line != "" {
				c.send(t, tc.line)
			}

			must(t, c.stdin.Close())

			if got := c.rest(t); len(got) != 0 {
				t.Fatalf("a refused start wrote envelopes %q", got)
			}

			if code := c.exitCode(t); code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}

			if !strings.Contains(c.stderr.String(), tc.why) {
				t.Fatalf("stderr %q does not hold %q", c.stderr.String(), tc.why)
			}
		})
	}
}

// A wrapper that goes away closes the verb's stdin and stdout. The verb takes
// the end of its stdin as a cancel, stops at its next safe point, and runs
// its undo hooks, although nothing reads its stdout any longer.
func TestAVerbStopsWhenTheWrapperGoes(t *testing.T) {
	t.Parallel()

	undoFile := filepath.Join(t.TempDir(), "undone")

	c := startHelperChild(t, helperVerbEnv+"=wait", helperUndoFileEnv+"="+undoFile)
	c.send(t, startLine(t, goodStart()))

	c.next(t)
	c.next(t)

	must(t, c.stdin.Close())
	must(t, c.stdout.Close())

	if code := c.exitCode(t); code != 1 {
		t.Fatalf("exit code = %d (%s), want 1 (stderr %q)", code, c.cmd.ProcessState, c.stderr.String())
	}

	if got := readFile(t, undoFile); string(got) != version.Get().Version {
		t.Fatalf("the undo hook wrote %q", got)
	}
}

// A wrapper whose verb process is of another version, as when the binary
// was replaced since the wrapper started, fails the job cleanly. The verb
// does not run, and the stream says why.
func TestRunRefusesAChildOfAnotherVersion(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
	rc.opts.start = reexec{env: []string{helperVersionEnv + "=0.0.0-other"}}.start

	err := rc.run(t)
	if err == nil {
		t.Fatal("Run reported success for a verb process of another version")
	}

	if _, ok := progress.AsRefusal(err); ok {
		t.Fatalf("a verb process of another version is a refusal of the request: %v", err)
	}

	recs := records(t, rc.stdout.Bytes())

	want := []progress.Kind{progress.KindHeader, progress.KindLog, progress.KindResult}
	if got := streamKindsOf(t, recs); !slices.Equal(got, want) {
		t.Fatalf("record kinds = %v, want %v:\n%s", got, want, rc.stdout)
	}

	l := bodyOf[progress.LogBody](t, recs[1])
	if l.Stream != "stderr" || !strings.Contains(l.Line, "0.0.0-other") || !strings.Contains(l.Line, version.Get().Version) {
		t.Fatalf("log = %+v, want a stderr line that names both versions", l)
	}

	if r := bodyOf[progress.ResultBody](t, recs[2]); r.ExitCode != 1 {
		t.Fatalf("result exit code = %d, want 1", r.ExitCode)
	}

	wantExitShape(t, readFile(t, filepath.Join(root, "job-0001", "exit.json")), 1)
}

// The default starter runs the verb in a process of its own, from this
// binary, and the job's stream is what the in-process starter gives.
func TestRunStartsTheVerbInAProcess(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
	rc.opts.start = nil

	err := rc.run(t)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}

	recs := records(t, rc.stdout.Bytes())

	want := []progress.Kind{progress.KindHeader, progress.KindEvent, progress.KindLog, progress.KindEvent, progress.KindResult}
	if got := streamKindsOf(t, recs); !slices.Equal(got, want) {
		t.Fatalf("record kinds = %v, want %v:\n%s", got, want, rc.stdout)
	}

	var out noopOutput

	if err := json.Unmarshal([]byte(bodyOf[progress.LogBody](t, recs[2]).Line), &out); err != nil {
		t.Fatal(err)
	}

	host, err := os.Hostname()
	must(t, err)

	if out.Hostname != host {
		t.Fatalf("the verb read host %q, want this host's %q", out.Hostname, host)
	}

	if n := rc.host.calls.Load(); n != 0 {
		t.Fatalf("the wrapper's Hostname was called %d times, want 0", n)
	}
}

// errNoChild is what a starter that must not run returns.
var errNoChild = errors.New("no verb process")

// A starter that fails ends the run with an error and no result.
func TestRunReportsAStarterThatFails(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
	rc.opts.start = func(verbStart) (*verbProc, error) { return nil, errNoChild }

	err := rc.run(t)
	if !errors.Is(err, errNoChild) {
		t.Fatalf("Run = %v, want the starter's error", err)
	}

	wantNoPath(t, filepath.Join(root, "job-0001", "exit.json"))
}
