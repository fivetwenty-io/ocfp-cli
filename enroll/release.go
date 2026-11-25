package enroll

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/sigchain"
)

// The bounds of the release read's messages. A chunk answer carries up
// to MaxChunkSize bytes in base64, which fits MaxReleaseAnswer.
const (
	MaxReleaseRequest = 1 << 10
	MaxReleaseAnswer  = 1 << 20
	// StatusInterval is the least time between two status requests.
	StatusInterval = time.Minute
)

// CodeReleaseAnswerInvalid refuses, on the asking side, a release
// answer that does not decode, does not hold to its form, or answers
// another request.
const CodeReleaseAnswerInvalid = "RELEASE_ANSWER_INVALID"

var codeForm = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// The release read has three requests, one per operation of
// ReleaseSubject, and an answer for each. Every message is canonical
// JSON, and every answer either refuses with a code or carries its
// result, never both. Byte fields are standard base64.

// ManifestRequest asks for the manifest of the version the answering
// copy runs. It carries no fields.
type ManifestRequest struct{}

// ChunkRequest asks for chunk Index of the binary for Arch, of Version.
type ChunkRequest struct {
	Version string `json:"version"`
	Arch    string `json:"arch"`
	Index   int    `json:"index"`
}

// StatusRequest reports the enrollment service's last refusal code, or
// none, and its clock reading, at most once per StatusInterval.
type StatusRequest struct {
	Code  string    `json:"code,omitempty"`
	Clock time.Time `json:"clock"`
}

// ReleaseRefusal is a refused release answer. Version and NotAfter are
// set only on CodeReleaseSignatureExpired, where they name the version
// whose signature lapsed and the end of its window.
type ReleaseRefusal struct {
	Code     string     `json:"code"`
	Version  string     `json:"version,omitempty"`
	NotAfter *time.Time `json:"notAfter,omitempty"`
}

// Err returns the refusal as a coded Error, which a fetch can return.
func (r ReleaseRefusal) Err() error {
	e := refuse(r.Code, "the release read refused the request")
	if r.NotAfter != nil {
		e.NotAfter = *r.NotAfter
	}

	return e
}

func (r ReleaseRefusal) validate() error {
	if !slices.Contains(ReleaseCodes, r.Code) {
		return fmt.Errorf("the refusal's code %q is not a release code", r.Code)
	}

	lapsed := r.Code == CodeReleaseSignatureExpired
	if lapsed != (r.Version != "") || lapsed != (r.NotAfter != nil) {
		return fmt.Errorf("only %s names a version and an end date, and it names both", CodeReleaseSignatureExpired)
	}

	if lapsed && (!ValidVersion(r.Version) || !utcSecond(*r.NotAfter)) {
		return errors.New("the refusal's version or end date is not in its form")
	}

	return nil
}

// ManifestAnswer answers a ManifestRequest with the manifest set: the
// manifest, the newest signature set for its version, and the newest
// revocation statement.
type ManifestAnswer struct {
	Refusal *ReleaseRefusal `json:"refusal,omitempty"`
	Set     *ManifestSet    `json:"set,omitempty"`
}

// ChunkAnswer answers a ChunkRequest with the chunk and its SHA-256, in
// lower-case hex, and names the chunk it carries. Like every zero field,
// an Index of 0 is left out of the encoding.
type ChunkAnswer struct {
	Refusal *ReleaseRefusal `json:"refusal,omitempty"`
	Version string          `json:"version,omitempty"`
	Arch    string          `json:"arch,omitempty"`
	Index   int             `json:"index,omitempty"`
	Chunk   []byte          `json:"chunk,omitempty"`
	SHA256  string          `json:"sha256,omitempty"`
}

// StatusAnswer acknowledges a StatusRequest, or refuses it.
type StatusAnswer struct {
	Refusal *ReleaseRefusal `json:"refusal,omitempty"`
}

func (ManifestRequest) validate() error { return nil }

func (r ChunkRequest) validate() error {
	if !ValidVersion(r.Version) || !archForm.MatchString(r.Arch) || r.Index < 0 || r.Index >= MaxChunks {
		return errors.New("the chunk request is not in its form")
	}

	return nil
}

func (r StatusRequest) validate() error {
	if (r.Code != "" && !codeForm.MatchString(r.Code)) || !utcSecond(r.Clock) {
		return errors.New("the status request is not in its form")
	}

	return nil
}

func (a ManifestAnswer) validate() error {
	if (a.Refusal == nil) == (a.Set == nil) {
		return errors.New("the manifest answer must refuse or carry a set, and not both")
	}

	if a.Refusal != nil {
		return a.Refusal.validate()
	}

	return a.Set.validate()
}

func (s ManifestSet) validate() error {
	sized := func(b []byte, limit int) bool { return len(b) > 0 && len(b) <= limit }

	switch {
	case !sized(s.Manifest, MaxManifest) || !sized(s.ManifestSig, sigchain.MaxSignature):
		return errors.New("the set's manifest or its signature is empty or too long")
	case !sized(s.Statement, sigchain.MaxStatement) || !sized(s.StatementSig, sigchain.MaxSignature):
		return errors.New("the set's statement or its signature is empty or too long")
	case (len(s.Revocation) == 0) != (len(s.RevocationSig) == 0):
		return errors.New("the set's revocation comes without its signature, or the reverse")
	case len(s.Revocation) > sigchain.MaxRevocation || len(s.RevocationSig) > sigchain.MaxSignature:
		return errors.New("the set's revocation or its signature is too long")
	}

	return nil
}

func (a ChunkAnswer) validate() error {
	if a.Refusal != nil {
		if a.Version != "" || a.Arch != "" || a.Index != 0 || a.Chunk != nil || a.SHA256 != "" {
			return errors.New("a refused chunk answer carries a chunk")
		}

		return a.Refusal.validate()
	}

	if err := (ChunkRequest{Version: a.Version, Arch: a.Arch, Index: a.Index}).validate(); err != nil {
		return err
	}

	if len(a.Chunk) == 0 || len(a.Chunk) > MaxChunkSize || !digestForm.MatchString(a.SHA256) {
		return errors.New("the chunk answer's chunk or digest is not in its form")
	}

	sum := sha256.Sum256(a.Chunk)
	if hex.EncodeToString(sum[:]) != a.SHA256 {
		return errors.New("the chunk answer's digest does not match its chunk")
	}

	return nil
}

func (a StatusAnswer) validate() error {
	if a.Refusal != nil {
		return a.Refusal.validate()
	}

	return nil
}

type releaseMessage interface {
	ManifestRequest | ChunkRequest | StatusRequest | ManifestAnswer | ChunkAnswer | StatusAnswer
	validate() error
}

// EncodeRelease returns a release message's canonical bytes. It refuses
// a request with CodeReleaseRequestInvalid and an answer with
// CodeReleaseAnswerInvalid.
func EncodeRelease[T releaseMessage](m T) ([]byte, error) {
	code, limit := releaseBounds(m)

	if err := m.validate(); err != nil {
		return nil, refuse(code, err.Error())
	}

	b, err := canonicalJSON(m)
	if err != nil || len(b) > limit {
		return nil, refuse(code, "the message does not encode within its bound")
	}

	return b, nil
}

// DecodeRelease reads a release message, refusing any bytes but the
// canonical form of a valid message. The answering side reads requests
// and refuses them with CodeReleaseRequestInvalid; the asking side reads
// answers and refuses them with CodeReleaseAnswerInvalid.
func DecodeRelease[T releaseMessage](body []byte) (T, error) {
	var m T

	code, limit := releaseBounds(m)

	if len(body) == 0 || len(body) > limit {
		return m, refuse(code, fmt.Sprintf("the message is empty or longer than %d bytes", limit))
	}

	if err := decodeStrict(body, &m); err != nil {
		return m, refuse(code, "the message does not decode")
	}

	canon, err := EncodeRelease(m)
	if err != nil {
		return m, err
	}

	if !bytes.Equal(canon, body) {
		return m, refuse(code, "the message is not in its canonical form")
	}

	return m, nil
}

func releaseBounds(m any) (string, int) {
	switch m.(type) {
	case ManifestRequest, ChunkRequest, StatusRequest:
		return CodeReleaseRequestInvalid, MaxReleaseRequest
	default:
		return CodeReleaseAnswerInvalid, MaxReleaseAnswer
	}
}

// Answers reports whether a, a decoded chunk answer that carries a
// chunk, is the chunk r asked for.
func (a ChunkAnswer) Answers(r ChunkRequest) bool {
	return a.Refusal == nil && a.Version == r.Version && a.Arch == r.Arch && a.Index == r.Index
}

func utcSecond(t time.Time) bool {
	return !t.IsZero() && t.Location() == time.UTC && t.Equal(t.Truncate(time.Second))
}
