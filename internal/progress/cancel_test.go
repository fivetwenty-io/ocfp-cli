package progress_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

func TestCancelOutcomesAreStable(t *testing.T) {
	t.Parallel()

	for want, got := range map[string]progress.CancelOutcome{
		"stopped":          progress.CancelStopped,
		"killed":           progress.CancelKilled,
		"already_finished": progress.CancelAlreadyFinished,
	} {
		if string(got) != want {
			t.Errorf("outcome %q has value %q", want, got)
		}
	}
}

func TestJobCancelledCodeIsStable(t *testing.T) {
	t.Parallel()

	if string(progress.CodeJobCancelled) != "JOB_CANCELLED" {
		t.Fatalf("CodeJobCancelled = %q, want JOB_CANCELLED", progress.CodeJobCancelled)
	}
}

func TestResultWithoutACancelEncodesAsBefore(t *testing.T) {
	t.Parallel()

	got, err := json.Marshal(progress.ResultBody{ExitCode: 1})
	if err != nil {
		t.Fatal(err)
	}

	if want := `{"exitCode":1}`; string(got) != want {
		t.Fatalf("result = %s, want %s", got, want)
	}

	got, err = json.Marshal(progress.ResultBody{ExitCode: 1, Cancel: progress.CancelKilled})
	if err != nil {
		t.Fatal(err)
	}

	if want := `{"exitCode":1,"cancel":"killed"}`; string(got) != want {
		t.Fatalf("result = %s, want %s", got, want)
	}
}

func TestHeaderCarriesTheCancelBudgetOnlyWhenSet(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	enc := progress.NewEncoder(&buf, newClock())

	err := enc.Header(progress.Header{Producer: "ocfp", Version: "1.2.3", JobID: "job-1", CancelBudgetMs: 5000})
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(buf.Bytes(), []byte(`"cancelBudgetMs":5000`)) {
		t.Fatalf("header lacks the budget: %s", buf.Bytes())
	}

	buf.Reset()

	enc = progress.NewEncoder(&buf, newClock())

	err = enc.Header(progress.Header{Producer: "ocfp", Version: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(buf.Bytes(), []byte("cancelBudgetMs")) {
		t.Fatalf("header without a verb names a budget: %s", buf.Bytes())
	}
}

// The warning that a killed job's stream carries lists the undo hooks that
// never ran as typed entries, so a requester parses the list rather than the
// message.
func TestTheUndoneWarningIsStable(t *testing.T) {
	t.Parallel()

	got, err := json.Marshal(progress.Event{
		Kind:    progress.EventWarning,
		Step:    "cancel",
		Message: "undo hooks that did not run: remote-task task-1234",
		Undone:  []progress.WorkRef{{Kind: "remote-task", ID: "task-1234"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := `{"kind":"warning","step":"cancel","message":"undo hooks that did not run: remote-task task-1234","undone":[{"kind":"remote-task","id":"task-1234"}]}`
	if string(got) != want {
		t.Fatalf("warning = %s, want %s", got, want)
	}

	// An entry always names its id, even an empty one, so that every
	// entry has the same two keys.
	got, err = json.Marshal(progress.WorkRef{Kind: "lease"})
	if err != nil {
		t.Fatal(err)
	}

	if want := `{"kind":"lease","id":""}`; string(got) != want {
		t.Fatalf("entry = %s, want %s", got, want)
	}

	// Every other event encodes as before.
	got, err = json.Marshal(progress.Event{Kind: progress.EventStepStarted, Step: "noop"})
	if err != nil {
		t.Fatal(err)
	}

	if want := `{"kind":"step_started","step":"noop"}`; string(got) != want {
		t.Fatalf("event = %s, want %s", got, want)
	}
}

func TestAWorkRefNamesItself(t *testing.T) {
	t.Parallel()

	for ref, want := range map[progress.WorkRef]string{
		{Kind: "remote-task", ID: "task-1234"}: "remote-task task-1234",
		{Kind: "lease"}:                        "lease",
	} {
		if got := ref.String(); got != want {
			t.Errorf("%+v names itself %q, want %q", ref, got, want)
		}
	}
}
