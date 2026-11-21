package jobexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// WireContractAnswerFormat is the format of every line AnswerWireContract
// writes.
const WireContractAnswerFormat = 1

// WireContractAnswer is the line AnswerWireContract writes for a query that
// the owner signed for this host. WireContract is progress.WireContract, the
// contract this CLI reads, and JobID is the query's job id.
type WireContractAnswer struct {
	Format       int    `json:"format"`
	WireContract int    `json:"wireContract"`
	JobID        string `json:"jobId"`
}

// WireContractRefusal is the line AnswerWireContract writes for a refused
// query. JobID is set only once the query's signature has verified and its
// payload has decoded to a job id of the job id form, so nothing from a
// payload the owner did not sign ever reaches it.
type WireContractRefusal struct {
	Format int                `json:"format"`
	JobID  string             `json:"jobId,omitempty"`
	Error  progress.ErrorBody `json:"error"`
}

// contractChecks are the checks of a query that job contract answers.
var contractChecks = checks{contract: checkContractFloor, action: checkContractAction}

// ContractOptions is what AnswerWireContract needs.
type ContractOptions struct {
	// Owner holds the owner record that a query is checked against.
	// AnswerWireContract reads it only through Owner.CurrentOnly, so a
	// record left under ownerrecord.LegacyRecordFile is never read.
	Owner *ownerrecord.DirStore

	// Stdin carries the signed query as one line of JSON.
	// AnswerWireContract reads that line and nothing after it.
	Stdin io.Reader

	// Stdout receives the one line of the answer.
	Stdout io.Writer

	// Clock checks the query's issue time. Nil means the wall clock.
	Clock progress.Clock
}

// AnswerWireContract reads one signed wire contract query from o.Stdin and,
// when the owner signed it for this host, writes to o.Stdout the one line
// of a WireContractAnswer that names progress.WireContract.
//
// The query is a SignedRequest of the same format, signing context, and
// payload as a request that job exec runs, with the action
// ActionWireContract. It passes the same checks in the same order, against
// the owner record under ownerrecord.RecordFile only, with two differences.
// Its wire contract may be progress.WireContract or any later one, and only
// an older one is refused with WIRE_CONTRACT_UNSUPPORTED. Its action must be
// ActionWireContract, and it names no verb, env, or interactive, or it is
// refused with JOB_REQUEST_INVALID. A directory that holds the record only
// under ownerrecord.LegacyRecordFile holds no record for a query, which is
// refused with OWNER_RECORD_MISSING.
//
// A refused query gets the one line of a WireContractRefusal, and
// AnswerWireContract returns an error in which progress.AsRefusal finds the
// *progress.Refusal whose code and message the line carries. Stdin that
// cannot be read or holds nothing, a lock that cannot be taken or given
// back, and any other failure that is not a refusal return a plain error
// with nothing written. The shared lock on the owner record is given back
// before anything is written, on every path.
func AnswerWireContract(ctx context.Context, o ContractOptions) error {
	switch {
	case o.Owner == nil:
		return errors.New("answer wire contract: no owner record store")
	case o.Stdin == nil || o.Stdout == nil:
		return errors.New("answer wire contract: no stdin or stdout")
	}

	clock := o.Clock
	if clock == nil {
		clock = progress.SystemClock()
	}

	sr, err := readRequest(o.Stdin)
	if err != nil {
		// A line that is not a signed request is refused with a fixed
		// message, because the decoder's message quotes the line, and
		// nothing the owner did not sign is written back.
		if _, ok := progress.AsRefusal(err); ok {
			err = invalid("job request line is not a signed request")
		}

		return writeContractRefusal(o.Stdout, Request{}, err)
	}

	req, release, err := checkOwnerAs(ctx, sr, o.Owner.CurrentOnly(), clock, contractChecks)
	if err != nil {
		return writeContractRefusal(o.Stdout, req, err)
	}

	// The answer acts on nothing the owner record guards, so the lock is
	// given back before it is written.
	err = release()
	if err != nil {
		return fmt.Errorf("release owner record lock: %w", err)
	}

	return writeLine(o.Stdout, WireContractAnswer{
		Format:       WireContractAnswerFormat,
		WireContract: progress.WireContract,
		JobID:        req.JobID,
	})
}

// writeContractRefusal writes the refusal line for err when it is a refusal,
// and returns err. Any other error is returned with nothing written. The line
// names req's job only when its job id is of the job id form, which a
// request that has not verified and decoded never is, because it is then the
// zero Request.
func writeContractRefusal(w io.Writer, req Request, err error) error {
	ref, ok := progress.AsRefusal(err)
	if !ok {
		return err
	}

	line := WireContractRefusal{
		Format: WireContractAnswerFormat,
		Error:  progress.ErrorBody{Code: ref.Code, Message: ref.Message},
	}

	if jobIDPattern.MatchString(req.JobID) {
		line.JobID = req.JobID
	}

	werr := writeLine(w, line)
	if werr != nil {
		return errors.Join(err, werr)
	}

	return err
}

// writeLine writes v to w as one line of JSON.
func writeLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode wire contract answer: %w", err)
	}

	_, err = w.Write(append(b, '\n'))
	if err != nil {
		return fmt.Errorf("write wire contract answer: %w", err)
	}

	return nil
}
