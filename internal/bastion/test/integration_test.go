package test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bastion"
	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// TestFullDryRunIntegration tests complete dry run integration
func TestFullDryRunIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	_, cleanup := setupTestEnvironment(t)
	defer cleanup()

	cfg := &config.Config{
		Name:      "test-bloc",
		Provider:  "stackit",
		ProjectID: "test-project-id",
		Region:    "eu01",
		BastionIP: "192.168.1.100", // Mock IP
		Bastion: config.Bastion{
			SSHUser: "ubuntu",
			Git: config.GitConfig{
				User: config.GitUser{
					Name:  "Test User",
					Email: "test@example.com",
				},
			},
		},
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

// TestCheckpointFunctionality tests checkpoint save/load functionality
func TestCheckpointFunctionality(t *testing.T) {
	_, cleanup := setupTestEnvironment(t)
	defer cleanup()

	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
	}

	checkpointMgr := bastion.NewCheckpointManager(cfg)

	// Test with no existing checkpoint
	checkpoint, err := checkpointMgr.Load()
	if err != nil {
		t.Fatalf("Failed to load checkpoint: %v", err)
	}
	if checkpoint != nil {
		t.Error("Expected no checkpoint initially")
	}

	// Create and save progress
	progress := &bastion.ProvisioningProgress{
		TotalSteps:     10,
		CompletedSteps: 5,
		CurrentStep:    "test_phase",
		StartTime:      time.Now().Add(-30 * time.Minute),
		Checkpoints: map[string]bool{
			"phase1": true,
			"phase2": true,
			"phase3": true,
		},
	}

	metadata := map[string]interface{}{
		"test": true,
		"mode": "test",
	}

	// Save checkpoint
	if err := checkpointMgr.Save(progress, metadata); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	// Load checkpoint
	loadedCheckpoint, err := checkpointMgr.Load()
	if err != nil {
		t.Fatalf("Failed to load saved checkpoint: %v", err)
	}
	if loadedCheckpoint == nil {
		t.Fatal("Expected loaded checkpoint, got nil")
	}

	// Verify checkpoint data
	if loadedCheckpoint.BlocName != cfg.Name {
		t.Errorf("Expected bloc name %s, got %s", cfg.Name, loadedCheckpoint.BlocName)
	}

	if loadedCheckpoint.Provider != cfg.Provider {
		t.Errorf("Expected provider %s, got %s", cfg.Provider, loadedCheckpoint.Provider)
	}

	if loadedCheckpoint.CompletedSteps != progress.CompletedSteps {
		t.Errorf("Expected %d completed steps, got %d",
			progress.CompletedSteps, loadedCheckpoint.CompletedSteps)
	}

	if len(loadedCheckpoint.CompletedPhases) != len(progress.Checkpoints) {
		t.Errorf("Expected %d completed phases, got %d",
			len(progress.Checkpoints), len(loadedCheckpoint.CompletedPhases))
	}

	// Test restoration
	restoredProgress := checkpointMgr.RestoreProgress(loadedCheckpoint)
	if restoredProgress.CompletedSteps != progress.CompletedSteps {
		t.Errorf("Expected restored progress to have %d completed steps, got %d",
			progress.CompletedSteps, restoredProgress.CompletedSteps)
	}

	// Test clearing checkpoint
	if err := checkpointMgr.Clear(); err != nil {
		t.Fatalf("Failed to clear checkpoint: %v", err)
	}

	// Verify checkpoint is gone
	clearedCheckpoint, err := checkpointMgr.Load()
	if err != nil {
		t.Fatalf("Failed to load after clear: %v", err)
	}
	if clearedCheckpoint != nil {
		t.Error("Expected no checkpoint after clear")
	}
}

// TestErrorClassification tests error classification and retry logic
func TestErrorClassification(t *testing.T) {
	errorHandler := bastion.NewErrorHandler()

	testCases := []struct {
		name         string
		error        string
		expectedType bastion.ErrorType
		retryable    bool
	}{
		{
			name:         "network error",
			error:        "connection refused",
			expectedType: bastion.ErrorTypeNetwork,
			retryable:    true,
		},
		{
			name:         "permission error",
			error:        "permission denied",
			expectedType: bastion.ErrorTypePermission,
			retryable:    false,
		},
		{
			name:         "configuration error",
			error:        "config file not found",
			expectedType: bastion.ErrorTypeConfiguration,
			retryable:    false,
		},
		{
			name:         "dependency error",
			error:        "command not found: bosh",
			expectedType: bastion.ErrorTypeDependency,
			retryable:    true,
		},
		{
			name:         "timeout error",
			error:        "context deadline exceeded",
			expectedType: bastion.ErrorTypeTimeout,
			retryable:    true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := fmt.Errorf("%s", testCase.error)
			bastionErr := errorHandler.ClassifyError(err, "test_phase", "test_command")

			if bastionErr.Type != testCase.expectedType {
				t.Errorf("Expected error type %s, got %s", testCase.expectedType, bastionErr.Type)
			}

			if bastionErr.Retryable != testCase.retryable {
				t.Errorf("Expected retryable %t, got %t", testCase.retryable, bastionErr.Retryable)
			}

			if len(bastionErr.Suggestions) == 0 {
				t.Error("Expected error suggestions to be provided")
			}
		})
	}
}

// TestProgressReporting tests progress reporting functionality
func TestProgressReporting(t *testing.T) {
	var output bytes.Buffer

	progress := &bastion.ProvisioningProgress{
		TotalSteps:     10,
		CompletedSteps: 3,
		CurrentStep:    "test_phase",
		StartTime:      time.Now().Add(-2 * time.Minute),
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

// TestStatusReporting tests status reporting functionality
func TestStatusReporting(t *testing.T) {
	_, cleanup := setupTestEnvironment(t)
	defer cleanup()

	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
	}

	checkpointMgr := bastion.NewCheckpointManager(cfg)
	statusReporter := bastion.NewStatusReporter(checkpointMgr)

	// Test status with no checkpoint
	status, err := statusReporter.GetCurrentStatus()
	if err != nil {
		t.Fatalf("Failed to get current status: %v", err)
	}

	if status["completed"].(bool) {
		t.Error("Expected status to show not completed")
	}

	if status["progress"].(float64) != 0.0 {
		t.Error("Expected 0% progress with no checkpoint")
	}

	// Test status printing
	var output bytes.Buffer
	if err := statusReporter.PrintStatus(&output); err != nil {
		t.Fatalf("Failed to print status: %v", err)
	}

	statusOutput := output.String()
	if !strings.Contains(statusOutput, "Bastion Initialization Status") {
		t.Error("Expected status header in output")
	}
}
