package bastion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/provision"
	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
)

// Phase execution errors.
var (
	ErrLocalScriptExecutionNotImplemented = errors.New("local script execution not implemented")
	ErrKeyFetchFailed                     = errors.New("failed to fetch keys")
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

// installBrew installs Linuxbrew itself.
func (m *Manager) installBrew(ctx context.Context) error {
	m.log.Info("Installing Linuxbrew")

	brewMgr := provision.NewBrewManager(m.config.Provider, m.config)
	script := brewMgr.GenerateBrewInstallScript(ctx)

	return m.executeScript(ctx, script, "brew-install")
}

// installBrewPackages installs brew packages.
func (m *Manager) installBrewPackages(ctx context.Context) error {
	m.log.Info("Installing brew packages")

	brewMgr := provision.NewBrewManager(m.config.Provider, m.config)
	script := brewMgr.GenerateBrewPackageScript(ctx)

	return m.executeScript(ctx, script, "brew-packages")
}

// installPostBrewPackages installs APT packages that have no brew formula,
// after Linuxbrew is available (e.g. libperl-dev, libfuse2).
func (m *Manager) installPostBrewPackages(ctx context.Context) error {
	m.log.Info("Installing post-brew APT packages")

	provCfg := provision.NewConfig(m.config.Provider, m.config, nil)
	packages := map[string]provision.PackageGroup{
		"post_brew": provCfg.GetPostBrewPackages(),
	}

	script := m.buildPackageInstallScript(packages)

	return m.executeScript(ctx, script, "post-brew-apt")
}

// installCPANModules installs CPAN modules.
func (m *Manager) installCPANModules(ctx context.Context) error {
	m.log.Info("Installing CPAN modules")

	cpanMgr := provision.NewCPANManager(m.config.Provider, m.config)
	script := cpanMgr.GenerateCPANInstallScript(ctx)

	return m.executeScript(ctx, script, "cpan-modules")
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

	// Upload the OCFP binary to the bastion
	err := m.uploadOCFPBinary(ctx)
	if err != nil {
		// In OCFPOnly mode, binary upload is critical
		if m.options.OCFPOnly {
			return fmt.Errorf("OCFP binary upload failed: %w", err)
		}
		// In full init mode, continue anyway - the binary might already be there
		m.log.Warnw("Failed to upload OCFP binary, continuing with setup", "error", err)
	}

	dirMgr := provision.NewDirectoryManager(m.config.Provider, m.config)
	script := dirMgr.GenerateOCFPCLISetupScript(ctx)

	return m.executeScript(ctx, script, "ocfp-cli-setup")
}

// uploadOCFPBinary uploads the OCFP CLI binary to the bastion.
//
//nolint:funlen // sequential upload steps (checksum, transfer, install) must remain together
func (m *Manager) uploadOCFPBinary(ctx context.Context) error {
	// NOTE: Currently uploading from local build until official OCFP releases are published.
	// Once official releases are available via GitHub releases or package repositories,
	// this should be updated to download and install from the official source.
	localBinaryPath := "./build/ocfp-linux-amd64"
	remoteTempPath := "/tmp/ocfp-upload"
	remoteFinalPath := "/usr/local/bin/ocfp"

	m.log.Infow("Setting up OCFP binary", "local", localBinaryPath, "remote", remoteFinalPath)

	// Step 1: Check if remote binary exists and compare checksums
	if m.reporter != nil {
		m.reporter.ReportSubtaskProgress("ocfp_cli_setup", 1, 4, "Checking remote binary") //nolint:mnd
	}

	// Calculate local checksum
	localChecksum, err := calculateFileSHA256(localBinaryPath)
	if err != nil {
		return fmt.Errorf("failed to calculate local binary checksum: %w", err)
	}

	// Check if remote binary exists and get its checksum
	remoteChecksumCmd := fmt.Sprintf("sha256sum '%s' 2>/dev/null | awk '{print $1}' || echo ''", remoteFinalPath)

	remoteResult, err := m.sshClient.ExecuteCommand(ctx, remoteChecksumCmd)
	if err != nil {
		m.log.Debugw("Could not check remote binary checksum", "error", err)
	}

	remoteChecksum := strings.TrimSpace(remoteResult.Stdout)

	// If checksums match, skip upload
	if remoteChecksum != "" && remoteChecksum == localChecksum {
		m.log.Infow("Remote binary already up to date", "checksum", localChecksum)

		if m.reporter != nil {
			m.reporter.ReportSubtaskProgress("ocfp_cli_setup", 4, 4, "Binary already up to date (skipped upload)") //nolint:mnd
		}

		return nil
	}

	m.log.Infow("Binary update needed", "local_checksum", localChecksum, "remote_checksum", remoteChecksum)

	// Step 2: Transfer binary
	if m.reporter != nil {
		m.reporter.ReportSubtaskProgress("ocfp_cli_setup", 2, 4, "Uploading "+localBinaryPath) //nolint:mnd
	}

	// Transfer to temporary location first (user has write permissions here)
	transferOpts := ssh.TransferOptions{
		Recursive:    false,
		Preserve:     false,
		Compress:     false,
		Progress:     nil,
		MaxRetries:   0,
		ChunkSize:    0,
		Verify:       true,
		BackupRemote: false,
	}

	err = m.sshClient.TransferFile(ctx, localBinaryPath, remoteTempPath, transferOpts)
	if err != nil {
		return fmt.Errorf("failed to transfer OCFP binary to temporary location: %w", err)
	}

	// Step 3: Binary uploaded
	if m.reporter != nil {
		m.reporter.ReportSubtaskProgress("ocfp_cli_setup", 3, 4, "Binary uploaded to temporary location") //nolint:mnd
	}

	// Step 4: Install binary with sudo
	if m.reporter != nil {
		m.reporter.ReportSubtaskProgress("ocfp_cli_setup", 4, 4, "Installing to "+remoteFinalPath) //nolint:mnd
	}

	cmd := fmt.Sprintf("sudo mv '%s' '%s' && sudo chmod +x '%s'", remoteTempPath, remoteFinalPath, remoteFinalPath)

	_, err = m.sshClient.ExecuteCommand(ctx, cmd)
	if err != nil {
		// Clean up temp file on failure
		cleanupCmd := fmt.Sprintf("rm -f '%s'", remoteTempPath)
		_, _ = m.sshClient.ExecuteCommand(ctx, cleanupCmd)

		return fmt.Errorf("failed to install OCFP binary to %s: %w", remoteFinalPath, err)
	}

	m.log.Infow("OCFP binary uploaded and made executable", "checksum", localChecksum)

	return nil
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

// setupGenesisSecretsProviders configures genesis deployments to use inception vault.
func (m *Manager) setupGenesisSecretsProviders(ctx context.Context) error {
	m.log.Info("Configuring Genesis secrets providers for deployments")

	ocfpMgr := provision.NewOCFPManager(m.config.Provider, m.config, m.deploymentModes)
	script := ocfpMgr.GenerateGenesisSecretsProvidersScript(ctx)

	return m.executeScript(ctx, script, "genesis-secrets-providers")
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
		m.log.Debugw("Skipping empty script", "script", scriptName)

		return nil
	}

	if m.options.DryRun {
		m.log.Infow("DRY RUN: Would execute script", "script", scriptName)
		m.log.Debug("Script content preview",
			"script", scriptName,
			"lines", len(strings.Split(script, "\n")))

		return nil
	}

	// For remote execution via SSH client
	if m.sshClient != nil { //nolint:nestif // sequential SSH command execution with error diagnostics
		// Create script content with proper shebang and functions
		fullScript := m.wrapScriptWithFunctions(script)

		// Execute script inline to avoid file transfer for simple scripts
		cmd := "bash -c " + m.escapeShellString(fullScript)

		result, err := m.sshClient.ExecuteCommand(ctx, cmd)
		if err != nil {
			m.log.Errorw("Script execution failed",
				"script", scriptName,
				"exit_code", result.ExitCode,
				"stdout", result.Stdout,
				"stderr", result.Stderr)

			// Include meaningful script output in the error so users can diagnose failures
			output := extractTail(result.Stderr, 20) //nolint:mnd
			if output == "" {
				output = extractTail(result.Stdout, 20) //nolint:mnd
			}

			if output != "" {
				return fmt.Errorf("script %s failed: %w\n--- script output ---\n%s", scriptName, err, output)
			}

			return fmt.Errorf("script %s failed: %w", scriptName, err)
		}

		m.log.Debugw("Script executed successfully", "script", scriptName, "stdout", result.Stdout)

		return nil
	}

	// For local execution, we would use os/exec
	return ErrLocalScriptExecutionNotImplemented
}

// extractTail returns the last n lines of a string.
// If the string has fewer than n lines, the entire string is returned.
func extractTail(text string, maxLines int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	lines := strings.Split(text, "\n")
	if len(lines) <= maxLines {
		return text
	}

	return strings.Join(lines[len(lines)-maxLines:], "\n")
}

// brewShellEnvSetup sources the Linuxbrew environment so brew-installed binaries are on PATH.
// Used for bare SSH commands (e.g., verification) that don't go through wrapScriptWithFunctions.
const brewShellEnvSetup = `eval "$(/home/linuxbrew/.linuxbrew/bin/brew shellenv)" 2>/dev/null; `

// bashScriptPreamble is the shell boilerplate prepended to bastion provisioning scripts.
const bashScriptPreamble = `#!/bin/bash
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

# Suppress interactive prompts and debconf warnings
export DEBIAN_FRONTEND=noninteractive
export NEEDRESTART_MODE=a
export NEEDRESTART_SUSPEND=1

# Terminal type for tmux and curses-based tools (not set in non-PTY SSH)
export TERM="${TERM:-screen}"

# Source Linuxbrew environment if available (adds brew bins to PATH)
if [ -x /home/linuxbrew/.linuxbrew/bin/brew ]; then
    eval "$(/home/linuxbrew/.linuxbrew/bin/brew shellenv)"
fi

# Include Linuxbrew terminfo so tmux can find terminal definitions
if [ -d "/home/linuxbrew/.linuxbrew/share/terminfo" ]; then
    export TERMINFO_DIRS="${TERMINFO_DIRS:+${TERMINFO_DIRS}:}/home/linuxbrew/.linuxbrew/share/terminfo:/usr/share/terminfo:/lib/terminfo"
fi

`

// wrapScriptWithFunctions wraps script content with necessary functions.
func (m *Manager) wrapScriptWithFunctions(script string) string {
	envVars := m.getEnvironmentVariables()

	var envExports strings.Builder
	envExports.WriteString("# Export OCFP environment variables\n")

	for key, value := range envVars {
		fmt.Fprintf(&envExports, "export %s='%s'\n", key, value)
	}

	envExports.WriteString("\n")

	return bashScriptPreamble + envExports.String() + script
}

// escapeShellString escapes a string for safe shell execution.
func (m *Manager) escapeShellString(script string) string {
	// Simple escaping - in production, this would need more sophisticated escaping
	escaped := strings.ReplaceAll(script, "'", "'\"'\"'")

	return fmt.Sprintf("'%s'", escaped)
}

// calculateFileSHA256 calculates the SHA256 checksum of a file.
func calculateFileSHA256(filePath string) (string, error) {
	file, err := os.Open(filepath.Clean(filePath))
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}

	defer func() { _ = file.Close() }()

	hash := sha256.New()

	_, err = io.Copy(hash, file)
	if err != nil {
		return "", fmt.Errorf("failed to calculate hash: %w", err)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// configureBastionKeys resolves bastion.keys from config and appends any new
// keys to ~/.ssh/authorized_keys on the bastion host.
func (m *Manager) configureBastionKeys(ctx context.Context) error {
	keys := m.config.Bastion.Keys
	if len(keys) == 0 {
		m.log.Infow("No bastion keys configured, skipping")

		return nil
	}

	m.log.Infow("Configuring bastion SSH authorized keys", "key_count", len(keys))

	keyManager := ssh.NewKeyManager()

	resolved, err := ssh.ResolveKeySpecs(ctx, keys, fetchGitHubKeysHTTP, fetchGitLabKeysHTTP)
	if err != nil {
		return fmt.Errorf("failed to resolve bastion key specs: %w", err)
	}

	if len(resolved) == 0 {
		m.log.Infow("No keys resolved, skipping authorized_keys update")

		return nil
	}

	block := keyManager.FormatAuthorizedKeysBlock(resolved, keys)

	return m.appendNewKeysToBastion(ctx, block, len(resolved))
}

// appendNewKeysToBastion reads the bastion's authorized_keys, deduplicates,
// and appends any keys not already present.
func (m *Manager) appendNewKeysToBastion(ctx context.Context, block string, keyCount int) error {
	result, err := m.sshClient.ExecuteCommand(ctx, "cat ~/.ssh/authorized_keys 2>/dev/null || true")
	if err != nil {
		return fmt.Errorf("failed to read authorized_keys from bastion: %w", err)
	}

	toAppend := deduplicateKeyBlock(block, result.Stdout)
	if strings.TrimSpace(toAppend) == "" {
		m.log.Infow("All bastion keys already present in authorized_keys")

		return nil
	}

	escapedBlock := strings.ReplaceAll(toAppend, "'", "'\"'\"'")
	appendCmd := fmt.Sprintf(
		`bash -c 'mkdir -p ~/.ssh && chmod 700 ~/.ssh && printf "\n%s\n" >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys'`,
		escapedBlock)

	_, err = m.sshClient.ExecuteCommand(ctx, appendCmd)
	if err != nil {
		return fmt.Errorf("failed to append keys to authorized_keys: %w", err)
	}

	m.log.Infow("Bastion authorized keys updated", "keys_added", keyCount)

	return nil
}

// deduplicateKeyBlock returns only lines from block that are not already
// present in existing, preserving comments and blank lines.
func deduplicateKeyBlock(block, existing string) string {
	var newLines []string

	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			newLines = append(newLines, line)

			continue
		}

		if !strings.Contains(existing, line) {
			newLines = append(newLines, line)
		}
	}

	return strings.Join(newLines, "\n")
}

// fetchGitHubKeysHTTP fetches SSH public keys from GitHub for a username.
func fetchGitHubKeysHTTP(ctx context.Context, username string) ([]string, error) {
	return fetchKeysFromURL(ctx, fmt.Sprintf("https://github.com/%s.keys", username), "GitHub")
}

// fetchGitLabKeysHTTP fetches SSH public keys from GitLab for a username.
func fetchGitLabKeysHTTP(ctx context.Context, username string) ([]string, error) {
	return fetchKeysFromURL(ctx, fmt.Sprintf("https://gitlab.com/%s.keys", username), "GitLab")
}

// fetchKeysFromURL performs an HTTP GET and parses newline-delimited SSH keys.
func fetchKeysFromURL(ctx context.Context, url, provider string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build %s key request: %w", provider, err)
	}

	resp, err := http.DefaultClient.Do(req) //nolint:gosec // URL built from trusted config
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s keys: %w", provider, err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s returned %s", ErrKeyFetchFailed, provider, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s keys response: %w", provider, err)
	}

	var keys []string

	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			keys = append(keys, line)
		}
	}

	return keys, nil
}
