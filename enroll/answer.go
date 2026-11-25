package enroll

import (
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"
)

// State is where a request stands, as an answer reports it.
type State string

// The states of a request.
const (
	// StatePending says the request waits for a person. Its answer
	// carries the verdict of the host key check.
	StatePending State = "pending"
	// StateApproved says a person approved the worker. Its answer carries
	// the worker id and a fresh credential for the key that asked.
	StateApproved State = "approved"
	// StateRejected says a person rejected the worker key.
	StateRejected State = "rejected"
	// StateRemoved says the worker key was enrolled and then removed.
	StateRemoved State = "removed"
	// StateRefused says the managing system refused the request. Its
	// answer carries the refusal's code.
	StateRefused State = "refused"
)

// Verdict is how the attestation's host key compares with the bloc's
// record.
type Verdict string

// The verdicts of the host key check.
const (
	VerdictMatch      Verdict = "match"
	VerdictMismatch   Verdict = "mismatch"
	VerdictUnverified Verdict = "unverified"
)

// credentialForm is the form of a compact JWS.
var credentialForm = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)

// Refusal is a refused request's code. It carries no message, since the
// asker is not yet trusted.
type Refusal struct {
	Code string `json:"code"`
}

// Answer answers one request. Request is the request's id, which a
// refusal of a request whose envelope did not decode leaves empty.
type Answer struct {
	State      State      `json:"state"`
	Request    string     `json:"request,omitempty"`
	Verdict    Verdict    `json:"verdict,omitempty"`
	WorkerID   string     `json:"workerId,omitempty"`
	Credential string     `json:"credential,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	Refusal    *Refusal   `json:"refusal,omitempty"`
}

// withheld stands for a secret's value wherever the package names the
// secret only by its key name.
const withheld = "withheld"

// String names the answer's fields and gives every value but the
// credential, which it names only by its key name, so that a log line or
// an error never carries the JWT.
func (a Answer) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "Answer{state: %s, request: %s", a.State, a.Request)

	if a.Verdict != "" {
		fmt.Fprintf(&b, ", verdict: %s", a.Verdict)
	}

	if a.WorkerID != "" {
		fmt.Fprintf(&b, ", workerId: %s", a.WorkerID)
	}

	if a.Credential != "" {
		b.WriteString(", credential: " + withheld)
	}

	if a.ExpiresAt != nil {
		fmt.Fprintf(&b, ", expiresAt: %s", a.ExpiresAt.UTC().Format(time.RFC3339))
	}

	if a.Refusal != nil {
		fmt.Fprintf(&b, ", refusal: %s", a.Refusal.Code)
	}

	b.WriteString("}")

	return b.String()
}

// GoString withholds the credential as String does.
func (a Answer) GoString() string { return a.String() }

// Format withholds the credential as String does, under every verb.
func (a Answer) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, a.String()) }

// LogValue gives slog the answer's fields with the credential named only
// by its key name, so a JSON handler, which ignores String, never
// carries the JWT.
func (a Answer) LogValue() slog.Value {
	attrs := []slog.Attr{slog.String("state", string(a.State)), slog.String("request", a.Request)}

	if a.Verdict != "" {
		attrs = append(attrs, slog.String("verdict", string(a.Verdict)))
	}

	if a.WorkerID != "" {
		attrs = append(attrs, slog.String("workerId", a.WorkerID))
	}

	if a.Credential != "" {
		attrs = append(attrs, slog.String("credential", withheld))
	}

	if a.ExpiresAt != nil {
		attrs = append(attrs, slog.Time("expiresAt", a.ExpiresAt.UTC()))
	}

	if a.Refusal != nil {
		attrs = append(attrs, slog.String("refusal", a.Refusal.Code))
	}

	return slog.GroupValue(attrs...)
}

// Pending answers a request that waits for a person.
func Pending(id string, v Verdict) Answer {
	return Answer{State: StatePending, Request: id, Verdict: v}
}

// Approved answers a request whose worker was approved, with its worker
// id and a credential that lapses at expires.
func Approved(id, worker, credential string, expires time.Time) Answer {
	exp := expires.UTC()

	return Answer{State: StateApproved, Request: id, WorkerID: worker, Credential: credential, ExpiresAt: &exp}
}

// Rejected answers a request whose worker key was rejected.
func Rejected(id string) Answer { return Answer{State: StateRejected, Request: id} }

// Removed answers a request whose worker key was removed.
func Removed(id string) Answer { return Answer{State: StateRemoved, Request: id} }

// Refused answers a refused request with code, one of RefusalCodes. id
// is the one DecodeEnvelope returned, which is empty only when the
// envelope did not decode.
func Refused(id, code string) Answer {
	return Answer{State: StateRefused, Request: id, Refusal: &Refusal{Code: code}}
}

// Encode returns the answer's bytes as the bus carries them, and refuses
// an answer that does not validate.
func (a Answer) Encode() ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}

	b, err := canonicalJSON(a)
	if err != nil {
		return nil, refuse(CodeAnswerInvalid, "the answer does not encode")
	}

	return b, nil
}

// Validate holds an answer to its form: each state carries exactly its
// own fields. It refuses with CodeAnswerInvalid.
func (a Answer) Validate() error {
	if (a.State != StateRefused || a.Request != "") && !ValidRequestID(a.Request) {
		return a.bad("names no request")
	}

	switch a.State {
	case StatePending:
		return a.validatePending()
	case StateApproved:
		return a.validateApproved()
	case StateRejected, StateRemoved:
		if a.Verdict != "" || a.approvedOnly() || a.Refusal != nil {
			return a.bad("carries another state's fields")
		}

		return nil
	case StateRefused:
		return a.validateRefused()
	default:
		return refuse(CodeAnswerInvalid, fmt.Sprintf("the answer's state %q is not a known state", a.State))
	}
}

func (a Answer) bad(what string) error {
	return refuse(CodeAnswerInvalid, fmt.Sprintf("a %s answer %s", a.State, what))
}

func (a Answer) approvedOnly() bool {
	return a.WorkerID != "" || a.Credential != "" || a.ExpiresAt != nil
}

func (a Answer) validatePending() error {
	if a.Verdict != VerdictMatch && a.Verdict != VerdictMismatch && a.Verdict != VerdictUnverified {
		return a.bad("has no verdict")
	}

	if a.approvedOnly() || a.Refusal != nil {
		return a.bad("carries another state's fields")
	}

	return nil
}

func (a Answer) validateApproved() error {
	switch {
	case !ValidWorkerID(a.WorkerID):
		return a.bad("names no worker")
	case len(a.Credential) > MaxCredential || !credentialForm.MatchString(a.Credential):
		return a.bad("carries no credential")
	case a.ExpiresAt == nil || a.ExpiresAt.IsZero():
		return a.bad("has no expiry")
	case a.Verdict != "" || a.Refusal != nil:
		return a.bad("carries another state's fields")
	}

	return nil
}

func (a Answer) validateRefused() error {
	if a.Refusal == nil || !slices.Contains(RefusalCodes, a.Refusal.Code) {
		return a.bad("carries no refusal code")
	}

	if a.Verdict != "" || a.approvedOnly() {
		return a.bad("carries another state's fields")
	}

	return nil
}

// DecodeAnswer reads one answer for the request whose id is request. It
// refuses with CodeAnswerInvalid a body longer than MaxMessage, an
// unknown field, trailing data, an answer that does not hold to its
// form, and an answer that names any other request or none.
//
// A bus publisher can set any reply subject it likes, so another asker
// can have the managing system answer its own request into this
// asker's inbox. The request id is what ties an answer to the asker
// that reads it, and every refusal from the envelope's decoding on
// names that id. A refusal that names no request is never taken as the
// asker's own.
func DecodeAnswer(body []byte, request string) (Answer, error) {
	if !ValidRequestID(request) {
		return Answer{}, refuse(CodeAnswerInvalid, "no request was named to read an answer for")
	}

	if len(body) > MaxMessage {
		return Answer{}, refuse(CodeAnswerInvalid, fmt.Sprintf("the answer is longer than %d bytes", MaxMessage))
	}

	var a Answer
	if err := decodeStrict(body, &a); err != nil {
		return Answer{}, refuse(CodeAnswerInvalid, "the answer does not decode")
	}

	if err := a.Validate(); err != nil {
		return Answer{}, err
	}

	if a.Request != request {
		return Answer{}, refuse(CodeAnswerInvalid, "the answer is not for this request")
	}

	return a, nil
}
