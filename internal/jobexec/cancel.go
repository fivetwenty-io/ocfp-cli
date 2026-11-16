package jobexec

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// ErrCancelled is what Checkpoint returns once the job has been asked to stop.
// A verb returns it unchanged, and the runtime then runs the undo hooks.
var ErrCancelled = errors.New("job cancelled")

// stepUndo is the step that the start and end of each undo hook appear under.
const stepUndo = "undo"

// undoMargin is how much of the remaining cancel budget the runtime keeps back
// from the undo hooks, so that the verb can report and exit before the budget
// passes.
const undoMargin = 250 * time.Millisecond

// Job is what a verb gets from the runtime.
type Job interface {
	// Checkpoint marks a safe point. It returns ErrCancelled once a cancel
	// has arrived, and nil otherwise. A verb returns that error unchanged.
	Checkpoint() error

	// Done is closed when a cancel arrives, for a verb that waits on a
	// channel and wants to reach its next safe point sooner.
	Done() <-chan struct{}

	// OnCancel registers undo for external work the verb has started. The
	// runtime calls undo at most once, in reverse order of registration, and
	// only when the verb returns ErrCancelled. The returned forget removes
	// the hook once the work has finished on its own.
	OnCancel(name string, undo func(ctx context.Context) error) (forget func())

	// Emit writes an event record.
	Emit(ev progress.Event) error

	// Log writes a log record on the named stream.
	Log(stream, line string) error
}

// undoHook is one registered undo.
type undoHook struct {
	id   int
	name string
	undo func(ctx context.Context) error
}

// recordEncoder writes the records a verb makes.
type recordEncoder interface {
	Encode(kind progress.Kind, body any) error
}

// undoReporter is a recordEncoder that also tells the wrapper which undo hooks
// are registered, and which are done, and that the verb has stopped.
type undoReporter interface {
	undoState(name, state string) error
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
}

// newJobRuntime returns a jobRuntime that writes records with enc.
func newJobRuntime(enc recordEncoder) *jobRuntime {
	return &jobRuntime{enc: enc, done: make(chan struct{})}
}

// cancel tells the verb to stop at its next safe point. It is safe to call
// more than once and from any goroutine.
func (r *jobRuntime) cancel() {
	r.cancelOnce.Do(func() { close(r.done) })
}

// Checkpoint implements Job.
func (r *jobRuntime) Checkpoint() error {
	select {
	case <-r.done:
		return ErrCancelled
	default:
		return nil
	}
}

// Done implements Job.
func (r *jobRuntime) Done() <-chan struct{} {
	return r.done
}

// OnCancel implements Job.
func (r *jobRuntime) OnCancel(name string, undo func(ctx context.Context) error) (forget func()) {
	r.mu.Lock()
	r.nextID++
	id := r.nextID
	r.hooks = append(r.hooks, undoHook{id: id, name: name, undo: undo})
	r.mu.Unlock()

	r.report(name, undoRegistered)

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
			r.report(name, undoDone)
		}
	}
}

// report tells the wrapper of a hook's state when enc can. A report that
// cannot be written means the wrapper has gone, and the verb learns that from
// its next record.
func (r *jobRuntime) report(name, state string) {
	if rep, ok := r.enc.(undoReporter); ok {
		_ = rep.undoState(name, state)
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
	return r.enc.Encode(progress.KindEvent, ev)
}

// Log implements Job.
func (r *jobRuntime) Log(stream, line string) error {
	return r.enc.Encode(progress.KindLog, progress.LogBody{Stream: stream, Line: line})
}

// pendingHooks returns the names of the hooks that are registered and have
// not run, oldest first.
func (r *jobRuntime) pendingHooks() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	names := make([]string, len(r.hooks))
	for i, h := range r.hooks {
		names[i] = h.name
	}

	return names
}

// runUndo runs the registered hooks, newest first, once. Each hook gets a
// context that ends remaining less undoMargin from now, and its start and end
// appear as step_started and step_finished events for the step undo, with the
// hook's name as the message. A hook that fails finishes with the outcome
// failed, and the others still run. A second call runs nothing. The error
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

		err := r.Emit(progress.Event{Kind: progress.EventStepStarted, Step: stepUndo, Message: h.name})
		if err != nil {
			errs = append(errs, err)
		}

		outcome := outcomeSucceeded

		uerr := h.undo(ctx)
		if uerr != nil {
			outcome = outcomeFailed

			errs = append(errs, fmt.Errorf("undo %s: %w", quote(h.name), uerr))
		}

		err = r.Emit(progress.Event{Kind: progress.EventStepFinished, Step: stepUndo, Message: h.name, Outcome: outcome})
		if err != nil {
			errs = append(errs, err)
		}

		r.report(h.name, undoDone)
	}

	return errors.Join(errs...)
}
