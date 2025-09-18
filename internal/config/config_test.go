package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// TestStackitDefaultRegion verifies that STACKIT defaults region to eu01 when unspecified.
func TestStackitDefaultRegion(t *testing.T) {
	t.Parallel()
	// Create a temporary config file inside the repo workspace
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")

	// Minimal config with a single bloc using STACKIT and no region
	yml := []byte("" +
		"blocs:\n" +
		"  test:\n" +
		"    name: test\n" +
		"    provider: stackit\n")

	err := os.WriteFile(cfgPath, yml, 0o600)
	if err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := config.LoadWithParams(cfgPath, "test")
	if err != nil {
		t.Fatalf("LoadWithParams failed: %v", err)
	}

	if got, want := cfg.Region, "eu01"; got != want {
		t.Fatalf("unexpected default region: got %q want %q", got, want)
	}
}

// TestStackitRegionOverride verifies that a specified region is preserved.
func TestStackitRegionOverride(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")

	yml := []byte("" +
		"blocs:\n" +
		"  prod:\n" +
		"    name: prod\n" +
		"    provider: stackit\n" +
		"    region: eu02\n")

	err := os.WriteFile(cfgPath, yml, 0o600)
	if err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := config.LoadWithParams(cfgPath, "prod")
	if err != nil {
		t.Fatalf("LoadWithParams failed: %v", err)
	}

	if got, want := cfg.Region, "eu02"; got != want {
		t.Fatalf("region override not respected: got %q want %q", got, want)
	}
}

func TestDeploymentsParsingWithGlobalURL(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")

	yml := []byte(`
blocs:
  test:
    name: test
    provider: stackit
    deployments:
      url: git@example.com:ocfp/deployments.git
      bosh:
        mode: release
      vault:
        mode: dev
      cf: {}
`)

	err := os.WriteFile(cfgPath, yml, 0o600)
	if err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := config.LoadWithParams(cfgPath, "test")
	if err != nil {
		t.Fatalf("LoadWithParams failed: %v", err)
	}

	if got, want := cfg.GetDeploymentsURL(), "git@example.com:ocfp/deployments.git"; got != want {
		t.Fatalf("unexpected deployments url: got %q want %q", got, want)
	}

	if mode := cfg.GetDeploymentMode("bosh"); mode != config.DeploymentModeRelease {
		t.Fatalf("expected bosh release mode, got %q", mode)
	}

	if mode := cfg.GetDeploymentMode("vault"); mode != config.DeploymentModeDev {
		t.Fatalf("expected vault dev mode, got %q", mode)
	}

	if mode := cfg.GetDeploymentMode("cf"); mode != config.DeploymentModeRelease {
		t.Fatalf("expected cf release mode by default, got %q", mode)
	}

	if mode := cfg.GetDeploymentMode("autoscaler"); mode != config.DeploymentModeRelease {
		t.Fatalf("expected autoscaler release mode by default, got %q", mode)
	}

	expected := []string{"bosh", "cf", "vault"}
	got := cfg.GetConfiguredDeployments()
	if len(got) != len(expected) {
		t.Fatalf("unexpected configured deployments length: got %v want %v", got, expected)
	}

	for i := range expected {
		if got[i] != expected[i] {
			t.Fatalf("configured deployments mismatch: got %v want %v", got, expected)
		}
	}
}

func TestDeploymentsParsingWithoutURL(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")

	yml := []byte(`
blocs:
  test:
    name: test
    provider: stackit
    deployments:
      bosh:
        mode: release
      vault: {}
`)

	err := os.WriteFile(cfgPath, yml, 0o600)
	if err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := config.LoadWithParams(cfgPath, "test")
	if err != nil {
		t.Fatalf("LoadWithParams failed: %v", err)
	}

	if cfg.GetDeploymentsURL() != "" {
		t.Fatalf("expected empty deployments url, got %q", cfg.GetDeploymentsURL())
	}

	if mode := cfg.GetDeploymentMode("bosh"); mode != config.DeploymentModeRelease {
		t.Fatalf("expected bosh release mode, got %q", mode)
	}

	if mode := cfg.GetDeploymentMode("vault"); mode != config.DeploymentModeDev {
		t.Fatalf("expected vault dev mode, got %q", mode)
	}

	if mode := cfg.GetDeploymentMode("cf"); mode != config.DeploymentModeDev {
		t.Fatalf("expected cf dev mode without global url, got %q", mode)
	}
}

func TestDeploymentsParsingStringShorthand(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")

	yml := []byte(`
blocs:
  test:
    name: test
    provider: stackit
    deployments:
      bosh: release
      vault: dev
`)

	err := os.WriteFile(cfgPath, yml, 0o600)
	if err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := config.LoadWithParams(cfgPath, "test")
	if err != nil {
		t.Fatalf("LoadWithParams failed: %v", err)
	}

	if mode := cfg.GetDeploymentMode("bosh"); mode != config.DeploymentModeRelease {
		t.Fatalf("expected bosh release mode, got %q", mode)
	}

	if mode := cfg.GetDeploymentMode("vault"); mode != config.DeploymentModeDev {
		t.Fatalf("expected vault dev mode, got %q", mode)
	}

	if cfg.GetDeploymentsURL() != "" {
		t.Fatalf("expected empty deployments url, got %q", cfg.GetDeploymentsURL())
	}
}
