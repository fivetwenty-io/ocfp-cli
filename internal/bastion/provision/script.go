package provision

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// ScriptGenerator generates provisioning scripts from templates.
type ScriptGenerator struct {
	config   *config.Config
	provider string
	log      logger.Logger
}

// NewScriptGenerator creates a new script generator.
func NewScriptGenerator(provider string, cfg *config.Config) *ScriptGenerator {
	return &ScriptGenerator{
		config:   cfg,
		provider: provider,
		log:      logger.Get(),
	}
}

// GenerateProvisioningScript generates the main provisioning script.
func (sg *ScriptGenerator) GenerateProvisioningScript(ctx context.Context, provConfig ProvisionConfig, envVars map[string]string) (string, error) {
	sg.log.Debug("Generating provisioning script")

	var scriptParts []string

	// Core script sections
	scriptParts = append(scriptParts, sg.generateScriptHeader())
	scriptParts = append(scriptParts, sg.generateEnvironmentSetup(envVars))

	// System and base configuration
	sg.addSystemProvisioningSections(provConfig, &scriptParts)

	// Package and tool management
	sg.addPackageManagementSections(ctx, provConfig, &scriptParts)

	// OCFP-specific provisioning
	sg.addOCFPProvisioningSections(ctx, &scriptParts)

	// Final sections
	sg.addFinalProvisioningSections(ctx, provConfig, &scriptParts)

	// Join all parts and perform variable substitution
	fullScript := strings.Join(scriptParts, "\n\n")
	finalScript := sg.performVariableSubstitution(fullScript, envVars)

	return finalScript, nil
}

// generateScriptHeader generates the script header.
func (sg *ScriptGenerator) generateScriptHeader() string {
	return `#!/bin/bash

# OCFP Bastion Provisioning Script
# Generated automatically - do not edit manually
#
# This script provisions a bastion host with all necessary tools
# and configurations for Cloud Foundry operations.

set -euo pipefail

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Logging functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1" | tee -a "${LOG_FILE}"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1" | tee -a "${LOG_FILE}"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1" | tee -a "${LOG_FILE}"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1" | tee -a "${LOG_FILE}"
}

# Setup logging
LOG_DIR="${HOME}/.ocfp/logs/provision"
mkdir -p "${LOG_DIR}"
LOG_FILE="${LOG_DIR}/bastion-init-$(date +%Y%m%d-%H%M%S).log"

log_info "Starting bastion provisioning at $(date)"
log_info "Log file: ${LOG_FILE}"

# Error handling
handle_error() {
    local line_number=$1
    local exit_code=$2
    log_error "Script failed at line ${line_number} with exit code ${exit_code}"
    log_error "Check ${LOG_FILE} for details"
    exit ${exit_code}
}

trap 'handle_error ${LINENO} $?' ERR

# Wait for system to stabilize after boot
log_info "Waiting for system to stabilize..."
sleep 10`
}

// generateEnvironmentSetup generates environment variable setup.
func (sg *ScriptGenerator) generateEnvironmentSetup(envVars map[string]string) string {
	lines := make([]string, 0, scriptBufferScriptBase+len(envVars))

	lines = append(lines, "# Environment variables setup")

	for key, value := range envVars {
		lines = append(lines, fmt.Sprintf("export %s='%s'", key, value))
	}

	lines = append(lines, "")
	lines = append(lines, "# Display environment info")
	lines = append(lines, `log_info "Environment: ${OCFP_BLOC_NAME} (${OCFP_PROVIDER})"`)

	return strings.Join(lines, "\n")
}

// generateSystemConfigScript generates system configuration.
func (sg *ScriptGenerator) generateSystemConfigScript(config SystemConfig) string {
	// Mostly fixed content; preallocate a sensible capacity
	lines := make([]string, 0, scriptBufferScript1)

	lines = append(lines, "# System configuration")

	if config.Hostname.Enabled && config.Hostname.Pattern != "" {
		lines = append(lines, "# Set hostname")
		lines = append(lines, fmt.Sprintf("HOSTNAME_PATTERN='%s'", config.Hostname.Pattern))
		lines = append(lines, "NEW_HOSTNAME=$(echo \"${HOSTNAME_PATTERN}\" | envsubst)")
		lines = append(lines, `if [ "${NEW_HOSTNAME}" != "$(hostname)" ]; then`)
		lines = append(lines, `    log_info "Setting hostname to ${NEW_HOSTNAME}"`)
		lines = append(lines, `    sudo hostnamectl set-hostname "${NEW_HOSTNAME}"`)
		// Atomically ensure /etc/hosts contains mapping for new hostname
		lines = append(lines, "    TMP_HOSTS=$(mktemp /tmp/ocfp-hosts.XXXXXX)")
		lines = append(lines, "    if [ -f /etc/hosts ]; then cp /etc/hosts \"$TMP_HOSTS\"; else : > \"$TMP_HOSTS\"; fi")
		// Remove any existing mention of NEW_HOSTNAME to avoid duplicates
		lines = append(lines, "    sudo sed -i -E \"/$NEW_HOSTNAME/d\" \"$TMP_HOSTS\"")
		// Append localhost mapping for the new hostname
		lines = append(lines, `    echo "127.0.0.1 ${NEW_HOSTNAME}" | sudo tee -a "$TMP_HOSTS" >/dev/null`)
		lines = append(lines, "    sudo install -m 0644 \"$TMP_HOSTS\" /etc/hosts")
		lines = append(lines, "    rm -f \"$TMP_HOSTS\"")
		lines = append(lines, `    if grep -q "${NEW_HOSTNAME}" /etc/hosts; then`)
		lines = append(lines, `        log_success "Hostname set to ${NEW_HOSTNAME} and /etc/hosts updated"`)
		lines = append(lines, `    else`)
		lines = append(lines, `        log_warning "Hostname set but /etc/hosts could not be verified"`)
		lines = append(lines, `    fi`)
		lines = append(lines, "fi")
		lines = append(lines, "")
	}

	if config.UpdateCache {
		lines = append(lines, "# Update package cache")
		lines = append(lines, "log_info 'Updating package cache...'")
		lines = append(lines, "sudo apt-get update -qq")
		lines = append(lines, "log_success 'Package cache updated'")
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// generateDirectoryScript generates directory creation script.
func (sg *ScriptGenerator) generateDirectoryScript(directories []DirectoryConfig) string {
	if len(directories) == 0 {
		return ""
	}

	lines := make([]string, 0, scriptBufferScript2+scriptBufferScript2*len(directories))

	lines = append(lines, "# Directory creation")

	for _, dir := range directories {
		if sg.shouldSkipCondition(dir.Condition) {
			continue
		}

		lines = append(lines, "# Create directory: "+dir.Path)
		lines = append(lines, fmt.Sprintf("DIR_PATH='%s'", dir.Path))
		lines = append(lines, "DIR_PATH=$(echo \"${DIR_PATH}\" | envsubst)")
		lines = append(lines, `log_info "Creating directory: ${DIR_PATH}"`)

		if dir.Mode != 0 {
			lines = append(lines, fmt.Sprintf("mkdir -p \"${DIR_PATH}\" && chmod %o \"${DIR_PATH}\"", dir.Mode))
		} else {
			lines = append(lines, `mkdir -p "${DIR_PATH}"`)
		}

		if dir.Owner != "" || dir.Group != "" {
			ownership := dir.Owner
			if dir.Group != "" {
				ownership += ":" + dir.Group
			}

			lines = append(lines, fmt.Sprintf(`sudo chown %s "${DIR_PATH}" 2>/dev/null || true`, ownership))
		}

		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// generateRepositoryScript generates APT repository setup.
func (sg *ScriptGenerator) generateRepositoryScript(repositories []APTRepository) string {
	if len(repositories) == 0 {
		return ""
	}

	lines := make([]string, 0, scriptBufferScript3+scriptBufferScript4*len(repositories))

	lines = append(lines, "# APT repository setup")

	for _, repo := range repositories {
		if !repo.Enabled || sg.shouldSkipCondition(repo.Condition) {
			continue
		}

		lines = append(lines, "# Add repository: "+repo.Name)

		// Download GPG key (atomic write)
		if repo.GPGKey.URL != "" {
			lines = append(lines, fmt.Sprintf("log_info 'Adding GPG key for %s repository'", repo.Name))
			lines = append(lines, fmt.Sprintf("sudo mkdir -p $(dirname '%s')", repo.GPGKey.Dest))

			lines = append(lines, "TMP_KEY=$(mktemp /tmp/ocfp-key-XXXXXX.gpg)")
			if repo.GPGKey.Dearmor {
				lines = append(lines, fmt.Sprintf("curl -fsSL '%s' | gpg --dearmor > \"$TMP_KEY\"", repo.GPGKey.URL))
			} else {
				lines = append(lines, fmt.Sprintf("curl -fsSL '%s' -o \"$TMP_KEY\"", repo.GPGKey.URL))
			}

			lines = append(lines, fmt.Sprintf("sudo install -m 0644 \"$TMP_KEY\" '%s'", repo.GPGKey.Dest))
			lines = append(lines, "rm -f \"$TMP_KEY\"")
			lines = append(lines, fmt.Sprintf("if [ -f '%s' ]; then log_success 'GPG key installed'; else log_error 'Failed to install GPG key'; fi", repo.GPGKey.Dest))
		}

		// Add repository (atomic write)
		if repo.SourceLine != "" && repo.SourceFile != "" {
			lines = append(lines, fmt.Sprintf("log_info 'Adding %s repository'", repo.Name))
			lines = append(lines, "TMP_LIST=$(mktemp /tmp/ocfp-list-XXXXXX.list)")
			lines = append(lines, fmt.Sprintf("echo '%s' > \"$TMP_LIST\"", repo.SourceLine))
			lines = append(lines, fmt.Sprintf("sudo install -m 0644 \"$TMP_LIST\" '%s'", repo.SourceFile))
			lines = append(lines, "rm -f \"$TMP_LIST\"")
			lines = append(lines, fmt.Sprintf("if grep -qF '%s' '%s'; then log_success '%s repository added'; else log_error 'Failed to add %s repository'; fi", repo.SourceLine, repo.SourceFile, repo.Name, repo.Name))
		}

		lines = append(lines, "")
	}

	if len(lines) > 1 {
		lines = append(lines, "# Update package cache after adding repositories")
		lines = append(lines, "sudo apt-get update -qq")
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// generatePackageScript generates package installation script.
func (sg *ScriptGenerator) generatePackageScript(packages map[string]PackageGroup) string {
	if len(packages) == 0 {
		return ""
	}

	lines := make([]string, 0, scriptBufferScript3+scriptBufferScript3*len(packages))

	lines = append(lines, "# Package installation")

	// Install packages by group
	for groupName, group := range packages {
		if !group.Enabled || sg.shouldSkipCondition(group.Condition) {
			continue
		}

		lines = append(lines, fmt.Sprintf("# Install %s packages", groupName))

		if len(group.Packages) > 0 {
			pkgList := strings.Join(group.Packages, " ")

			lines = append(lines, fmt.Sprintf("log_info 'Installing %s packages'", groupName))
			lines = append(lines, "sudo apt-get install -y "+pkgList)
		}

		if len(group.PipPackages) > 0 {
			pipList := strings.Join(group.PipPackages, " ")

			lines = append(lines, fmt.Sprintf("log_info 'Installing %s pip packages'", groupName))
			lines = append(lines, "pip3 install --user "+pipList)
		}

		// Verify packages
		if len(group.Verify) > 0 {
			lines = append(lines, fmt.Sprintf("log_info 'Verifying %s packages'", groupName))
			for _, cmd := range group.Verify {
				lines = append(lines, fmt.Sprintf("if ! command -v %s >/dev/null 2>&1; then", cmd))
				lines = append(lines, fmt.Sprintf("    log_warning '%s command not found after installation'", cmd))
				lines = append(lines, "fi")
			}
		}

		lines = append(lines, fmt.Sprintf("log_success '%s packages installed'", groupName))
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// generateBinaryToolScript generates binary tool installation script.
func (sg *ScriptGenerator) generateBinaryToolScript(tools []BinaryTool) string {
	if len(tools) == 0 {
		return ""
	}

	lines := make([]string, 0, scriptBufferScript5+scriptBufferScript3*len(tools))
	lines = append(lines, "# Binary tool installation")

	for _, tool := range tools {
		if !sg.shouldProcessTool(tool) {
			continue
		}

		lines = append(lines, sg.generateToolInstallationScript(tool)...)
	}

	return strings.Join(lines, "\n")
}

// generateGitRepositoryScript generates git repository cloning script.
func (sg *ScriptGenerator) generateGitRepositoryScript(repos []GitRepository) string {
	if len(repos) == 0 {
		return ""
	}

	lines := make([]string, 0, scriptBufferScript3+decimalBase*len(repos))

	lines = append(lines, "# Git repository cloning")

	for _, repo := range repos {
		if !repo.Enabled || sg.shouldSkipCondition(repo.Condition) {
			continue
		}

		lines = append(lines, fmt.Sprintf("# Clone %s repository", repo.Name))
		lines = append(lines, fmt.Sprintf("REPO_DEST='%s'", repo.Dest))
		lines = append(lines, "REPO_DEST=$(echo \"${REPO_DEST}\" | envsubst)")
		lines = append(lines, fmt.Sprintf("log_info 'Cloning %s repository to ${REPO_DEST}'", repo.Name))

		lines = append(lines, `if [ -d "${REPO_DEST}" ]; then`)
		lines = append(lines, fmt.Sprintf("    log_info '%s repository already exists, updating'", repo.Name))
		lines = append(lines, `    cd "${REPO_DEST}" && git pull`)
		lines = append(lines, "else")

		cloneCmd := fmt.Sprintf(`    git clone "%s"`, repo.URL)
		if repo.Branch != "" {
			cloneCmd += fmt.Sprintf(` -b "%s"`, repo.Branch)
		}

		if repo.Depth > 0 {
			cloneCmd += fmt.Sprintf(` --depth %d`, repo.Depth)
		}

		cloneCmd += ` "${REPO_DEST}"`

		lines = append(lines, cloneCmd)
		lines = append(lines, "fi")

		lines = append(lines, fmt.Sprintf("log_success '%s repository ready'", repo.Name))
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// generateGenesisDeploymentScript generates Genesis deployment initialization.
func (sg *ScriptGenerator) generateGenesisDeploymentScript(deployments []GenesisDeployment) string {
	if len(deployments) == 0 {
		return ""
	}

	lines := make([]string, 0, scriptBufferScript3+decimalBase*len(deployments))

	lines = append(lines, "# Genesis deployment initialization")

	for _, deployment := range deployments {
		if !deployment.Enabled || sg.shouldSkipCondition(deployment.Condition) {
			continue
		}

		lines = append(lines, fmt.Sprintf("# Initialize %s deployment", deployment.Name))
		lines = append(lines, fmt.Sprintf("log_info 'Initializing %s Genesis deployment'", deployment.Name))

		// Create deployment directory
		lines = append(lines, fmt.Sprintf("DEPLOY_DIR=\"${HOME}/deployments/%s\"", deployment.Name))
		lines = append(lines, `mkdir -p "${DEPLOY_DIR}"`)
		lines = append(lines, `cd "${DEPLOY_DIR}"`)

		// Initialize Genesis kit
		initCmd := fmt.Sprintf(`genesis init -k "%s"`, deployment.Kit)
		if deployment.Branch != "" {
			initCmd += fmt.Sprintf(` --kit-version "%s"`, deployment.Branch)
		}

		initCmd += fmt.Sprintf(` "%s"`, deployment.Name)

		lines = append(lines, initCmd)
		lines = append(lines, fmt.Sprintf("log_success '%s deployment initialized'", deployment.Name))
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// generateCustomScriptSection generates custom script execution.
func (sg *ScriptGenerator) generateCustomScriptSection(scripts []CustomScript) string {
	if len(scripts) == 0 {
		return ""
	}

	lines := make([]string, 0, scriptBufferScript3+decimalBase*len(scripts))

	lines = append(lines, "# Custom script execution")

	for _, script := range scripts {
		if !script.Enabled || sg.shouldSkipCondition(script.Condition) {
			continue
		}

		lines = append(lines, "# Execute custom script: "+script.Name)
		lines = append(lines, fmt.Sprintf("log_info 'Executing custom script: %s'", script.Name))

		if script.Content != "" {
			if script.Execute {
				// Execute inline
				lines = append(lines, "# Inline script execution")
				lines = append(lines, script.Content)
			} else if script.Path != "" {
				// Write to file
				lines = append(lines, fmt.Sprintf("cat > '%s' << 'SCRIPT_EOF'", script.Path))
				lines = append(lines, script.Content)
				lines = append(lines, "SCRIPT_EOF")

				if script.Mode != 0 {
					lines = append(lines, fmt.Sprintf("chmod %o '%s'", script.Mode, script.Path))
				}
			}
		}

		lines = append(lines, fmt.Sprintf("log_success 'Custom script %s completed'", script.Name))
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// generateScriptFooter generates the script footer.
func (sg *ScriptGenerator) generateScriptFooter() string {
	return `# Provisioning completed
log_success "Bastion provisioning completed successfully at $(date)"
log_info "Total duration: $(($(date +%s) - start_time)) seconds"
log_info "Log file: ${LOG_FILE}"

# Create completion marker
touch "${HOME}/.ocfp/provisioned"
echo "$(date)" > "${HOME}/.ocfp/provisioned"`
}

// shouldProcessTool checks if a tool should be processed.
func (sg *ScriptGenerator) shouldProcessTool(tool BinaryTool) bool {
	return tool.Enabled && !sg.shouldSkipCondition(tool.Condition)
}

// generateToolInstallationScript generates installation script lines for a single tool.
func (sg *ScriptGenerator) generateToolInstallationScript(tool BinaryTool) []string {
	var lines []string

	// Add header
	lines = append(lines, "# Install "+tool.Name)
	lines = append(lines, fmt.Sprintf("log_info 'Installing %s'", tool.Name))

	// Add download and installation steps
	lines = append(lines, sg.generateDownloadSteps(tool)...)

	// Add verification step
	lines = append(lines, sg.generateVerificationStep(tool)...)

	// Add empty line separator
	lines = append(lines, "")

	return lines
}

// generateDownloadSteps generates download and installation steps for a tool.
func (sg *ScriptGenerator) generateDownloadSteps(tool BinaryTool) []string {
	var lines []string

	if tool.URL != "" {
		lines = append(lines, sg.generateDirectDownload(tool)...)
	} else if tool.VersionURL != "" && tool.URLTemplate != "" {
		lines = append(lines, sg.generateVersionBasedDownload(tool)...)
	}

	return lines
}

// generateDirectDownload generates script lines for direct URL download.
func (sg *ScriptGenerator) generateDirectDownload(tool BinaryTool) []string {
	var lines []string

	// Download file
	lines = append(lines, fmt.Sprintf("curl -fsSL '%s' -o '/tmp/%s'", tool.URL, tool.Name))

	// Move file to destination
	lines = append(lines, sg.generateMoveCommand(tool, "/tmp/"+tool.Name)...)

	// Set permissions
	lines = append(lines, sg.generatePermissionCommand(tool)...)

	return lines
}

// generateVersionBasedDownload generates script lines for version-based download.
func (sg *ScriptGenerator) generateVersionBasedDownload(tool BinaryTool) []string {
	var lines []string

	// Get latest version
	lines = append(lines, fmt.Sprintf("LATEST_VERSION=$(curl -s '%s' | grep -oP '%s' | head -1)", tool.VersionURL, tool.VersionPattern))
	lines = append(lines, "DOWNLOAD_URL=$(echo \""+tool.URLTemplate+"\" | sed \"s/\\${VERSION}/${LATEST_VERSION}/g\")")
	lines = append(lines, fmt.Sprintf("curl -fsSL \"${DOWNLOAD_URL}\" -o '/tmp/%s'", tool.Name))

	// Extract if needed
	if tool.Extract {
		lines = append(lines, fmt.Sprintf("cd /tmp && tar -xf '%s'", tool.Name))
		lines = append(lines, sg.generateMoveCommand(tool, tool.Name)...)
	} else {
		lines = append(lines, sg.generateMoveCommand(tool, "/tmp/"+tool.Name)...)
	}

	// Set permissions
	lines = append(lines, sg.generatePermissionCommand(tool)...)

	return lines
}

// generateMoveCommand generates move command with appropriate sudo usage.
func (sg *ScriptGenerator) generateMoveCommand(tool BinaryTool, source string) []string {
	var lines []string

	if tool.Sudo {
		lines = append(lines, fmt.Sprintf("sudo mv '%s' '%s'", source, tool.Dest))
	} else {
		lines = append(lines, fmt.Sprintf("mv '%s' '%s'", source, tool.Dest))
	}

	return lines
}

// generatePermissionCommand generates chmod command if mode is specified.
func (sg *ScriptGenerator) generatePermissionCommand(tool BinaryTool) []string {
	var lines []string

	if tool.Mode != 0 {
		if tool.Sudo {
			lines = append(lines, fmt.Sprintf("sudo chmod %o '%s'", tool.Mode, tool.Dest))
		} else {
			lines = append(lines, fmt.Sprintf("chmod %o '%s'", tool.Mode, tool.Dest))
		}
	}

	return lines
}

// generateVerificationStep generates verification script lines if verification is configured.
func (sg *ScriptGenerator) generateVerificationStep(tool BinaryTool) []string {
	var lines []string

	if tool.Verify != "" {
		lines = append(lines,
			fmt.Sprintf("if %s >/dev/null 2>&1; then", tool.Verify),
			fmt.Sprintf("    log_success '%s installed successfully'", tool.Name),
			"else",
			fmt.Sprintf("    log_warning '%s installation may have failed'", tool.Name),
			"fi",
		)
	}

	return lines
}

// addSystemProvisioningSections adds system configuration and directory setup.
func (sg *ScriptGenerator) addSystemProvisioningSections(provConfig ProvisionConfig, scriptParts *[]string) {
	// System configuration
	systemConfig := provConfig.GetSystemConfig()
	sg.appendIfNotEmpty(scriptParts, sg.generateSystemConfigScript(systemConfig))

	// Directory creation
	directories := provConfig.GetDirectories()
	sg.appendIfNotEmpty(scriptParts, sg.generateDirectoryScript(directories))

	// APT repositories setup
	repositories := provConfig.GetAPTRepositories()
	sg.appendIfNotEmpty(scriptParts, sg.generateRepositoryScript(repositories))
}

// addPackageManagementSections adds package installation and tool management.
func (sg *ScriptGenerator) addPackageManagementSections(ctx context.Context, provConfig ProvisionConfig, scriptParts *[]string) {
	// Package installation
	packages := provConfig.GetPackages()
	sg.appendIfNotEmpty(scriptParts, sg.generatePackageScript(packages))

	// Binary tools installation
	tools := provConfig.GetBinaryTools()
	sg.appendIfNotEmpty(scriptParts, sg.generateBinaryToolScript(tools))

	// Git repositories
	repos := provConfig.GetGitRepositories()
	sg.appendIfNotEmpty(scriptParts, sg.generateGitRepositoryScript(repos))

	// Genesis deployments
	deployments := provConfig.GetGenesisDeployments()
	sg.appendIfNotEmpty(scriptParts, sg.generateGenesisDeploymentScript(deployments))

	// Third-party package managers
	sg.addThirdPartyPackageManagers(ctx, scriptParts)
}

// addThirdPartyPackageManagers adds snap, CPAN, and other package managers.
func (sg *ScriptGenerator) addThirdPartyPackageManagers(ctx context.Context, scriptParts *[]string) {
	// Snap packages
	snapMgr := NewSnapManager(sg.provider, sg.config)
	sg.appendIfNotEmpty(scriptParts, snapMgr.GenerateSnapInstallScript(ctx))

	// CPAN modules
	cpanMgr := NewCPANManager(sg.provider, sg.config)
	sg.appendIfNotEmpty(scriptParts, cpanMgr.GenerateCPANInstallScript(ctx))
	sg.appendIfNotEmpty(scriptParts, cpanMgr.InstallOCFPPerlDependencies(ctx))

	// Advanced binary tools
	toolMgr := NewAdvancedToolManager(sg.provider, sg.config)
	sg.appendIfNotEmpty(scriptParts, toolMgr.GenerateAdvancedToolScript(ctx))

	// CF plugins
	cfMgr := NewCFPluginManager(sg.provider, sg.config)
	sg.appendIfNotEmpty(scriptParts, cfMgr.GenerateCFPluginInstallScript(ctx))
}

// addOCFPProvisioningSections adds OCFP-specific configuration and setup.
func (sg *ScriptGenerator) addOCFPProvisioningSections(ctx context.Context, scriptParts *[]string) {
	// Configuration files
	configMgr := NewConfigFileManager(sg.provider, sg.config)
	sg.appendIfNotEmpty(scriptParts, configMgr.GenerateConfigFileScript(ctx))

	// OCFP directory structure and CLI setup
	sg.addOCFPDirectoryAndCLISetup(ctx, scriptParts)

	// Environment setup
	sg.addEnvironmentSetup(ctx, scriptParts)

	// OCFP integration
	sg.addOCFPIntegration(ctx, scriptParts)
}

// addOCFPDirectoryAndCLISetup adds OCFP directory structure and CLI setup.
func (sg *ScriptGenerator) addOCFPDirectoryAndCLISetup(ctx context.Context, scriptParts *[]string) {
	dirMgr := NewDirectoryManager(sg.provider, sg.config)
	sg.appendIfNotEmpty(scriptParts, dirMgr.GenerateOCFPDirectoryScript(ctx))
	sg.appendIfNotEmpty(scriptParts, dirMgr.GenerateOCFPCLISetupScript(ctx))
}

// addEnvironmentSetup adds shell and system environment setup.
func (sg *ScriptGenerator) addEnvironmentSetup(ctx context.Context, scriptParts *[]string) {
	envMgr := NewEnvironmentManager(sg.provider, sg.config)
	sg.appendIfNotEmpty(scriptParts, envMgr.GenerateShellEnvironmentScript(ctx))
	sg.appendIfNotEmpty(scriptParts, envMgr.GenerateSystemEnvironmentScript(ctx))
}

// addOCFPIntegration adds OCFP integration components.
func (sg *ScriptGenerator) addOCFPIntegration(ctx context.Context, scriptParts *[]string) {
	ocfpMgr := NewOCFPManager(sg.provider, sg.config)
	sg.appendIfNotEmpty(scriptParts, ocfpMgr.GenerateVaultInceptionScript(ctx))
	sg.appendIfNotEmpty(scriptParts, ocfpMgr.GenerateOCFPConfigureScript(ctx))
	sg.appendIfNotEmpty(scriptParts, ocfpMgr.GenerateVaultPopulateScript(ctx))
	sg.appendIfNotEmpty(scriptParts, ocfpMgr.GenerateOCFPToolVerificationScript(ctx))
}

// addFinalProvisioningSections adds custom scripts and verification.
func (sg *ScriptGenerator) addFinalProvisioningSections(ctx context.Context, provConfig ProvisionConfig, scriptParts *[]string) {
	// Custom scripts
	scripts := provConfig.GetCustomScripts()
	sg.appendIfNotEmpty(scriptParts, sg.generateCustomScriptSection(scripts))

	// Comprehensive verification and summary
	verificationMgr := NewVerificationManager(sg.provider, sg.config)
	sg.appendIfNotEmpty(scriptParts, verificationMgr.GenerateVerificationScript(ctx))
	sg.appendIfNotEmpty(scriptParts, verificationMgr.GenerateProvisioningSummaryScript(ctx))

	// Script footer
	*scriptParts = append(*scriptParts, sg.generateScriptFooter())
}

// appendIfNotEmpty appends a script part to the slice if it's not empty.
func (sg *ScriptGenerator) appendIfNotEmpty(scriptParts *[]string, part string) {
	if part != "" {
		*scriptParts = append(*scriptParts, part)
	}
}

// shouldSkipCondition evaluates whether a condition should be skipped.
func (sg *ScriptGenerator) shouldSkipCondition(condition string) bool {
	if condition == "" {
		return false
	}

	switch condition {
	case condProviderIsStackit:
		return sg.provider != providerStackit
	case condProviderIsAWS:
		return sg.provider != providerAWS
	case condProviderIsAzure:
		return sg.provider != providerAzure
	case condProviderIsGCP:
		return sg.provider != providerGCP
	case condProviderIsOpenstack:
		return sg.provider != providerOpenStack
	case condProviderIsVMware:
		return sg.provider != providerVMware && sg.provider != providerVsphere
	default:
		// Unknown condition, don't skip
		return false
	}
}

// performVariableSubstitution performs variable substitution in the script.
func (sg *ScriptGenerator) performVariableSubstitution(script string, envVars map[string]string) string {
	// Add standard variables
	vars := map[string]string{
		"HOME": "${HOME}",
		"USER": "${USER}",
	}

	// Add environment variables
	for k, v := range envVars {
		vars[k] = v
	}

	// Perform substitution
	tmpl := template.New("script")
	tmpl.Delims("${", "}")

	// Custom template functions
	funcMap := template.FuncMap{
		"env": func(key string) string {
			if val, ok := vars[key]; ok {
				return val
			}

			return "${" + key + "}"
		},
	}
	tmpl.Funcs(funcMap)

	// Simple variable substitution using regex
	re := regexp.MustCompile(`\$\{([^}]+)(?:\:-([^}]*))?\}`)

	result := re.ReplaceAllStringFunc(script, func(match string) string {
		// Extract variable name and default value
		parts := re.FindStringSubmatch(match)
		if len(parts) < minScriptParts {
			return match
		}

		varName := parts[1]

		defaultValue := ""
		if len(parts) > maxScriptParts {
			defaultValue = parts[2]
		}

		// Look up variable
		if val, ok := vars[varName]; ok && val != "" {
			return val
		}

		// Use default if available
		if defaultValue != "" {
			return defaultValue
		}

		// Return original for shell expansion
		return "${" + varName + "}"
	})

	return result
}
