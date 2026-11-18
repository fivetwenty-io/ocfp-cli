package jobexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/version"
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

	if err := j.Checkpoint(); !errors.Is(err, ErrCanceled) {
		t.Fatalf("Checkpoint after a cancel = %v, want ErrCanceled", err)
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
		j.OnCancel(work(name), func(context.Context) error {
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

// work names the work of a hook by a kind alone, so that the hook's events
// carry the kind as their message.
func work(kind string) progress.WorkRef {
	return progress.WorkRef{Kind: kind}
}

// works names the work of several hooks by their kinds.
func works(kinds ...string) []progress.WorkRef {
	refs := make([]progress.WorkRef, len(kinds))
	for i, k := range kinds {
		refs[i] = work(k)
	}

	return refs
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

	j.OnCancel(work("kept"), keep("kept"))
	forget := j.OnCancel(work("forgotten"), keep("forgotten"))
	j.OnCancel(work("also kept"), keep("also kept"))

	if got, want := j.pendingHooks(), works("kept", "forgotten", "also kept"); !slices.Equal(got, want) {
		t.Fatalf("pending hooks = %v, want %v", got, want)
	}

	forget()
	forget()

	if got, want := j.pendingHooks(), works("kept", "also kept"); !slices.Equal(got, want) {
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

	j.OnCancel(work("early"), func(context.Context) error {
		ran = append(ran, "early")

		return nil
	})
	j.OnCancel(work("broken"), func(context.Context) error {
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

	j.OnCancel(work("deadline"), func(ctx context.Context) error {
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

	j.OnCancel(work("late"), func(ctx context.Context) error {
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
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("runNoop of a canceled job = %v, want ErrCanceled", err)
	}

	recs := records(t, append(headerLine(t), buf.Bytes()...))
	if len(recs) != 2 || recs[1].Kind != progress.KindEvent {
		t.Fatalf("a canceled noop wrote %d records, want the header and step_started:\n%s", len(recs), buf)
	}
}

// serveTestVerb serves run as noop, with the start line and then whatever
// stdin holds, and returns the exit code and what the verb wrote on stderr.
func serveTestVerb(t *testing.T, run func(context.Context, Job, func() (string, error)) (int, error), stdin io.Reader, stdout io.Writer) (int, string) {
	t.Helper()

	cfg := verbConfig{
		version:  version.Get().Version,
		verbs:    map[string]verb{verbNoop: {name: verbNoop, cancelBudget: time.Second, run: run}},
		hostname: func() (string, error) { return testHost, nil },
	}

	in := io.MultiReader(strings.NewReader(startLine(t, goodStart())+"\n"), stdin)

	var stderr bytes.Buffer

	code := serveVerb(t.Context(), cfg, in, stdout, &stderr)

	return code, stderr.String()
}

// A wrapper that goes away ends the verb's stdin and breaks its stdout at
// once. A verb that writes before its next safe point gets an error that is
// a cancel, and its undo hooks run whatever it returns.
func TestAVerbWhoseWriteFailsAfterTheWrapperGoesIsUndone(t *testing.T) {
	t.Parallel()

	var (
		undos  int
		logErr error
	)

	code, stderr := serveTestVerb(t, func(_ context.Context, j Job, _ func() (string, error)) (int, error) {
		j.OnCancel(work("task-1"), func(context.Context) error {
			undos++

			return nil
		})

		<-j.Done()

		logErr = j.Log("stdout", "progress")
		if logErr != nil {
			return 0, fmt.Errorf("log progress: %v", logErr) //nolint:errorlint // a verb that drops the wrap
		}

		return 0, j.Checkpoint()
	}, strings.NewReader(""), failingWriter{})

	if undos != 1 {
		t.Fatalf("the undo hook ran %d times, want once (stderr %q)", undos, stderr)
	}

	if !errors.Is(logErr, ErrCanceled) || !strings.Contains(logErr.Error(), "broken pipe") {
		t.Fatalf("the log after the cancel = %v, want a cancel that holds the write's error", logErr)
	}

	if code != exitVerbFailed {
		t.Fatalf("exit code = %d, want %d", code, exitVerbFailed)
	}
}

// A verb that fails with an error of its own once a cancel has come stops
// all the same: its undo hooks run, its error reaches stderr, and it writes
// the stopped mark.
func TestAVerbThatFailsAfterACancelIsUndone(t *testing.T) {
	t.Parallel()

	var (
		undos int
		out   strings.Builder
	)

	code, stderr := serveTestVerb(t, func(_ context.Context, j Job, _ func() (string, error)) (int, error) {
		j.OnCancel(work("task-1"), func(context.Context) error {
			undos++

			return nil
		})

		<-j.Done()

		return 0, errors.New("the remote call failed")
	}, strings.NewReader(""), &out)

	if undos != 1 {
		t.Fatalf("the undo hook ran %d times, want once", undos)
	}

	if !strings.Contains(stderr, "the remote call failed") {
		t.Fatalf("stderr %q does not name the verb's error", stderr)
	}

	if !strings.HasSuffix(out.String(), `{"type":"stopped"}`+"\n") {
		t.Fatalf("envelopes end\n%s\nwant the stopped mark last", out.String())
	}

	if code != exitVerbFailed {
		t.Fatalf("exit code = %d, want %d", code, exitVerbFailed)
	}
}

// A verb that fails with no cancel has no undo hooks run, and writes no
// stopped mark.
func TestAVerbThatFailsWithNoCancelIsNotUndone(t *testing.T) {
	t.Parallel()

	stdin, closeStdin := io.Pipe()
	t.Cleanup(func() { _ = closeStdin.Close() })

	var (
		undos int
		out   strings.Builder
	)

	code, stderr := serveTestVerb(t, func(_ context.Context, j Job, _ func() (string, error)) (int, error) {
		j.OnCancel(work("task-1"), func(context.Context) error {
			undos++

			return nil
		})

		return 0, errors.New("the remote call failed")
	}, stdin, &out)

	if undos != 0 {
		t.Fatalf("the undo hook ran %d times with no cancel", undos)
	}

	if strings.Contains(out.String(), `"stopped"`) || !strings.Contains(stderr, "the remote call failed") {
		t.Fatalf("envelopes\n%s\nstderr %q, want no stopped mark and the error on stderr", out.String(), stderr)
	}

	if code != exitVerbFailed {
		t.Fatalf("exit code = %d, want %d", code, exitVerbFailed)
	}
}
