package progress

import (
	"encoding/json"
	"time"
)

// Kind names the type of a record in the stream.
type Kind string

// The record kinds. The first record of every stream is a header.
const (
	KindHeader   Kind = "header"
	KindEvent    Kind = "event"
	KindQuestion Kind = "question"
	KindLog      Kind = "log"
	KindResult   Kind = "result"
	KindError    Kind = "error"
)

// Record is one line of the stream. Seq is 0 for the header and counts up by
// one for every record after it.
type Record struct {
	Kind Kind            `json:"kind"`
	Seq  int64           `json:"seq"`
	Time time.Time       `json:"time"`
	Body json.RawMessage `json:"body"`
}

// Header is the body of the first record. It names the producer and the wire
// contract the rest of the stream follows.
type Header struct {
	WireContract int    `json:"wireContract"`
	Producer     string `json:"producer"`
	Version      string `json:"version"`
	JobID        string `json:"jobId,omitempty"`

	// CancelBudgetMs is how long, in whole milliseconds, the verb that runs
	// has to stop once a cancel arrives. It is absent from a header that
	// names no verb.
	CancelBudgetMs int64 `json:"cancelBudgetMs,omitempty"`
}

// ErrorBody is the body of an error record.
type ErrorBody struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

// CancelOutcome says how a cancelled job ended.
type CancelOutcome string

// The cancel outcomes. A run stores only stopped and killed. A requester
// derives already_finished from a stream that carries no cancel.
const (
	// CancelStopped means the verb reached a safe point and stopped.
	CancelStopped CancelOutcome = "stopped"

	// CancelKilled means the cancel budget passed and the verb was killed.
	CancelKilled CancelOutcome = "killed"

	// CancelAlreadyFinished means the job had ended before the cancel took
	// effect.
	CancelAlreadyFinished CancelOutcome = "already_finished"
)

// ResultBody is the body of the final record of a stream. Cancel is set only
// when a cancel ended the job.
type ResultBody struct {
	ExitCode int           `json:"exitCode"`
	Cancel   CancelOutcome `json:"cancel,omitempty"`
}

// CancelOutcomeOf is the outcome that a cancel requester reads from the
// stream it got back, given the stream's last error, if any, and its result.
// A result whose cancel is stopped or killed gives that outcome. A complete
// stream with no error and no cancel in its result means the job had
// already finished. A stream that ends in JOB_ORPHANED or in a refusal, or
// whose result names a cancel this CLI does not know, has no outcome, and
// CancelOutcomeOf then reports false.
func CancelOutcomeOf(lastError *ErrorBody, res ResultBody) (CancelOutcome, bool) {
	switch res.Cancel {
	case CancelStopped, CancelKilled:
		return res.Cancel, true
	case "":
	case CancelAlreadyFinished:
		// A run never stores it, so a result that names it is not one.
		return "", false
	default:
		return "", false
	}

	if lastError != nil {
		return "", false
	}

	return CancelAlreadyFinished, true
}

// LogBody is the body of a log record.
type LogBody struct {
	Stream string `json:"stream"`
	Line   string `json:"line"`
}
