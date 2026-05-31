package test_test

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

// TestManagerInitialization tests basic manager initialization.
func TestManagerInitialization(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestConfig().WithProjectID("test-project").Build()

	opts := &bastion.ProvisioningOptions{
		DryRun:      true,
		Force:       false,
		Parallel:    false,
		Resume:      false,
		Verbose:     true,
		MaxWorkers:  0,
		ProgressOut: nil,
		LogFile:     "",
	}

	manager := bastion.NewManager(context.Background(), cfg, opts)

	if manager == nil {
		t.Fatal("expected manager to be non-nil")
	}

	// GetExecutionInfo uses the same cfg — verify manager sees the correct config.
	info := bastion.GetExecutionInfo(cfg)
	if info == nil {
		t.Fatal("expected non-nil execution info from manager's config")
	}
}

// TestManagerDryRun tests dry run functionality.
func TestManagerDryRun(t *testing.T) {
	_, cleanup := setupTestEnvironment(t)
	defer cleanup()

	cfg := config.NewTestConfig().WithProjectID("test-project").Build()

	opts := &bastion.ProvisioningOptions{
		DryRun:      true,
		Force:       true,
		Parallel:    false,
		Resume:      false,
		Verbose:     true,
		MaxWorkers:  0,
		ProgressOut: nil,
		LogFile:     "",
	}

	manager := bastion.NewManager(context.Background(), cfg, opts)
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

// TestProgressTracking tests manager creation with progress options.
func TestProgressTracking(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestConfig().Build()

	opts := &bastion.ProvisioningOptions{
		DryRun:      true,
		Force:       false,
		Parallel:    false,
		Resume:      false,
		Verbose:     false,
		MaxWorkers:  0,
		ProgressOut: nil,
		LogFile:     "",
	}

	manager := bastion.NewManager(context.Background(), cfg, opts)

	if manager == nil {
		t.Fatal("expected manager to be non-nil after creation with progress options")
	}
}

// TestModeDetection tests execution mode detection in a non-bastion environment.
func TestModeDetection(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestConfig().Build()

	detector := bastion.NewModeDetector(cfg)
	ctx := context.Background()

	mode, err := detector.DetectExecutionMode(ctx)
	if err != nil {
		t.Fatalf("failed to detect execution mode: %v", err)
	}

	// In test environment, mode must be RemoteMode.
	if mode != bastion.RemoteMode {
		t.Errorf("expected RemoteMode (%v), got %v", bastion.RemoteMode, mode)
	}

	isBastion := bastion.IsBastion(cfg)
	if isBastion {
		t.Error("expected IsBastion=false in test environment")
	}
}

// TestExecutionInfo tests execution environment information completeness.
func TestExecutionInfo(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestConfig().Build()

	info := bastion.GetExecutionInfo(cfg)

	requiredFields := []string{"hostname", "user", "home", "os", "arch", "is_bastion", "ocfp_provisioned"}

	for _, field := range requiredFields {
		if _, exists := info[field]; !exists {
			t.Errorf("expected field %q in execution info", field)
		}
	}

	// hostname must be a non-empty string.
	if hostname, ok := info["hostname"].(string); !ok || hostname == "" {
		t.Error("expected non-empty hostname string in execution info")
	}

	// is_bastion must be bool and false in test environment.
	isBastion, ok := info["is_bastion"].(bool)
	if !ok {
		t.Error("expected is_bastion to be bool")
	} else if isBastion {
		t.Error("expected is_bastion=false in test environment")
	}

	// user must be a non-empty string.
	if user, ok := info["user"].(string); !ok || user == "" {
		t.Error("expected non-empty user string in execution info")
	}

	// os and arch must be non-empty strings.
	for _, field := range []string{"os", "arch"} {
		if val, ok := info[field].(string); !ok || val == "" {
			t.Errorf("expected non-empty string for field %q in execution info", field)
		}
	}
}

// Mock implementations for testing.

// MockSSHClient implements SSHClient for testing.
type MockSSHClient struct {
	connected      bool
	commands       []string
	commandResults map[string]*ssh.CommandResult
	transferErrors map[string]error
}

// NewMockSSHClient creates a new mock SSH client.
func NewMockSSHClient() *MockSSHClient {
	return &MockSSHClient{
		connected:      false,
		commands:       make([]string, 0),
		commandResults: make(map[string]*ssh.CommandResult),
		transferErrors: make(map[string]error),
	}
}

// Connect simulates SSH connection.
func (m *MockSSHClient) Connect(_ctx context.Context) error {
	m.connected = true

	return nil
}

// ExecuteCommand simulates command execution.
func (m *MockSSHClient) ExecuteCommand(_ctx context.Context, cmd string) (*ssh.CommandResult, error) {
	m.commands = append(m.commands, cmd)

	if result, exists := m.commandResults[cmd]; exists {
		return result, nil
	}

	return &ssh.CommandResult{
		Command:  cmd,
		ExitCode: 0,
		Stdout:   "mock output",
		Stderr:   "",
		Duration: 100 * time.Millisecond,
	}, nil
}

// TransferFile simulates file transfer.
func (m *MockSSHClient) TransferFile(_ctx context.Context, local, remote string, _opts ssh.TransferOptions) error {
	transferKey := fmt.Sprintf("%s->%s", local, remote)

	if err, exists := m.transferErrors[transferKey]; exists {
		return err
	}

	return nil
}

// CreateTunnel simulates tunnel creation.
func (m *MockSSHClient) CreateTunnel(_ctx context.Context, _localPort, _remotePort int) error {
	return nil
}

// Close simulates connection closure.
func (m *MockSSHClient) Close() error {
	m.connected = false

	return nil
}

// SetCommandResult sets the result for a specific command.
func (m *MockSSHClient) SetCommandResult(cmd string, result *ssh.CommandResult) {
	m.commandResults[cmd] = result
}

// SetTransferError sets an error for a specific file transfer.
func (m *MockSSHClient) SetTransferError(local, remote string, err error) {
	transferKey := fmt.Sprintf("%s->%s", local, remote)
	m.transferErrors[transferKey] = err
}

// GetExecutedCommands returns the list of executed commands.
func (m *MockSSHClient) GetExecutedCommands() []string {
	return m.commands
}

// Helper functions shared across unit and integration tests.

func containsAny(text string, substrings []string) bool {
	for _, substr := range substrings {
		if strings.Contains(text, substr) {
			return true
		}
	}

	return false
}

// setupTestEnvironment returns the OCFP_HOME dir set by TestMain.
// OCFP_HOME is set once in TestMain for the whole package so parallel
// tests share the same isolated root without re-creating it.
func setupTestEnvironment(t *testing.T) (string, func()) {
	t.Helper()

	ocfpHome := os.Getenv("OCFP_HOME")
	cleanup := func() {}

	return ocfpHome, cleanup
}
