package bastion

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/provision"
)

// Phase execution errors.
var (
	ErrLocalScriptExecutionNotImplemented = errors.New("local script execution not implemented")
)

// Additional phase implementations for comprehensive bastion initialization

// runPrerequisiteChecks performs prerequisite validation.
func (m *Manager) runPrerequisiteChecks(ctx context.Context) error {
	m.log.Info("Running prerequisite checks")

	verificationMgr := provision.NewVerificationManager(m.config.Provider, m.config)
	script := verificationMgr.GeneratePreRequisiteCheckScript(ctx)

	return m.executeScript(ctx, script, "prerequisite-checks")
}

// setupOCFPDirectories sets up OCFP-specific directory structure.
func (m *Manager) setupOCFPDirectories(ctx context.Context) error {
	m.log.Info("Setting up OCFP directories")

	dirMgr := provision.NewDirectoryManager(m.config.Provider, m.config)
	script := dirMgr.GenerateOCFPDirectoryScript(ctx)

	return m.executeScript(ctx, script, "ocfp-directories")
}

// installSnapPackages installs snap packages.
func (m *Manager) installSnapPackages(ctx context.Context) error {
	m.log.Info("Installing snap packages")

	snapMgr := provision.NewSnapManager(m.config.Provider, m.config)
	script := snapMgr.GenerateSnapInstallScript(ctx)

	return m.executeScript(ctx, script, "snap-packages")
}

// installCPANModules installs CPAN modules.
func (m *Manager) installCPANModules(ctx context.Context) error {
	m.log.Info("Installing CPAN modules")

	cpanMgr := provision.NewCPANManager(m.config.Provider, m.config)

	// Install core CPAN modules
	script := cpanMgr.GenerateCPANInstallScript(ctx)

	err := m.executeScript(ctx, script, "cpan-modules")
	if err != nil {
		return err
	}

	// Install OCFP Perl dependencies
	ocfpScript := cpanMgr.InstallOCFPPerlDependencies(ctx)

	return m.executeScript(ctx, ocfpScript, "ocfp-perl-deps")
}

// installCFPlugins installs CloudFoundry plugins.
func (m *Manager) installCFPlugins(ctx context.Context) error {
	m.log.Info("Installing CloudFoundry plugins")

	cfMgr := provision.NewCFPluginManager(m.config.Provider, m.config)
	script := cfMgr.GenerateCFPluginInstallScript(ctx)

	return m.executeScript(ctx, script, "cf-plugins")
}

// createConfigFiles creates configuration files.
func (m *Manager) createConfigFiles(ctx context.Context) error {
	m.log.Info("Creating configuration files")

	configMgr := provision.NewConfigFileManager(m.config.Provider, m.config)
	script := configMgr.GenerateConfigFileScript(ctx)

	return m.executeScript(ctx, script, "config-files")
}

// setupShellEnvironment configures shell environment (.bashrc, aliases).
func (m *Manager) setupShellEnvironment(ctx context.Context) error {
	m.log.Info("Setting up shell environment")

	envMgr := provision.NewEnvironmentManager(m.config.Provider, m.config)
	script := envMgr.GenerateShellEnvironmentScript(ctx)

	return m.executeScript(ctx, script, "shell-environment")
}

// setupSystemEnvironment configures system environment (/etc/environment, /etc/profile.d).
func (m *Manager) setupSystemEnvironment(ctx context.Context) error {
	m.log.Info("Setting up system environment")

	envMgr := provision.NewEnvironmentManager(m.config.Provider, m.config)
	script := envMgr.GenerateSystemEnvironmentScript(ctx)

	return m.executeScript(ctx, script, "system-environment")
}

// setupOCFPCLI sets up OCFP CLI.
func (m *Manager) setupOCFPCLI(ctx context.Context) error {
	m.log.Info("Setting up OCFP CLI")

	dirMgr := provision.NewDirectoryManager(m.config.Provider, m.config)
	script := dirMgr.GenerateOCFPCLISetupScript(ctx)

	return m.executeScript(ctx, script, "ocfp-cli-setup")
}

// setupVaultInception runs vault inception.
func (m *Manager) setupVaultInception(ctx context.Context) error {
	m.log.Info("Setting up vault inception")

	ocfpMgr := provision.NewOCFPManager(m.config.Provider, m.config, m.deploymentModes)
	script := ocfpMgr.GenerateVaultInceptionScript(ctx)

	return m.executeScript(ctx, script, "vault-inception")
}

// runOCFPConfigure runs OCFP configure deployments.
func (m *Manager) runOCFPConfigure(ctx context.Context) error {
	m.log.Info("Running OCFP configure deployments")

	ocfpMgr := provision.NewOCFPManager(m.config.Provider, m.config, m.deploymentModes)
	script := ocfpMgr.GenerateOCFPConfigureScript(ctx)

	return m.executeScript(ctx, script, "ocfp-configure")
}

// runVaultPopulate runs OCFP vault populate.
func (m *Manager) runVaultPopulate(ctx context.Context) error {
	m.log.Info("Running vault populate")

	ocfpMgr := provision.NewOCFPManager(m.config.Provider, m.config, m.deploymentModes)
	script := ocfpMgr.GenerateVaultPopulateScript(ctx)

	return m.executeScript(ctx, script, "vault-populate")
}

// runHealthCheck performs comprehensive health check.
func (m *Manager) runHealthCheck(ctx context.Context) error {
	m.log.Info("Running health check")

	verificationMgr := provision.NewVerificationManager(m.config.Provider, m.config)
	script := verificationMgr.GenerateHealthCheckScript(ctx)

	return m.executeScript(ctx, script, "health-check")
}

// executeScript is a helper method to execute generated scripts.
func (m *Manager) executeScript(ctx context.Context, script, scriptName string) error {
	if script == "" {
		m.log.Debug("Skipping empty script", "script", scriptName)

		return nil
	}

	if m.options.DryRun {
		m.log.Info("DRY RUN: Would execute script", "script", scriptName)
		m.log.Debug("Script content preview",
			"script", scriptName,
			"lines", len(strings.Split(script, "\n")))

		return nil
	}

	// For remote execution via SSH client
	if m.sshClient != nil {
		// Create script content with proper shebang and functions
		fullScript := m.wrapScriptWithFunctions(script)

		// Execute script inline to avoid file transfer for simple scripts
		cmd := "bash -c " + m.escapeShellString(fullScript)

		result, err := m.sshClient.ExecuteCommand(ctx, cmd)
		if err != nil {
			m.log.Error("Script execution failed",
				"script", scriptName,
				"exit_code", result.ExitCode,
				"stderr", result.Stderr)

			return fmt.Errorf("script %s failed: %w", scriptName, err)
		}

		m.log.Debug("Script executed successfully", "script", scriptName)

		return nil
	}

	// For local execution, we would use os/exec
	return ErrLocalScriptExecutionNotImplemented
}

// wrapScriptWithFunctions wraps script content with necessary functions.
func (m *Manager) wrapScriptWithFunctions(script string) string {
	functions := `#!/bin/bash
set -euo pipefail

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m' 
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Logging functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Initialize script variables
export USER=$(whoami)
export start_time=$(date +%s)
LOG_DIR="${HOME}/.ocfp/logs/provision"
mkdir -p "${LOG_DIR}"
LOG_FILE="${LOG_DIR}/bastion-init-$(date +%Y%m%d-%H%M%S).log"

log_info "Starting script execution at $(date)"
log_info "Log file: ${LOG_FILE}"

`

	return functions + "\n" + script
}

// escapeShellString escapes a string for safe shell execution.
func (m *Manager) escapeShellString(script string) string {
	// Simple escaping - in production, this would need more sophisticated escaping
	escaped := strings.ReplaceAll(script, "'", "'\"'\"'")

	return fmt.Sprintf("'%s'", escaped)
}
