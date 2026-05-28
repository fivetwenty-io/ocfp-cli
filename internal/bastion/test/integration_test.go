//go:build integration

package test_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bastion"
	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// TestFullDryRunIntegration tests complete dry run integration.
func TestFullDryRunIntegration(t *testing.T) {
	_, cleanup := setupTestEnvironment(t)
	defer cleanup()

	// Isolate HOME so SSH key writes do not touch the real ~/.ssh directory.
	isolatedHome := t.TempDir()
	t.Setenv("HOME", isolatedHome)

	cfg := config.NewTestConfig().
		WithRegion("eu01").
		WithProjectID("test-project-id").
		WithBastionIP("192.168.1.100").
		WithBastion(config.TestBastionConfig()).
		Build()

	sshDir := filepath.Join(isolatedHome, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatalf("failed to create ssh dir: %v", err)
	}

	keyPath := filepath.Join(sshDir, cfg.Name+"-bastion")
	keyManager := ssh.NewKeyManager()
	if err := keyManager.GenerateKeyPair(keyPath, "ed25519", 0); err != nil {
		t.Fatalf("failed to generate mock ssh key: %v", err)
	}

	// Confirm key was written to isolated HOME, not real one.
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("expected key at %s, got: %v", keyPath, err)
	}

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

	err := bastion.InitializeBastionWithMode(context.Background(), cfg, opts)
	if err != nil {
		t.Fatalf("expected dry run to succeed, got: %v", err)
	}

	outputStr := output.String()
	if outputStr == "" {
		t.Error("expected progress output, got empty string")
	}

	// Dry-run mode must be confirmed in output.
	if !strings.Contains(strings.ToLower(outputStr), "dry") {
		t.Errorf("expected dry-run indicator in output, got: %s", outputStr)
	}

	// At least one phase name must appear — phase names are lower-snake identifiers.
	knownPhases := []string{"environment", "packages", "tools", "genesis", "config", "provision"}
	foundPhase := false
	for _, phase := range knownPhases {
		if strings.Contains(strings.ToLower(outputStr), phase) {
			foundPhase = true
			break
		}
	}
	if !foundPhase {
		t.Errorf("expected at least one phase name in output, got: %s", outputStr)
	}
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
		t.Fatalf("failed to load checkpoint: %v", err)
	}

	if checkpoint != nil {
		t.Error("expected no checkpoint initially")
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
		t.Fatalf("failed to save checkpoint: %v", err)
	}

	loadedCheckpoint, err := checkpointMgr.Load()
	if err != nil {
		t.Fatalf("failed to load saved checkpoint: %v", err)
	}

	if loadedCheckpoint == nil {
		t.Fatal("expected loaded checkpoint, got nil")
	}

	verifyCheckpointData(t, loadedCheckpoint, progress, cfg)

	// Verify checkpoint file exists in OCFP_HOME.
	ocfpHome := os.Getenv("OCFP_HOME")
	if ocfpHome != "" {
		checkpointDir := filepath.Join(ocfpHome, "checkpoints")
		entries, err := os.ReadDir(checkpointDir)
		if err != nil {
			t.Fatalf("expected checkpoint dir at %s: %v", checkpointDir, err)
		}
		if len(entries) == 0 {
			t.Error("expected at least one checkpoint file in OCFP_HOME/checkpoints")
		}
	}
}

func verifyCheckpointData(t *testing.T, checkpoint *bastion.CheckpointData, progress *bastion.ProvisioningProgress, cfg *config.Config) {
	t.Helper()

	if checkpoint.BlocName != cfg.Name {
		t.Errorf("expected bloc name %q, got %q", cfg.Name, checkpoint.BlocName)
	}

	if checkpoint.Provider != cfg.Provider {
		t.Errorf("expected provider %q, got %q", cfg.Provider, checkpoint.Provider)
	}

	if checkpoint.CompletedSteps != progress.CompletedSteps {
		t.Errorf("expected %d completed steps, got %d", progress.CompletedSteps, checkpoint.CompletedSteps)
	}

	if len(checkpoint.CompletedPhases) != len(progress.Checkpoints) {
		t.Errorf("expected %d completed phases, got %d", len(progress.Checkpoints), len(checkpoint.CompletedPhases))
	}

	// Verify all checkpoint phase names round-trip correctly.
	for phase := range progress.Checkpoints {
		if _, ok := checkpoint.CompletedPhases[phase]; !ok {
			t.Errorf("checkpoint phase %q not found after round-trip", phase)
		}
	}
}

func testCheckpointRestoration(t *testing.T, checkpointMgr *bastion.CheckpointManager, originalProgress *bastion.ProvisioningProgress) {
	t.Helper()

	loadedCheckpoint, err := checkpointMgr.Load()
	if err != nil {
		t.Fatalf("failed to load checkpoint for restoration test: %v", err)
	}

	restoredProgress := checkpointMgr.RestoreProgress(loadedCheckpoint)

	if restoredProgress.CompletedSteps != originalProgress.CompletedSteps {
		t.Errorf("expected restored progress to have %d completed steps, got %d",
			originalProgress.CompletedSteps, restoredProgress.CompletedSteps)
	}

	if restoredProgress.TotalSteps != originalProgress.TotalSteps {
		t.Errorf("expected restored total steps %d, got %d",
			originalProgress.TotalSteps, restoredProgress.TotalSteps)
	}
}

func testCheckpointClearing(t *testing.T, checkpointMgr *bastion.CheckpointManager) {
	t.Helper()

	err := checkpointMgr.Clear()
	if err != nil {
		t.Fatalf("failed to clear checkpoint: %v", err)
	}

	clearedCheckpoint, err := checkpointMgr.Load()
	if err != nil {
		t.Fatalf("failed to load after clear: %v", err)
	}

	if clearedCheckpoint != nil {
		t.Error("expected no checkpoint after clear")
	}
}

// TestStatusReporting tests status reporting functionality.
func TestStatusReporting(t *testing.T) {
	_, cleanup := setupTestEnvironment(t)
	defer cleanup()

	cfg := config.NewTestConfig().Build()

	checkpointMgr := bastion.NewCheckpointManager(cfg)
	statusReporter := bastion.NewStatusReporter(checkpointMgr)

	status, err := statusReporter.GetCurrentStatus()
	if err != nil {
		t.Fatalf("failed to get current status: %v", err)
	}

	if status == nil {
		t.Fatal("expected non-nil status map")
	}

	// With no checkpoint, completed must be false.
	completed, ok := status["completed"].(bool)
	if !ok {
		t.Error("expected status[\"completed\"] to be bool")
	} else if completed {
		t.Error("expected status to show not completed")
	}

	// With no checkpoint, progress must be 0.
	prog, ok := status["progress"].(float64)
	if !ok {
		t.Error("expected status[\"progress\"] to be float64")
	} else if prog != 0.0 {
		t.Errorf("expected 0%% progress with no checkpoint, got %.2f", prog)
	}

	var output bytes.Buffer

	err = statusReporter.PrintStatus(&output)
	if err != nil {
		t.Fatalf("failed to print status: %v", err)
	}

	statusOutput := output.String()
	if statusOutput == "" {
		t.Error("expected non-empty status output")
	}
	if !strings.Contains(statusOutput, "Bastion Initialization Status") {
		t.Errorf("expected status header in output, got: %s", statusOutput)
	}
}
