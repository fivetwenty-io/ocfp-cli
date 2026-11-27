package firstrun_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/firstrun"
)

// knownAnswer is one code with everything the derivation gives for it.
// The values were taken from the derivation before the package kept its
// secrets in byte buffers, so they pin the derived keys across that
// change.
type knownAnswer struct {
	code       string
	normalized string
	formatted  string
	seed       string
	private    string
	public     string
}

var knownAnswers = []knownAnswer{
	{
		code:       "0000 1111 aaaa 2222 7777 zzzz",
		normalized: "OOOOIIIIAAAA22227777ZZZZ",
		formatted:  "OOOO-IIII-AAAA-2222-7777-ZZZZ",
		seed:       "8529879cdfd62c73aaf12f4605db23cbc4b73d37c1a31e71a640ad64aa3141f6",
		private: "8529879cdfd62c73aaf12f4605db23cbc4b73d37c1a31e71a640ad64aa3141f6" +
			"7f1bc2ee8aef3d0f4369836f3282637bf4e44ad6d27325922d6a65cf6d0adb1a",
		public: "fxvC7orvPQ9DaYNvMoJje/TkStbScyWSLWplz20K2xo=",
	},
	{
		code:       "mzxw-6ytb-oi2d-cnrr-gq3t-g6zb",
		normalized: "MZXW6YTBOI2DCNRRGQ3TG6ZB",
		formatted:  "MZXW-6YTB-OI2D-CNRR-GQ3T-G6ZB",
		seed:       "6b6a5cd4a97fc55973adbfa353cc2a981e14bcd9a1fe700174a64d44d8b2c1b9",
		private: "6b6a5cd4a97fc55973adbfa353cc2a981e14bcd9a1fe700174a64d44d8b2c1b9" +
			"82f40650ac291512c8b431fcd8c9e41439160a66a23a9fad51584ee2bd2f3243",
		public: "gvQGUKwpFRLItDH82MnkFDkWCmaiOp+tUVhO4r0vMkM=",
	},
	{
		code:       "AAAQEAYEAUDAOCAJBIFQYDIO",
		normalized: "AAAQEAYEAUDAOCAJBIFQYDIO",
		formatted:  "AAAQ-EAYE-AUDA-OCAJ-BIFQ-YDIO",
		seed:       "19564ac9b84c8389323d171050b619f1284a577c95c7c296e03ecac38d9d03b3",
		private: "19564ac9b84c8389323d171050b619f1284a577c95c7c296e03ecac38d9d03b3" +
			"727b22d4fd58b39b56761b905f91bcb017db3a2dfe31bc4db0b9904f646a7ff1",
		public: "cnsi1P1Ys5tWdhuQX5G8sBfbOi3+MbxNsLmQT2Rqf/E=",
	},
}

// TestKnownAnswersAgreeAcrossBothAPIs checks that the string and the byte
// functions give the pinned values, byte for byte, and that the public
// half's wire form is unchanged.
func TestKnownAnswersAgreeAcrossBothAPIs(t *testing.T) {
	t.Parallel()

	for _, ka := range knownAnswers {
		t.Run(ka.code, func(t *testing.T) {
			t.Parallel()

			checkStringAPI(t, ka)
			checkByteAPI(t, ka)
		})
	}
}

func checkStringAPI(t *testing.T, ka knownAnswer) {
	t.Helper()

	norm, err := firstrun.Normalize(ka.code)
	if err != nil || norm != ka.normalized {
		t.Fatalf("Normalize = %q, %v; want %q", norm, err, ka.normalized)
	}

	formatted, err := firstrun.Format(ka.code)
	if err != nil || formatted != ka.formatted {
		t.Fatalf("Format = %q, %v; want %q", formatted, err, ka.formatted)
	}

	seed, err := firstrun.Seed(ka.code)
	if err != nil || hex.EncodeToString(seed[:]) != ka.seed {
		t.Fatalf("Seed = %x, %v; want %s", seed, err, ka.seed)
	}

	pub, priv, err := firstrun.KeyPair(ka.code)
	if err != nil || hex.EncodeToString(priv) != ka.private {
		t.Fatalf("KeyPair private = %x, %v; want %s", []byte(priv), err, ka.private)
	}

	checkWireForm(t, pub, priv, ka.public)

	public, err := firstrun.PublicFor(ka.code)
	if err != nil || public != ka.public {
		t.Fatalf("PublicFor = %q, %v; want %q", public, err, ka.public)
	}
}

func checkByteAPI(t *testing.T, ka knownAnswer) {
	t.Helper()

	code := []byte(ka.code)

	norm, err := firstrun.NormalizeBytes(code)
	if err != nil || string(norm) != ka.normalized {
		t.Fatalf("NormalizeBytes = %q, %v; want %q", norm, err, ka.normalized)
	}

	formatted, err := firstrun.FormatBytes(code)
	if err != nil || string(formatted) != ka.formatted {
		t.Fatalf("FormatBytes = %q, %v; want %q", formatted, err, ka.formatted)
	}

	seed, err := firstrun.SeedBytes(code)
	if err != nil || hex.EncodeToString(seed[:]) != ka.seed {
		t.Fatalf("SeedBytes = %x, %v; want %s", seed, err, ka.seed)
	}

	pub, priv, err := firstrun.KeyPairBytes(code)
	if err != nil || hex.EncodeToString(priv) != ka.private {
		t.Fatalf("KeyPairBytes private = %x, %v; want %s", []byte(priv), err, ka.private)
	}

	checkWireForm(t, pub, priv, ka.public)

	public, err := firstrun.PublicForBytes(code)
	if err != nil || public != ka.public {
		t.Fatalf("PublicForBytes = %q, %v; want %q", public, err, ka.public)
	}

	if string(code) != ka.code {
		t.Fatal("a byte function changed the code it was given")
	}
}

// checkWireForm checks PublicString and ParsePublic against the pinned
// public half, and that the public half is the private key's tail.
func checkWireForm(t *testing.T, pub ed25519.PublicKey, priv ed25519.PrivateKey, want string) {
	t.Helper()

	got, err := firstrun.PublicString(pub)
	if err != nil || got != want {
		t.Fatalf("PublicString = %q, %v; want %q", got, err, want)
	}

	parsed, err := firstrun.ParsePublic(want)
	if err != nil || !bytes.Equal(parsed, pub) || !bytes.Equal(parsed, priv[ed25519.SeedSize:]) {
		t.Fatalf("ParsePublic(%q) = %x, %v; want %x", want, []byte(parsed), err, []byte(pub))
	}
}

// TestGoldenVectorsByteAPI runs the shared vectors through the byte
// functions as well.
func TestGoldenVectorsByteAPI(t *testing.T) {
	t.Parallel()

	for _, v := range loadVectors(t).Vectors {
		norm, err := firstrun.NormalizeBytes([]byte(v.Code))
		if err != nil || string(norm) != v.Normalized {
			t.Fatalf("NormalizeBytes(%q) = %q, %v", v.Code, norm, err)
		}

		seed, err := firstrun.SeedBytes([]byte(v.Code))
		if err != nil || hex.EncodeToString(seed[:]) != v.Seed {
			t.Fatalf("SeedBytes(%q) = %x, %v", v.Code, seed, err)
		}

		public, err := firstrun.PublicForBytes([]byte(v.Code))
		if err != nil || public != v.Public {
			t.Fatalf("PublicForBytes(%q) = %q, %v", v.Code, public, err)
		}
	}
}

// TestGenerateBytesMatchesGenerate checks that both forms of the
// generator encode the same random bytes the same way.
func TestGenerateBytesMatchesGenerate(t *testing.T) {
	t.Parallel()

	random := bytes.Repeat([]byte{0x5a}, firstrun.RandomBytes)

	s, err := firstrun.GenerateFrom(bytes.NewReader(random))
	if err != nil {
		t.Fatal(err)
	}

	b, err := firstrun.GenerateBytesFrom(bytes.NewReader(random))
	if err != nil || string(b) != s {
		t.Fatalf("GenerateBytesFrom = %q, %v; GenerateFrom = %q", b, err, s)
	}

	fresh, err := firstrun.GenerateBytes()
	if err != nil || len(fresh) != firstrun.CodeLength {
		t.Fatalf("GenerateBytes = %d bytes, %v", len(fresh), err)
	}

	_, err = firstrun.GenerateBytesFrom(nil)
	if firstrun.CodeOf(err) != firstrun.CodeRandomUnavailable {
		t.Fatalf("GenerateBytesFrom(nil) gave %v", err)
	}
}

// TestWipeZeroesItsInput checks Wipe on a buffer, on an empty and a nil
// slice, and on a private key, whose first half is the seed.
func TestWipeZeroesItsInput(t *testing.T) {
	t.Parallel()

	b := bytes.Repeat([]byte{0xa5}, 37)
	firstrun.Wipe(b)

	if !allZero(b) {
		t.Fatalf("Wipe left %x", b)
	}

	firstrun.Wipe(nil)
	firstrun.Wipe([]byte{})

	pub, priv, err := firstrun.KeyPair(knownAnswers[0].code)
	if err != nil {
		t.Fatal(err)
	}

	firstrun.Wipe(priv)

	if !allZero(priv) || !allZero(priv.Seed()) {
		t.Fatal("Wipe left part of the private key")
	}

	if got, _ := firstrun.PublicString(pub); got != knownAnswers[0].public {
		t.Fatal("wiping the private key changed the public half")
	}

	seed, err := firstrun.Seed(knownAnswers[0].code)
	if err != nil {
		t.Fatal(err)
	}

	firstrun.Wipe(seed[:])

	if seed != [sha256.Size]byte{} {
		t.Fatal("Wipe left part of the seed")
	}
}

// owned is a buffer the package reported through its hook.
type owned struct {
	label string
	b     []byte
}

// trackCall runs call with the hook set and returns what the package
// reported. It must not run in parallel with other tests of the package.
func trackCall(t *testing.T, call func()) []owned {
	t.Helper()

	var seen []owned

	restore := firstrun.TrackOwned(func(label string, b []byte) {
		seen = append(seen, owned{label, b})
	})
	defer restore()

	call()

	return seen
}

// checkWiped fails unless every reported buffer, other than one that
// shares memory with a value the call handed back, is zero, and unless
// every wanted label was reported.
func checkWiped(t *testing.T, seen []owned, want []string, returned ...[]byte) {
	t.Helper()

	labels := make([]string, 0, len(seen))

	for _, o := range seen {
		labels = append(labels, o.label)

		if slices.ContainsFunc(returned, func(r []byte) bool { return sameMemory(r, o.b) }) {
			continue
		}

		if !allZero(o.b) {
			t.Errorf("the %s buffer is not zero after the call", o.label)
		}
	}

	for _, w := range want {
		if !slices.Contains(labels, w) {
			t.Errorf("no %s buffer was reported; reported %v", w, labels)
		}
	}
}

// TestPackageBuffersAreWipedAfterUse checks that every secret buffer the
// package owns is zero once each call returns, on success and on refusal.
// It sets a package hook, so it does not run in parallel.
//
//nolint:paralleltest // The hook is one package variable.
func TestPackageBuffersAreWipedAfterUse(t *testing.T) {
	code := knownAnswers[1].code
	bad := "AAAQ-EAYE-AUDA-OCAJ-BIFQ-YDI!"

	var (
		bs  []byte
		pub ed25519.PublicKey
		key ed25519.PrivateKey
		err error
	)

	short := func() *strings.Reader { return strings.NewReader("0123") }
	full := func() *strings.Reader { return strings.NewReader("0123456789abcde") }
	malformed, unavailable := firstrun.CodeMalformed, firstrun.CodeRandomUnavailable

	// Each case names the buffers it must report and, for a refusal, the
	// code it must refuse with. Every function here has a refusal path.
	// The functions that take a code refuse a malformed one, and the
	// generators refuse a short read and a nil reader. A nil reader is
	// refused before anything is allocated, so those cases want no buffer.
	cases := []struct {
		name     string
		call     func()
		want     []string
		refuses  string
		returned func() [][]byte
	}{
		{"Normalize", func() { _, err = firstrun.Normalize(code) }, []string{"normalized"}, "", nil},
		{"Normalize refused", func() { _, err = firstrun.Normalize(bad) }, []string{"normalized"}, malformed, nil},
		{"NormalizeBytes", func() { bs, err = firstrun.NormalizeBytes([]byte(code)) }, []string{"normalized"}, "", func() [][]byte { return [][]byte{bs} }},
		{"NormalizeBytes refused", func() { _, err = firstrun.NormalizeBytes([]byte(bad)) }, []string{"normalized"}, malformed, nil},
		{"Format", func() { _, err = firstrun.Format(code) }, []string{"normalized", "formatted"}, "", nil},
		{"Format refused", func() { _, err = firstrun.Format(bad) }, []string{"normalized"}, malformed, nil},
		{"FormatBytes", func() { bs, err = firstrun.FormatBytes([]byte(code)) }, []string{"normalized", "formatted"}, "", func() [][]byte { return [][]byte{bs} }},
		{"FormatBytes refused", func() { _, err = firstrun.FormatBytes([]byte(bad)) }, []string{"normalized"}, malformed, nil},
		{"Seed", func() { _, err = firstrun.Seed(code) }, []string{"message"}, "", nil},
		{"Seed refused", func() { _, err = firstrun.Seed(bad) }, []string{"message"}, malformed, nil},
		{"SeedBytes", func() { _, err = firstrun.SeedBytes([]byte(code)) }, []string{"message"}, "", nil},
		{"SeedBytes refused", func() { _, err = firstrun.SeedBytes([]byte(bad)) }, []string{"message"}, malformed, nil},
		{"KeyPair", func() { pub, key, err = firstrun.KeyPair(code) }, []string{"message", "seed"}, "", func() [][]byte { return [][]byte{pub, key} }},
		{"KeyPair refused", func() { _, _, err = firstrun.KeyPair(bad) }, []string{"message"}, malformed, nil},
		{"KeyPairBytes", func() { pub, key, err = firstrun.KeyPairBytes([]byte(code)) }, []string{"message", "seed"}, "", func() [][]byte { return [][]byte{pub, key} }},
		{"KeyPairBytes refused", func() { _, _, err = firstrun.KeyPairBytes([]byte(bad)) }, []string{"message"}, malformed, nil},
		{"PublicFor", func() { _, err = firstrun.PublicFor(code) }, []string{"message", "seed", "private"}, "", nil},
		{"PublicFor refused", func() { _, err = firstrun.PublicFor(bad) }, []string{"message"}, malformed, nil},
		{"PublicForBytes", func() { _, err = firstrun.PublicForBytes([]byte(code)) }, []string{"message", "seed", "private"}, "", nil},
		{"PublicForBytes refused", func() { _, err = firstrun.PublicForBytes([]byte(bad)) }, []string{"message"}, malformed, nil},
		{"GenerateFrom", func() { _, err = firstrun.GenerateFrom(full()) }, []string{"random", "code"}, "", nil},
		{"GenerateFrom short", func() { _, err = firstrun.GenerateFrom(short()) }, []string{"random"}, unavailable, nil},
		{"GenerateFrom nil", func() { _, err = firstrun.GenerateFrom(nil) }, nil, unavailable, nil},
		{"GenerateBytesFrom", func() { bs, err = firstrun.GenerateBytesFrom(full()) }, []string{"random"}, "", func() [][]byte { return [][]byte{bs} }},
		{"GenerateBytesFrom short", func() { _, err = firstrun.GenerateBytesFrom(short()) }, []string{"random"}, unavailable, nil},
		{"GenerateBytesFrom nil", func() { _, err = firstrun.GenerateBytesFrom(nil) }, nil, unavailable, nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err = nil
			seen := trackCall(t, c.call)

			if firstrun.CodeOf(err) != c.refuses {
				t.Fatalf("the call gave %v, want the refusal code %q", err, c.refuses)
			}

			if c.want == nil && len(seen) != 0 {
				t.Fatalf("the call reported %d buffers, want none", len(seen))
			}

			var returned [][]byte
			if c.returned != nil {
				returned = c.returned()
			}

			checkWiped(t, seen, c.want, returned...)
		})
	}
}

// TestByteFunctionsLeaveTheirInput checks the byte functions' promise to
// read the caller's code and never change it, on success and on refusal.
func TestByteFunctionsLeaveTheirInput(t *testing.T) {
	t.Parallel()

	for _, typed := range []string{knownAnswers[0].code, "AAAQ-EAYE-AUDA-OCAJ-BIFQ-YDI!"} {
		code := []byte(typed)

		_, _ = firstrun.NormalizeBytes(code)
		_, _ = firstrun.FormatBytes(code)
		_, _ = firstrun.SeedBytes(code)
		_, _, _ = firstrun.KeyPairBytes(code)
		_, _ = firstrun.PublicForBytes(code)

		if string(code) != typed {
			t.Fatalf("a byte function changed its input %q to %q", typed, code)
		}
	}
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}

	return true
}

// sameMemory reports whether a and b share any backing memory.
func sameMemory(a, b []byte) bool {
	for i := range a {
		for j := range b {
			if &a[i] == &b[j] {
				return true
			}
		}
	}

	return false
}
