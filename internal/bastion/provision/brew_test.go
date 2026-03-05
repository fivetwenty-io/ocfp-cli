package provision

import (
	"context"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

func TestGetBrewPackages_Defaults(t *testing.T) {
	cfg := &config.Config{
		Bastion: config.Bastion{
			Brews:         config.OverrideSets{},
			BrewOverrides: nil,
		},
	}

	bm := NewBrewManager("aws", cfg)
	pkgs := bm.GetBrewPackages()

	if len(pkgs) == 0 {
		t.Fatal("Expected non-empty default brew package list")
	}

	// Verify key packages are present (including migrated-from-APT packages)
	expected := map[string]bool{
		"rsync": false, "wget": false, "jq": false, "go": false,
		"kubectl": false, "vim": false, "neovim": false, "vault": false,
		"yq": false, "ripgrep": false, "tmux": false, "ruby": false,
		"gcc": false, "make": false, "cpanminus": false, "gnupg": false,
		"python@3": false, "readline": false, "libyaml": false, "zlib": false,
		"bosh-cli": false, "cf-cli": false, "credhub-cli": false,
		"uaa-cli": false, "spruce": false, "openbao": false,
	}

	for _, pkg := range pkgs {
		if _, ok := expected[pkg.Name]; ok {
			expected[pkg.Name] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("Expected default brew package list to contain '%s'", name)
		}
	}
}

func TestGetBrewPackages_EnableDisable(t *testing.T) {
	cfg := &config.Config{
		Bastion: config.Bastion{
			Brews: config.OverrideSets{
				Enable:  []string{"node"},
				Disable: []string{"vim"},
			},
			BrewOverrides: nil,
		},
	}

	bm := NewBrewManager("aws", cfg)
	pkgs := bm.GetBrewPackages()

	for _, pkg := range pkgs {
		switch pkg.Name {
		case "node":
			if !pkg.Enabled {
				t.Error("Expected 'node' to be enabled via override")
			}
		case "vim":
			if pkg.Enabled {
				t.Error("Expected 'vim' to be disabled via override")
			}
		}
	}
}

func TestGetBrewPackages_ProviderAzure(t *testing.T) {
	cfg := &config.Config{
		Bastion: config.Bastion{
			Brews:         config.OverrideSets{},
			BrewOverrides: nil,
		},
	}

	bm := NewBrewManager("azure", cfg)
	pkgs := bm.GetBrewPackages()

	found := false

	for _, pkg := range pkgs {
		if pkg.Name == "azure-cli" {
			found = true

			if !pkg.Enabled {
				t.Error("Expected azure-cli to be enabled for Azure provider")
			}

			if pkg.CheckCommand != "az" {
				t.Errorf("Expected azure-cli check command 'az', got '%s'", pkg.CheckCommand)
			}
		}
	}

	if !found {
		t.Error("Expected azure-cli in brew packages for Azure provider")
	}
}

func TestGetBrewPackages_ProviderGCP(t *testing.T) {
	cfg := &config.Config{
		Bastion: config.Bastion{
			Brews:         config.OverrideSets{},
			BrewOverrides: nil,
		},
	}

	bm := NewBrewManager("gcp", cfg)
	pkgs := bm.GetBrewPackages()

	found := false

	for _, pkg := range pkgs {
		if pkg.Name == "google-cloud-sdk" {
			found = true

			if !pkg.Cask {
				t.Error("Expected google-cloud-sdk to be a cask")
			}

			if pkg.CheckCommand != "gcloud" {
				t.Errorf("Expected google-cloud-sdk check command 'gcloud', got '%s'", pkg.CheckCommand)
			}
		}
	}

	if !found {
		t.Error("Expected google-cloud-sdk in brew packages for GCP provider")
	}
}

func TestGetBrewPackages_ProviderNonAzure_NoAzureCLI(t *testing.T) {
	cfg := &config.Config{
		Bastion: config.Bastion{
			Brews:         config.OverrideSets{},
			BrewOverrides: nil,
		},
	}

	bm := NewBrewManager("aws", cfg)
	pkgs := bm.GetBrewPackages()

	for _, pkg := range pkgs {
		if pkg.Name == "azure-cli" {
			t.Error("Expected azure-cli to NOT be present for non-Azure provider")
		}
	}
}

func TestGenerateBrewInstallScript(t *testing.T) {
	cfg := &config.Config{}
	bm := NewBrewManager("aws", cfg)

	script := bm.GenerateBrewInstallScript(context.Background())

	if script == "" {
		t.Fatal("Expected non-empty brew install script")
	}

	// Verify key elements
	checks := []string{
		"NONINTERACTIVE=1",
		"brew shellenv",
		"brew analytics off",
		"command -v brew",
		"/home/linuxbrew/.linuxbrew/bin/brew",
	}

	for _, check := range checks {
		if !strings.Contains(script, check) {
			t.Errorf("Expected brew install script to contain '%s'", check)
		}
	}
}

func TestGenerateBrewPackageScript(t *testing.T) {
	cfg := &config.Config{
		Bastion: config.Bastion{
			Brews:         config.OverrideSets{},
			BrewOverrides: nil,
		},
	}

	bm := NewBrewManager("aws", cfg)
	script := bm.GenerateBrewPackageScript(context.Background())

	if script == "" {
		t.Fatal("Expected non-empty brew package script")
	}

	// Verify batch install (single brew install command with multiple packages)
	if !strings.Contains(script, "brew install \\") {
		t.Error("Expected batched brew install command in brew package script")
	}

	// Verify HOMEBREW_NO_AUTO_UPDATE
	if !strings.Contains(script, "HOMEBREW_NO_AUTO_UPDATE=1") {
		t.Error("Expected HOMEBREW_NO_AUTO_UPDATE=1 in brew package script")
	}

	// Verify brew shellenv for PATH propagation
	if !strings.Contains(script, "brew shellenv") {
		t.Error("Expected brew shellenv in brew package script for PATH propagation")
	}

	// Verify vault tap handling
	if !strings.Contains(script, "hashicorp/tap") {
		t.Error("Expected hashicorp/tap in brew package script for vault")
	}

	// Verify cloudfoundry/tap for CF ecosystem tools
	if !strings.Contains(script, "cloudfoundry/tap") {
		t.Error("Expected cloudfoundry/tap in brew package script")
	}

	// spruce brew formula is macOS-only; installed via binary_tools instead
}

func TestGenerateBrewPackageScript_CaskSupport(t *testing.T) {
	cfg := &config.Config{
		Bastion: config.Bastion{
			Brews:         config.OverrideSets{},
			BrewOverrides: nil,
		},
	}

	bm := NewBrewManager("gcp", cfg)
	script := bm.GenerateBrewPackageScript(context.Background())

	if !strings.Contains(script, "--cask") {
		t.Error("Expected --cask flag for google-cloud-sdk in GCP provider script")
	}
}

func TestBrewOverrides(t *testing.T) {
	caskTrue := true

	cfg := &config.Config{
		Bastion: config.Bastion{
			Brews: config.OverrideSets{},
			BrewOverrides: map[string]config.BrewOverride{
				"jq": {
					Version:      "1.7",
					CheckCommand: "jq-custom",
				},
				"vault": {
					Tap:  "custom/tap",
					Cask: &caskTrue,
				},
			},
		},
	}

	bm := NewBrewManager("aws", cfg)
	pkgs := bm.GetBrewPackages()

	for _, pkg := range pkgs {
		switch pkg.Name {
		case "jq":
			if pkg.Version != "1.7" {
				t.Errorf("Expected jq version '1.7', got '%s'", pkg.Version)
			}

			if pkg.CheckCommand != "jq-custom" {
				t.Errorf("Expected jq check command 'jq-custom', got '%s'", pkg.CheckCommand)
			}
		case "vault":
			if pkg.Tap != "custom/tap" {
				t.Errorf("Expected vault tap 'custom/tap', got '%s'", pkg.Tap)
			}

			if !pkg.Cask {
				t.Error("Expected vault to be a cask via override")
			}
		}
	}
}

func TestBuildBrewInstallCommand(t *testing.T) {
	bm := NewBrewManager("aws", nil)

	tests := []struct {
		name     string
		pkg      BrewPackage
		expected string
	}{
		{
			name:     "simple package",
			pkg:      BrewPackage{Name: "jq"},
			expected: "brew install jq",
		},
		{
			name:     "versioned package",
			pkg:      BrewPackage{Name: "go", Version: "1.24"},
			expected: "brew install go@1.24",
		},
		{
			name:     "cask package",
			pkg:      BrewPackage{Name: "google-cloud-sdk", Cask: true},
			expected: "brew install --cask google-cloud-sdk",
		},
		{
			name:     "tap package",
			pkg:      BrewPackage{Name: "vault", Tap: "hashicorp/tap"},
			expected: "brew install hashicorp/tap/vault",
		},
		{
			name:     "package with options",
			pkg:      BrewPackage{Name: "openssl", Options: "--force"},
			expected: "brew install openssl --force",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := bm.buildBrewInstallCommand(tt.pkg)
			if result != tt.expected {
				t.Errorf("Expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

func TestGetBrewPackages_GoPinnedVersion(t *testing.T) {
	cfg := &config.Config{
		Bastion: config.Bastion{
			Brews:         config.OverrideSets{},
			BrewOverrides: nil,
		},
	}

	bm := NewBrewManager("aws", cfg)
	pkgs := bm.GetBrewPackages()

	for _, pkg := range pkgs {
		if pkg.Name == "go" {
			if pkg.Version != "1.26" {
				t.Errorf("Expected go pinned to version '1.26', got '%s'", pkg.Version)
			}

			return
		}
	}

	t.Error("Expected 'go' package in brew packages")
}

func TestGetBrewPackages_NodeDisabledByDefault(t *testing.T) {
	cfg := &config.Config{
		Bastion: config.Bastion{
			Brews:         config.OverrideSets{},
			BrewOverrides: nil,
		},
	}

	bm := NewBrewManager("aws", cfg)
	pkgs := bm.GetBrewPackages()

	for _, pkg := range pkgs {
		if pkg.Name == "node" {
			if pkg.Enabled {
				t.Error("Expected 'node' to be disabled by default")
			}

			return
		}
	}

	t.Error("Expected 'node' package in brew packages (even if disabled)")
}
