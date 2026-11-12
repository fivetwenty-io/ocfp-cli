// Package jobexec runs jobs on a bastion for the instance that owns the bloc.
// It accepts a job only when the request is signed by the owner key that the
// bastion's owner record names.
package jobexec

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fivetwenty-io/ocfp-cli/internal/strictjson"
)

// RequestFormatV1 is the only signed job request format this package reads.
const RequestFormatV1 = 1

// RequestSigningContext is prefixed to a job request's payload before it is
// signed. It differs from the owner record's signing context, so that a signed
// owner record can never pass as a signed job request, nor the other way round.
const RequestSigningContext = "ocfp-job-request-v1\n"

// Request is the payload of a job request.
type Request struct {
	// WireContract is the version of the progress stream the requester reads.
	WireContract int `json:"wireContract"`

	// JobID names the job and its spool directory.
	JobID string `json:"jobId"`

	// Verb names what the job does.
	Verb string `json:"verb"`

	// InstallationID, InstanceID, and Epoch name the requester. They must
	// match the owner record.
	InstallationID string `json:"installationId"`
	InstanceID     string `json:"instanceId"`
	Epoch          uint64 `json:"epoch"`

	// Org and Bloc name the organisation and the bloc the job is for. They
	// must match the owner record, which names the org and the bloc this
	// host serves.
	Org  string `json:"org"`
	Bloc string `json:"bloc"`

	// Env holds environment variables for the job.
	Env map[string]string `json:"env,omitempty"`

	// Interactive says whether the job reads answers on its standard input.
	Interactive bool `json:"interactive,omitempty"`

	// IssuedAt is when the requester signed the request.
	IssuedAt time.Time `json:"issuedAt"`
}

// SignedRequest is the signed form of a job request as it is sent. The payload
// is the request's JSON exactly as it was signed, kept as bytes so that the
// bastion verifies what the requester signed and never a re-encoding of it.
type SignedRequest struct {
	Format    int    `json:"format"`
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

// RequestSigningBytes returns the message that is signed for payload, which is
// RequestSigningContext followed by payload. The result never shares memory
// with payload.
func RequestSigningBytes(payload []byte) []byte {
	msg := make([]byte, 0, len(RequestSigningContext)+len(payload))
	msg = append(msg, RequestSigningContext...)

	return append(msg, payload...)
}

// decodeRequest decodes a job request payload with strictjson, which refuses
// anything that two JSON readers could read differently, and then refuses a
// string that holds U+FFFD, which checkStrings explains. It returns the
// top-level keys that name no field, so the caller can refuse them once it
// has checked the wire contract.
func decodeRequest(payload []byte) (Request, []string, error) {
	var req Request

	unknown, err := strictjson.DecodeWithUnknown(payload, &req)
	if err != nil {
		return Request{}, nil, err
	}

	err = req.checkStrings()
	if err != nil {
		return Request{}, nil, err
	}

	return req, unknown, nil
}

// checkStrings refuses a request whose strings hold U+FFFD, which the
// standard decoder writes in place of an escape that names no character. The
// decoded text cannot tell that character from a U+FFFD the requester wrote,
// so a request that really means U+FFFD is refused as well, on purpose. The
// fields are checked in a fixed order, and env in key order, so the message
// always names the same field.
func (r Request) checkStrings() error {
	for _, f := range []struct{ name, value string }{
		{"job id", r.JobID},
		{"verb", r.Verb},
		{"installation id", r.InstallationID},
		{"instance id", r.InstanceID},
		{"org", r.Org},
		{"bloc", r.Bloc},
	} {
		if strings.ContainsRune(f.value, utf8.RuneError) {
			return fmt.Errorf("%s holds U+FFFD", f.name)
		}
	}

	for _, k := range slices.Sorted(maps.Keys(r.Env)) {
		if strings.ContainsRune(k, utf8.RuneError) || strings.ContainsRune(r.Env[k], utf8.RuneError) {
			return fmt.Errorf("env %s holds U+FFFD", quote(k))
		}
	}

	return nil
}

// maxQuoted bounds how many bytes of request text a message quotes.
const maxQuoted = 80

// quote returns s quoted with %+q, which escapes every byte outside printable
// ASCII, cut at maxQuoted bytes on a rune boundary, so that text from a
// request never reaches a message unescaped or unbounded.
func quote(s string) string {
	if len(s) <= maxQuoted {
		return fmt.Sprintf("%+q", s)
	}

	cut := maxQuoted
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}

	return fmt.Sprintf("%+q...", s[:cut])
}
