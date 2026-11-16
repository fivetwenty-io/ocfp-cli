package jobexec

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// cancelRequest is goodRequest as a cancel, which names no verb and no env.
func cancelRequest() Request {
	req := goodRequest()
	req.Action, req.Verb, req.Env = ActionCancel, "", nil

	return req
}

// liveSpool is the spool of a job that a run of this contract is running.
// The test holds the lock on out for the run, and ends the run with the
// function it returns, which appends the result and writes exit.json first.
func liveSpool(t *testing.T, root string) (Spool, func()) {
	t.Helper()

	st := runningStatus(os.Getpid())
	st.WireContract = progress.WireContract

	s := partialSpool(t, root, st)
	release := holdOut(t, s)

	var once sync.Once

	return s, func() {
		once.Do(func() {
			appendLine(t, filepath.Join(s.Dir, outFile), resultLine)
			must(t, s.WriteExit(Exit{ExitCode: 0, FinishedAt: testNow}))
			release()
		})
	}
}

// cancelRun is a cancel request run against store and root, which checks
// the lock on out as a real attach does.
func cancelRun(t *testing.T, store ownerrecord.Store, root string) *runCase {
	t.Helper()

	rc := newRun(t, store, root, signedRequest(t, cancelRequest()))
	rc.opts.held = outHeld

	return rc
}

// markerStore is a store that notes, at each release of a shared lock,
// whether the spool at dir holds a cancel marker.
type markerStore struct {
	ownerrecord.Store

	dir string

	mu       sync.Mutex
	released []bool
}

func (m *markerStore) RLock(ctx context.Context) (func() error, error) {
	release, err := m.Store.RLock(ctx)
	if err != nil {
		return nil, err
	}

	return func() error {
		_, statErr := os.Stat(filepath.Join(m.dir, cancelFile))

		m.mu.Lock()
		m.released = append(m.released, statErr == nil)
		m.mu.Unlock()

		return release()
	}, nil
}

func (m *markerStore) releases() []bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]bool(nil), m.released...)
}

// A cancel request is checked exactly as a run request is. Each fault of the
// owner check's table draws the same code and the same refusal stream for a
// cancel as for a run, and a refused cancel neither makes a spool nor writes
// a marker into one that exists.
func TestACancelRequestNeedsTheOwnersSignature(t *testing.T) {
	t.Parallel()

	for _, f := range faults() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()

			streams := map[string][]byte{}

			for _, action := range []string{ActionRun, ActionCancel} {
				s := baseScenario()
				if action == ActionCancel {
					s.request = cancelRequest()
				}

				f.apply(s)

				root := spoolRoot(t)
				rc := newRun(t, s.store(t), root, s.signed(t))

				err := rc.run(t)
				wantRefusal(t, err, f.code)
				wantRefusalStream(t, rc.stdout.Bytes(), f.code, "")
				wantNoPath(t, filepath.Join(root, "job-0001"))

				streams[action] = rc.stdout.Bytes()
			}

			if !bytes.Equal(streams[ActionRun], streams[ActionCancel]) {
				t.Fatalf("the refusal streams differ\nrun:\n%s\ncancel:\n%s", streams[ActionRun], streams[ActionCancel])
			}

			// A refused cancel of a job that is running writes no marker.
			s := baseScenario()
			s.request = cancelRequest()
			f.apply(s)

			root := spoolRoot(t)
			sp, _ := liveSpool(t, root)

			rc := newRun(t, s.store(t), root, s.signed(t))
			wantRefusal(t, rc.run(t), f.code)
			wantNoPath(t, filepath.Join(sp.Dir, cancelFile))
		})
	}
}

// A cancel of a job that has finished writes no marker and changes nothing in
// its spool. Its reply is the replay that a plain attach gets.
func TestACancelOfAFinishedJobChangesNothing(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	store := storeWith(t, liveRecord())

	first := newRun(t, store, root, signedRequest(t, goodRequest()))
	must(t, first.run(t))

	dir := filepath.Join(root, "job-0001")
	before := map[string][]byte{}

	for _, name := range []string{outFile, statusFile, exitFile} {
		before[name] = readFile(t, filepath.Join(dir, name))
	}

	attach := newRun(t, store, root, signedRequest(t, goodRequest()))
	must(t, attach.run(t))

	rc := cancelRun(t, store, root)
	if err := rc.run(t); err != nil {
		t.Fatalf("a cancel of a finished job = %v", err)
	}

	for name, want := range before {
		if got := readFile(t, filepath.Join(dir, name)); !bytes.Equal(got, want) {
			t.Fatalf("%s changed from\n%s\nto\n%s", name, want, got)
		}
	}

	wantNoPath(t, filepath.Join(dir, cancelFile))

	if !bytes.Equal(rc.stdout.Bytes(), attach.stdout.Bytes()) {
		t.Fatalf("the cancel's reply differs from an attach\nattach:\n%s\ncancel:\n%s", attach.stdout, rc.stdout)
	}
}

// A cancel of a running job writes the marker while it holds the owner
// lock, gives the lock back, and attaches. A second cancel while the job
// still runs finds the marker there, leaves it as it is, and attaches too,
// and both see the same stream.
func TestTwoCancelsWriteOneMarker(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	s, finish := liveSpool(t, root)
	store := &markerStore{Store: storeWith(t, liveRecord()), dir: s.Dir}

	var marker []byte

	first := cancelRun(t, store, root)
	second := cancelRun(t, store, root)

	second.clock.now = testNow.Add(pollInterval * 7)

	first.clock.onWait = func(n int) {
		if n != 1 {
			return
		}

		marker = readFile(t, filepath.Join(s.Dir, cancelFile))

		// Each attach gives the owner lock back before it polls.
		wantUnlocked(t, store)

		if err := second.run(t); err != nil {
			t.Errorf("the second cancel = %v", err)
		}
	}

	second.clock.onWait = func(n int) {
		if n == 1 {
			finish()
		}
	}

	if err := first.run(t); err != nil {
		t.Fatalf("the first cancel = %v", err)
	}

	var c Cancel

	decodeStrictFile(t, filepath.Join(s.Dir, cancelFile), &c)

	if !c.RequestedAt.Equal(testNow) {
		t.Fatalf("marker = %+v, want the first cancel's time %s", c, testNow)
	}

	if got := readFile(t, filepath.Join(s.Dir, cancelFile)); !bytes.Equal(got, marker) {
		t.Fatalf("the second cancel changed the marker from %s to %s", marker, got)
	}

	if got := store.releases(); len(got) != 2 || !got[0] || !got[1] {
		t.Fatalf("the marker was there at the lock's releases %v, want at both", got)
	}

	if !bytes.Equal(first.stdout.Bytes(), second.stdout.Bytes()) {
		t.Fatalf("the two cancels saw different streams\nfirst:\n%s\nsecond:\n%s", first.stdout, second.stdout)
	}

	if !bytes.HasSuffix(first.stdout.Bytes(), []byte(resultLine)) {
		t.Fatalf("the cancel's reply does not end in the job's result:\n%s", first.stdout)
	}
}

// A cancel of a job with no spool is refused with JOB_NOT_FOUND and exit
// code 2, and makes neither a root nor a spool.
func TestACancelOfAnUnknownJobIsRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		root func(t *testing.T) string
	}{
		{"in a root with no spool for it", spoolRoot},
		{"with no root", func(t *testing.T) string { return filepath.Join(t.TempDir(), "spool") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := tc.root(t)
			store := storeWith(t, liveRecord())

			rc := cancelRun(t, store, root)

			r := wantRefusal(t, rc.run(t), progress.CodeJobNotFound)
			if r.Message != "job job-0001 has no spool on this host" {
				t.Fatalf("message = %q", r.Message)
			}

			wantRefusalStream(t, rc.stdout.Bytes(), progress.CodeJobNotFound, "job-0001")
			wantNoPath(t, filepath.Join(root, "job-0001"))
			wantUnlocked(t, store)
		})
	}

	// A root that fails the checks CreateSpool makes is not an unknown job.
	t.Run("in a root that others can write", func(t *testing.T) {
		t.Parallel()

		root := filepath.Join(t.TempDir(), "spool")
		must(t, os.Mkdir(root, 0o700))
		must(t, os.Chmod(root, 0o777)) //nolint:gosec // a root the checks must refuse

		rc := cancelRun(t, storeWith(t, liveRecord()), root)

		err := rc.run(t)
		if err == nil {
			t.Fatal("a cancel in a root that others can write succeeded")
		}

		if _, ok := progress.AsRefusal(err); ok {
			t.Fatalf("a cancel in a root that others can write was refused: %v", err)
		}
	})
}

// A cancel of a job that a run of an older contract started is refused with
// WIRE_CONTRACT_UNSUPPORTED, since that run never looks for a marker, and
// writes none. A spool whose status.json is not yet written gets the marker,
// since only a run of this contract writes into a spool before its status.
func TestACancelOfAContractTwoJobIsRefused(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	store := storeWith(t, liveRecord())

	s := partialSpool(t, root, runningStatus(os.Getpid()))
	holdOut(t, s)

	rc := cancelRun(t, store, root)

	r := wantRefusal(t, rc.run(t), progress.CodeWireContractUnsupported)
	if !strings.Contains(r.Message, "wire contract 2") {
		t.Fatalf("message = %q, want it to name the job's contract", r.Message)
	}

	wantRefusalStream(t, rc.stdout.Bytes(), progress.CodeWireContractUnsupported, "job-0001")
	wantNoPath(t, filepath.Join(s.Dir, cancelFile))
	wantUnlocked(t, store)

	t.Run("before its status", func(t *testing.T) {
		t.Parallel()

		root := spoolRoot(t)
		s := partialSpool(t, root, nil)
		release := holdOut(t, s)

		rc := cancelRun(t, storeWith(t, liveRecord()), root)
		rc.clock.onWait = func(n int) {
			if n != 1 {
				return
			}

			if _, ok, err := s.readCancel(); err != nil || !ok {
				t.Errorf("readCancel = %v, %v; want the marker", ok, err)
			}

			st := runningStatus(os.Getpid())
			st.WireContract = progress.WireContract
			must(t, s.WriteStatus(*st))
			appendLine(t, filepath.Join(s.Dir, outFile), resultLine)
			must(t, s.WriteExit(Exit{ExitCode: 0, FinishedAt: testNow}))
			release()
		}

		must(t, rc.run(t))
	})
}

// outcomes records each cancel outcome a run reports.
type outcomes struct {
	mu  sync.Mutex
	got []string
}

func (oc *outcomes) report(jobID string, outcome progress.CancelOutcome) {
	oc.mu.Lock()
	defer oc.mu.Unlock()

	oc.got = append(oc.got, jobID+" "+string(outcome))
}

func (oc *outcomes) all() []string {
	oc.mu.Lock()
	defer oc.mu.Unlock()

	return append([]string(nil), oc.got...)
}

// A cancel request reports how the job it names ended: stopped when the
// cancel stopped it, and already_finished when it had ended first. A refused
// cancel and a cancel of an orphaned job report nothing.
func TestACancelReportsItsOutcome(t *testing.T) {
	t.Parallel()

	t.Run("stopped", func(t *testing.T) {
		t.Parallel()

		root := spoolRoot(t)
		store := storeWith(t, liveRecord())
		s := Spool{Dir: filepath.Join(root, "job-0001")}
		w := &waitingVerb{}

		oc := &outcomes{}
		cancel := cancelRun(t, store, root)
		cancel.opts.CancelOutcome = oc.report

		cancelled := make(chan error, 1)

		job := newRun(t, store, root, signedRequest(t, goodRequest()))
		job.opts.start = w.start
		job.clock.onWait = func(n int) {
			if n == 1 {
				go func() { cancelled <- cancel.run(t) }()

				waitFor(t, func() bool {
					_, ok, _ := s.readCancel()

					return ok
				})

				return
			}

			w.waitExited(t)
		}

		if err := job.run(t); err == nil {
			t.Fatal("the cancelled job reported success")
		}

		if err := <-cancelled; err == nil {
			t.Fatal("the cancel of a job that stopped reported success")
		}

		if got, want := oc.all(), []string{"job-0001 stopped"}; !slices.Equal(got, want) {
			t.Fatalf("outcomes = %q, want %q", got, want)
		}

		if !bytes.Equal(cancel.stdout.Bytes(), job.stdout.Bytes()) {
			t.Fatalf("the cancel's reply differs from the job's stream\njob:\n%s\ncancel:\n%s", job.stdout, cancel.stdout)
		}
	})

	t.Run("already finished", func(t *testing.T) {
		t.Parallel()

		root := spoolRoot(t)
		store := storeWith(t, liveRecord())

		must(t, newRun(t, store, root, signedRequest(t, goodRequest())).run(t))

		oc := &outcomes{}
		rc := cancelRun(t, store, root)
		rc.opts.CancelOutcome = oc.report

		must(t, rc.run(t))

		if got, want := oc.all(), []string{"job-0001 already_finished"}; !slices.Equal(got, want) {
			t.Fatalf("outcomes = %q, want %q", got, want)
		}
	})

	t.Run("refused", func(t *testing.T) {
		t.Parallel()

		oc := &outcomes{}
		rc := cancelRun(t, storeWith(t, liveRecord()), spoolRoot(t))
		rc.opts.CancelOutcome = oc.report

		wantRefusal(t, rc.run(t), progress.CodeJobNotFound)

		if got := oc.all(); len(got) != 0 {
			t.Fatalf("a refused cancel reported %q", got)
		}
	})

	t.Run("orphaned", func(t *testing.T) {
		t.Parallel()

		root := spoolRoot(t)
		st := runningStatus(os.Getpid())
		st.WireContract = progress.WireContract
		partialSpool(t, root, st)

		oc := &outcomes{}
		rc := cancelRun(t, storeWith(t, liveRecord()), root)
		rc.opts.CancelOutcome = oc.report

		if err := rc.run(t); err == nil || !strings.Contains(err.Error(), string(progress.CodeJobOrphaned)) {
			t.Fatalf("a cancel of an orphaned job = %v", err)
		}

		if got := oc.all(); len(got) != 0 {
			t.Fatalf("a cancel of an orphaned job reported %q", got)
		}
	})
}

// waitFor waits, in real time, until cond holds, and fails the test when it
// does not hold within verbWait.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(verbWait)

	for !cond() {
		if time.Now().After(deadline) {
			t.Error("the condition did not hold in time")

			return
		}

		time.Sleep(time.Millisecond)
	}
}
