package provision

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// CPANManager handles CPAN module installations.
type CPANManager struct {
	config   *config.Config
	provider string
	log      logger.Logger
}

// CPANModule represents a CPAN module configuration.
type CPANModule struct {
	Name    string `yaml:"name"`
	Enabled bool   `yaml:"enabled"`
	NoTest  bool   `yaml:"notest"`
	Force   bool   `yaml:"force"`
	Sudo    bool   `yaml:"sudo"`
}

// NewCPANManager creates a new CPAN manager.
func NewCPANManager(provider string, cfg *config.Config) *CPANManager {
	return &CPANManager{
		config:   cfg,
		provider: provider,
		log:      logger.Get(),
	}
}

// GetCPANModules returns the list of CPAN modules to install.
func (cm *CPANManager) GetCPANModules() []CPANModule {
	return []CPANModule{
		// Required for OCFP core functionality
		{Name: "YAML::XS", NoTest: true},
		{Name: "JSON::PP", NoTest: true},
		{Name: "Try::Tiny", NoTest: true},
		{Name: "Time::HiRes", NoTest: true},
		{Name: "Digest::SHA", NoTest: true},
		{Name: "Service::Vault", NoTest: true},
		{Name: "Graph", NoTest: true},

		// Development and debugging tools
		{Name: "Perl::Tidy", NoTest: true},
		{Name: "Perl::Critic", NoTest: true},
		{Name: "autodie", NoTest: true},
		{Name: "App::Ack", NoTest: true},
		{Name: "Term::ReadLine::Gnu", NoTest: true},
		{Name: "Reply", NoTest: true},
		{Name: "Data::Printer", NoTest: true},
		{Name: "Devel::REPL", NoTest: true},
		{Name: "B::Keywords", NoTest: true},
		{Name: "Lexical::Persistence", NoTest: true},
		{Name: "PPI", NoTest: true},
	}
}

// GenerateCPANInstallScript generates script for CPAN module installation.
func (cm *CPANManager) GenerateCPANInstallScript(ctx context.Context) string {
	modules := cm.GetCPANModules()
	if len(modules) == 0 {
		return ""
	}

	var lines []string

	lines = append(lines, "# CPAN module installation")
	lines = append(lines, "")

	// Ensure cpanminus is available
	lines = append(lines, "# Ensure cpanm is available")
	lines = append(lines, "if ! command -v cpanm >/dev/null 2>&1; then")
	lines = append(lines, "    log_info 'Installing cpanminus'")
	lines = append(lines, "    sudo apt-get install -y cpanminus")
	lines = append(lines, "    if [ $? -eq 0 ]; then")
	lines = append(lines, "        log_success 'cpanminus installed successfully'")
	lines = append(lines, "    else")
	lines = append(lines, "        log_error 'Failed to install cpanminus'")
	lines = append(lines, "        return 1")
	lines = append(lines, "    fi")
	lines = append(lines, "else")
	lines = append(lines, "    log_info 'cpanminus already available'")
	lines = append(lines, "fi")
	lines = append(lines, "")

	// Install modules individually with proper error handling
	for _, module := range modules {
		if !module.Enabled {
			continue
		}

		lines = append(lines, "# Install CPAN module: "+module.Name)
		lines = append(lines, fmt.Sprintf("log_info 'Installing CPAN module: %s'", module.Name))

		// Check if module is already installed
		lines = append(lines, fmt.Sprintf("perl -e 'use %s; print \"installed\\n\"' >/dev/null 2>&1", module.Name))
		lines = append(lines, "if [ $? -eq 0 ]; then")
		lines = append(lines, fmt.Sprintf("    log_info 'CPAN module %s already installed'", module.Name))
		lines = append(lines, "else")

		// Build cpanm command
		installCmd := "cpanm"

		if module.NoTest {
			installCmd += " --notest"
		}

		if module.Force {
			installCmd += " --force"
		}

		if module.Sudo {
			installCmd = "sudo " + installCmd
		}

		installCmd += fmt.Sprintf(" '%s'", module.Name)

		lines = append(lines, "    "+installCmd)
		lines = append(lines, "    if [ $? -eq 0 ]; then")
		lines = append(lines, fmt.Sprintf("        log_success 'CPAN module %s installed successfully'", module.Name))
		lines = append(lines, "    else")
		lines = append(lines, fmt.Sprintf("        log_warning 'Failed to install CPAN module %s'", module.Name))
		lines = append(lines, "    fi")
		lines = append(lines, "fi")
		lines = append(lines, "")
	}

	// Also install system-wide with sudo
	lines = append(lines, "# Install critical CPAN modules system-wide")
	criticalModules := []string{"YAML::XS", "JSON::PP", "Try::Tiny", "Service::Vault"}

	for _, module := range criticalModules {
		lines = append(lines, fmt.Sprintf("log_info 'Installing %s system-wide'", module))
		lines = append(lines, fmt.Sprintf("sudo cpanm --notest '%s' || log_warning 'Failed to install %s system-wide'", module, module))
	}

	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// InstallOCFPPerlDependencies installs OCFP Perl dependencies from Makefile.PL.
func (cm *CPANManager) InstallOCFPPerlDependencies(ctx context.Context) string {
	var lines []string

	lines = append(lines, "# Install OCFP Perl dependencies")
	lines = append(lines, "")

	// Check multiple possible locations for Makefile.PL
	lines = append(lines, "# Find and install from Makefile.PL")
	lines = append(lines, "MAKEFILE_LOCATIONS=(")
	lines = append(lines, `    "${HOME}/ocfp/ocfp-cli/Makefile.PL"`)
	lines = append(lines, `    "${HOME}/ocfp/cli/Makefile.PL"`)
	lines = append(lines, `    "${HOME}/ocfp/cli/perl/Makefile.PL"`)
	lines = append(lines, ")")
	lines = append(lines, "")

	lines = append(lines, "MAKEFILE_PL=\"\"")
	lines = append(lines, "for location in \"${MAKEFILE_LOCATIONS[@]}\"; do")
	lines = append(lines, "    if [ -f \"$location\" ]; then")
	lines = append(lines, "        MAKEFILE_PL=\"$location\"")
	lines = append(lines, "        MAKEFILE_DIR=$(dirname \"$location\")")
	lines = append(lines, "        log_info \"Found Makefile.PL at: $location\"")
	lines = append(lines, "        break")
	lines = append(lines, "    fi")
	lines = append(lines, "done")
	lines = append(lines, "")

	lines = append(lines, "if [ -n \"$MAKEFILE_PL\" ]; then")
	lines = append(lines, "    log_info \"Installing Perl dependencies from $MAKEFILE_DIR\"")
	lines = append(lines, "    cd \"$MAKEFILE_DIR\"")
	lines = append(lines, "    ")
	lines = append(lines, "    # Install user dependencies")
	lines = append(lines, "    cpanm --installdeps . --notest")
	lines = append(lines, "    if [ $? -eq 0 ]; then")
	lines = append(lines, "        log_success 'User Perl dependencies installed successfully'")
	lines = append(lines, "    else")
	lines = append(lines, "        log_warning 'Some user Perl dependencies failed to install'")
	lines = append(lines, "    fi")
	lines = append(lines, "    ")
	lines = append(lines, "    # Install system-wide dependencies")
	lines = append(lines, "    sudo cpanm --installdeps .")
	lines = append(lines, "    if [ $? -eq 0 ]; then")
	lines = append(lines, "        log_success 'System Perl dependencies installed successfully'")
	lines = append(lines, "    else")
	lines = append(lines, "        log_warning 'Some system Perl dependencies failed to install'")
	lines = append(lines, "    fi")
	lines = append(lines, "else")
	lines = append(lines, "    log_info 'Makefile.PL not found yet, will be available after OCFP CLI is copied'")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// shouldSkipCondition evaluates whether a condition should be skipped
// (Removed unused condition helper; CPAN modules currently have no provider condition)
