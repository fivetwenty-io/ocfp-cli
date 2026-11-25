package enroll

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// updateGolden rewrites the golden files from the code when set. A
// change to a golden file is a change to the wire contract, so only a
// deliberate change to a format runs with it.
var updateGolden = os.Getenv("ENROLL_UPDATE_GOLDEN") == "1"

// rfcSeeds are the secret keys of test vectors 1, 2, and 3 of RFC 8032
// section 7.1. Every key in the golden vectors is made from them.
var rfcSeeds = []string{
	"9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60",
	"4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb",
	"c5aa8df43f9f837bedb7442f31dcb7b166d38535076f094b85ce3a2e0b4458f7",
}

func rfcKey(t testing.TB, n int) ed25519.PrivateKey {
	t.Helper()

	seed, err := hex.DecodeString(rfcSeeds[n])
	if err != nil {
		t.Fatal(err)
	}

	return ed25519.NewKeyFromSeed(seed)
}

func hostKeyLine(t testing.TB, n int) string {
	t.Helper()

	pub, err := ssh.NewPublicKey(rfcKey(t, n).Public())
	if err != nil {
		t.Fatal(err)
	}

	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
}

func readTestdata(t testing.TB, name string) []byte {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join("testdata", name)
	if updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}

		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the code's output:\ngot  %q\nwant %q", name, got, want)
	}
}

func checkGoldenJSON(t *testing.T, name string, v any) {
	t.Helper()

	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	checkGolden(t, name, append(b, '\n'))
}

// nkeyPublic encodes an Ed25519 public key as an nkeys public key with
// the prefix byte, the way the nkeys library does: base32 of the prefix,
// the key, and its little-endian CRC16, with no padding.
func nkeyPublic(t testing.TB, prefix byte, n int) string {
	t.Helper()

	raw := append([]byte{prefix}, rfcKey(t, n).Public().(ed25519.PublicKey)...)

	var crc uint16

	for _, c := range raw {
		crc ^= uint16(c) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}

	raw = append(raw, byte(crc), byte(crc>>8))

	return nkeyEncoding.EncodeToString(raw)
}

// The golden names. None names the managing system; the roots are
// whatever the address carries.
const (
	goldenInstallation = "inst-0123456789abcdef"
	goldenOrg          = "acme"
	goldenBloc         = "ocfp-acme-prod"
	goldenWorker       = "w-0001"
)

func goldenAddress(t testing.TB) Address {
	t.Helper()

	return Address{
		UIHost:             "console.example.com",
		Version:            AddressVersion,
		BusURL:             "wss://bus.example.com:443",
		Installation:       goldenInstallation,
		Org:                goldenOrg,
		AdmissionAccount:   nkeyPublic(t, nkeyAccount, 2),
		RequestRoot:        "example.acme.svc.join.v1",
		InboxRoot:          "example.acme.svc.join.inbox",
		ReleaseRequestRoot: "example.acme.svc.release.v1",
		ReleaseAnswerRoot:  "example.acme.svc.release.inbox",
		CAPin:              "6db5e9b8a1bace1cdd9a7c6adb9e9396acc5073465d9fe8e3a0ef6d9c60d6d4f",
	}
}
