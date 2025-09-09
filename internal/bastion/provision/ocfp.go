package provision

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// OCFPManager handles OCFP-specific provisioning tasks.
type OCFPManager struct {
	config   *config.Config
	provider string
	log      logger.Logger
}

// NewOCFPManager creates a new OCFP manager.
func NewOCFPManager(provider string, cfg *config.Config) *OCFPManager {
	return &OCFPManager{
		config:   cfg,
		provider: provider,
		log:      logger.Get(),
	}
}

// GenerateVaultInceptionScript generates script for vault inception setup.
func (om *OCFPManager) GenerateVaultInceptionScript(ctx context.Context) string {
	var lines []string

	lines = append(lines, "# Vault inception setup")
	lines = append(lines, "")

	lines = append(lines, om.generateVaultInceptionScriptLocator()...)
	lines = append(lines, om.generateVaultInceptionExecution()...)

	return strings.Join(lines, "\n")
}

// GenerateOCFPConfigureScript generates script for OCFP configure deployments.
func (om *OCFPManager) GenerateOCFPConfigureScript(ctx context.Context) string {
	var lines []string

	lines = append(lines, "# OCFP deployments setup")
	lines = append(lines, "")

	lines = append(lines, om.generateOCFPCLILocator()...)
	lines = append(lines, om.generateConfigFileLocator()...)
	lines = append(lines, om.generateDeploymentConfiguration()...)

	return strings.Join(lines, "\n")
}

// GenerateVaultPopulateScript generates script for vault population.
func (om *OCFPManager) GenerateVaultPopulateScript(ctx context.Context) string {
	var lines []string

	lines = append(lines, "# Vault population")
	lines = append(lines, "")

	lines = append(lines, om.generateVaultPopulatePrerequisites()...)
	lines = append(lines, om.generateVaultPreparation()...)
	lines = append(lines, om.generateVaultPopulateExecution()...)

	return strings.Join(lines, "\n")
}

// GenerateOCFPToolVerificationScript generates script to verify required tools after bastion-init.
func (om *OCFPManager) GenerateOCFPToolVerificationScript(ctx context.Context) string {
	requiredTools := []string{"safe", "vault", "bosh", "spruce", "yq", "go", "genesis"}
	lines := make([]string, 0, scriptBufferOCFPBase+scriptBufferOCFPPerTool*len(requiredTools))

	lines = append(lines, "# Verify bastion-init prerequisites")
	lines = append(lines, "")

	// tools declared above for capacity

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

// GenerateScriptCommandVerificationScript ensures script command is available.
func (om *OCFPManager) GenerateScriptCommandVerificationScript(ctx context.Context) string {
	lines := make([]string, 0, scriptBufferOCFP1)

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

// GenerateHostnameVerificationScript verifies hostname configuration.
func (om *OCFPManager) GenerateHostnameVerificationScript(ctx context.Context) string {
	lines := make([]string, 0, scriptBufferOCFP2)

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

// GenerateEnvironmentLoggingScript generates detailed environment logging.
func (om *OCFPManager) GenerateEnvironmentLoggingScript(ctx context.Context) string {
	lines := make([]string, 0, scriptBufferOCFP2)

	lines = append(lines, "# Environment information logging")
	lines = append(lines, "")

	lines = append(lines, om.generateSystemInfoLogging()...)
	lines = append(lines, om.generateDiskSpaceLogging()...)
	lines = append(lines, om.generateEnvironmentVariableLogging()...)

	return strings.Join(lines, "\n")
}

func (om *OCFPManager) generateVaultInceptionScriptLocator() []string {
	return []string{
		"# Check for vault-inception script",
		"VAULT_INCEPTION_LOCATIONS=(",
		`    "${HOME}/ocfp/cli/scripts/provision/vault-inception"`,
		`    "${HOME}/ocfp/ocfp-cli/scripts/provision/vault-inception"`,
		")",
		"",
		"VAULT_INCEPTION_SCRIPT=\"\"",
		"for location in \"${VAULT_INCEPTION_LOCATIONS[@]}\"; do",
		"    if [ -f \"$location\" ]; then",
		"        VAULT_INCEPTION_SCRIPT=\"$location\"",
		"        log_info \"Found vault-inception script at: $location\"",
		"        break",
		"    fi",
		"done",
		"",
		"if [ -z \"$VAULT_INCEPTION_SCRIPT\" ]; then",
		"    log_error 'vault-inception script not found!'",
		"    log_error 'Expected at: ~/ocfp/cli/scripts/provision/vault-inception'",
		"    return 1",
		"fi",
		"",
	}
}

func (om *OCFPManager) generateVaultInceptionExecution() []string {
	return []string{
		"if [ ! -x \"$VAULT_INCEPTION_SCRIPT\" ]; then",
		"    log_info 'Making vault-inception script executable'",
		"    chmod +x \"$VAULT_INCEPTION_SCRIPT\"",
		"fi",
		"",
		"log_info 'Running vault-inception script'",
		"perl \"$VAULT_INCEPTION_SCRIPT\"",
		"VAULT_INCEPTION_EXIT=$?",
		"",
		"if [ $VAULT_INCEPTION_EXIT -eq 0 ]; then",
		"    log_success 'Vault inception completed successfully'",
		"else",
		"    log_error \"Vault inception failed with exit code $VAULT_INCEPTION_EXIT\"",
		"    # Check if it's because vault is already set up",
		"    if safe target 2>&1 | grep -q 'inception\\|production'; then",
		"        log_success 'Vault already configured'",
		"    else",
		"        return 1",
		"    fi",
		"fi",
		"",
	}
}

func (om *OCFPManager) generateOCFPCLILocator() []string {
	return []string{
		"# Check for OCFP CLI",
		"OCFP_CLI_LOCATIONS=(",
		`    "${HOME}/ocfp/ocfp-cli/bin/ocfp"`,
		`    "${HOME}/ocfp/cli/bin/ocfp"`,
		`    "${HOME}/bin/ocfp"`,
		")",
		"",
		"OCFP_CLI_PATH=\"\"",
		"for location in \"${OCFP_CLI_LOCATIONS[@]}\"; do",
		"    if [ -f \"$location\" ]; then",
		"        OCFP_CLI_PATH=\"$location\"",
		"        log_info \"Found OCFP CLI at: $location\"",
		"        break",
		"    fi",
		"done",
		"",
		"if [ -z \"$OCFP_CLI_PATH\" ]; then",
		"    log_warning 'OCFP CLI not found, skipping deployment setup'",
		"    return 0",
		"fi",
		"",
	}
}

func (om *OCFPManager) generateConfigFileLocator() []string {
	return []string{
		"# Find configuration file",
		"CONFIG_FILE=\"\"",
		"CONFIG_LOCATIONS=(",
		`    "${HOME}/.ocfp/config.yml"`,
		`    "${HOME}/.ocfp/config/config.yml"`,
		")",
		"",
		"for location in \"${CONFIG_LOCATIONS[@]}\"; do",
		"    if [ -f \"$location\" ]; then",
		"        CONFIG_FILE=\"$location\"",
		"        log_info \"Found config file at: $location\"",
		"        break",
		"    fi",
		"done",
		"",
		"if [ -z \"$CONFIG_FILE\" ]; then",
		"    log_warning 'Configuration file not found, skipping deployment setup'",
		"    return 0",
		"fi",
		"",
	}
}

func (om *OCFPManager) generateDeploymentConfiguration() []string {
	return []string{
		"# Change to OCFP CLI directory",
		"OCFP_CLI_DIR=$(dirname \"$OCFP_CLI_PATH\")",
		"OCFP_CLI_DIR=$(dirname \"$OCFP_CLI_DIR\")",
		"log_info \"Changing to OCFP CLI directory: $OCFP_CLI_DIR\"",
		"cd \"$OCFP_CLI_DIR\"",
		"",
		"# Run OCFP configure deployments",
		"log_info 'Running OCFP configure to setup deployments'",
		"CONFIGURE_CMD=\"perl bin/ocfp configure deployments --bloc '${OCFP_BLOC_NAME}'\"",
		"log_info \"Executing: $CONFIGURE_CMD\"",
		"",
		"eval $CONFIGURE_CMD",
		"CONFIGURE_EXIT=$?",
		"",
		"if [ $CONFIGURE_EXIT -eq 0 ]; then",
		"    log_success 'Genesis deployments setup completed successfully'",
		"    # List created deployments",
		"    if [ -d \"$HOME/ocfp\" ]; then",
		"        log_info 'Created ~/ocfp directories:'",
		"        find \"$HOME/ocfp\" -maxdepth 1 -type d -not -name ocfp | while read deployment; do",
		"            DEPLOY_NAME=$(basename \"$deployment\")",
		"            [ \"$DEPLOY_NAME\" != \"ocfp-cli\" ] && log_info \"  - $DEPLOY_NAME\"",
		"        done",
		"    fi",
		"else",
		"    log_warning \"Deployment setup completed with warnings (exit code: $CONFIGURE_EXIT)\"",
		"fi",
		"",
	}
}

func (om *OCFPManager) generateVaultPopulatePrerequisites() []string {
	return []string{
		"if [ -z \"$OCFP_CLI_PATH\" ]; then",
		"    log_warning 'OCFP CLI not found, skipping vault populate'",
		"    return 0",
		"fi",
		"",
		"if [ -z \"$OCFP_BLOC_NAME\" ] || [ -z \"$OCFP_PROVIDER\" ]; then",
		"    log_warning 'Missing required environment variables for vault populate'",
		"    log_warning \"OCFP_BLOC_NAME: ${OCFP_BLOC_NAME:-not set}\"",
		"    log_warning \"OCFP_PROVIDER: ${OCFP_PROVIDER:-not set}\"",
		"    return 0",
		"fi",
		"",
		"log_info \"Running vault populate for bloc: $OCFP_BLOC_NAME\"",
		"",
	}
}

func (om *OCFPManager) generateVaultPreparation() []string {
	return []string{
		"# Wait for vault to settle",
		"log_info 'Waiting for vault to settle...'",
		"sleep 3",
		"",
		"# Verify vault accessibility",
		"VAULT_CHECK=$(safe target 2>&1 || echo 'not-accessible')",
		"if echo \"$VAULT_CHECK\" | grep -q 'inception\\|production'; then",
		"    log_info 'Vault is accessible and targeted'",
		"else",
		"    log_warning 'Vault may not be properly initialized yet'",
		"    log_warning \"Current vault target: $VAULT_CHECK\"",
		"fi",
		"",
	}
}

func (om *OCFPManager) generateVaultPopulateExecution() []string {
	return []string{
		"# Change to OCFP CLI directory",
		"ORIGINAL_DIR=$(pwd)",
		"OCFP_CLI_DIR=$(dirname \"$OCFP_CLI_PATH\")",
		"OCFP_CLI_DIR=$(dirname \"$OCFP_CLI_DIR\")",
		"cd \"$OCFP_CLI_DIR\"",
		"",
		"# Build vault populate command",
		"VAULT_CMD=\"perl bin/ocfp vault populate --bloc '${OCFP_BLOC_NAME}'\"",
		"log_info \"Running: $VAULT_CMD\"",
		"",
		"# Execute vault populate",
		"eval $VAULT_CMD",
		"VAULT_EXIT=$?",
		"",
		"# Return to original directory",
		"cd \"$ORIGINAL_DIR\"",
		"",
		"if [ $VAULT_EXIT -eq 0 ]; then",
		"    log_success 'Vault populate completed successfully'",
		"    ",
		"    # Verify vault populate results",
		"    log_info 'Verifying vault populate...'",
		"    VERIFY_OUTPUT=$(safe tree \"secret/config/${OCFP_BLOC_NAME}\" 2>&1 | head -10 || echo 'verification-failed')",
		"    if echo \"$VERIFY_OUTPUT\" | grep -q 'secret/config'; then",
		"        log_success 'Vault populate verification passed'",
		"        log_info \"Found paths in vault: $(echo \"$VERIFY_OUTPUT\" | head -3)\"",
		"    else",
		"        log_warning 'Could not verify vault populate results'",
		"    fi",
		"else",
		"    log_error \"Vault populate failed with exit code $VAULT_EXIT\"",
		"    log_warning 'Continuing without vault populate. You may need to run \"ocfp vault populate\" manually later.'",
		"fi",
		"",
	}
}

func (om *OCFPManager) generateSystemInfoLogging() []string {
	return []string{
		"log_info 'Environment Details:'",
		"log_info \"  Hostname: $(hostname -f 2>/dev/null || hostname)\"",
		"log_info \"  Kernel: $(uname -r)\"",
		"log_info \"  Distribution: $(lsb_release -d 2>/dev/null | cut -f2 || grep PRETTY_NAME /etc/os-release | cut -d= -f2)\"",
		"log_info \"  CPU count: $(nproc)\"",
		"log_info \"  Memory: $(free -h | grep Mem | awk '{print $2}')\"",
		"log_info \"  Current user: $USER (UID: $(id -u))\"",
		"log_info \"  Home directory: $HOME\"",
		"",
	}
}

func (om *OCFPManager) generateDiskSpaceLogging() []string {
	return []string{
		"# Log disk space",
		"log_info 'Disk space:'",
		"df -h / /opt 2>/dev/null | grep '^/' | while read filesystem size used avail percent mount; do",
		"    log_info \"  $mount: $used/$size ($percent)\"",
		"done",
		"",
	}
}

func (om *OCFPManager) generateEnvironmentVariableLogging() []string {
	return []string{
		"# Log OCFP-related environment variables",
		"log_info 'OCFP-related environment variables:'",
		"env | grep -E '^(OCFP|STACKIT|GENESIS|VAULT|SAFE|BOSH|GO)' | sort | while read envvar; do",
		"    log_info \"  $envvar\"",
		"done",
		"",
	}
}
