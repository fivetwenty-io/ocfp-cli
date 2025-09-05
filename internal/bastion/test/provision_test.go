package test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/provision"
	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// TestProvisioningConfigGeneration tests provisioning configuration generation.
func TestProvisioningConfigGeneration(t *testing.T) {
	cfg := &config.Config{
		Name:      "test-bloc",
		Provider:  "stackit",
		ProjectID: "test-project",
		Region:    "eu01",
	}

	provConfig := provision.NewConfig("stackit", cfg)

	// Test system configuration
	sysConfig := provConfig.GetSystemConfig()
	if !sysConfig.Hostname.Enabled {
		t.Error("Expected hostname configuration to be enabled")
	}

	if sysConfig.Hostname.Pattern != "${OCFP_BLOC_NAME}-bastion" {
		t.Errorf("Expected hostname pattern '${OCFP_BLOC_NAME}-bastion', got '%s'",
			sysConfig.Hostname.Pattern)
	}

	// Test directories
	directories := provConfig.GetDirectories()
	if len(directories) == 0 {
		t.Error("Expected directories to be configured")
	}

	// Check for required directories
	requiredDirs := []string{"${HOME}/ocfp/cli", "${HOME}/.ocfp/config", "${HOME}/bin"}
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

// TestScriptGeneration tests script generation functionality.
func TestScriptGeneration(t *testing.T) {
	cfg := &config.Config{
		Name:      "test-bloc",
		Provider:  "stackit",
		ProjectID: "test-project",
		Region:    "eu01",
	}

	scriptGen := provision.NewScriptGenerator("stackit", cfg)
	provConfig := provision.NewConfig("stackit", cfg)

	envVars := map[string]string{
		"OCFP_BLOC_NAME":     "test-bloc",
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
func TestSnapPackageGeneration(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
	}

	snapMgr := provision.NewSnapManager("stackit", cfg)

	snapPackages := snapMgr.GetSnapPackages()
	if len(snapPackages) == 0 {
		t.Error("Expected snap packages to be configured")
	}

	// Check for Go package
	goFound := false

	for _, pkg := range snapPackages {
		if pkg.Name == "go" {
			goFound = true

			if !pkg.Enabled {
				t.Error("Expected Go snap package to be enabled")
			}

			if !pkg.Classic {
				t.Error("Expected Go snap package to use classic confinement")
			}

			break
		}
	}

	if !goFound {
		t.Error("Expected Go snap package to be configured")
	}

	ctx := context.Background()
	script := snapMgr.GenerateSnapInstallScript(ctx)

	if script == "" {
		t.Error("Expected non-empty snap installation script")
	}

	// Check script content
	requiredContent := []string{
		"snap install go",
		"--classic",
		"snapd",
		"log_info",
	}

	for _, content := range requiredContent {
		if !strings.Contains(script, content) {
			t.Errorf("Snap script missing required content: %s", content)
		}
	}
}

// TestAdvancedToolsGeneration tests advanced binary tools script generation.
func TestAdvancedToolsGeneration(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
	}

	toolMgr := provision.NewAdvancedToolManager("stackit", cfg)

	tools := toolMgr.GetAdvancedBinaryTools()
	if len(tools) == 0 {
		t.Error("Expected advanced binary tools to be configured")
	}

	// Check for required tools
	requiredTools := []string{"yq", "ripgrep", "fly", "bun", "nvim"}
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
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
	}

	cpanMgr := provision.NewCPANManager("stackit", cfg)

	modules := cpanMgr.GetCPANModules()
	if len(modules) == 0 {
		t.Error("Expected CPAN modules to be configured")
	}

	// Check for required modules
	requiredModules := []string{"YAML::XS", "JSON::PP", "Service::Vault", "Try::Tiny"}
	moduleMap := make(map[string]bool)

	for _, module := range modules {
		moduleMap[module.Name] = module.Enabled
	}

	for _, reqModule := range requiredModules {
		if enabled, exists := moduleMap[reqModule]; !exists {
			t.Errorf("Required CPAN module not found: %s", reqModule)
		} else if !enabled {
			t.Errorf("Required CPAN module not enabled: %s", reqModule)
		}
	}

	ctx := context.Background()
	script := cpanMgr.GenerateCPANInstallScript(ctx)

	if script == "" {
		t.Error("Expected non-empty CPAN installation script")
	}

	// Check script content
	cpanContent := []string{
		"cpanm",
		"--notest",
		"perl -e",
		"YAML::XS",
		"Service::Vault",
	}

	for _, content := range cpanContent {
		if !strings.Contains(script, content) {
			t.Errorf("CPAN script missing required content: %s", content)
		}
	}
}

// TestConfigFileGeneration tests configuration file generation.
func TestConfigFileGeneration(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Bastion: config.Bastion{
			Git: config.GitConfig{
				User: config.GitUser{
					Name:  "Test User",
					Email: "test@example.com",
				},
			},
		},
	}

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
