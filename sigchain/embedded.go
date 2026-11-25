package sigchain

import (
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

//go:embed embedded/roots.pub
var embeddedRoots string

//go:embed embedded/signing-key-revocation.json
var embeddedRevocation []byte

//go:embed embedded/signing-key-revocation.json.sig
var embeddedRevocationSig []byte

// Embedded returns the trust built into this binary: the root keys and,
// when one is embedded, the newest revocation statement known at build
// time. With no root embedded, every chain fails at its first step.
func Embedded() (Trust, error) {
	roots, err := ParseRoots(embeddedRoots)
	if err != nil {
		return Trust{}, err
	}

	t := Trust{Roots: roots}

	switch {
	case len(embeddedRevocation) == 0 && len(embeddedRevocationSig) == 0:
	case len(embeddedRevocation) == 0 || len(embeddedRevocationSig) == 0:
		return Trust{}, errors.New("sigchain: the embedded revocation lacks its statement or its signature")
	default:
		t.Revocations = []Signed{{Bytes: embeddedRevocation, Signature: embeddedRevocationSig}}
	}

	return t, nil
}

// ParseRoots reads root keys from text that holds one ssh-ed25519 key
// line per line, with blank lines and lines that begin with # passed
// over. It refuses more than MaxRoots keys and a key named twice.
func ParseRoots(text string) ([]ssh.PublicKey, error) {
	var roots []ssh.PublicKey

	seen := map[string]bool{}

	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		pub, err := ParsePublicKey(line)
		if err != nil {
			return nil, fmt.Errorf("sigchain: root line %d: %w", n+1, err)
		}

		if seen[string(pub.Marshal())] {
			return nil, fmt.Errorf("sigchain: root line %d names a key twice", n+1)
		}

		seen[string(pub.Marshal())] = true

		roots = append(roots, pub)
	}

	if len(roots) > MaxRoots {
		return nil, fmt.Errorf("sigchain: %d root keys, more than %d", len(roots), MaxRoots)
	}

	return roots, nil
}
