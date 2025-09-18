package test_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bastion"
	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// Static errors for testing error classification.
var (
	errConnectionRefused       = errors.New("connection refused")
	errPermissionDenied        = errors.New("permission denied")
	errConfigFileNotFound      = errors.New("config file not found")
	errCommandNotFoundBosh     = errors.New("command not found: bosh")
	errContextDeadlineExceeded = errors.New("context deadline exceeded")
)

// TestFullDryRunIntegration tests complete dry run integration.
func TestFullDryRunIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	_, cleanup := setupTestEnvironment(t)
	defer cleanup()

	cfg := config.NewTestConfig().
		WithRegion("eu01").
		WithProjectID("test-project-id").
		WithBastionIP("192.168.1.100"). // Mock IP
		WithBastion(config.TestBastionConfig()).
		Build()

	sshDir := filepath.Join(os.Getenv("HOME"), ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatalf("failed to create ssh dir: %v", err)
	}

	keyPath := filepath.Join(sshDir, cfg.Name+"-bastion")
	keyManager := ssh.NewKeyManager()
	if err := keyManager.GenerateKeyPair(keyPath, "ed25519", 0); err != nil {
		t.Fatalf("failed to generate mock ssh key: %v", err)
	}

	// Capture output
	var output bytes.Buffer

	opts := &bastion.ProvisioningOptions{
		DryRun:      true,
		Force:       true,
		Parallel:    false,
		Resume:      false,
		Verbose:     true,
		MaxWorkers:  2,
		ProgressOut: &output,
		LogFile:     "",
	}

	// Test mode-aware initialization
	err := bastion.InitializeBastionWithMode(context.Background(), cfg, opts)

	// Should succeed in dry run mode
	if err != nil {
		t.Fatalf("Expected dry run to succeed, got error: %v", err)
	}

	// Check that output was generated
	outputStr := output.String()
	if outputStr == "" {
		t.Error("Expected progress output, got empty string")
	}

	t.Logf("Dry run output: %s", outputStr)
}

// TestCheckpointFunctionality tests checkpoint save/load functionality.
func TestCheckpointFunctionality(t *testing.T) {
	_, cleanup := setupTestEnvironment(t)
	defer cleanup()

	cfg := config.NewTestConfig().Build()
	checkpointMgr := bastion.NewCheckpointManager(cfg)

	testInitialCheckpointState(t, checkpointMgr)

	progress := createTestProgress()
	metadata := createTestMetadata()
	saveAndVerifyCheckpoint(t, checkpointMgr, progress, metadata, cfg)
	testCheckpointRestoration(t, checkpointMgr, progress)
	testCheckpointClearing(t, checkpointMgr)
}

func testInitialCheckpointState(t *testing.T, checkpointMgr *bastion.CheckpointManager) {
	t.Helper()

	checkpoint, err := checkpointMgr.Load()
	if err != nil {
		t.Fatalf("Failed to load checkpoint: %v", err)
	}

	if checkpoint != nil {
		t.Error("Expected no checkpoint initially")
	}
}

func createTestProgress() *bastion.ProvisioningProgress {
	return &bastion.ProvisioningProgress{
		TotalSteps:     10,
		CompletedSteps: 5,
		CurrentStep:    "test_phase",
		StartTime:      time.Now().Add(-30 * time.Minute),
		Errors:         []error{},
		Checkpoints: map[string]bool{
			"phase1": true,
			"phase2": true,
			"phase3": true,
		},
	}
}

func createTestMetadata() map[string]interface{} {
	return map[string]interface{}{
		"test": true,
		"mode": "test",
	}
}

func saveAndVerifyCheckpoint(t *testing.T, checkpointMgr *bastion.CheckpointManager, progress *bastion.ProvisioningProgress, metadata map[string]interface{}, cfg *config.Config) {
	t.Helper()

	err := checkpointMgr.Save(progress, metadata)
	if err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	loadedCheckpoint, err := checkpointMgr.Load()
	if err != nil {
		t.Fatalf("Failed to load saved checkpoint: %v", err)
	}

	if loadedCheckpoint == nil {
		t.Fatal("Expected loaded checkpoint, got nil")
	}

	verifyCheckpointData(t, loadedCheckpoint, progress, cfg)
}

func verifyCheckpointData(t *testing.T, checkpoint *bastion.CheckpointData, progress *bastion.ProvisioningProgress, cfg *config.Config) {
	t.Helper()

	if checkpoint.BlocName != cfg.Name {
		t.Errorf("Expected bloc name %s, got %s", cfg.Name, checkpoint.BlocName)
	}

	if checkpoint.Provider != cfg.Provider {
		t.Errorf("Expected provider %s, got %s", cfg.Provider, checkpoint.Provider)
	}

	if checkpoint.CompletedSteps != progress.CompletedSteps {
		t.Errorf("Expected %d completed steps, got %d", progress.CompletedSteps, checkpoint.CompletedSteps)
	}

	if len(checkpoint.CompletedPhases) != len(progress.Checkpoints) {
		t.Errorf("Expected %d completed phases, got %d", len(progress.Checkpoints), len(checkpoint.CompletedPhases))
	}
}

func testCheckpointRestoration(t *testing.T, checkpointMgr *bastion.CheckpointManager, originalProgress *bastion.ProvisioningProgress) {
	t.Helper()

	loadedCheckpoint, err := checkpointMgr.Load()
	if err != nil {
		t.Fatalf("Failed to load checkpoint for restoration test: %v", err)
	}

	restoredProgress := checkpointMgr.RestoreProgress(loadedCheckpoint)
	if restoredProgress.CompletedSteps != originalProgress.CompletedSteps {
		t.Errorf("Expected restored progress to have %d completed steps, got %d",
			originalProgress.CompletedSteps, restoredProgress.CompletedSteps)
	}
}

func testCheckpointClearing(t *testing.T, checkpointMgr *bastion.CheckpointManager) {
	t.Helper()

	err := checkpointMgr.Clear()
	if err != nil {
		t.Fatalf("Failed to clear checkpoint: %v", err)
	}

	clearedCheckpoint, err := checkpointMgr.Load()
	if err != nil {
		t.Fatalf("Failed to load after clear: %v", err)
	}

	if clearedCheckpoint != nil {
		t.Error("Expected no checkpoint after clear")
	}
}

// TestErrorClassification tests error classification and retry logic.
func TestErrorClassification(t *testing.T) {
	t.Parallel()

	errorHandler := bastion.NewErrorHandler()
	testCases := getErrorClassificationTestCases()

	for _, testCase := range testCases {
		currentTestCase := testCase
		t.Run(currentTestCase.name, func(t *testing.T) {
			t.Parallel()
			runErrorClassificationTest(t, errorHandler, currentTestCase)
		})
	}
}

type errorTestCase struct {
	name         string
	err          error
	expectedType bastion.ErrorType
	retryable    bool
}

func getErrorClassificationTestCases() []errorTestCase {
	return []errorTestCase{
		{
			name:         "network error",
			err:          errConnectionRefused,
			expectedType: bastion.ErrorTypeNetwork,
			retryable:    true,
		},
		{
			name:         "permission error",
			err:          errPermissionDenied,
			expectedType: bastion.ErrorTypePermission,
			retryable:    false,
		},
		{
			name:         "configuration error",
			err:          errConfigFileNotFound,
			expectedType: bastion.ErrorTypeConfiguration,
			retryable:    false,
		},
		{
			name:         "dependency error",
			err:          errCommandNotFoundBosh,
			expectedType: bastion.ErrorTypeDependency,
			retryable:    true,
		},
		{
			name:         "timeout error",
			err:          errContextDeadlineExceeded,
			expectedType: bastion.ErrorTypeTimeout,
			retryable:    true,
		},
	}
}

func runErrorClassificationTest(t *testing.T, errorHandler *bastion.ErrorHandler, testCase errorTestCase) {
	t.Helper()

	bastionErr := errorHandler.ClassifyError(testCase.err, "test_phase", "test_command")

	if bastionErr.Type != testCase.expectedType {
		t.Errorf("Expected error type %s, got %s", testCase.expectedType, bastionErr.Type)
	}

	if bastionErr.Retryable != testCase.retryable {
		t.Errorf("Expected retryable %t, got %t", testCase.retryable, bastionErr.Retryable)
	}

	if len(bastionErr.Suggestions) == 0 {
		t.Error("Expected error suggestions to be provided")
	}
}

// TestProgressReporting tests progress reporting functionality.
func TestProgressReporting(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer

	progress := &bastion.ProvisioningProgress{
		TotalSteps:     10,
		CompletedSteps: 3,
		CurrentStep:    "test_phase",
		StartTime:      time.Now().Add(-2 * time.Minute),
		Errors:         []error{},
		Checkpoints:    make(map[string]bool),
	}

	reporter := bastion.NewProgressReporter(&output, progress)

	// Test phase reporting
	reporter.ReportPhaseStart("test_phase", 3, 10)

	if output.Len() == 0 {
		t.Error("Expected output from phase start report")
	}

	// Reset output buffer
	output.Reset()

	// Test phase completion
	reporter.ReportPhaseComplete("test_phase", 5*time.Second)

	if output.Len() == 0 {
		t.Error("Expected output from phase completion report")
	}

	outputStr := output.String()
	if !strings.Contains(outputStr, "test_phase") {
		t.Error("Expected phase name in completion output")
	}
}

// TestStatusReporting tests status reporting functionality.
func TestStatusReporting(t *testing.T) {
	t.Parallel()

	_, cleanup := setupTestEnvironment(t)
	defer cleanup()

	cfg := config.NewTestConfig().Build()

	checkpointMgr := bastion.NewCheckpointManager(cfg)
	statusReporter := bastion.NewStatusReporter(checkpointMgr)

	// Test status with no checkpoint
	status, err := statusReporter.GetCurrentStatus()
	if err != nil {
		t.Fatalf("Failed to get current status: %v", err)
	}

	if completed, _ := status["completed"].(bool); completed {
		t.Error("Expected status to show not completed")
	}

	if prog, _ := status["progress"].(float64); prog != 0.0 {
		t.Error("Expected 0% progress with no checkpoint")
	}

	// Test status printing
	var output bytes.Buffer

	err = statusReporter.PrintStatus(&output)
	if err != nil {
		t.Fatalf("Failed to print status: %v", err)
	}

	statusOutput := output.String()
	if !strings.Contains(statusOutput, "Bastion Initialization Status") {
		t.Error("Expected status header in output")
	}
}
