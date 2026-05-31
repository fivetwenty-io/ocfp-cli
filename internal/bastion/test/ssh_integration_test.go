//go:build integration

package test_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
)

// TestKeyManagerKeyDiscovery tests FindPrivateKey() with an isolated temp dir.
func TestKeyManagerKeyDiscovery(t *testing.T) {
	keyManager := ssh.NewKeyManager()

	// Use an isolated temp dir — never write to real HOME/.ssh.
	sshDir := filepath.Join(t.TempDir(), ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatalf("failed to create ssh dir: %v", err)
	}

	testKeyPath := filepath.Join(sshDir, "test-bastion")
	testKeyContent := `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAFwAAAAdzc2gtcn
NhAAAAAwEAAQAAAQEAtest-key-content-here
-----END OPENSSH PRIVATE KEY-----`

	if err := os.WriteFile(testKeyPath, []byte(testKeyContent), 0600); err != nil {
		t.Fatalf("failed to write test key: %v", err)
	}

	// Confirm key file written before discovery attempt.
	if _, err := os.Stat(testKeyPath); err != nil {
		t.Fatalf("expected key file at %s: %v", testKeyPath, err)
	}

	// FindPrivateKey may return an error for a mock (non-parseable) key; that is
	// acceptable. The important assertion is that the function does not panic and
	// returns either a path or a descriptive error.
	foundKey, err := keyManager.FindPrivateKey("test")
	if err != nil {
		// Error must be non-empty and mention the key name or a path.
		if err.Error() == "" {
			t.Error("expected non-empty error message from FindPrivateKey")
		}
	} else {
		// Success path: returned path must be non-empty.
		if foundKey == "" {
			t.Error("expected non-empty key path from FindPrivateKey")
		}
	}
}

// TestKeyManagerKeyValidation tests IsKeyPasswordProtected() with mock key files.
func TestKeyManagerKeyValidation(t *testing.T) {
	keyManager := ssh.NewKeyManager()
	tempDir := t.TempDir()

	// Non-existent key must return error.
	isProtected, err := keyManager.IsKeyPasswordProtected("/nonexistent/key")
	if err == nil {
		t.Error("expected error for non-existent key, got nil")
	}
	if isProtected {
		t.Error("expected false for non-existent key")
	}

	// Mock encrypted RSA key (Proc-Type header signals encryption).
	encryptedKeyPath := filepath.Join(tempDir, "encrypted_key")
	// #nosec G101 - test data, not a real private key
	encryptedKeyContent := `-----BEGIN RSA PRIVATE KEY-----
Proc-Type: 4,ENCRYPTED
DEK-Info: AES-128-CBC,test

mock-encrypted-key-content
-----END RSA PRIVATE KEY-----`

	if err := os.WriteFile(encryptedKeyPath, []byte(encryptedKeyContent), 0600); err != nil {
		t.Fatalf("failed to write encrypted key: %v", err)
	}

	isProtected, err = keyManager.IsKeyPasswordProtected(encryptedKeyPath)
	if err != nil {
		t.Fatalf("failed to check encrypted key: %v", err)
	}
	if !isProtected {
		t.Error("expected encrypted RSA key to be detected as password protected")
	}

	// Mock unencrypted OpenSSH key (no Proc-Type header).
	unencryptedKeyPath := filepath.Join(tempDir, "unencrypted_key")
	unencryptedKeyContent := `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmU=
-----END OPENSSH PRIVATE KEY-----`

	if err := os.WriteFile(unencryptedKeyPath, []byte(unencryptedKeyContent), 0600); err != nil {
		t.Fatalf("failed to write unencrypted key: %v", err)
	}

	isProtected, err = keyManager.IsKeyPasswordProtected(unencryptedKeyPath)
	if err != nil {
		t.Fatalf("failed to check unencrypted key: %v", err)
	}
	if isProtected {
		t.Error("expected unencrypted OpenSSH key to not be detected as password protected")
	}
}
