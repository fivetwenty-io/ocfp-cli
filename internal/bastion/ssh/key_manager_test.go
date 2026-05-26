package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	xssh "golang.org/x/crypto/ssh"
)

// writeTestEd25519Key generates an OpenSSH-format ed25519 private key at path.
func writeTestEd25519Key(t *testing.T, path string) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	block, err := xssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
}

// FindPrivateKey must discover the shared OCFP fleet bastion key at
// ~/.ssh/ocfp-bastions when no bloc-specific key exists. This is the
// IdentityFile the ocfp bastions Host block uses, and PVE blocs rely on it.
func TestFindPrivateKeyFindsSharedOCFPBastionsKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatalf("mkdir .ssh: %v", err)
	}

	keyPath := filepath.Join(sshDir, "ocfp-bastions")
	writeTestEd25519Key(t, keyPath)

	got, err := NewKeyManager().FindPrivateKey("ocfp-lab-wayne")
	if err != nil {
		t.Fatalf("FindPrivateKey returned error: %v", err)
	}

	if got != keyPath {
		t.Errorf("expected %q, got %q", keyPath, got)
	}
}
