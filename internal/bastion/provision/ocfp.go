package provision

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// OCFPManager handles OCFP-specific provisioning tasks
type OCFPManager struct {
	config   *config.Config
	provider string
	log      logger.Logger
}

// NewOCFPManager creates a new OCFP manager
func NewOCFPManager(provider string, cfg *config.Config) *OCFPManager {
	return &OCFPManager{
		config:   cfg,
		provider: provider,
		log:      logger.Get(),
	}
}

// GenerateVaultInceptionScript generates script for vault inception setup
func (om *OCFPManager) GenerateVaultInceptionScript(ctx context.Context) string {
	var lines []string
	lines = append(lines, "# Vault inception setup")
	lines = append(lines, "")

	// Check if vault-inception script exists
	lines = append(lines, "# Check for vault-inception script")
	lines = append(lines, "VAULT_INCEPTION_LOCATIONS=(")
	lines = append(lines, `    "${HOME}/ocfp/cli/scripts/provision/vault-inception"`)
	lines = append(lines, `    "${HOME}/ocfp/ocfp-cli/scripts/provision/vault-inception"`)
	lines = append(lines, ")")
	lines = append(lines, "")

	lines = append(lines, "VAULT_INCEPTION_SCRIPT=\"\"")
	lines = append(lines, "for location in \"${VAULT_INCEPTION_LOCATIONS[@]}\"; do")
	lines = append(lines, "    if [ -f \"$location\" ]; then")
	lines = append(lines, "        VAULT_INCEPTION_SCRIPT=\"$location\"")
	lines = append(lines, "        log_info \"Found vault-inception script at: $location\"")
	lines = append(lines, "        break")
	lines = append(lines, "    fi")
	lines = append(lines, "done")
	lines = append(lines, "")

	lines = append(lines, "if [ -z \"$VAULT_INCEPTION_SCRIPT\" ]; then")
	lines = append(lines, "    log_error 'vault-inception script not found!'")
	lines = append(lines, "    log_error 'Expected at: ~/ocfp/cli/scripts/provision/vault-inception'")
	lines = append(lines, "    return 1")
	lines = append(lines, "fi")
	lines = append(lines, "")

	// Make script executable
	lines = append(lines, "if [ ! -x \"$VAULT_INCEPTION_SCRIPT\" ]; then")
	lines = append(lines, "    log_info 'Making vault-inception script executable'")
	lines = append(lines, "    chmod +x \"$VAULT_INCEPTION_SCRIPT\"")
	lines = append(lines, "fi")
	lines = append(lines, "")

	// Execute vault inception script
	lines = append(lines, "log_info 'Running vault-inception script'")
	lines = append(lines, "perl \"$VAULT_INCEPTION_SCRIPT\"")
	lines = append(lines, "VAULT_INCEPTION_EXIT=$?")
	lines = append(lines, "")

	lines = append(lines, "if [ $VAULT_INCEPTION_EXIT -eq 0 ]; then")
	lines = append(lines, "    log_success 'Vault inception completed successfully'")
	lines = append(lines, "else")
	lines = append(lines, "    log_error \"Vault inception failed with exit code $VAULT_INCEPTION_EXIT\"")
	lines = append(lines, "    # Check if it's because vault is already set up")
	lines = append(lines, "    if safe target 2>&1 | grep -q 'inception\\|production'; then")
	lines = append(lines, "        log_success 'Vault already configured'")
	lines = append(lines, "    else")
	lines = append(lines, "        return 1")
	lines = append(lines, "    fi")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GenerateOCFPConfigureScript generates script for OCFP configure deployments
func (om *OCFPManager) GenerateOCFPConfigureScript(ctx context.Context) string {
	var lines []string
	lines = append(lines, "# OCFP deployments setup")
	lines = append(lines, "")

	// Check if OCFP CLI is available
	lines = append(lines, "# Check for OCFP CLI")
	lines = append(lines, "OCFP_CLI_LOCATIONS=(")
	lines = append(lines, `    "${HOME}/ocfp/ocfp-cli/bin/ocfp"`)
	lines = append(lines, `    "${HOME}/ocfp/cli/bin/ocfp"`)
	lines = append(lines, `    "${HOME}/bin/ocfp"`)
	lines = append(lines, ")")
	lines = append(lines, "")

	lines = append(lines, "OCFP_CLI_PATH=\"\"")
	lines = append(lines, "for location in \"${OCFP_CLI_LOCATIONS[@]}\"; do")
	lines = append(lines, "    if [ -f \"$location\" ]; then")
	lines = append(lines, "        OCFP_CLI_PATH=\"$location\"")
	lines = append(lines, "        log_info \"Found OCFP CLI at: $location\"")
	lines = append(lines, "        break")
	lines = append(lines, "    fi")
	lines = append(lines, "done")
	lines = append(lines, "")

	lines = append(lines, "if [ -z \"$OCFP_CLI_PATH\" ]; then")
	lines = append(lines, "    log_warning 'OCFP CLI not found, skipping deployment setup'")
	lines = append(lines, "    return 0")
	lines = append(lines, "fi")
	lines = append(lines, "")

	// Find configuration file
	lines = append(lines, "# Find configuration file")
	lines = append(lines, "CONFIG_FILE=\"\"")
	lines = append(lines, "CONFIG_LOCATIONS=(")
	lines = append(lines, `    "${HOME}/.ocfp/config.yml"`)
	lines = append(lines, `    "${HOME}/.ocfp/config/config.yml"`)
	lines = append(lines, ")")
	lines = append(lines, "")

	lines = append(lines, "for location in \"${CONFIG_LOCATIONS[@]}\"; do")
	lines = append(lines, "    if [ -f \"$location\" ]; then")
	lines = append(lines, "        CONFIG_FILE=\"$location\"")
	lines = append(lines, "        log_info \"Found config file at: $location\"")
	lines = append(lines, "        break")
	lines = append(lines, "    fi")
	lines = append(lines, "done")
	lines = append(lines, "")

	lines = append(lines, "if [ -z \"$CONFIG_FILE\" ]; then")
	lines = append(lines, "    log_warning 'Configuration file not found, skipping deployment setup'")
	lines = append(lines, "    return 0")
	lines = append(lines, "fi")
	lines = append(lines, "")

	// Change to OCFP CLI directory
	lines = append(lines, "# Change to OCFP CLI directory")
	lines = append(lines, "OCFP_CLI_DIR=$(dirname \"$OCFP_CLI_PATH\")")
	lines = append(lines, "OCFP_CLI_DIR=$(dirname \"$OCFP_CLI_DIR\")") // Go up one level from bin/
	lines = append(lines, "log_info \"Changing to OCFP CLI directory: $OCFP_CLI_DIR\"")
	lines = append(lines, "cd \"$OCFP_CLI_DIR\"")
	lines = append(lines, "")

	// Run OCFP configure deployments
	lines = append(lines, "# Run OCFP configure deployments")
	lines = append(lines, "log_info 'Running OCFP configure to setup deployments'")
	lines = append(lines, "CONFIGURE_CMD=\"perl bin/ocfp configure deployments --bloc '${OCFP_BLOC_NAME}'\"")
	lines = append(lines, "log_info \"Executing: $CONFIGURE_CMD\"")
	lines = append(lines, "")

	lines = append(lines, "eval $CONFIGURE_CMD")
	lines = append(lines, "CONFIGURE_EXIT=$?")
	lines = append(lines, "")

	lines = append(lines, "if [ $CONFIGURE_EXIT -eq 0 ]; then")
	lines = append(lines, "    log_success 'Genesis deployments setup completed successfully'")
	lines = append(lines, "    # List created deployments")
	lines = append(lines, "    if [ -d \"$HOME/ocfp\" ]; then")
	lines = append(lines, "        log_info 'Created ~/ocfp directories:'")
	lines = append(lines, "        find \"$HOME/ocfp\" -maxdepth 1 -type d -not -name ocfp | while read deployment; do")
	lines = append(lines, "            DEPLOY_NAME=$(basename \"$deployment\")")
	lines = append(lines, "            [ \"$DEPLOY_NAME\" != \"ocfp-cli\" ] && log_info \"  - $DEPLOY_NAME\"")
	lines = append(lines, "        done")
	lines = append(lines, "    fi")
	lines = append(lines, "else")
	lines = append(lines, "    log_warning \"Deployment setup completed with warnings (exit code: $CONFIGURE_EXIT)\"")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GenerateVaultPopulateScript generates script for vault population
func (om *OCFPManager) GenerateVaultPopulateScript(ctx context.Context) string {
	var lines []string
	lines = append(lines, "# Vault population")
	lines = append(lines, "")

	// Check if OCFP CLI is available
	lines = append(lines, "if [ -z \"$OCFP_CLI_PATH\" ]; then")
	lines = append(lines, "    log_warning 'OCFP CLI not found, skipping vault populate'")
	lines = append(lines, "    return 0")
	lines = append(lines, "fi")
	lines = append(lines, "")

	// Ensure required environment variables
	lines = append(lines, "if [ -z \"$OCFP_BLOC_NAME\" ] || [ -z \"$OCFP_PROVIDER\" ]; then")
	lines = append(lines, "    log_warning 'Missing required environment variables for vault populate'")
	lines = append(lines, "    log_warning \"OCFP_BLOC_NAME: ${OCFP_BLOC_NAME:-not set}\"")
	lines = append(lines, "    log_warning \"OCFP_PROVIDER: ${OCFP_PROVIDER:-not set}\"")
	lines = append(lines, "    return 0")
	lines = append(lines, "fi")
	lines = append(lines, "")

	lines = append(lines, "log_info \"Running vault populate for bloc: $OCFP_BLOC_NAME\"")
	lines = append(lines, "")

	// Wait for vault to settle after inception
	lines = append(lines, "# Wait for vault to settle")
	lines = append(lines, "log_info 'Waiting for vault to settle...'")
	lines = append(lines, "sleep 3")
	lines = append(lines, "")

	// Verify vault is accessible
	lines = append(lines, "# Verify vault accessibility")
	lines = append(lines, "VAULT_CHECK=$(safe target 2>&1 || echo 'not-accessible')")
	lines = append(lines, "if echo \"$VAULT_CHECK\" | grep -q 'inception\\|production'; then")
	lines = append(lines, "    log_info 'Vault is accessible and targeted'")
	lines = append(lines, "else")
	lines = append(lines, "    log_warning 'Vault may not be properly initialized yet'")
	lines = append(lines, "    log_warning \"Current vault target: $VAULT_CHECK\"")
	lines = append(lines, "fi")
	lines = append(lines, "")

	// Change to OCFP CLI directory and run vault populate
	lines = append(lines, "# Change to OCFP CLI directory")
	lines = append(lines, "ORIGINAL_DIR=$(pwd)")
	lines = append(lines, "OCFP_CLI_DIR=$(dirname \"$OCFP_CLI_PATH\")")
	lines = append(lines, "OCFP_CLI_DIR=$(dirname \"$OCFP_CLI_DIR\")")
	lines = append(lines, "cd \"$OCFP_CLI_DIR\"")
	lines = append(lines, "")

	// Build and execute vault populate command
	lines = append(lines, "# Build vault populate command")
	lines = append(lines, "VAULT_CMD=\"perl bin/ocfp vault populate --bloc '${OCFP_BLOC_NAME}'\"")
	lines = append(lines, "log_info \"Running: $VAULT_CMD\"")
	lines = append(lines, "")

	lines = append(lines, "# Execute vault populate")
	lines = append(lines, "eval $VAULT_CMD")
	lines = append(lines, "VAULT_EXIT=$?")
	lines = append(lines, "")

	// Return to original directory
	lines = append(lines, "# Return to original directory")
	lines = append(lines, "cd \"$ORIGINAL_DIR\"")
	lines = append(lines, "")

	// Handle results
	lines = append(lines, "if [ $VAULT_EXIT -eq 0 ]; then")
	lines = append(lines, "    log_success 'Vault populate completed successfully'")
	lines = append(lines, "    ")
	lines = append(lines, "    # Verify vault populate results")
	lines = append(lines, "    log_info 'Verifying vault populate...'")
	lines = append(lines, "    VERIFY_OUTPUT=$(safe tree \"secret/config/${OCFP_BLOC_NAME}\" 2>&1 | head -10 || echo 'verification-failed')")
	lines = append(lines, "    if echo \"$VERIFY_OUTPUT\" | grep -q 'secret/config'; then")
	lines = append(lines, "        log_success 'Vault populate verification passed'")
	lines = append(lines, "        log_info \"Found paths in vault: $(echo \"$VERIFY_OUTPUT\" | head -3)\"")
	lines = append(lines, "    else")
	lines = append(lines, "        log_warning 'Could not verify vault populate results'")
	lines = append(lines, "    fi")
	lines = append(lines, "else")
	lines = append(lines, "    log_error \"Vault populate failed with exit code $VAULT_EXIT\"")
	lines = append(lines, "    log_warning 'Continuing without vault populate. You may need to run \"ocfp vault populate\" manually later.'")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GenerateOCFPToolVerificationScript generates script to verify required tools after bastion-init
func (om *OCFPManager) GenerateOCFPToolVerificationScript(ctx context.Context) string {
	var lines []string
	lines = append(lines, "# Verify bastion-init prerequisites")
	lines = append(lines, "")

	requiredTools := []string{"safe", "vault", "bosh", "spruce", "yq", "go", "genesis"}

	lines = append(lines, "log_info 'Verifying bastion-init prerequisites'")
	lines = append(lines, "ALL_TOOLS_FOUND=true")
	lines = append(lines, "")

	for _, tool := range requiredTools {
		lines = append(lines, fmt.Sprintf("if command -v %s >/dev/null 2>&1; then", tool))
		lines = append(lines, fmt.Sprintf("    log_info '%s found'", tool))
		lines = append(lines, "else")
		lines = append(lines, fmt.Sprintf("    log_warning '%s not found'", tool))
		lines = append(lines, "    ALL_TOOLS_FOUND=false")
		lines = append(lines, "fi")
	}

	lines = append(lines, "")
	lines = append(lines, "if [ \"$ALL_TOOLS_FOUND\" = \"true\" ]; then")
	lines = append(lines, "    log_success 'All required tools are available'")
	lines = append(lines, "    return 0")
	lines = append(lines, "else")
	lines = append(lines, "    log_error 'Some required tools are missing'")
	lines = append(lines, "    log_error 'Please ensure bastion-init provisioning completed successfully'")
	lines = append(lines, "    return 1")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GenerateScriptCommandVerificationScript ensures script command is available
func (om *OCFPManager) GenerateScriptCommandVerificationScript(ctx context.Context) string {
	var lines []string
	lines = append(lines, "# Verify script command availability")
	lines = append(lines, "")

	lines = append(lines, "if command -v script >/dev/null 2>&1; then")
	lines = append(lines, "    log_success 'script command already available'")
	lines = append(lines, "else")
	lines = append(lines, "    log_info 'Installing script command (bsdutils package)'")
	lines = append(lines, "    sudo apt-get update -qq")
	lines = append(lines, "    sudo apt-get install -y bsdutils")
	lines = append(lines, "    if command -v script >/dev/null 2>&1; then")
	lines = append(lines, "        log_success 'script command installed successfully'")
	lines = append(lines, "    else")
	lines = append(lines, "        log_error 'Failed to install script command'")
	lines = append(lines, "        return 1")
	lines = append(lines, "    fi")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GenerateHostnameVerificationScript verifies hostname configuration
func (om *OCFPManager) GenerateHostnameVerificationScript(ctx context.Context) string {
	var lines []string
	lines = append(lines, "# Hostname verification")
	lines = append(lines, "")

	lines = append(lines, "if [ -z \"$OCFP_BLOC_NAME\" ]; then")
	lines = append(lines, "    log_info 'No OCFP_BLOC_NAME provided, skipping hostname verification'")
	lines = append(lines, "    return 0")
	lines = append(lines, "fi")
	lines = append(lines, "")

	lines = append(lines, "EXPECTED_HOSTNAME=\"${OCFP_BLOC_NAME}-bastion\"")
	lines = append(lines, "CURRENT_HOSTNAME=$(hostname)")
	lines = append(lines, "")

	lines = append(lines, "if [ \"$CURRENT_HOSTNAME\" = \"$EXPECTED_HOSTNAME\" ]; then")
	lines = append(lines, "    log_success \"Hostname correctly set to $EXPECTED_HOSTNAME\"")
	lines = append(lines, "else")
	lines = append(lines, "    log_info \"Verifying hostname configuration\"")
	lines = append(lines, "    log_info \"Current hostname: $CURRENT_HOSTNAME\"")
	lines = append(lines, "    log_info \"Expected hostname: $EXPECTED_HOSTNAME\"")
	lines = append(lines, "    ")
	lines = append(lines, "    # Check hostname file")
	lines = append(lines, "    if [ -f \"/etc/hostname\" ]; then")
	lines = append(lines, "        FILE_HOSTNAME=$(cat /etc/hostname)")
	lines = append(lines, "        if [ \"$FILE_HOSTNAME\" = \"$EXPECTED_HOSTNAME\" ]; then")
	lines = append(lines, "            log_success 'Hostname file configured correctly'")
	lines = append(lines, "        else")
	lines = append(lines, "            log_warning \"Hostname file has different value: $FILE_HOSTNAME\"")
	lines = append(lines, "        fi")
	lines = append(lines, "    fi")
	lines = append(lines, "    ")
	lines = append(lines, "    # Check /etc/hosts")
	lines = append(lines, "    if grep -q \"$EXPECTED_HOSTNAME\" /etc/hosts; then")
	lines = append(lines, "        log_success 'Hostname found in /etc/hosts'")
	lines = append(lines, "    else")
	lines = append(lines, "        log_warning 'Hostname not found in /etc/hosts'")
	lines = append(lines, "    fi")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GenerateEnvironmentLoggingScript generates detailed environment logging
func (om *OCFPManager) GenerateEnvironmentLoggingScript(ctx context.Context) string {
	var lines []string
	lines = append(lines, "# Environment information logging")
	lines = append(lines, "")

	lines = append(lines, "log_info 'Environment Details:'")
	lines = append(lines, "log_info \"  Hostname: $(hostname -f 2>/dev/null || hostname)\"")
	lines = append(lines, "log_info \"  Kernel: $(uname -r)\"")
	lines = append(lines, "log_info \"  Distribution: $(lsb_release -d 2>/dev/null | cut -f2 || grep PRETTY_NAME /etc/os-release | cut -d= -f2)\"")
	lines = append(lines, "log_info \"  CPU count: $(nproc)\"")
	lines = append(lines, "log_info \"  Memory: $(free -h | grep Mem | awk '{print $2}')\"")
	lines = append(lines, "log_info \"  Current user: $USER (UID: $(id -u))\"")
	lines = append(lines, "log_info \"  Home directory: $HOME\"")
	lines = append(lines, "")

	lines = append(lines, "# Log disk space")
	lines = append(lines, "log_info 'Disk space:'")
	lines = append(lines, "df -h / /opt 2>/dev/null | grep '^/' | while read filesystem size used avail percent mount; do")
	lines = append(lines, "    log_info \"  $mount: $used/$size ($percent)\"")
	lines = append(lines, "done")
	lines = append(lines, "")

	lines = append(lines, "# Log OCFP-related environment variables")
	lines = append(lines, "log_info 'OCFP-related environment variables:'")
	lines = append(lines, "env | grep -E '^(OCFP|STACKIT|GENESIS|VAULT|SAFE|BOSH|GO)' | sort | while read envvar; do")
	lines = append(lines, "    log_info \"  $envvar\"")
	lines = append(lines, "done")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}
