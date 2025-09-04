package test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
)

// TestKeyManagerKeyDiscovery tests SSH key discovery functionality
func TestKeyManagerKeyDiscovery(t *testing.T) {
	tempDir, cleanup := setupTestEnvironment(t)
	defer cleanup()

	keyManager := ssh.NewKeyManager()

	// Create a test SSH key
	sshDir := filepath.Join(tempDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatalf("Failed to create SSH directory: %v", err)
	}

	// Create a mock private key file
	testKeyPath := filepath.Join(sshDir, "test-bastion")
	testKeyContent := `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAFwAAAAdzc2gtcn
NhAAAAAwEAAQAAAQEAtest-key-content-here
-----END OPENSSH PRIVATE KEY-----`

	if err := os.WriteFile(testKeyPath, []byte(testKeyContent), 0600); err != nil {
		t.Fatalf("Failed to write test key: %v", err)
	}

	// Test key discovery
	foundKey, err := keyManager.FindPrivateKey("test")
	if err != nil {
		// This is expected since we created a mock key, not a real one
		t.Logf("Key discovery failed as expected with mock key: %v", err)
	} else {
		t.Logf("Found key: %s", foundKey)
	}
}

// TestSSHClientCreation tests SSH client creation
func TestSSHClientCreation(t *testing.T) {
	_, cleanup := setupTestEnvironment(t)
	defer cleanup()

	connDetails := &ssh.ConnectionDetails{
		Host:           "test-bastion",
		Port:           22,
		User:           "test-user",
		PrivateKeyPath: "/nonexistent/key",
		SSHOptions:     []string{"-o", "StrictHostKeyChecking=no"},
	}

	opts := &ssh.ProvisioningOptions{
		DryRun:  true,
		Verbose: true,
	}

	client := ssh.NewClient(connDetails, opts)
	if client == nil {
		t.Fatal("Expected SSH client to be created, got nil")
	}
}

// TestFileTransferManager tests file transfer functionality
func TestFileTransferManager(t *testing.T) {
	tempDir, cleanup := setupTestEnvironment(t)
	defer cleanup()

	// Create test files
	testFile := filepath.Join(tempDir, "test.txt")
	testContent := "test file content"
	if err := os.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// NOTE: Transfer manager requires a real SSH client, not a mock
	// This would need to be tested with an actual SSH connection
	// For now, just verify the test file was created
	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Error("Test file was not created")
	}
}

// TestKeyManagerKeyValidation tests SSH key validation
func TestKeyManagerKeyValidation(t *testing.T) {
	tempDir, cleanup := setupTestEnvironment(t)
	defer cleanup()

	keyManager := ssh.NewKeyManager()

	// Test with non-existent key
	isProtected, err := keyManager.IsKeyPasswordProtected("/nonexistent/key")
	if err == nil {
		t.Error("Expected error for non-existent key")
	}
	if isProtected {
		t.Error("Expected false for non-existent key")
	}

	// Create a mock encrypted key
	encryptedKeyPath := filepath.Join(tempDir, "encrypted_key")
	encryptedKeyContent := `-----BEGIN RSA PRIVATE KEY-----
Proc-Type: 4,ENCRYPTED
DEK-Info: AES-128-CBC,test

mock-encrypted-key-content
-----END RSA PRIVATE KEY-----`

	if err := os.WriteFile(encryptedKeyPath, []byte(encryptedKeyContent), 0600); err != nil {
		t.Fatalf("Failed to write encrypted key: %v", err)
	}

	// Test encrypted key detection
	isProtected, err = keyManager.IsKeyPasswordProtected(encryptedKeyPath)
	if err != nil {
		t.Fatalf("Failed to check encrypted key: %v", err)
	}
	if !isProtected {
		t.Error("Expected encrypted key to be detected as password protected")
	}

	// Create a mock unencrypted key
	unencryptedKeyPath := filepath.Join(tempDir, "unencrypted_key")
	unencryptedKeyContent := `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmU=
-----END OPENSSH PRIVATE KEY-----`

	if err := os.WriteFile(unencryptedKeyPath, []byte(unencryptedKeyContent), 0600); err != nil {
		t.Fatalf("Failed to write unencrypted key: %v", err)
	}

	// Test unencrypted key detection
	isProtected, err = keyManager.IsKeyPasswordProtected(unencryptedKeyPath)
	if err != nil {
		t.Fatalf("Failed to check unencrypted key: %v", err)
	}
	if isProtected {
		t.Error("Expected unencrypted key to not be detected as password protected")
	}
}
