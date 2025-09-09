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
