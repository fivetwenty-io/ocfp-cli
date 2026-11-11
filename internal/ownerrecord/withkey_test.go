package ownerrecord_test

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord/ownerrecordtest"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// The source is the test key's instance, and the target has a key of its own.
var targetKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x5a}, ed25519.SeedSize))

func sourceKey() ed25519.PublicKey {
	return ownerrecordtest.TestKey().Public().(ed25519.PublicKey)
}

// handoverToTarget is a record that names the target as owner in handover and
// is written and signed by the source.
func handoverToTarget() ownerrecord.Record {
	r := liveRecord()
	r.OwnerInstanceID = "instance-fixture-b"
	r.OwnerPublicKey = targetKey.Public().(ed25519.PublicKey)
	r.Epoch = 4
	r.Mode = ownerrecord.ModeHandover
	r.Generation = 9
	r.SignerID = "instance-fixture-a"
	r.Relocation = &ownerrecord.Relocation{
		ID:               "reloc-fixture-01",
		TargetInstanceID: "instance-fixture-b",
		CutOverPosition:  ptr(int64(1040)),
		CutOverHash:      "sha256:" + strings.Repeat("cd", 32),
	}

	return r
}

func sealHandover(t *testing.T) ownerrecord.Signed {
	t.Helper()

	sf, err := ownerrecord.SealWithKey(handoverToTarget(), sourceKey(), testSign)
	if err != nil {
		t.Fatal(err)
	}

	return sf
}

func TestSealWithKeyWritesARecordSignedByAnotherInstance(t *testing.T) {
	t.Parallel()

	// Seal checks its output against the payload's owner key, so it cannot
	// write this record.
	if _, err := ownerrecord.Seal(handoverToTarget(), testSign); err == nil {
		t.Error("Seal wrote a record that its owner key did not sign")
	}

	sf := sealHandover(t)

	got, err := ownerrecord.OpenWithKey(sf, "instance-fixture-a", sourceKey())
	if err != nil {
		t.Fatal(err)
	}

	if !recordsEqual(got, handoverToTarget()) {
		t.Errorf("opened record differs\ngot:  %+v\nwant: %+v", got, handoverToTarget())
	}

	// SealWithKey still refuses a signer that does not hold the key it names.
	if _, err := ownerrecord.SealWithKey(handoverToTarget(), targetKey.Public().(ed25519.PublicKey), testSign); err == nil {
		t.Error("SealWithKey accepted a signature that the named key did not make")
	}
}

func TestOpenRefusesARecordSignedByAnotherInstance(t *testing.T) {
	t.Parallel()

	_, err := ownerrecord.Open(sealHandover(t))
	wantRefusal(t, err, progress.CodeRequestUnsigned)
}

func TestOpenWithKeyNeverUsesThePayloadKey(t *testing.T) {
	t.Parallel()

	sf := sealHandover(t)

	// The payload names the target's key, but the record was signed by the
	// source, so verifying with the target's key must fail.
	_, err := ownerrecord.OpenWithKey(sf, "instance-fixture-b", targetKey.Public().(ed25519.PublicKey))
	wantRefusal(t, err, progress.CodeRequestUnsigned)

	// A record that its own owner signed opens under OpenWithKey only when the
	// caller supplies that key.
	live := seal(t, liveRecord())
	if _, err := ownerrecord.OpenWithKey(live, "instance-fixture-a", sourceKey()); err != nil {
		t.Errorf("OpenWithKey refused a record signed by the supplied key: %v", err)
	}

	_, err = ownerrecord.OpenWithKey(live, "instance-fixture-b", targetKey.Public().(ed25519.PublicKey))
	wantRefusal(t, err, progress.CodeRequestUnsigned)
}

func TestOpenWithKeyBindsTheSignerID(t *testing.T) {
	t.Parallel()

	sf := sealHandover(t)

	// The key verifies, but the caller says it belongs to another instance
	// than the one the record names as its signer.
	for _, id := range []string{"instance-fixture-b", "instance-other", ""} {
		_, err := ownerrecord.OpenWithKey(sf, id, sourceKey())
		wantRefusal(t, err, progress.CodeRequestUnsigned)
	}
}

func TestOpenWithKeyRefusesBadKeys(t *testing.T) {
	t.Parallel()

	sf := sealHandover(t)

	for name, key := range map[string]ed25519.PublicKey{
		"nil":                    nil,
		"short":                  sourceKey()[:31],
		"long":                   append(bytes.Clone(sourceKey()), 0),
		"identity":               ed25519.PublicKey(unhex(weakKeys["identity"])),
		"all zero":               ed25519.PublicKey(make([]byte, 32)),
		"non-canonical identity": ed25519.PublicKey(unhex("eeffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f")),
	} {
		_, err := ownerrecord.OpenWithKey(sf, "instance-fixture-a", key)
		if r, ok := progress.AsRefusal(err); !ok || r.Code != progress.CodeRequestUnsigned {
			t.Errorf("%s: err = %v, want a REQUEST_UNSIGNED refusal", name, err)
		}
	}
}

func TestOpenWithKeyAppliesTheSameChecks(t *testing.T) {
	t.Parallel()

	sf := sealHandover(t)

	wrongFormat := sf
	wrongFormat.Format = 2

	_, err := ownerrecord.OpenWithKey(wrongFormat, "instance-fixture-a", sourceKey())
	wantRefusal(t, err, progress.CodeJobRequestInvalid)

	shortSig := sf
	shortSig.Signature = sf.Signature[:10]

	_, err = ownerrecord.OpenWithKey(shortSig, "instance-fixture-a", sourceKey())
	wantRefusal(t, err, progress.CodeRequestUnsigned)

	// A record whose payload names a small-order owner key is refused even
	// when a trusted key signed it.
	weak := handoverToTarget()
	weak.OwnerPublicKey = unhex(weakKeys["identity"])

	if _, err := ownerrecord.SealWithKey(weak, sourceKey(), testSign); err == nil {
		t.Error("SealWithKey wrote a record that names a small-order owner key")
	}
}

func TestHandoverRefusal(t *testing.T) {
	t.Parallel()

	sf := sealHandover(t)

	_, openErr := ownerrecord.Open(sf)

	got := ownerrecord.HandoverRefusal(sf, openErr)
	wantRefusal(t, got, progress.CodeOwnerHandover)

	// A live record that fails Open keeps its own refusal.
	live := seal(t, liveRecord())
	live.Signature = bytes.Clone(live.Signature)
	live.Signature[0] ^= 0x01

	_, liveErr := ownerrecord.Open(live)
	if got := ownerrecord.HandoverRefusal(live, liveErr); !errors.Is(got, liveErr) {
		t.Errorf("a live record's refusal became %v", got)
	}

	// Only a signature failure is refined. A wrong format, a malformed payload,
	// or an error that is not a refusal is passed back unchanged.
	wrongFormat := sf
	wrongFormat.Format = 2

	_, formatErr := ownerrecord.Open(wrongFormat)
	if got := ownerrecord.HandoverRefusal(wrongFormat, formatErr); !errors.Is(got, formatErr) {
		t.Errorf("a format refusal became %v", got)
	}

	plain := errors.New("disk read failed")
	if got := ownerrecord.HandoverRefusal(sf, plain); !errors.Is(got, plain) {
		t.Errorf("a plain error became %v", got)
	}

	garbled := sf
	garbled.Payload = append(bytes.Clone(sf.Payload), 'x')

	unsignedErr := &progress.Refusal{Code: progress.CodeRequestUnsigned, Message: "fixture"}
	if got := ownerrecord.HandoverRefusal(garbled, unsignedErr); !errors.Is(got, unsignedErr) {
		t.Errorf("a malformed payload's refusal became %v", got)
	}

	// A payload that says handover twice in different ways is not read as
	// handover, because the strict decoder refuses it.
	ambiguous := sf
	ambiguous.Payload = bytes.Replace(sf.Payload, []byte(`"mode":"handover"`), []byte(`"mode":"handover","MODE":"live"`), 1)

	if got := ownerrecord.HandoverRefusal(ambiguous, unsignedErr); !errors.Is(got, unsignedErr) {
		t.Errorf("an ambiguous payload's refusal became %v", got)
	}

	// No error in, no error out, and never a record.
	if got := ownerrecord.HandoverRefusal(sf, nil); got != nil {
		t.Errorf("HandoverRefusal(nil) = %v", got)
	}
}

func TestHandoverRefusalNeverReturnsNilForAnError(t *testing.T) {
	t.Parallel()

	inputs := []ownerrecord.Signed{
		{},
		sealHandover(t),
		seal(t, liveRecord()),
		{Format: ownerrecord.FormatV1, Payload: []byte(`{"mode":"handover"}`)},
	}

	for i, sf := range inputs {
		for _, err := range []error{
			errors.New("plain"),
			&progress.Refusal{Code: progress.CodeRequestUnsigned, Message: "x"},
			&progress.Refusal{Code: progress.CodeJobRequestInvalid, Message: "x"},
		} {
			if ownerrecord.HandoverRefusal(sf, err) == nil {
				t.Errorf("input %d, error %v: HandoverRefusal returned nil", i, err)
			}
		}
	}
}
