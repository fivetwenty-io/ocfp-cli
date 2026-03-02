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
		{Name: "Pry", NoTest: true, Enabled: true, Force: false, Sudo: true},
		{Name: "Carp::Always", NoTest: true, Enabled: true, Force: false, Sudo: true},
		{Name: "Smart::Comments", NoTest: true, Enabled: true, Force: false, Sudo: true},
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
	checkCmd := fmt.Sprintf("perl -e 'use %s' 2>/dev/null", module.Name)

	return []string{
		"# Install CPAN module: " + module.Name,
		fmt.Sprintf("if ! %s; then", checkCmd),
		fmt.Sprintf("    log_info 'Installing CPAN module: %s'", module.Name),
		"    # Allow CPAN module compilation failures for optional debugging tools",
		"    # Build dependencies (build-essential, libperl-dev, etc.) are installed in packages phase",
		"    set +e  # Temporarily disable exit-on-error for this module",
		"    " + installCmd,
		"    CPAN_EXIT_CODE=$?",
		"    set -e  # Re-enable exit-on-error",
		"    if [ $CPAN_EXIT_CODE -eq 0 ]; then",
		fmt.Sprintf("        log_success 'CPAN module %s installed successfully'", module.Name),
		"    else",
		fmt.Sprintf("        log_warning 'Failed to install CPAN module %s (non-critical, continuing)'", module.Name),
		"    fi",
		"else",
		fmt.Sprintf("    log_info 'CPAN module %s already installed'", module.Name),
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
	// Critical modules are now installed via GetCPANModules with Sudo: true
	// This function kept for backward compatibility but returns empty
	return []string{}
}
