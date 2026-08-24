package provision

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	// Line estimation constants for various operations.
	networkConnectivityLinesPerURL = 5
	networkConnectivityExtraLines  = 3
	serviceStatusLinesPerService   = 4
	serviceStatusExtraLines        = 3
	componentListExtraLines        = 8
	healthCheckBaseLines           = 32
	healthCheckTestURLLines        = 4
	healthCheckServiceLines        = 4
)

// VerificationManager handles comprehensive tool verification and health checks.
type VerificationManager struct {
	config   *config.Config
	provider string
	log      logger.Logger
}

// NewVerificationManager creates a new verification manager.
func NewVerificationManager(provider string, cfg *config.Config) *VerificationManager {
	return &VerificationManager{
		config:   cfg,
		provider: provider,
		log:      logger.Get(),
	}
}

// ToolVerification represents a tool verification configuration.
type ToolVerification struct {
	Name            string   `yaml:"name"`
	Commands        []string `yaml:"commands"`
	VersionCommand  string   `yaml:"versionCommand"`
	ConfigCheck     string   `yaml:"configCheck"`
	ServiceCheck    string   `yaml:"serviceCheck"`
	Required        bool     `yaml:"required"`
	PostInstallTest string   `yaml:"postInstallTest"`
}

// GetToolVerifications returns comprehensive tool verifications.
func (vm *VerificationManager) GetToolVerifications() []ToolVerification {
	verifications := vm.getCoreToolVerifications()
	verifications = append(verifications, vm.getProviderToolVerifications()...)

	return verifications
}

// GenerateVerificationScript generates comprehensive verification script.
func (vm *VerificationManager) GenerateVerificationScript(_ctx context.Context) string {
	verifications := vm.GetToolVerifications()

	lines := make([]string, 0, scriptBufferVerificationBase+scriptBufferVerificationPerItem*len(verifications))
	lines = append(lines, vm.getVerificationScriptHeader()...)
	lines = append(lines, vm.generateVerificationChecks(verifications)...)
	lines = append(lines, vm.getVerificationScriptFooter()...)

	return strings.Join(lines, "\n")
}

// GenerateHealthCheckScript generates health check for bastion services.
func (vm *VerificationManager) GenerateHealthCheckScript(_ctx context.Context) string {
	testUrls := []string{
		"google.com",
		"github.com",
		"releases.hashicorp.com",
		"packages.cloud.google.com",
		"packages.microsoft.com",
		"packages.stackit.cloud",
	}
	services := []string{"ssh", "snapd"}
	lines := make([]string, 0, healthCheckBaseLines+healthCheckTestURLLines*len(testUrls)+healthCheckServiceLines*len(services))

	lines = append(lines, vm.getHealthCheckHeader()...)
	lines = append(lines, vm.generateSystemHealthChecks()...)
	lines = append(lines, vm.generateNetworkConnectivityChecks(testUrls)...)
	lines = append(lines, vm.generateServiceStatusChecks(services)...)
	lines = append(lines, vm.generateVaultStatusCheck()...)

	return strings.Join(lines, "\n")
}

// GenerateProvisioningSummaryScript generates final provisioning summary.
func (vm *VerificationManager) GenerateProvisioningSummaryScript(_ctx context.Context) string {
	components := []string{
		"APT repositories",
		"APT packages",
		"Snap packages",
		"Binary tools",
		"Git repositories",
		"CPAN modules",
		"CF plugins",
		"Configuration files",
	}
	lines := make([]string, 0, scriptBufferVerification1+len(components)+scriptBufferVerification2)

	lines = append(lines, vm.getProvisioningSummaryHeader()...)
	lines = append(lines, vm.generateInstalledComponentsList(components)...)
	lines = append(lines, vm.generateDirectoryStructureCheck()...)
	lines = append(lines, vm.generatePerformanceInfo()...)
	lines = append(lines, vm.generateNextStepsInfo()...)
	lines = append(lines, vm.generateCompletionMarkers()...)

	return strings.Join(lines, "\n")
}

// GeneratePreRequisiteCheckScript generates prerequisite checking script.
func (vm *VerificationManager) GeneratePreRequisiteCheckScript(_ctx context.Context) string {
	lines := make([]string, 0, scriptBufferVerification3)

	lines = append(lines, "# Prerequisite checks")
	lines = append(lines, "")

	// Check if running as root
	lines = append(lines, "# Check if running as root")
	lines = append(lines, "if [ \"$(id -u)\" -eq 0 ]; then")
	lines = append(lines, "    log_error 'This script should not be run as root. Please run as the ubuntu user.'")
	lines = append(lines, "    exit 1")
	lines = append(lines, "fi")
	lines = append(lines, "")

	// Check sudo access
	lines = append(lines, "# Check sudo access")
	lines = append(lines, "if sudo -n true 2>/dev/null; then")
	lines = append(lines, "    log_info 'Sudo access verified'")
	lines = append(lines, "else")
	lines = append(lines, "    log_warning 'Sudo access may require password'")
	lines = append(lines, "fi")
	lines = append(lines, "")

	// Check internet connectivity
	lines = append(lines, "# Check internet connectivity")
	lines = append(lines, "if ping -c 1 8.8.8.8 >/dev/null 2>&1; then")
	lines = append(lines, "    log_info 'Internet connectivity verified'")
	lines = append(lines, "else")
	lines = append(lines, "    log_error 'No internet connectivity detected'")
	lines = append(lines, "    log_error 'Internet access is required for package installation'")
	lines = append(lines, "    exit 1")
	lines = append(lines, "fi")
	lines = append(lines, "")

	// Check available disk space
	lines = append(lines, "# Check available disk space")
	lines = append(lines, "AVAILABLE_SPACE=$(df / | tail -1 | awk '{print $4}')")
	lines = append(lines, "if [ \"$AVAILABLE_SPACE\" -gt 2097152 ]; then") // 2GB in KB
	lines = append(lines, "    log_info \"Sufficient disk space available: $(($AVAILABLE_SPACE / 1024 / 1024))GB\"")
	lines = append(lines, "else")
	lines = append(lines, "    log_warning \"Low disk space: $(($AVAILABLE_SPACE / 1024 / 1024))GB available\"")
	lines = append(lines, "    log_warning 'At least 2GB recommended for bastion provisioning'")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GenerateRootCheckScript generates root user check.
func (vm *VerificationManager) GenerateRootCheckScript() string {
	return `# Root user check
if [ "$(id -u)" -eq 0 ]; then
    log_error 'This script should not be run as root. Please run as the ubuntu user.'
    exit 1
fi
`
}

func (vm *VerificationManager) getCoreToolVerifications() []ToolVerification {
	return []ToolVerification{
		vm.getEssentialToolsVerification(),
		vm.getCloudFoundryToolsVerification(),
		vm.getOptionalCFToolsVerification(),
		vm.getDevelopmentToolsVerification(),
		vm.getPerlEnvironmentVerification(),
		vm.getGenesisToolsVerification(),
		vm.getGitConfigVerification(),
		vm.getOCFPStructureVerification(),
	}
}

func (vm *VerificationManager) getEssentialToolsVerification() ToolVerification {
	return ToolVerification{
		Name:            "essential-tools",
		Commands:        []string{"curl", "wget", "git", "unzip", "tar", "gzip"},
		Required:        true,
		VersionCommand:  "",
		ConfigCheck:     "",
		ServiceCheck:    "",
		PostInstallTest: "",
	}
}

func (vm *VerificationManager) getCloudFoundryToolsVerification() ToolVerification {
	// vault is only required when secrets_backend=vault; otherwise bao handles the
	// secrets path and vault lives in the optional group.
	commands := []string{"safe", "graft", "spruce", "jq", "bosh", "cf", "credhub"}
	versionCommand := "safe --version && graft --version && spruce --version && bosh --version && cf --version"

	if vm.config != nil && vm.config.SecretsBackendName() == "vault" {
		commands = append(commands, "vault")
		versionCommand += " && vault --version"
	}

	return ToolVerification{
		Name:            "cloudfoundry-tools",
		Commands:        commands,
		VersionCommand:  versionCommand,
		Required:        true,
		ConfigCheck:     "",
		ServiceCheck:    "",
		PostInstallTest: "",
	}
}

// getOptionalCFToolsVerification covers tools that are part of the OCFP toolchain
// but not required for the core BOSH/CF deploy path. bao is always optional (installed
// via brew); vault is optional when secrets_backend=openbao (the default). uaa is
// optional in all cases.
func (vm *VerificationManager) getOptionalCFToolsVerification() ToolVerification {
	commands := []string{"bao", "uaa"}

	// When using the default openbao backend, vault is also optional (not required).
	if vm.config == nil || vm.config.SecretsBackendName() != "vault" {
		commands = append(commands, "vault")
	}

	return ToolVerification{
		Name:            "optional-cf-tools",
		Commands:        commands,
		VersionCommand:  "",
		Required:        false,
		ConfigCheck:     "",
		ServiceCheck:    "",
		PostInstallTest: "",
	}
}

func (vm *VerificationManager) getDevelopmentToolsVerification() ToolVerification {
	return ToolVerification{
		Name:            "development-tools",
		Commands:        []string{"go", "nvim", "tmux", "screen"},
		VersionCommand:  "go version && nvim --version && tmux -V",
		Required:        false,
		ConfigCheck:     "",
		ServiceCheck:    "",
		PostInstallTest: "",
	}
}

func (vm *VerificationManager) getPerlEnvironmentVerification() ToolVerification {
	return ToolVerification{
		Name:            "perl-environment",
		Commands:        []string{"perl", "cpanm"},
		VersionCommand:  "perl --version && cpanm --version",
		PostInstallTest: "perl -e 'use YAML::XS; use JSON::PP; use Service::Vault;'",
		Required:        true,
		ConfigCheck:     "",
		ServiceCheck:    "",
	}
}

func (vm *VerificationManager) getGenesisToolsVerification() ToolVerification {
	return ToolVerification{
		Name:            "genesis-tools",
		Commands:        []string{"genesis", "yq"},
		VersionCommand:  "genesis version && yq --version",
		ConfigCheck:     "test -f $HOME/.genesis/config",
		Required:        true,
		ServiceCheck:    "",
		PostInstallTest: "",
	}
}

func (vm *VerificationManager) getGitConfigVerification() ToolVerification {
	return ToolVerification{
		Name:            "git-config",
		ConfigCheck:     "git config --global user.name && git config --global user.email",
		Required:        false,
		Commands:        nil,
		VersionCommand:  "",
		ServiceCheck:    "",
		PostInstallTest: "",
	}
}

func (vm *VerificationManager) getOCFPStructureVerification() ToolVerification {
	return ToolVerification{
		Name:            "ocfp-structure",
		ConfigCheck:     "test -d $HOME/ocfp && test -d $HOME/ocfp/deployments && test -L $HOME/ops",
		Required:        true,
		Commands:        nil,
		VersionCommand:  "",
		ServiceCheck:    "",
		PostInstallTest: "",
	}
}

func (vm *VerificationManager) getProviderToolVerifications() []ToolVerification {
	switch vm.provider {
	case providerStackit:
		return []ToolVerification{vm.getStackitToolsVerification()}
	case providerAWS:
		return []ToolVerification{vm.getAWSToolsVerification()}
	case providerAzure:
		return []ToolVerification{vm.getAzureToolsVerification()}
	case providerGCP:
		return []ToolVerification{vm.getGCPToolsVerification()}
	case providerOpenStack:
		return []ToolVerification{vm.getOpenStackToolsVerification()}
	case providerPVE:
		return []ToolVerification{vm.getPVEToolsVerification()}
	}

	return nil
}

// getPVEToolsVerification checks pmx, the Proxmox CLI installed on PVE bastions.
// No ConfigCheck runs here: pmx needs API credentials that are configured later.
func (vm *VerificationManager) getPVEToolsVerification() ToolVerification {
	return ToolVerification{
		Name:            "pve-tools",
		Commands:        []string{"pmx"},
		VersionCommand:  "pmx --version",
		ConfigCheck:     "",
		Required:        true,
		ServiceCheck:    "",
		PostInstallTest: "",
	}
}

func (vm *VerificationManager) getStackitToolsVerification() ToolVerification {
	return ToolVerification{
		Name:            "stackit-tools",
		Commands:        []string{"stackit"},
		VersionCommand:  "stackit --version",
		ConfigCheck:     "stackit config list",
		Required:        true,
		ServiceCheck:    "",
		PostInstallTest: "",
	}
}

func (vm *VerificationManager) getAWSToolsVerification() ToolVerification {
	return ToolVerification{
		Name:            "aws-tools",
		Commands:        []string{"aws"},
		VersionCommand:  "aws --version",
		ConfigCheck:     "aws sts get-caller-identity",
		Required:        true,
		ServiceCheck:    "",
		PostInstallTest: "",
	}
}

func (vm *VerificationManager) getAzureToolsVerification() ToolVerification {
	return ToolVerification{
		Name:            "azure-tools",
		Commands:        []string{"az"},
		VersionCommand:  "az --version",
		ConfigCheck:     "az account show",
		Required:        true,
		ServiceCheck:    "",
		PostInstallTest: "",
	}
}

func (vm *VerificationManager) getGCPToolsVerification() ToolVerification {
	return ToolVerification{
		Name:            "gcp-tools",
		Commands:        []string{"gcloud"},
		VersionCommand:  "gcloud --version",
		ConfigCheck:     "gcloud config list",
		Required:        true,
		ServiceCheck:    "",
		PostInstallTest: "",
	}
}

func (vm *VerificationManager) getOpenStackToolsVerification() ToolVerification {
	return ToolVerification{
		Name:            "openstack-tools",
		Commands:        []string{"openstack"},
		VersionCommand:  "openstack --version",
		Required:        true,
		ConfigCheck:     "",
		ServiceCheck:    "",
		PostInstallTest: "",
	}
}

func (vm *VerificationManager) getVerificationScriptHeader() []string {
	return []string{
		"# Comprehensive tool verification and health checks",
		"",
		"log_info 'Starting comprehensive verification'",
		"VERIFICATION_FAILED=false",
		"VERIFICATION_WARNINGS=false",
		"",
	}
}

func (vm *VerificationManager) generateVerificationChecks(verifications []ToolVerification) []string {
	lines := make([]string, 0, len(verifications)*scriptBufferVerificationPerItem)

	for _, verification := range verifications {
		lines = append(lines, "# Verify "+verification.Name)
		lines = append(lines, fmt.Sprintf("log_info 'Verifying %s'", verification.Name))
		lines = append(lines, vm.generateCommandChecks(verification)...)
		lines = append(lines, vm.generateVersionChecks(verification)...)
		lines = append(lines, vm.generateConfigChecks(verification)...)
		lines = append(lines, vm.generatePostInstallTests(verification)...)
	}

	return lines
}

func (vm *VerificationManager) generateCommandChecks(verification ToolVerification) []string {
	var lines []string

	if len(verification.Commands) > 0 {
		lines = append(lines, fmt.Sprintf("# Check %s commands", verification.Name))
		for _, cmd := range verification.Commands {
			lines = append(lines, fmt.Sprintf("if command -v %s >/dev/null 2>&1; then", cmd))
			lines = append(lines, fmt.Sprintf("    log_info '  ✓ %s found'", cmd))
			lines = append(lines, "else")

			if verification.Required {
				lines = append(lines, fmt.Sprintf("    log_error '  ✗ %s missing (required)'", cmd))
				lines = append(lines, "    VERIFICATION_FAILED=true")
			} else {
				lines = append(lines, fmt.Sprintf("    log_warning '  ⚠ %s missing (optional)'", cmd))
				lines = append(lines, "    VERIFICATION_WARNINGS=true")
			}

			lines = append(lines, "fi")
		}

		lines = append(lines, "")
	}

	return lines
}

func (vm *VerificationManager) generateVersionChecks(verification ToolVerification) []string {
	var lines []string

	if verification.VersionCommand != "" {
		lines = append(lines, fmt.Sprintf("# Check %s versions", verification.Name))
		lines = append(lines, fmt.Sprintf("log_info 'Checking %s versions'", verification.Name))
		lines = append(lines, fmt.Sprintf("if %s >/dev/null 2>&1; then", verification.VersionCommand))
		lines = append(lines, fmt.Sprintf("    VERSION_INFO=$(%s 2>&1 | head -3)", verification.VersionCommand))
		lines = append(lines, fmt.Sprintf("    log_info 'Version info for %s:'", verification.Name))
		lines = append(lines, `    echo "$VERSION_INFO" | while IFS= read -r line; do`)
		lines = append(lines, `        log_info "    $line"`)
		lines = append(lines, "    done")
		lines = append(lines, "else")
		lines = append(lines, fmt.Sprintf("    log_warning 'Could not get version information for %s'", verification.Name))
		lines = append(lines, "    VERIFICATION_WARNINGS=true")
		lines = append(lines, "fi")
		lines = append(lines, "")
	}

	return lines
}

func (vm *VerificationManager) generateConfigChecks(verification ToolVerification) []string {
	var lines []string

	if verification.ConfigCheck != "" {
		lines = append(lines, fmt.Sprintf("# Check %s configuration", verification.Name))
		lines = append(lines, fmt.Sprintf("if %s >/dev/null 2>&1; then", verification.ConfigCheck))
		lines = append(lines, fmt.Sprintf("    log_success '%s configuration verified'", verification.Name))
		lines = append(lines, "else")

		if verification.Required {
			lines = append(lines, fmt.Sprintf("    log_error '%s configuration check failed'", verification.Name))
			lines = append(lines, "    VERIFICATION_FAILED=true")
		} else {
			lines = append(lines, fmt.Sprintf("    log_warning '%s configuration check failed (optional)'", verification.Name))
			lines = append(lines, "    VERIFICATION_WARNINGS=true")
		}

		lines = append(lines, "fi")
		lines = append(lines, "")
	}

	return lines
}

func (vm *VerificationManager) generatePostInstallTests(verification ToolVerification) []string {
	var lines []string

	if verification.PostInstallTest != "" {
		lines = append(lines, "# Post-install test for "+verification.Name)
		lines = append(lines, fmt.Sprintf("if %s >/dev/null 2>&1; then", verification.PostInstallTest))
		lines = append(lines, fmt.Sprintf("    log_success '%s post-install test passed'", verification.Name))
		lines = append(lines, "else")
		lines = append(lines, fmt.Sprintf("    log_warning '%s post-install test failed'", verification.Name))
		lines = append(lines, "    VERIFICATION_WARNINGS=true")
		lines = append(lines, "fi")
		lines = append(lines, "")
	}

	return lines
}

func (vm *VerificationManager) getVerificationScriptFooter() []string {
	return []string{
		"# Final verification result",
		"if [ \"$VERIFICATION_FAILED\" = \"true\" ]; then",
		"    log_error 'Verification failed - required tools or configurations are missing'",
		"    log_error 'Please review errors above and re-run provisioning if needed'",
		"elif [ \"$VERIFICATION_WARNINGS\" = \"true\" ]; then",
		"    log_warning 'Verification completed with warnings'",
		"    log_warning 'Review warnings above - some optional components may be missing'",
		"else",
		"    log_success 'All verifications passed'",
		"fi",
		"",
	}
}

func (vm *VerificationManager) getHealthCheckHeader() []string {
	return []string{
		"# Bastion health check",
		"",
		"log_info 'Performing bastion health check'",
		"",
	}
}

func (vm *VerificationManager) generateSystemHealthChecks() []string {
	return []string{
		"# System health",
		"log_info 'System health:'",
		"LOAD_AVG=$(uptime | awk -F'load average:' '{print $2}' | sed 's/^[[:space:]]*//')",
		"log_info \"  Load average: $LOAD_AVG\"",
		"",
		"DISK_USAGE=$(df -h / | tail -1 | awk '{print $5}')",
		"log_info \"  Root disk usage: $DISK_USAGE\"",
		"",
		"MEMORY_USAGE=$(free | grep Mem | awk '{printf \"%.1f%%\", $3/$2 * 100.0}')",
		"log_info \"  Memory usage: $MEMORY_USAGE\"",
		"",
	}
}

func (vm *VerificationManager) generateNetworkConnectivityChecks(testUrls []string) []string {
	lines := make([]string, 0, len(testUrls)*networkConnectivityLinesPerURL+networkConnectivityExtraLines)

	lines = append(lines, "# Network connectivity")
	lines = append(lines, "log_info 'Network connectivity:'")

	for _, url := range testUrls {
		lines = append(lines, fmt.Sprintf("if ping -c 1 %s >/dev/null 2>&1; then", url))
		lines = append(lines, fmt.Sprintf("    log_info '  ✓ %s reachable'", url))
		lines = append(lines, "else")
		lines = append(lines, fmt.Sprintf("    log_warning '  ⚠ %s unreachable'", url))
		lines = append(lines, "fi")
	}

	lines = append(lines, "")

	return lines
}

func (vm *VerificationManager) generateServiceStatusChecks(services []string) []string {
	lines := make([]string, 0, len(services)*serviceStatusLinesPerService+serviceStatusExtraLines)

	lines = append(lines, "# Service status")
	lines = append(lines, "log_info 'Service status:'")

	for _, service := range services {
		lines = append(lines, fmt.Sprintf("if systemctl is-active %s >/dev/null 2>&1; then", service))
		lines = append(lines, fmt.Sprintf("    log_info '  ✓ %s service active'", service))
		lines = append(lines, "else")
		lines = append(lines, fmt.Sprintf("    log_warning '  ⚠ %s service inactive'", service))
		lines = append(lines, "fi")
	}

	lines = append(lines, "")

	return lines
}

func (vm *VerificationManager) generateVaultStatusCheck() []string {
	return []string{
		"# Vault status",
		"if command -v safe >/dev/null 2>&1; then",
		"    VAULT_STATUS=$(safe target 2>&1 || echo 'not-targeted')",
		"    if echo \"$VAULT_STATUS\" | grep -q 'inception\\|production'; then",
		"        log_success '  ✓ Vault targeted and accessible'",
		"    else",
		"        log_info \"  ⚠ Vault status: $VAULT_STATUS\"",
		"    fi",
		"else",
		"    log_info '  Safe command not available for vault check'",
		"fi",
		"",
	}
}

func (vm *VerificationManager) getProvisioningSummaryHeader() []string {
	return []string{
		"# Generate provisioning summary",
		"",
		"log_info '=== Provisioning Summary ==='",
		"START_TIME=${start_time:-$(date +%s)}",
		"END_TIME=$(date +%s)",
		"DURATION=$((END_TIME - START_TIME))",
		"log_info \"Total duration: ${DURATION} seconds\"",
		"log_info \"Log file: ${LOG_FILE}\"",
		"",
	}
}

func (vm *VerificationManager) generateInstalledComponentsList(components []string) []string {
	lines := make([]string, 0, len(components)+componentListExtraLines)

	lines = append(lines, "# List installed components")
	lines = append(lines, "INSTALLED_COMPONENTS=()")

	for _, component := range components {
		lines = append(lines, fmt.Sprintf("INSTALLED_COMPONENTS+=('%s')", component))
	}

	lines = append(lines, "")
	lines = append(lines, "if [ ${#INSTALLED_COMPONENTS[@]} -gt 0 ]; then")
	lines = append(lines, "    log_info 'Components installed:'")
	lines = append(lines, "    for component in \"${INSTALLED_COMPONENTS[@]}\"; do")
	lines = append(lines, "        log_info \"  - $component\"")
	lines = append(lines, "    done")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return lines
}

func (vm *VerificationManager) generateDirectoryStructureCheck() []string {
	return []string{
		"# List created directories",
		"CREATED_DIRS=(",
		"    \"$HOME/ocfp\"",
		"    \"$HOME/ocfp/deployments\"",
		"    \"$HOME/ocfp/releases\"",
		"    \"$HOME/ocfp/artifacts\"",
		"    \"$HOME/ocfp/cli\"",
		"    \"$HOME/.ocfp\"",
		"    \"$HOME/bin\"",
		")",
		"",
		"log_info 'Directory structure:'",
		"for dir in \"${CREATED_DIRS[@]}\"; do",
		"    if [ -d \"$dir\" ]; then",
		"        log_info \"  ✓ $dir\"",
		"    else",
		"        log_warning \"  ✗ $dir (missing)\"",
		"    fi",
		"done",
		"",
	}
}

func (vm *VerificationManager) generatePerformanceInfo() []string {
	return []string{
		"# Performance information",
		"MEMORY_USAGE=$(ps -o rss= -p $$ 2>/dev/null | awk '{print $1}' || echo 'unknown')",
		"log_info \"Peak memory usage: ${MEMORY_USAGE} KB\"",
		"",
	}
}

func (vm *VerificationManager) generateNextStepsInfo() []string {
	return []string{
		"# Next steps information",
		"log_success '=== Provisioning Complete ==='",
		"log_info 'Next steps:'",
		"log_info '  1. Run: source ~/.bashrc (or start new shell session)'",
		"log_info '  2. OCFP directories:'",
		"log_info '     - Deployments: ~/ocfp/deployments (symlinked as ~/ops)'",
		"log_info '     - Releases: ~/ocfp/releases'",
		"log_info '     - Artifacts: ~/ocfp/artifacts'",
		"log_info '     - Kits: ~/ocfp/kits (Genesis kit repositories)'",
		"log_info '  3. Available tools: genesis (g), safe, graft (as spruce), vault, bosh, cf'",
		"",
	}
}

func (vm *VerificationManager) generateCompletionMarkers() []string {
	return []string{
		"# Create completion markers",
		"touch \"$HOME/.ocfp/provisioned\"",
		"echo \"$(date)\" > \"$HOME/.ocfp/provisioned\"",
		"touch \"$HOME/.ocfp/bastion-init-completed\"",
		"echo \"$(date)\" > \"$HOME/.ocfp/bastion-init-completed\"",
		"",
	}
}
