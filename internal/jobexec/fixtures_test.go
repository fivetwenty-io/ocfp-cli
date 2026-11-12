package jobexec

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress/progresstest"
)

// fixtureStart is the instant the status and exit fixtures start from.
var fixtureStart = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

// spoolWith returns a spool that holds the named fixture as the named file.
func spoolWith(t *testing.T, name, fixture string) Spool {
	t.Helper()

	s, _, err := CreateSpool(spoolRoot(t), "job-fixture-01")
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(filepath.Join(s.Dir, name), progresstest.Fixture(fixture), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func TestStatusFixtureLoadsIntoJobexecType(t *testing.T) {
	t.Parallel()

	st, ok, err := spoolWith(t, statusFile, "status-v1.json").readStatus()
	if err != nil || !ok {
		t.Fatalf("readStatus = %v, %v", ok, err)
	}

	if st.State != StateRunning || st.PID != 4242 || !st.StartedAt.Equal(fixtureStart) || st.ExitCode != nil {
		t.Fatalf("status = %+v, want running by pid 4242 since %s with no exit code", st, fixtureStart)
	}

	// The fixture is what the real writer produces for the same value.
	s, _, err := CreateSpool(spoolRoot(t), "job-fixture-01")
	if err != nil {
		t.Fatal(err)
	}

	must(t, s.WriteStatus(st))

	got, want := readFile(t, filepath.Join(s.Dir, statusFile)), progresstest.Fixture("status-v1.json")
	if string(got) != string(want) {
		t.Fatalf("WriteStatus wrote\n%s\nthe fixture holds\n%s", got, want)
	}
}

func TestExitFixtureLoadsIntoJobexecType(t *testing.T) {
	t.Parallel()

	e, ok, err := spoolWith(t, exitFile, "exit-v1.json").readExit()
	if err != nil || !ok {
		t.Fatalf("readExit = %v, %v", ok, err)
	}

	if e.ExitCode != 0 || !e.FinishedAt.Equal(fixtureStart.Add(2*time.Second)) {
		t.Fatalf("exit = %+v, want code 0 at %s", e, fixtureStart.Add(2*time.Second))
	}

	s, _, err := CreateSpool(spoolRoot(t), "job-fixture-01")
	if err != nil {
		t.Fatal(err)
	}

	must(t, s.WriteExit(e))

	got, want := readFile(t, filepath.Join(s.Dir, exitFile)), progresstest.Fixture("exit-v1.json")
	if string(got) != string(want) {
		t.Fatalf("WriteExit wrote\n%s\nthe fixture holds\n%s", got, want)
	}
}

func TestExitedStatusFixture(t *testing.T) {
	t.Parallel()

	st, ok, err := spoolWith(t, statusFile, "exit-status-exited-v1.json").readStatus()
	if err != nil || !ok {
		t.Fatalf("readStatus = %v, %v", ok, err)
	}

	if st.State != StateExited || st.ExitCode == nil || *st.ExitCode != 0 {
		t.Fatalf("status = %+v, want exited with code 0", st)
	}

	if st.PID != 4242 || !st.StartedAt.Equal(fixtureStart) {
		t.Fatalf("status = %+v, want pid 4242 since %s", st, fixtureStart)
	}

	s, _, err := CreateSpool(spoolRoot(t), "job-fixture-01")
	if err != nil {
		t.Fatal(err)
	}

	must(t, s.WriteStatus(st))

	got, want := readFile(t, filepath.Join(s.Dir, statusFile)), progresstest.Fixture("exit-status-exited-v1.json")
	if string(got) != string(want) {
		t.Fatalf("WriteStatus wrote\n%s\nthe fixture holds\n%s", got, want)
	}
}

// A refused request is refused before the payload is read, so the stream's
// header names no job. The fixture must show that, and match what Run writes.
func TestRefusedStreamFixtureMatchesRun(t *testing.T) {
	t.Parallel()

	store, _ := emptyStore(t)
	rc := newRun(t, store, spoolRoot(t), signedRequest(t, fixtureRequest()))

	wantRefusal(t, rc.run(t), progress.CodeOwnerRecordMissing)

	got := records(t, rc.stdout.Bytes())
	want := records(t, progresstest.Fixture("stream-refused-v1.ndjson"))

	if len(got) != len(want) {
		t.Fatalf("stream has %d records, the fixture %d:\n%s", len(got), len(want), rc.stdout)
	}

	for i := range got {
		if g, w := normalized(t, fixturePlaceholders(t, got[i])), normalized(t, want[i]); g != w {
			t.Errorf("record %d = %s\nwant %s", i, g, w)
		}
	}

	if h := bodyOf[progress.Header](t, want[0]); h.JobID != "" {
		t.Errorf("fixture header names job %q, a refused stream's names none", h.JobID)
	}
}

// The newer-fields fixture carries fields this reader does not know, but the
// records it shares with a no-op run must be what a no-op run writes. With the
// telemetry record dropped, its kinds and its known body fields must equal the
// no-op stream, apart from the version of its producer.
func TestNewerFieldsFixtureKnownPartMatchesNoop(t *testing.T) {
	t.Parallel()

	noop := records(t, progresstest.Fixture("stream-noop-v1.ndjson"))

	var known []progress.Record

	for _, r := range looseRecords(t, progresstest.Fixture("stream-newer-fields-v1.ndjson")) {
		if r.Kind != progress.Kind("telemetry") {
			known = append(known, r)
		}
	}

	if len(known) != len(noop) {
		t.Fatalf("newer stream has %d known records, the no-op stream %d", len(known), len(noop))
	}

	for i := range known {
		if known[i].Kind != noop[i].Kind {
			t.Fatalf("record %d is %s, the no-op stream has %s", i, known[i].Kind, noop[i].Kind)
		}

		// The telemetry record sits at seq 1, so every record after the
		// header is one later than in the no-op stream.
		wantSeq := noop[i].Seq
		if i > 0 {
			wantSeq++
		}

		if known[i].Seq != wantSeq {
			t.Errorf("record %d has seq %d, want %d", i, known[i].Seq, wantSeq)
		}

		var got, want any

		switch known[i].Kind {
		case progress.KindHeader:
			got, want = looseBody[progress.Header](t, known[i]), bodyOf[progress.Header](t, noop[i])
			got.(*progress.Header).Version = want.(progress.Header).Version
		case progress.KindEvent:
			got, want = looseBody[progress.Event](t, known[i]), bodyOf[progress.Event](t, noop[i])
		case progress.KindLog:
			got, want = looseBody[progress.LogBody](t, known[i]), bodyOf[progress.LogBody](t, noop[i])
		default:
			got, want = looseBody[progress.ResultBody](t, known[i]), bodyOf[progress.ResultBody](t, noop[i])
		}

		if g, w := string(mustMarshal(t, got)), string(mustMarshal(t, want)); g != w {
			t.Errorf("record %d body = %s, the no-op stream has %s", i, g, w)
		}
	}
}

// looseBody decodes a record's body and ignores the fields it does not know.
func looseBody[T any](t *testing.T, r progress.Record) *T {
	t.Helper()

	v := new(T)

	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("%s body %s: %v", r.Kind, r.Body, err)
	}

	return v
}

// looseRecords decodes a stream and ignores the record fields it does not
// know.
func looseRecords(t *testing.T, stream []byte) []progress.Record {
	t.Helper()

	var out []progress.Record

	for line := range bytes.Lines(stream) {
		var r progress.Record

		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("stream line %q: %v", line, err)
		}

		out = append(out, r)
	}

	return out
}

// The newer-fields fixture exists to carry fields this reader does not know.
// Reading its raw lines as generic JSON, it must hold at least one such key
// on the record line, in the header body, and in the step_finished event.
func TestNewerFieldsFixtureKeepsUnknownFields(t *testing.T) {
	t.Parallel()

	var lines []map[string]json.RawMessage

	for line := range bytes.Lines(progresstest.Fixture("stream-newer-fields-v1.ndjson")) {
		var m map[string]json.RawMessage

		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("stream line %q: %v", line, err)
		}

		lines = append(lines, m)
	}

	unknown := func(raw map[string]json.RawMessage, known ...string) []string {
		var extra []string

		for k := range raw {
			if !slices.Contains(known, k) {
				extra = append(extra, k)
			}
		}

		return extra
	}

	body := func(m map[string]json.RawMessage) map[string]json.RawMessage {
		var b map[string]json.RawMessage

		if err := json.Unmarshal(m["body"], &b); err != nil {
			t.Fatalf("body %s: %v", m["body"], err)
		}

		return b
	}

	if len(lines) < 5 {
		t.Fatalf("the fixture has %d lines, want at least 5", len(lines))
	}

	if len(unknown(lines[0], "kind", "seq", "time", "body")) == 0 {
		t.Error("the header record line has no key the record type does not know")
	}

	if len(unknown(body(lines[0]), "wireContract", "producer", "version", "jobId")) == 0 {
		t.Error("the header body has no key the header type does not know")
	}

	var finished map[string]json.RawMessage

	for _, m := range lines {
		var kind string

		_ = json.Unmarshal(m["kind"], &kind)

		if b := body(m); kind == "event" && string(b["kind"]) == `"step_finished"` {
			finished = b
		}
	}

	if finished == nil {
		t.Fatal("the fixture has no step_finished event")
	}

	if len(unknown(finished, "kind", "step", "message", "current", "total", "outcome", "artifact")) == 0 {
		t.Error("the step_finished event has no key the event type does not know")
	}
}
