package jobexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress/progresstest"
)

// verbWait bounds how long a test waits, in real time, for a verb to do what
// the test expects of it.
const verbWait = 10 * time.Second

// waitingVerb is a verb that runs in the test's process, registers one undo
// hook, and waits for a cancel. It counts the runs of its hook, and gives
// the test the process it runs in.
type waitingVerb struct {
	undos atomic.Int32

	// undoErr is what the hook returns.
	undoErr error

	mu   sync.Mutex
	proc *verbProc
}

func (w *waitingVerb) start(s verbStart) (*verbProc, error) {
	s.verb.run = func(_ context.Context, j Job, _ func() (string, error)) (int, error) {
		err := j.Emit(progress.Event{Kind: progress.EventStepStarted, Step: verbNoop})
		if err != nil {
			return 0, err
		}

		j.OnCancel(progress.WorkRef{Kind: "remote-task", ID: "task-1234"}, func(context.Context) error {
			w.undos.Add(1)

			return w.undoErr
		})

		select {
		case <-j.Done():
		case <-time.After(verbWait):
		}

		return 0, j.Checkpoint()
	}

	p, err := startInProcess(s)

	w.mu.Lock()
	w.proc = p
	w.mu.Unlock()

	return p, err
}

// waitExited waits for the verb's process to exit.
func (w *waitingVerb) waitExited(t *testing.T) {
	t.Helper()

	w.mu.Lock()
	p := w.proc
	w.mu.Unlock()

	if p == nil {
		t.Error("the verb has not started")

		return
	}

	select {
	case <-p.exited:
	case <-time.After(verbWait):
		t.Error("the verb did not exit")
	}
}

// stoppedRun runs a job whose verb waits for a cancel, writes the marker at
// the first wait, and lets each later wait last until the verb has exited.
func stoppedRun(t *testing.T) (*runCase, *waitingVerb, Spool, error) {
	t.Helper()

	return stoppedRunOf(t, &waitingVerb{})
}

// stoppedRunOf is stoppedRun for the verb w.
func stoppedRunOf(t *testing.T, w *waitingVerb) (*runCase, *waitingVerb, Spool, error) {
	t.Helper()

	root := spoolRoot(t)
	s := Spool{Dir: filepath.Join(root, "job-0001")}

	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
	rc.opts.start = w.start
	rc.clock.onWait = func(n int) {
		if n == 1 {
			_, err := s.WriteCancel(Cancel{RequestedAt: testNow})
			if err != nil {
				t.Error(err)
			}

			return
		}

		w.waitExited(t)
	}

	err := rc.run(t)

	return rc, w, s, err
}

// A cancel reaches the verb, which stops at its next safe point. Its undo
// hook runs once, and the stream ends with JOB_CANCELED and a result that
// says the job stopped.
func TestACancelStopsAtTheNextSafePoint(t *testing.T) {
	t.Parallel()

	rc, w, s, err := stoppedRun(t)
	if err == nil {
		t.Fatal("Run reported success for a cancelled job")
	}

	if _, ok := progress.AsRefusal(err); ok {
		t.Fatalf("a cancelled job is a refusal: %v", err)
	}

	if n := w.undos.Load(); n != 1 {
		t.Fatalf("the undo hook ran %d times, want once", n)
	}

	recs := records(t, rc.stdout.Bytes())

	want := []progress.Kind{progress.KindHeader, progress.KindEvent, progress.KindEvent, progress.KindEvent, progress.KindError, progress.KindResult}
	if got := streamKindsOf(t, recs); !slices.Equal(got, want) {
		t.Fatalf("record kinds = %v, want %v:\n%s", got, want, rc.stdout)
	}

	for i, step := range []progress.EventKind{progress.EventStepStarted, progress.EventStepFinished} {
		if ev := bodyOf[progress.Event](t, recs[2+i]); ev.Kind != step || ev.Step != stepUndo || ev.Message != "remote-task task-1234" {
			t.Fatalf("undo event %d = %+v, want %s for the hook", i, ev, step)
		}
	}

	if e := bodyOf[progress.ErrorBody](t, recs[4]); e.Code != progress.CodeJobCanceled || e.Message != "job job-0001 was cancelled and stopped at a safe point" {
		t.Fatalf("error = %+v, want JOB_CANCELED for a stop", e)
	}

	if r := bodyOf[progress.ResultBody](t, recs[5]); r.ExitCode != 1 || r.Cancel != progress.CancelStopped {
		t.Fatalf("result = %+v, want exit code 1 and cancel stopped", r)
	}

	if out := readFile(t, filepath.Join(s.Dir, outFile)); !bytes.Equal(out, rc.stdout.Bytes()) {
		t.Fatalf("out differs from stdout\nout:\n%s\nstdout:\n%s", out, rc.stdout)
	}
}

// A job that stops at a safe point with an undo hook that failed carries
// the hook's error on stderr, and then the warning that names the work left
// undone, before its error.
func TestAStopNamesTheHooksThatFailed(t *testing.T) {
	t.Parallel()

	rc, w, _, err := stoppedRunOf(t, &waitingVerb{undoErr: errors.New("boom")})
	if err == nil {
		t.Fatal("Run reported success for a cancelled job")
	}

	if n := w.undos.Load(); n != 1 {
		t.Fatalf("the undo hook ran %d times, want once", n)
	}

	recs := records(t, rc.stdout.Bytes())

	want := []progress.Kind{progress.KindHeader, progress.KindEvent, progress.KindEvent, progress.KindEvent, progress.KindLog, progress.KindEvent, progress.KindError, progress.KindResult}
	if got := streamKindsOf(t, recs); !slices.Equal(got, want) {
		t.Fatalf("record kinds = %v, want %v:\n%s", got, want, rc.stdout)
	}

	wantBody := `{"kind":"warning","step":"cancel","message":"undo hooks that failed: remote-task task-1234","undone":[{"kind":"remote-task","id":"task-1234","state":"failed"}]}`
	if string(recs[5].Body) != wantBody {
		t.Fatalf("warning = %s\nwant %s", recs[5].Body, wantBody)
	}

	if r := bodyOf[progress.ResultBody](t, recs[7]); r.ExitCode != 1 || r.Cancel != progress.CancelStopped {
		t.Fatalf("result = %+v, want exit code 1 and cancel stopped", r)
	}
}

// exit.json of a stopped job names the outcome and when the run first saw the
// marker, which is the clock's reading at the poll after the marker came.
func TestACancelledExitRecordsTheOutcome(t *testing.T) {
	t.Parallel()

	_, _, s, _ := stoppedRun(t)

	raw := readFile(t, filepath.Join(s.Dir, exitFile))

	wantKeysOf(t, raw, "exit-cancelled-v1.json")

	var e Exit

	decodeStrictFile(t, filepath.Join(s.Dir, exitFile), &e)

	seen := testNow.Add(pollInterval)

	if e.ExitCode != 1 || e.Cancel != progress.CancelStopped || e.CancelSeenAt == nil || !e.CancelSeenAt.Equal(seen) {
		t.Fatalf("exit = %+v, want exit code 1, cancel stopped, seen at %s", e, seen)
	}

	var st Status

	decodeStrictFile(t, filepath.Join(s.Dir, statusFile), &st)

	if st.State != StateExited || st.ExitCode == nil || *st.ExitCode != 1 {
		t.Fatalf("status = %+v, want exited with code 1", st)
	}
}

// A marker already in the spool when the run has written the header stops
// the job before its verb starts: no verb runs, no hook runs, and the stream
// ends with JOB_CANCELED and a stop.
func TestACancelBeforeTheVerbStartsRunsNothing(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	s := Spool{Dir: filepath.Join(root, "job-0001")}

	var starts atomic.Int32

	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
	rc.opts.start = func(verbStart) (*verbProc, error) {
		starts.Add(1)

		return nil, errors.New("no verb may start")
	}

	var once sync.Once

	// The first clock read once out exists is the start time, which comes
	// before the header.
	rc.clock.onNow = func() {
		if _, ok, err := s.openOut(); err != nil || !ok {
			return
		}

		once.Do(func() {
			if _, err := s.WriteCancel(Cancel{RequestedAt: testNow}); err != nil {
				t.Error(err)
			}
		})
	}

	err := rc.run(t)
	if err == nil {
		t.Fatal("Run reported success for a job cancelled before it started")
	}

	if n := starts.Load(); n != 0 {
		t.Fatalf("the verb was started %d times", n)
	}

	if n := rc.host.calls.Load(); n != 0 {
		t.Fatalf("Hostname was called %d times", n)
	}

	recs := records(t, rc.stdout.Bytes())

	want := []progress.Kind{progress.KindHeader, progress.KindError, progress.KindResult}
	if got := streamKindsOf(t, recs); !slices.Equal(got, want) {
		t.Fatalf("record kinds = %v, want %v:\n%s", got, want, rc.stdout)
	}

	if h := bodyOf[progress.Header](t, recs[0]); h.CancelBudgetMs != verbs[verbNoop].cancelBudget.Milliseconds() {
		t.Fatalf("header = %+v, want the verb's budget", h)
	}

	if e := bodyOf[progress.ErrorBody](t, recs[1]); e.Code != progress.CodeJobCanceled || !strings.Contains(e.Message, "before its verb started") {
		t.Fatalf("error = %+v, want JOB_CANCELED before the verb started", e)
	}

	if r := bodyOf[progress.ResultBody](t, recs[2]); r.ExitCode != 1 || r.Cancel != progress.CancelStopped {
		t.Fatalf("result = %+v, want exit code 1 and cancel stopped", r)
	}

	var e Exit

	decodeStrictFile(t, filepath.Join(s.Dir, exitFile), &e)

	if e.Cancel != progress.CancelStopped || e.CancelSeenAt == nil || !e.CancelSeenAt.Equal(testNow) {
		t.Fatalf("exit = %+v, want cancel stopped seen at %s", e, testNow)
	}
}

// A marker that lands once the job has finished changes nothing, and a verb
// that finishes on its own after the run saw the marker ends the job as if
// no cancel had come.
func TestAMarkerAfterTheVerbEndsChangesNothing(t *testing.T) {
	t.Parallel()

	t.Run("after the end", func(t *testing.T) {
		t.Parallel()

		root := spoolRoot(t)
		store := storeWith(t, liveRecord())
		sr := signedRequest(t, goodRequest())
		dir := filepath.Join(root, "job-0001")

		first := newRun(t, store, root, sr)
		must(t, first.run(t))

		exit := readFile(t, filepath.Join(dir, exitFile))
		status := readFile(t, filepath.Join(dir, statusFile))

		_, err := Spool{Dir: dir}.WriteCancel(Cancel{RequestedAt: testNow})
		must(t, err)

		second := newRun(t, store, root, sr)
		must(t, second.run(t))

		if !bytes.Equal(second.stdout.Bytes(), first.stdout.Bytes()) {
			t.Fatalf("the replay differs from the run\nrun:\n%s\nreplay:\n%s", first.stdout, second.stdout)
		}

		if got := readFile(t, filepath.Join(dir, exitFile)); !bytes.Equal(got, exit) {
			t.Fatalf("exit.json changed from %s to %s", exit, got)
		}

		if got := readFile(t, filepath.Join(dir, statusFile)); !bytes.Equal(got, status) {
			t.Fatalf("status.json changed from %s to %s", status, got)
		}

		wantExitShape(t, exit, 0)
	})

	t.Run("while it finishes", func(t *testing.T) {
		t.Parallel()

		finish := make(chan struct{})

		start, p := scriptedStart(0, func(p *scriptedProc) {
			_ = p.write(stepStartedEnvelope)

			select {
			case <-finish:
			case <-time.After(verbWait):
			}

			_ = p.write(`{"type":"record","kind":"event","body":{"kind":"step_finished","step":"noop","outcome":"succeeded"}}`)
			_ = p.out.Close()

			p.exit()
		})

		root := spoolRoot(t)
		s := Spool{Dir: filepath.Join(root, "job-0001")}

		rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
		rc.opts.start = start
		rc.clock.onWait = func(n int) {
			switch n {
			case 1:
				if _, err := s.WriteCancel(Cancel{RequestedAt: testNow}); err != nil {
					t.Error(err)
				}
			case 2:
				close(finish)

				select {
				case <-p.exited:
				case <-time.After(verbWait):
					t.Error("the verb did not exit")
				}
			}
		}

		err := rc.run(t)
		if err != nil {
			t.Fatalf("Run = %v, want the verb's own success", err)
		}

		recs := records(t, rc.stdout.Bytes())

		want := []progress.Kind{progress.KindHeader, progress.KindEvent, progress.KindEvent, progress.KindResult}
		if got := streamKindsOf(t, recs); !slices.Equal(got, want) {
			t.Fatalf("record kinds = %v, want %v:\n%s", got, want, rc.stdout)
		}

		if r := bodyOf[progress.ResultBody](t, recs[3]); r.ExitCode != 0 || r.Cancel != "" {
			t.Fatalf("result = %+v, want exit code 0 and no cancel", r)
		}

		wantExitShape(t, readFile(t, filepath.Join(s.Dir, exitFile)), 0)
	})
}

// wantKeysOf checks that the JSON object raw has exactly the keys of the
// named fixture.
func wantKeysOf(t *testing.T, raw []byte, fixture string) {
	t.Helper()

	var got, want map[string]json.RawMessage

	must(t, json.Unmarshal(raw, &got))
	must(t, json.Unmarshal(progresstest.Fixture(fixture), &want))

	if g, w := slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)); !slices.Equal(g, w) {
		t.Fatalf("keys = %v, the fixture's %v", g, w)
	}
}

// monoClock reads the system's clock once, with its monotonic reading, and
// moves on from there only when After is called. It keeps every reading it
// gives out.
type monoClock struct {
	mu       sync.Mutex
	now      time.Time
	readings []time.Time
	onWait   func(n int)
	waits    int
}

func newMonoClock() *monoClock { return &monoClock{now: time.Now()} }

func (c *monoClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.readings = append(c.readings, c.now)

	return c.now
}

func (c *monoClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.waits++
	n, now, hook := c.waits, c.now, c.onWait
	c.mu.Unlock()

	if hook != nil {
		hook(n)
	}

	time.Sleep(fakePace)

	ch := make(chan time.Time, 1)
	ch <- now

	return ch
}

// The supervisor measures the budget on the clock's own reading, monotonic
// part and all, so a step of the wall clock cannot move the kill. Only the
// time it reports for exit.json is in UTC.
func TestTheBudgetIsMeasuredOnTheClocksOwnReading(t *testing.T) {
	t.Parallel()

	s, _, err := CreateSpool(spoolRoot(t), "job-0001")
	must(t, err)

	_, err = s.WriteCancel(Cancel{RequestedAt: testNow})
	must(t, err)

	start, p := scriptedStart(0, func(p *scriptedProc) {
		select {
		case <-p.exited:
		case <-time.After(verbWait):
			p.exit()
		}
	})

	proc, err := start(verbStart{})
	must(t, err)

	clock := newMonoClock()
	clock.onWait = func(n int) {
		if n == 2 {
			p.exit()
		}
	}

	if first := clock.now; first == first.Round(0) {
		t.Fatal("the system clock gave no monotonic reading, so the test shows nothing")
	}

	sv := &supervisor{clock: clock, spool: s, proc: proc, relay: &relay{enc: progress.NewEncoder(&bytes.Buffer{}, clock)}, budget: time.Second}
	sv.watch()

	clock.mu.Lock()
	first := clock.readings[0]
	clock.mu.Unlock()

	if sv.seenAt != first { //nolint:gocritic // the monotonic reading is what is checked
		t.Fatalf("the supervisor saw the marker at %v, want the clock's reading %v as it is", sv.seenAt, first)
	}

	if got := sv.seenUTC(); got.Location() != time.UTC || !got.Equal(first) {
		t.Fatalf("the reported time = %v, want %v in UTC", got, first)
	}
}
