package ownerrecord

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// ReleaseSigningContext is prefixed to the payload of a release proof before
// it is signed. It differs from SigningContext, so a written record never
// passes as a release proof and a release proof never passes as a record.
const ReleaseSigningContext = "ocfp-owner-release-v1\n"

// ReleaseSigningBytes returns the message that is signed for a release proof
// whose payload is payload, which is ReleaseSigningContext followed by
// payload. The result never shares memory with payload.
func ReleaseSigningBytes(payload []byte) []byte {
	msg := make([]byte, 0, len(ReleaseSigningContext)+len(payload))
	msg = append(msg, ReleaseSigningContext...)

	return append(msg, payload...)
}

// SealRelease seals r as a release proof: the owner's signed statement that
// it gives up the record r describes. It signs ReleaseSigningBytes of the
// encoding with sign, and refuses a record that is not valid or whose owner
// is not its signer, and a signature that does not verify against
// r.OwnerPublicKey.
func SealRelease(r Record, sign func(msg []byte) ([]byte, error)) (Signed, error) {
	return seal(r, sign, ReleaseSigningBytes, openRelease)
}

// openRelease verifies a release proof against the owner key in its own
// payload.
func openRelease(sf Signed) (Record, error) {
	return openWith(sf, nil, "", ReleaseSigningBytes)
}

// Release removes the stored record when proof is its owner's release proof
// for it. expect must be the stored record's generation. The proof must be
// signed over ReleaseSigningContext by the stored record's owner key, and it
// must name the stored record's installation, owner, owner key, epoch,
// generation, head, and signing time.
//
// Release returns ErrNoRecord when there is no record, an error wrapping
// ErrStoredRecordInvalid when the stored record does not open, and an error
// wrapping ErrRecordChanged when the stored generation is not expect. It
// refuses, with a *progress.Refusal, a proof that does not verify against the
// stored owner key (REQUEST_UNSIGNED), a proof that another key signed or
// that names another record (OWNER_MISMATCH), and a proof that is not a valid
// signed form (JOB_REQUEST_INVALID). A refused release leaves the record as
// it was. A release keeps the lock file, so the directory can be removed
// afterwards.
func (s *DirStore) Release(ctx context.Context, expect uint64, proof Signed) (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("release owner record: %w", err)
		}
	}()

	if err := ctx.Err(); err != nil {
		return err
	}

	f, err := s.lock(ctx, lockExclusive)
	if err != nil {
		return err
	}

	defer func() {
		if rerr := release(f); rerr != nil && err == nil {
			err = rerr
		}
	}()

	s.sweepTemps()

	cur, err := s.readSigned()
	if err != nil {
		return err
	}

	stored, err := Open(cur)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStoredRecordInvalid, err)
	}

	if stored.Generation != expect {
		return fmt.Errorf("stored generation is %d, expected %d: %w", stored.Generation, expect, ErrRecordChanged)
	}

	if err := checkRelease(stored, proof); err != nil {
		return err
	}

	path := filepath.Join(s.dir, RecordFile)

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove owner record: %w", err)
	}

	return s.syncDirectory(s.dir)
}

// checkRelease checks that proof is stored's owner's release proof for
// stored. The signing time pins the proof to one record, so a proof for a
// released record never releases a later one with the same other fields.
func checkRelease(stored Record, proof Signed) error {
	pr, err := openWith(proof, stored.OwnerPublicKey, "", ReleaseSigningBytes)
	if err != nil {
		if ref, ok := progress.AsRefusal(err); ok && ref.Code == progress.CodeRequestUnsigned {
			// A proof that verifies against the key it names, which is not
			// the stored owner's, was made by someone who does not own the
			// record.
			if _, oerr := openRelease(proof); oerr == nil {
				return mismatch("the release proof is not signed by the stored record's owner key")
			}
		}

		return err
	}

	switch {
	case pr.InstallationID != stored.InstallationID:
		return mismatch("the release proof names another installation")
	case pr.OwnerInstanceID != stored.OwnerInstanceID:
		return mismatch("the release proof names another owner")
	case !bytes.Equal(pr.OwnerPublicKey, stored.OwnerPublicKey):
		return mismatch("the release proof names another owner key")
	case pr.Epoch != stored.Epoch:
		return mismatch(fmt.Sprintf("the release proof names epoch %d, the stored record has %d", pr.Epoch, stored.Epoch))
	case pr.Generation != stored.Generation:
		return mismatch(fmt.Sprintf("the release proof names generation %d, the stored record has %d", pr.Generation, stored.Generation))
	case pr.HeadPosition != stored.HeadPosition || pr.HeadHash != stored.HeadHash:
		return mismatch("the release proof names another head")
	case !pr.SignedAt.Equal(stored.SignedAt):
		return mismatch("the release proof names another signing time")
	}

	return nil
}

func mismatch(msg string) error {
	return &progress.Refusal{Code: progress.CodeOwnerMismatch, Message: msg}
}
