package enroll

import (
	"encoding/hex"
	"encoding/json"
	"testing"
)

// confirmVector is one golden vector of the confirmation code. Matches
// are typed values that must match, and Refuses typed values that must
// not.
type confirmVector struct {
	PublicKey   string   `json:"publicKey"`
	Digest      string   `json:"digest"`
	Fingerprint string   `json:"fingerprint"`
	Code        string   `json:"code"`
	Matches     []string `json:"matches"`
	Refuses     []string `json:"refuses"`
}

// TestConfirmationCodeSpecVectors pins the two vectors the derivation
// was published with, from the public keys of RFC 8032 test vectors 1
// and 2.
func TestConfirmationCodeSpecVectors(t *testing.T) {
	t.Parallel()

	for _, v := range []struct{ key, digest, fp, code string }{
		{
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINdamAGCsQq31Uv+08lkBzoO4XLz2qYjJa8CGmj3B1Ea",
			"6db5e9b8a1bace1cdd9a7c6adb9e9396acc5073465d9fe8e3a0ef6d9c60d6d4f",
			"SHA256:bbXpuKG6zhzdmnxq256TlqzFBzRl2f6OOg722cYNbU8", "AIOW-ADSS-GVKI",
		},
		{
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAID1AF8PoQ4lakrcKp00bfrycmCzPLsSWjMDNVfEq9GYM",
			"177e278a7eed71a607e964792d25927e3eb078504f7c1a6ec945283c53fd6230",
			"SHA256:F34nin7tcaYH6WR5LSWSfj6weFBPfBpuyUUoPFP9YjA", "5ML2-2ILH-DNS5",
		},
	} {
		d, err := HostKeyDigest(v.key + " root@bastion")
		if err != nil {
			t.Fatal(err)
		}

		if hex.EncodeToString(d[:]) != v.digest {
			t.Errorf("D = %x, want %s", d, v.digest)
		}

		if Fingerprint(d) != v.fp {
			t.Errorf("fingerprint = %s, want %s", Fingerprint(d), v.fp)
		}

		if ConfirmationCode(d) != v.code {
			t.Errorf("code = %s, want %s", ConfirmationCode(d), v.code)
		}

		back, err := ParseFingerprint(v.fp)
		if err != nil || back != d {
			t.Errorf("ParseFingerprint(%s) = %x, %v", v.fp, back, err)
		}
	}
}

// TestConfirmationCodeGolden checks every vector in the golden file,
// which the closed contract checks its own derivation against.
func TestConfirmationCodeGolden(t *testing.T) {
	t.Parallel()

	var vectors []confirmVector

	got := make([]confirmVector, 0, len(rfcSeeds))

	for i := range rfcSeeds {
		key := hostKeyLine(t, i)

		d, err := HostKeyDigest(key)
		if err != nil {
			t.Fatal(err)
		}

		code := ConfirmationCode(d)
		bare := code[0:4] + code[5:9] + code[10:14]
		got = append(got, confirmVector{
			PublicKey: key, Digest: hex.EncodeToString(d[:]), Fingerprint: Fingerprint(d), Code: code,
			Matches: []string{code, bare, lowerLookalikes(bare), bare[0:4] + " " + bare[4:8] + " " + bare[8:]},
			Refuses: []string{bare[:11], bare + "A", "", bare[:11] + "8", Fingerprint(d)[7:19]},
		})
	}

	checkGoldenJSON(t, "confirmation-codes.json", got)

	if err := json.Unmarshal(readTestdata(t, "confirmation-codes.json"), &vectors); err != nil {
		t.Fatal(err)
	}

	for _, v := range vectors {
		d, err := HostKeyDigest(v.PublicKey)
		if err != nil {
			t.Fatal(err)
		}

		if ConfirmationCode(d) != v.Code || Fingerprint(d) != v.Fingerprint {
			t.Errorf("%s: code %s, fingerprint %s", v.PublicKey, ConfirmationCode(d), Fingerprint(d))
		}

		for _, m := range v.Matches {
			if !CodeMatches(m, d) {
				t.Errorf("%q does not match %s", m, v.Code)
			}
		}

		for _, r := range v.Refuses {
			if CodeMatches(r, d) {
				t.Errorf("%q matches %s", r, v.Code)
			}
		}
	}
}

// lowerLookalikes lower-cases a bare code and writes O as 0 and I as 1,
// which normalization reads back.
func lowerLookalikes(bare string) string {
	out := []rune{}

	for _, r := range bare {
		switch r {
		case 'O':
			out = append(out, '0')
		case 'I':
			out = append(out, '1')
		default:
			out = append(out, r|0x20)
		}
	}

	return string(out)
}

func TestNormalizeCode(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"aiow adss gvk1":      "AIOWADSSGVKI",
		"AIOWADSSGVKI":        "AIOWADSSGVKI",
		"AIOW-ADSS-GVKI":      "AIOWADSSGVKI",
		" a-i-o-w-adss-gvki ": "AIOWADSSGVKI",
		"0000 1111 2222":      "OOOOIIII2222",
	} {
		got, err := NormalizeCode(in)
		if err != nil || got != want {
			t.Errorf("NormalizeCode(%q) = %q, %v, want %q", in, got, err, want)
		}
	}

	for _, bad := range []string{"", "AIOWADSSGVK", "AIOWADSSGVKII", "AIOW-ADSS-GVK8", "AIOW_ADSS_GVKI", "AIOWADSSGVKÉ"} {
		if got, err := NormalizeCode(bad); err == nil {
			t.Errorf("NormalizeCode(%q) = %q", bad, got)
		}
	}
}

func TestHostKeyDigestRefuses(t *testing.T) {
	t.Parallel()

	if _, err := HostKeyDigest("ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQC7"); CodeOf(err) != CodeHostKeyUnsupported {
		t.Errorf("an rsa key: %v", err)
	}

	if _, err := HostKeyDigest("ssh-ed25519 AAAA"); CodeOf(err) != CodeRequestInvalid {
		t.Errorf("a broken key: %v", err)
	}

	for _, fp := range []string{"", "MD5:aa", "SHA256:", "SHA256:bbXpuKG6zhzdmnxq256TlqzFBzRl2f6OOg722cYNbU8=", "SHA256:bbXpuKG6"} {
		if _, err := ParseFingerprint(fp); err == nil {
			t.Errorf("ParseFingerprint(%q) took it", fp)
		}
	}
}
