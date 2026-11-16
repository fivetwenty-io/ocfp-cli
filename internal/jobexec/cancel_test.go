package jobexec

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// newTestJob returns a runtime that writes to a buffer.
func newTestJob(t *testing.T) (*jobRuntime, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer

	enc := progress.NewEncoder(&buf, newFakeClock())
	if err := enc.Header(progress.Header{Producer: producer}); err != nil {
		t.Fatal(err)
	}

	buf.Reset()

	return newJobRuntime(enc), &buf
}

func TestCheckpointReportsACancel(t *testing.T) {
	t.Parallel()

	j, _ := newTestJob(t)

	if err := j.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint before a cancel = %v", err)
	}

	select {
	case <-j.Done():
		t.Fatal("Done was closed before a cancel")
	default:
	}

	j.cancel()
	j.cancel()

	if err := j.Checkpoint(); !errors.Is(err, ErrCancelled) {
		t.Fatalf("Checkpoint after a cancel = %v, want ErrCancelled", err)
	}

	select {
	case <-j.Done():
	default:
		t.Fatal("Done was not closed after a cancel")
	}
}

func TestUndoHooksRunOnceNewestFirst(t *testing.T) {
	t.Parallel()

	j, buf := newTestJob(t)

	var order []string

	for _, name := range []string{"first", "second", "third"} {
		j.OnCancel(name, func(context.Context) error {
			order = append(order, name)

			return nil
		})
	}

	if err := j.runUndo(t.Context(), time.Second); err != nil {
		t.Fatal(err)
	}

	if err := j.runUndo(t.Context(), time.Second); err != nil {
		t.Fatal(err)
	}

	if want := []string{"third", "second", "first"}; !slices.Equal(order, want) {
		t.Fatalf("hooks ran %v, want %v", order, want)
	}

	var steps []string

	for _, r := range records(t, append(headerLine(t), buf.Bytes()...)) {
		if r.Kind != progress.KindEvent {
			continue
		}

		ev := bodyOf[progress.Event](t, r)
		if ev.Step != "undo" {
			t.Fatalf("event %+v is not for the undo step", ev)
		}

		steps = append(steps, string(ev.Kind)+" "+ev.Message+" "+ev.Outcome)
	}

	want := []string{
		"step_started third ", "step_finished third succeeded",
		"step_started second ", "step_finished second succeeded",
		"step_started first ", "step_finished first succeeded",
	}
	if !slices.Equal(steps, want) {
		t.Fatalf("undo events = %q, want %q", steps, want)
	}
}

// headerLine returns a header record, so that records can decode a buffer of
// the records after it.
func headerLine(t *testing.T) []byte {
	t.Helper()

	var buf bytes.Buffer

	if err := progress.NewEncoder(&buf, newFakeClock()).Header(progress.Header{Producer: producer}); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestAForgottenHookDoesNotRun(t *testing.T) {
	t.Parallel()

	j, _ := newTestJob(t)

	var ran []string

	keep := func(name string) func(context.Context) error {
		return func(context.Context) error {
			ran = append(ran, name)

			return nil
		}
	}

	j.OnCancel("kept", keep("kept"))
	forget := j.OnCancel("forgotten", keep("forgotten"))
	j.OnCancel("also kept", keep("also kept"))

	if got, want := j.pendingHooks(), []string{"kept", "forgotten", "also kept"}; !slices.Equal(got, want) {
		t.Fatalf("pending hooks = %v, want %v", got, want)
	}

	forget()
	forget()

	if got, want := j.pendingHooks(), []string{"kept", "also kept"}; !slices.Equal(got, want) {
		t.Fatalf("pending hooks after forget = %v, want %v", got, want)
	}

	if err := j.runUndo(t.Context(), time.Second); err != nil {
		t.Fatal(err)
	}

	if want := []string{"also kept", "kept"}; !slices.Equal(ran, want) {
		t.Fatalf("hooks ran %v, want %v", ran, want)
	}
}

func TestAFailedHookDoesNotStopTheOthers(t *testing.T) {
	t.Parallel()

	j, buf := newTestJob(t)

	boom := errors.New("boom")

	var ran []string

	j.OnCancel("early", func(context.Context) error {
		ran = append(ran, "early")

		return nil
	})
	j.OnCancel("broken", func(context.Context) error {
		ran = append(ran, "broken")

		return boom
	})

	err := j.runUndo(t.Context(), time.Second)
	if !errors.Is(err, boom) {
		t.Fatalf("runUndo = %v, want the hook's error", err)
	}

	if want := []string{"broken", "early"}; !slices.Equal(ran, want) {
		t.Fatalf("hooks ran %v, want %v", ran, want)
	}

	var outcomes []string

	for _, r := range records(t, append(headerLine(t), buf.Bytes()...)) {
		if r.Kind == progress.KindEvent {
			if ev := bodyOf[progress.Event](t, r); ev.Kind == progress.EventStepFinished {
				outcomes = append(outcomes, ev.Message+" "+ev.Outcome)
			}
		}
	}

	if want := []string{"broken failed", "early succeeded"}; !slices.Equal(outcomes, want) {
		t.Fatalf("outcomes = %q, want %q", outcomes, want)
	}
}

func TestHooksGetTheRemainingBudget(t *testing.T) {
	t.Parallel()

	j, _ := newTestJob(t)

	var left time.Duration

	j.OnCancel("deadline", func(ctx context.Context) error {
		dl, ok := ctx.Deadline()
		if !ok {
			t.Error("the hook's context has no deadline")

			return nil
		}

		left = time.Until(dl)

		return nil
	})

	remaining := 4 * time.Second

	if err := j.runUndo(t.Context(), remaining); err != nil {
		t.Fatal(err)
	}

	if hi, lo := remaining-undoMargin, remaining-undoMargin-time.Second; left > hi || left < lo {
		t.Fatalf("the hook had %s left, want between %s and %s", left, lo, hi)
	}

	// A budget the margin eats leaves the hook a context that has ended.
	j, _ = newTestJob(t)

	j.OnCancel("late", func(ctx context.Context) error {
		if ctx.Err() == nil {
			t.Error("a hook with no budget left got a live context")
		}

		return nil
	})

	if err := j.runUndo(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
}

func TestNoopStopsAtACheckpoint(t *testing.T) {
	t.Parallel()

	j, buf := newTestJob(t)

	j.cancel()

	_, err := runNoop(t.Context(), j, func() (string, error) { return "host", nil })
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("runNoop of a cancelled job = %v, want ErrCancelled", err)
	}

	recs := records(t, append(headerLine(t), buf.Bytes()...))
	if len(recs) != 2 || recs[1].Kind != progress.KindEvent {
		t.Fatalf("a cancelled noop wrote %d records, want the header and step_started:\n%s", len(recs), buf)
	}
}
