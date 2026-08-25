package test_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/deployments"
	"github.com/ocfp/ocfp-cli-go/internal/bastion/provision"
	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// TestProvisioningConfigGeneration tests provisioning configuration generation.
func TestProvisioningConfigGeneration(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestConfig().WithRegion("eu01").WithProjectID("test-project").Build()

	provConfig := provision.NewConfig("stackit", cfg, deployments.NewResolver(cfg))

	// Test system configuration
	sysConfig := provConfig.GetSystemConfig()
	if !sysConfig.Hostname.Enabled {
		t.Error("Expected hostname configuration to be enabled")
	}

	if sysConfig.Hostname.Pattern != "${OCFP_BLOC}-bastion" {
		t.Errorf("Expected hostname pattern '${OCFP_BLOC}-bastion', got '%s'",
			sysConfig.Hostname.Pattern)
	}

	// Test directories
	directories := provConfig.GetDirectories()
	if len(directories) == 0 {
		t.Error("Expected directories to be configured")
	}

	// Check for required directories
	requiredDirs := []string{"${HOME}/ocfp/cli", "${HOME}/.ocfp", "${HOME}/bin"}
	foundDirs := make(map[string]bool)

	for _, dir := range directories {
		foundDirs[dir.Path] = true
	}

	for _, reqDir := range requiredDirs {
		if !foundDirs[reqDir] {
			t.Errorf("Required directory not found: %s", reqDir)
		}
	}

	// Test packages
	packages := provConfig.GetPackages()
	if len(packages) == 0 {
		t.Error("Expected packages to be configured")
	}

	// Check for essential packages
	if essentialPkg, exists := packages["essential"]; exists {
		if !essentialPkg.Enabled {
			t.Error("Expected essential packages to be enabled")
		}

		if len(essentialPkg.Packages) == 0 {
			t.Error("Expected essential packages to have package list")
		}
	} else {
		t.Error("Expected essential packages group")
	}

	// Test provider-specific packages
	if stackitPkg, exists := packages["stackit"]; exists {
		if !stackitPkg.Enabled {
			t.Error("Expected STACKIT packages to be enabled for STACKIT provider")
		}
	} else {
		t.Error("Expected STACKIT-specific packages for STACKIT provider")
	}
}

func TestProvisionScriptIncludesDeploymentRepoSetup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.NewTestConfig().WithRegion("eu01").WithProjectID("test-project").Build()
	cfg.Deployments = config.NewDeploymentSettings("git@example.com:ocfp/deployments.git", map[string]*config.DeploymentEntry{
		"bosh":  {Mode: config.DeploymentModeRelease},
		"vault": {Mode: config.DeploymentModeDev},
	})

	scriptGen := provision.NewScriptGenerator("stackit", cfg)
	provConfig := provision.NewConfig("stackit", cfg, deployments.NewResolver(cfg))

	envVars := map[string]string{
		"OCFP_BLOC":          "test-bloc",
		"OCFP_PROVIDER":      "stackit",
		"STACKIT_PROJECT_ID": "test-project",
		"STACKIT_REGION":     "eu01",
	}

	script, err := scriptGen.GenerateProvisioningScript(context.Background(), provConfig, envVars)
	if err != nil {
		t.Fatalf("failed to generate provisioning script: %v", err)
	}

	if !strings.Contains(script, `GLOBAL_DEPLOYMENTS_URL="git@example.com:ocfp/deployments.git"`) {
		t.Fatalf("expected global deployments url in script\nscript: %s", script)
	}

	if !strings.Contains(script, `DEV_DEPLOYMENTS=("vault")`) {
		t.Fatalf("expected dev deployments array to include vault\nscript: %s", script)
	}

	if !strings.Contains(script, `RELEASE_DEPLOYMENTS=("bosh"`) {
		t.Fatalf("expected release deployments array to list bosh\nscript: %s", script)
	}

	if !strings.Contains(script, `git clone "$GLOBAL_DEPLOYMENTS_URL" "${DEPLOYMENTS_ROOT}"`) {
		t.Fatalf("expected deployments repo clone command in script\nscript: %s", script)
	}

	if !strings.Contains(script, `ln -sfn "$KIT_DIR" "${DEPLOYMENTS_ROOT}/${deployment}/dev"`) {
		t.Fatalf("expected dev kit symlink in script\nscript: %s", script)
	}

	// The provisioning script must never invoke the command that generates it.
	// `ocfp configure` provisions the bastion, and provisioning builds and runs
	// this script, so the trailing `ocfp configure deployments` call had the two
	// spawning each other without bound and `ocfp init bastion` never returned.
	if strings.Contains(script, `"${OCFP_CLI_PATH}" configure`) {
		t.Fatalf("provisioning script re-invokes ocfp configure, which re-enters provisioning\nscript: %s", script)
	}
}

func TestScriptGeneration(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestConfig().WithRegion("eu01").WithProjectID("test-project").Build()

	scriptGen := provision.NewScriptGenerator("stackit", cfg)
	provConfig := provision.NewConfig("stackit", cfg, deployments.NewResolver(cfg))

	envVars := map[string]string{
		"OCFP_BLOC":          "test-bloc",
		"OCFP_PROVIDER":      "stackit",
		"STACKIT_PROJECT_ID": "test-project",
		"STACKIT_REGION":     "eu01",
	}

	ctx := context.Background()

	script, err := scriptGen.GenerateProvisioningScript(ctx, provConfig, envVars)
	if err != nil {
		t.Fatalf("Failed to generate provisioning script: %v", err)
	}

	if script == "" {
		t.Error("Expected non-empty script")
	}

	// Check for required script components
	requiredComponents := []string{
		"#!/bin/bash",
		"log_info",
		"log_success",
		"log_error",
		"OCFP Environment Configuration",
		"apt-get",
		"mkdir -p",
	}

	for _, component := range requiredComponents {
		if !strings.Contains(script, component) {
			t.Errorf("Script missing required component: %s", component)
		}
	}

	// Check for provider-specific content
	stackitComponents := []string{
		"STACKIT_PROJECT_ID",
		"stackit",
	}

	for _, component := range stackitComponents {
		if !strings.Contains(script, component) {
			t.Errorf("Script missing STACKIT-specific component: %s", component)
		}
	}
}

// TestSnapPackageGeneration tests snap package script generation.
// Snap packages have been deprecated in favor of Linuxbrew.
func TestSnapPackageGeneration(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestConfig().Build()

	snapMgr := provision.NewSnapManager("stackit", cfg)

	snapPackages := snapMgr.GetSnapPackages()
	if len(snapPackages) != 0 {
		t.Errorf("Expected empty snap package list (deprecated), got %d packages", len(snapPackages))
	}

	ctx := context.Background()
	script := snapMgr.GenerateSnapInstallScript(ctx)

	if script != "" {
		t.Error("Expected empty snap installation script (deprecated)")
	}
}

// TestBrewPackageGeneration tests brew package script generation.
func TestBrewPackageGeneration(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestConfig().Build()

	brewMgr := provision.NewBrewManager("stackit", cfg)

	brewPackages := brewMgr.GetBrewPackages()
	if len(brewPackages) == 0 {
		t.Error("Expected brew packages to be configured")
	}

	// Check for Go package (migrated from snap)
	goFound := false

	for _, pkg := range brewPackages {
		if pkg.Name == "go" {
			goFound = true

			if !pkg.Enabled {
				t.Error("Expected Go brew package to be enabled")
			}

			if pkg.Version != "1.27" {
				t.Errorf("Expected Go version '1.27', got '%s'", pkg.Version)
			}

			break
		}
	}

	if !goFound {
		t.Error("Expected Go brew package to be configured")
	}

	ctx := context.Background()
	script := brewMgr.GenerateBrewPackageScript(ctx)

	if script == "" {
		t.Error("Expected non-empty brew installation script")
	}

	// Check script content
	requiredContent := []string{
		"brew install",
		"go@1.27",
		"HOMEBREW_NO_AUTO_UPDATE",
		"brew shellenv",
	}

	for _, content := range requiredContent {
		if !strings.Contains(script, content) {
			t.Errorf("Brew script missing required content: %s", content)
		}
	}
}

// TestAdvancedToolsGeneration tests advanced binary tools script generation.
func TestAdvancedToolsGeneration(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestConfig().Build()

	toolMgr := provision.NewAdvancedToolManager("stackit", cfg)

	tools := toolMgr.GetAdvancedBinaryTools()
	if len(tools) == 0 {
		t.Error("Expected advanced binary tools to be configured")
	}

	// Check for required tools (yq and ripgrep moved to brew, now disabled here)
	requiredTools := []string{"fly", "bun"}
	brewDisabledTools := []string{"yq", "ripgrep", "vault"}
	toolMap := make(map[string]bool)

	for _, tool := range tools {
		toolMap[tool.Name] = tool.Enabled
	}

	for _, reqTool := range requiredTools {
		if enabled, exists := toolMap[reqTool]; !exists {
			t.Errorf("Required tool not found: %s", reqTool)
		} else if !enabled {
			t.Errorf("Required tool not enabled: %s", reqTool)
		}
	}

	for _, brewTool := range brewDisabledTools {
		if enabled, exists := toolMap[brewTool]; exists && enabled {
			t.Errorf("Expected tool '%s' to be disabled (installed via brew)", brewTool)
		}
	}

	ctx := context.Background()
	script := toolMgr.GenerateAdvancedToolScript(ctx)

	if script == "" {
		t.Error("Expected non-empty advanced tools script")
	}

	// Check for version detection logic
	versionContent := []string{
		"LATEST_VERSION=",
		"curl -s",
		"github.com",
		"releases/latest",
	}

	for _, content := range versionContent {
		if !strings.Contains(script, content) {
			t.Errorf("Advanced tools script missing version detection content: %s", content)
		}
	}
}

// TestCPANModuleGeneration tests CPAN module installation script generation.
func TestCPANModuleGeneration(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestConfig().Build()

	cpanMgr := provision.NewCPANManager("stackit", cfg)

	modules := cpanMgr.GetCPANModules()

	// Verify all expected modules are present (networking + debugging)
	expectedModules := map[string]struct{}{
		"Net::IP":         {},
		"NetAddr::IP":     {},
		"JSON":            {},
		"Net::CIDR":       {},
		"YAML":            {},
		"YAML::LibYAML":   {},
		"Pry":             {},
		"Carp::Always":    {},
		"Smart::Comments": {},
	}

	if len(modules) != len(expectedModules) {
		t.Fatalf("Expected %d CPAN modules, got %d", len(expectedModules), len(modules))
	}

	for _, module := range modules {
		if _, ok := expectedModules[module.Name]; !ok {
			t.Fatalf("Unexpected CPAN module configured: %s", module.Name)
		}
		if !module.Enabled {
			t.Fatalf("Expected CPAN module %s to be enabled", module.Name)
		}
		delete(expectedModules, module.Name)
	}

	if len(expectedModules) != 0 {
		t.Fatalf("Missing expected CPAN modules: %v", expectedModules)
	}

	ctx := context.Background()
	script := cpanMgr.GenerateCPANInstallScript(ctx)

	if script == "" {
		t.Error("Expected non-empty CPAN installation script")
	}

	// Check script content includes networking and debugging modules
	cpanContent := []string{
		"cpanm",
		"--notest",
		"perl -e",
		"Net::IP",
		"Net::CIDR",
		"YAML",
		"Pry",
		"Carp::Always",
		"Smart::Comments",
	}

	for _, content := range cpanContent {
		if !strings.Contains(script, content) {
			t.Errorf("CPAN script missing required content: %s", content)
		}
	}
}

// TestConfigFileGeneration tests configuration file generation.
func TestConfigFileGeneration(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestConfig().WithBastion(config.TestBastionConfig()).Build()

	configMgr := provision.NewConfigFileManager("stackit", cfg)

	configFiles := configMgr.GetConfigFiles()
	if len(configFiles) == 0 {
		t.Error("Expected configuration files to be configured")
	}

	// Check for required config files
	requiredConfigs := []string{"tmux", "replyrc", "genesis", "gitconfig"}
	configMap := make(map[string]bool)

	for _, configFile := range configFiles {
		configMap[configFile.Name] = configFile.Enabled
	}

	for _, reqConfig := range requiredConfigs {
		if _, exists := configMap[reqConfig]; !exists {
			t.Errorf("Required config file not found: %s", reqConfig)
		}
	}

	ctx := context.Background()
	script := configMgr.GenerateConfigFileScript(ctx)

	if script == "" {
		t.Error("Expected non-empty config file script")
	}

	// Check script content for tmux config
	tmuxContent := []string{
		".tmux.conf",
		"mouse on",
		"CONFIG_EOF",
	}

	for _, content := range tmuxContent {
		if !strings.Contains(script, content) {
			t.Errorf("Config script missing tmux content: %s", content)
		}
	}
}
