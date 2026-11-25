package enroll

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/fivetwenty-io/ocfp-cli/sigchain"
)

// The domain an attestation names and the SSHSIG namespace the host key
// signs it under.
const (
	AttestationDomain = "ocfp-bastion-join-attestation-v1"
	JoinNamespace     = "ocfp-bastion-join-v1"
)

// The bounds of an admission message. MaxMessage bounds a request or an
// answer, MaxAttestation and MaxRequestSignature the two parts of a
// request, and MaxCredential the longest credential an answer carries.
const (
	MaxMessage          = 8 << 10
	MaxAttestation      = 2 << 10
	MaxRequestSignature = 2 << 10
	MaxCredential       = 4 << 10
)

// keyType is the form of an SSH key type name.
var keyType = regexp.MustCompile(`^[a-z0-9][a-z0-9@.-]{0,63}$`)

// Attestation is what the bootstrap signs with the bastion's host key.
// It names two user nkeys that prepare generates together: WorkerKey is
// the identity a person enrolls, whose seed only the installed worker
// holds, and InstallKey belongs to the enrollment service, which can
// only ever get a credential that reads the release. The two must
// differ. HostKey is the host's public key as its type and its base64
// blob, without a comment. CreatedAt is a UTC time in whole seconds.
type Attestation struct {
	Domain       string    `json:"domain"`
	Installation string    `json:"installation"`
	Org          string    `json:"org"`
	Bloc         string    `json:"bloc"`
	WorkerKey    string    `json:"workerKey"`
	InstallKey   string    `json:"installKey"`
	HostKey      string    `json:"hostKey"`
	CreatedAt    time.Time `json:"createdAt"`
}

// NewAttestation returns the attestation for a pair of keys, with
// hostKey as a .pub file or an authorized_keys line gives it and at as
// the time it was made. It drops the key line's comment and keeps at as
// the UTC second, which is the attestation's canonical form.
func NewAttestation(installation, org, bloc, workerKey, installKey, hostKey string, at time.Time) (Attestation, error) {
	fields := strings.Fields(hostKey)
	if len(fields) < 2 {
		return Attestation{}, refuse(CodeRequestInvalid, "the host key is not a key line")
	}

	a := Attestation{
		Domain:       AttestationDomain,
		Installation: installation,
		Org:          org,
		Bloc:         bloc,
		WorkerKey:    workerKey,
		InstallKey:   installKey,
		HostKey:      fields[0] + " " + fields[1],
		CreatedAt:    at.UTC().Truncate(time.Second),
	}

	if err := a.Validate(); err != nil {
		return Attestation{}, err
	}

	return a, nil
}

// Validate holds an attestation to its form, and refuses with
// CodeRequestInvalid, including when both keys are one key.
func (a Attestation) Validate() error {
	bad := func(what string) error { return refuse(CodeRequestInvalid, "the attestation's "+what) }

	switch {
	case a.Domain != AttestationDomain:
		return bad("domain is not " + AttestationDomain)
	case !ValidInstallation(a.Installation):
		return bad("installation is not in its form")
	case !ValidOrg(a.Org):
		return bad("org is not in its form")
	case !ValidBloc(a.Bloc):
		return bad("bloc is not in its form")
	case !ValidUserKey(a.WorkerKey):
		return bad("worker key is not a user nkey")
	case !ValidUserKey(a.InstallKey):
		return bad("install key is not a user nkey")
	case a.WorkerKey == a.InstallKey:
		return bad("worker key and install key are one key")
	case !validHostKey(a.HostKey):
		return bad("host key is not a type and a base64 blob")
	case a.CreatedAt.IsZero() || a.CreatedAt.Location() != time.UTC || !a.CreatedAt.Equal(a.CreatedAt.Truncate(time.Second)):
		return bad("time is not a UTC second")
	}

	return nil
}

func validHostKey(key string) bool {
	typ, blob, ok := strings.Cut(key, " ")
	if !ok || !keyType.MatchString(typ) || blob == "" {
		return false
	}

	_, err := base64.StdEncoding.Strict().DecodeString(blob)

	return err == nil
}

// Canonical returns the attestation's canonical bytes, the ones the
// bootstrap signs and the request carries: compact JSON with the fields
// in the order of the type, and no trailing newline.
func (a Attestation) Canonical() ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}

	b, err := canonicalJSON(a)
	if err != nil {
		return nil, refuse(CodeRequestInvalid, "the attestation does not encode")
	}

	return b, nil
}

// ParseAttestation reads an attestation's bytes. It refuses bytes longer
// than MaxAttestation, an unknown field, trailing data, an attestation
// that does not hold to its form, and any bytes but the canonical ones,
// so that one attestation has one request id.
func ParseAttestation(b []byte) (Attestation, error) {
	if len(b) == 0 || len(b) > MaxAttestation {
		return Attestation{}, refuse(CodeRequestInvalid, fmt.Sprintf("the attestation is empty or longer than %d bytes", MaxAttestation))
	}

	var a Attestation
	if err := decodeStrict(b, &a); err != nil {
		return Attestation{}, refuse(CodeRequestInvalid, "the attestation does not decode")
	}

	canon, err := a.Canonical()
	if err != nil {
		return Attestation{}, err
	}

	if !bytes.Equal(canon, b) {
		return Attestation{}, refuse(CodeRequestInvalid, "the attestation is not in its canonical form")
	}

	return a, nil
}

// SignAttestation returns the armored SSHSIG of the attestation's
// canonical bytes, made with an Ed25519 host key under JoinNamespace.
// It writes what ssh-keygen -Y sign -n ocfp-bastion-join-v1 writes.
func SignAttestation(hostKey ed25519.PrivateKey, a Attestation) (Request, error) {
	b, err := a.Canonical()
	if err != nil {
		return Request{}, err
	}

	sig, err := sigchain.Sign(hostKey, JoinNamespace, b)
	if err != nil {
		return Request{}, refuse(CodeRequestInvalid, "the attestation does not sign")
	}

	return Request{Attestation: b, Signature: string(sig)}, nil
}

// RequestID returns the id of the request that carries attestation, the
// lower-case hex of the SHA-256 of its bytes.
func RequestID(attestation []byte) string {
	d := sha256.Sum256(attestation)

	return hex.EncodeToString(d[:])
}

// Request is a worker's request for admission. Attestation is the
// attestation's canonical bytes, which JSON carries as base64, and
// Signature the armored SSHSIG over them. The worker key and the
// install key send the same Request, so both share one request id.
type Request struct {
	Attestation []byte `json:"attestation"`
	Signature   string `json:"signature"`
}

// ID returns the request's id.
func (r Request) ID() string { return RequestID(r.Attestation) }

// Encode returns the request's bytes as the bus carries them, and
// refuses a request DecodeRequest would refuse.
func (r Request) Encode() ([]byte, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, refuse(CodeRequestInvalid, "the request does not encode")
	}

	if _, _, err := DecodeRequest(b); err != nil {
		return nil, err
	}

	return b, nil
}

// DecodeEnvelope is the second step of the check order. It checks the
// size, decodes the request's envelope, and hashes its attestation field
// to get the request id, without reading the attestation itself. From
// this step on, every refusal names the id DecodeEnvelope returns, so
// the asker can bind the refusal to its own request. It refuses with
// CodeRequestInvalid and an empty id a body longer than MaxMessage, one
// that does not decode, one with an unknown field or trailing data, and
// one whose attestation is empty, which names no request.
func DecodeEnvelope(body []byte) (Request, string, error) {
	if len(body) > MaxMessage {
		return Request{}, "", refuse(CodeRequestInvalid, fmt.Sprintf("the request is longer than %d bytes", MaxMessage))
	}

	var r Request
	if err := decodeStrict(body, &r); err != nil {
		return Request{}, "", refuse(CodeRequestInvalid, "the request does not decode")
	}

	if len(r.Attestation) == 0 {
		return Request{}, "", refuse(CodeRequestInvalid, "the request carries no attestation")
	}

	return r, r.ID(), nil
}

// DecodeRequest reads one request and returns it with its parsed
// attestation. Beyond DecodeEnvelope, it refuses a signature that is
// empty or longer than MaxRequestSignature and an attestation that
// ParseAttestation refuses. It checks no signature, which comes later
// in the check order.
func DecodeRequest(body []byte) (Request, Attestation, error) {
	r, _, err := DecodeEnvelope(body)
	if err != nil {
		return Request{}, Attestation{}, err
	}

	if r.Signature == "" || len(r.Signature) > MaxRequestSignature {
		return Request{}, Attestation{}, refuse(CodeRequestInvalid, fmt.Sprintf("the signature is empty or longer than %d bytes", MaxRequestSignature))
	}

	a, err := ParseAttestation(r.Attestation)
	if err != nil {
		return Request{}, Attestation{}, err
	}

	return r, a, nil
}

// VerifyRequest checks the request's signature against the host key its
// attestation names, under JoinNamespace. It refuses a host key of a
// type other than ssh-ed25519 with CodeHostKeyUnsupported and a
// signature that does not verify with CodeAttestationInvalid.
func VerifyRequest(r Request, a Attestation) error {
	typ, _, _ := strings.Cut(a.HostKey, " ")
	if typ != ssh.KeyAlgoED25519 {
		return refuse(CodeHostKeyUnsupported, "the host key is not an ssh-ed25519 key")
	}

	pub, err := sigchain.ParsePublicKey(a.HostKey)
	if err != nil {
		return refuse(CodeAttestationInvalid, "the host key does not parse")
	}

	if err := sigchain.VerifySignature(pub, JoinNamespace, r.Attestation, []byte(r.Signature)); err != nil {
		return refuse(CodeAttestationInvalid, "the host key's signature does not verify")
	}

	return nil
}

// Asker names which of the attestation's two keys sent a request.
type Asker int

// The two keys that may ask.
const (
	AskerWorker Asker = iota + 1
	AskerInstall
)

// String names the asker.
func (k Asker) String() string {
	switch k {
	case AskerWorker:
		return "worker"
	case AskerInstall:
		return "install"
	default:
		return "none"
	}
}

// Bind checks the four equalities that bind a request to one
// installation, one org, one bloc, and one pair of keys, and returns
// which key asked. subjectBloc and subjectKey come from the request's
// subject, and org and installation are those of the service that
// received it. The subject's key must be the attestation's worker key
// or its install key, else CodeAttestationInvalid. The org and the
// installation must match, else CodeWrongInstallation, and the blocs
// must match, else CodeBlocMismatch.
func Bind(a Attestation, subjectBloc, subjectKey, org, installation string) (Asker, error) {
	var asker Asker

	switch subjectKey {
	case a.WorkerKey:
		asker = AskerWorker
	case a.InstallKey:
		asker = AskerInstall
	default:
		return 0, refuse(CodeAttestationInvalid, "the subject's key is neither of the attestation's keys")
	}

	switch {
	case a.Org != org:
		return 0, refuse(CodeWrongInstallation, "the attestation names another org")
	case a.Installation != installation:
		return 0, refuse(CodeWrongInstallation, "the attestation names another installation")
	case a.Bloc != subjectBloc:
		return 0, refuse(CodeBlocMismatch, "the subject and the attestation name different blocs")
	}

	return asker, nil
}

// decodeStrict decodes one JSON value from b into v, refusing an unknown
// field and anything after the value.
func decodeStrict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		return err
	}

	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("enroll: data after the value")
	}

	return nil
}
