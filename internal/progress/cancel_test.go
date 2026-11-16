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
