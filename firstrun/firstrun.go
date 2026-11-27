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
//
// The code, the seed, and the private key are secrets, so the package
// keeps them in byte slices and overwrites with zeros every buffer it
// owns before the call that made it returns. A Go string can't be
// overwritten, so a string code, and every string the string functions
// return, stays in memory until the collector reclaims it and the memory
// is reused. Where that matters, use the functions whose names end in
// Bytes. They read the caller's []byte code in place and never change or
// keep it, and they return []byte results that belong to the caller, who
// clears them with Wipe when done. A seed is cleared with Wipe(seed[:]),
// and a private key with Wipe(priv), because its first 32 bytes are the
// seed.
//
// Wiping in Go is best effort. Every buffer the package owns is on the
// heap, where the collector never moves it, so Wipe clears its only copy.
// What the package can't clear is the 32-byte seed left in stack frames
// and result slots, in seedOf, in sha256.Sum256, and in the values Seed
// and SeedBytes return, along with anything the compiler spills from a
// register. SHA-256 and Ed25519 also keep copies in their own internal
// state, and no Go string can be cleared at all.
package firstrun

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"io"
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

// formattedLength is the number of characters in a formatted code: the
// 24 characters and the five hyphens between their six groups.
const formattedLength = CodeLength + CodeLength/GroupSize - 1

// codeText is either form a code comes in. The derivation reads both in
// place, so a string code is never copied into a second buffer.
type codeText interface {
	~string | ~[]byte
}

// ownedHook, when set, sees every secret buffer the package allocates or
// takes over, so a test can check that each one is zero once the call
// returns. It is nil outside tests.
var ownedHook func(label string, b []byte)

// own reports b to ownedHook and returns it.
func own(label string, b []byte) []byte {
	if ownedHook != nil {
		ownedHook(label, b)
	}

	return b
}

// Wipe overwrites every byte of b with zero. Use it on a []byte code, on
// a seed as Wipe(seed[:]), and on a private key as Wipe(priv), whose
// first 32 bytes are the seed. Wiping a private key leaves the public
// half that came with it intact, because that is a separate buffer.
//
// Wipe is never inlined, so the compiler can't drop the zeros as stores
// to memory that is not read again.
//
//go:noinline
func Wipe(b []byte) {
	clear(b)
}

// Generate makes a new code from the operating system's random source and
// returns it in its normalized form. A Go string can't be wiped, so use
// GenerateBytes where that matters.
func Generate() (string, error) {
	return GenerateFrom(rand.Reader)
}

// GenerateBytes makes a new code from the operating system's random
// source and returns it in its normalized form. The caller owns the code
// and clears it with Wipe when done.
func GenerateBytes() ([]byte, error) {
	return GenerateBytesFrom(rand.Reader)
}

// GenerateFrom makes a code from the next RandomBytes bytes of r and
// returns it in its normalized form. It refuses with
// CodeRandomUnavailable when r is nil, fails, or ends early. A Go string
// can't be wiped, so use GenerateBytesFrom where that matters.
func GenerateFrom(r io.Reader) (string, error) {
	code, err := GenerateBytesFrom(r)
	if err != nil {
		return "", err
	}

	defer Wipe(own("code", code))

	return string(code), nil
}

// GenerateBytesFrom makes a code from the next RandomBytes bytes of r and
// returns it in its normalized form. It refuses with
// CodeRandomUnavailable when r is nil, fails, or ends early. It wipes the
// random bytes it read, and the caller owns the code and clears it with
// Wipe when done.
func GenerateBytesFrom(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, refuse(CodeRandomUnavailable, "no random source")
	}

	random := own("random", make([]byte, RandomBytes))
	defer Wipe(random)

	_, err := io.ReadFull(r, random)
	if err != nil {
		return nil, refuse(CodeRandomUnavailable, "the random source failed: "+err.Error())
	}

	code := make([]byte, encoding.EncodedLen(RandomBytes))
	encoding.Encode(code, random)

	return code, nil
}

// normalizeInto writes the normalized form of typed into dst, which holds
// exactly CodeLength bytes, and refuses with CodeMalformed what isn't a
// code. On a refusal dst may hold part of the code, so the caller wipes
// dst on every path.
func normalizeInto[T codeText](dst []byte, typed T) error {
	// A longer input can't be a code, and stopping early keeps the work
	// bounded for an input that is far too long.
	const most = 4 * CodeLength

	if len(typed) > most {
		return refuse(CodeMalformed, "the first-run code is too long")
	}

	n := 0

	for i := range len(typed) {
		c := typed[i]

		switch {
		case c == ' ' || c == '-':
			continue
		case c == '0':
			c = 'O'
		case c == '1':
			c = 'I'
		case c >= 'a' && c <= 'z':
			c -= 'a' - 'A'
		case (c >= 'A' && c <= 'Z') || (c >= '2' && c <= '7'):
		default:
			return refuse(CodeMalformed, "the first-run code holds a character outside its alphabet")
		}

		if n < len(dst) {
			dst[n] = c
		}

		n++
	}

	if n != CodeLength {
		return refuse(CodeMalformed, "the first-run code is not 24 characters")
	}

	return nil
}

// Normalize returns a typed code in its normalized form of 24 characters.
// It drops spaces and hyphens, upper-cases ASCII letters, and reads 0 as
// O and 1 as I. It refuses with CodeMalformed anything that is then not
// exactly 24 characters of the base32 alphabet. It reads the input byte
// by byte, so a non-ASCII letter is refused rather than upper-cased into
// the alphabet. A Go string can't be wiped, so use NormalizeBytes where
// that matters.
func Normalize(typed string) (string, error) {
	norm := own("normalized", make([]byte, CodeLength))
	defer Wipe(norm)

	err := normalizeInto(norm, typed)
	if err != nil {
		return "", err
	}

	return string(norm), nil
}

// NormalizeBytes is Normalize for a code held in bytes. It reads typed
// and never changes or keeps it, so the caller still owns typed and
// wipes it. The caller also owns the normalized code it returns, and
// clears it with Wipe when done. On a refusal it wipes what it had
// written.
func NormalizeBytes(typed []byte) ([]byte, error) {
	norm := own("normalized", make([]byte, CodeLength))

	err := normalizeInto(norm, typed)
	if err != nil {
		Wipe(norm)

		return nil, err
	}

	return norm, nil
}

// formatted returns a code as six groups of four characters joined by
// hyphens, in a buffer that belongs to the caller.
func formatted[T codeText](code T) ([]byte, error) {
	norm := own("normalized", make([]byte, CodeLength))
	defer Wipe(norm)

	err := normalizeInto(norm, code)
	if err != nil {
		return nil, err
	}

	out := own("formatted", make([]byte, formattedLength))

	j := 0

	for i := 0; i < CodeLength; i += GroupSize {
		if i > 0 {
			out[j] = '-'
			j++
		}

		j += copy(out[j:], norm[i:i+GroupSize])
	}

	return out, nil
}

// Format returns a code as six groups of four characters joined by
// hyphens. It normalizes the code first, and refuses with CodeMalformed
// what doesn't normalize. A Go string can't be wiped, so use FormatBytes
// where that matters.
func Format(code string) (string, error) {
	out, err := formatted(code)
	if err != nil {
		return "", err
	}

	defer Wipe(out)

	return string(out), nil
}

// FormatBytes is Format for a code held in bytes. It reads code and never
// changes or keeps it. The caller owns the formatted code it returns,
// and clears it with Wipe when done.
func FormatBytes(code []byte) ([]byte, error) {
	return formatted(code)
}

// seedOf hashes Domain, one zero byte, and the normalized code, all in one
// buffer it wipes before it returns.
func seedOf[T codeText](code T) ([sha256.Size]byte, error) {
	msg := own("message", make([]byte, len(Domain)+1+CodeLength))
	defer Wipe(msg)

	n := copy(msg, Domain)
	msg[n] = 0

	err := normalizeInto(msg[n+1:], code)
	if err != nil {
		return [sha256.Size]byte{}, err
	}

	return sha256.Sum256(msg), nil
}

// Seed returns the Ed25519 seed a code derives: the SHA-256 of Domain,
// one zero byte, and the code's 24 normalized characters. The caller
// owns the seed and clears it with Wipe(seed[:]) when done. A Go string
// can't be wiped, so use SeedBytes where the code matters too.
func Seed(code string) ([sha256.Size]byte, error) {
	return seedOf(code)
}

// SeedBytes is Seed for a code held in bytes. It reads code and never
// changes or keeps it. The caller owns the seed and clears it with
// Wipe(seed[:]) when done.
func SeedBytes(code []byte) ([sha256.Size]byte, error) {
	return seedOf(code)
}

// keyPairOf derives the key pair and wipes the seed it derived it from.
func keyPairOf[T codeText](code T) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	seed, err := seedOf(code)
	if err != nil {
		return nil, nil, err
	}

	defer Wipe(own("seed", seed[:]))

	priv := ed25519.NewKeyFromSeed(seed[:])

	pub := make(ed25519.PublicKey, PublicSize)
	copy(pub, priv[ed25519.SeedSize:])

	return pub, priv, nil
}

// KeyPair returns the Ed25519 key pair whose seed the code derives. The
// private key holds the seed in its first 32 bytes, so the caller clears
// it with Wipe(priv) when done. The public half is a separate buffer.
// A Go string can't be wiped, so use KeyPairBytes where the code matters
// too.
func KeyPair(code string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return keyPairOf(code)
}

// KeyPairBytes is KeyPair for a code held in bytes. It reads code and
// never changes or keeps it. The private key holds the seed in its first
// 32 bytes, so the caller clears it with Wipe(priv) when done.
func KeyPairBytes(code []byte) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return keyPairOf(code)
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

// publicFor derives the key pair, wipes the private key, and returns the
// public half's wire form.
func publicFor[T codeText](code T) (string, error) {
	pub, priv, err := keyPairOf(code)
	if err != nil {
		return "", err
	}

	Wipe(own("private", priv))

	return PublicString(pub)
}

// PublicFor returns the public half, in standard base64, of the key the
// code derives. It wipes the seed and the private key before it returns.
// A Go string can't be wiped, so use PublicForBytes where that matters.
func PublicFor(code string) (string, error) {
	return publicFor(code)
}

// PublicForBytes is PublicFor for a code held in bytes. It reads code and
// never changes or keeps it, and it wipes the seed and the private key
// before it returns.
func PublicForBytes(code []byte) (string, error) {
	return publicFor(code)
}
