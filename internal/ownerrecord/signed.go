package ownerrecord

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/strictjson"
)

// FormatV1 is the only signed owner record format this package reads or
// writes.
const FormatV1 = 1

// SigningContext is prefixed to the payload before it is signed. Every signed
// form has its own context, so a signature made for one form can never pass
// as a signature for another.
const SigningContext = "ocfp-owner-record-v1\n"

// Signed is the signed form of an owner record as it is stored and sent. The
// payload is the record's JSON exactly as it was signed, kept as bytes so that
// a reader verifies what the writer signed and never a re-encoding of it.
type Signed struct {
	Format    int    `json:"format"`
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

// SigningBytes returns the message that is signed for payload, which is
// SigningContext followed by payload. The result never shares memory with
// payload.
func SigningBytes(payload []byte) []byte {
	msg := make([]byte, 0, len(SigningContext)+len(payload))
	msg = append(msg, SigningContext...)

	return append(msg, payload...)
}

// Seal encodes r once and signs SigningBytes of the encoding with sign. The
// signer is a function so that a caller can pass its key's Sign method without
// this package holding the key. SignedAt is stored in UTC, so the same record
// always seals to the same payload.
//
// Seal is for a record that its owner signs. It refuses a record that Open
// would refuse, including one whose signer is not its owner and one whose
// signature does not verify against r.OwnerPublicKey.
func Seal(r Record, sign func(msg []byte) ([]byte, error)) (Signed, error) {
	return seal(r, sign, SigningBytes, Open)
}

// SealWithKey is Seal for a record that another instance signs, such as one
// that names a new owner and is signed by the instance handing ownership
// over. signerKey is the public key of the instance that r.SignerID names,
// and sign must sign with its private key. SealWithKey refuses a record that
// OpenWithKey would refuse with the same id and key.
func SealWithKey(r Record, signerKey ed25519.PublicKey, sign func(msg []byte) ([]byte, error)) (Signed, error) {
	return seal(r, sign, SigningBytes, func(sf Signed) (Record, error) {
		return OpenWithKey(sf, r.SignerID, signerKey)
	})
}

// seal encodes r and signs signingBytes of the encoding, and refuses a signed
// form that open does not accept.
func seal(
	r Record,
	sign func(msg []byte) ([]byte, error),
	signingBytes func(payload []byte) []byte,
	open func(Signed) (Record, error),
) (Signed, error) {
	if sign == nil {
		return Signed{}, errors.New("seal owner record: no signer")
	}

	r.SignedAt = r.SignedAt.UTC()

	if err := r.validate(); err != nil {
		return Signed{}, fmt.Errorf("seal owner record: %w", err)
	}

	payload, err := json.Marshal(r)
	if err != nil {
		return Signed{}, fmt.Errorf("seal owner record: encode payload: %w", err)
	}

	sig, err := sign(signingBytes(payload))
	if err != nil {
		return Signed{}, fmt.Errorf("seal owner record: sign: %w", err)
	}

	sf := Signed{Format: FormatV1, Payload: payload, Signature: bytes.Clone(sig)}

	if _, err := open(sf); err != nil {
		return Signed{}, fmt.Errorf("seal owner record: the signed form does not open: %w", err)
	}

	return sf, nil
}

// Open verifies sf against the owner public key that its own payload carries
// and returns the record. Because that key is the owner's, the only signer
// Open can prove is the owner, so it also refuses a record whose SignerID is
// not its OwnerInstanceID.
//
// Open refuses, with a *progress.Refusal, a format other than FormatV1
// (JOB_REQUEST_INVALID), a signature of the wrong length (REQUEST_UNSIGNED), a
// payload that is not valid UTF-8, that is not exactly one JSON object, or
// that repeats a key or spells a field's name in another case
// (JOB_REQUEST_INVALID), an owner public key
// that is not a canonical Ed25519 key of large order (JOB_REQUEST_INVALID), a
// signature that does not verify over SigningBytes(sf.Payload) with that key
// (REQUEST_UNSIGNED), a record whose fields are not valid (JOB_REQUEST_INVALID),
// and a signer that is not the owner (REQUEST_UNSIGNED).
//
// Open decodes the payload once and uses only its owner public key to check
// the signature, and no other field is used until the signature has verified.
// Open never re-encodes the payload, so fields added by a later writer are
// covered by the signature and then ignored.
func Open(sf Signed) (Record, error) {
	return open(sf, nil, "")
}

// OpenWithKey verifies sf against signerKey, which the caller already trusts
// as the key of the instance signerID, and returns the record. It never
// verifies against a key from the payload, and it refuses a record whose
// SignerID is not signerID. A signerKey that is not a canonical Ed25519 key of
// large order is refused with REQUEST_UNSIGNED. Every other check is the same
// as in Open, including the check of the payload's owner public key.
func OpenWithKey(sf Signed, signerID string, signerKey ed25519.PublicKey) (Record, error) {
	if err := checkPublicKey(signerKey); err != nil {
		return Record{}, unsigned("signer key: %v", err)
	}

	if signerID == "" {
		return Record{}, unsigned("no signer id to check the record against")
	}

	return open(sf, signerKey, signerID)
}

// open verifies sf with key and binds the record's signer to signerID. A nil
// key means the payload's own owner key, which binds the signer to the owner.
func open(sf Signed, key ed25519.PublicKey, signerID string) (Record, error) {
	return openWith(sf, key, signerID, SigningBytes)
}

// openWith is open for a signed form whose signature covers
// signingBytes(sf.Payload).
func openWith(sf Signed, key ed25519.PublicKey, signerID string, signingBytes func(payload []byte) []byte) (Record, error) {
	if sf.Format != FormatV1 {
		return Record{}, invalid("unsupported owner record format %d, want %d", sf.Format, FormatV1)
	}

	if len(sf.Signature) != ed25519.SignatureSize {
		return Record{}, unsigned("owner record signature is %d bytes, want %d", len(sf.Signature), ed25519.SignatureSize)
	}

	var r Record
	if err := decodePayload(sf.Payload, &r); err != nil {
		return Record{}, invalid("owner record payload: %v", err)
	}

	if err := checkPublicKey(r.OwnerPublicKey); err != nil {
		return Record{}, invalid("owner %v", err)
	}

	if key == nil {
		key = ed25519.PublicKey(r.OwnerPublicKey)
	}

	if !ed25519.Verify(key, signingBytes(sf.Payload), sf.Signature) {
		return Record{}, unsigned("owner record signature does not verify")
	}

	if err := r.validate(); err != nil {
		return Record{}, invalid("owner record: %v", err)
	}

	if signerID == "" {
		signerID = r.OwnerInstanceID
	}

	if r.SignerID != signerID {
		return Record{}, unsigned("owner record names signer %q, but the signature proves %q", r.SignerID, signerID)
	}

	return r, nil
}

// HandoverRefusal chooses the refusal for a record that failed Open. When
// openErr is a REQUEST_UNSIGNED refusal and sf's payload decodes cleanly into
// a valid record in ModeHandover, it returns an OWNER_HANDOVER refusal in its
// place. A record that its new owner did not sign, such as one written by the
// instance handing ownership over, fails Open, and a job runner must still
// answer it as a hand-over. In every other case it returns openErr unchanged.
//
// HandoverRefusal only ever chooses which refusal to return. The mode it reads
// is unverified, so it never returns a record, never turns an error into
// success, and returns nil only when openErr is nil. Never use it to decide
// that a record may be trusted.
func HandoverRefusal(sf Signed, openErr error) error {
	if openErr == nil {
		return nil
	}

	ref, ok := progress.AsRefusal(openErr)
	if !ok || ref == nil || ref.Code != progress.CodeRequestUnsigned || sf.Format != FormatV1 {
		return openErr
	}

	var r Record
	if decodePayload(sf.Payload, &r) != nil || r.validate() != nil || r.Mode != ModeHandover {
		return openErr
	}

	return &progress.Refusal{Code: progress.CodeOwnerHandover, Message: "owner record is in handover"}
}

// decodePayload decodes a record payload, refusing anything that two JSON
// readers could read differently. Fields that a newer writer added are
// decoded past, because the signature covers them and nothing here reads
// them.
func decodePayload(payload []byte, r *Record) error {
	_, err := strictjson.DecodeWithUnknown(payload, r)

	return err
}

// Parse decodes the stored or sent form of a signed owner record. It refuses,
// with a *progress.Refusal, anything but exactly one JSON object with a
// format, any field it does not know, and anything else strictjson.Decode
// refuses. Parse does not verify the record; call Open for that.
func Parse(data []byte) (Signed, error) {
	var doc struct {
		Format    *int   `json:"format"`
		Payload   []byte `json:"payload"`
		Signature []byte `json:"signature"`
	}

	if err := strictjson.Decode(data, &doc); err != nil {
		return Signed{}, invalid("signed owner record: %v", err)
	}

	if doc.Format == nil {
		return Signed{}, invalid("signed owner record has no format")
	}

	return Signed{Format: *doc.Format, Payload: doc.Payload, Signature: doc.Signature}, nil
}

func (r Record) validate() error {
	switch {
	case r.InstallationID == "":
		return errors.New("no installation id")
	case r.OwnerInstanceID == "":
		return errors.New("no owner instance id")
	case r.Mode != ModeLive && r.Mode != ModeHandover:
		return fmt.Errorf("unknown mode %q", r.Mode)
	case r.Mode == ModeHandover && r.Relocation == nil:
		return errors.New("handover without a relocation")
	case r.HeadPosition < 0:
		return fmt.Errorf("negative head position %d", r.HeadPosition)
	case r.SignerID == "":
		return errors.New("no signer id")
	case r.SignedAt.IsZero():
		return errors.New("no signing time")
	}

	if err := checkPublicKey(r.OwnerPublicKey); err != nil {
		return fmt.Errorf("owner %w", err)
	}

	// A payload that Open reads has passed the UTF-8 check already. These
	// checks are for Seal, whose record comes from Go strings that may hold
	// any bytes, and which json.Marshal would encode as U+FFFD.
	for name, v := range map[string]string{
		"installation id":   r.InstallationID,
		"owner instance id": r.OwnerInstanceID,
		"mode":              string(r.Mode),
		"head hash":         r.HeadHash,
		"signer id":         r.SignerID,
	} {
		if !utf8.ValidString(v) {
			return fmt.Errorf("%s is not valid UTF-8", name)
		}
	}

	if r.Relocation != nil {
		return r.Relocation.validate()
	}

	return nil
}

func (rl Relocation) validate() error {
	switch {
	case rl.ID == "":
		return errors.New("relocation has no id")
	case !utf8.ValidString(rl.ID):
		return errors.New("relocation id is not valid UTF-8")
	case !utf8.ValidString(rl.TargetInstanceID):
		return errors.New("relocation target instance id is not valid UTF-8")
	case !utf8.ValidString(rl.CutOverHash):
		return errors.New("cut-over hash is not valid UTF-8")
	case (rl.CutOverPosition == nil) != (rl.CutOverHash == ""):
		return errors.New("cut-over position and hash must be set together")
	case rl.CutOverPosition != nil && *rl.CutOverPosition < 0:
		return fmt.Errorf("negative cut-over position %d", *rl.CutOverPosition)
	}

	return nil
}

func invalid(format string, args ...any) error {
	return &progress.Refusal{Code: progress.CodeJobRequestInvalid, Message: fmt.Sprintf(format, args...)}
}

func unsigned(format string, args ...any) error {
	return &progress.Refusal{Code: progress.CodeRequestUnsigned, Message: fmt.Sprintf(format, args...)}
}
