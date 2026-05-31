package test_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
)

// TestSSHClientCreation tests SSH client creation (pure instantiation, no connection).
func TestSSHClientCreation(t *testing.T) {
	t.Parallel()

	connDetails := &ssh.ConnectionDetails{
		Host:           "test-bastion",
		Port:           22,
		User:           "test-user",
		Password:       "",
		PrivateKeyPath: "/nonexistent/key",
		UseSSHPass:     false,
		SSHOptions:     []string{"-o", "StrictHostKeyChecking=no"},
	}

	opts := &ssh.ProvisioningOptions{
		DryRun:      true,
		Force:       false,
		Parallel:    false,
		Resume:      false,
		Verbose:     true,
		MaxWorkers:  0,
		ProgressOut: nil,
		LogFile:     "",
	}

	client := ssh.NewClient(connDetails, opts)
	if client == nil {
		t.Fatal("expected SSH client to be non-nil")
	}
}

// TestFileTransferManager verifies file creation as a precondition for transfer.
// Real transfer requires a live SSH session and is not tested here.
func TestFileTransferManager(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")
	testContent := "test file content"

	if err := os.WriteFile(testFile, []byte(testContent), 0600); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	info, err := os.Stat(testFile)
	if err != nil {
		t.Fatalf("expected test file at %s: %v", testFile, err)
	}

	if info.Size() != int64(len(testContent)) {
		t.Errorf("expected file size %d, got %d", len(testContent), info.Size())
	}
}
