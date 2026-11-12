package ownerrecordtest_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord/ownerrecordtest"
)

var update = flag.Bool("update", false, "rewrite the golden owner records in testdata")

// seedLabel is the public string whose SHA-256 is the test key's seed. Anyone
// can derive the key from it, which is the point: it signs nothing real.
const seedLabel = "ocfp owner record test vector, signs nothing real"

func sign(msg []byte) ([]byte, error) {
	return ed25519.Sign(ownerrecordtest.TestKey(), msg), nil
}

func baseRecord() ownerrecord.Record {
	return ownerrecord.Record{
		InstallationID:  "inst-fixture-01",
		Org:             "org-fixture",
		Bloc:            "bloc-fixture",
		OwnerInstanceID: "instance-fixture-a",
		OwnerPublicKey:  ownerrecordtest.TestKey().Public().(ed25519.PublicKey),
		Epoch:           3,
		Mode:            ownerrecord.ModeLive,
		Generation:      7,
		HeadPosition:    1042,
		HeadHash:        "sha256:" + strings.Repeat("ab", 32),
		SignerID:        "instance-fixture-a",
		SignedAt:        time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC),
	}
}

func encode(t *testing.T, sf ownerrecord.Signed) []byte {
	t.Helper()

	b, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	return append(b, '\n')
}

func sealed(t *testing.T, r ownerrecord.Record) ownerrecord.Signed {
	t.Helper()

	sf, err := ownerrecord.Seal(r, sign)
	if err != nil {
		t.Fatal(err)
	}

	return sf
}

// golden builds each fixture from the code, so the files on disk can be
// checked against it byte for byte. Ed25519 signatures are deterministic, so
// the same inputs always give the same bytes.
func golden(t *testing.T) map[string][]byte {
	t.Helper()

	out := map[string][]byte{}

	live := sealed(t, baseRecord())
	out["record-v1.json"] = encode(t, live)

	handover := baseRecord()
	handover.Mode = ownerrecord.ModeHandover
	handover.Generation = 8
	handover.Relocation = &ownerrecord.Relocation{ID: "reloc-fixture-01", TargetInstanceID: "instance-fixture-b"}
	out["record-handover-v1.json"] = encode(t, sealed(t, handover))

	// A payload from a later writer that adds a field this version does not
	// know. It is signed over its own bytes, as a newer writer would sign it.
	newer := bytes.Replace(live.Payload, []byte(`{"installationId"`), []byte(`{"futureField":{"note":"added by a later writer"},"installationId"`), 1)
	out["record-newer-fields-v1.json"] = encode(t, ownerrecord.Signed{
		Format:    ownerrecord.FormatV1,
		Payload:   newer,
		Signature: ed25519.Sign(ownerrecordtest.TestKey(), ownerrecord.SigningBytes(newer)),
	})

	bad := live
	bad.Signature = bytes.Clone(live.Signature)
	bad.Signature[len(bad.Signature)-1] ^= 0x01
	out["record-bad-signature-v1.json"] = encode(t, bad)

	return out
}

func TestFixturesMatchSeal(t *testing.T) {
	for name, want := range golden(t) {
		if *update {
			if err := os.WriteFile(filepath.Join("testdata", name), want, 0o644); err != nil {
				t.Fatal(err)
			}

			continue
		}

		if got := ownerrecordtest.Fixture(name); !bytes.Equal(got, want) {
			t.Errorf("%s differs from what Seal produces\ngot:\n%s\nwant:\n%s", name, got, want)
		}
	}
}

func TestTestKeyComesFromThePublishedSeed(t *testing.T) {
	t.Parallel()

	seed := sha256.Sum256([]byte(seedLabel))

	raw := strings.TrimSpace(string(ownerrecordtest.Fixture("test-key.seed.hex")))
	if raw != hex.EncodeToString(seed[:]) {
		t.Errorf("test-key.seed.hex = %s, want SHA-256 of %q", raw, seedLabel)
	}

	if !bytes.Equal(ownerrecordtest.TestKey().Seed(), seed[:]) {
		t.Error("TestKey does not come from test-key.seed.hex")
	}

	note := string(ownerrecordtest.Fixture("test-key.seed.README"))
	if !strings.Contains(note, "published test vector") || !strings.Contains(note, "signs nothing real") {
		t.Error("the note beside the seed does not say it is a published test vector that signs nothing real")
	}
}

func TestTestKeyIsACopy(t *testing.T) {
	t.Parallel()

	k := ownerrecordtest.TestKey()
	for i := range k {
		k[i] = 0
	}

	if bytes.Equal(ownerrecordtest.TestKey(), k) {
		t.Error("TestKey returns shared memory")
	}
}

func TestFixturePanicsOnUnknownName(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("Fixture did not panic for an unknown name")
		}
	}()

	ownerrecordtest.Fixture("missing.json")
}
