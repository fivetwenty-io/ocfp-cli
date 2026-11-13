package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"

	xssh "golang.org/x/crypto/ssh"
)

// openSSHEd25519 returns a new ed25519 key in OpenSSH format, encrypted
// with passphrase when it isn't empty.
func openSSHEd25519(t *testing.T, passphrase string) []byte {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	var block *pem.Block
	if passphrase == "" {
		block, err = xssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = xssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}

	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	return pem.EncodeToMemory(block)
}

// rsaPKCS1 returns a new unencrypted RSA key in PKCS#1 PEM format.
func rsaPKCS1(t *testing.T) []byte {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func TestIsKeyDataPasswordProtected(t *testing.T) {
	t.Parallel()

	km := NewKeyManager()

	for _, tc := range []struct {
		name string
		key  []byte
		want bool
	}{
		{"OpenSSH ed25519 with a passphrase", openSSHEd25519(t, "sesame"), true},
		{"OpenSSH ed25519 without a passphrase", openSSHEd25519(t, ""), false},
		{"legacy PEM with an encrypted Proc-Type", pem.EncodeToMemory(&pem.Block{
			Type:    "RSA PRIVATE KEY",
			Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-128-CBC,00112233445566778899AABBCCDDEEFF"},
			Bytes:   []byte("not a key until it is decrypted"),
		}), true},
		{"encrypted PKCS#8", pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte{0x30, 0x00}}), true},
		{"unencrypted PKCS#1 RSA", rsaPKCS1(t), false},
		{"not a key", []byte("hello"), false},
		{"empty", nil, false},
	} {
		if got := km.IsKeyDataPasswordProtected(tc.key); got != tc.want {
			t.Errorf("%s: IsKeyDataPasswordProtected = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsKeyPasswordProtectedReadsAnOpenSSHKeyFile(t *testing.T) {
	t.Parallel()

	km := NewKeyManager()
	dir := t.TempDir()

	for _, tc := range []struct {
		file, passphrase string
		want             bool
	}{
		{"id_ed25519_encrypted", "sesame", true},
		{"id_ed25519_plain", "", false},
	} {
		path := filepath.Join(dir, tc.file)
		if err := os.WriteFile(path, openSSHEd25519(t, tc.passphrase), 0o600); err != nil {
			t.Fatalf("write key: %v", err)
		}

		got, err := km.IsKeyPasswordProtected(path)
		if err != nil {
			t.Fatalf("IsKeyPasswordProtected(%s): %v", path, err)
		}

		if got != tc.want {
			t.Errorf("IsKeyPasswordProtected(%s) = %v, want %v", path, got, tc.want)
		}
	}
}

// An encrypted OpenSSH key without a passphrase is refused as encrypted,
// and the right passphrase opens it.
func TestCreatePublicKeyAuthWithAnEncryptedOpenSSHKey(t *testing.T) {
	t.Parallel()

	km := NewKeyManager()
	path := filepath.Join(t.TempDir(), "id_ed25519")

	if err := os.WriteFile(path, openSSHEd25519(t, "sesame"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	if _, err := km.CreatePublicKeyAuth(path, ""); !errors.Is(err, ErrKeyEncryptedNoPassphrase) {
		t.Errorf("no passphrase: err = %v, want ErrKeyEncryptedNoPassphrase", err)
	}

	if _, err := km.CreatePublicKeyAuth(path, "sesame"); err != nil {
		t.Errorf("right passphrase: %v", err)
	}

	if _, err := km.CreatePublicKeyAuth(path, "wrong"); err == nil {
		t.Error("a wrong passphrase must fail")
	}
}
