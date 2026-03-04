package provision

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/deployments"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// OCFPManager handles OCFP-specific provisioning tasks.
type OCFPManager struct {
	config   *config.Config
	provider string
	log      logger.Logger
	modes    *deployments.Resolver
}

// NewOCFPManager creates a new OCFP manager.
func NewOCFPManager(provider string, cfg *config.Config, modes *deployments.Resolver) *OCFPManager {
	if modes == nil {
		modes = deployments.NewResolver(cfg)
	}

	return &OCFPManager{
		config:   cfg,
		provider: provider,
		log:      logger.Get(),
		modes:    modes,
	}
}

// GenerateVaultInceptionScript generates script for vault inception setup.
func (om *OCFPManager) GenerateVaultInceptionScript(_ctx context.Context) string {
	lines := make([]string, 0, 16) //nolint:mnd // rough capacity for script header + locator + execution
	lines = append(lines, "# Vault inception setup")
	lines = append(lines, "")

	// Use OCFP CLI locator to find the installed binary
	lines = append(lines, om.generateOCFPCLILocator()...)
	lines = append(lines, om.generateVaultInceptionExecution()...)

	return strings.Join(lines, "\n")
}

// GenerateOCFPConfigureScript generates script for OCFP configure deployments.
//
//nolint:funlen // Script generation requires many statements
func (om *OCFPManager) GenerateOCFPConfigureScript(_ctx context.Context) string {
	resolver := om.resolver()
	names := om.mergeDeploymentNames(defaultDeploymentNames, resolver.Configured())
	devDeployments, releaseDeployments := om.partitionDeployments(names)

	lines := make([]string, 0, scriptBufferOCFPBase)
	lines = append(lines, "# OCFP deployments setup")
	lines = append(lines, "")

	lines = append(lines, om.generateOCFPCLILocator()...)

	lines = append(lines, fmt.Sprintf("GLOBAL_DEPLOYMENTS_URL=%q", resolver.GlobalURL()))
	lines = append(lines, `DEPLOYMENTS_ROOT="${HOME}/ocfp/deployments"`)
	lines = append(lines, `KITS_ROOT="${HOME}/ocfp/kits"`)
	lines = append(lines, "DEV_DEPLOYMENTS="+formatShellArray(devDeployments))
	lines = append(lines, "RELEASE_DEPLOYMENTS="+formatShellArray(releaseDeployments))
	lines = append(lines, "")

	lines = append(lines, "log_info 'Preparing OCFP deployments'")
	lines = append(lines, `mkdir -p "${DEPLOYMENTS_ROOT}"`)
	lines = append(lines, `mkdir -p "${KITS_ROOT}"`)
	lines = append(lines, "")

	lines = append(lines, `if [ -n "$GLOBAL_DEPLOYMENTS_URL" ]; then`)
	lines = append(lines, `    if [ -d "${DEPLOYMENTS_ROOT}/.git" ]; then`)
	lines = append(lines, `        # Valid git repository exists, update it`)
	lines = append(lines, `        log_info 'Updating deployments repository'`)
	lines = append(lines, `        if git -C "${DEPLOYMENTS_ROOT}" fetch --all --prune && git -C "${DEPLOYMENTS_ROOT}" pull --ff-only; then`)
	lines = append(lines, `            log_success 'Deployments repository updated'`)
	lines = append(lines, "        else")
	lines = append(lines, `            log_warning 'Failed to update deployments repository - please verify connectivity and credentials'`)
	lines = append(lines, "        fi")
	lines = append(lines, `    elif [ -d "${DEPLOYMENTS_ROOT}" ]; then`)
	lines = append(lines, `        # Directory exists but is not a valid git repository`)
	lines = append(lines, `        log_warning 'Deployments directory exists but is not a valid git repository'`)
	lines = append(lines, `        log_info 'Removing invalid deployments directory'`)
	lines = append(lines, `        rm -rf "${DEPLOYMENTS_ROOT}"`)
	lines = append(lines, `        log_info 'Cloning deployments repository'`)
	lines = append(lines, `        if git clone "$GLOBAL_DEPLOYMENTS_URL" "${DEPLOYMENTS_ROOT}"; then`)
	lines = append(lines, `            log_success 'Deployments repository cloned'`)
	lines = append(lines, "        else")
	lines = append(lines, `            log_error 'Failed to clone deployments repository'`)
	lines = append(lines, "        fi")
	lines = append(lines, "    else")
	lines = append(lines, `        # No directory exists, clone fresh`)
	lines = append(lines, `        log_info 'Cloning deployments repository'`)
	lines = append(lines, `        if git clone "$GLOBAL_DEPLOYMENTS_URL" "${DEPLOYMENTS_ROOT}"; then`)
	lines = append(lines, `            log_success 'Deployments repository cloned'`)
	lines = append(lines, "        else")
	lines = append(lines, `            log_error 'Failed to clone deployments repository'`)
	lines = append(lines, "        fi")
	lines = append(lines, "    fi")
	lines = append(lines, "fi")
	lines = append(lines, "")

	lines = append(lines, "# Verify release-mode deployments are present")
	lines = append(lines, `for deployment in "${RELEASE_DEPLOYMENTS[@]}"; do`)
	lines = append(lines, `    DEPLOY_PATH="${DEPLOYMENTS_ROOT}/${deployment}"`)
	lines = append(lines, `    if [ -d "$DEPLOY_PATH" ]; then`)
	lines = append(lines, `        log_success "Release deployment available: ${deployment}"`)
	lines = append(lines, "    else")
	lines = append(lines, `        log_warning "Release deployment directory missing: ${deployment}"`)
	lines = append(lines, "    fi")
	lines = append(lines, "done")
	lines = append(lines, "")

	lines = append(lines, "# Ensure dev-mode deployment directories exist")
	lines = append(lines, `for deployment in "${DEV_DEPLOYMENTS[@]}"; do`)
	lines = append(lines, `    DEPLOY_PATH="${DEPLOYMENTS_ROOT}/${deployment}"`)
	lines = append(lines, `    if [ ! -d "$DEPLOY_PATH" ]; then`)
	lines = append(lines, `        log_info "Creating dev deployment directory: ${deployment}"`)
	lines = append(lines, `        mkdir -p "$DEPLOY_PATH"`)
	lines = append(lines, "    fi")
	lines = append(lines, "done")
	lines = append(lines, "")

	lines = append(lines, "# Setup dev kits when using global deployments repository")
	lines = append(lines, `if [ -n "$GLOBAL_DEPLOYMENTS_URL" ]; then`)
	lines = append(lines, `    for deployment in "${DEV_DEPLOYMENTS[@]}"; do`)
	lines = append(lines, `        KIT_REPO="git@github.com:genesis-community/${deployment}-genesis-kit.git"`)
	lines = append(lines, `        KIT_DIR="${KITS_ROOT}/${deployment}"`)
	lines = append(lines, `        if [ -d "${KIT_DIR}/.git" ]; then`)
	lines = append(lines, `            log_info "Updating ${deployment} genesis kit"`)
	lines = append(lines, `            if git -C "${KIT_DIR}" pull --ff-only; then`)
	lines = append(lines, `                log_success "${deployment} kit updated"`)
	lines = append(lines, "            else")
	lines = append(lines, `                log_warning "Failed to update ${deployment} kit"`)
	lines = append(lines, "                continue")
	lines = append(lines, "            fi")
	lines = append(lines, "        else")
	lines = append(lines, `            log_info "Cloning ${deployment} genesis kit"`)
	lines = append(lines, `            if git clone "$KIT_REPO" "$KIT_DIR"; then`)
	lines = append(lines, `                log_success "${deployment} kit cloned"`)
	lines = append(lines, "            else")
	lines = append(lines, `                log_warning "Failed to clone ${deployment} kit from $KIT_REPO"`)
	lines = append(lines, "                continue")
	lines = append(lines, "            fi")
	lines = append(lines, "        fi")
	lines = append(lines, `        mkdir -p "${DEPLOYMENTS_ROOT}/${deployment}"`)
	lines = append(lines, `        ln -sfn "$KIT_DIR" "${DEPLOYMENTS_ROOT}/${deployment}/dev"`)
	lines = append(lines, `        log_info "Linked ${DEPLOYMENTS_ROOT}/${deployment}/dev -> ${KIT_DIR}"`)
	lines = append(lines, "    done")
	lines = append(lines, "fi")
	lines = append(lines, "")

	lines = append(lines, om.generateDeploymentConfiguration()...)

	return strings.Join(lines, "\n")
}

//nolint:gochecknoglobals // Default deployment list is package-level constant
var defaultDeploymentNames = []string{
	"bosh",
	"vault",
	"concourse",
	"cf",
	"blacksmith",
	"shield",
	"prometheus",
	"doomsday",
	"scheduler",
	"autoscaler",
	"jumpbox",
}

func formatShellArray(values []string) string {
	if len(values) == 0 {
		return "()"
	}

	escaped := make([]string, len(values))
	for i, v := range values {
		escaped[i] = fmt.Sprintf("\"%s\"", v)
	}

	return fmt.Sprintf("(%s)", strings.Join(escaped, " "))
}

//nolint:funcorder,nonamedreturns // Helper method placed after exported methods; named returns for clarity
func (om *OCFPManager) partitionDeployments(names []string) (dev []string, release []string) {
	resolver := om.resolver()

	for _, name := range names {
		if resolver.IsRelease(name) {
			release = append(release, name)
		} else {
			dev = append(dev, name)
		}
	}

	return dev, release
}

//nolint:funcorder // Helper method placed after exported methods
func (om *OCFPManager) mergeDeploymentNames(defaults []string, configured []string) []string {
	seen := make(map[string]struct{}, len(defaults)+len(configured))
	combined := make([]string, 0, len(defaults)+len(configured))

	for _, name := range defaults {
		if name == "" {
			continue
		}

		if _, ok := seen[name]; ok {
			continue
		}

		seen[name] = struct{}{}
		combined = append(combined, name)
	}

	for _, name := range configured {
		if name == "" {
			continue
		}

		if _, ok := seen[name]; ok {
			continue
		}

		seen[name] = struct{}{}
		combined = append(combined, name)
	}

	return combined
}

//nolint:funcorder // Helper method placed after exported methods
func (om *OCFPManager) generateDeploymentConfiguration() []string {
	return []string{
		"# Run ocfp configure deployments",
		"if [ -z \"$OCFP_CLI_PATH\" ]; then",
		"    log_warning 'OCFP CLI not found, skipping ocfp configure deployments'",
		"else",
		"    CONFIGURE_ARGS=()",
		"    if [ -n \"${OCFP_BLOC}\" ]; then",
		"        CONFIGURE_ARGS+=(\"--bloc\" \"${OCFP_BLOC}\")",
		"    fi",
		"    log_info \"Executing: ${OCFP_CLI_PATH} configure deployments ${CONFIGURE_ARGS[*]}\"",
		"    if \"${OCFP_CLI_PATH}\" configure deployments \"${CONFIGURE_ARGS[@]}\"; then",
		"        log_success 'Genesis deployments setup completed successfully'",
		"    else",
		"        log_warning 'Deployment setup completed with warnings'",
		"    fi",
		"fi",
		"",
	}
}

//nolint:funcorder // Helper method placed after exported methods
func (om *OCFPManager) resolver() *deployments.Resolver {
	if om.modes == nil {
		om.modes = deployments.NewResolver(om.config)
	}

	return om.modes
}

// GenerateVaultPopulateScript generates the script for populating vault secrets.
func (om *OCFPManager) GenerateVaultPopulateScript(_ctx context.Context) string {
	lines := make([]string, 0, 32) //nolint:mnd // rough capacity for script sections
	lines = append(lines, "# Vault population")
	lines = append(lines, "")

	lines = append(lines, om.generateOCFPCLILocator()...)
	lines = append(lines, om.generateVaultPopulatePrerequisites()...)
	lines = append(lines, om.generateVaultPreparation()...)
	lines = append(lines, om.generateVaultPopulateExecution()...)

	return strings.Join(lines, "\n")
}

// GenerateGenesisSecretsProvidersScript generates script to configure genesis deployments to use inception vault.
//
//nolint:funlen // shell script generation with line-by-line append is inherently verbose
func (om *OCFPManager) GenerateGenesisSecretsProvidersScript(_ctx context.Context) string {
	lines := make([]string, 0, 71) //nolint:mnd // rough capacity for genesis secrets providers script
	lines = append(lines, "# Configure Genesis secrets providers for deployments")
	lines = append(lines, "")

	lines = append(lines, `DEPLOYMENTS_ROOT="${HOME}/ocfp/deployments"`)
	lines = append(lines, "")

	lines = append(lines, "if [ ! -d \"$DEPLOYMENTS_ROOT\" ]; then")
	lines = append(lines, "    log_warning \"Deployments directory not found: $DEPLOYMENTS_ROOT\"")
	lines = append(lines, "    log_warning 'Skipping genesis secrets-provider configuration'")
	lines = append(lines, "else")
	lines = append(lines, "    log_info 'Configuring Genesis secrets providers for deployments'")
	lines = append(lines, "    ")
	lines = append(lines, "    CONFIGURED_COUNT=0")
	lines = append(lines, "    SKIPPED_COUNT=0")
	lines = append(lines, "    FAILED_COUNT=0")
	lines = append(lines, "    ")
	lines = append(lines, "    for deployment_dir in \"$DEPLOYMENTS_ROOT\"/*; do")
	lines = append(lines, "        if [ ! -d \"$deployment_dir\" ]; then")
	lines = append(lines, "            continue")
	lines = append(lines, "        fi")
	lines = append(lines, "        ")
	lines = append(lines, "        if [ ! -d \"$deployment_dir/.genesis\" ]; then")
	lines = append(lines, "            continue")
	lines = append(lines, "        fi")
	lines = append(lines, "        ")
	lines = append(lines, "        deployment_name=$(basename \"$deployment_dir\")")
	lines = append(lines, "        log_info \"Configuring secrets provider for deployment: $deployment_name\"")
	lines = append(lines, "        ")
	lines = append(lines, "        # Change to deployment directory for genesis command context")
	lines = append(lines, "        if ! cd \"$deployment_dir\"; then")
	lines = append(lines, "            log_warning \"  Failed to enter directory: $deployment_dir\"")
	lines = append(lines, "            SKIPPED_COUNT=$((SKIPPED_COUNT + 1))")
	lines = append(lines, "            continue")
	lines = append(lines, "        fi")
	lines = append(lines, "        ")
	lines = append(lines, "        # Run genesis secrets-provider inception")
	lines = append(lines, "        if genesis secrets-provider inception >/dev/null 2>&1; then")
	lines = append(lines, "            log_success \"  Configured secrets provider for: $deployment_name\"")
	lines = append(lines, "            CONFIGURED_COUNT=$((CONFIGURED_COUNT + 1))")
	lines = append(lines, "        else")
	lines = append(lines, "            # Check if it was already configured")
	lines = append(lines, "            if grep -q 'vault_target: inception' .genesis/config 2>/dev/null; then")
	lines = append(lines, "                log_success \"  Secrets provider already configured for: $deployment_name\"")
	lines = append(lines, "                CONFIGURED_COUNT=$((CONFIGURED_COUNT + 1))")
	lines = append(lines, "            else")
	lines = append(lines, "                log_warning \"  Failed to configure secrets provider for: $deployment_name\"")
	lines = append(lines, "                FAILED_COUNT=$((FAILED_COUNT + 1))")
	lines = append(lines, "            fi")
	lines = append(lines, "        fi")
	lines = append(lines, "    done")
	lines = append(lines, "    ")
	lines = append(lines, "    # Return to original directory")
	lines = append(lines, "    cd ~ || true")
	lines = append(lines, "    ")
	lines = append(lines, "    # Summary logging")
	lines = append(lines, "    log_info 'Genesis secrets provider configuration summary:'")
	lines = append(lines, "    log_info \"  Configured: $CONFIGURED_COUNT\"")
	lines = append(lines, "    if [ $SKIPPED_COUNT -gt 0 ]; then")
	lines = append(lines, "        log_info \"  Skipped: $SKIPPED_COUNT\"")
	lines = append(lines, "    fi")
	lines = append(lines, "    if [ $FAILED_COUNT -gt 0 ]; then")
	lines = append(lines, "        log_warning \"  Failed: $FAILED_COUNT\"")
	lines = append(lines, "    fi")
	lines = append(lines, "    ")
	lines = append(lines, "    if [ $CONFIGURED_COUNT -gt 0 ]; then")
	lines = append(lines, "        log_success 'Genesis secrets providers configured successfully'")
	lines = append(lines, "    elif [ $FAILED_COUNT -gt 0 ]; then")
	lines = append(lines, "        log_warning 'Some deployments failed to configure - manual intervention may be required'")
	lines = append(lines, "    else")
	lines = append(lines, "        log_info 'No Genesis deployments found to configure'")
	lines = append(lines, "    fi")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GenerateOCFPToolVerificationScript generates script to verify required tools after bastion-init.
func (om *OCFPManager) GenerateOCFPToolVerificationScript(_ctx context.Context) string {
	requiredTools := []string{"safe", "vault", "bao", "bosh", "cf", "credhub", "uaa", "spruce", "yq", "go", "genesis"}
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
	lines = append(lines, "else")
	lines = append(lines, "    log_error 'Some required tools are missing'")
	lines = append(lines, "    log_error 'Please ensure bastion-init provisioning completed successfully'")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GenerateScriptCommandVerificationScript ensures script command is available.
func (om *OCFPManager) GenerateScriptCommandVerificationScript(_ctx context.Context) string {
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
	lines = append(lines, "        log_warning 'script command may be required for some operations'")
	lines = append(lines, "    fi")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GenerateHostnameVerificationScript verifies hostname configuration.
func (om *OCFPManager) GenerateHostnameVerificationScript(_ctx context.Context) string {
	lines := make([]string, 0, scriptBufferOCFP2)

	lines = append(lines, "# Hostname verification")
	lines = append(lines, "")

	lines = append(lines, "if [ -n \"$OCFP_BLOC\" ]; then")
	lines = append(lines, "")

	lines = append(lines, "EXPECTED_HOSTNAME=\"${OCFP_BLOC}-bastion\"")
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
	lines = append(lines, "else")
	lines = append(lines, "    log_info 'No OCFP_BLOC provided, skipping hostname verification'")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GenerateEnvironmentLoggingScript generates detailed environment logging.
func (om *OCFPManager) GenerateEnvironmentLoggingScript(_ctx context.Context) string {
	lines := make([]string, 0, scriptBufferOCFP2)

	lines = append(lines, "# Environment information logging")
	lines = append(lines, "")

	lines = append(lines, om.generateSystemInfoLogging()...)
	lines = append(lines, om.generateDiskSpaceLogging()...)
	lines = append(lines, om.generateEnvironmentVariableLogging()...)

	return strings.Join(lines, "\n")
}

func (om *OCFPManager) generateVaultInceptionExecution() []string {
	return []string{
		"# Run vault inception using OCFP CLI binary on bastion",
		"if [ -n \"$OCFP_CLI_PATH\" ]; then",
		"    # Ensure /usr/local/bin is in PATH for vault and safe commands",
		"    export PATH=\"/usr/local/bin:${PATH}\"",
		"    ",
		"    # Ensure TERM/TERMINFO for tmux in non-PTY SSH",
		"    if [ -d /home/linuxbrew/.linuxbrew/share/terminfo ]; then",
		"        export TERMINFO_DIRS=\"${TERMINFO_DIRS:+${TERMINFO_DIRS}:}/home/linuxbrew/.linuxbrew/share/terminfo:/usr/share/terminfo:/lib/terminfo\"",
		"    fi",
		"    if [ -z \"$TERM\" ] || ! infocmp \"$TERM\" >/dev/null 2>&1; then",
		"        for t in xterm-256color screen-256color screen xterm dumb; do",
		"            if infocmp \"$t\" >/dev/null 2>&1; then export TERM=\"$t\"; break; fi",
		"        done",
		"    fi",
		"    ",
		"    VAULT_PORT=8234",
		"    VAULT_ADDR=\"http://127.0.0.1:${VAULT_PORT}\"",
		"    INCEPTION_SESSION=\"${OCFP_BLOC:+${OCFP_BLOC}-}inception-vault\"",
		"    ",
		"    # Fast-path: skip if tmux session exists, vault responds, and safe target set",
		"    if tmux has-session -t \"${INCEPTION_SESSION}\" 2>/dev/null \\",
		"       && VAULT_ADDR=\"${VAULT_ADDR}\" vault status >/dev/null 2>&1 \\",
		"       && safe target 2>&1 | grep -q 'inception'; then",
		"        log_success 'Inception vault already running - skipping'",
		"        export VAULT_ADDR",
		"    else",
		"        # Run full inception",
		"        log_info 'Running vault inception via OCFP CLI'",
		"        PATH=\"/usr/local/bin:${PATH}\" \"${OCFP_CLI_PATH}\" vault inception",
		"        VAULT_INCEPTION_EXIT=$?",
		"        ",
		"        if [ $VAULT_INCEPTION_EXIT -eq 0 ]; then",
		"            log_success 'Vault inception completed successfully'",
		"        else",
		"            log_error \"Vault inception failed with exit code $VAULT_INCEPTION_EXIT\"",
		"            # Check if it's because vault is already set up",
		"            if safe target 2>&1 | grep -q 'inception\\|production'; then",
		"                log_success 'Vault already configured'",
		"            else",
		"                log_error 'Vault inception failed - vault may need manual setup'",
		"                exit 1",
		"            fi",
		"        fi",
		"    fi",
		"else",
		"    log_error 'OCFP CLI not found at expected locations'",
		"    log_error 'Cannot proceed without vault initialization'",
		"    exit 1",
		"fi",
		"",
	}
}

func (om *OCFPManager) generateVaultPopulatePrerequisites() []string {
	return []string{
		"# Check prerequisites for vault populate",
		"if [ -z \"$OCFP_CLI_PATH\" ]; then",
		"    log_warning 'OCFP CLI not found, skipping vault populate'",
		"elif [ -z \"$OCFP_BLOC\" ] || [ -z \"$OCFP_PROVIDER\" ]; then",
		"    log_warning 'Missing required environment variables for vault populate'",
		"    log_warning \"OCFP_BLOC: ${OCFP_BLOC:-not set}\"",
		"    log_warning \"OCFP_PROVIDER: ${OCFP_PROVIDER:-not set}\"",
		"else",
		"    log_info \"Running vault populate for bloc: $OCFP_BLOC\"",
		"",
	}
}

func (om *OCFPManager) generateOCFPCLILocator() []string {
	return []string{
		"# Locate OCFP CLI",
		"OCFP_LOCATIONS=(",
		"    \"${HOME}/ocfp/cli/bin/ocfp\"",
		"    \"${HOME}/ocfp/cli/ocfp\"",
		"    \"${HOME}/ocfp/ocfp-cli/bin/ocfp\"",
		"    \"/usr/local/bin/ocfp\"",
		")",
		"",
		"OCFP_CLI_PATH=\"\"",
		"for location in \"${OCFP_LOCATIONS[@]}\"; do",
		"    if [ -x \"$location\" ]; then",
		"        OCFP_CLI_PATH=\"$location\"",
		"        log_info \"Found OCFP CLI at: $location\"",
		"        break",
		"    fi",
		"done",
		"",
		"if [ -z \"$OCFP_CLI_PATH\" ]; then",
		"    log_warning 'OCFP CLI not found - some operations may be skipped'",
		"fi",
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
		"    # Execute vault populate",
		`    VAULT_ARGS=("vault" "populate")`,
		"    if [ -n \"${OCFP_BLOC}\" ]; then",
		`        VAULT_ARGS+=("--bloc" "${OCFP_BLOC}")`,
		"    fi",
		"    ",
		"    log_info \"Running: ${OCFP_CLI_PATH} ${VAULT_ARGS[*]}\"",
		`    "${OCFP_CLI_PATH}" "${VAULT_ARGS[@]}"`,
		"    VAULT_EXIT=$?",
		"    ",
		"    if [ $VAULT_EXIT -eq 0 ]; then",
		"        log_success 'Vault populate completed successfully'",
		"        ",
		"        # Verify vault populate results",
		"        log_info 'Verifying vault populate...'",
		"        VERIFY_OUTPUT=$(safe tree \"secret/config/${OCFP_BLOC}\" 2>&1 | head -10 || echo 'verification-failed')",
		"        if echo \"$VERIFY_OUTPUT\" | grep -q 'secret/config'; then",
		"            log_success 'Vault populate verification passed'",
		"            log_info \"Found paths in vault: $(echo \"$VERIFY_OUTPUT\" | head -3)\"",
		"        else",
		"            log_warning 'Could not verify vault populate results'",
		"        fi",
		"    else",
		"        log_error \"Vault populate failed with exit code $VAULT_EXIT\"",
		"        log_warning 'Continuing without vault populate. You may need to run \"ocfp vault populate\" manually later.'",
		"    fi",
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
