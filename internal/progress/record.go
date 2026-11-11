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
}

// ErrorBody is the body of an error record.
type ErrorBody struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

// ResultBody is the body of the final record of a stream.
type ResultBody struct {
	ExitCode int `json:"exitCode"`
}

// LogBody is the body of a log record.
type LogBody struct {
	Stream string `json:"stream"`
	Line   string `json:"line"`
}
