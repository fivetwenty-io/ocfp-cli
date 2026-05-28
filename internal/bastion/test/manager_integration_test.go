//go:build integration

package test_test

import (
	"context"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bastion"
	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// TestManagerDryRun tests that Initialize() in dry-run mode fails at the
// connection stage (no real bastion), not at the configuration stage.
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

	manager := bastion.NewManager(cfg, opts)
	ctx := context.Background()

	err := manager.Initialize(ctx)

	// Without real connection details, Initialize must fail.
	if err == nil {
		t.Error("expected dry run with no real bastion to return an error, got nil")
	}

	// Error must be connection-related, not a config parsing failure.
	if !containsAny(err.Error(), []string{"connection", "provider", "bastion IP"}) {
		t.Errorf("expected connection-related error, got: %s", err.Error())
	}
}
