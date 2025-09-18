package deployments

import (
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

func TestResolverModesAndPaths(t *testing.T) {
	cfg := config.NewTestConfig().Build()
	cfg.Deployments = config.NewDeploymentSettings("git@example.com:ocfp/deployments.git", map[string]*config.DeploymentEntry{
		"bosh":  {Mode: config.DeploymentModeRelease},
		"vault": {Mode: config.DeploymentModeDev},
	})

	resolver := NewResolver(cfg)
	tempHome := t.TempDir()
	resolver.SetHome(tempHome)

	if !resolver.IsRelease("bosh") {
		t.Fatalf("expected bosh to be in release mode")
	}

	if resolver.Mode("unknown") != config.DeploymentModeRelease {
		t.Fatalf("expected unknown deployments to default to release when url is configured")
	}

	if resolver.KitPath("vault") != filepath.Join(tempHome, "ocfp", "kits", "vault") {
		t.Fatalf("unexpected kit path for vault: %s", resolver.KitPath("vault"))
	}

	if resolver.KitPath("bosh") != "" {
		t.Fatalf("expected empty kit path for release deployment")
	}

	if err := resolver.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestResolverValidateRequiresURLForRelease(t *testing.T) {
	cfg := config.NewTestConfig().Build()
	cfg.Deployments = config.NewDeploymentSettings("", map[string]*config.DeploymentEntry{
		"bosh": {Mode: config.DeploymentModeRelease},
	})

	resolver := NewResolver(cfg)

	if err := resolver.Validate(); err == nil {
		t.Fatalf("expected validation error when release mode is set without global url")
	}

	cfg.Deployments.URL = "git@example.com:ocfp/deployments.git"

	if err := resolver.Validate(); err != nil {
		t.Fatalf("unexpected validation error with url configured: %v", err)
	}
}

func TestResolverKitPathDevWithoutURL(t *testing.T) {
	cfg := config.NewTestConfig().Build()
	cfg.Deployments = config.NewDeploymentSettings("", map[string]*config.DeploymentEntry{
		"vault": {Mode: config.DeploymentModeDev},
	})

	resolver := NewResolver(cfg)
	tempHome := t.TempDir()
	resolver.SetHome(tempHome)

	expected := filepath.Join(tempHome, "ocfp", "kits", "vault", "dev")
	if resolver.KitPath("vault") != expected {
		t.Fatalf("expected kit path %s, got %s", expected, resolver.KitPath("vault"))
	}
}
