package jobexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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

	// Hostname returns the host's name. Nil means os.Hostname.
	Hostname func() (string, error)

	// held reports whether the run that made a spool still holds its lock
	// on out, given the attach's own descriptor of out. Nil means outHeld.
	held func(out *os.File) (bool, error)
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
// process id, runs the verb in this process while it holds the shared lock on
// the owner record, and writes every record to o.Stdout and to out. When the
// verb ends it syncs out, writes exit.json, writes the final status, and only
// then closes out, which gives its lock back. A failed o.Stdout does not stop
// the job, so that a later run can still attach to the whole stream, and Run
// reports it once the job is done.
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

	if req.Verb != verbNoop {
		return refusalStream(o, req.JobID, refuse(progress.CodeVerbNotAllowed,
			fmt.Sprintf("job request verb %s is not allowed, this host runs only %q", quote(req.Verb), verbNoop)))
	}

	s, created, err := CreateSpool(o.SpoolRoot, req.JobID)
	if err != nil {
		return err
	}

	if created {
		return runNew(o, s, req.JobID)
	}

	// An attach acts on nothing the owner record guards.
	released = true

	err = release()
	if err != nil {
		return fmt.Errorf("release owner record lock: %w", err)
	}

	return attach(ctx, o, s, req.JobID)
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

// runNew runs the job in the spool this run made.
func runNew(o Options, s Spool, jobID string) (err error) {
	out, err := s.createOut()
	if err != nil {
		return err
	}

	closed := false

	// Closing out gives back the lock that tells an attach this run is live,
	// so out stays open until exit.json and the final status are written. On
	// an earlier failure the lock goes, and an attach reports the job
	// orphaned.
	defer func() {
		if !closed {
			_ = out.Close()
		}
	}()

	err = lockOut(out)
	if err != nil {
		return err
	}

	st := Status{State: StateRunning, PID: os.Getpid(), StartedAt: o.Clock.Now().UTC()}

	err = s.WriteStatus(st)
	if err != nil {
		return err
	}

	tee := &teeWriter{spool: out, client: o.Stdout}
	enc := progress.NewEncoder(tee, o.Clock)

	err = enc.Header(streamHeader(jobID))
	if err != nil {
		return fmt.Errorf("job %s: %w", jobID, err)
	}

	code, err := runNoop(enc, o.Hostname)
	if err != nil {
		return fmt.Errorf("job %s: %w", jobID, err)
	}

	err = out.Sync()
	if err != nil {
		return fmt.Errorf("job %s: sync out: %w", jobID, err)
	}

	err = s.WriteExit(Exit{ExitCode: code, FinishedAt: o.Clock.Now().UTC()})
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

	if tee.clientErr != nil {
		errs = append(errs, fmt.Errorf("job %s: write stream to stdout: %w", jobID, tee.clientErr))
	}

	return errors.Join(append(errs, exitError(jobID, code))...)
}

// exitError reports a job whose exit code is not 0, and is nil otherwise.
func exitError(jobID string, code int) error {
	if code == 0 {
		return nil
	}

	return fmt.Errorf("job %s exited with code %d", jobID, code)
}

// teeWriter writes to the spool first and then to the client. A failed spool
// write fails the record. A failed client write is kept, and the client gets
// nothing more, but the job goes on writing to the spool.
type teeWriter struct {
	spool     io.Writer
	client    io.Writer
	clientErr error
}

func (t *teeWriter) Write(p []byte) (int, error) {
	n, err := t.spool.Write(p)
	if err != nil {
		return n, err
	}

	if t.clientErr == nil {
		_, t.clientErr = t.client.Write(p)
	}

	return len(p), nil
}

// attach follows the spool of a job that another run made.
func attach(ctx context.Context, o Options, s Spool, jobID string) error {
	fl := &follower{spool: s, jobID: jobID, w: o.Stdout, clock: o.Clock}
	defer fl.close()

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

	out     *os.File
	partial []byte // the start of a record whose newline is not yet in out
	next    int64  // the seq of the next record
	result  *int   // the exit code of the result record, once one is seen
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

		fl.result = &res.ExitCode
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
		return exitError(fl.jobID, *fl.result)
	}

	msg := fmt.Sprintf("job %s is orphaned: %s", fl.jobID, why)

	var err error

	if fl.next == 0 {
		err = fl.emit(progress.KindHeader, streamHeader(fl.jobID))
	}

	if err == nil {
		err = fl.emit(progress.KindError, progress.ErrorBody{Code: progress.CodeJobOrphaned, Message: msg})
	}

	if err == nil {
		err = fl.emit(progress.KindResult, progress.ResultBody{ExitCode: 1})
	}

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
