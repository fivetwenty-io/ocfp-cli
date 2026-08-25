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
		"bosh-cli": false, "cf-cli@8": false, "credhub-cli": false,
		"uaa-cli": false, "graft": false, "openbao": false,
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

	// CF ecosystem tools (bosh, cf, credhub, uaa) are installed via binary_tools
	// because the cloudfoundry brew taps ship macOS-only binaries
}

// TestGenerateBrewPackageScript_VaultTap verifies hashicorp/tap appears when vault backend is selected.
func TestGenerateBrewPackageScript_VaultTap(t *testing.T) {
	cfg := &config.Config{
		SecretsBackend: "vault",
		Bastion: config.Bastion{
			Brews:         config.OverrideSets{},
			BrewOverrides: nil,
		},
	}

	bm := NewBrewManager("aws", cfg)
	script := bm.GenerateBrewPackageScript(context.Background())

	if !strings.Contains(script, "hashicorp/tap") {
		t.Error("Expected hashicorp/tap in brew package script when secrets_backend=vault")
	}
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
			if pkg.Version != "1.27" {
				t.Errorf("Expected go pinned to version '1.27', got '%s'", pkg.Version)
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

// --- A5: vault brew package enabled state ---

// The inception vault (`safe local` + `vault status`) runs on the vault
// binary regardless of the bloc's secrets backend, so vault must be
// installed even with the openbao default.
func TestGetBrewPackages_VaultEnabledByDefault(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	bm := NewBrewManager("aws", cfg)
	pkgs := bm.GetBrewPackages()

	for _, p := range pkgs {
		if p.Name == "vault" {
			if !p.Enabled {
				t.Error("vault brew package must be Enabled=true even when secrets_backend is default (openbao); the inception vault requires it")
			}

			return
		}
	}

	t.Error("vault package not found in brew packages")
}

func TestGetBrewPackages_VaultEnabledWhenVaultBackend(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{SecretsBackend: "vault"}
	bm := NewBrewManager("aws", cfg)
	pkgs := bm.GetBrewPackages()

	for _, p := range pkgs {
		if p.Name == "vault" {
			if !p.Enabled {
				t.Error("vault brew package must be Enabled=true when secrets_backend=vault")
			}

			return
		}
	}

	t.Error("vault package not found in brew packages")
}

func TestGetBrewPackages_OpenBaoAlwaysEnabled(t *testing.T) {
	t.Parallel()

	for _, backend := range []string{"", "openbao", "vault"} {
		backend := backend
		t.Run("backend="+backend, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{SecretsBackend: backend}
			bm := NewBrewManager("aws", cfg)
			pkgs := bm.GetBrewPackages()

			for _, p := range pkgs {
				if p.Name == "openbao" {
					if !p.Enabled {
						t.Errorf("openbao brew package must always be Enabled=true (backend=%q)", backend)
					}

					return
				}
			}

			t.Errorf("openbao package not found in brew packages (backend=%q)", backend)
		})
	}
}

// TestGetBrewPackages_GraftCask verifies graft ships as a cask from the
// FiveTwenty tap and is enabled everywhere.
func TestGetBrewPackages_GraftCask(t *testing.T) {
	t.Parallel()

	bm := NewBrewManager("aws", nil)

	for _, pkg := range bm.GetBrewPackages() {
		if pkg.Name != "graft" {
			continue
		}

		if !pkg.Enabled {
			t.Error("graft brew package must be enabled by default")
		}

		if !pkg.Cask {
			t.Error("graft is published as a cask, expected Cask=true")
		}

		if pkg.Tap != "fivetwenty-io/tap" {
			t.Errorf("graft tap = %q, want fivetwenty-io/tap", pkg.Tap)
		}

		return
	}

	t.Error("graft package not found in brew packages")
}

// TestGetBrewPackages_PMXOnlyOnPVE verifies pmx is a PVE-only package.
func TestGetBrewPackages_PMXOnlyOnPVE(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{"pve": true, "aws": false, "gcp": false, "stackit": false}

	for provider, want := range cases {
		provider, want := provider, want
		t.Run("provider="+provider, func(t *testing.T) {
			t.Parallel()

			bm := NewBrewManager(provider, nil)

			found := false

			for _, pkg := range bm.GetBrewPackages() {
				if pkg.Name == "pmx" {
					found = true

					if !pkg.Cask || pkg.Tap != "fivetwenty-io/tap" {
						t.Errorf("pmx = {Cask:%v Tap:%q}, want {Cask:true Tap:fivetwenty-io/tap}", pkg.Cask, pkg.Tap)
					}
				}
			}

			if found != want {
				t.Errorf("pmx present = %v, want %v", found, want)
			}
		})
	}
}

// TestGenerateBrewPackageScript_TrustsThirdPartyTaps verifies every tap we add
// is also trusted: Homebrew 6 will not evaluate an untrusted tap's code.
func TestGenerateBrewPackageScript_TrustsThirdPartyTaps(t *testing.T) {
	t.Parallel()

	script := NewBrewManager("pve", nil).GenerateBrewPackageScript(context.Background())

	for _, tap := range []string{"fivetwenty-io/tap", "hashicorp/tap"} {
		if !strings.Contains(script, "brew tap "+tap) {
			t.Errorf("expected script to tap %s", tap)
		}

		if !strings.Contains(script, "brew trust --tap "+tap) {
			t.Errorf("expected script to trust tap %s", tap)
		}
	}
}

// TestGenerateBrewPackageScript_InstallsPMXOnPVE verifies the PVE cask install.
func TestGenerateBrewPackageScript_InstallsPMXOnPVE(t *testing.T) {
	t.Parallel()

	pveScript := NewBrewManager("pve", nil).GenerateBrewPackageScript(context.Background())
	if !strings.Contains(pveScript, "fivetwenty-io/tap/pmx") {
		t.Error("expected pmx cask install on the PVE bastion")
	}

	awsScript := NewBrewManager("aws", nil).GenerateBrewPackageScript(context.Background())
	if strings.Contains(awsScript, "fivetwenty-io/tap/pmx") {
		t.Error("pmx must not be installed on non-PVE bastions")
	}
}

// TestGenerateGraftSpruceLinkScript verifies graft is linked as spruce.
func TestGenerateGraftSpruceLinkScript(t *testing.T) {
	t.Parallel()

	script := NewBrewManager("aws", nil).GenerateGraftSpruceLinkScript(context.Background())

	for _, want := range []string{
		"command -v graft",
		"sudo ln -sf \"$GRAFT_BIN\" /usr/local/bin/spruce",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("expected graft link script to contain %q\ngot:\n%s", want, script)
		}
	}
}

// TestGenerateGraftSpruceLinkScript_FallsBackWhenGraftDisabled verifies that
// opting out of graft still leaves a working `spruce`, backed by the upstream
// binary, since verification and every kit depend on that command existing.
func TestGenerateGraftSpruceLinkScript_FallsBackWhenGraftDisabled(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Bastion: config.Bastion{
			Brews: config.OverrideSets{Disable: []string{"graft"}},
		},
	}

	script := NewBrewManager("aws", cfg).GenerateGraftSpruceLinkScript(context.Background())
	if strings.Contains(script, "Link graft as spruce") {
		t.Errorf("expected no graft link when graft is disabled, got:\n%s", script)
	}

	if !strings.Contains(script, spruceOrigPath+" "+spruceLinkPath) {
		t.Errorf("expected upstream spruce to be linked as spruce, got:\n%s", script)
	}
}

// TestGenerateBrewPackageScript_UpgradesLatestTrackingTools verifies that every
// init moves graft and pmx to their newest release. `brew install` is a no-op
// once a package exists, so without the upgrade step a bastion keeps whichever
// version it was first built with.
func TestGenerateBrewPackageScript_UpgradesLatestTrackingTools(t *testing.T) {
	t.Parallel()

	script := NewBrewManager("pve", nil).GenerateBrewPackageScript(context.Background())

	for _, want := range []string{
		"brew update",
		"brew upgrade --cask ",
		"fivetwenty-io/tap/graft",
		"fivetwenty-io/tap/pmx",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("expected package script to contain %q\ngot:\n%s", want, script)
		}
	}

	upgrade := script[strings.Index(script, "brew upgrade --cask "):]
	if !strings.Contains(upgrade, "fivetwenty-io/tap/graft") || !strings.Contains(upgrade, "fivetwenty-io/tap/pmx") {
		t.Errorf("expected both casks on the upgrade line, got:\n%s", upgrade)
	}
}

// TestGenerateBrewPackageScript_NoUpgradeWithoutLatestTrackingTools verifies the
// upgrade step disappears when nothing asks to track latest, so bastions that
// opt out of graft and pmx do not pay for a tap refresh.
func TestGenerateBrewPackageScript_NoUpgradeWithoutLatestTrackingTools(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Bastion: config.Bastion{
			Brews: config.OverrideSets{Disable: []string{"graft", "pmx"}},
		},
	}

	script := NewBrewManager("pve", cfg).GenerateBrewPackageScript(context.Background())
	if strings.Contains(script, "brew upgrade") {
		t.Errorf("expected no upgrade step when no package tracks latest, got:\n%s", script)
	}
}
