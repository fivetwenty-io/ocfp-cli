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

func TestFormatAvailabilityZone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		region   string
		index    int
		want     string
	}{
		{
			name:     "AWS index 0",
			provider: "aws",
			region:   "us-east-1",
			index:    0,
			want:     "us-east-1a",
		},
		{
			name:     "AWS index 2",
			provider: "aws",
			region:   "us-west-2",
			index:    2,
			want:     "us-west-2c",
		},
		{
			name:     "STACKIT index 0",
			provider: "stackit",
			region:   "eu01",
			index:    0,
			want:     "eu01-1",
		},
		{
			name:     "STACKIT index 2",
			provider: "stackit",
			region:   "eu01",
			index:    2,
			want:     "eu01-3",
		},
		{
			name:     "STACKIT case insensitive",
			provider: "STACKIT",
			region:   "eu01",
			index:    1,
			want:     "eu01-2",
		},
		{
			name:     "GCP uses letter suffix",
			provider: "gcp",
			region:   "us-central1",
			index:    1,
			want:     "us-central1b",
		},
		{
			name:     "Azure uses letter suffix",
			provider: "azure",
			region:   "eastus",
			index:    0,
			want:     "eastusa",
		},
		{
			name:     "empty provider uses letter suffix",
			provider: "",
			region:   "us-east-1",
			index:    0,
			want:     "us-east-1a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := config.FormatAvailabilityZone(tt.provider, tt.region, tt.index)
			if got != tt.want {
				t.Errorf("FormatAvailabilityZone(%q, %q, %d) = %q, want %q",
					tt.provider, tt.region, tt.index, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// IMP-10: VMStorage / DiskStorage Config field tests
// ---------------------------------------------------------------------------

// T29 TestConfig_VMStorage_FieldPresent verifies VMStorage field can be set and
// round-trips through struct assignment without data loss.
func TestConfig_VMStorage_FieldPresent(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{VMStorage: "data"}
	if got, want := cfg.VMStorage, "data"; got != want {
		t.Errorf("Config.VMStorage = %q, want %q", got, want)
	}
}

// T29b TestConfig_DiskStorage_FieldPresent verifies DiskStorage field can be
// set and round-trips through struct assignment without data loss.
func TestConfig_DiskStorage_FieldPresent(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{DiskStorage: "zfs-1"}
	if got, want := cfg.DiskStorage, "zfs-1"; got != want {
		t.Errorf("Config.DiskStorage = %q, want %q", got, want)
	}
}

// TestConfig_VMStorage_ZeroValueSafe verifies an empty VMStorage field does not
// panic and returns empty string (zero value).
func TestConfig_VMStorage_ZeroValueSafe(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	if cfg.VMStorage != "" {
		t.Errorf("Config.VMStorage zero value = %q, want empty string", cfg.VMStorage)
	}
}

// TestConfig_DiskStorage_ZeroValueSafe verifies an empty DiskStorage field does
// not panic and returns empty string (zero value).
func TestConfig_DiskStorage_ZeroValueSafe(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	if cfg.DiskStorage != "" {
		t.Errorf("Config.DiskStorage zero value = %q, want empty string", cfg.DiskStorage)
	}
}

// TestConfig_VMStorage_YAMLRoundTrip verifies VMStorage field round-trips
// through YAML serialization via config file load.
func TestConfig_VMStorage_YAMLRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")

	yml := []byte("" +
		"blocs:\n" +
		"  lab:\n" +
		"    provider: pve\n" +
		"    api_endpoint: https://pve.example.com:8006\n" +
		"    auth_token: root@pam!tok\n" +
		"    token_secret: secret\n" +
		"    region: pve01\n" +
		"    vm_storage: data\n" +
		"    disk_storage: zfs-1\n")

	if err := os.WriteFile(cfgPath, yml, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.LoadWithParams(cfgPath, "lab")
	if err != nil {
		t.Fatalf("LoadWithParams: %v", err)
	}

	if got, want := cfg.VMStorage, "data"; got != want {
		t.Errorf("VMStorage after YAML load = %q, want %q", got, want)
	}

	if got, want := cfg.DiskStorage, "zfs-1"; got != want {
		t.Errorf("DiskStorage after YAML load = %q, want %q", got, want)
	}
}
