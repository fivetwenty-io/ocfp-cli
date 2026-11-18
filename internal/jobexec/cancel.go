package jobexec

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// ErrCanceled is what Checkpoint returns once the job has been asked to stop.
// A verb returns it unchanged, and the runtime then runs the undo hooks.
var ErrCanceled = errors.New("job canceled")

// stepUndo is the step that the start and end of each undo hook appear under.
const stepUndo = "undo"

// undoMargin is how much of the remaining cancel budget the runtime keeps back
// from the undo hooks, so that the verb can report and exit before the budget
// passes.
const undoMargin = 250 * time.Millisecond

// Job is what a verb gets from the runtime.
type Job interface {
	// Checkpoint marks a safe point. It returns ErrCanceled once a cancel
	// has arrived, and nil otherwise. A verb returns that error unchanged.
	Checkpoint() error

	// Done is closed when a cancel arrives, for a verb that waits on a
	// channel and wants to reach its next safe point sooner.
	Done() <-chan struct{}

	// OnCancel registers undo for work, which the verb has started outside
	// the job. The runtime calls undo at most once, in reverse order of
	// registration, and only when the verb returns ErrCanceled or fails
	// with any error once a cancel has come. The returned forget removes the
	// hook once the work has finished on its own.
	OnCancel(work progress.WorkRef, undo func(ctx context.Context) error) (forget func())

	// Emit writes an event record. A write that fails once a cancel has
	// come returns an error that matches ErrCanceled as well as the
	// write's own error, since the wrapper may have gone.
	Emit(ev progress.Event) error

	// Log writes a log record on the named stream. It fails as Emit does.
	Log(stream, line string) error
}

// undoHook is one registered undo.
type undoHook struct {
	id   int
	work progress.WorkRef
	undo func(ctx context.Context) error
}

// recordEncoder writes the records a verb makes.
type recordEncoder interface {
	Encode(kind progress.Kind, body any) error
}

// undoReporter is a recordEncoder that also tells the wrapper which undo hooks
// are registered, and which are done, and that the verb has stopped.
type undoReporter interface {
	undoState(work progress.WorkRef, state string) error
	stopped() error
}

// jobRuntime is the Job a verb runs against. It holds the cancel signal and
// the undo hooks, and it writes the verb's records with enc.
type jobRuntime struct {
	enc recordEncoder

	done       chan struct{}
	cancelOnce sync.Once

	mu      sync.Mutex
	hooks   []undoHook
	nextID  int
	undoRan bool

	// cancelAt is when the cancel arrived, and remaining how much of the
	// cancel budget was left then.
	cancelAt  time.Time
	remaining time.Duration
}

// newJobRuntime returns a jobRuntime that writes records with enc.
func newJobRuntime(enc recordEncoder) *jobRuntime {
	return &jobRuntime{enc: enc, done: make(chan struct{})}
}

// cancel tells the verb to stop at its next safe point, with nothing of the
// cancel budget left. It is safe to call more than once and from any
// goroutine.
func (r *jobRuntime) cancel() {
	r.cancelWithin(0)
}

// cancelWithin tells the verb to stop at its next safe point, with remaining
// left of the cancel budget from now. Only the first call counts, and it is
// safe to call from any goroutine.
func (r *jobRuntime) cancelWithin(remaining time.Duration) {
	r.cancelOnce.Do(func() {
		r.mu.Lock()
		r.cancelAt = time.Now()
		r.remaining = remaining
		r.mu.Unlock()

		close(r.done)
	})
}

// budgetLeft is how much of the cancel budget is left now. A verb that stops
// although no cancel arrived has the whole of budget.
func (r *jobRuntime) budgetLeft(budget time.Duration) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cancelAt.IsZero() {
		return budget
	}

	return max(r.remaining-time.Since(r.cancelAt), 0)
}

// Checkpoint implements Job.
func (r *jobRuntime) Checkpoint() error {
	select {
	case <-r.done:
		return ErrCanceled
	default:
		return nil
	}
}

// Done implements Job.
func (r *jobRuntime) Done() <-chan struct{} {
	return r.done
}

// OnCancel implements Job.
func (r *jobRuntime) OnCancel(work progress.WorkRef, undo func(ctx context.Context) error) (forget func()) {
	r.mu.Lock()
	r.nextID++
	id := r.nextID
	r.hooks = append(r.hooks, undoHook{id: id, work: work, undo: undo})
	r.mu.Unlock()

	r.report(work, undoRegistered)

	return func() {
		r.mu.Lock()

		found := false

		for i, h := range r.hooks {
			if h.id == id {
				r.hooks = append(r.hooks[:i:i], r.hooks[i+1:]...)
				found = true

				break
			}
		}

		r.mu.Unlock()

		if found {
			r.report(work, undoDone)
		}
	}
}

// report tells the wrapper of a hook's state when enc can. A report that
// cannot be written means the wrapper has gone, and the verb learns that from
// its next record.
func (r *jobRuntime) report(work progress.WorkRef, state string) {
	if rep, ok := r.enc.(undoReporter); ok {
		_ = rep.undoState(work, state)
	}
}

// stopped writes the mark that the verb stopped after a cancel, when enc can.
func (r *jobRuntime) stopped() error {
	if rep, ok := r.enc.(undoReporter); ok {
		return rep.stopped()
	}

	return nil
}

// Emit implements Job.
func (r *jobRuntime) Emit(ev progress.Event) error {
	return r.write(progress.KindEvent, ev)
}

// Log implements Job.
func (r *jobRuntime) Log(stream, line string) error {
	return r.write(progress.KindLog, progress.LogBody{Stream: stream, Line: line})
}

// write writes one record. A write that fails once a cancel has come also
// matches ErrCanceled.
func (r *jobRuntime) write(kind progress.Kind, body any) error {
	err := r.enc.Encode(kind, body)
	if err != nil && r.Checkpoint() != nil {
		return fmt.Errorf("%w: %w", ErrCanceled, err)
	}

	return err //nolint:wrapcheck // the encoder's errors name what failed
}

// pendingHooks returns the work of the hooks that are registered and have not
// run, oldest first.
func (r *jobRuntime) pendingHooks() []progress.WorkRef {
	r.mu.Lock()
	defer r.mu.Unlock()

	work := make([]progress.WorkRef, len(r.hooks))
	for i, h := range r.hooks {
		work[i] = h.work
	}

	return work
}

// runUndo runs the registered hooks, newest first, once. Each hook gets a
// context that ends remaining less undoMargin from now, and its start and end
// appear as step_started and step_finished events for the step undo, with the
// hook's work as the message. A hook that fails finishes with the outcome
// failed, is reported to the wrapper as failed rather than done, and the
// others still run. A second call runs nothing. The error
// joins every failed hook's error and any record that could not be written.
func (r *jobRuntime) runUndo(ctx context.Context, remaining time.Duration) error {
	r.mu.Lock()

	if r.undoRan {
		r.mu.Unlock()

		return nil
	}

	r.undoRan = true
	hooks := r.hooks
	r.hooks = nil

	r.mu.Unlock()

	ctx, stop := context.WithTimeout(ctx, max(remaining-undoMargin, 0))
	defer stop()

	var errs []error

	for i := len(hooks) - 1; i >= 0; i-- {
		h := hooks[i]

		err := r.Emit(progress.Event{Kind: progress.EventStepStarted, Step: stepUndo, Message: h.work.String()})
		if err != nil {
			errs = append(errs, err)
		}

		outcome, state := outcomeSucceeded, undoDone

		uerr := h.undo(ctx)
		if uerr != nil {
			outcome, state = outcomeFailed, undoFailed

			errs = append(errs, fmt.Errorf("undo %s: %w", quote(h.work.String()), uerr))
		}

		err = r.Emit(progress.Event{Kind: progress.EventStepFinished, Step: stepUndo, Message: h.work.String(), Outcome: outcome})
		if err != nil {
			errs = append(errs, err)
		}

		r.report(h.work, state)
	}

	return errors.Join(errs...)
}
