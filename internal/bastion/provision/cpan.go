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
		{Name: "YAML::XS", NoTest: true, Enabled: true, Force: false, Sudo: false},
		{Name: "JSON::PP", NoTest: true, Enabled: true, Force: false, Sudo: false},
		{Name: "Try::Tiny", NoTest: true, Enabled: true, Force: false, Sudo: false},
		{Name: "Time::HiRes", NoTest: true, Enabled: true, Force: false, Sudo: false},
		{Name: "Digest::SHA", NoTest: true, Enabled: true, Force: false, Sudo: false},
		{Name: "Service::Vault", NoTest: true, Enabled: true, Force: false, Sudo: false},
		{Name: "Graph", NoTest: true, Enabled: true, Force: false, Sudo: false},

		// Development and debugging tools
		{Name: "Perl::Tidy", NoTest: true, Enabled: true, Force: false, Sudo: false},
		{Name: "Perl::Critic", NoTest: true, Enabled: true, Force: false, Sudo: false},
		{Name: "autodie", NoTest: true, Enabled: true, Force: false, Sudo: false},
		{Name: "App::Ack", NoTest: true, Enabled: true, Force: false, Sudo: false},
		{Name: "Term::ReadLine::Gnu", NoTest: true, Enabled: true, Force: false, Sudo: false},
		{Name: "Reply", NoTest: true, Enabled: true, Force: false, Sudo: false},
		{Name: "Data::Printer", NoTest: true, Enabled: true, Force: false, Sudo: false},
		{Name: "Devel::REPL", NoTest: true, Enabled: true, Force: false, Sudo: false},
		{Name: "B::Keywords", NoTest: true, Enabled: true, Force: false, Sudo: false},
		{Name: "Lexical::Persistence", NoTest: true, Enabled: true, Force: false, Sudo: false},
		{Name: "PPI", NoTest: true, Enabled: true, Force: false, Sudo: false},
	}
}

// GenerateCPANInstallScript generates script for CPAN module installation.
func (cm *CPANManager) GenerateCPANInstallScript(ctx context.Context) string {
	modules := cm.GetCPANModules()
	if len(modules) == 0 {
		return ""
	}

	lines := []string{}
	lines = append(lines, cm.generateCPANHeader()...)
	lines = append(lines, cm.generateCPANSetup()...)
	lines = append(lines, cm.generateCPANModuleInstalls(modules)...)
	lines = append(lines, cm.generateCriticalModuleInstalls()...)

	return strings.Join(lines, "\n")
}

// InstallOCFPPerlDependencies installs OCFP Perl dependencies from Makefile.PL.
func (cm *CPANManager) InstallOCFPPerlDependencies(ctx context.Context) string {
	lines := make([]string, 0, scriptBufferCPANBase)

	lines = append(lines, "# Install OCFP Perl dependencies")
	lines = append(lines, "")

	lines = append(lines, cm.generateMakefileLocationScript()...)
	lines = append(lines, cm.generateMakefileDependencyInstallScript()...)

	return strings.Join(lines, "\n")
}

func (cm *CPANManager) generateCPANHeader() []string {
	return []string{
		"# CPAN module installation",
		"",
	}
}

func (cm *CPANManager) generateCPANSetup() []string {
	return []string{
		"# Ensure cpanm is available",
		"if ! command -v cpanm >/dev/null 2>&1; then",
		"    log_info 'Installing cpanminus'",
		"    sudo apt-get install -y cpanminus",
		"    if [ $? -eq 0 ]; then",
		"        log_success 'cpanminus installed successfully'",
		"    else",
		"        log_error 'Failed to install cpanminus'",
		"        return 1",
		"    fi",
		"else",
		"    log_info 'cpanminus already available'",
		"fi",
		"",
	}
}

func (cm *CPANManager) generateCPANModuleInstalls(modules []CPANModule) []string {
	lines := []string{}

	for _, module := range modules {
		if module.Enabled {
			lines = append(lines, cm.generateModuleInstall(module)...)
		}
	}

	return lines
}

func (cm *CPANManager) generateModuleInstall(module CPANModule) []string {
	installCmd := cm.buildCPANCommand(module)

	return []string{
		"# Install CPAN module: " + module.Name,
		fmt.Sprintf("log_info 'Installing CPAN module: %s'", module.Name),
		fmt.Sprintf("perl -e 'use %s; print \"installed\\n\"' >/dev/null 2>&1", module.Name),
		"if [ $? -eq 0 ]; then",
		fmt.Sprintf("    log_info 'CPAN module %s already installed'", module.Name),
		"else",
		"    " + installCmd,
		"    if [ $? -eq 0 ]; then",
		fmt.Sprintf("        log_success 'CPAN module %s installed successfully'", module.Name),
		"    else",
		fmt.Sprintf("        log_warning 'Failed to install CPAN module %s'", module.Name),
		"    fi",
		"fi",
		"",
	}
}

func (cm *CPANManager) buildCPANCommand(module CPANModule) string {
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

	return installCmd
}

func (cm *CPANManager) generateCriticalModuleInstalls() []string {
	criticalModules := []string{"YAML::XS", "JSON::PP", "Try::Tiny", "Service::Vault"}
	lines := []string{"# Install critical CPAN modules system-wide"}

	for _, module := range criticalModules {
		lines = append(lines, fmt.Sprintf("log_info 'Installing %s system-wide'", module))
		lines = append(lines, fmt.Sprintf("sudo cpanm --notest '%s' || log_warning 'Failed to install %s system-wide'", module, module))
	}

	lines = append(lines, "")

	return lines
}

func (cm *CPANManager) generateMakefileLocationScript() []string {
	return []string{
		"# Find and install from Makefile.PL",
		"MAKEFILE_LOCATIONS=(",
		`    "${HOME}/ocfp/ocfp-cli/Makefile.PL"`,
		`    "${HOME}/ocfp/cli/Makefile.PL"`,
		`    "${HOME}/ocfp/cli/perl/Makefile.PL"`,
		")",
		"",
		"MAKEFILE_PL=\"\"",
		"for location in \"${MAKEFILE_LOCATIONS[@]}\"; do",
		"    if [ -f \"$location\" ]; then",
		"        MAKEFILE_PL=\"$location\"",
		"        MAKEFILE_DIR=$(dirname \"$location\")",
		"        log_info \"Found Makefile.PL at: $location\"",
		"        break",
		"    fi",
		"done",
		"",
	}
}

func (cm *CPANManager) generateMakefileDependencyInstallScript() []string {
	return []string{
		"if [ -n \"$MAKEFILE_PL\" ]; then",
		"    log_info \"Installing Perl dependencies from $MAKEFILE_DIR\"",
		"    cd \"$MAKEFILE_DIR\"",
		"    ",
		"    # Install user dependencies",
		"    cpanm --installdeps . --notest",
		"    if [ $? -eq 0 ]; then",
		"        log_success 'User Perl dependencies installed successfully'",
		"    else",
		"        log_warning 'Some user Perl dependencies failed to install'",
		"    fi",
		"    ",
		"    # Install system-wide dependencies",
		"    sudo cpanm --installdeps .",
		"    if [ $? -eq 0 ]; then",
		"        log_success 'System Perl dependencies installed successfully'",
		"    else",
		"        log_warning 'Some system Perl dependencies failed to install'",
		"    fi",
		"else",
		"    log_info 'Makefile.PL not found yet, will be available after OCFP CLI is copied'",
		"fi",
		"",
	}
}

// shouldSkipCondition evaluates whether a condition should be skipped
// (Removed unused condition helper; CPAN modules currently have no provider condition)
