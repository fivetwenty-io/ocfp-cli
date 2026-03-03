package provision

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	// averageScriptLinesPerPackage represents the estimated average number of script lines per snap package.
	averageScriptLinesPerPackage = 10
)

// SnapManager handles snap package installations.
type SnapManager struct {
	config   *config.Config
	provider string
	log      logger.Logger
}

// SnapPackage represents a snap package configuration.
type SnapPackage struct {
	Name         string `yaml:"name"`
	Enabled      bool   `yaml:"enabled"`
	Condition    string `yaml:"condition"`
	CheckCommand string `yaml:"checkCommand"`
	Channel      string `yaml:"channel"`
	Classic      bool   `yaml:"classic"`
	DevMode      bool   `yaml:"devmode"`
	Dangerous    bool   `yaml:"dangerous"`
}

// NewSnapManager creates a new snap package manager.
func NewSnapManager(provider string, cfg *config.Config) *SnapManager {
	return &SnapManager{
		config:   cfg,
		provider: provider,
		log:      logger.Get(),
	}
}

// GetSnapPackages returns snap packages configuration.
func (sm *SnapManager) GetSnapPackages() []SnapPackage {
	pkgs := sm.getDefaultSnapPackages()

	if sm.config != nil {
		sm.applyConfigOverrides(&pkgs)
	}

	return pkgs
}

// GenerateSnapInstallScript generates script for snap package installation.
func (sm *SnapManager) GenerateSnapInstallScript(_ctx context.Context) string {
	packages := sm.GetSnapPackages()
	if len(packages) == 0 {
		return ""
	}

	lines := make([]string, 0, scriptBufferSnapBase+scriptBufferSnapPerPackage*len(packages))

	lines = append(lines, "# Snap package installation")
	lines = append(lines, "")

	lines = append(lines, sm.generateSnapdSetup()...)
	lines = append(lines, sm.generatePackageInstalls(packages)...)
	lines = append(lines, sm.generatePathRefresh()...)

	return strings.Join(lines, "\n")
}

// getDefaultSnapPackages returns the default snap package configurations.
func (sm *SnapManager) getDefaultSnapPackages() []SnapPackage {
	return []SnapPackage{
		{
			Name:         "go",
			Enabled:      true,
			CheckCommand: "go",
			Channel:      "1.24/stable",
			Classic:      true,
			Condition:    "",
			DevMode:      false,
			Dangerous:    false,
		},
		{
			Name:         "node",
			Enabled:      false, // Disabled by default, use nvm instead
			CheckCommand: "node",
			Channel:      "18/stable",
			Classic:      false,
			Condition:    "",
			DevMode:      false,
			Dangerous:    false,
		},
		{
			Name:         "kubectl",
			Enabled:      true,
			CheckCommand: "kubectl",
			Channel:      "stable",
			Classic:      true,
			Condition:    "",
			DevMode:      false,
			Dangerous:    false,
		},
	}
}

// applyConfigOverrides applies configuration overrides to snap packages.
func (sm *SnapManager) applyConfigOverrides(pkgs *[]SnapPackage) {
	sm.applyEnableDisableOverrides(pkgs)
	sm.applySnapSpecificOverrides(pkgs)
}

// applyEnableDisableOverrides applies enable/disable overrides to packages.
func (sm *SnapManager) applyEnableDisableOverrides(pkgs *[]SnapPackage) {
	enable := sm.createEnableDisableMap(sm.config.Bastion.Snaps.Enable)
	disable := sm.createEnableDisableMap(sm.config.Bastion.Snaps.Disable)

	if len(enable) == 0 && len(disable) == 0 {
		return
	}

	for pkgIndex := range *pkgs {
		name := strings.ToLower((*pkgs)[pkgIndex].Name)

		if _, ok := enable[name]; ok {
			(*pkgs)[pkgIndex].Enabled = true
		}

		if _, ok := disable[name]; ok {
			(*pkgs)[pkgIndex].Enabled = false
		}
	}
}

// createEnableDisableMap creates a map for enable/disable snap names.
func (sm *SnapManager) createEnableDisableMap(names []string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, n := range names {
		result[strings.ToLower(n)] = struct{}{}
	}

	return result
}

// applySnapSpecificOverrides applies per-snap overrides by name.
func (sm *SnapManager) applySnapSpecificOverrides(pkgs *[]SnapPackage) {
	if sm.config.Bastion.SnapOverrides == nil {
		return
	}

	for index := range *pkgs {
		name := strings.ToLower((*pkgs)[index].Name)

		override := sm.findSnapOverride(name)
		if override != nil {
			sm.applyOverrideToPackage(&(*pkgs)[index], override)
		}
	}
}

// findSnapOverride finds the override configuration for a snap package name.
func (sm *SnapManager) findSnapOverride(name string) *config.SnapOverride {
	for key, override := range sm.config.Bastion.SnapOverrides {
		if strings.ToLower(key) == name {
			temp := override

			return &temp
		}
	}

	return nil
}

// applyOverrideToPackage applies a specific override to a snap package.
func (sm *SnapManager) applyOverrideToPackage(pkg *SnapPackage, override *config.SnapOverride) {
	if override.Channel != "" {
		pkg.Channel = override.Channel
	}

	if override.Classic != nil {
		pkg.Classic = *override.Classic
	}

	if override.DevMode != nil {
		pkg.DevMode = *override.DevMode
	}

	if override.Dangerous != nil {
		pkg.Dangerous = *override.Dangerous
	}

	if override.CheckCommand != "" {
		pkg.CheckCommand = override.CheckCommand
	}
}

func (sm *SnapManager) generateSnapdSetup() []string {
	return []string{
		"# Ensure snapd is installed and running",
		"if ! command -v snap >/dev/null 2>&1; then",
		"    log_info 'Installing snapd'",
		"    sudo apt-get update -qq",
		"    sudo apt-get install -y snapd",
		"    sudo systemctl enable --now snapd",
		"    log_success 'snapd installed and enabled'",
		"else",
		"    log_info 'snapd already available'",
		"fi",
		"",
		"# Wait for snap to be ready",
		"log_info 'Waiting for snap daemon to be ready'",
		"sudo snap wait system seed.loaded",
		"",
	}
}

func (sm *SnapManager) generatePackageInstalls(packages []SnapPackage) []string {
	lines := make([]string, 0, len(packages)*averageScriptLinesPerPackage)

	for _, pkg := range packages {
		if !pkg.Enabled || sm.shouldSkipCondition(pkg.Condition) {
			continue
		}

		lines = append(lines, "# Install snap package: "+pkg.Name)

		if pkg.CheckCommand != "" {
			lines = append(lines, fmt.Sprintf("if command -v %s >/dev/null 2>&1; then", pkg.CheckCommand))
			lines = append(lines, fmt.Sprintf("    log_info '%s already installed via snap or system package'", pkg.Name))
			lines = append(lines, "else")
		} else {
			lines = append(lines, fmt.Sprintf("if snap list %s >/dev/null 2>&1; then", pkg.Name))
			lines = append(lines, fmt.Sprintf("    log_info 'Snap package %s already installed'", pkg.Name))
			lines = append(lines, "else")
		}

		installCmd := sm.buildSnapInstallCommand(pkg)
		lines = append(lines, fmt.Sprintf("    log_info 'Installing snap package: %s'", pkg.Name))
		lines = append(lines, "    "+installCmd)
		lines = append(lines, "    if [ $? -eq 0 ]; then")
		lines = append(lines, fmt.Sprintf("        log_success 'Snap package %s installed successfully'", pkg.Name))
		lines = append(lines, "    else")
		lines = append(lines, fmt.Sprintf("        log_error 'Failed to install snap package %s'", pkg.Name))
		lines = append(lines, "    fi")
		lines = append(lines, "fi")
		lines = append(lines, "")
	}

	return lines
}

func (sm *SnapManager) buildSnapInstallCommand(pkg SnapPackage) string {
	installCmd := "sudo snap install " + pkg.Name

	if pkg.Channel != "" {
		installCmd += " --channel=" + pkg.Channel
	}

	if pkg.Classic {
		installCmd += " --classic"
	}

	if pkg.DevMode {
		installCmd += " --devmode"
	}

	if pkg.Dangerous {
		installCmd += " --dangerous"
	}

	return installCmd
}

func (sm *SnapManager) generatePathRefresh() []string {
	return []string{
		"# Refresh PATH for snap binaries",
		"export PATH=\"/snap/bin:$PATH\"",
		"",
	}
}

// shouldSkipCondition evaluates whether a condition should be skipped.
func (sm *SnapManager) shouldSkipCondition(condition string) bool {
	if condition == "" {
		return false
	}

	switch condition {
	case condProviderIsStackit:
		return sm.provider != providerStackit
	case condProviderIsAWS:
		return sm.provider != providerAWS
	case condProviderIsAzure:
		return sm.provider != providerAzure
	case condProviderIsGCP:
		return sm.provider != providerGCP
	case condProviderIsOpenstack:
		return sm.provider != providerOpenStack
	case condProviderIsVMware:
		return sm.provider != providerVMware && sm.provider != providerVsphere
	default:
		return false
	}
}
