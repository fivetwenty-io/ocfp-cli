package jobexec

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// The window around the bastion's clock in which a request's issue time must
// fall.
const (
	issuedBefore = 10 * time.Minute
	issuedAfter  = time.Minute
)

// jobIDPattern is the form of a job id. A job id names a spool directory, so
// it takes the form of every name that may reach a path on the host.
var jobIDPattern = ownerrecord.NamePattern

// CheckOwner verifies sr against the owner record in store and returns the
// request with the release of the shared lock on the record. The lock stays
// held until the release is called, so the record cannot be replaced while
// the job acts on it. The release may be called more than once, and only the
// first call acts.
//
// CheckOwner takes the shared lock, reads the record, and refuses, with a
// *progress.Refusal and in this order:
//
//   - a missing record (OWNER_RECORD_MISSING), or a stored record in
//     handover that does not open because the incoming owner did not sign it
//     (OWNER_HANDOVER);
//   - a signature that is not 64 bytes or that does not verify with the
//     record's owner public key over RequestSigningBytes(sr.Payload)
//     (REQUEST_UNSIGNED);
//   - a payload that is not exactly one JSON object of valid UTF-8, or that
//     repeats a key, or spells a field's name in another case
//     (JOB_REQUEST_INVALID);
//   - an installation, instance, or epoch other than the record's
//     (OWNER_MISMATCH);
//   - a record in handover or any mode but live (OWNER_HANDOVER);
//   - a wire contract other than progress.WireContract
//     (WIRE_CONTRACT_UNSUPPORTED);
//   - a key that names no field, an issue time outside the window from ten
//     minutes before the bastion's clock to one minute after it, a job id that
//     does not match ^[a-z0-9][a-z0-9-]{0,63}$, and a format other than
//     RequestFormatV1 (JOB_REQUEST_INVALID).
//
// A stored record that does not open for any other reason is refused with
// OWNER_RECORD_INVALID, and the error also matches
// ownerrecord.ErrStoredRecordInvalid. Its message carries the reason the
// record did not open, but only as text, so the record's own refusal code
// never reaches a caller through progress.AsRefusal and is never read as a
// fault in the request. Nothing in the payload is read until the signature
// has verified. On every refusal and
// every error, CheckOwner releases the lock before it returns.
func CheckOwner(ctx context.Context, sr SignedRequest, store ownerrecord.Store) (Request, func() error, error) {
	return checkOwner(ctx, sr, store, progress.SystemClock())
}

// checkOwner is CheckOwner with the clock that the issue time is checked
// against.
func checkOwner(ctx context.Context, sr SignedRequest, store ownerrecord.Store, clock progress.Clock) (Request, func() error, error) {
	if store == nil || clock == nil {
		return Request{}, nil, errors.New("check owner: no owner record store or clock")
	}

	unlock, err := store.RLock(ctx)
	if errors.Is(err, ownerrecord.ErrNoRecord) {
		return Request{}, nil, refuse(progress.CodeOwnerRecordMissing, "no owner record on this host")
	}

	if err != nil {
		return Request{}, nil, fmt.Errorf("check owner: %w", err)
	}

	release := releaseOnce(unlock)
	kept := false

	// The release is deferred as well, so that a panic recovered further up
	// cannot leave the lock held. It acts only once.
	defer func() {
		if !kept {
			_ = release()
		}
	}()

	req, err := verify(ctx, sr, store, clock)
	if err != nil {
		rerr := release()
		if rerr != nil {
			err = errors.Join(err, fmt.Errorf("release owner record lock: %w", rerr))
		}

		return Request{}, nil, err
	}

	kept = true

	return req, release, nil
}

// releaseOnce wraps a store's release so that only its first call acts and
// every call returns the first call's error.
func releaseOnce(unlock func() error) func() error {
	var (
		once sync.Once
		err  error
	)

	return func() error {
		once.Do(func() { err = unlock() })

		return err
	}
}

// verify runs the checks while the caller holds the shared lock.
func verify(ctx context.Context, sr SignedRequest, store ownerrecord.Store, clock progress.Clock) (Request, error) {
	rec, err := readRecord(ctx, store)
	if err != nil {
		return Request{}, err
	}

	if len(sr.Signature) != ed25519.SignatureSize {
		return Request{}, refuse(progress.CodeRequestUnsigned,
			fmt.Sprintf("job request signature is %d bytes, want %d", len(sr.Signature), ed25519.SignatureSize))
	}

	// Open has checked that the record's owner key is a canonical key of
	// large order, so a signature under it proves the owner's private key.
	if !ed25519.Verify(ed25519.PublicKey(rec.OwnerPublicKey), RequestSigningBytes(sr.Payload), sr.Signature) {
		return Request{}, refuse(progress.CodeRequestUnsigned, "job request signature does not verify with the owner key")
	}

	req, unknown, err := decodeRequest(sr.Payload)
	if err != nil {
		return Request{}, invalid("job request payload: " + err.Error())
	}

	err = checkOwnerFields(req, rec)
	if err != nil {
		return Request{}, err
	}

	err = checkRequest(req, unknown, sr.Format, clock.Now())
	if err != nil {
		return Request{}, err
	}

	return req, nil
}

// readRecord reads the owner record. A record that does not open is answered
// as a hand-over when it is one, and otherwise with OWNER_RECORD_INVALID.
func readRecord(ctx context.Context, store ownerrecord.Store) (ownerrecord.Record, error) {
	sf, rec, err := store.Read(ctx)
	if errors.Is(err, ownerrecord.ErrNoRecord) {
		return ownerrecord.Record{}, refuse(progress.CodeOwnerRecordMissing, "no owner record on this host")
	}

	if errors.Is(err, ownerrecord.ErrStoredRecordInvalid) {
		herr := ownerrecord.HandoverRefusal(sf, err)
		if ref, ok := progress.AsRefusal(herr); ok && ref.Code == progress.CodeOwnerHandover {
			return ownerrecord.Record{}, herr
		}

		return ownerrecord.Record{}, &storedRecordError{ref: &progress.Refusal{
			Code:    progress.CodeOwnerRecordInvalid,
			Message: "owner record on this host does not open: " + err.Error(),
		}}
	}

	if err != nil {
		return ownerrecord.Record{}, fmt.Errorf("check owner: %w", err)
	}

	return rec, nil
}

// storedRecordError reports a stored owner record that does not open. Its
// chain holds only its own OWNER_RECORD_INVALID refusal and
// ownerrecord.ErrStoredRecordInvalid. The store's error, and the refusal
// inside it, survive only as text in the message.
type storedRecordError struct {
	ref *progress.Refusal
}

func (e *storedRecordError) Error() string { return e.ref.Error() }

func (e *storedRecordError) Unwrap() []error {
	return []error{e.ref, ownerrecord.ErrStoredRecordInvalid}
}

// checkOwnerFields compares the request's requester with the record, and then
// refuses a record in handover.
func checkOwnerFields(req Request, rec ownerrecord.Record) error {
	switch {
	case req.InstallationID != rec.InstallationID:
		return refuse(progress.CodeOwnerMismatch, fmt.Sprintf("job request names installation %s, the owner record names %q",
			quote(req.InstallationID), rec.InstallationID))
	case req.InstanceID != rec.OwnerInstanceID:
		return refuse(progress.CodeOwnerMismatch, fmt.Sprintf("job request names instance %s, the owner record names %q",
			quote(req.InstanceID), rec.OwnerInstanceID))
	case req.Epoch != rec.Epoch:
		return refuse(progress.CodeOwnerMismatch, fmt.Sprintf("job request names epoch %d, the owner record names %d",
			req.Epoch, rec.Epoch))
	case rec.Mode != ownerrecord.ModeLive:
		// Open allows only live and handover, and anything but live is
		// refused as a hand-over, so a store that broke its contract still
		// fails closed.
		return refuse(progress.CodeOwnerHandover, fmt.Sprintf("owner record is in mode %q, not live", rec.Mode))
	}

	return nil
}

// checkRequest checks the request's own fields once its requester is known to
// be the owner.
func checkRequest(req Request, unknown []string, format int, now time.Time) error {
	if req.WireContract != progress.WireContract {
		return refuse(progress.CodeWireContractUnsupported, fmt.Sprintf("job request wire contract is %d, this CLI reads %d",
			req.WireContract, progress.WireContract))
	}

	if len(unknown) > 0 {
		return invalid("job request holds the unknown field " + quote(unknown[0]))
	}

	if req.IssuedAt.Before(now.Add(-issuedBefore)) || req.IssuedAt.After(now.Add(issuedAfter)) {
		return invalid(fmt.Sprintf("job request was issued at %s, outside %s before to %s after this host's clock at %s",
			req.IssuedAt.UTC().Format(time.RFC3339Nano), issuedBefore, issuedAfter, now.UTC().Format(time.RFC3339Nano)))
	}

	if !jobIDPattern.MatchString(req.JobID) {
		return invalid("job request job id " + quote(req.JobID) + " is not of the form " + jobIDPattern.String())
	}

	if format != RequestFormatV1 {
		return invalid(fmt.Sprintf("unsupported job request format %d, want %d", format, RequestFormatV1))
	}

	return nil
}

func refuse(code progress.Code, msg string) error {
	return &progress.Refusal{Code: code, Message: msg}
}

func invalid(msg string) error {
	return refuse(progress.CodeJobRequestInvalid, msg)
}
