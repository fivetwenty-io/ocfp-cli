package test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bastion"
	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// TestManagerInitialization tests basic manager initialization
func TestManagerInitialization(t *testing.T) {
	cfg := &config.Config{
		Name:      "test-bloc",
		Provider:  "stackit",
		ProjectID: "test-project",
	}

	opts := &bastion.ProvisioningOptions{
		DryRun:   true,
		Force:    false,
		Parallel: false,
		Resume:   false,
		Verbose:  true,
	}

	manager := bastion.NewManager(cfg, opts)

	if manager == nil {
		t.Fatal("Expected manager to be created, got nil")
	}

	// Manager successfully created with provided configuration
	// (Can't test internal fields as they're unexported)
}

// TestManagerDryRun tests dry run functionality
func TestManagerDryRun(t *testing.T) {
	cfg := &config.Config{
		Name:      "test-bloc",
		Provider:  "stackit",
		ProjectID: "test-project",
	}

	opts := &bastion.ProvisioningOptions{
		DryRun:   true,
		Force:    true,
		Parallel: false,
		Resume:   false,
		Verbose:  true,
	}

	manager := bastion.NewManager(cfg, opts)
	ctx := context.Background()

	// This should succeed in dry run mode without actual connections
	err := manager.Initialize(ctx)

	// We expect this to fail because we don't have real connection details
	// but it should fail at the connection stage, not before
	if err == nil {
		t.Error("Expected dry run to encounter connection error, got success")
	}

	// Check that the error is related to connection, not configuration
	if !containsAny(err.Error(), []string{"connection", "provider", "bastion IP"}) {
		t.Errorf("Expected connection-related error, got: %s", err.Error())
	}
}

// TestProgressTracking tests progress tracking functionality
func TestProgressTracking(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
	}

	opts := &bastion.ProvisioningOptions{
		DryRun: true,
	}

	manager := bastion.NewManager(cfg, opts)

	// Manager successfully created with tracking options
	// (Can't test internal progress fields as they're unexported)
	if manager == nil {
		t.Fatal("Expected manager to be created, got nil")
	}
}

// TestModeDetection tests execution mode detection
func TestModeDetection(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
	}

	detector := bastion.NewModeDetector(cfg)
	ctx := context.Background()

	mode, err := detector.DetectExecutionMode(ctx)
	if err != nil {
		t.Fatalf("Failed to detect execution mode: %v", err)
	}

	// In test environment, this should be RemoteMode
	expectedMode := bastion.RemoteMode
	if mode != expectedMode {
		t.Errorf("Expected mode %v, got %v", expectedMode, mode)
	}

	// Test bastion detection
	isBastion := bastion.IsBastion(cfg)
	if isBastion {
		t.Error("Expected non-bastion environment in test, got bastion")
	}
}

// TestExecutionInfo tests execution environment information
func TestExecutionInfo(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
	}

	info := bastion.GetExecutionInfo(cfg)

	// Check required fields
	requiredFields := []string{"hostname", "user", "home", "os", "arch", "is_bastion", "ocfp_provisioned"}

	for _, field := range requiredFields {
		if _, exists := info[field]; !exists {
			t.Errorf("Expected field %s in execution info", field)
		}
	}

	// Check field types
	if hostname, ok := info["hostname"].(string); !ok || hostname == "" {
		t.Error("Expected non-empty hostname string")
	}

	if isBastion, ok := info["is_bastion"].(bool); !ok {
		t.Error("Expected is_bastion to be boolean")
	} else if isBastion {
		t.Error("Expected is_bastion to be false in test environment")
	}
}

// Mock implementations for testing

// MockSSHClient implements SSHClient for testing
type MockSSHClient struct {
	connected      bool
	commands       []string
	commandResults map[string]*ssh.CommandResult
	transferErrors map[string]error
}

// NewMockSSHClient creates a new mock SSH client
func NewMockSSHClient() *MockSSHClient {
	return &MockSSHClient{
		commands:       make([]string, 0),
		commandResults: make(map[string]*ssh.CommandResult),
		transferErrors: make(map[string]error),
	}
}

// Connect simulates SSH connection
func (m *MockSSHClient) Connect(ctx context.Context) error {
	m.connected = true
	return nil
}

// ExecuteCommand simulates command execution
func (m *MockSSHClient) ExecuteCommand(ctx context.Context, cmd string) (*ssh.CommandResult, error) {
	m.commands = append(m.commands, cmd)

	if result, exists := m.commandResults[cmd]; exists {
		return result, nil
	}

	// Default successful result
	return &ssh.CommandResult{
		Command:  cmd,
		ExitCode: 0,
		Stdout:   "mock output",
		Stderr:   "",
		Duration: 100 * time.Millisecond,
	}, nil
}

// TransferFile simulates file transfer
func (m *MockSSHClient) TransferFile(ctx context.Context, local, remote string, opts ssh.TransferOptions) error {
	transferKey := fmt.Sprintf("%s->%s", local, remote)

	if err, exists := m.transferErrors[transferKey]; exists {
		return err
	}

	return nil // Success by default
}

// CreateTunnel simulates tunnel creation
func (m *MockSSHClient) CreateTunnel(ctx context.Context, localPort, remotePort int) error {
	return nil
}

// Close simulates connection closure
func (m *MockSSHClient) Close() error {
	m.connected = false
	return nil
}

// SetCommandResult sets the result for a specific command
func (m *MockSSHClient) SetCommandResult(cmd string, result *ssh.CommandResult) {
	m.commandResults[cmd] = result
}

// SetTransferError sets an error for a specific file transfer
func (m *MockSSHClient) SetTransferError(local, remote string, err error) {
	transferKey := fmt.Sprintf("%s->%s", local, remote)
	m.transferErrors[transferKey] = err
}

// GetExecutedCommands returns the list of executed commands
func (m *MockSSHClient) GetExecutedCommands() []string {
	return m.commands
}

// Helper functions

func containsAny(text string, substrings []string) bool {
	for _, substr := range substrings {
		if strings.Contains(text, substr) {
			return true
		}
	}
	return false
}

// setupTestEnvironment creates a test environment
func setupTestEnvironment(t *testing.T) (string, func()) {
	// Create temporary directory for test files
	tempDir, err := os.MkdirTemp("", "ocfp-bastion-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Set HOME environment variable for tests
	oldHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", tempDir)

	cleanup := func() {
		_ = os.Setenv("HOME", oldHome)
		_ = os.RemoveAll(tempDir)
	}

	return tempDir, cleanup
}
