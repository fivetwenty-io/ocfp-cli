package provision

import (
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

func TestGenerateGenesisConfig(t *testing.T) {
	cfg := &config.Config{
		Name: "520-aws-wayne",
	}

	cfm := NewConfigFileManager("test-provider", cfg)
	result := cfm.GenerateGenesisConfig()

	// Verify that bash variable references are NOT present
	bashVarPatterns := []string{
		"${OCFP_BLOC}",
		"$OCFP_BLOC",
		":-development}",
		":-bosh}",
	}

	for _, pattern := range bashVarPatterns {
		if strings.Contains(result, pattern) {
			t.Errorf("Genesis config should not contain bash variable pattern '%s'", pattern)
		}
	}

	// Verify required configuration sections are present
	requiredSections := []string{
		"# ~/.genesis/config",
		"default_bosh_target: ask",
		"deployment_roots:",
		"  - /home/ubuntu/ops/deployments",
		"output_style: fun",
		"show_duration: true",
		"confirm_release_overrides: outdated",
		"bosh_logs_path: \"/home/ubuntu/ocfp/logs\"",
		"suppress_warnings:",
		"  oversized_secrets: false",
		"  bosh_target: true",
		"embedded_genesis: warn",
		"automatic_config_upgrade: \"yes\"",
		"logs:",
	}

	for _, section := range requiredSections {
		if !strings.Contains(result, section) {
			t.Errorf("Expected genesis config to contain '%s', but it was not found.\nGenerated config:\n%s",
				section, result)
		}
	}

	// fix_on_deploy MUST be 'never'. Any other value makes genesis deploy take
	// the secret-fixing path, which FATALs on entombed vaultified environments
	// with manifest-source secrets (blacksmith kit >=1.3.0).
	if !strings.Contains(result, "fix_on_deploy: never") {
		t.Errorf("Expected genesis config to contain 'fix_on_deploy: never'.\nGenerated config:\n%s", result)
	}

	if strings.Contains(result, "fix_on_deploy: ask") {
		t.Errorf("Genesis config must not set 'fix_on_deploy: ask'; it must be 'never'.\nGenerated config:\n%s", result)
	}
}

func TestGenerateGenesisConfig_LogsConfiguration(t *testing.T) {
	cfg := &config.Config{
		Name: "520-aws-wayne",
	}

	cfm := NewConfigFileManager("aws", cfg)
	result := cfm.GenerateGenesisConfig()

	// Verify comprehensive logging configuration
	logPatterns := []string{
		"logs:",
		"# Main application log",
		"- file: \"/home/ubuntu/.genesis/logs/genesis.log\"",
		"level: INFO",
		"timestamp: true",
		"style: plain",
		"lifespan: forever",
		"show_stack: default",
		"truncate: false",
		"# Debug log for troubleshooting",
		"- file: \"/home/ubuntu/.genesis/logs/debug.log\"",
		"level: DEBUG",
		"style: rfc-5424",
		"lifespan: current",
		"show_stack: full",
		"# Error-only log",
		"- file: \"/home/ubuntu/.genesis/logs/errors.log\"",
		"level: ERROR",
		"show_stack: fatal",
	}

	for _, pattern := range logPatterns {
		if !strings.Contains(result, pattern) {
			t.Errorf("Expected genesis config to contain '%s', but it was not found.\nGenerated config:\n%s",
				pattern, result)
		}
	}

	// Ensure no bash variables remain
	bashVarPatterns := []string{
		"${OCFP_BLOC}",
		"$OCFP_BLOC",
		":-development}",
		":-bosh}",
	}

	for _, pattern := range bashVarPatterns {
		if strings.Contains(result, pattern) {
			t.Errorf("Genesis config should not contain bash variable pattern '%s'.\nGenerated config:\n%s",
				pattern, result)
		}
	}
}

func TestGetGenesisRepository_SourceBased(t *testing.T) {
	cfg := &config.Config{
		Genesis: config.Genesis{
			Enabled: true,
		},
	}

	provCfg := NewConfig("stackit", cfg, nil)
	repo := provCfg.getGenesisRepository()

	if repo.Name != "genesis" {
		t.Errorf("Expected repo name 'genesis', got '%s'", repo.Name)
	}

	if !repo.Enabled {
		t.Error("Expected repo to be enabled")
	}

	if repo.URL != "git@github.com:genesis-community/genesis" {
		t.Errorf("Expected default repo URL, got '%s'", repo.URL)
	}

	if repo.Branch != "v3.2.x-dev" {
		t.Errorf("Expected default branch 'v3.2.x-dev', got '%s'", repo.Branch)
	}

	if repo.Dest != "${HOME}/ocfp/genesis" {
		t.Errorf("Expected dest '${HOME}/ocfp/genesis', got '%s'", repo.Dest)
	}
}

func TestGetGenesisRepository_CustomRepo(t *testing.T) {
	cfg := &config.Config{
		Genesis: config.Genesis{
			Enabled:       true,
			Repo:          "git@github.com:myorg/genesis-fork",
			Branch:        "custom-branch",
			VersionPrefix: "3.2.0",
		},
	}

	provCfg := NewConfig("stackit", cfg, nil)
	repo := provCfg.getGenesisRepository()

	if repo.URL != "git@github.com:myorg/genesis-fork" {
		t.Errorf("Expected custom repo URL, got '%s'", repo.URL)
	}

	if repo.Branch != "custom-branch" {
		t.Errorf("Expected branch 'custom-branch', got '%s'", repo.Branch)
	}
}

func TestGetGenesisTool_SourceBased(t *testing.T) {
	cfg := &config.Config{
		Genesis: config.Genesis{
			Enabled: true,
		},
		Bastion: config.Bastion{
			ToolOverrides: map[string]config.ToolOverride{},
		},
	}

	provCfg := NewConfig("stackit", cfg, nil)
	tool := provCfg.getGenesisTool()

	if tool.Name != "genesis" {
		t.Errorf("Expected tool name 'genesis', got '%s'", tool.Name)
	}

	if !tool.Enabled {
		t.Error("Expected tool to be enabled")
	}

	if tool.URL != "" {
		t.Errorf("Expected empty URL for source-based install, got '%s'", tool.URL)
	}

	if tool.InstallCommand == "" {
		t.Error("Expected InstallCommand to be set for source-based install")
	}

	if !strings.Contains(tool.InstallCommand, "./pack 3.2.0") {
		t.Error("Expected InstallCommand to contain './pack 3.2.0'")
	}

	if !strings.Contains(tool.InstallCommand, "~/ocfp/genesis") {
		t.Error("Expected InstallCommand to reference ~/ocfp/genesis")
	}
}

func TestGetGenesisTool_BinaryDownload(t *testing.T) {
	cfg := &config.Config{
		Bastion: config.Bastion{
			Genesis: config.Genesis{
				Enabled:       true,
				VersionPrefix: "3.1.0",
			},
			ToolOverrides: map[string]config.ToolOverride{
				"genesis": {
					URL: "https://example.com/genesis-3.1.0",
				},
			},
		},
	}

	provCfg := NewConfig("stackit", cfg, nil)
	tool := provCfg.getGenesisTool()

	if tool.Name != "genesis" {
		t.Errorf("Expected tool name 'genesis', got '%s'", tool.Name)
	}

	if tool.URL != "https://example.com/genesis-3.1.0" {
		t.Errorf("Expected URL 'https://example.com/genesis-3.1.0', got '%s'", tool.URL)
	}

	if tool.InstallCommand != "" {
		t.Errorf("Expected empty InstallCommand for binary download, got '%s'", tool.InstallCommand)
	}

	if !tool.Sudo {
		t.Error("Expected Sudo to be true for binary download")
	}
}

func TestGetGenesisTool_CustomInstall(t *testing.T) {
	customCmd := "echo 'custom install'"

	cfg := &config.Config{
		Bastion: config.Bastion{
			Genesis: config.Genesis{
				Enabled: true,
			},
			ToolOverrides: map[string]config.ToolOverride{
				"genesis": {
					InstallCommand: customCmd,
					VerifyCommand:  "genesis version",
				},
			},
		},
	}

	provCfg := NewConfig("stackit", cfg, nil)
	tool := provCfg.getGenesisTool()

	if tool.InstallCommand != customCmd {
		t.Errorf("Expected custom install command, got '%s'", tool.InstallCommand)
	}

	if tool.Verify != "genesis version" {
		t.Errorf("Expected verify command 'genesis version', got '%s'", tool.Verify)
	}
}

func TestShouldInstallGenesisFromSource_Enabled(t *testing.T) {
	cfg := &config.Config{
		Genesis: config.Genesis{
			Enabled: true,
			Branch:  "v3.1.x-dev",
		},
		Bastion: config.Bastion{
			ToolOverrides: map[string]config.ToolOverride{},
		},
	}

	provCfg := NewConfig("stackit", cfg, nil)

	if !provCfg.shouldInstallGenesisFromSource() {
		t.Error("Expected shouldInstallGenesisFromSource to return true")
	}
}

func TestShouldInstallGenesisFromSource_BinaryOverride(t *testing.T) {
	cfg := &config.Config{
		Genesis: config.Genesis{
			Enabled: true,
		},
		Bastion: config.Bastion{
			ToolOverrides: map[string]config.ToolOverride{
				"genesis": {
					URL: "https://example.com/genesis",
				},
			},
		},
	}

	provCfg := NewConfig("stackit", cfg, nil)

	if provCfg.shouldInstallGenesisFromSource() {
		t.Error("Expected shouldInstallGenesisFromSource to return false with binary URL override")
	}
}

func TestShouldInstallGenesisFromSource_Disabled(t *testing.T) {
	cfg := &config.Config{
		Genesis: config.Genesis{
			Enabled: false,
		},
	}

	provCfg := NewConfig("stackit", cfg, nil)

	if provCfg.shouldInstallGenesisFromSource() {
		t.Error("Expected shouldInstallGenesisFromSource to return false when disabled")
	}
}

func TestGetGitRepositories_IncludesGenesis(t *testing.T) {
	cfg := &config.Config{
		Genesis: config.Genesis{
			Enabled:       true,
			Branch:        "v3.1.x-dev",
			VersionPrefix: "3.1.0",
		},
		Bastion: config.Bastion{
			ToolOverrides: map[string]config.ToolOverride{},
		},
	}

	provCfg := NewConfig("stackit", cfg, nil)
	repos := provCfg.GetGitRepositories()

	if len(repos) == 0 {
		t.Fatal("Expected at least one repository")
	}

	found := false
	for _, repo := range repos {
		if repo.Name == "genesis" {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected Genesis repository to be included in git repositories")
	}
}

func TestGetGitRepositories_ExcludesGenesisWhenBinaryMode(t *testing.T) {
	cfg := &config.Config{
		Genesis: config.Genesis{
			Enabled: true,
		},
		Bastion: config.Bastion{
			ToolOverrides: map[string]config.ToolOverride{
				"genesis": {
					URL: "https://example.com/genesis",
				},
			},
		},
	}

	provCfg := NewConfig("stackit", cfg, nil)
	repos := provCfg.GetGitRepositories()

	for _, repo := range repos {
		if repo.Name == "genesis" {
			t.Error("Expected Genesis repository to NOT be included when using binary download mode")
		}
	}
}

func TestGetPostBrewPackages(t *testing.T) {
	cfg := &config.Config{}
	provCfg := NewConfig("aws", cfg, nil)
	group := provCfg.GetPostBrewPackages()

	if !group.Enabled {
		t.Error("Expected post-brew package group to be enabled")
	}

	if len(group.Packages) == 0 {
		t.Fatal("Expected post-brew package group to have packages")
	}

	required := []string{"libperl-dev", "libfuse2", "apt-rdepends", "lsb-release", "perl-doc"}
	pkgSet := make(map[string]bool, len(group.Packages))

	for _, p := range group.Packages {
		pkgSet[p] = true
	}

	for _, req := range required {
		if !pkgSet[req] {
			t.Errorf("Expected post-brew package group to contain '%s'", req)
		}
	}
}

func TestGetEssentialPackages_BrewPrerequisitesOnly(t *testing.T) {
	cfg := &config.Config{}
	provCfg := NewConfig("aws", cfg, nil)
	packages := provCfg.GetPackages()

	essential, ok := packages["essential"]
	if !ok {
		t.Fatal("Expected 'essential' package group")
	}

	if !essential.Enabled {
		t.Error("Expected essential package group to be enabled")
	}

	// Should contain brew prerequisites + system dev libs for BOSH CPI builds.
	// unzip is required by the binary_tools phase to extract tool archives and
	// cannot rely on brew (which may no-op on some bastions).
	brewPrereqs := map[string]bool{
		"build-essential": false, "procps": false, "curl": false,
		"file": false, "git": false, "ca-certificates": false,
		"ncurses-term": false, "zlib1g-dev": false, "libssl-dev": false,
		"libffi-dev": false, "unzip": false,
	}

	for _, pkg := range essential.Packages {
		if _, ok := brewPrereqs[pkg]; ok {
			brewPrereqs[pkg] = true
		} else {
			t.Errorf("Unexpected package in essential group: '%s'", pkg)
		}
	}

	for pkg, found := range brewPrereqs {
		if !found {
			t.Errorf("Expected brew prerequisite '%s' in essential group", pkg)
		}
	}
}

// --- A3: GetGenesisDeployments conditionalization ---

func TestGetGenesisDeployments_OpenbaoDefault(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "test-bloc"}
	c := NewConfig("aws", cfg, nil)
	deployments := c.GetGenesisDeployments()

	if len(deployments) == 0 {
		t.Fatal("GetGenesisDeployments returned empty slice")
	}

	// Position 1 (after bosh) must be openbao, not vault.
	if deployments[1].Name != "openbao" {
		t.Errorf("deployments[1].Name = %q, want 'openbao'", deployments[1].Name)
	}

	if deployments[1].Repo != "openbao-genesis-kit" {
		t.Errorf("deployments[1].Repo = %q, want 'openbao-genesis-kit'", deployments[1].Repo)
	}

	for _, d := range deployments {
		if d.Name == "vault" {
			t.Error("GetGenesisDeployments with default backend must not contain 'vault'")
		}
	}
}

func TestGetGenesisDeployments_VaultOptIn(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "test-bloc", SecretsBackend: "vault"}
	c := NewConfig("aws", cfg, nil)
	deployments := c.GetGenesisDeployments()

	if len(deployments) == 0 {
		t.Fatal("GetGenesisDeployments returned empty slice")
	}

	if deployments[1].Name != "vault" {
		t.Errorf("deployments[1].Name = %q, want 'vault'", deployments[1].Name)
	}

	if deployments[1].Repo != "vault-genesis-kit" {
		t.Errorf("deployments[1].Repo = %q, want 'vault-genesis-kit'", deployments[1].Repo)
	}

	for _, d := range deployments {
		if d.Name == "openbao" {
			t.Error("GetGenesisDeployments with vault backend must not contain 'openbao'")
		}
	}
}

func TestGetGenesisDeployments_LengthUnchanged(t *testing.T) {
	t.Parallel()

	cfgOpenbao := &config.Config{Name: "test-bloc"}
	cfgVault := &config.Config{Name: "test-bloc", SecretsBackend: "vault"}

	cOpenbao := NewConfig("aws", cfgOpenbao, nil)
	cVault := NewConfig("aws", cfgVault, nil)

	dOpenbao := cOpenbao.GetGenesisDeployments()
	dVault := cVault.GetGenesisDeployments()

	if len(dOpenbao) != len(dVault) {
		t.Errorf("GetGenesisDeployments length mismatch: openbao=%d vault=%d", len(dOpenbao), len(dVault))
	}
}
