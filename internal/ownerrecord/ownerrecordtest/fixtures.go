// Package ownerrecordtest holds golden signed owner records for tests and the
// published test key that signs them.
package ownerrecordtest

import (
	"bytes"
	"crypto/ed25519"
	"embed"
	"encoding/hex"
	"fmt"
)

//go:embed testdata
var files embed.FS

// Fixture returns the bytes of the named golden file. It panics when the name
// is unknown, because a test that names a missing fixture is a programming
// error.
func Fixture(name string) []byte {
	b, err := files.ReadFile("testdata/" + name)
	if err != nil {
		panic(fmt.Sprintf("ownerrecordtest: unknown fixture %q", name))
	}

	return b
}

// TestKey returns a fresh copy of the private key built from the seed in
// testdata/test-key.seed.hex. The seed is a published test vector, so the key
// signs nothing real. It panics when the seed file is not a valid seed.
func TestKey() ed25519.PrivateKey {
	seed, err := hex.DecodeString(string(bytes.TrimSpace(Fixture("test-key.seed.hex"))))
	if err != nil || len(seed) != ed25519.SeedSize {
		panic("ownerrecordtest: test-key.seed.hex is not a hex Ed25519 seed")
	}

	return ed25519.NewKeyFromSeed(seed)
}
