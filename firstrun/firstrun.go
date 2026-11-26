// Package firstrun makes and reads a bloc's first-run code, and derives
// the first-run key from it.
//
// The first-run code lets the operator claim the first sign-in of a
// managing system deployed into the bloc. ocfp makes the code when it
// builds the bloc, keeps it in the bloc's vault, and shows it to the
// operator once. The managing system holds only the key's public half,
// which it reads from the bloc's config tree through its deployment
// manifest. A person proves they hold the code by signing a statement
// with the key the code derives.
//
// The derivation is exact, because the managing system and its setup
// page each implement it again and check the vectors in
// testdata/vectors.json:
//
//  1. The code is 15 random bytes in RFC 4648 base32, with the upper-case
//     alphabet A to Z and 2 to 7 and no padding, which gives 24
//     characters and 120 bits.
//  2. It is shown as six groups of four characters joined by hyphens.
//  3. A typed code is normalized by dropping spaces and hyphens,
//     upper-casing ASCII letters, and reading 0 as O and 1 as I. What
//     remains must be exactly 24 characters of the alphabet.
//  4. The seed is the SHA-256 of the ASCII string Domain, one zero byte,
//     and the 24 normalized characters.
//  5. The key pair is the RFC 8032 Ed25519 key pair with that seed, and
//     the public half is its 32 bytes in standard base64.
//
// Nothing in the package panics on any input. Every refusal is an *Error
// with a code, and no refusal repeats the code it was given.
package firstrun

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"io"
	"strings"
)

// Domain is the string the seed hashes before the code, so the seed is
// bound to this one use.
const Domain = "ocfp-first-run-key-v1"

// The code's shape.
const (
	// RandomBytes is how many random bytes a code encodes.
	RandomBytes = 15
	// CodeLength is the number of characters in a normalized code.
	CodeLength = 24
	// GroupSize is the number of characters in each group of a formatted
	// code.
	GroupSize = 4
	// PublicSize is the size of the public half in bytes.
	PublicSize = ed25519.PublicKeySize
)

// The codes of the package's refusals.
const (
	// CodeMalformed refuses a typed code that is not 24 characters of the
	// alphabet once spaces and hyphens are dropped.
	CodeMalformed = "FIRST_RUN_CODE_MALFORMED"
	// CodeRandomUnavailable refuses to make a code when the random source
	// fails or runs dry.
	CodeRandomUnavailable = "FIRST_RUN_RANDOM_UNAVAILABLE"
	// CodePublicInvalid refuses a public half that is not 32 bytes in
	// standard base64.
	CodePublicInvalid = "FIRST_RUN_PUBLIC_INVALID"
)

// Error is a coded refusal. Detail never holds the code it refused.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// CodeOf returns the code of the first *Error in err's chain, or the
// empty string when err holds none.
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}

	return ""
}

func refuse(code, detail string) *Error { return &Error{Code: code, Detail: detail} }

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Generate makes a new code from the operating system's random source and
// returns it in its normalized form.
func Generate() (string, error) {
	return GenerateFrom(rand.Reader)
}

// GenerateFrom makes a code from the next RandomBytes bytes of r and
// returns it in its normalized form. It refuses with
// CodeRandomUnavailable when r is nil, fails, or ends early.
func GenerateFrom(r io.Reader) (string, error) {
	if r == nil {
		return "", refuse(CodeRandomUnavailable, "no random source")
	}

	b := make([]byte, RandomBytes)

	_, err := io.ReadFull(r, b)
	if err != nil {
		return "", refuse(CodeRandomUnavailable, "the random source failed: "+err.Error())
	}

	return encoding.EncodeToString(b), nil
}

// Normalize returns a typed code in its normalized form of 24 characters.
// It drops spaces and hyphens, upper-cases ASCII letters, and reads 0 as
// O and 1 as I. It refuses with CodeMalformed anything that is then not
// exactly 24 characters of the base32 alphabet. It reads the input byte
// by byte, so a non-ASCII letter is refused rather than upper-cased into
// the alphabet.
func Normalize(typed string) (string, error) {
	// A longer input can't be a code, and stopping early keeps the work
	// bounded for an input that is far too long.
	const most = 4 * CodeLength

	if len(typed) > most {
		return "", refuse(CodeMalformed, "the first-run code is too long")
	}

	var b strings.Builder

	b.Grow(CodeLength)

	for i := range len(typed) {
		c := typed[i]

		switch {
		case c == ' ' || c == '-':
		case c == '0':
			b.WriteByte('O')
		case c == '1':
			b.WriteByte('I')
		case c >= 'a' && c <= 'z':
			b.WriteByte(c - 'a' + 'A')
		case (c >= 'A' && c <= 'Z') || (c >= '2' && c <= '7'):
			b.WriteByte(c)
		default:
			return "", refuse(CodeMalformed, "the first-run code holds a character outside its alphabet")
		}
	}

	if b.Len() != CodeLength {
		return "", refuse(CodeMalformed, "the first-run code is not 24 characters")
	}

	return b.String(), nil
}

// Format returns a code as six groups of four characters joined by
// hyphens. It normalizes the code first, and refuses with CodeMalformed
// what doesn't normalize.
func Format(code string) (string, error) {
	norm, err := Normalize(code)
	if err != nil {
		return "", err
	}

	groups := make([]string, 0, CodeLength/GroupSize)
	for i := 0; i < CodeLength; i += GroupSize {
		groups = append(groups, norm[i:i+GroupSize])
	}

	return strings.Join(groups, "-"), nil
}

// Seed returns the Ed25519 seed a code derives: the SHA-256 of Domain,
// one zero byte, and the code's 24 normalized characters.
func Seed(code string) ([sha256.Size]byte, error) {
	norm, err := Normalize(code)
	if err != nil {
		return [sha256.Size]byte{}, err
	}

	h := sha256.New()
	h.Write([]byte(Domain))
	h.Write([]byte{0})
	h.Write([]byte(norm))

	var seed [sha256.Size]byte

	copy(seed[:], h.Sum(nil))

	return seed, nil
}

// KeyPair returns the Ed25519 key pair whose seed the code derives.
func KeyPair(code string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	seed, err := Seed(code)
	if err != nil {
		return nil, nil, err
	}

	priv := ed25519.NewKeyFromSeed(seed[:])

	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, nil, refuse(CodePublicInvalid, "the derived key has no Ed25519 public half")
	}

	return pub, priv, nil
}

// PublicString returns a public half as its 32 bytes in standard base64.
// It refuses with CodePublicInvalid a key of any other size.
func PublicString(pub ed25519.PublicKey) (string, error) {
	if len(pub) != PublicSize {
		return "", refuse(CodePublicInvalid, "the public half is not 32 bytes")
	}

	return base64.StdEncoding.EncodeToString(pub), nil
}

// ParsePublic reads a public half from its standard base64 form. It
// refuses with CodePublicInvalid anything but the padded standard
// encoding of exactly 32 bytes.
func ParsePublic(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil || len(raw) != PublicSize {
		return nil, refuse(CodePublicInvalid, "the public half is not 32 bytes in standard base64")
	}

	return ed25519.PublicKey(raw), nil
}

// PublicFor returns the public half, in standard base64, of the key the
// code derives.
func PublicFor(code string) (string, error) {
	pub, _, err := KeyPair(code)
	if err != nil {
		return "", err
	}

	return PublicString(pub)
}
