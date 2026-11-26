package firstrun_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/fivetwenty-io/ocfp-cli/firstrun"
)

type vector struct {
	Code       string `json:"code"`
	Normalized string `json:"normalized"`
	Seed       string `json:"seed"`
	Public     string `json:"public"`
}

type vectorFile struct {
	Domain  string   `json:"domain"`
	Vectors []vector `json:"vectors"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()

	b, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}

	var f vectorFile

	err = json.Unmarshal(b, &f)
	if err != nil {
		t.Fatal(err)
	}

	if len(f.Vectors) < 3 {
		t.Fatalf("the vector file holds %d vectors, want at least 3", len(f.Vectors))
	}

	return f
}

// TestGoldenVectors checks every vector the core and the setup page also
// check: the normalized form, the seed, the key pair, and the public half.
func TestGoldenVectors(t *testing.T) {
	t.Parallel()

	f := loadVectors(t)

	if f.Domain != firstrun.Domain {
		t.Fatalf("the vector file's domain is %q, the package's is %q", f.Domain, firstrun.Domain)
	}

	for _, v := range f.Vectors {
		t.Run(v.Code, func(t *testing.T) {
			t.Parallel()

			norm, err := firstrun.Normalize(v.Code)
			if err != nil {
				t.Fatalf("Normalize: %v", err)
			}

			if norm != v.Normalized {
				t.Fatalf("Normalize = %q, want %q", norm, v.Normalized)
			}

			seed, err := firstrun.Seed(v.Code)
			if err != nil {
				t.Fatalf("Seed: %v", err)
			}

			if got := hex.EncodeToString(seed[:]); got != v.Seed {
				t.Fatalf("Seed = %s, want %s", got, v.Seed)
			}

			pub, priv, err := firstrun.KeyPair(v.Code)
			if err != nil {
				t.Fatalf("KeyPair: %v", err)
			}

			if !bytes.Equal(priv.Seed(), seed[:]) {
				t.Fatal("the private key's seed is not the derived seed")
			}

			got, err := firstrun.PublicString(pub)
			if err != nil {
				t.Fatalf("PublicString: %v", err)
			}

			if got != v.Public {
				t.Fatalf("PublicString = %s, want %s", got, v.Public)
			}

			parsed, err := firstrun.ParsePublic(v.Public)
			if err != nil {
				t.Fatalf("ParsePublic: %v", err)
			}

			msg := []byte("statement")
			if !ed25519.Verify(parsed, msg, ed25519.Sign(priv, msg)) {
				t.Fatal("a signature by the derived key does not verify against the vector's public half")
			}

			formatted, err := firstrun.Format(v.Code)
			if err != nil {
				t.Fatalf("Format: %v", err)
			}

			if again, _ := firstrun.Normalize(formatted); again != v.Normalized {
				t.Fatalf("Format(%q) = %q does not normalize back", v.Code, formatted)
			}

			if strings.Contains(v.Code, "-") && formatted != v.Code {
				t.Fatalf("Format = %q, want %q", formatted, v.Code)
			}
		})
	}
}

// TestGenerateFromKnownBytes pins the encoding of the random bytes: bytes
// 0 to 14 give the first vector's code, and 15 bytes of 0xff the second.
func TestGenerateFromKnownBytes(t *testing.T) {
	t.Parallel()

	counting := make([]byte, firstrun.RandomBytes)
	for i := range counting {
		counting[i] = byte(i)
	}

	cases := []struct {
		in   []byte
		want string
	}{
		{counting, "AAAQEAYEAUDAOCAJBIFQYDIO"},
		{bytes.Repeat([]byte{0xff}, firstrun.RandomBytes), "777777777777777777777777"},
	}

	for _, c := range cases {
		got, err := firstrun.GenerateFrom(bytes.NewReader(c.in))
		if err != nil {
			t.Fatalf("GenerateFrom: %v", err)
		}

		if got != c.want {
			t.Fatalf("GenerateFrom = %q, want %q", got, c.want)
		}
	}
}

var formattedShape = regexp.MustCompile(`^[A-Z2-7]{4}(-[A-Z2-7]{4}){5}$`)

// TestGenerateGivesFreshWellFormedCodes checks that Generate reads the
// system's generator and that every code it returns normalizes and
// formats.
func TestGenerateGivesFreshWellFormedCodes(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}

	for range 64 {
		code, err := firstrun.Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}

		if len(code) != firstrun.CodeLength {
			t.Fatalf("Generate gave %d characters, want %d", len(code), firstrun.CodeLength)
		}

		norm, err := firstrun.Normalize(code)
		if err != nil || norm != code {
			t.Fatalf("Generate's code %q is not in its normalized form", code)
		}

		formatted, err := firstrun.Format(code)
		if err != nil || !formattedShape.MatchString(formatted) {
			t.Fatalf("Format(%q) = %q, %v", code, formatted, err)
		}

		if seen[code] {
			t.Fatal("Generate repeated a code")
		}

		seen[code] = true
	}
}

// TestGenerateRefusesAShortRead checks that a generator that runs dry is
// a coded refusal and never a short code.
func TestGenerateRefusesAShortRead(t *testing.T) {
	t.Parallel()

	_, err := firstrun.GenerateFrom(bytes.NewReader(make([]byte, firstrun.RandomBytes-1)))
	if firstrun.CodeOf(err) != firstrun.CodeRandomUnavailable {
		t.Fatalf("a short read gave %v, want %s", err, firstrun.CodeRandomUnavailable)
	}

	_, err = firstrun.GenerateFrom(iotest.ErrReader(errors.New("no entropy")))
	if firstrun.CodeOf(err) != firstrun.CodeRandomUnavailable {
		t.Fatalf("a failing reader gave %v, want %s", err, firstrun.CodeRandomUnavailable)
	}

	_, err = firstrun.GenerateFrom(nil)
	if firstrun.CodeOf(err) != firstrun.CodeRandomUnavailable {
		t.Fatalf("a nil reader gave %v, want %s", err, firstrun.CodeRandomUnavailable)
	}
}

// TestNormalizeRefusesMalformedCodes checks every way a typed code can be
// wrong, and that each refusal is coded and never echoes the input.
func TestNormalizeRefusesMalformedCodes(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"empty":                    "",
		"only separators":          " - - ",
		"one short":                "AAAQ-EAYE-AUDA-OCAJ-BIFQ-YDI",
		"one long":                 "AAAQ-EAYE-AUDA-OCAJ-BIFQ-YDIOA",
		"digit 8":                  "AAAQ-EAYE-AUDA-OCAJ-BIFQ-YDI8",
		"digit 9":                  "AAAQ-EAYE-AUDA-OCAJ-BIFQ-YDI9",
		"padding":                  "AAAQ-EAYE-AUDA-OCAJ-BIFQ-YDI=",
		"underscore":               "AAAQ_EAYE_AUDA_OCAJ_BIFQ_YDIO",
		"tab":                      "AAAQ\tEAYE-AUDA-OCAJ-BIFQ-YDIO",
		"newline":                  "AAAQ-EAYE-AUDA-OCAJ-BIFQ-YDIO\n",
		"non-ASCII dotless i":      "AAAQ-EAYE-AUDA-OCAJ-BıFQ-YDIO",
		"non-ASCII long s":         "AAAQ-EAYE-AUDA-OCAJ-BIFQ-YDſO",
		"fullwidth letter":         "AAAQ-EAYE-AUDA-OCAJ-BIFQ-YDIＯ",
		"invalid UTF-8":            "AAAQ-EAYE-AUDA-OCAJ-BIFQ-YDI\xff",
		"a NUL byte":               "AAAQ-EAYE-AUDA-OCAJ-BIFQ-YDI\x00",
		"far too long":             strings.Repeat("A", 4096),
		"right count, bad letter":  "AAAQEAYEAUDAOCAJBIFQYD!O",
		"base64 public half typed": "cnsi1P1Ys5tWdhuQX5G8sBfbOi3+MbxNsLmQT2Rqf/E=",
	}

	for name, typed := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			norm, err := firstrun.Normalize(typed)
			if err == nil {
				t.Fatalf("Normalize accepted it as %q", norm)
			}

			if firstrun.CodeOf(err) != firstrun.CodeMalformed {
				t.Fatalf("the refusal's code is %q, want %s", firstrun.CodeOf(err), firstrun.CodeMalformed)
			}

			if typed != "" && strings.Contains(err.Error(), typed) {
				t.Fatal("the refusal echoes the typed code")
			}

			if _, err := firstrun.Seed(typed); firstrun.CodeOf(err) != firstrun.CodeMalformed {
				t.Fatalf("Seed gave %v", err)
			}

			if _, _, err := firstrun.KeyPair(typed); firstrun.CodeOf(err) != firstrun.CodeMalformed {
				t.Fatalf("KeyPair gave %v", err)
			}

			if _, err := firstrun.Format(typed); firstrun.CodeOf(err) != firstrun.CodeMalformed {
				t.Fatalf("Format gave %v", err)
			}
		})
	}
}

// TestNormalizeMapsLookalikes checks the two lookalike digits and the
// upper-casing on their own.
func TestNormalizeMapsLookalikes(t *testing.T) {
	t.Parallel()

	got, err := firstrun.Normalize("0000 1111 aaaa 2222 7777 zzzz")
	if err != nil {
		t.Fatal(err)
	}

	if want := "OOOOIIIIAAAA22227777ZZZZ"; got != want {
		t.Fatalf("Normalize = %q, want %q", got, want)
	}
}

// TestPublicHalfRefusesWrongLengths checks that the public half's encoder
// and parser refuse anything but 32 bytes in standard base64.
func TestPublicHalfRefusesWrongLengths(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, 31, 33, 64} {
		_, err := firstrun.PublicString(make(ed25519.PublicKey, n))
		if firstrun.CodeOf(err) != firstrun.CodePublicInvalid {
			t.Fatalf("PublicString of %d bytes gave %v", n, err)
		}
	}

	_, err := firstrun.PublicString(nil)
	if firstrun.CodeOf(err) != firstrun.CodePublicInvalid {
		t.Fatalf("PublicString of nil gave %v", err)
	}

	bad := []string{
		"",
		"not base64!",
		base64.StdEncoding.EncodeToString(make([]byte, 31)),
		base64.StdEncoding.EncodeToString(make([]byte, 33)),
		base64.RawStdEncoding.EncodeToString(make([]byte, 32)),
		base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32)),
		" " + base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}

	for _, s := range bad {
		_, err := firstrun.ParsePublic(s)
		if firstrun.CodeOf(err) != firstrun.CodePublicInvalid {
			t.Fatalf("ParsePublic(%q) gave %v", s, err)
		}
	}
}

// TestCodeOfReadsOnlyCodedErrors checks CodeOf on an uncoded and a
// wrapped error.
func TestCodeOfReadsOnlyCodedErrors(t *testing.T) {
	t.Parallel()

	if got := firstrun.CodeOf(nil); got != "" {
		t.Fatalf("CodeOf(nil) = %q", got)
	}

	if got := firstrun.CodeOf(errors.New("plain")); got != "" {
		t.Fatalf("CodeOf(plain) = %q", got)
	}

	_, err := firstrun.Normalize("x")

	wrapped := errors.Join(errors.New("outer"), err)
	if got := firstrun.CodeOf(wrapped); got != firstrun.CodeMalformed {
		t.Fatalf("CodeOf(wrapped) = %q", got)
	}
}

// FuzzNormalize checks that no input panics and that whatever Normalize
// accepts is in its normalized form and derives a key.
func FuzzNormalize(f *testing.F) {
	f.Add("aaaq eaye auda ocaj bifq yd1o")
	f.Add("7777-7777-7777-7777-7777-7777")
	f.Add("")
	f.Add("\xff\xfe")

	f.Fuzz(func(t *testing.T, typed string) {
		norm, err := firstrun.Normalize(typed)
		if err != nil {
			return
		}

		again, err := firstrun.Normalize(norm)
		if err != nil || again != norm || len(norm) != firstrun.CodeLength {
			t.Fatalf("Normalize(%q) = %q is not stable", typed, norm)
		}

		if _, _, err := firstrun.KeyPair(typed); err != nil {
			t.Fatalf("KeyPair refused an accepted code: %v", err)
		}
	})
}
