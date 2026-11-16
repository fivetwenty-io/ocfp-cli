package jobexec

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/strictjson"
)

// A verb runs apart from the wrapper that owns its job's stream. The wrapper
// starts it, and reads envelope lines from its stdout. Each envelope carries
// one record for the stream, a change to an undo hook, or the mark that the
// verb stopped after a cancel. The wrapper checks every envelope, and numbers
// and stamps the records it takes with its own encoder, so the verb never
// writes the stream itself. The header, the error, and the result belong to
// the wrapper alone. The verb's stderr reaches the stream as log records on
// the stderr stream, one per line. This protocol is internal to one binary
// and is not part of the wire contract.

// The types of envelope a verb writes.
const (
	envelopeRecord  = "record"
	envelopeUndo    = "undo"
	envelopeStopped = "stopped"
)

// The states that an undo envelope reports for a hook.
const (
	undoRegistered = "registered"
	undoDone       = "done"
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

// envelope is one line that a verb writes on its stdout.
type envelope struct {
	Type  string          `json:"type"`
	Kind  progress.Kind   `json:"kind,omitempty"`
	Body  json.RawMessage `json:"body,omitempty"`
	Name  string          `json:"name,omitempty"`
	State string          `json:"state,omitempty"`
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

	go func() {
		defer close(exited)

		code = runVerbChild(context.Background(), s.verb, s.hostname, newJobRuntime(&envelopeWriter{w: outW}), errW)

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

// runVerbChild runs v against rt, on the side of the protocol that runs the
// verb, and returns the exit code that side exits with. A verb that stops at
// a safe point after a cancel has its undo hooks run, and the stopped mark
// written, and exits with exitVerbFailed. A verb that fails with any other
// error has the error written to stderr and exits with exitVerbFailed.
func runVerbChild(ctx context.Context, v verb, hostname func() (string, error), rt *jobRuntime, stderr io.Writer) int {
	code, err := v.run(ctx, rt, hostname)

	switch {
	case errors.Is(err, ErrCancelled):
		uerr := rt.runUndo(ctx, v.cancelBudget)
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

// undoState reports that the undo hook name is registered or done.
func (e *envelopeWriter) undoState(name, state string) error {
	return e.write(envelope{Type: envelopeUndo, Name: name, State: state})
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
// forwards records through enc until it is sealed, and keeps the names of the
// undo hooks that the verb has registered and not yet reported done.
type relay struct {
	enc *progress.Encoder

	// kill ends the verb once the relay has failed. Nil means nothing to
	// end.
	kill func() error

	mu       sync.Mutex
	sealed   bool
	refused  error // the first envelope the relay refused
	writeErr error // the first record the relay could not write
	undo     []string
	stopped  bool
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
		if env.Name != "" || env.State != "" {
			return refusedEnvelope("a record envelope with an undo field")
		}

		return rl.record(env.Kind, env.Body)
	case envelopeUndo:
		if env.Kind != "" || env.Body != nil || env.Name == "" {
			return refusedEnvelope("an undo envelope that names no hook, or that holds a record")
		}

		return rl.undoState(env.Name, env.State)
	case envelopeStopped:
		if env.Kind != "" || env.Body != nil || env.Name != "" || env.State != "" {
			return refusedEnvelope("a stopped envelope with fields")
		}

		rl.mu.Lock()
		rl.stopped = true
		rl.mu.Unlock()

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

	return rl.forward(kind, v)
}

// undoState records the state an undo envelope reports. A hook reported done
// is the newest one registered under its name.
func (rl *relay) undoState(name, state string) error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	switch state {
	case undoRegistered:
		rl.undo = append(rl.undo, name)
	case undoDone:
		for i := len(rl.undo) - 1; i >= 0; i-- {
			if rl.undo[i] == name {
				rl.undo = append(rl.undo[:i:i], rl.undo[i+1:]...)

				break
			}
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
// the wrapper.
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

// pendingUndo returns the undo hooks that the verb registered and has not
// reported done, oldest first.
func (rl *relay) pendingUndo() []string {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	return append([]string(nil), rl.undo...)
}

// sawStopped reports whether the verb wrote the stopped mark.
func (rl *relay) sawStopped() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	return rl.stopped
}

// runVerb starts v and relays what it writes into enc until it has exited and
// its output has ended, or until the grace for that end has passed. The relay
// is sealed before runVerb returns, so the result the caller writes comes
// after every record of the verb's. runVerb returns the verb's exit code. An
// error means a record could not be written or the verb could not be started
// or waited for, and the stream then gets no result.
func runVerb(o Options, enc *progress.Encoder, jobID string, v verb) (int, error) {
	proc, err := o.start(verbStart{jobID: jobID, verb: v, hostname: o.Hostname})
	if err != nil {
		return 0, fmt.Errorf("start verb %s: %w", v.name, err)
	}

	rl := &relay{enc: enc, kill: proc.kill}

	var wg sync.WaitGroup

	wg.Go(func() { rl.envelopes(proc.stdout) })
	wg.Go(func() { rl.stderrLines(proc.stderr) })

	<-proc.exited

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
		return 0, fmt.Errorf("wait for verb %s: %w", v.name, rerr)
	}

	writeErr, refused := rl.failure()
	if writeErr != nil {
		return 0, writeErr
	}

	if refused != nil {
		err := enc.Encode(progress.KindLog, progress.LogBody{Stream: stderrStream, Line: fmt.Sprintf("job %s: verb %s stopped: %v", jobID, v.name, refused)})
		if err != nil {
			return 0, fmt.Errorf("job %s: %w", jobID, err)
		}

		return exitVerbFailed, nil
	}

	if code < 0 {
		return exitVerbFailed, nil
	}

	return code, nil
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
