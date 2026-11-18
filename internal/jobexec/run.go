package jobexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/strictjson"
	"github.com/fivetwenty-io/ocfp-cli/internal/version"
)

const (
	// producer names this CLI in a stream's header.
	producer = "ocfp"

	// pollInterval is how often an attach looks at a spool whose job has
	// not finished.
	pollInterval = 200 * time.Millisecond

	// statusGrace is how long an attach waits for status.json in a spool
	// whose directory exists without one. The run that made the directory
	// writes status.json at once, so a spool still without one after this
	// long belongs to a run that stopped in between.
	statusGrace = 10 * time.Second

	// maxRequestLine bounds the line that holds the signed request.
	maxRequestLine = 1 << 20

	// maxRecordLine bounds one record in a spool's out.
	maxRecordLine = 8 << 20

	// readChunk is how much of out an attach reads at a time.
	readChunk = 32 << 10
)

// Options is what Run needs.
type Options struct {
	// SpoolRoot is the directory that holds one spool per job.
	SpoolRoot string

	// Owner holds the owner record that a request is checked against.
	Owner ownerrecord.Store

	// Stdin carries the signed request as one line of JSON. Run reads that
	// line and nothing after it.
	Stdin io.Reader

	// Stdout receives the progress stream.
	Stdout io.Writer

	// Clock stamps records and checks the request's issue time. When it also
	// has a method After(time.Duration) <-chan time.Time, an attach waits
	// through it between two looks at the spool. Nil means the wall clock.
	Clock progress.Clock

	// Hostname returns the host's name to a verb that runs in this process.
	// A verb in a process of its own uses os.Hostname. Nil means
	// os.Hostname.
	Hostname func() (string, error)

	// CancelOutcome, when set, is told how a cancel request ended the job
	// it names, as progress.CancelOutcomeOf reads it from the stream the
	// request got back. It is not called for a refused request, nor for a
	// job that is orphaned.
	CancelOutcome func(jobID string, outcome progress.CancelOutcome)

	// held reports whether the run that made a spool still holds its lock
	// on out, given the attach's own descriptor of out. Nil means outHeld.
	held func(out *os.File) (bool, error)

	// start starts the verb of a job that this run made. Nil means
	// reexec, which starts it in a process of its own.
	start func(verbStart) (*verbProc, error)

	// ioGrace bounds how long the run waits for a verb's output to end once
	// the verb has exited. Zero means defaultIOGrace.
	ioGrace time.Duration

	// clientGrace bounds how long a run that has finished its job waits on
	// a client that takes none of the rest of the stream. Zero means
	// defaultClientGrace.
	clientGrace time.Duration

	// clientBuffer bounds how much of the stream waits for a client that is
	// behind. Zero means defaultClientBuffer.
	clientBuffer int
}

// waitClock is a Clock that can also wait.
type waitClock interface {
	After(d time.Duration) <-chan time.Time
}

// Run reads one signed request from o.Stdin, checks it against the owner
// record, and runs its job, writing the progress stream to o.Stdout.
//
// A request that does not decode, or that CheckOwner refuses, or whose verb
// is not noop, gets a stream of the header, the error, and a result with exit
// code 2, and Run returns an error in which progress.AsRefusal finds the
// *progress.Refusal. The header names the job only when the request has
// verified, because nothing in an unverified payload is read. No spool is
// made, so the same job id can run once the cause is put right. Stdin that
// cannot be read, and any other failure before the job starts, return a
// plain error with no stream.
//
// The first run of a job makes its spool with CreateSpool, makes out and takes
// an exclusive lock on it, writes status.json with state running and its own
// process id, and runs the verb while it holds the shared lock on the owner
// record. The verb's records reach the stream through a relay that takes only
// events and logs, and every record goes to o.Stdout and to out. When the verb
// ends the relay is sealed, and the run writes the result, syncs out, writes
// exit.json, writes the final status, and only then closes out, which gives
// its lock back. The stream reaches o.Stdout on a goroutine of its own,
// through a bounded buffer, so a client that is slow or stops reading never
// holds up the job, its cancel, or its spool. A failed o.Stdout, or one that
// falls too far behind or takes nothing for a while once the job is done,
// gets nothing more but does not stop the job, so that a later run can still
// attach to the whole stream, and Run reports it once the job is done.
//
// While the verb runs, the first run looks for a cancel marker in the spool
// every 200 ms, on its own clock. The first time it finds one, it passes the
// cancel on to the verb, which stops at its next safe point and runs its undo
// hooks. A job that stops that way, and a job whose marker was there before
// its verb started, end with a JOB_CANCELED error and a result whose cancel
// is stopped, and exit.json records the outcome and when the run first saw
// the marker. A verb that finishes before it reaches a safe point ends the
// job as if no cancel had come. A verb that has not exited once its cancel
// budget has passed from then is killed with every process in its group,
// and the job ends with a cancel of killed. Its stream first carries a
// warning that lists, by kind and id, the work of each undo hook that did
// not run.
//
// Every later run of the job attaches to the spool. It gives the lock on the
// owner record back, copies out to o.Stdout whole record by whole record,
// and, while exit.json is missing, looks again every 200 ms until exit.json
// appears or nothing holds the lock on out any longer. The kernel drops that
// lock when the run's process ends, so a pid that a later process has taken
// cannot keep a dead job running. The pid in status.json is only for people
// to read. An attach writes nothing into the spool. A job whose lock is gone
// without exit.json is orphaned, and the stream ends with a JOB_ORPHANED
// error and a result with exit code 1 after the records it has. Run returns
// a plain error for an orphaned job, and for a job whose exit code is not 0.
//
// A cancel request gets the same owner checks as a run request. It never
// makes a spool: a job with none is refused with JOB_NOT_FOUND, and a job
// that a run of an older wire contract started is refused with
// WIRE_CONTRACT_UNSUPPORTED. A cancel of a job that has not finished writes
// the marker while it holds the shared lock on the owner record, and then
// attaches like any later run, so its reply is the job's stream. A cancel
// of a finished job writes nothing, and its reply is the replay.
func Run(ctx context.Context, o Options) error {
	o, err := o.withDefaults()
	if err != nil {
		return err
	}

	sr, err := readRequest(o.Stdin)
	if err != nil {
		return refusalStream(o, "", err)
	}

	req, release, err := checkOwner(ctx, sr, o.Owner, o.Clock)
	if err != nil {
		return refusalStream(o, "", err)
	}

	return runVerified(ctx, o, req, release)
}

func (o Options) withDefaults() (Options, error) {
	switch {
	case o.SpoolRoot == "":
		return o, errors.New("run job: no spool root")
	case o.Owner == nil:
		return o, errors.New("run job: no owner record store")
	case o.Stdin == nil || o.Stdout == nil:
		return o, errors.New("run job: no stdin or stdout")
	}

	if o.Clock == nil {
		o.Clock = progress.SystemClock()
	}

	if o.Hostname == nil {
		o.Hostname = os.Hostname
	}

	if o.held == nil {
		o.held = outHeld
	}

	if o.start == nil {
		o.start = reexec{}.start
	}

	if o.ioGrace == 0 {
		o.ioGrace = defaultIOGrace
	}

	if o.clientGrace == 0 {
		o.clientGrace = defaultClientGrace
	}

	if o.clientBuffer == 0 {
		o.clientBuffer = defaultClientBuffer
	}

	return o, nil
}

// runVerified runs a request that CheckOwner accepted, and gives back the
// lock on the owner record.
func runVerified(ctx context.Context, o Options, req Request, release func() error) (err error) {
	released := false

	defer func() {
		if !released {
			rerr := release()
			if rerr != nil {
				err = errors.Join(err, fmt.Errorf("release owner record lock: %w", rerr))
			}
		}
	}()

	if req.Action == ActionCancel {
		return cancelJob(ctx, o, req, func() error {
			released = true

			return release()
		})
	}

	v, ok := verbs[req.Verb]
	if !ok {
		return refusalStream(o, req.JobID, refuse(progress.CodeVerbNotAllowed,
			fmt.Sprintf("job request verb %s is not allowed, this host runs only %s", quote(req.Verb), verbList())))
	}

	s, created, err := CreateSpool(o.SpoolRoot, req.JobID)
	if err != nil {
		return err
	}

	if created {
		// The owner check read the clock before this run made its spool. A
		// stall since then can have closed the window, and a job must not
		// start after that.
		err = checkIssueWindow(req, o.Clock.Now())
		if err != nil {
			rerr := removeEmptySpool(s)
			if rerr != nil {
				err = errors.Join(err, rerr)
			}

			return refusalStream(o, req.JobID, err)
		}

		return runNew(o, s, req.JobID, v)
	}

	// An attach acts on nothing the owner record guards.
	released = true

	err = release()
	if err != nil {
		return fmt.Errorf("release owner record lock: %w", err)
	}

	_, err = attach(ctx, o, s, req.JobID, verbHeader(req.JobID, v))

	return err
}

// removeEmptySpool removes the spool that this run made and has put nothing
// in, so that a later request for the same job can make it again. It removes
// a directory only when it is empty. A run that attached in the meantime
// finds no spool and waits out statusGrace, the same as for a run that made
// its spool and died before writing status.json, and then reports the job
// orphaned. Leaving the empty spool would give every later request that
// same fate.
func removeEmptySpool(s Spool) error {
	err := os.Remove(s.Dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove spool of a request that expired: %w", err)
	}

	return nil
}

// cancelJob asks the job that a cancel request names to stop, and then
// attaches to it, so the requester's reply is the job's stream. It opens the
// job's spool and never makes one, because a cancel must not become a job's
// first run. A job with no spool is refused with JOB_NOT_FOUND, and a job
// that a run of an older wire contract started, which never looks for a
// marker, is refused with WIRE_CONTRACT_UNSUPPORTED.
//
// A job that has finished gets no marker, so its spool stays as it was, and
// the requester gets the replay any later run gets. Otherwise cancelJob
// writes the marker while it still holds the owner lock, so that a hand-over
// cannot come between the owner check and the write. A marker that is
// already there stays as it is. release gives the lock back before the
// attach, as a run's attach does.
func cancelJob(ctx context.Context, o Options, req Request, release func() error) error {
	s, err := OpenSpool(o.SpoolRoot, req.JobID)
	if errors.Is(err, ErrSpoolNotFound) {
		return refusalStream(o, req.JobID, refuse(progress.CodeJobNotFound,
			fmt.Sprintf("job %s has no spool on this host", req.JobID)))
	}

	if err != nil {
		return err
	}

	st, ok, err := s.readStatus()
	if err != nil {
		return err
	}

	// A run of this contract can make the spool and not yet have written its
	// status, and it looks for the marker before it starts its verb. A run of
	// an older contract writes its status with no contract in it.
	//
	// A spool with no status yet can also be a run of an older contract that
	// has not written its status, as during an upgrade. That run never reads
	// the marker, so the job runs to its end, and the requester then hears
	// already_finished although the cancel came first. The window is the
	// moment between a run's making its spool and writing its status, and
	// only a binary from before cancels can be in it, so it is accepted.
	if ok && st.WireContract < actionWireContract {
		return refusalStream(o, req.JobID, refuse(progress.CodeWireContractUnsupported,
			fmt.Sprintf("job %s was started by a run of wire contract %d or older, which does not take a cancel",
				req.JobID, actionWireContract-1)))
	}

	_, finished, err := s.readExit()
	if err != nil {
		return err
	}

	if !finished {
		_, err = s.WriteCancel(Cancel{RequestedAt: o.Clock.Now().UTC()})
		if err != nil {
			return err
		}
	}

	err = release()
	if err != nil {
		return fmt.Errorf("release owner record lock: %w", err)
	}

	end, err := attach(ctx, o, s, req.JobID, streamHeader(req.JobID))

	if o.CancelOutcome != nil && end.result != nil {
		if outcome, ok := progress.CancelOutcomeOf(end.lastError, *end.result); ok {
			o.CancelOutcome(req.JobID, outcome)
		}
	}

	return err
}

// readRequest reads one line from r, one byte at a time so that nothing
// after the line is taken from r, and decodes it as a SignedRequest. A line
// may end at the end of r instead of a newline. A read that fails, or finds
// nothing, is a plain error. A line that is too long, or that is not exactly
// one SignedRequest with no unknown field, is refused with
// JOB_REQUEST_INVALID.
func readRequest(r io.Reader) (SignedRequest, error) {
	var (
		line []byte
		b    [1]byte
	)

	for {
		n, err := r.Read(b[:])
		if n == 1 {
			if b[0] == '\n' {
				break
			}

			if len(line) == maxRequestLine {
				return SignedRequest{}, invalid(fmt.Sprintf("job request line is longer than %d bytes", maxRequestLine))
			}

			line = append(line, b[0])
		}

		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				return SignedRequest{}, errors.New("read job request: no request on stdin")
			}

			break
		}

		if err != nil {
			return SignedRequest{}, fmt.Errorf("read job request: %w", err)
		}
	}

	var sr SignedRequest

	// strictjson escapes and bounds the input it quotes, so its message
	// is passed on as it is.
	err := strictjson.Decode(line, &sr)
	if err != nil {
		return SignedRequest{}, invalid("job request line: " + err.Error())
	}

	return sr, nil
}

// refusalStream writes the stream for err when it is a refusal, and returns
// err. Any other error is returned with no stream.
func refusalStream(o Options, jobID string, err error) error {
	ref, ok := progress.AsRefusal(err)
	if !ok {
		return err
	}

	enc := progress.NewEncoder(o.Stdout, o.Clock)

	werr := enc.Header(streamHeader(jobID))
	if werr == nil {
		werr = enc.Encode(progress.KindError, progress.ErrorBody{Code: ref.Code, Message: ref.Message})
	}

	if werr == nil {
		werr = enc.Encode(progress.KindResult, progress.ResultBody{ExitCode: 2})
	}

	if werr != nil {
		return errors.Join(err, werr)
	}

	return err
}

func streamHeader(jobID string) progress.Header {
	return progress.Header{WireContract: progress.WireContract, Producer: producer, Version: version.Get().Version, JobID: jobID}
}

// verbHeader is the header of a job that runs v, which also reports v's cancel
// budget in whole milliseconds.
func verbHeader(jobID string, v verb) progress.Header {
	h := streamHeader(jobID)
	h.CancelBudgetMs = v.cancelBudget.Milliseconds()

	return h
}

// runNew runs the job in the spool this run made.
func runNew(o Options, s Spool, jobID string, v verb) (err error) {
	out, err := s.createOut()
	if err != nil {
		return err
	}

	closed := false

	var client *clientWriter

	// Closing out gives back the lock that tells an attach this run is live,
	// so out stays open until exit.json and the final status are written. On
	// an earlier failure the lock goes, and an attach reports the job
	// orphaned. Either way the client then gets what is left of the stream
	// for it, unless it has stopped taking it.
	defer func() {
		if !closed {
			_ = out.Close()
		}

		if client != nil {
			_ = client.finish(o.clientGrace)
		}
	}()

	err = lockOut(out)
	if err != nil {
		return err
	}

	st := Status{State: StateRunning, PID: os.Getpid(), StartedAt: o.Clock.Now().UTC(), WireContract: progress.WireContract}

	err = s.WriteStatus(st)
	if err != nil {
		return err
	}

	client = newClientWriter(o.Stdout, o.clientBuffer)
	enc := progress.NewEncoder(&teeWriter{spool: out, client: client}, o.Clock)

	err = enc.Header(verbHeader(jobID, v))
	if err != nil {
		return fmt.Errorf("job %s: %w", jobID, err)
	}

	end, err := runVerb(o, enc, s, jobID, v)
	if err != nil {
		return fmt.Errorf("job %s: %w", jobID, err)
	}

	code := end.code

	err = writeEnd(enc, end)
	if err != nil {
		return fmt.Errorf("job %s: %w", jobID, err)
	}

	err = out.Sync()
	if err != nil {
		return fmt.Errorf("job %s: sync out: %w", jobID, err)
	}

	exit := Exit{ExitCode: code, FinishedAt: o.Clock.Now().UTC()}

	if end.cancel != "" {
		seen := end.seenAt
		exit.Cancel, exit.CancelSeenAt = end.cancel, &seen
	}

	err = s.WriteExit(exit)
	if err != nil {
		return err
	}

	st.State = StateExited
	st.ExitCode = &code

	err = s.WriteStatus(st)
	if err != nil {
		return err
	}

	closed = true

	err = out.Close()
	if err != nil {
		return fmt.Errorf("job %s: close out: %w", jobID, err)
	}

	var errs []error

	cerr := client.finish(o.clientGrace)
	if cerr != nil {
		errs = append(errs, fmt.Errorf("job %s: write stream to stdout: %w", jobID, cerr))
	}

	return errors.Join(append(errs, exitError(jobID, code))...)
}

// writeEnd writes the records that end a job's stream: a warning that names
// the work left undone, when a cancelled verb left any, a
// JOB_CANCELED error when a cancel ended the job, and the result, which
// names the cancel's outcome.
func writeEnd(enc *progress.Encoder, end verbEnd) error {
	if len(end.undone) > 0 {
		err := enc.Encode(progress.KindEvent, progress.Event{
			Kind:    progress.EventWarning,
			Step:    stepCancel,
			Message: undoneMessage(end.undone),
			Undone:  end.undone,
		})
		if err != nil {
			return err
		}
	}

	if end.cancel != "" {
		err := enc.Encode(progress.KindError, progress.ErrorBody{Code: progress.CodeJobCanceled, Message: end.message})
		if err != nil {
			return err
		}
	}

	return enc.Encode(progress.KindResult, progress.ResultBody{ExitCode: end.code, Cancel: end.cancel})
}

// undoneMessage names the work left undone, first the work whose hooks did
// not run and then the work whose hooks failed.
func undoneMessage(undone []progress.UndoneWork) string {
	var notRun, failed []string

	for _, w := range undone {
		if w.State == progress.UndoFailed {
			failed = append(failed, w.String())
		} else {
			notRun = append(notRun, w.String())
		}
	}

	var parts []string

	if len(notRun) > 0 {
		parts = append(parts, "undo hooks that did not run: "+strings.Join(notRun, ", "))
	}

	if len(failed) > 0 {
		parts = append(parts, "undo hooks that failed: "+strings.Join(failed, ", "))
	}

	return strings.Join(parts, "; ")
}

// stepCancel is the step that the warning about work left undone appears
// under.
const stepCancel = "cancel"

// exitError reports a job whose exit code is not 0, and is nil otherwise.
func exitError(jobID string, code int) error {
	if code == 0 {
		return nil
	}

	return fmt.Errorf("job %s exited with code %d", jobID, code)
}

// teeWriter writes to the spool first and then hands the bytes to the
// client. A failed spool write fails the record. The client is written on a
// goroutine of its own, so a client that is slow or has stopped reading
// never holds up the job. A client that falls too far behind, or whose write
// fails, gets nothing more, but the job goes on writing to the spool.
type teeWriter struct {
	spool  io.Writer
	client *clientWriter
}

func (t *teeWriter) Write(p []byte) (int, error) {
	n, err := t.spool.Write(p)
	if err != nil {
		return n, err
	}

	t.client.queue(p)

	return len(p), nil
}

// streamEnd is how the stream that an attach wrote ended: its last error,
// if any, and its result, once it has one.
type streamEnd struct {
	lastError *progress.ErrorBody
	result    *progress.ResultBody
}

// attach follows the spool of a job that another run made. header is the
// header it writes for a spool whose out holds none. It returns how the
// stream it wrote ended, along with its error.
func attach(ctx context.Context, o Options, s Spool, jobID string, header progress.Header) (streamEnd, error) {
	fl := &follower{spool: s, jobID: jobID, w: o.Stdout, clock: o.Clock, header: header}
	defer fl.close()

	err := fl.follow(ctx, o)

	return streamEnd{lastError: fl.lastError, result: fl.result}, err
}

// follow copies the spool until its stream is complete or its run is gone.
func (fl *follower) follow(ctx context.Context, o Options) error {
	s := fl.spool

	start := o.Clock.Now()

	for {
		err := ctx.Err()
		if err != nil {
			return err
		}

		done, err := fl.step()
		if err != nil || done {
			return err
		}

		st, ok, err := s.readStatus()
		if err != nil {
			return err
		}

		if !ok {
			if o.Clock.Now().Sub(start) >= statusGrace {
				return fl.orphan(fmt.Sprintf("the run that made its spool wrote no %s within %s", statusFile, statusGrace))
			}
		} else {
			live, err := fl.live(o.held)
			if err != nil {
				return err
			}

			if !live {
				// The run may have written exit.json and ended since the
				// look above, so look once more before calling it orphaned.
				done, err := fl.step()
				if err != nil || done {
					return err
				}

				return fl.orphan(fmt.Sprintf("the run that made it, process %d, no longer holds %s and wrote no %s", st.PID, outFile, exitFile))
			}
		}

		err = wait(ctx, o.Clock, pollInterval)
		if err != nil {
			return err
		}
	}
}

// wait waits d through c when it can wait, and on a timer when it cannot. It
// returns ctx's error when ctx is done.
func wait(ctx context.Context, c progress.Clock, d time.Duration) error {
	var ch <-chan time.Time

	if wc, ok := c.(waitClock); ok {
		ch = wc.After(d)
	} else {
		t := time.NewTimer(d)
		defer t.Stop()

		ch = t.C
	}

	select {
	case <-ctx.Done():
	case <-ch:
	}

	return ctx.Err()
}

// follower copies a spool's out to a writer one whole record at a time, and
// checks each record before it copies it.
type follower struct {
	spool Spool
	jobID string
	w     io.Writer
	clock progress.Clock

	// header is the header the follower writes for a spool that holds none.
	header progress.Header

	out       *os.File
	partial   []byte               // the start of a record whose newline is not yet in out
	next      int64                // the seq of the next record
	lastError *progress.ErrorBody  // the body of the last error record written
	result    *progress.ResultBody // the body of the result record, once one is written
}

// live reports whether the run that made the spool still holds its lock on
// out. The run makes and locks out before it writes status.json, so once
// status.json is there out must be too.
func (fl *follower) live(held func(*os.File) (bool, error)) (bool, error) {
	if fl.out == nil {
		err := fl.drain()
		if err != nil {
			return false, err
		}

		if fl.out == nil {
			return false, fmt.Errorf("spool: %s holds %s but no %s", fl.spool.Dir, statusFile, outFile)
		}
	}

	return held(fl.out)
}

func (fl *follower) close() {
	if fl.out != nil {
		_ = fl.out.Close()
	}
}

// step looks for exit.json and then copies what out holds. Because a run
// writes exit.json only after its whole stream is in out, finding exit.json
// first means the copy that follows reaches the end of the stream. step
// reports true, with the job's outcome, once the stream is complete.
func (fl *follower) step() (bool, error) {
	e, done, err := fl.spool.readExit()
	if err != nil {
		return false, err
	}

	err = fl.drain()
	if err != nil {
		return false, err
	}

	if !done {
		return false, nil
	}

	if len(fl.partial) > 0 {
		return true, fl.damaged("ends part way through a record")
	}

	if fl.result == nil {
		return true, fl.damaged("holds no result although " + exitFile + " is written")
	}

	return true, exitError(fl.jobID, e.ExitCode)
}

// drain copies every whole record that out holds now. It opens out the first
// time out exists.
func (fl *follower) drain() error {
	if fl.out == nil {
		f, ok, err := fl.spool.openOut()
		if err != nil || !ok {
			return err
		}

		fl.out = f
	}

	chunk := make([]byte, readChunk)

	for {
		n, err := fl.out.Read(chunk)
		if n > 0 {
			fl.partial = append(fl.partial, chunk[:n]...)

			werr := fl.copyLines()
			if werr != nil {
				return werr
			}
		}

		if errors.Is(err, io.EOF) || (n == 0 && err == nil) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("spool: read out: %w", err)
		}
	}
}

// copyLines checks and copies each whole line in fl.partial.
func (fl *follower) copyLines() error {
	used := 0

	for {
		i := bytes.IndexByte(fl.partial[used:], '\n')
		if i < 0 {
			break
		}

		line := fl.partial[used : used+i+1]

		err := fl.check(line)
		if err != nil {
			return err
		}

		_, err = fl.w.Write(line)
		if err != nil {
			return fmt.Errorf("write stream to stdout: %w", err)
		}

		used += i + 1
	}

	if used > 0 {
		// Keep only the partial record, in its own memory, so the buffer
		// that held the lines before it can go.
		fl.partial = append([]byte(nil), fl.partial[used:]...)
	}

	if len(fl.partial) > maxRecordLine {
		return fl.damaged(fmt.Sprintf("holds a record longer than %d bytes", maxRecordLine))
	}

	return nil
}

// check refuses a line that is not the next record of a well formed stream:
// valid UTF-8 and JSON, numbered on from the record before it, a header
// first and only first, and nothing after the result.
func (fl *follower) check(line []byte) error {
	if !utf8.Valid(line) {
		return fl.damaged(fmt.Sprintf("record %d is not valid UTF-8", fl.next))
	}

	var r progress.Record

	err := json.Unmarshal(line, &r)
	if err != nil {
		return fl.damaged(fmt.Sprintf("record %d does not decode: %s", fl.next, quote(err.Error())))
	}

	switch {
	case fl.result != nil:
		return fl.damaged(fmt.Sprintf("holds record %d after the result", fl.next))
	case r.Seq != fl.next:
		return fl.damaged(fmt.Sprintf("record %d has seq %d", fl.next, r.Seq))
	case (r.Kind == progress.KindHeader) != (fl.next == 0):
		return fl.damaged(fmt.Sprintf("record %d is of kind %s", fl.next, quote(string(r.Kind))))
	case r.Kind == progress.KindResult:
		var res progress.ResultBody

		err := json.Unmarshal(r.Body, &res)
		if err != nil {
			return fl.damaged(fmt.Sprintf("result record %d does not decode: %s", fl.next, quote(err.Error())))
		}

		fl.result = &res
	case r.Kind == progress.KindError:
		var e progress.ErrorBody

		err := json.Unmarshal(r.Body, &e)
		if err != nil {
			return fl.damaged(fmt.Sprintf("error record %d does not decode: %s", fl.next, quote(err.Error())))
		}

		fl.lastError = &e
	}

	fl.next++

	return nil
}

func (fl *follower) damaged(what string) error {
	return fmt.Errorf("spool: out in %s %s", fl.spool.Dir, what)
}

// orphan ends the stream of a job whose run is gone. It drops a partial last
// record and adds a JOB_ORPHANED error and a result with exit code 1, after
// a header of its own when out held no record. A stream that already ended
// in a result is complete, so orphan adds nothing to it and reports the
// result's exit code.
func (fl *follower) orphan(why string) error {
	if fl.result != nil {
		return exitError(fl.jobID, fl.result.ExitCode)
	}

	msg := fmt.Sprintf("job %s is orphaned: %s", fl.jobID, why)

	var err error

	if fl.next == 0 {
		err = fl.emit(progress.KindHeader, fl.header)
	}

	orphaned := progress.ErrorBody{Code: progress.CodeJobOrphaned, Message: msg}
	result := progress.ResultBody{ExitCode: 1}

	if err == nil {
		err = fl.emit(progress.KindError, orphaned)
	}

	if err == nil {
		err = fl.emit(progress.KindResult, result)
	}

	fl.lastError, fl.result = &orphaned, &result

	return errors.Join(errors.New(string(progress.CodeJobOrphaned)+": "+msg), err)
}

// emit writes one record numbered on from the records copied so far.
func (fl *follower) emit(kind progress.Kind, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode %s body: %w", kind, err)
	}

	line, err := json.Marshal(progress.Record{Kind: kind, Seq: fl.next, Time: fl.clock.Now().UTC(), Body: raw})
	if err != nil {
		return fmt.Errorf("encode %s record: %w", kind, err)
	}

	_, err = fl.w.Write(append(line, '\n'))
	if err != nil {
		return fmt.Errorf("write %s record: %w", kind, err)
	}

	fl.next++

	return nil
}
