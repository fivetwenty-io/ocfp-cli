package jobexec

import (
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// stubbornVerb is a scripted verb that ignores every cancel. It writes its
// lines, says it is ready once the relay has taken them all, and then waits
// to be killed.
func stubbornVerb(lines ...string) (func(verbStart) (*verbProc, error), *scriptedProc, <-chan struct{}) {
	ready := make(chan struct{})

	start, p := scriptedStart(0, func(p *scriptedProc) {
		for _, line := range append([]string{stepStartedEnvelope}, lines...) {
			_ = p.write(line)
		}

		// The relay reads one line at a time and handles it before it reads
		// the next, so once this line is taken every line before it has been
		// handled.
		_ = p.write(`{"type":"record","kind":"log","body":{"stream":"stdout","line":"waiting"}}`)

		close(ready)

		select {
		case <-p.exited:
		case <-time.After(verbWait):
			p.exit()
		}
	})

	return start, p, ready
}

// killRun runs a job whose verb ignores the cancel. The marker lands at the
// second wait, once the verb is ready. It returns the run, the verb, the
// clock's reading when the run first saw the marker, and the clock's
// reading at each kill.
func killRun(t *testing.T, lines ...string) (*runCase, *scriptedProc, time.Time, []time.Time, error) {
	t.Helper()

	return killRunWith(t, nil, lines...)
}

// killRunWith is killRun with setup, when it is set, run on the run's case
// before the run starts.
func killRunWith(t *testing.T, setup func(rc *runCase), lines ...string) (*runCase, *scriptedProc, time.Time, []time.Time, error) {
	t.Helper()

	start, p, ready := stubbornVerb(lines...)

	root := spoolRoot(t)
	s := Spool{Dir: filepath.Join(root, "job-0001")}

	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
	rc.opts.start = start
	rc.opts.ioGrace = 20 * time.Millisecond

	var (
		mu    sync.Mutex
		kills []time.Time
	)

	p.onKill = func() {
		now := rc.clock.Now()

		mu.Lock()
		kills = append(kills, now)
		mu.Unlock()
	}

	budget := verbs[verbNoop].cancelBudget
	seen := testNow.Add(2 * pollInterval)

	rc.clock.onWait = func(n int) {
		switch n {
		case 1:
			select {
			case <-ready:
			case <-time.After(verbWait):
				t.Error("the verb did not get ready")
			}
		case 2:
			if _, err := s.WriteCancel(Cancel{RequestedAt: testNow}); err != nil {
				t.Error(err)
			}
		}

		// The run reads the clock after each wait, so a kill at this
		// wait's reading has not happened yet.
		if p.kills.Load() != 0 {
			t.Errorf("the verb was killed before wait %d, at most %s after the marker was seen, ended", n, rc.clock.Now().Sub(seen))
		}

		// A run that does not kill would wait on for ever, so the verb
		// exits at the first wait past the budget.
		if rc.clock.Now().After(seen.Add(budget)) {
			t.Errorf("wait %d goes past the budget", n)
			p.exit()
		}
	}

	if setup != nil {
		setup(rc)
	}

	err := rc.run(t)

	mu.Lock()
	defer mu.Unlock()

	return rc, p, seen, slices.Clone(kills), err
}

// A verb that does not stop is killed once the budget has passed on the
// run's clock from when the run first saw the marker: not at any wait
// before that, and first at it. The scripted verb exits at its first kill,
// so it is killed once. A real verb whose kill fails is killed again at the
// next look, and its group gets one more kill when it is reaped. The stream
// ends with JOB_CANCELED and a result that says the job was killed, and
// exit.json records it.
func TestACancelKillsWhenTheBudgetPasses(t *testing.T) {
	t.Parallel()

	rc, p, seen, kills, err := killRun(t)
	if err == nil {
		t.Fatal("Run reported success for a killed job")
	}

	budget := verbs[verbNoop].cancelBudget

	if len(kills) != 1 || !kills[0].Equal(seen.Add(budget)) {
		t.Fatalf("kills at %v, want one at %s", kills, seen.Add(budget))
	}

	if n := p.kills.Load(); n != 1 {
		t.Fatalf("the verb was killed %d times, want once", n)
	}

	// The marker is seen after the second wait, and the budget is a whole
	// number of waits.
	if got, want := len(rc.clock.waited()), 2+int(budget/pollInterval); got != want {
		t.Fatalf("the run waited %d times, want %d", got, want)
	}

	recs := records(t, rc.stdout.Bytes())

	want := []progress.Kind{progress.KindHeader, progress.KindEvent, progress.KindLog, progress.KindError, progress.KindResult}
	if got := streamKindsOf(t, recs); !slices.Equal(got, want) {
		t.Fatalf("record kinds = %v, want %v and no warning:\n%s", got, want, rc.stdout)
	}

	if e := bodyOf[progress.ErrorBody](t, recs[3]); e.Code != progress.CodeJobCanceled || e.Message != "job job-0001 was canceled and killed when its cancel budget of 5s passed" {
		t.Fatalf("error = %+v, want JOB_CANCELED for a kill", e)
	}

	if r := bodyOf[progress.ResultBody](t, recs[4]); r.ExitCode != 1 || r.Cancel != progress.CancelKilled {
		t.Fatalf("result = %+v, want exit code 1 and cancel killed", r)
	}

	var e Exit

	decodeStrictFile(t, filepath.Join(rc.opts.SpoolRoot, "job-0001", exitFile), &e)

	if e.ExitCode != 1 || e.Cancel != progress.CancelKilled || e.CancelSeenAt == nil || !e.CancelSeenAt.Equal(seen) {
		t.Fatalf("exit = %+v, want exit code 1, cancel killed, seen at %s", e, seen)
	}
}

// A verb that writes the stopped mark before the budget passes has stopped,
// and is never killed. One that writes the mark and then does not exit is
// killed at the budget, but it had stopped first, and that is the outcome.
func TestAStopBeforeTheBudgetIsNotAKill(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		exits bool
		kills int32
	}{
		{"and exits", true, 0},
		{"and does not exit", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stop := make(chan struct{})
			stopped := make(chan struct{})

			start, p := scriptedStart(1, func(p *scriptedProc) {
				_ = p.write(stepStartedEnvelope)

				select {
				case <-stop:
				case <-time.After(verbWait):
				}

				_ = p.write(`{"type":"stopped"}`)

				// As in stubbornVerb, the relay has handled the stopped
				// mark once it takes the next line.
				_ = p.write(`{"type":"record","kind":"log","body":{"stream":"stdout","line":"stopped"}}`)

				if tc.exits {
					_ = p.out.Close()

					p.exit()
				}

				close(stopped)
			})

			root := spoolRoot(t)
			s := Spool{Dir: filepath.Join(root, "job-0001")}

			rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
			rc.opts.start = start
			rc.opts.ioGrace = 20 * time.Millisecond
			rc.clock.onWait = func(n int) {
				switch n {
				case 1:
					if _, err := s.WriteCancel(Cancel{RequestedAt: testNow}); err != nil {
						t.Error(err)
					}
				case 3:
					close(stop)

					select {
					case <-stopped:
					case <-time.After(verbWait):
						t.Error("the verb did not stop")
					}
				}

				// A run that does not kill would wait on for ever, so the
				// verb exits at the first wait past the budget.
				if rc.clock.Now().After(testNow.Add(pollInterval + verbs[verbNoop].cancelBudget)) {
					t.Errorf("wait %d goes past the budget", n)
					p.exit()
				}
			}

			err := rc.run(t)
			if err == nil {
				t.Fatal("Run reported success for a stopped job")
			}

			if n := p.kills.Load(); n != tc.kills {
				t.Fatalf("the verb was killed %d times, want %d", n, tc.kills)
			}

			recs := records(t, rc.stdout.Bytes())
			last := recs[len(recs)-1]

			if r := bodyOf[progress.ResultBody](t, last); r.ExitCode != 1 || r.Cancel != progress.CancelStopped {
				t.Fatalf("result = %+v, want exit code 1 and cancel stopped", r)
			}

			if e := bodyOf[progress.ErrorBody](t, recs[len(recs)-2]); e.Code != progress.CodeJobCanceled || e.Message != "job job-0001 was canceled and stopped at a safe point" {
				t.Fatalf("error = %+v, want JOB_CANCELED for a stop", e)
			}
		})
	}
}

// A killed job's stream carries one warning before its error, which lists
// the undo hooks that the verb registered and never reported done, oldest
// first, as typed entries that say whether each hook did not run or failed.
func TestAKillNamesTheHooksLeftUndone(t *testing.T) {
	t.Parallel()

	rc, _, _, _, err := killRun(t,
		`{"type":"undo","work":{"kind":"remote-task","id":"task-1"},"state":"registered"}`,
		`{"type":"undo","work":{"kind":"lease","id":"lease-7"},"state":"registered"}`,
		`{"type":"undo","work":{"kind":"remote-task","id":"task-3"},"state":"registered"}`,
		`{"type":"undo","work":{"kind":"lease","id":"lease-9"},"state":"registered"}`,
		`{"type":"undo","work":{"kind":"lease","id":"lease-9"},"state":"failed"}`,
		`{"type":"undo","work":{"kind":"lease","id":"lease-7"},"state":"done"}`,
	)
	if err == nil {
		t.Fatal("Run reported success for a killed job")
	}

	recs := records(t, rc.stdout.Bytes())

	want := []progress.Kind{progress.KindHeader, progress.KindEvent, progress.KindLog, progress.KindEvent, progress.KindError, progress.KindResult}
	if got := streamKindsOf(t, recs); !slices.Equal(got, want) {
		t.Fatalf("record kinds = %v, want %v:\n%s", got, want, rc.stdout)
	}

	wantBody := `{"kind":"warning","step":"cancel","message":"undo hooks that did not run: remote-task task-1, remote-task task-3; undo hooks that failed: lease lease-9","undone":[{"kind":"remote-task","id":"task-1","state":"not_run"},{"kind":"remote-task","id":"task-3","state":"not_run"},{"kind":"lease","id":"lease-9","state":"failed"}]}`
	if string(recs[3].Body) != wantBody {
		t.Fatalf("warning = %s\nwant %s", recs[3].Body, wantBody)
	}

	if r := bodyOf[progress.ResultBody](t, recs[5]); r.Cancel != progress.CancelKilled {
		t.Fatalf("result = %+v, want cancel killed", r)
	}
}

// stalledClient takes its first write, which is the header, and then stops
// reading: every later write waits until the test ends.
type stalledClient struct {
	writes  atomic.Int32
	release chan struct{}
}

func newStalledClient(t *testing.T) *stalledClient {
	t.Helper()

	c := &stalledClient{release: make(chan struct{})}
	t.Cleanup(func() { close(c.release) })

	return c
}

func (c *stalledClient) Write(p []byte) (int, error) {
	if c.writes.Add(1) > 1 {
		<-c.release
	}

	return len(p), nil
}

// A client that stops reading part way through the stream holds up neither
// the kill nor the end of the job. The kill comes at the budget on the run's
// clock, the spool's out holds the whole stream with its result, exit.json is
// written, and the run reports that the client fell behind.
func TestAStalledClientDoesNotHoldUpTheKill(t *testing.T) {
	t.Parallel()

	client := newStalledClient(t)

	type ran struct {
		rc    *runCase
		seen  time.Time
		kills []time.Time
		err   error
	}

	done := make(chan ran, 1)

	go func() {
		rc, _, seen, kills, err := killRunWith(t, func(rc *runCase) {
			rc.opts.Stdout = client
			rc.opts.clientGrace = 20 * time.Millisecond
		})

		done <- ran{rc, seen, kills, err}
	}()

	var r ran

	select {
	case r = <-done:
	case <-time.After(verbWait / 2):
		t.Fatal("the run waited on a client that had stopped reading")
	}

	budget := verbs[verbNoop].cancelBudget

	if len(r.kills) != 1 || !r.kills[0].Equal(r.seen.Add(budget)) {
		t.Fatalf("kills at %v, want one at %s", r.kills, r.seen.Add(budget))
	}

	if r.err == nil || !strings.Contains(r.err.Error(), "stdout") {
		t.Fatalf("Run = %v, want an error that names the client on stdout", r.err)
	}

	dir := filepath.Join(r.rc.opts.SpoolRoot, "job-0001")
	recs := records(t, readFile(t, filepath.Join(dir, outFile)))

	want := []progress.Kind{progress.KindHeader, progress.KindEvent, progress.KindLog, progress.KindError, progress.KindResult}
	if got := streamKindsOf(t, recs); !slices.Equal(got, want) {
		t.Fatalf("out holds record kinds %v, want %v", got, want)
	}

	if res := bodyOf[progress.ResultBody](t, recs[4]); res.Cancel != progress.CancelKilled {
		t.Fatalf("result = %+v, want cancel killed", res)
	}

	var e Exit

	decodeStrictFile(t, filepath.Join(dir, exitFile), &e)

	if e.Cancel != progress.CancelKilled {
		t.Fatalf("exit = %+v, want cancel killed", e)
	}

}
