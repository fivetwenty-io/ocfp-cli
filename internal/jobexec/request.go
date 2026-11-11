// Package jobexec runs jobs on a bastion for the instance that owns the bloc.
// It accepts a job only when the request is signed by the owner key that the
// bastion's owner record names.
package jobexec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
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

// requestFields lists the JSON names of Request's fields.
func requestFields() []string {
	return []string{
		"wireContract", "jobId", "verb", "installationId", "instanceId",
		"epoch", "env", "interactive", "issuedAt",
	}
}

// decodeRequest decodes a job request payload, refusing anything that two
// JSON readers could read differently. The payload must be valid UTF-8 and
// hold exactly one JSON object with nothing after it but white space. No key
// may repeat within an object, and no key may equal a field's name only when
// case is ignored, because the standard decoder keeps the last of two equal
// keys and matches names without regard to case. No string may hold U+FFFD,
// because the standard decoder writes that character in place of a bad
// escape, and checkStrings says why that refuses a real U+FFFD too. It
// returns the top-level keys that name no field, so the caller can refuse
// them once it has checked the wire contract.
func decodeRequest(payload []byte) (Request, []string, error) {
	if !utf8.Valid(payload) {
		return Request{}, nil, errors.New("not valid UTF-8")
	}

	trimmed := bytes.TrimLeft(payload, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Request{}, nil, errors.New("not a JSON object")
	}

	var req Request

	dec := json.NewDecoder(bytes.NewReader(trimmed))

	err := dec.Decode(&req)
	if err != nil {
		return Request{}, nil, errors.New("does not decode: " + quote(err.Error()))
	}

	_, err = dec.Token()
	if !errors.Is(err, io.EOF) {
		return Request{}, nil, errors.New("data after the JSON object")
	}

	unknown, err := checkRequestKeys(trimmed)
	if err != nil {
		return Request{}, nil, err
	}

	err = req.checkStrings()
	if err != nil {
		return Request{}, nil, err
	}

	return req, unknown, nil
}

// checkRequestKeys walks the payload's keys. It refuses a key that repeats
// within any object, and a top-level key that equals a field's name only when
// case is ignored, and it returns the top-level keys that name no field.
func checkRequestKeys(data []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return nil, errors.New("does not decode: " + quote(err.Error()))
	}

	if tok != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}

	var unknown []string

	seen := map[string]bool{}

	for dec.More() {
		key, err := nextKey(dec, seen, "")
		if err != nil {
			return nil, err
		}

		known, err := matchField(key)
		if err != nil {
			return nil, err
		}

		if !known {
			unknown = append(unknown, key)
		}

		err = walkValue(dec, key+".")
		if err != nil {
			return nil, err
		}
	}

	return unknown, nil
}

// nextKey reads the next key of an object and refuses it when seen already
// holds it.
func nextKey(dec *json.Decoder, seen map[string]bool, path string) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", errors.New("does not decode: " + quote(err.Error()))
	}

	key, ok := tok.(string)
	if !ok {
		return "", errors.New("an object key is not a string")
	}

	if seen[key] {
		return "", fmt.Errorf("key %s repeats", quote(path+key))
	}

	seen[key] = true

	return key, nil
}

// matchField reports whether key names a field of Request. It refuses a key
// that equals a field's name only when case is ignored.
func matchField(key string) (bool, error) {
	for _, name := range requestFields() {
		if name == key {
			return true, nil
		}

		if strings.EqualFold(name, key) {
			return false, fmt.Errorf("key %s differs from the field %q only in case", quote(key), name)
		}
	}

	return false, nil
}

// walkValue reads one value and refuses a key that repeats within any object
// inside it.
func walkValue(dec *json.Decoder, path string) error {
	tok, err := dec.Token()
	if err != nil {
		return errors.New("does not decode: " + quote(err.Error()))
	}

	switch tok {
	case json.Delim('{'):
		seen := map[string]bool{}

		for dec.More() {
			key, err := nextKey(dec, seen, path)
			if err != nil {
				return err
			}

			err = walkValue(dec, path+key+".")
			if err != nil {
				return err
			}
		}
	case json.Delim('['):
		for dec.More() {
			err := walkValue(dec, path+"[].")
			if err != nil {
				return err
			}
		}
	default:
		return nil
	}

	_, err = dec.Token()
	if err != nil {
		return errors.New("does not decode: " + quote(err.Error()))
	}

	return nil
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
