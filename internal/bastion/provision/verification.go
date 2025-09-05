package provision

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// VerificationManager handles comprehensive tool verification and health checks
type VerificationManager struct {
	config   *config.Config
	provider string
	log      logger.Logger
}

// NewVerificationManager creates a new verification manager
func NewVerificationManager(provider string, cfg *config.Config) *VerificationManager {
	return &VerificationManager{
		config:   cfg,
		provider: provider,
		log:      logger.Get(),
	}
}

// ToolVerification represents a tool verification configuration
type ToolVerification struct {
	Name            string   `yaml:"name"`
	Commands        []string `yaml:"commands"`
	VersionCommand  string   `yaml:"versionCommand"`
	ConfigCheck     string   `yaml:"configCheck"`
	ServiceCheck    string   `yaml:"serviceCheck"`
	Required        bool     `yaml:"required"`
	PostInstallTest string   `yaml:"postInstallTest"`
}

// GetToolVerifications returns comprehensive tool verifications
func (vm *VerificationManager) GetToolVerifications() []ToolVerification {
	verifications := []ToolVerification{
		// Core system tools
		{
			Name:     "essential-tools",
			Commands: []string{"curl", "wget", "git", "unzip", "tar", "gzip"},
			Required: true,
		},

		// CloudFoundry tools
		{
			Name:           "cloudfoundry-tools",
			Commands:       []string{"safe", "spruce", "vault", "jq", "bosh", "cf", "credhub", "uaa"},
			VersionCommand: "safe --version && spruce --version && vault --version && bosh --version && cf --version",
			Required:       true,
		},

		// Development tools
		{
			Name:           "development-tools",
			Commands:       []string{"go", "nvim", "tmux", "screen"},
			VersionCommand: "go version && nvim --version && tmux -V",
			Required:       false,
		},

		// Perl environment
		{
			Name:            "perl-environment",
			Commands:        []string{"perl", "cpanm"},
			VersionCommand:  "perl --version && cpanm --version",
			PostInstallTest: "perl -e 'use YAML::XS; use JSON::PP; use Service::Vault;'",
			Required:        true,
		},

		// Genesis tools
		{
			Name:           "genesis-tools",
			Commands:       []string{"genesis", "yq"},
			VersionCommand: "genesis version && yq --version",
			ConfigCheck:    "test -f $HOME/.genesis/config",
			Required:       true,
		},

		// Git configuration
		{
			Name:        "git-config",
			ConfigCheck: "git config --global user.name && git config --global user.email",
			Required:    false,
		},

		// OCFP structure
		{
			Name:        "ocfp-structure",
			ConfigCheck: "test -d $HOME/ocfp && test -d $HOME/ocfp/deployments && test -L $HOME/ops",
			Required:    true,
		},
	}

	// Add provider-specific verifications
	switch vm.provider {
	case "stackit":
		verifications = append(verifications, ToolVerification{
			Name:           "stackit-tools",
			Commands:       []string{"stackit"},
			VersionCommand: "stackit --version",
			ConfigCheck:    "stackit config list",
			Required:       true,
		})
	case "aws":
		verifications = append(verifications, ToolVerification{
			Name:           "aws-tools",
			Commands:       []string{"aws"},
			VersionCommand: "aws --version",
			ConfigCheck:    "aws sts get-caller-identity",
			Required:       true,
		})
	case "azure":
		verifications = append(verifications, ToolVerification{
			Name:           "azure-tools",
			Commands:       []string{"az"},
			VersionCommand: "az --version",
			ConfigCheck:    "az account show",
			Required:       true,
		})
	case "gcp":
		verifications = append(verifications, ToolVerification{
			Name:           "gcp-tools",
			Commands:       []string{"gcloud"},
			VersionCommand: "gcloud --version",
			ConfigCheck:    "gcloud config list",
			Required:       true,
		})
	case "openstack":
		verifications = append(verifications, ToolVerification{
			Name:           "openstack-tools",
			Commands:       []string{"openstack"},
			VersionCommand: "openstack --version",
			Required:       true,
		})
	}

	return verifications
}

// GenerateVerificationScript generates comprehensive verification script
func (vm *VerificationManager) GenerateVerificationScript(ctx context.Context) string {
	verifications := vm.GetToolVerifications()

	var lines []string
	lines = append(lines, "# Comprehensive tool verification and health checks")
	lines = append(lines, "")

	lines = append(lines, "log_info 'Starting comprehensive verification'")
	lines = append(lines, "VERIFICATION_FAILED=false")
	lines = append(lines, "VERIFICATION_WARNINGS=false")
	lines = append(lines, "")

	for _, verification := range verifications {
		lines = append(lines, fmt.Sprintf("# Verify %s", verification.Name))
		lines = append(lines, fmt.Sprintf("log_info 'Verifying %s'", verification.Name))

		// Check commands exist
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

		// Check versions
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

		// Check configuration
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

		// Post-install test
		if verification.PostInstallTest != "" {
			lines = append(lines, fmt.Sprintf("# Post-install test for %s", verification.Name))
			lines = append(lines, fmt.Sprintf("if %s >/dev/null 2>&1; then", verification.PostInstallTest))
			lines = append(lines, fmt.Sprintf("    log_success '%s post-install test passed'", verification.Name))
			lines = append(lines, "else")
			lines = append(lines, fmt.Sprintf("    log_warning '%s post-install test failed'", verification.Name))
			lines = append(lines, "    VERIFICATION_WARNINGS=true")
			lines = append(lines, "fi")
			lines = append(lines, "")
		}
	}

	// Final verification result
	lines = append(lines, "# Final verification result")
	lines = append(lines, "if [ \"$VERIFICATION_FAILED\" = \"true\" ]; then")
	lines = append(lines, "    log_error 'Verification failed - required tools or configurations are missing'")
	lines = append(lines, "    return 1")
	lines = append(lines, "elif [ \"$VERIFICATION_WARNINGS\" = \"true\" ]; then")
	lines = append(lines, "    log_warning 'Verification completed with warnings'")
	lines = append(lines, "    return 0")
	lines = append(lines, "else")
	lines = append(lines, "    log_success 'All verifications passed'")
	lines = append(lines, "    return 0")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GenerateHealthCheckScript generates health check for bastion services
func (vm *VerificationManager) GenerateHealthCheckScript(ctx context.Context) string {
	var lines []string
	lines = append(lines, "# Bastion health check")
	lines = append(lines, "")

	lines = append(lines, "log_info 'Performing bastion health check'")
	lines = append(lines, "")

	// Check system health
	lines = append(lines, "# System health")
	lines = append(lines, "log_info 'System health:'")
	lines = append(lines, "LOAD_AVG=$(uptime | awk -F'load average:' '{print $2}' | sed 's/^[[:space:]]*//')")
	lines = append(lines, "log_info \"  Load average: $LOAD_AVG\"")
	lines = append(lines, "")

	lines = append(lines, "DISK_USAGE=$(df -h / | tail -1 | awk '{print $5}')")
	lines = append(lines, "log_info \"  Root disk usage: $DISK_USAGE\"")
	lines = append(lines, "")

	lines = append(lines, "MEMORY_USAGE=$(free | grep Mem | awk '{printf \"%.1f%%\", $3/$2 * 100.0}')")
	lines = append(lines, "log_info \"  Memory usage: $MEMORY_USAGE\"")
	lines = append(lines, "")

	// Check network connectivity
	lines = append(lines, "# Network connectivity")
	lines = append(lines, "log_info 'Network connectivity:'")

	testUrls := []string{
		"google.com",
		"github.com",
		"releases.hashicorp.com",
		"packages.cloud.google.com",
		"packages.microsoft.com",
		"packages.stackit.cloud",
	}

	for _, url := range testUrls {
		lines = append(lines, fmt.Sprintf("if ping -c 1 %s >/dev/null 2>&1; then", url))
		lines = append(lines, fmt.Sprintf("    log_info '  ✓ %s reachable'", url))
		lines = append(lines, "else")
		lines = append(lines, fmt.Sprintf("    log_warning '  ⚠ %s unreachable'", url))
		lines = append(lines, "fi")
	}
	lines = append(lines, "")

	// Check services
	lines = append(lines, "# Service status")
	lines = append(lines, "log_info 'Service status:'")

	services := []string{"ssh", "snapd"}
	for _, service := range services {
		lines = append(lines, fmt.Sprintf("if systemctl is-active %s >/dev/null 2>&1; then", service))
		lines = append(lines, fmt.Sprintf("    log_info '  ✓ %s service active'", service))
		lines = append(lines, "else")
		lines = append(lines, fmt.Sprintf("    log_warning '  ⚠ %s service inactive'", service))
		lines = append(lines, "fi")
	}
	lines = append(lines, "")

	// Check vault if available
	lines = append(lines, "# Vault status")
	lines = append(lines, "if command -v safe >/dev/null 2>&1; then")
	lines = append(lines, "    VAULT_STATUS=$(safe target 2>&1 || echo 'not-targeted')")
	lines = append(lines, "    if echo \"$VAULT_STATUS\" | grep -q 'inception\\|production'; then")
	lines = append(lines, "        log_success '  ✓ Vault targeted and accessible'")
	lines = append(lines, "    else")
	lines = append(lines, "        log_info \"  ⚠ Vault status: $VAULT_STATUS\"")
	lines = append(lines, "    fi")
	lines = append(lines, "else")
	lines = append(lines, "    log_info '  Safe command not available for vault check'")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GenerateProvisioningSummaryScript generates final provisioning summary
func (vm *VerificationManager) GenerateProvisioningSummaryScript(ctx context.Context) string {
	var lines []string
	lines = append(lines, "# Generate provisioning summary")
	lines = append(lines, "")

	lines = append(lines, "log_info '=== Provisioning Summary ==='")
	lines = append(lines, "START_TIME=${start_time:-$(date +%s)}")
	lines = append(lines, "END_TIME=$(date +%s)")
	lines = append(lines, "DURATION=$((END_TIME - START_TIME))")
	lines = append(lines, "log_info \"Total duration: ${DURATION} seconds\"")
	lines = append(lines, "log_info \"Log file: ${LOG_FILE}\"")
	lines = append(lines, "")

	// List installed components
	lines = append(lines, "# List installed components")
	lines = append(lines, "INSTALLED_COMPONENTS=()")

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

	// List created directories
	lines = append(lines, "# List created directories")
	lines = append(lines, "CREATED_DIRS=(")
	lines = append(lines, "    \"$HOME/ocfp\"")
	lines = append(lines, "    \"$HOME/ocfp/deployments\"")
	lines = append(lines, "    \"$HOME/ocfp/releases\"")
	lines = append(lines, "    \"$HOME/ocfp/artifacts\"")
	lines = append(lines, "    \"$HOME/ocfp/cli\"")
	lines = append(lines, "    \"$HOME/.ocfp\"")
	lines = append(lines, "    \"$HOME/bin\"")
	lines = append(lines, ")")
	lines = append(lines, "")

	lines = append(lines, "log_info 'Directory structure:'")
	lines = append(lines, "for dir in \"${CREATED_DIRS[@]}\"; do")
	lines = append(lines, "    if [ -d \"$dir\" ]; then")
	lines = append(lines, "        log_info \"  ✓ $dir\"")
	lines = append(lines, "    else")
	lines = append(lines, "        log_warning \"  ✗ $dir (missing)\"")
	lines = append(lines, "    fi")
	lines = append(lines, "done")
	lines = append(lines, "")

	// Performance info
	lines = append(lines, "# Performance information")
	lines = append(lines, "MEMORY_USAGE=$(ps -o rss= -p $$ 2>/dev/null | awk '{print $1}' || echo 'unknown')")
	lines = append(lines, "log_info \"Peak memory usage: ${MEMORY_USAGE} KB\"")
	lines = append(lines, "")

	// Next steps
	lines = append(lines, "# Next steps information")
	lines = append(lines, "log_success '=== Provisioning Complete ==='")
	lines = append(lines, "log_info 'Next steps:'")
	lines = append(lines, "log_info '  1. Run: source ~/.bashrc (or start new shell session)'")
	lines = append(lines, "log_info '  2. OCFP directories:'")
	lines = append(lines, "log_info '     - Deployments: ~/ocfp/deployments (symlinked as ~/ops)'")
	lines = append(lines, "log_info '     - Releases: ~/ocfp/releases'")
	lines = append(lines, "log_info '     - Artifacts: ~/ocfp/artifacts'")
	lines = append(lines, "log_info '     - Kits: ~/ocfp/kits (Genesis kit repositories)'")
	lines = append(lines, "log_info '  3. Available tools: genesis (g), safe, spruce, vault, bosh, cf'")
	lines = append(lines, "")

	// Create completion marker
	lines = append(lines, "# Create completion markers")
	lines = append(lines, "touch \"$HOME/.ocfp/provisioned\"")
	lines = append(lines, "echo \"$(date)\" > \"$HOME/.ocfp/provisioned\"")
	lines = append(lines, "touch \"$HOME/.ocfp/bastion-init-completed\"")
	lines = append(lines, "echo \"$(date)\" > \"$HOME/.ocfp/bastion-init-completed\"")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GeneratePreRequisiteCheckScript generates prerequisite checking script
func (vm *VerificationManager) GeneratePreRequisiteCheckScript(ctx context.Context) string {
	var lines []string
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
	lines = append(lines, "    return 1")
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

// GenerateRootCheckScript generates root user check
func (vm *VerificationManager) GenerateRootCheckScript() string {
	return `# Root user check
if [ "$(id -u)" -eq 0 ]; then
    log_error 'This script should not be run as root. Please run as the ubuntu user.'
    exit 1
fi
`
}
