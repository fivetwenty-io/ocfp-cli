package ownerrecord_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord/ownerrecordtest"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

func unhex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}

	return b
}

// The encodings of the eight points of small order, with the sign bit as the
// canonical encoding sets it. A signature whose R is one of these and whose S
// is zero needs no private key, and under a small-order owner key one of them
// verifies for every message.
var smallOrderR = [][]byte{
	unhex("0100000000000000000000000000000000000000000000000000000000000000"), // identity
	unhex("ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f"), // order 2
	unhex("0000000000000000000000000000000000000000000000000000000000000000"), // order 4
	unhex("0000000000000000000000000000000000000000000000000000000000000080"), // order 4
	unhex("26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05"), // order 8
	unhex("26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85"), // order 8
	unhex("c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a"), // order 8
	unhex("c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa"), // order 8
}

// weakKeys are owner keys under which a signature verifies without any
// private key. Each is a small-order point, given in its canonical encoding
// and, where one exists, a non-canonical one.
var weakKeys = map[string]string{
	"identity":                 "0100000000000000000000000000000000000000000000000000000000000000",
	"identity, sign bit":       "0100000000000000000000000000000000000000000000000000000000000080",
	"identity, y = p + 1":      "eeffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
	"order 2":                  "ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
	"order 2, sign bit":        "ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	"order 4, all zero":        "0000000000000000000000000000000000000000000000000000000000000000",
	"order 4, sign bit":        "0000000000000000000000000000000000000000000000000000000000000080",
	"order 4, y = p":           "edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
	"order 4, y = p, sign bit": "edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	"order 8, first":           "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05",
	"order 8, first, sign bit": "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85",
	"order 8, second":          "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a",
	"order 8, second, sign":    "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa",
}

func payloadWithKey(t *testing.T, key []byte) []byte {
	t.Helper()

	r := liveRecord()
	r.OwnerPublicKey = key

	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// forge looks for a record under key and a signature with no private key
// behind it that the standard library accepts for that record. It tries a
// number of generations, because whether a given R verifies depends on the
// message. It returns nil when it finds none.
func forge(t *testing.T, key []byte) (payload, sig []byte) {
	t.Helper()

	for gen := range uint64(256) {
		r := liveRecord()
		r.OwnerPublicKey = key
		r.Generation = gen

		p, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}

		for _, rr := range smallOrderR {
			s := append(bytes.Clone(rr), make([]byte, 32)...)
			if ed25519.Verify(key, ownerrecord.SigningBytes(p), s) {
				return p, s
			}
		}
	}

	return nil, nil
}

func TestOpenRefusesSmallOrderKeys(t *testing.T) {
	t.Parallel()

	for name, h := range weakKeys {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			key := unhex(h)

			// Prove the key is weak: the standard library alone accepts a
			// signature that no private key made. Without this, a wrong entry
			// in the table would pass for the wrong reason.
			payload, sig := forge(t, key)
			if sig == nil {
				t.Fatal("no keyless signature verifies under this key, so the table entry is wrong")
			}

			sf := ownerrecord.Signed{Format: ownerrecord.FormatV1, Payload: payload, Signature: sig}

			_, err := ownerrecord.Open(sf)
			wantRefusal(t, err, progress.CodeJobRequestInvalid)

			constant := func([]byte) ([]byte, error) { return sig, nil }

			r := liveRecord()
			r.OwnerPublicKey = key

			if _, err := ownerrecord.Seal(r, constant); err == nil {
				t.Error("Seal wrote a record under a small-order owner key")
			}

			if _, err := ownerrecord.OpenWithKey(sf, "instance-fixture-a", key); err == nil {
				t.Error("OpenWithKey accepted a small-order signer key")
			}
		})
	}
}

func TestOpenRefusesNonCanonicalKeys(t *testing.T) {
	t.Parallel()

	// y = p + k for k from 2 to 18 encodes the same y as k, which is another
	// encoding of a different, canonical key.
	for k := byte(2); k <= 18; k++ {
		key := unhex("edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f")
		key[0] += k

		_, err := ownerrecord.Open(resign(payloadWithKey(t, key)))
		wantRefusal(t, err, progress.CodeJobRequestInvalid)
	}
}

func TestOpenAcceptsTheTestKey(t *testing.T) {
	t.Parallel()

	// The weak-key check must not refuse an ordinary key whose top bit is
	// set or whose encoding is close to p.
	for i := range 64 {
		seed := bytes.Repeat([]byte{byte(i)}, ed25519.SeedSize)
		priv := ed25519.NewKeyFromSeed(seed)

		r := liveRecord()
		r.OwnerPublicKey = priv.Public().(ed25519.PublicKey)

		sign := func(msg []byte) ([]byte, error) { return ed25519.Sign(priv, msg), nil }
		if _, err := ownerrecord.Seal(r, sign); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
}

func TestOpenBindsSignerToOwner(t *testing.T) {
	t.Parallel()

	r := liveRecord()
	r.SignerID = "instance-someone-else"

	payload, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}

	// The signature is good under the owner key, so the only signer Open can
	// prove is the owner, and a record that names another signer is refused.
	got, err := ownerrecord.Open(resign(payload))
	wantRefusal(t, err, progress.CodeRequestUnsigned)

	if got.SignerID != "" || got.InstallationID != "" {
		t.Errorf("Open returned a record with its refusal: %+v", got)
	}

	if _, err := ownerrecord.Seal(r, testSign); err == nil {
		t.Error("Seal wrote a record whose signer is not the owner")
	}
}

func TestOpenRefusesAmbiguousKeys(t *testing.T) {
	t.Parallel()

	handover := liveRecord()
	handover.Mode = ownerrecord.ModeHandover
	handover.Generation = 8
	handover.Relocation = &ownerrecord.Relocation{ID: "reloc-fixture-01", TargetInstanceID: "instance-fixture-b"}

	base, err := json.Marshal(handover)
	if err != nil {
		t.Fatal(err)
	}

	insert := func(before, add string) []byte {
		if !bytes.Contains(base, []byte(before)) {
			t.Fatalf("payload has no %s", before)
		}

		return bytes.Replace(base, []byte(before), []byte(add+before), 1)
	}

	cases := map[string][]byte{
		"duplicate mode":          insert(`"generation"`, `"mode":"live",`),
		"escaped duplicate mode":  insert(`"generation"`, `"\u006dode":"live",`),
		"mode in upper case":      insert(`"generation"`, `"MODE":"live",`),
		"owner id in other case":  insert(`"generation"`, `"OwnerInstanceID":"instance-other",`),
		"kelvin sign folds to k":  insert(`"generation"`, `"ownerPublicKey":"AAAA",`),
		"duplicate relocation id": bytes.Replace(base, []byte(`{"id":"reloc-fixture-01"`), []byte(`{"id":"x","id":"reloc-fixture-01"`), 1),
		"relocation id upper":     bytes.Replace(base, []byte(`{"id":"reloc-fixture-01"`), []byte(`{"ID":"x","id":"reloc-fixture-01"`), 1),
		"duplicate in new field":  insert(`"generation"`, `"futureField":{"a":1,"a":2},`),
	}

	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := ownerrecord.Open(resign(payload))
			wantRefusal(t, err, progress.CodeJobRequestInvalid)
		})
	}

	// Unknown fields that match no known field, in any case, still pass.
	ok := insert(`"generation"`, `"futureField":{"a":1,"A":[{"b":1},{"b":2}]},"otherField":null,`)
	if _, err := ownerrecord.Open(resign(ok)); err != nil {
		t.Errorf("a payload with distinct newer fields was refused: %v", err)
	}
}

func TestSealRefusesInvalidUTF8(t *testing.T) {
	t.Parallel()

	bad := "inst-\xff"
	if utf8.ValidString(bad) {
		t.Fatal("the test string is valid UTF-8")
	}

	cases := map[string]func(*ownerrecord.Record){
		"installation id": func(r *ownerrecord.Record) { r.InstallationID = bad },
		"owner instance":  func(r *ownerrecord.Record) { r.OwnerInstanceID, r.SignerID = bad, bad },
		"signer id":       func(r *ownerrecord.Record) { r.SignerID = bad },
		"head hash":       func(r *ownerrecord.Record) { r.HeadHash = bad },
		"mode":            func(r *ownerrecord.Record) { r.Mode = ownerrecord.Mode(bad) },
		"relocation id": func(r *ownerrecord.Record) {
			r.Relocation = &ownerrecord.Relocation{ID: bad}
		},
		"relocation target": func(r *ownerrecord.Record) {
			r.Relocation = &ownerrecord.Relocation{ID: "reloc-fixture-01", TargetInstanceID: bad}
		},
		"cut-over hash": func(r *ownerrecord.Record) {
			r.Relocation = &ownerrecord.Relocation{ID: "reloc-fixture-01", CutOverPosition: ptr(int64(1)), CutOverHash: bad}
		},
	}

	for name, mutate := range cases {
		r := liveRecord()
		mutate(&r)

		if _, err := ownerrecord.Seal(r, testSign); err == nil {
			t.Errorf("%s: Seal accepted invalid UTF-8", name)
		}
	}

	// Two ids that differ only in their invalid bytes must not seal to the
	// same payload, which json.Marshal would do by replacing both.
	a, b := liveRecord(), liveRecord()
	a.InstallationID, b.InstallationID = "inst-\xff", "inst-\xfe"

	sa, errA := ownerrecord.Seal(a, testSign)
	sb, errB := ownerrecord.Seal(b, testSign)

	if errA == nil && errB == nil && bytes.Equal(sa.Payload, sb.Payload) {
		t.Error("two different installation ids sealed to the same payload")
	}
}

func TestCutOverAtPositionZeroIsKept(t *testing.T) {
	t.Parallel()

	r := liveRecord()
	r.Relocation = &ownerrecord.Relocation{
		ID:              "reloc-fixture-01",
		CutOverPosition: ptr(int64(0)),
		CutOverHash:     "sha256:" + strings.Repeat("cd", 32),
	}

	sf := seal(t, r)
	if !bytes.Contains(sf.Payload, []byte(`"cutOverPosition":0`)) {
		t.Errorf("the payload dropped a cut-over at position 0: %s", sf.Payload)
	}

	got, err := ownerrecord.Open(sf)
	if err != nil {
		t.Fatal(err)
	}

	if got.Relocation == nil || got.Relocation.CutOverPosition == nil || *got.Relocation.CutOverPosition != 0 {
		t.Errorf("opened relocation = %+v", got.Relocation)
	}
}

func TestOwnerKeyIsTheTestKey(t *testing.T) {
	t.Parallel()

	// Guards the helpers above: liveRecord must carry the key that testSign
	// uses, or every refusal in this file could come from a key mismatch.
	if !bytes.Equal(liveRecord().OwnerPublicKey, ownerrecordtest.TestKey().Public().(ed25519.PublicKey)) {
		t.Fatal("liveRecord does not carry the test key")
	}
}
