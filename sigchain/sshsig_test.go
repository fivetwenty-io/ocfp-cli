package sigchain

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// rfcSeeds are the secret keys of test vectors 1, 2, and 3 of RFC 8032
// section 7.1. Every golden vector of the package is made from them, so
// anyone can make the vectors again from the RFC alone.
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

func sshPub(t testing.TB, k ed25519.PrivateKey) ssh.PublicKey {
	t.Helper()

	pub, err := ssh.NewPublicKey(k.Public())
	if err != nil {
		t.Fatal(err)
	}

	return pub
}

func readTestdata(t testing.TB, name string) []byte {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// TestSignMatchesSSHKeygen pins Sign to the bytes ssh-keygen -Y sign
// wrote for the same key, namespace, and message, and Verify to reading
// them.
func TestSignMatchesSSHKeygen(t *testing.T) {
	t.Parallel()

	msg := readTestdata(t, "sshkeygen/message.txt")
	want := readTestdata(t, "sshkeygen/message.txt.sig")

	pub, err := ParsePublicKey(string(readTestdata(t, "sshkeygen/key.pub")))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(pub.Marshal(), sshPub(t, rfcKey(t, 0)).Marshal()) {
		t.Fatal("the fixture's key is not the key of RFC 8032 test vector 1")
	}

	got, err := Sign(rfcKey(t, 0), "sigchain-test-v1", msg)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, want) {
		t.Errorf("Sign wrote\n%s\nssh-keygen wrote\n%s", got, want)
	}

	if err := Verify(pub, "sigchain-test-v1", msg, want); err != nil {
		t.Errorf("Verify refused the ssh-keygen signature: %v", err)
	}
}

// TestVerifyRefuses refuses a signature over other bytes, under another
// namespace, by another key, or that is not an SSHSIG at all, and wraps
// ErrSignature in each refusal.
func TestVerifyRefuses(t *testing.T) {
	t.Parallel()

	msg := []byte("payload")
	k0, k1 := rfcKey(t, 0), rfcKey(t, 1)

	sig, err := Sign(k0, "ns-v1", msg)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		pub ssh.PublicKey
		ns  string
		msg []byte
		sig []byte
	}{
		"other message":   {sshPub(t, k0), "ns-v1", []byte("payload!"), sig},
		"other namespace": {sshPub(t, k0), "ns-v2", msg, sig},
		"other key":       {sshPub(t, k1), "ns-v1", msg, sig},
		"empty":           {sshPub(t, k0), "ns-v1", msg, nil},
		"no armor":        {sshPub(t, k0), "ns-v1", msg, []byte("U1NIU0lH")},
		"not base64":      {sshPub(t, k0), "ns-v1", msg, []byte(armorBegin + "\n!!!!\n" + armorEnd + "\n")},
		"too long":        {sshPub(t, k0), "ns-v1", msg, bytes.Repeat([]byte("A"), MaxSignature+1)},
		"cut short":       {sshPub(t, k0), "ns-v1", msg, armor([]byte(sigMagic + "\x00\x00\x00\x01\x00\x00"))},
		"nil key":         {nil, "ns-v1", msg, sig},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := Verify(c.pub, c.ns, c.msg, c.sig)
			if !errors.Is(err, ErrSignature) {
				t.Errorf("Verify = %v, want an error that wraps ErrSignature", err)
			}
		})
	}
}

// TestVerifyAcceptsSHA256 reads a signature made with the sha256 hash,
// which ssh-keygen accepts too.
func TestVerifyAcceptsSHA256(t *testing.T) {
	t.Parallel()

	k := rfcKey(t, 2)
	msg := []byte("payload")
	digest := sha256Sum(msg)
	raw := ed25519.Sign(k, signedData("ns-v1", hashSHA256, digest))

	var inner bytes.Buffer
	writeString(&inner, []byte(ssh.KeyAlgoED25519))
	writeString(&inner, raw)

	var blob bytes.Buffer
	blob.WriteString(sigMagic)
	blob.Write([]byte{0, 0, 0, 1})
	writeString(&blob, sshPub(t, k).Marshal())
	writeString(&blob, []byte("ns-v1"))
	writeString(&blob, nil)
	writeString(&blob, []byte(hashSHA256))
	writeString(&blob, inner.Bytes())

	if err := Verify(sshPub(t, k), "ns-v1", msg, armor(blob.Bytes())); err != nil {
		t.Errorf("Verify refused a sha256 signature: %v", err)
	}
}

// TestParsePublicKey takes an Ed25519 key line with or without its
// comment and refuses every other key type and anything after the key.
func TestParsePublicKey(t *testing.T) {
	t.Parallel()

	line := KeyLine(sshPub(t, rfcKey(t, 0)))
	if strings.Contains(line, "\n") || strings.Count(line, " ") != 1 {
		t.Fatalf("KeyLine = %q, want a type and a blob", line)
	}

	for _, ok := range []string{line, line + " root@bastion", line + "\n"} {
		if _, err := ParsePublicKey(ok); err != nil {
			t.Errorf("ParsePublicKey(%q) = %v", ok, err)
		}
	}

	rsa := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQC7"
	for _, bad := range []string{"", "ssh-ed25519", rsa, line + "\n" + line} {
		if _, err := ParsePublicKey(bad); err == nil {
			t.Errorf("ParsePublicKey(%q) took it", bad)
		}
	}
}

func TestSignRefusesABadKeyOrNamespace(t *testing.T) {
	t.Parallel()

	if _, err := Sign(ed25519.PrivateKey{1, 2}, "ns", nil); err == nil {
		t.Error("Sign took a short key")
	}

	if _, err := Sign(rfcKey(t, 0), "", nil); err == nil {
		t.Error("Sign took an empty namespace")
	}
}

func sha256Sum(b []byte) []byte {
	d := sha256.Sum256(b)

	return d[:]
}
