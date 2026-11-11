package jobexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress/progresstest"
	"github.com/fivetwenty-io/ocfp-cli/internal/version"
)

// fakeClock reads testNow until After moves it on. After records how long it
// was asked to wait, moves the clock that far at once, and runs onWait when
// it is set, so a test can change the spool between two polls.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	waits  []time.Duration
	onWait func(n int)
	onNow  func()
}

func newFakeClock() *fakeClock { return &fakeClock{now: testNow} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	now := c.now
	hook := c.onNow
	c.mu.Unlock()

	if hook != nil {
		hook()
	}

	return now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.waits = append(c.waits, d)
	n := len(c.waits)
	now := c.now
	hook := c.onWait
	c.mu.Unlock()

	if hook != nil {
		hook(n)
	}

	ch := make(chan time.Time, 1)
	ch <- now

	return ch
}

func (c *fakeClock) waited() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	return slices.Clone(c.waits)
}

const testHost = "host-fixture-a"

// hostCounter is a Hostname function that counts its calls.
type hostCounter struct{ calls atomic.Int32 }

func (h *hostCounter) hostname() (string, error) {
	h.calls.Add(1)

	return testHost, nil
}

// runCase is one call of Run.
type runCase struct {
	opts   Options
	stdout *bytes.Buffer
	clock  *fakeClock
	host   *hostCounter
}

func newRun(t *testing.T, store ownerrecord.Store, root string, sr SignedRequest) *runCase {
	t.Helper()

	rc := &runCase{stdout: &bytes.Buffer{}, clock: newFakeClock(), host: &hostCounter{}}
	rc.opts = Options{
		SpoolRoot: root,
		Owner:     store,
		Stdin:     bytes.NewReader(append(mustMarshal(t, sr), '\n')),
		Stdout:    rc.stdout,
		Clock:     rc.clock,
		Hostname:  rc.host.hostname,
		held: func(*os.File) (bool, error) {
			t.Error("the lock on out was checked by the run that made its spool")
			return false, nil
		},
	}

	return rc
}

func (rc *runCase) run(t *testing.T) error {
	t.Helper()

	return Run(t.Context(), rc.opts)
}

func signedRequest(t *testing.T, req Request) SignedRequest {
	t.Helper()

	return signPayload(mustMarshal(t, req), ownerKey())
}

func fixtureRequest() Request {
	req := goodRequest()
	req.JobID = "job-fixture-01"

	return req
}

// records splits a stream into its lines and decodes each one.
func records(t *testing.T, stream []byte) []progress.Record {
	t.Helper()

	var out []progress.Record

	for line := range bytes.Lines(stream) {
		if !bytes.HasSuffix(line, []byte("\n")) {
			t.Fatalf("stream line %q has no newline", line)
		}

		var r progress.Record

		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()

		if err := dec.Decode(&r); err != nil {
			t.Fatalf("stream line %q: %v", line, err)
		}

		out = append(out, r)
	}

	return out
}

// normalized returns a record line with its time set to the zero time, so
// that only the clock's reading is left out of a comparison.
func normalized(t *testing.T, r progress.Record) string {
	t.Helper()

	r.Time = time.Time{}

	return string(mustMarshal(t, r))
}

func bodyOf[T any](t *testing.T, r progress.Record) T {
	t.Helper()

	var v T

	dec := json.NewDecoder(bytes.NewReader(r.Body))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&v); err != nil {
		t.Fatalf("%s body %s: %v", r.Kind, r.Body, err)
	}

	return v
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func wantNoPath(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s exists or cannot be checked: %v", path, err)
	}
}

// wantRefusalStream checks a refused run's stream: the header, one error with
// code, and a result with exit code 2.
func wantRefusalStream(t *testing.T, stream []byte, code progress.Code, jobID string) {
	t.Helper()

	recs := records(t, stream)
	if len(recs) != 3 {
		t.Fatalf("refused stream has %d records, want 3:\n%s", len(recs), stream)
	}

	for i, kind := range []progress.Kind{progress.KindHeader, progress.KindError, progress.KindResult} {
		if recs[i].Kind != kind || recs[i].Seq != int64(i) {
			t.Fatalf("record %d is %s seq %d, want %s seq %d", i, recs[i].Kind, recs[i].Seq, kind, i)
		}
	}

	h := bodyOf[progress.Header](t, recs[0])
	if h.WireContract != progress.WireContract || h.Producer != "ocfp" || h.Version != version.Get().Version || h.JobID != jobID {
		t.Fatalf("header = %+v, want job id %q", h, jobID)
	}

	if e := bodyOf[progress.ErrorBody](t, recs[1]); e.Code != code || e.Message == "" {
		t.Fatalf("error = %+v, want code %s with a message", e, code)
	}

	if r := bodyOf[progress.ResultBody](t, recs[2]); r.ExitCode != 2 {
		t.Fatalf("result exit code = %d, want 2", r.ExitCode)
	}
}

// The placeholders the no-op fixture holds for the host name and the version.
const (
	fixtureHost    = "host-fixture"
	fixtureVersion = "0.0.0-fixture"
)

// fixturePlaceholders checks that a record of a no-op run carries this run's
// host name and this CLI's version wherever it names them, and returns it
// with the fixture's placeholders in their place.
func fixturePlaceholders(t *testing.T, r progress.Record) progress.Record {
	t.Helper()

	switch r.Kind {
	case progress.KindHeader:
		h := bodyOf[progress.Header](t, r)
		if h.Version != version.Get().Version {
			t.Fatalf("header version = %q, want %q", h.Version, version.Get().Version)
		}

		h.Version = fixtureVersion
		r.Body = mustMarshal(t, h)
	case progress.KindLog:
		l := bodyOf[progress.LogBody](t, r)

		var out noopOutput

		dec := json.NewDecoder(strings.NewReader(l.Line))
		dec.DisallowUnknownFields()

		if err := dec.Decode(&out); err != nil {
			t.Fatalf("log line %q: %v", l.Line, err)
		}

		if out.Hostname != testHost || out.Version != version.Get().Version {
			t.Fatalf("log line = %+v, want host %q and version %q", out, testHost, version.Get().Version)
		}

		l.Line = string(mustMarshal(t, noopOutput{Hostname: fixtureHost, Version: fixtureVersion}))
		r.Body = mustMarshal(t, l)
	}

	return r
}

func TestRunNoopStreams(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, fixtureRequest()))

	err := rc.run(t)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}

	got := records(t, rc.stdout.Bytes())
	want := records(t, progresstest.Fixture("stream-noop-v1.ndjson"))

	if len(got) != len(want) {
		t.Fatalf("stream has %d records, the fixture %d:\n%s", len(got), len(want), rc.stdout)
	}

	// Every record matches the fixture once the time, the host name, and
	// the version are set to the fixture's placeholders.
	for i := range got {
		if g, w := normalized(t, fixturePlaceholders(t, got[i])), normalized(t, want[i]); g != w {
			t.Errorf("record %d = %s\nwant %s", i, g, w)
		}
	}

	for i, r := range got {
		if !r.Time.Equal(testNow) {
			t.Errorf("record %d time = %s, want the clock's %s", i, r.Time, testNow)
		}
	}

	dir := filepath.Join(root, "job-fixture-01")

	if out := readFile(t, filepath.Join(dir, "out")); !bytes.Equal(out, rc.stdout.Bytes()) {
		t.Fatalf("out differs from stdout\nout:\n%s\nstdout:\n%s", out, rc.stdout)
	}

	var st Status

	decodeStrictFile(t, filepath.Join(dir, "status.json"), &st)

	if st.State != StateExited || st.ExitCode == nil || *st.ExitCode != 0 || st.PID != os.Getpid() || !st.StartedAt.Equal(testNow) {
		t.Fatalf("status = %+v, want exited with code 0 by pid %d", st, os.Getpid())
	}

	wantExitShape(t, readFile(t, filepath.Join(dir, "exit.json")), 0)

	if n := rc.host.calls.Load(); n != 1 {
		t.Fatalf("Hostname was called %d times, want 1", n)
	}
}

func decodeStrictFile(t *testing.T, path string, v any) {
	t.Helper()

	dec := json.NewDecoder(bytes.NewReader(readFile(t, path)))
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// wantExitShape checks that raw has exactly the exit fixture's keys and the
// given exit code.
func wantExitShape(t *testing.T, raw []byte, code int) {
	t.Helper()

	var got, want map[string]json.RawMessage

	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(progresstest.Fixture("exit-v1.json"), &want); err != nil {
		t.Fatal(err)
	}

	if g, w := slices.Sorted(mapKeys(got)), slices.Sorted(mapKeys(want)); !slices.Equal(g, w) {
		t.Fatalf("exit.json keys = %v, the fixture's %v", g, w)
	}

	var e Exit

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&e); err != nil {
		t.Fatal(err)
	}

	if e.ExitCode != code || e.FinishedAt.IsZero() {
		t.Fatalf("exit = %+v, want code %d and a finish time", e, code)
	}
}

func mapKeys(m map[string]json.RawMessage) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// The run that made a spool holds its lock on out from before it writes
// status.json until after it writes exit.json, so an attach can never find
// the lock free while exit.json is still to come. Every time the run reads
// the clock, the test checks that rule from outside, and once Run returns it
// checks that the lock is gone.
func TestRunHoldsOutUntilExitIsWritten(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	dir := filepath.Join(root, "job-0001")

	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))

	checks := 0

	rc.clock.onNow = func() {
		if _, err := os.Stat(filepath.Join(dir, "status.json")); err != nil {
			return
		}

		if _, err := os.Stat(filepath.Join(dir, "exit.json")); err == nil {
			return
		}

		checks++

		if !lockedByAnother(t, dir) {
			t.Errorf("check %d: out is not locked while exit.json is still to come", checks)
		}
	}

	err := rc.run(t)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}

	// After status.json is written, the clock is read for each of the five
	// records and once more for the exit time.
	if checks < 6 {
		t.Fatalf("the lock was checked %d times, want one for every clock read after status.json", checks)
	}

	if lockedByAnother(t, dir) {
		t.Fatal("out is still locked after Run returned")
	}
}

// lockedByAnother reports whether something other than a new open file holds
// the lock on out in dir.
func lockedByAnother(t *testing.T, dir string) bool {
	t.Helper()

	f, err := os.Open(filepath.Join(dir, "out")) //nolint:gosec // a file under the test's own directory
	must(t, err)

	defer func() { _ = f.Close() }()

	held, err := outHeld(f)
	must(t, err)

	return held
}

func TestRunRefusedLeavesNoSpool(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	store, _ := emptyStore(t)
	sr := signedRequest(t, goodRequest())

	rc := newRun(t, store, root, sr)

	err := rc.run(t)
	wantRefusal(t, err, progress.CodeOwnerRecordMissing)

	// Nothing in the payload is read before the signature verifies, so the
	// header names no job.
	wantRefusalStream(t, rc.stdout.Bytes(), progress.CodeOwnerRecordMissing, "")
	wantNoPath(t, filepath.Join(root, "job-0001"))

	if n := rc.host.calls.Load(); n != 0 {
		t.Fatalf("a refused run called Hostname %d times", n)
	}

	// Once the record is signed, the same job id runs.
	err = store.Replace(t.Context(), nil, sealRecord(t, liveRecord()))
	if err != nil {
		t.Fatal(err)
	}

	again := newRun(t, store, root, sr)

	err = again.run(t)
	if err != nil {
		t.Fatalf("Run after the record was signed = %v", err)
	}

	if recs := records(t, again.stdout.Bytes()); recs[len(recs)-1].Kind != progress.KindResult {
		t.Fatalf("the second run's stream does not end in a result:\n%s", again.stdout)
	}
}

func TestRelaunchSameJobAttaches(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	store := storeWith(t, liveRecord())
	sr := signedRequest(t, goodRequest())

	first := newRun(t, store, root, sr)

	err := first.run(t)
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(root, "job-0001")
	status := readFile(t, filepath.Join(dir, "status.json"))
	exit := readFile(t, filepath.Join(dir, "exit.json"))

	second := newRun(t, store, root, sr)
	second.clock.now = testNow.Add(3 * time.Minute)

	err = second.run(t)
	if err != nil {
		t.Fatalf("relaunch = %v", err)
	}

	if !bytes.Equal(second.stdout.Bytes(), first.stdout.Bytes()) {
		t.Fatalf("relaunch stream differs from the first run's\nfirst:\n%s\nsecond:\n%s", first.stdout, second.stdout)
	}

	if n := second.host.calls.Load(); n != 0 {
		t.Fatalf("relaunch called Hostname %d times", n)
	}

	if got := readFile(t, filepath.Join(dir, "status.json")); !bytes.Equal(got, status) {
		t.Fatalf("relaunch changed status.json from %s to %s", status, got)
	}

	if got := readFile(t, filepath.Join(dir, "exit.json")); !bytes.Equal(got, exit) {
		t.Fatalf("relaunch changed exit.json from %s to %s", exit, got)
	}

	if w := second.clock.waited(); len(w) != 0 {
		t.Fatalf("relaunch of a finished job waited %v", w)
	}
}

// partialSpool makes a spool for job-0001 whose out holds a header and a
// step_started record, as a run that stopped part way through leaves it.
func partialSpool(t *testing.T, root string, status *Status) Spool {
	t.Helper()

	s, created, err := CreateSpool(root, "job-0001")
	if err != nil || !created {
		t.Fatalf("CreateSpool = %v, %v", created, err)
	}

	var buf bytes.Buffer

	enc := progress.NewEncoder(&buf, fixedClock{testNow})
	must(t, enc.Header(progress.Header{Producer: "ocfp", Version: "1.2.3", JobID: "job-0001"}))
	must(t, enc.Encode(progress.KindEvent, progress.Event{Kind: progress.EventStepStarted, Step: "noop"}))

	must(t, os.WriteFile(filepath.Join(s.Dir, "out"), buf.Bytes(), 0o600))

	if status != nil {
		must(t, s.WriteStatus(*status))
	}

	return s
}

// resultLine is a result record that follows partialSpool's two records.
const resultLine = `{"kind":"result","seq":2,"time":"2026-11-11T12:00:00Z","body":{"exitCode":0}}` + "\n"

// appendLine appends text to the file at path, as a run appends to out.
func appendLine(t *testing.T, path, text string) {
	t.Helper()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	must(t, err)

	_, err = f.WriteString(text)
	must(t, err)
	must(t, f.Close())
}

func must(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatal(err)
	}
}

// holdOut takes the lock on out that the run which made the spool holds
// while it lives, as if that run were still going. It returns the function
// that ends the run, which the test's cleanup also calls.
func holdOut(t *testing.T, s Spool) func() {
	t.Helper()

	f, err := os.OpenFile(filepath.Join(s.Dir, "out"), os.O_WRONLY|os.O_APPEND, 0)
	must(t, err)
	must(t, lockOut(f))

	var once sync.Once

	release := func() {
		once.Do(func() {
			if err := f.Close(); err != nil {
				t.Error(err)
			}
		})
	}

	t.Cleanup(release)

	return release
}

func runningStatus(pid int) *Status {
	return &Status{State: StateRunning, PID: pid, StartedAt: testNow}
}

// wantOrphanTail checks that stream is prefix followed by a JOB_ORPHANED
// error and a result with exit code 1, numbered on from the prefix.
func wantOrphanTail(t *testing.T, stream, prefix []byte) {
	t.Helper()

	if !bytes.HasPrefix(stream, prefix) {
		t.Fatalf("stream does not replay the spool\nstream:\n%s\nspool:\n%s", stream, prefix)
	}

	recs := records(t, stream)
	n := len(recs)

	if n < 3 {
		t.Fatalf("stream has %d records, want a header, an error, and a result", n)
	}

	for i, r := range recs {
		if r.Seq != int64(i) {
			t.Fatalf("record %d has seq %d", i, r.Seq)
		}
	}

	if recs[n-2].Kind != progress.KindError || recs[n-1].Kind != progress.KindResult {
		t.Fatalf("stream ends in %s, %s; want error, result", recs[n-2].Kind, recs[n-1].Kind)
	}

	if e := bodyOf[progress.ErrorBody](t, recs[n-2]); e.Code != progress.CodeJobOrphaned {
		t.Fatalf("error code = %s, want %s", e.Code, progress.CodeJobOrphaned)
	}

	if r := bodyOf[progress.ResultBody](t, recs[n-1]); r.ExitCode != 1 {
		t.Fatalf("result exit code = %d, want 1", r.ExitCode)
	}
}

// wantOrphanError checks that err reports an orphaned job without being a
// refusal, so that the command exits with status 1.
func wantOrphanError(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("Run reported success for an orphaned job")
	}

	if r, ok := progress.AsRefusal(err); ok {
		t.Fatalf("orphaned job is a refusal %v, want a plain error", r)
	}

	if !strings.Contains(err.Error(), string(progress.CodeJobOrphaned)) {
		t.Fatalf("error %q does not name %s", err, progress.CodeJobOrphaned)
	}
}

func TestAttachToOrphanedSpool(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	s := partialSpool(t, root, runningStatus(4242))
	out := readFile(t, filepath.Join(s.Dir, "out"))
	status := readFile(t, filepath.Join(s.Dir, "status.json"))

	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))

	var asked []string

	rc.opts.held = func(f *os.File) (bool, error) {
		asked = append(asked, f.Name())

		return outHeld(f)
	}

	err := rc.run(t)
	wantOrphanError(t, err)
	wantOrphanTail(t, rc.stdout.Bytes(), out)

	if !slices.Contains(asked, filepath.Join(s.Dir, "out")) {
		t.Fatalf("the lock was checked on %v, want out", asked)
	}

	if n := rc.host.calls.Load(); n != 0 {
		t.Fatalf("an attach called Hostname %d times", n)
	}

	// An attach writes nothing into the spool.
	if got := readFile(t, filepath.Join(s.Dir, "out")); !bytes.Equal(got, out) {
		t.Fatal("the attach changed out")
	}

	if got := readFile(t, filepath.Join(s.Dir, "status.json")); !bytes.Equal(got, status) {
		t.Fatal("the attach changed status.json")
	}

	wantNoPath(t, filepath.Join(s.Dir, "exit.json"))
}

func TestRunRefusesOtherVerbs(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	req := goodRequest()
	req.Verb = "deploy"

	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, req))

	err := rc.run(t)
	r := wantRefusal(t, err, progress.CodeVerbNotAllowed)

	if !strings.Contains(r.Message, `"deploy"`) {
		t.Fatalf("message %q does not name the verb", r.Message)
	}

	wantRefusalStream(t, rc.stdout.Bytes(), progress.CodeVerbNotAllowed, "job-0001")
	wantNoPath(t, filepath.Join(root, "job-0001"))
}

// A verb is quoted before it reaches a message, so no control or
// bidirectional character from a request reaches a terminal.
func TestRunQuotesARefusedVerb(t *testing.T) {
	t.Parallel()

	req := goodRequest()
	req.Verb = "noop\x1b[2J\u202e"

	rc := newRun(t, storeWith(t, liveRecord()), spoolRoot(t), signedRequest(t, req))

	err := rc.run(t)
	r := wantRefusal(t, err, progress.CodeVerbNotAllowed)

	if strings.ContainsAny(r.Message, "\x1b\u202e") {
		t.Fatalf("message %q holds a raw control or bidirectional character", r.Message)
	}
}

func TestRunConcurrentSameJob(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	store := storeWith(t, liveRecord())
	sr := signedRequest(t, goodRequest())

	host := &hostCounter{}
	runs := []*runCase{newRun(t, store, root, sr), newRun(t, store, root, sr)}

	for _, rc := range runs {
		rc.opts.Hostname = host.hostname
		// The real check of the lock that the run which made the spool
		// holds on out.
		rc.opts.held = nil
		// Each poll waits as long in real time as the clock moves on, so
		// the grace period for status.json is a real one.
		rc.clock.onWait = func(int) { time.Sleep(pollInterval) }
	}

	start := make(chan struct{})
	errs := make([]error, len(runs))

	var wg sync.WaitGroup

	for i, rc := range runs {
		wg.Go(func() {
			<-start

			errs[i] = rc.run(t)
		})
	}

	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("run %d = %v", i, err)
		}
	}

	if !bytes.Equal(runs[0].stdout.Bytes(), runs[1].stdout.Bytes()) {
		t.Fatalf("the two runs streamed different records\n%s\n%s", runs[0].stdout, runs[1].stdout)
	}

	if n := host.calls.Load(); n != 1 {
		t.Fatalf("Hostname was called %d times, want 1", n)
	}

	out := readFile(t, filepath.Join(root, "job-0001", "out"))
	if !bytes.Equal(out, runs[0].stdout.Bytes()) {
		t.Fatal("out differs from the streams")
	}
}

// A run that stopped after making its spool directory and before writing
// status.json leaves a spool that no process will finish. An attach waits a
// grace period for the status and then reports the job orphaned.
func TestAttachToSpoolWithoutStatus(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)

	_, created, err := CreateSpool(root, "job-0001")
	if err != nil || !created {
		t.Fatalf("CreateSpool = %v, %v", created, err)
	}

	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
	rc.opts.held = func(*os.File) (bool, error) {
		t.Error("the lock on out was checked without a status")
		return true, nil
	}

	err = rc.run(t)
	wantOrphanError(t, err)

	// With nothing in out, the attach writes its own header.
	recs := records(t, rc.stdout.Bytes())
	if len(recs) != 3 {
		t.Fatalf("stream has %d records, want 3:\n%s", len(recs), rc.stdout)
	}

	if h := bodyOf[progress.Header](t, recs[0]); h.JobID != "job-0001" || h.Version != version.Get().Version {
		t.Fatalf("header = %+v", h)
	}

	wantOrphanTail(t, rc.stdout.Bytes(), nil)

	waits := rc.clock.waited()
	if len(waits) == 0 {
		t.Fatal("the attach did not wait for the status")
	}

	var total time.Duration

	for _, w := range waits {
		if w != pollInterval {
			t.Fatalf("the attach waited %s, want %s each time", w, pollInterval)
		}

		total += w
	}

	if total < statusGrace {
		t.Fatalf("the attach gave up after %s, before the grace period of %s", total, statusGrace)
	}

	if total > statusGrace+pollInterval {
		t.Fatalf("the attach waited %s, more than the grace period of %s", total, statusGrace)
	}
}

// A status written by a run that died before it wrote any record gives a
// stream with only the attach's own header, error, and result.
func TestAttachToDeadRunWithEmptyOut(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)

	s, _, err := CreateSpool(root, "job-0001")
	if err != nil {
		t.Fatal(err)
	}

	must(t, os.WriteFile(filepath.Join(s.Dir, "out"), nil, 0o600))
	must(t, s.WriteStatus(*runningStatus(4242)))

	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
	rc.opts.held = nil // nothing holds out

	err = rc.run(t)
	wantOrphanError(t, err)
	wantOrphanTail(t, rc.stdout.Bytes(), nil)

	if n := len(records(t, rc.stdout.Bytes())); n != 3 {
		t.Fatalf("stream has %d records, want 3", n)
	}
}

// An attach to a running job follows out every pollInterval and finishes
// when exit.json appears.
func TestAttachFollowsARunningJob(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	s := partialSpool(t, root, runningStatus(4242))
	outPath := filepath.Join(s.Dir, "out")

	var full bytes.Buffer

	enc := progress.NewEncoder(&full, fixedClock{testNow})
	must(t, enc.Header(progress.Header{Producer: "ocfp", Version: "1.2.3", JobID: "job-0001"}))
	must(t, enc.Encode(progress.KindEvent, progress.Event{Kind: progress.EventStepStarted, Step: "noop"}))
	must(t, enc.Encode(progress.KindLog, progress.LogBody{Stream: "stdout", Line: "one"}))

	cut := full.Len()

	must(t, enc.Encode(progress.KindEvent, progress.Event{Kind: progress.EventStepFinished, Step: "noop", Outcome: "succeeded"}))
	must(t, enc.Encode(progress.KindResult, progress.ResultBody{ExitCode: 0}))

	release := holdOut(t, s)

	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
	rc.opts.held = nil
	rc.clock.onWait = func(n int) {
		switch n {
		case 1:
			// The rest of the log record and the start of the next one,
			// which the attach must hold back.
			appendLine(t, outPath, string(full.Bytes()[len(readFile(t, outPath)):cut+5]))
		case 2:
			appendLine(t, outPath, string(full.Bytes()[cut+5:]))
		case 3:
			must(t, s.WriteExit(Exit{ExitCode: 0, FinishedAt: testNow}))
			release()
		}
	}

	err := rc.run(t)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}

	if !bytes.Equal(rc.stdout.Bytes(), full.Bytes()) {
		t.Fatalf("attach streamed\n%s\nwant\n%s", rc.stdout, full.Bytes())
	}

	if w := rc.clock.waited(); len(w) != 3 || w[0] != pollInterval {
		t.Fatalf("attach waited %v, want three waits of %s", w, pollInterval)
	}
}

// A run can finish between the attach's look for exit.json and its look at
// the process. The attach then looks for exit.json once more and does not
// call the job orphaned.
func TestAttachSeesExitWrittenAsTheRunEnds(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	s := partialSpool(t, root, runningStatus(4242))

	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
	rc.opts.held = func(*os.File) (bool, error) {
		appendLine(t, filepath.Join(s.Dir, "out"), resultLine)
		must(t, s.WriteExit(Exit{ExitCode: 0, FinishedAt: testNow}))

		return false, nil
	}

	err := rc.run(t)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}

	recs := records(t, rc.stdout.Bytes())
	if last := recs[len(recs)-1]; last.Kind != progress.KindResult || last.Seq != 2 {
		t.Fatalf("stream ends in %s seq %d, want the run's own result", last.Kind, last.Seq)
	}
}

// A run that died part way through a record leaves half a line. The attach
// drops it and numbers its own records on from the last whole one.
func TestAttachDropsAPartialRecord(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	s := partialSpool(t, root, runningStatus(4242))
	whole := readFile(t, filepath.Join(s.Dir, "out"))

	appendLine(t, filepath.Join(s.Dir, "out"), `{"kind":"log","seq":2,"ti`)

	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
	rc.opts.held = nil // nothing holds out

	err := rc.run(t)
	wantOrphanError(t, err)
	wantOrphanTail(t, rc.stdout.Bytes(), whole)

	if n := len(records(t, rc.stdout.Bytes())); n != 4 {
		t.Fatalf("stream has %d records, want the 2 whole ones and 2 of the attach's", n)
	}
}

// A run that wrote its result and died before exit.json has a complete
// stream. The attach replays it and adds nothing after the result.
func TestAttachToDeadRunWithAResult(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	s := partialSpool(t, root, runningStatus(4242))

	appendLine(t, filepath.Join(s.Dir, "out"), resultLine)

	out := readFile(t, filepath.Join(s.Dir, "out"))

	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
	rc.opts.held = nil // nothing holds out

	err := rc.run(t)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}

	if !bytes.Equal(rc.stdout.Bytes(), out) {
		t.Fatalf("attach streamed\n%s\nwant only the spool\n%s", rc.stdout, out)
	}
}

// After a reboot, or once enough processes have come and gone, the pid in a
// dead run's status.json can name an unrelated live process. The attach
// must still report the job orphaned rather than wait for that process, so
// it asks the lock on out and never the pid. A pid of 0 or less, which names
// no single process, is no different.
func TestAttachIgnoresARecycledPID(t *testing.T) {
	t.Parallel()

	for _, pid := range []int{os.Getpid(), 1, 0, -1} {
		root := spoolRoot(t)
		s := partialSpool(t, root, runningStatus(pid))
		out := readFile(t, filepath.Join(s.Dir, "out"))

		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)

		rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
		rc.opts.held = nil // nothing holds out

		err := Run(ctx, rc.opts)

		cancel()
		wantOrphanError(t, err)
		wantOrphanTail(t, rc.stdout.Bytes(), out)
	}
}

func TestAttachRefusesADamagedSpool(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"not json":       "not json\n",
		"seq skips":      `{"kind":"header","seq":0,"time":"2026-11-11T12:00:00Z","body":{}}` + "\n" + `{"kind":"log","seq":5,"time":"2026-11-11T12:00:00Z","body":{}}` + "\n",
		"no header":      `{"kind":"log","seq":0,"time":"2026-11-11T12:00:00Z","body":{}}` + "\n",
		"record after":   `{"kind":"header","seq":0,"time":"2026-11-11T12:00:00Z","body":{}}` + "\n" + `{"kind":"result","seq":1,"time":"2026-11-11T12:00:00Z","body":{"exitCode":0}}` + "\n" + `{"kind":"log","seq":2,"time":"2026-11-11T12:00:00Z","body":{}}` + "\n",
		"control output": `{"kind":"header","seq":0,"time":"2026-11-11T12:00:00Z","body":{}}` + "\n\x1b[2J\n",
		"bad utf-8":      `{"kind":"header","seq":0,"time":"2026-11-11T12:00:00Z","body":{"x":"` + "\xff" + `"}}` + "\n",
		"endless record": strings.Repeat("x", maxRecordLine+1),
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := spoolRoot(t)
			s := partialSpool(t, root, runningStatus(4242))
			must(t, os.WriteFile(filepath.Join(s.Dir, "out"), []byte(content), 0o600))

			rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
			rc.opts.held = nil // nothing holds out

			err := rc.run(t)
			if err == nil {
				t.Fatal("Run accepted a damaged spool")
			}

			if _, ok := progress.AsRefusal(err); ok {
				t.Fatalf("a damaged spool is a refusal: %v", err)
			}

			if strings.ContainsRune(err.Error(), '\x1b') {
				t.Fatalf("error %q holds a raw control character", err)
			}

			if bytes.Contains(rc.stdout.Bytes(), []byte("\x1b")) {
				t.Fatal("the attach copied a raw control character to stdout")
			}
		})
	}
}

// A run whose stdout fails, as when the requester goes away, still finishes
// the job into the spool, so that a relaunch can attach to it.
func TestRunFinishesTheSpoolWhenStdoutFails(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	store := storeWith(t, liveRecord())
	sr := signedRequest(t, goodRequest())

	rc := newRun(t, store, root, sr)
	rc.opts.Stdout = failingWriter{}

	err := rc.run(t)
	if err == nil {
		t.Fatal("Run hid the failed stdout")
	}

	if _, ok := progress.AsRefusal(err); ok {
		t.Fatalf("a failed stdout is a refusal: %v", err)
	}

	dir := filepath.Join(root, "job-0001")
	wantExitShape(t, readFile(t, filepath.Join(dir, "exit.json")), 0)

	again := newRun(t, store, root, sr)

	err = again.run(t)
	if err != nil {
		t.Fatalf("relaunch = %v", err)
	}

	if out := readFile(t, filepath.Join(dir, "out")); !bytes.Equal(again.stdout.Bytes(), out) || len(records(t, out)) != 5 {
		t.Fatalf("relaunch streamed\n%s\nwant the five records in out\n%s", again.stdout, out)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestRunNoopReportsAHostnameFailure(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)

	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
	rc.opts.Hostname = func() (string, error) { return "", errors.New("no name\x1b") }

	err := rc.run(t)
	if err == nil {
		t.Fatal("Run reported success when the host name failed")
	}

	if _, ok := progress.AsRefusal(err); ok {
		t.Fatalf("a failed verb is a refusal: %v", err)
	}

	recs := records(t, rc.stdout.Bytes())

	kinds := make([]progress.Kind, 0, len(recs))
	for _, r := range recs {
		kinds = append(kinds, r.Kind)
	}

	want := []progress.Kind{progress.KindHeader, progress.KindEvent, progress.KindLog, progress.KindEvent, progress.KindResult}
	if !slices.Equal(kinds, want) {
		t.Fatalf("record kinds = %v, want %v", kinds, want)
	}

	if l := bodyOf[progress.LogBody](t, recs[2]); l.Stream != "stderr" || !strings.Contains(l.Line, "no name") {
		t.Fatalf("log = %+v, want the failure on stderr", l)
	}

	if ev := bodyOf[progress.Event](t, recs[3]); ev.Kind != progress.EventStepFinished || ev.Outcome != "failed" {
		t.Fatalf("event = %+v, want step_finished failed", ev)
	}

	if r := bodyOf[progress.ResultBody](t, recs[4]); r.ExitCode != 1 {
		t.Fatalf("result exit code = %d, want 1", r.ExitCode)
	}

	dir := filepath.Join(root, "job-0001")
	wantExitShape(t, readFile(t, filepath.Join(dir, "exit.json")), 1)

	var st Status

	decodeStrictFile(t, filepath.Join(dir, "status.json"), &st)

	if st.State != StateExited || st.ExitCode == nil || *st.ExitCode != 1 {
		t.Fatalf("status = %+v, want exited with code 1", st)
	}
}

// Run reads exactly one line from stdin and leaves the rest for the job.
func TestRunReadsOneLine(t *testing.T) {
	t.Parallel()

	rc := newRun(t, storeWith(t, liveRecord()), spoolRoot(t), signedRequest(t, goodRequest()))

	line := append(mustMarshal(t, signedRequest(t, goodRequest())), '\n')
	stdin := bytes.NewReader(append(line, "answer-1\n"...))
	rc.opts.Stdin = stdin

	err := rc.run(t)
	if err != nil {
		t.Fatal(err)
	}

	rest, err := io.ReadAll(stdin)
	must(t, err)

	if string(rest) != "answer-1\n" {
		t.Fatalf("stdin left %q, want the line after the request", rest)
	}
}

// A request line with no newline before end of input is still one line.
func TestRunAcceptsALastLineWithoutNewline(t *testing.T) {
	t.Parallel()

	rc := newRun(t, storeWith(t, liveRecord()), spoolRoot(t), signedRequest(t, goodRequest()))
	rc.opts.Stdin = bytes.NewReader(mustMarshal(t, signedRequest(t, goodRequest())))

	err := rc.run(t)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunRefusesABadRequestLine(t *testing.T) {
	t.Parallel()

	good := mustMarshal(t, signedRequest(t, goodRequest()))

	cases := map[string][]byte{
		"not json":      []byte("hello\x1b[2J\n"),
		"unknown field": append(append([]byte(`{"extra":1,`), good[1:]...), '\n'),
		"two values":    append(append(slices.Clone(good), good...), '\n'),
		"too long":      append(bytes.Repeat([]byte(" "), maxRequestLine+1), '\n'),
	}

	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := spoolRoot(t)
			rc := newRun(t, storeWith(t, liveRecord()), root, SignedRequest{})
			rc.opts.Stdin = bytes.NewReader(line)

			err := rc.run(t)
			r := wantRefusal(t, err, progress.CodeJobRequestInvalid)

			if strings.ContainsRune(r.Message, '\x1b') {
				t.Fatalf("message %q holds a raw control character", r.Message)
			}

			wantRefusalStream(t, rc.stdout.Bytes(), progress.CodeJobRequestInvalid, "")
			wantNoPath(t, filepath.Join(root, "job-0001"))
		})
	}
}

func TestRunFailsOnUnreadableStdin(t *testing.T) {
	t.Parallel()

	for name, stdin := range map[string]io.Reader{
		"empty":      bytes.NewReader(nil),
		"read error": io.MultiReader(strings.NewReader(`{"format":1`), errReader{}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rc := newRun(t, storeWith(t, liveRecord()), spoolRoot(t), SignedRequest{})
			rc.opts.Stdin = stdin

			err := rc.run(t)
			if err == nil {
				t.Fatal("Run accepted unreadable stdin")
			}

			if _, ok := progress.AsRefusal(err); ok {
				t.Fatalf("unreadable stdin is a refusal: %v", err)
			}
		})
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

// A run holds the shared lock on the owner record while the verb runs, so
// the record cannot be replaced under it, and gives it back when it ends.
func TestRunHoldsTheOwnerLockWhileTheVerbRuns(t *testing.T) {
	t.Parallel()

	store := storeWith(t, liveRecord())
	rc := newRun(t, store, spoolRoot(t), signedRequest(t, goodRequest()))

	var during error

	rc.opts.Hostname = func() (string, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()

		during = exclusiveAttempt(t)(ctx, store)

		return testHost, nil
	}

	err := rc.run(t)
	if err != nil {
		t.Fatal(err)
	}

	if !errors.Is(during, context.DeadlineExceeded) {
		t.Fatalf("an exclusive lock attempt during the verb = %v, want it to wait", during)
	}

	wantUnlocked(t, store)
}

// An attach acts on nothing the record guards, so it gives the lock back
// before it follows the spool.
func TestAttachGivesTheOwnerLockBack(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	store := storeWith(t, liveRecord())
	s := partialSpool(t, root, runningStatus(4242))

	release := holdOut(t, s)

	rc := newRun(t, store, root, signedRequest(t, goodRequest()))
	rc.opts.held = nil
	rc.clock.onWait = func(n int) {
		if n == 1 {
			wantUnlocked(t, store)
			appendLine(t, filepath.Join(s.Dir, "out"), resultLine)
			must(t, s.WriteExit(Exit{ExitCode: 0, FinishedAt: testNow}))
			release()
		}
	}

	err := rc.run(t)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunStopsWaitingWhenTheContextEnds(t *testing.T) {
	t.Parallel()

	root := spoolRoot(t)
	holdOut(t, partialSpool(t, root, runningStatus(4242)))

	ctx, cancel := context.WithCancel(t.Context())

	rc := newRun(t, storeWith(t, liveRecord()), root, signedRequest(t, goodRequest()))
	rc.opts.held = nil
	rc.clock.onWait = func(n int) {
		if n == 2 {
			cancel()
		}
	}

	err := Run(ctx, rc.opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want the context's error", err)
	}
}

func TestRunNeedsItsOptions(t *testing.T) {
	t.Parallel()

	base := func() Options {
		return newRun(t, storeWith(t, liveRecord()), spoolRoot(t), signedRequest(t, goodRequest())).opts
	}

	for name, change := range map[string]func(*Options){
		"spool root": func(o *Options) { o.SpoolRoot = "" },
		"owner":      func(o *Options) { o.Owner = nil },
		"stdin":      func(o *Options) { o.Stdin = nil },
		"stdout":     func(o *Options) { o.Stdout = nil },
	} {
		o := base()
		change(&o)

		if err := Run(t.Context(), o); err == nil {
			t.Errorf("Run without its %s succeeded", name)
		}
	}
}
