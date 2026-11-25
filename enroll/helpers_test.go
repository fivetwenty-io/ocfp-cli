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
