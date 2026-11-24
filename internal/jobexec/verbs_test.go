package jobexec

import (
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

func TestEveryVerbDeclaresABudget(t *testing.T) {
	t.Parallel()

	if len(verbs) == 0 {
		t.Fatal("the verb table is empty")
	}

	for name, v := range verbs {
		if v.name != name {
			t.Errorf("verb %q is filed under %q", v.name, name)
		}

		if v.cancelBudget <= 0 {
			t.Errorf("verb %q has the cancel budget %s, want more than zero", name, v.cancelBudget)
		}

		if v.run == nil {
			t.Errorf("verb %q has no run function", name)
		}
	}
}

// TestTheHeaderReportsTheCancelBudget runs each verb in the table and reads
// the budget from its header, in whole milliseconds.
func TestTheHeaderReportsTheCancelBudget(t *testing.T) {
	t.Parallel()

	for name, v := range verbs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			req := goodRequest()
			req.Verb = name

			rc := newRun(t, storeWith(t, liveRecord()), spoolRoot(t), signedRequest(t, req))

			// A verb that needs a host this test lacks, such as genesis.info
			// without Genesis, ends with a failure, and still writes its
			// header.
			err := rc.run(t)
			if err != nil && name == verbNoop {
				t.Fatalf("Run = %v", err)
			}

			recs := records(t, rc.stdout.Bytes())
			h := bodyOf[progress.Header](t, recs[0])

			if want := v.cancelBudget.Milliseconds(); h.CancelBudgetMs != want {
				t.Errorf("header budget = %d ms, want %d ms", h.CancelBudgetMs, want)
			}
		})
	}
}

// TestARefusedVerbHeaderNamesNoBudget checks that a header that names no
// verb carries no budget, and that the refusal lists the table's verbs.
func TestARefusedVerbHeaderNamesNoBudget(t *testing.T) {
	t.Parallel()

	req := goodRequest()
	req.Verb = "rm"

	rc := newRun(t, storeWith(t, liveRecord()), spoolRoot(t), signedRequest(t, req))

	r := wantRefusal(t, rc.run(t), progress.CodeVerbNotAllowed)
	if want := `this host runs only "genesis.info", "noop"`; !strings.Contains(r.Message, want) {
		t.Errorf("message %q does not hold %q", r.Message, want)
	}

	h := bodyOf[progress.Header](t, records(t, rc.stdout.Bytes())[0])
	if h.CancelBudgetMs != 0 {
		t.Errorf("a refusal header names the budget %d", h.CancelBudgetMs)
	}
}
