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

func TestJobCanceledCodeIsStable(t *testing.T) {
	t.Parallel()

	if string(progress.CodeJobCanceled) != "JOB_CANCELED" {
		t.Fatalf("CodeJobCanceled = %q, want JOB_CANCELED", progress.CodeJobCanceled)
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

// The warning that a canceled job's stream carries lists the work its undo
// hooks left undone as typed entries, each with whether its hook did not run
// or failed, so a requester parses the list rather than the message.
func TestTheUndoneWarningIsStable(t *testing.T) {
	t.Parallel()

	got, err := json.Marshal(progress.Event{
		Kind:    progress.EventWarning,
		Step:    "cancel",
		Message: "undo hooks that did not run: remote-task task-1234; undo hooks that failed: lease lease-7",
		Undone: []progress.UndoneWork{
			{WorkRef: progress.WorkRef{Kind: "remote-task", ID: "task-1234"}, State: progress.UndoNotRun},
			{WorkRef: progress.WorkRef{Kind: "lease", ID: "lease-7"}, State: progress.UndoFailed},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := `{"kind":"warning","step":"cancel","message":"undo hooks that did not run: remote-task task-1234; undo hooks that failed: lease lease-7","undone":[{"kind":"remote-task","id":"task-1234","state":"not_run"},{"kind":"lease","id":"lease-7","state":"failed"}]}`
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

func TestCancelOutcomeOfAStoppedStream(t *testing.T) {
	t.Parallel()

	got, ok := progress.CancelOutcomeOf(
		&progress.ErrorBody{Code: progress.CodeJobCanceled, Message: "job job-0001 was canceled and stopped at a safe point"},
		progress.ResultBody{ExitCode: 1, Cancel: progress.CancelStopped})
	if !ok || got != progress.CancelStopped {
		t.Fatalf("outcome = %q, %v; want stopped", got, ok)
	}
}

func TestCancelOutcomeOfAKilledStream(t *testing.T) {
	t.Parallel()

	got, ok := progress.CancelOutcomeOf(
		&progress.ErrorBody{Code: progress.CodeJobCanceled, Message: "job job-0001 was canceled and killed when its cancel budget of 5s passed"},
		progress.ResultBody{ExitCode: 1, Cancel: progress.CancelKilled})
	if !ok || got != progress.CancelKilled {
		t.Fatalf("outcome = %q, %v; want killed", got, ok)
	}
}

// A complete stream with no cancel in its result, and no error, is a job
// that had finished before the cancel could stop it, whatever its exit
// code.
func TestCancelOutcomeOfAFinishedStream(t *testing.T) {
	t.Parallel()

	for _, code := range []int{0, 1, 3} {
		got, ok := progress.CancelOutcomeOf(nil, progress.ResultBody{ExitCode: code})
		if !ok || got != progress.CancelAlreadyFinished {
			t.Errorf("outcome of exit code %d = %q, %v; want already_finished", code, got, ok)
		}
	}
}

// A stream that ends in JOB_ORPHANED, in a refusal, or in a result whose
// cancel this CLI does not know says nothing of how a cancel ended the job.
func TestAnOrphanedStreamHasNoCancelOutcome(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		err *progress.ErrorBody
		res progress.ResultBody
	}{
		"orphaned": {&progress.ErrorBody{Code: progress.CodeJobOrphaned, Message: "job job-0001 is orphaned"}, progress.ResultBody{ExitCode: 1}},
		"refused":  {&progress.ErrorBody{Code: progress.CodeJobNotFound, Message: "job job-0001 has no spool on this host"}, progress.ResultBody{ExitCode: 2}},
		"unknown":  {&progress.ErrorBody{Code: progress.CodeJobCanceled, Message: "job job-0001 was canceled"}, progress.ResultBody{ExitCode: 1, Cancel: "unknown-outcome"}},
		"stored":   {nil, progress.ResultBody{ExitCode: 0, Cancel: progress.CancelAlreadyFinished}},
	} {
		if got, ok := progress.CancelOutcomeOf(tc.err, tc.res); ok || got != "" {
			t.Errorf("%s: outcome = %q, %v; want none", name, got, ok)
		}
	}
}
