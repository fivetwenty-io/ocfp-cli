package jobexec

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// scriptedProc is a verb process whose envelopes a test writes. It exits
// when the test says so or when the wrapper kills it.
type scriptedProc struct {
	out    *io.PipeWriter
	in     *io.PipeReader
	exited chan struct{}
	once   sync.Once
	kills  atomic.Int32
	code   int

	// stubborn makes the wrapper's close of stdout do nothing, as when a
	// close does not wake a read that is waiting on the pipe.
	stubborn bool

	// onKill, when set, runs at each kill before the process exits.
	onKill func()
}

// scriptedStart returns a starter whose process runs script on a goroutine
// of its own, and the process it starts. The process exits with code.
func scriptedStart(code int, script func(p *scriptedProc)) (func(verbStart) (*verbProc, error), *scriptedProc) {
	return scriptedStartOf(&scriptedProc{exited: make(chan struct{}), code: code}, script)
}

func scriptedStartOf(p *scriptedProc, script func(p *scriptedProc)) (func(verbStart) (*verbProc, error), *scriptedProc) {

	start := func(verbStart) (*verbProc, error) {
		pr, pw := io.Pipe()
		p.out, p.in = pw, pr

		go script(p)

		var stdout io.ReadCloser = pr
		if p.stubborn {
			stdout = io.NopCloser(pr)
		}

		return &verbProc{
			stdin:  nopWriteCloser{io.Discard},
			stdout: stdout,
			stderr: io.NopCloser(strings.NewReader("")),
			exited: p.exited,
			kill: func() error {
				p.kills.Add(1)

				if p.onKill != nil {
					p.onKill()
				}

				p.exit()

				return nil
			},
			reap: func() (int, error) {
				<-p.exited

				return p.code, nil
			},
		}, nil
	}

	return start, p
}

// exit ends the process. The pipe stays open, as when a process the verb
// started still holds it.
func (p *scriptedProc) exit() {
	p.once.Do(func() { close(p.exited) })
}

func (p *scriptedProc) write(line string) error {
	_, err := p.out.Write([]byte(line + "\n"))

	return err
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

const stepStartedEnvelope = `{"type":"record","kind":"event","body":{"kind":"step_started","step":"noop"}}`

func streamKindsOf(t *testing.T, recs []progress.Record) []progress.Kind {
	t.Helper()

	kinds := make([]progress.Kind, 0, len(recs))
	for _, r := range recs {
		kinds = append(kinds, r.Kind)
	}

	return kinds
}

// A verb that writes a record only the wrapper may write is killed, and the
// job ends with exit code 1 after a stderr log line that says why. The
// refused record never reaches the stream.
func TestTheWrapperRefusesAHeaderFromTheVerb(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"header", "error", "result", "question"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()

			start, p := scriptedStart(0, func(p *scriptedProc) {
				_ = p.write(stepStartedEnvelope)
				_ = p.write(`{"type":"record","kind":"` + kind + `","body":{}}`)

				<-p.exited
			})

			root := spoolRoot(t)
			rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
			rc.opts.start = start

			err := rc.run(t)
			if err == nil {
				t.Fatal("Run reported success for a verb that wrote a " + kind)
			}

			if _, ok := progress.AsRefusal(err); ok {
				t.Fatalf("a refused envelope is a refusal of the request: %v", err)
			}

			recs := records(t, rc.stdout.Bytes())

			want := []progress.Kind{progress.KindHeader, progress.KindEvent, progress.KindLog, progress.KindResult}
			if got := streamKindsOf(t, recs); !slices.Equal(got, want) {
				t.Fatalf("record kinds = %v, want %v:\n%s", got, want, rc.stdout)
			}

			if l := bodyOf[progress.LogBody](t, recs[2]); l.Stream != "stderr" || !strings.Contains(l.Line, kind) {
				t.Fatalf("log = %+v, want a stderr line that names the %s", l, kind)
			}

			if r := bodyOf[progress.ResultBody](t, recs[3]); r.ExitCode != 1 {
				t.Fatalf("result exit code = %d, want 1", r.ExitCode)
			}

			if n := p.kills.Load(); n != 1 {
				t.Fatalf("the verb was killed %d times, want 1", n)
			}

			wantExitShape(t, readFile(t, filepath.Join(root, "job-0001", "exit.json")), 1)
		})
	}
}

// A verb whose output stays open after it exits, as when a process it
// started still holds it, keeps writing while the wrapper finishes the job,
// and the wrapper's close of the pipe does not stop the relay's reads. The
// wrapper seals the relay before it writes the result, so no record of the
// verb's lands after it, in out or on stdout.
func TestNoVerbRecordFollowsTheSeal(t *testing.T) {
	t.Parallel()

	stopped := make(chan struct{})

	var wrote atomic.Int32

	p := &scriptedProc{exited: make(chan struct{}), stubborn: true}

	start, _ := scriptedStartOf(p, func(p *scriptedProc) {
		defer close(stopped)

		_ = p.write(stepStartedEnvelope)
		p.exit()

		for {
			if err := p.write(`{"type":"record","kind":"log","body":{"stream":"stdout","line":"late"}}`); err != nil {
				return
			}

			wrote.Add(1)
		}
	})

	root := spoolRoot(t)
	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
	rc.opts.start = start
	rc.opts.ioGrace = 20 * time.Millisecond

	err := rc.run(t)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}

	// Give a relay that was not sealed the time to write after the result,
	// and then end the verb's writes.
	time.Sleep(20 * time.Millisecond)

	_ = p.in.Close()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the verb could still write after the job ended")
	}

	if wrote.Load() == 0 {
		t.Fatal("the verb wrote nothing after it exited, so the test proves nothing")
	}

	out := readFile(t, filepath.Join(root, "job-0001", "out"))
	if string(out) != rc.stdout.String() {
		t.Fatalf("out differs from stdout\nout:\n%s\nstdout:\n%s", out, rc.stdout)
	}

	recs := records(t, out)
	last := recs[len(recs)-1]

	if last.Kind != progress.KindResult {
		t.Fatalf("the last record is a %s, want the result", last.Kind)
	}

	for i, r := range recs {
		if r.Seq != int64(i) {
			t.Fatalf("record %d has seq %d", i, r.Seq)
		}

		if r.Kind == progress.KindResult && i != len(recs)-1 {
			t.Fatalf("record %d is a result before the end", i)
		}
	}
}

// The relay refuses envelopes that are not records, undo reports, or the
// stopped mark, and it keeps the undo hooks still registered.
func TestTheRelayChecksEveryEnvelope(t *testing.T) {
	t.Parallel()

	bad := []string{
		`not json`,
		`{"type":"record","kind":"event","body":{"kind":"step_started","step":"noop","extra":1}}`,
		`{"type":"record","kind":"log","body":{"stream":"stdout"},"work":{"kind":"x"}}`,
		`{"type":"record","kind":"event","body":{"kind":"warning","undone":[{"kind":"x","id":"1"}]}}`,
		`{"type":"record","kind":"event","body":{"kind":"warning","undone":[]}}`,
		`{"type":"undo","state":"registered"}`,
		`{"type":"undo","work":{"kind":"","id":"x"},"state":"registered"}`,
		`{"type":"undo","work":{"kind":"x","name":"y"},"state":"registered"}`,
		`{"type":"undo","work":{"kind":"x"},"state":"lost"}`,
		`{"type":"stopped","work":{"kind":"x"}}`,
		`{"type":"surprise"}`,
		`{"type":"record","kind":"event","body":{"kind":"step_started","step":"noop"}} {}`,
	}

	for _, line := range bad {
		rl := &relay{enc: progress.NewEncoder(io.Discard, newFakeClock())}

		err := rl.envelope([]byte(line))

		var ref *refusedEnvelopeError
		if !errors.As(err, &ref) {
			t.Errorf("envelope %s = %v, want a refusal", line, err)
		}
	}

	rl := &relay{enc: progress.NewEncoder(io.Discard, newFakeClock())}

	for _, line := range []string{
		`{"type":"undo","work":{"kind":"task","id":"a"},"state":"registered"}`,
		`{"type":"undo","work":{"kind":"task","id":"b"},"state":"registered"}`,
		`{"type":"undo","work":{"kind":"lease","id":"c"},"state":"registered"}`,
		`{"type":"undo","work":{"kind":"lease","id":"b"},"state":"done"}`,
		`{"type":"undo","work":{"kind":"task","id":"b"},"state":"done"}`,
		`{"type":"stopped"}`,
	} {
		if err := rl.envelope([]byte(line)); err != nil {
			t.Fatalf("envelope %s = %v", line, err)
		}
	}

	if got, want := rl.pendingUndo(), []progress.WorkRef{{Kind: "task", ID: "a"}, {Kind: "lease", ID: "c"}}; !slices.Equal(got, want) {
		t.Fatalf("pending undo = %v, want %v", got, want)
	}

	if !rl.sawStopped() {
		t.Fatal("the relay missed the stopped mark")
	}
}

// A verb's stderr reaches the stream as log records on stderr, one per line.
func TestTheVerbsStderrIsRelayed(t *testing.T) {
	t.Parallel()

	var buf strings.Builder

	enc := progress.NewEncoder(&buf, newFakeClock())
	if err := enc.Header(progress.Header{Producer: producer}); err != nil {
		t.Fatal(err)
	}

	rl := &relay{enc: enc}
	rl.stderrLines(strings.NewReader("first\nsecond\r\nlast without newline"))

	recs := records(t, []byte(buf.String()))
	if len(recs) != 4 {
		t.Fatalf("stream has %d records, want the header and three logs:\n%s", len(recs), buf.String())
	}

	for i, want := range []string{"first", "second", "last without newline"} {
		if l := bodyOf[progress.LogBody](t, recs[i+1]); l.Stream != "stderr" || l.Line != want {
			t.Errorf("log %d = %+v, want %q on stderr", i, l, want)
		}
	}
}

// The child side writes each record as an envelope, and reports undo hooks
// as they are registered and done.
func TestTheChildWritesEnvelopes(t *testing.T) {
	t.Parallel()

	var buf strings.Builder

	rt := newJobRuntime(&envelopeWriter{w: &buf})

	if err := rt.Emit(progress.Event{Kind: progress.EventStepStarted, Step: "s"}); err != nil {
		t.Fatal(err)
	}

	if err := rt.Log("stdout", "hello"); err != nil {
		t.Fatal(err)
	}

	forget := rt.OnCancel(progress.WorkRef{Kind: "remote-task", ID: "task-1"}, func(context.Context) error { return nil })
	forget()
	forget()

	want := strings.Join([]string{
		`{"type":"record","kind":"event","body":{"kind":"step_started","step":"s"}}`,
		`{"type":"record","kind":"log","body":{"stream":"stdout","line":"hello"}}`,
		`{"type":"undo","work":{"kind":"remote-task","id":"task-1"},"state":"registered"}`,
		`{"type":"undo","work":{"kind":"remote-task","id":"task-1"},"state":"done"}`,
	}, "\n") + "\n"

	if buf.String() != want {
		t.Fatalf("envelopes =\n%s\nwant\n%s", buf.String(), want)
	}
}
