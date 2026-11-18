package jobexec

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/strictjson"
	"github.com/fivetwenty-io/ocfp-cli/internal/version"
)

// A verb runs apart from the wrapper that owns its job's stream. The wrapper
// starts it, writes control lines to its stdin, and reads envelope lines from
// its stdout. The first control line is the start line, which names the
// protocol, the wrapper's version, the job, the verb, and its cancel budget.
// The verb reads it before it does anything else and refuses a start line of
// another protocol or version. A later cancel line carries what is left of
// the cancel budget. The end of stdin means that the wrapper has gone, and
// the verb takes it as a cancel with the whole budget left. Each envelope carries
// one record for the stream, a change to an undo hook, or the mark that the
// verb stopped after a cancel. The wrapper checks every envelope, and numbers
// and stamps the records it takes with its own encoder, so the verb never
// writes the stream itself. The header, the error, and the result belong to
// the wrapper alone. The verb's stderr reaches the stream as log records on
// the stderr stream, one per line. This protocol is internal to one binary
// and is not part of the wire contract.

// verbProtocol is the version of the protocol between a wrapper and its verb.
const verbProtocol = 1

// The types of control line the wrapper writes.
const (
	controlStart  = "start"
	controlCancel = "cancel"
)

// maxControlLine bounds one control line.
const maxControlLine = 64 << 10

// recordOverhead bounds what the wrapper adds around a record's body when it
// stamps it: the kind, a sequence number of up to 20 digits, a UTC time of
// up to 30 characters, and the JSON around them come to under 100 bytes.
const recordOverhead = 128

// The types of envelope a verb writes.
const (
	envelopeRecord  = "record"
	envelopeUndo    = "undo"
	envelopeStopped = "stopped"
)

// The states that an undo envelope reports for a hook. A hook is reported
// done once it has run and succeeded, or once the verb forgets it, and
// failed once it has run and failed.
const (
	undoRegistered = "registered"
	undoDone       = "done"
	undoFailed     = "failed"
)

// exitVerbFailed is the exit code of a verb that failed with an error rather
// than an exit code of its own, that stopped after a cancel, or that the
// wrapper stopped for an envelope it refused.
const exitVerbFailed = 1

// defaultIOGrace is how long the wrapper waits, once the verb has exited, for
// its stdout and stderr to reach their end. Something the verb started that
// left its group can hold them open for longer.
const defaultIOGrace = 5 * time.Second

// stderrStream is the stream a log record of the verb's stderr names.
const stderrStream = "stderr"

// control is one line that the wrapper writes on a verb's stdin.
type control struct {
	Type           string `json:"type"`
	Protocol       int    `json:"protocol,omitempty"`
	Version        string `json:"version,omitempty"`
	JobID          string `json:"jobId,omitempty"`
	Verb           string `json:"verb,omitempty"`
	CancelBudgetMs int64  `json:"cancelBudgetMs,omitempty"`
	RemainingMs    *int64 `json:"remainingMs,omitempty"`
}

// envelope is one line that a verb writes on its stdout.
type envelope struct {
	Type  string            `json:"type"`
	Kind  progress.Kind     `json:"kind,omitempty"`
	Body  json.RawMessage   `json:"body,omitempty"`
	Work  *progress.WorkRef `json:"work,omitempty"`
	State string            `json:"state,omitempty"`
}

// verbStart is what a starter needs to start a verb.
type verbStart struct {
	jobID string
	verb  verb

	// hostname is the host name function of the wrapper's options, for a
	// verb that runs in the wrapper's own process.
	hostname func() (string, error)
}

// verbProc is a verb that a starter has started.
type verbProc struct {
	// stdin carries control lines to the verb.
	stdin io.WriteCloser

	// stdout carries the verb's envelopes, and stderr its error output.
	stdout io.ReadCloser
	stderr io.ReadCloser

	// exited is closed once the verb has exited.
	exited <-chan struct{}

	// kill ends the verb and everything it started. It does nothing once
	// reap has returned.
	kill func() error

	// reap waits for exited, sweeps up anything the verb left behind, and
	// returns the verb's exit code, which is less than 0 when a signal ended
	// it.
	reap func() (int, error)
}

// startInProcess runs the verb on a goroutine of this process, joined to the
// wrapper by pipes as a child process is, so that the verb sees the host name
// function of the wrapper's options. A verb that runs this way cannot be
// killed. Kill closes its pipes instead, and the verb ends at its next write.
func startInProcess(s verbStart) (*verbProc, error) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()

	exited := make(chan struct{})

	var code int

	cfg := verbConfig{version: version.Get().Version, verbs: map[string]verb{s.verb.name: s.verb}, hostname: s.hostname}

	go func() {
		defer close(exited)

		code = serveVerb(context.Background(), cfg, inR, outW, errW)

		_ = outW.Close()
		_ = errW.Close()
		_ = inR.Close()
	}()

	errKilled := errors.New("verb killed")

	return &verbProc{
		stdin:  inW,
		stdout: outR,
		stderr: errR,
		exited: exited,
		kill: func() error {
			_ = outW.CloseWithError(errKilled)
			_ = errW.CloseWithError(errKilled)
			_ = inR.CloseWithError(errKilled)

			return nil
		},
		reap: func() (int, error) {
			<-exited

			return code, nil
		},
	}, nil
}

// ServeVerb is the whole of the process that runs one verb for the wrapper of
// its job, `ocfp job verb`. It reads the start line from stdin, runs the verb
// that the start line names, writes its envelopes to stdout, and returns the
// exit code the process exits with: the verb's own, or 1 when the start line
// is refused, the verb fails with an error, or it stops after a cancel.
func ServeVerb(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) int {
	return serveVerbProcess(ctx, verbProcessConfig(), stdin, stdout, stderr)
}

// verbConfig is what the side of the protocol that runs a verb needs.
type verbConfig struct {
	// version is the version a start line must name.
	version string

	// verbs is the table that a start line's verb is looked up in.
	verbs map[string]verb

	// hostname is the host name function the verb gets.
	hostname func() (string, error)
}

// verbProcessConfig is the configuration of a verb process.
func verbProcessConfig() verbConfig {
	return verbConfig{version: version.Get().Version, verbs: verbs, hostname: os.Hostname}
}

// serveVerbProcess is serveVerb for a process of its own. A wrapper that goes
// away closes the pipe the verb writes to, and the verb must then get an
// error from its next write and go on to its next safe point, rather than be
// killed by SIGPIPE. A lost terminal must not kill it either. The runtime
// does not keep a SIGPIPE that the wrapper ignored, so the process ignores
// both signals itself.
func serveVerbProcess(ctx context.Context, cfg verbConfig, stdin io.Reader, stdout, stderr io.Writer) int {
	signal.Ignore(syscall.SIGPIPE, syscall.SIGHUP)

	// The mark is for this process alone, so the processes the verb starts
	// do not inherit it.
	_ = os.Unsetenv(VerbChildEnv)

	return serveVerb(ctx, cfg, stdin, stdout, stderr)
}

// serveVerb reads the start line from stdin, runs its verb from cfg with
// envelopes written to stdout, and returns the exit code. While the verb
// runs, a cancel line, a control line that cannot be read, or the end of
// stdin tells the verb to stop at its next safe point.
func serveVerb(ctx context.Context, cfg verbConfig, stdin io.Reader, stdout, stderr io.Writer) int {
	br := bufio.NewReaderSize(stdin, readChunk)

	v, budget, err := readStart(br, cfg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "job verb: %v\n", err)

		return exitVerbFailed
	}

	rt := newJobRuntime(&envelopeWriter{w: stdout})

	go watchControl(br, rt, budget)

	return runVerbChild(ctx, v, cfg.hostname, rt, stderr)
}

// readStart reads and checks the start line, and returns the verb it names
// and its cancel budget.
func readStart(br *bufio.Reader, cfg verbConfig) (verb, time.Duration, error) {
	line, err := readControl(br)
	if err != nil {
		return verb{}, 0, fmt.Errorf("read the start line: %w", err)
	}

	var c control

	err = strictjson.Decode(line, &c)

	switch {
	case err != nil:
		return verb{}, 0, fmt.Errorf("the start line does not decode: %s", quote(err.Error()))
	case c.Type != controlStart:
		return verb{}, 0, fmt.Errorf("the first control line is of type %s, not a start line", quote(c.Type))
	case c.Protocol != verbProtocol:
		return verb{}, 0, fmt.Errorf("the wrapper speaks protocol %d and this binary protocol %d", c.Protocol, verbProtocol)
	case c.Version != cfg.version:
		return verb{}, 0, fmt.Errorf("the wrapper is version %s and this binary is version %s", quote(c.Version), quote(cfg.version))
	case c.CancelBudgetMs <= 0:
		return verb{}, 0, errors.New("the start line names no cancel budget")
	case c.RemainingMs != nil:
		return verb{}, 0, errors.New("the start line names a remaining budget")
	}

	v, ok := cfg.verbs[c.Verb]
	if !ok {
		return verb{}, 0, fmt.Errorf("this binary has no verb %s", quote(c.Verb))
	}

	return v, time.Duration(c.CancelBudgetMs) * time.Millisecond, nil
}

// watchControl reads control lines after the start line and passes a cancel
// on to rt. A cancel line carries what is left of the budget. A line that
// cannot be read, and the end of br, which means the wrapper has gone, are a
// cancel with the whole budget left, because a verb that cannot hear its
// wrapper is safest stopped.
func watchControl(br *bufio.Reader, rt *jobRuntime, budget time.Duration) {
	for {
		line, err := readControl(br)
		if err != nil {
			rt.cancelWithin(budget)

			return
		}

		var c control

		err = strictjson.Decode(line, &c)
		if err != nil || c.Type != controlCancel || c.RemainingMs == nil {
			rt.cancelWithin(budget)

			continue
		}

		rt.cancelWithin(max(time.Duration(*c.RemainingMs)*time.Millisecond, 0))
	}
}

// readControl reads one control line, without its newline.
func readControl(br *bufio.Reader) ([]byte, error) {
	var line []byte

	for {
		chunk, err := br.ReadSlice('\n')
		line = append(line, chunk...)

		if len(line) > maxControlLine+1 {
			return nil, fmt.Errorf("a control line is longer than %d bytes", maxControlLine)
		}

		switch {
		case err == nil:
			return line[:len(line)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) == 0:
			return nil, errors.New("stdin ended")
		case errors.Is(err, io.EOF):
			return nil, errors.New("stdin ended part way through a control line")
		default:
			return nil, fmt.Errorf("read stdin: %w", err)
		}
	}
}

// writeControl writes one control line.
func writeControl(w io.Writer, c control) error {
	line, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode %s line: %w", c.Type, err)
	}

	_, err = w.Write(append(line, '\n'))
	if err != nil {
		return fmt.Errorf("write %s line: %w", c.Type, err)
	}

	return nil
}

// runVerbChild runs v against rt, on the side of the protocol that runs the
// verb, and returns the exit code that side exits with. A verb that stops at
// a safe point after a cancel has its undo hooks run, and the stopped mark
// written, and exits with exitVerbFailed. So does a verb that fails with any
// error once a cancel has come, since a wrapper that has gone fails its
// writes before it reaches a safe point, and its error goes to stderr first.
// A verb that fails with an error and no cancel has the error written to
// stderr and exits with exitVerbFailed.
func runVerbChild(ctx context.Context, v verb, hostname func() (string, error), rt *jobRuntime, stderr io.Writer) int {
	code, err := v.run(ctx, rt, hostname)

	cancelled := errors.Is(err, ErrCancelled)

	switch {
	case cancelled || (err != nil && rt.Checkpoint() != nil):
		if !cancelled {
			_, _ = fmt.Fprintf(stderr, "job verb %s: %v\n", v.name, err)
		}

		uerr := rt.runUndo(ctx, rt.budgetLeft(v.cancelBudget))
		if uerr != nil {
			_, _ = fmt.Fprintf(stderr, "job verb %s: %v\n", v.name, uerr)
		}

		serr := rt.stopped()
		if serr != nil {
			_, _ = fmt.Fprintf(stderr, "job verb %s: %v\n", v.name, serr)
		}

		return exitVerbFailed
	case err != nil:
		_, _ = fmt.Fprintf(stderr, "job verb %s: %v\n", v.name, err)

		return exitVerbFailed
	}

	return code
}

// envelopeWriter writes envelopes, each as one whole line. It is safe for
// concurrent use.
type envelopeWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// Encode writes one record of the given kind.
func (e *envelopeWriter) Encode(kind progress.Kind, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode %s body: %w", kind, err)
	}

	return e.write(envelope{Type: envelopeRecord, Kind: kind, Body: raw})
}

// undoState reports that the undo hook for work is registered, done, or
// failed.
func (e *envelopeWriter) undoState(work progress.WorkRef, state string) error {
	return e.write(envelope{Type: envelopeUndo, Work: &work, State: state})
}

// stopped writes the mark that the verb stopped after a cancel.
func (e *envelopeWriter) stopped() error {
	return e.write(envelope{Type: envelopeStopped})
}

func (e *envelopeWriter) write(env envelope) error {
	line, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("encode %s envelope: %w", env.Type, err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	_, err = e.w.Write(append(line, '\n'))
	if err != nil {
		return fmt.Errorf("write %s envelope: %w", env.Type, err)
	}

	return nil
}

// refusedEnvelopeError is an envelope the wrapper does not take from a verb.
type refusedEnvelopeError struct {
	why string
}

func (e *refusedEnvelopeError) Error() string {
	return "the verb wrote " + e.why
}

func refusedEnvelope(format string, args ...any) error {
	return &refusedEnvelopeError{why: fmt.Sprintf(format, args...)}
}

// errSealed ends a relay that finds itself sealed.
var errSealed = errors.New("relay sealed")

// relay copies a verb's envelopes and stderr into the job's stream. It
// forwards records through enc until it is sealed, and keeps the work of the
// undo hooks that the verb has registered and not yet reported done, with
// whether each has failed.
//
// mu is held while a record is written, so the supervisor never takes it:
// the stopped mark lives apart from it, and the kill needs nothing under it.
type relay struct {
	enc *progress.Encoder

	// kill ends the verb once the relay has failed. Nil means nothing to
	// end.
	kill func() error

	// stopped is set once the verb has written the stopped mark.
	stopped atomic.Bool

	mu       sync.Mutex
	sealed   bool
	refused  error // the first envelope the relay refused
	writeErr error // the first record the relay could not write
	undo     []progress.UndoneWork
}

// envelopes relays the envelopes on r, one line at a time, until r ends, the
// relay is sealed, or an envelope is refused or cannot be written. A refusal
// or a failed write kills the verb.
func (rl *relay) envelopes(r io.Reader) {
	br := bufio.NewReaderSize(r, readChunk)

	for {
		line, err := readEnvelope(br)
		if err == nil {
			err = rl.envelope(line)
		}

		if err != nil {
			rl.fail(err)

			return
		}
	}
}

// readEnvelope reads one line of at most maxRecordLine bytes and returns it
// without its newline. A read that ends r returns io.EOF, after a refusal
// for a line that the end of r cuts short.
func readEnvelope(br *bufio.Reader) ([]byte, error) {
	var line []byte

	for {
		chunk, err := br.ReadSlice('\n')
		line = append(line, chunk...)

		if len(line) > maxRecordLine+1 {
			return nil, refusedEnvelope("an envelope longer than %d bytes", maxRecordLine)
		}

		switch {
		case err == nil:
			return line[:len(line)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) > 0:
			return nil, refusedEnvelope("an envelope that ends part way through")
		default:
			return nil, err //nolint:wrapcheck // the relay only tells an end from a refusal
		}
	}
}

// envelope relays one envelope, or refuses it.
func (rl *relay) envelope(line []byte) error {
	var env envelope

	err := strictjson.Decode(line, &env)
	if err != nil {
		return refusedEnvelope("an envelope that does not decode: %s", quote(err.Error()))
	}

	switch env.Type {
	case envelopeRecord:
		if env.Work != nil || env.State != "" {
			return refusedEnvelope("a record envelope with an undo field")
		}

		return rl.record(env.Kind, env.Body)
	case envelopeUndo:
		if env.Kind != "" || env.Body != nil || env.Work == nil || env.Work.Kind == "" {
			return refusedEnvelope("an undo envelope that names no work, or that holds a record")
		}

		return rl.undoState(*env.Work, env.State)
	case envelopeStopped:
		if env.Kind != "" || env.Body != nil || env.Work != nil || env.State != "" {
			return refusedEnvelope("a stopped envelope with fields")
		}

		rl.stopped.Store(true)

		return nil
	default:
		return refusedEnvelope("an envelope of type %s", quote(env.Type))
	}
}

// record forwards an event or log record. Every other kind belongs to the
// wrapper.
func (rl *relay) record(kind progress.Kind, body json.RawMessage) error {
	var (
		v   any
		err error
	)

	switch kind {
	case progress.KindEvent:
		var ev progress.Event

		err = strictjson.Decode(body, &ev)
		if err == nil && ev.Undone != nil {
			return refusedEnvelope("an event that names work left undone, which only the wrapper reports")
		}

		v = ev
	case progress.KindLog:
		var l progress.LogBody

		err = strictjson.Decode(body, &l)
		v = l
	case progress.KindHeader, progress.KindError, progress.KindResult, progress.KindQuestion:
		return refusedEnvelope("a record of kind %s, which only the wrapper writes", quote(string(kind)))
	default:
		return refusedEnvelope("a record of kind %s", quote(string(kind)))
	}

	if err != nil {
		return refusedEnvelope("a %s record that does not decode: %s", kind, quote(err.Error()))
	}

	// The body is measured as the wrapper writes it, which can be longer
	// than the verb wrote it, since it escapes what the verb need not. A
	// record longer than a follower takes would leave the whole stream
	// unreadable, so it is refused here.
	raw, err := json.Marshal(v)
	if err != nil {
		return refusedEnvelope("a %s record that does not encode: %s", kind, quote(err.Error()))
	}

	if len(raw)+recordOverhead > maxRecordLine {
		return refusedEnvelope("a %s record longer than %d bytes once stamped", kind, maxRecordLine)
	}

	return rl.forward(kind, json.RawMessage(raw))
}

// undoState records the state an undo envelope reports. A hook reported done
// or failed is the newest one registered for its work that has not been
// reported failed. A done hook's work is no longer undone, and a failed
// hook's work stays undone, marked failed.
func (rl *relay) undoState(work progress.WorkRef, state string) error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	switch state {
	case undoRegistered:
		rl.undo = append(rl.undo, progress.UndoneWork{WorkRef: work, State: progress.UndoNotRun})
	case undoDone, undoFailed:
		for i := len(rl.undo) - 1; i >= 0; i-- {
			if rl.undo[i].WorkRef != work || rl.undo[i].State != progress.UndoNotRun {
				continue
			}

			if state == undoFailed {
				rl.undo[i].State = progress.UndoFailed
			} else {
				rl.undo = append(rl.undo[:i:i], rl.undo[i+1:]...)
			}

			break
		}
	default:
		return refusedEnvelope("an undo envelope in state %s", quote(state))
	}

	return nil
}

// forward writes one record unless the relay is sealed.
func (rl *relay) forward(kind progress.Kind, body any) error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if rl.sealed {
		return errSealed
	}

	err := rl.enc.Encode(kind, body)
	if err != nil {
		return &relayWriteError{err: err}
	}

	return nil
}

// relayWriteError is a record that the relay could not write to the stream.
type relayWriteError struct {
	err error
}

func (e *relayWriteError) Error() string { return e.err.Error() }

func (e *relayWriteError) Unwrap() error { return e.err }

// stderrLines relays each line on r as a log record on the stderr stream,
// until r ends or the relay fails. A line longer than the reader's buffer
// goes as more than one record.
func (rl *relay) stderrLines(r io.Reader) {
	br := bufio.NewReaderSize(r, readChunk)

	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			line := strings.TrimSuffix(string(bytes.TrimSuffix(chunk, []byte("\n"))), "\r")

			ferr := rl.forward(progress.KindLog, progress.LogBody{Stream: stderrStream, Line: line})
			if ferr != nil {
				rl.fail(ferr)

				return
			}
		}

		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return
		}
	}
}

// fail records why the relay stopped, and kills the verb when the relay
// refused an envelope or could not write a record. The end of the verb's
// output, and a sealed relay, are not failures.
func (rl *relay) fail(err error) {
	var (
		ref *refusedEnvelopeError
		we  *relayWriteError
	)

	switch {
	case errors.As(err, &ref):
		rl.mu.Lock()
		if rl.refused == nil && rl.writeErr == nil {
			rl.refused = err
		}
		rl.mu.Unlock()
	case errors.As(err, &we):
		rl.mu.Lock()
		if rl.writeErr == nil {
			rl.writeErr = we.err
		}
		rl.mu.Unlock()
	default:
		return
	}

	if rl.kill != nil {
		_ = rl.kill()
	}
}

// seal stops the relay from writing any more records. It waits for a record
// that is being written to finish, so once seal returns the stream belongs to
// the wrapper. A record goes to the spool and is only queued for the client,
// so that wait never waits on the client.
func (rl *relay) seal() {
	rl.mu.Lock()
	rl.sealed = true
	rl.mu.Unlock()
}

// failure returns the record the relay could not write, and the envelope it
// refused.
func (rl *relay) failure() (writeErr, refused error) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	return rl.writeErr, rl.refused
}

// pendingUndo returns the work of the undo hooks that the verb registered
// and has not reported done, oldest first, with whether each hook failed.
func (rl *relay) pendingUndo() []progress.UndoneWork {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	return append([]progress.UndoneWork(nil), rl.undo...)
}

// sawStopped reports whether the verb wrote the stopped mark. It never
// waits on a record that is being written.
func (rl *relay) sawStopped() bool {
	return rl.stopped.Load()
}

// verbEnd is how the verb of a job ended.
type verbEnd struct {
	// code is the job's exit code.
	code int

	// cancel is how a cancel ended the job, and empty when none did.
	cancel progress.CancelOutcome

	// seenAt is when the run first saw the cancel marker. It is zero when
	// the run saw none.
	seenAt time.Time

	// message says how the cancel ended the job, for its JOB_CANCELED
	// error.
	message string

	// undone lists the work that a cancelled verb left undone, oldest
	// first: the undo hooks it had registered and had not reported done,
	// with whether each failed.
	undone []progress.UndoneWork
}

// runVerb starts v and relays what it writes into enc until it has exited and
// its output has ended, or until the grace for that end has passed. While the
// verb runs, it watches s for a cancel marker. The relay is sealed before
// runVerb returns, so the records the caller writes come after every record
// of the verb's. An error means a record could not be written or the verb
// could not be started or waited for, and the stream then gets no result.
//
// A marker that is already in s before the verb starts stops the job there,
// with no verb started, since the point before any work is the safest point
// there is. A verb that writes the stopped mark after the run passed a cancel
// on to it has stopped at a safe point. A verb that exits without that mark
// has finished, even when the run saw a marker, and the job ends as if no
// cancel had come.
//
// A verb that has not exited once the cancel budget has passed on the run's
// clock, from when the run first saw the marker, is killed with its whole
// group. The kill is repeated at each poll until the verb exits. The job was
// killed unless the verb had written the stopped mark before then, and a
// killed job names the work of the undo hooks that never ran.
func runVerb(o Options, enc *progress.Encoder, s Spool, jobID string, v verb) (verbEnd, error) {
	if s.cancelled() {
		return verbEnd{
			code:    exitVerbFailed,
			cancel:  progress.CancelStopped,
			seenAt:  o.Clock.Now().UTC(),
			message: fmt.Sprintf("job %s was cancelled and stopped before its verb started", jobID),
		}, nil
	}

	proc, err := o.start(verbStart{jobID: jobID, verb: v, hostname: o.Hostname})
	if err != nil {
		return verbEnd{}, fmt.Errorf("start verb %s: %w", v.name, err)
	}

	rl := &relay{enc: enc, kill: proc.kill}

	var wg sync.WaitGroup

	wg.Go(func() { rl.envelopes(proc.stdout) })
	wg.Go(func() { rl.stderrLines(proc.stderr) })

	// A verb that cannot take the start line has already exited, and says
	// why on its stderr, so a failed write needs nothing more.
	_ = writeControl(proc.stdin, control{
		Type:           controlStart,
		Protocol:       verbProtocol,
		Version:        version.Get().Version,
		JobID:          jobID,
		Verb:           v.name,
		CancelBudgetMs: v.cancelBudget.Milliseconds(),
	})

	sv := &supervisor{clock: o.Clock, spool: s, proc: proc, relay: rl, budget: v.cancelBudget}
	sv.watch()

	code, rerr := proc.reap()

	drained := make(chan struct{})

	go func() {
		wg.Wait()
		close(drained)
	}()

	waitDrained(drained, o.ioGrace)

	// Once the relay is sealed, a record it has read is dropped rather than
	// written after the result, and the relay ends at its next record. A
	// pipe whose close does not wake a read can keep a relay goroutine
	// waiting after that, so the last wait is bounded too.
	rl.seal()

	_ = proc.stdout.Close()
	_ = proc.stderr.Close()
	_ = proc.stdin.Close()

	waitDrained(drained, o.ioGrace)

	if rerr != nil {
		return verbEnd{}, fmt.Errorf("wait for verb %s: %w", v.name, rerr)
	}

	writeErr, refused := rl.failure()
	if writeErr != nil {
		return verbEnd{}, writeErr
	}

	if sv.outcome == progress.CancelKilled {
		return verbEnd{
			code:    exitVerbFailed,
			cancel:  progress.CancelKilled,
			seenAt:  sv.seenUTC(),
			message: fmt.Sprintf("job %s was cancelled and killed when its cancel budget of %s passed", jobID, v.cancelBudget),
			undone:  rl.pendingUndo(),
		}, nil
	}

	if !sv.seenAt.IsZero() && rl.sawStopped() {
		return verbEnd{
			code:    exitVerbFailed,
			cancel:  progress.CancelStopped,
			seenAt:  sv.seenUTC(),
			message: fmt.Sprintf("job %s was cancelled and stopped at a safe point", jobID),
			undone:  rl.pendingUndo(),
		}, nil
	}

	if refused != nil {
		err := enc.Encode(progress.KindLog, progress.LogBody{Stream: stderrStream, Line: fmt.Sprintf("job %s: verb %s stopped: %v", jobID, v.name, refused)})
		if err != nil {
			return verbEnd{}, fmt.Errorf("job %s: %w", jobID, err)
		}

		return verbEnd{code: exitVerbFailed}, nil
	}

	// Only 0 and exitVerbFailed belong to the protocol. A verb process that
	// exits with any other code, as on a crash, or on a signal, fails the
	// job, since 2 would read as a refusal and other codes mean nothing to
	// the stream's reader.
	if code != 0 && code != exitVerbFailed {
		err := enc.Encode(progress.KindLog, progress.LogBody{Stream: stderrStream, Line: fmt.Sprintf("job %s: verb %s exited with code %d", jobID, v.name, code)})
		if err != nil {
			return verbEnd{}, fmt.Errorf("job %s: %w", jobID, err)
		}

		return verbEnd{code: exitVerbFailed}, nil
	}

	return verbEnd{code: code}, nil
}

// supervisor watches a running verb's spool for the cancel marker, and ends
// the verb once its cancel budget has passed, on the run's own clock.
type supervisor struct {
	clock  progress.Clock
	spool  Spool
	proc   *verbProc
	relay  *relay
	budget time.Duration

	// seenAt is the clock's reading at the look that first found the
	// marker, and zero until one does. It is kept as the clock gave it,
	// with any monotonic reading, so that the budget is measured on that
	// reading and a step of the wall clock cannot move the kill. seenUTC
	// gives it for the stream and exit.json.
	seenAt time.Time

	// outcome is how the cancel ended the verb, once the supervisor has
	// seen which: stopped when the stopped mark came before the budget
	// passed, and killed when the budget passed first. It is set once.
	outcome progress.CancelOutcome
}

// watch looks for the marker every pollInterval until the verb exits. The
// first time it finds the marker it notes the time and passes the cancel on
// to the verb, with the whole budget left. After that, each look checks for
// the stopped mark and then for the end of the budget, and kills the verb's
// group once the budget has passed.
func (sv *supervisor) watch() {
	for {
		if isClosed(sv.proc.exited) {
			return
		}

		if !sv.seenAt.IsZero() {
			sv.enforce()
		} else if sv.spool.cancelled() {
			sv.seenAt = sv.clock.Now()

			ms := sv.budget.Milliseconds()

			// A verb reads its control lines on a goroutine of its own, so
			// the write ends at once. It is not waited for all the same,
			// so that a verb that has stopped reading cannot stall the
			// run, and closing stdin ends it.
			go func() { _ = writeControl(sv.proc.stdin, control{Type: controlCancel, RemainingMs: &ms}) }()
		}

		pause(sv.clock, sv.proc.exited)
	}
}

// enforce settles the outcome and kills the verb once the budget has passed.
// The kill reaches only the verb's own group, and only while the verb is
// unreaped, which it is until watch has returned.
func (sv *supervisor) enforce() {
	if sv.outcome == "" && sv.relay.sawStopped() {
		sv.outcome = progress.CancelStopped
	}

	if sv.clock.Now().Before(sv.seenAt.Add(sv.budget)) {
		return
	}

	if sv.outcome == "" {
		sv.outcome = progress.CancelKilled
	}

	// A kill that fails leaves the verb running, and the next look kills
	// again.
	_ = sv.proc.kill()
}

// seenUTC is when the supervisor first saw the marker, in UTC, as the job's
// stream and exit.json report it.
func (sv *supervisor) seenUTC() time.Time {
	return sv.seenAt.UTC()
}

// pause waits pollInterval through c when it can wait, and on a timer when it
// cannot, or until exited is closed. It does not wait at all once exited is
// closed.
func pause(c progress.Clock, exited <-chan struct{}) {
	if isClosed(exited) {
		return
	}

	var ch <-chan time.Time

	if wc, ok := c.(waitClock); ok {
		ch = wc.After(pollInterval)
	} else {
		t := time.NewTimer(pollInterval)
		defer t.Stop()

		ch = t.C
	}

	select {
	case <-exited:
	case <-ch:
	}
}

// isClosed reports whether ch is closed.
func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// waitDrained waits for drained to close, for at most d.
func waitDrained(drained <-chan struct{}, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-drained:
	case <-t.C:
	}
}
