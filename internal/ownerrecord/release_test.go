package ownerrecord_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// sealRelease seals a release proof for r with the test key.
func sealRelease(t *testing.T, r ownerrecord.Record) ownerrecord.Signed {
	t.Helper()

	sf, err := ownerrecord.SealRelease(r, testSign)
	if err != nil {
		t.Fatal(err)
	}

	return sf
}

// releaseStore is a store in a new directory that holds the live record at
// generation 7, and the path of its record file.
func releaseStore(t *testing.T) (*ownerrecord.DirStore, string) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "owner")
	s := ownerrecord.NewDirStore(dir)

	if err := s.Replace(t.Context(), nil, liveAt(t, 7)); err != nil {
		t.Fatal(err)
	}

	return s, filepath.Join(dir, ownerrecord.RecordFile)
}

func wantRefusalCode(t *testing.T, err error, code progress.Code) {
	t.Helper()

	ref, ok := progress.AsRefusal(err)
	if !ok {
		t.Fatalf("err = %v, want a %s refusal", err, code)
	}

	if ref.Code != code {
		t.Fatalf("refusal = %v, want %s", ref, code)
	}
}

func TestReleaseSigningContext(t *testing.T) {
	t.Parallel()

	if ownerrecord.ReleaseSigningContext != "ocfp-owner-release-v1\n" {
		t.Fatalf("ReleaseSigningContext = %q", ownerrecord.ReleaseSigningContext)
	}

	if ownerrecord.ReleaseSigningContext == ownerrecord.SigningContext {
		t.Fatal("a release proof shares the record's signing context")
	}
}

// A release proof signs the record's fields under its own context, so it
// never verifies as a written record, and a written record never verifies
// as a release proof.
func TestSealReleaseUsesItsOwnContext(t *testing.T) {
	t.Parallel()

	proof := sealRelease(t, liveRecord())

	if proof.Format != ownerrecord.FormatV1 {
		t.Fatalf("Format = %d", proof.Format)
	}

	key := liveRecord().OwnerPublicKey
	msg := append([]byte(ownerrecord.ReleaseSigningContext), proof.Payload...)

	if !ed25519.Verify(key, msg, proof.Signature) {
		t.Fatal("the proof does not verify over ReleaseSigningContext and its payload")
	}

	_, err := ownerrecord.Open(proof)
	wantRefusalCode(t, err, progress.CodeRequestUnsigned)
}

func TestSealReleaseRefusesWhatCannotVerify(t *testing.T) {
	t.Parallel()

	if _, err := ownerrecord.SealRelease(liveRecord(), nil); err == nil {
		t.Error("SealRelease accepted no signer")
	}

	// The record names the test key as owner, but the signer uses another
	// key, so the proof could never verify.
	_, err := ownerrecord.SealRelease(liveRecord(), func(msg []byte) ([]byte, error) {
		return ed25519.Sign(targetKey, msg), nil
	})
	if err == nil {
		t.Error("SealRelease accepted a signature by a key the record does not name")
	}

	bad := liveRecord()
	bad.InstallationID = ""

	if _, err := ownerrecord.SealRelease(bad, testSign); err == nil {
		t.Error("SealRelease accepted a record with no installation id")
	}
}

func TestReleaseOnlyByOwner(t *testing.T) {
	t.Parallel()

	otherEpoch := liveRecord()
	otherEpoch.Generation = 7
	otherEpoch.Epoch = 4

	otherGeneration := liveRecord()
	otherGeneration.Generation = 8

	otherInstallation := liveRecord()
	otherInstallation.Generation = 7
	otherInstallation.InstallationID = "inst-fixture-02"

	writeForm := liveRecord()
	writeForm.Generation = 7

	// A later record can share the five fields above, so a proof also names
	// the head and the signing time of the one record it releases.
	otherHead := liveRecord()
	otherHead.Generation = 7
	otherHead.HeadHash = "sha256:" + strings.Repeat("cd", 32)

	otherTime := liveRecord()
	otherTime.Generation = 7
	otherTime.SignedAt = otherTime.SignedAt.Add(time.Hour)

	refused := []struct {
		name  string
		proof func(t *testing.T) ownerrecord.Signed
		code  progress.Code
	}{
		{"another key", func(t *testing.T) ownerrecord.Signed { return otherOwnerRelease(t, 7) }, progress.CodeOwnerMismatch},
		{"the write form", func(t *testing.T) ownerrecord.Signed { return seal(t, writeForm) }, progress.CodeRequestUnsigned},
		{"another epoch", func(t *testing.T) ownerrecord.Signed { return sealRelease(t, otherEpoch) }, progress.CodeOwnerMismatch},
		{"another generation", func(t *testing.T) ownerrecord.Signed { return sealRelease(t, otherGeneration) }, progress.CodeOwnerMismatch},
		{"another installation", func(t *testing.T) ownerrecord.Signed { return sealRelease(t, otherInstallation) }, progress.CodeOwnerMismatch},
		{"another head", func(t *testing.T) ownerrecord.Signed { return sealRelease(t, otherHead) }, progress.CodeOwnerMismatch},
		{"another signing time", func(t *testing.T) ownerrecord.Signed { return sealRelease(t, otherTime) }, progress.CodeOwnerMismatch},
	}

	s, path := releaseStore(t)

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range refused {
		err := s.Release(t.Context(), 7, tc.proof(t))
		wantRefusalCode(t, err, tc.code)

		after, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("%s: the record is gone after a refusal: %v", tc.name, rerr)
		}

		if !bytes.Equal(before, after) {
			t.Fatalf("%s: the record changed after a refusal", tc.name)
		}
	}

	owner := liveRecord()
	owner.Generation = 7

	if err := s.Release(t.Context(), 7, sealRelease(t, owner)); err != nil {
		t.Fatalf("the owner's release = %v", err)
	}

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the record is still there after the owner's release: %v", err)
	}

	if _, _, err := s.Read(t.Context()); !errors.Is(err, ownerrecord.ErrNoRecord) {
		t.Fatalf("Read after release = %v, want ErrNoRecord", err)
	}
}

// otherOwnerRelease is a release proof at generation gen for a record whose
// owner is the target instance, signed by the target's own key.
func otherOwnerRelease(t *testing.T, gen uint64) ownerrecord.Signed {
	t.Helper()

	r := liveRecord()
	r.OwnerInstanceID = "instance-fixture-b"
	r.SignerID = "instance-fixture-b"
	r.OwnerPublicKey = targetKey.Public().(ed25519.PublicKey)
	r.Generation = gen

	sf, err := ownerrecord.SealRelease(r, func(msg []byte) ([]byte, error) {
		return ed25519.Sign(targetKey, msg), nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return sf
}

func TestReleaseRefusesABrokenProof(t *testing.T) {
	t.Parallel()

	owner := liveRecord()
	owner.Generation = 7

	good := sealRelease(t, owner)

	flipped := good
	flipped.Signature = bytes.Clone(good.Signature)
	flipped.Signature[0] ^= 0x01

	short := good
	short.Signature = good.Signature[:10]

	format := good
	format.Format = 2

	cases := []struct {
		name  string
		proof ownerrecord.Signed
		code  progress.Code
	}{
		{"flipped signature", flipped, progress.CodeRequestUnsigned},
		{"short signature", short, progress.CodeRequestUnsigned},
		{"another format", format, progress.CodeJobRequestInvalid},
	}

	s, path := releaseStore(t)

	for _, tc := range cases {
		err := s.Release(t.Context(), 7, tc.proof)
		wantRefusalCode(t, err, tc.code)

		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s: the record is gone after a refusal: %v", tc.name, err)
		}
	}
}

func TestReleaseNeedsTheStoredGeneration(t *testing.T) {
	t.Parallel()

	s, path := releaseStore(t)

	owner := liveRecord()
	owner.Generation = 7

	err := s.Release(t.Context(), 6, sealRelease(t, owner))
	if !errors.Is(err, ownerrecord.ErrRecordChanged) {
		t.Fatalf("Release with a stale generation = %v, want ErrRecordChanged", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the record is gone after a stale release: %v", err)
	}
}

// A release on a missing directory reports no record and creates nothing,
// and a release on an empty directory reports no record.
func TestReleaseWithNoRecord(t *testing.T) {
	t.Parallel()

	owner := liveRecord()
	owner.Generation = 7
	proof := sealRelease(t, owner)

	missing := filepath.Join(t.TempDir(), "owner")

	err := ownerrecord.NewDirStore(missing).Release(t.Context(), 7, proof)
	if !errors.Is(err, ownerrecord.ErrNoRecord) {
		t.Fatalf("Release on a missing directory = %v, want ErrNoRecord", err)
	}

	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Release made the directory: %v", err)
	}

	empty := t.TempDir()

	err = ownerrecord.NewDirStore(empty).Release(t.Context(), 7, proof)
	if !errors.Is(err, ownerrecord.ErrNoRecord) {
		t.Fatalf("Release on an empty directory = %v, want ErrNoRecord", err)
	}
}

// A stored record that does not open cannot say whose key may release it,
// so nothing releases it.
func TestReleaseRefusesAnInvalidStoredRecord(t *testing.T) {
	t.Parallel()

	s, path := releaseStore(t)

	bad := liveAt(t, 7)
	bad.Signature = bytes.Clone(bad.Signature)
	bad.Signature[0] ^= 0x01

	if err := os.WriteFile(path, encode(t, bad), 0o600); err != nil {
		t.Fatal(err)
	}

	owner := liveRecord()
	owner.Generation = 7

	err := s.Release(t.Context(), 7, sealRelease(t, owner))
	if !errors.Is(err, ownerrecord.ErrStoredRecordInvalid) {
		t.Fatalf("Release of an invalid stored record = %v, want ErrStoredRecordInvalid", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the invalid record is gone: %v", err)
	}
}

func TestReleaseLeavesLockFile(t *testing.T) {
	t.Parallel()

	s, path := releaseStore(t)
	dir := filepath.Dir(path)

	owner := liveRecord()
	owner.Generation = 7

	if err := s.Release(t.Context(), 7, sealRelease(t, owner)); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}

	if !slices.Equal(names, []string{ownerrecord.LockFile}) {
		t.Fatalf("after release the directory holds %v, want only %s", names, ownerrecord.LockFile)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the directory survived its deletion: %v", err)
	}
}

func TestReleaseHonoursContext(t *testing.T) {
	t.Parallel()

	s, path := releaseStore(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	owner := liveRecord()
	owner.Generation = 7

	if err := s.Release(ctx, 7, sealRelease(t, owner)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Release with a done context = %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the record is gone after a cancelled release: %v", err)
	}
}
