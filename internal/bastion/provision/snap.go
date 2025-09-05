package provision

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
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
	pkgs := []SnapPackage{
		{
			Name:         "go",
			Enabled:      true,
			CheckCommand: "go",
			Channel:      "1.24/stable",
			Classic:      true,
		},
		{
			Name:         "node",
			Enabled:      false, // Disabled by default, use nvm instead
			CheckCommand: "node",
			Channel:      "18/stable",
			Classic:      false,
		},
		{
			Name:         "kubectl",
			Enabled:      true,
			CheckCommand: "kubectl",
			Channel:      "stable",
			Classic:      true,
		},
		{
			Name:         "helm",
			Enabled:      true,
			CheckCommand: "helm",
			Channel:      "stable",
			Classic:      false,
		},
	}
	// Apply config overrides
	if sm.config != nil {
		enable := make(map[string]struct{})
		for _, n := range sm.config.Bastion.Snaps.Enable {
			enable[strings.ToLower(n)] = struct{}{}
		}

		disable := make(map[string]struct{})
		for _, n := range sm.config.Bastion.Snaps.Disable {
			disable[strings.ToLower(n)] = struct{}{}
		}

		if len(enable) > 0 || len(disable) > 0 {
			for pkgIndex := range pkgs {
				name := strings.ToLower(pkgs[pkgIndex].Name)
				if _, ok := enable[name]; ok {
					pkgs[pkgIndex].Enabled = true
				}

				if _, ok := disable[name]; ok {
					pkgs[pkgIndex].Enabled = false
				}
			}
		}
		// Per-snap overrides by name
		if sm.config.Bastion.SnapOverrides != nil {
			for index := range pkgs {
				name := strings.ToLower(pkgs[index].Name)
				for key, override := range sm.config.Bastion.SnapOverrides {
					if strings.ToLower(key) != name {
						continue
					}

					if override.Channel != "" {
						pkgs[index].Channel = override.Channel
					}

					if override.Classic != nil {
						pkgs[index].Classic = *override.Classic
					}

					if override.DevMode != nil {
						pkgs[index].DevMode = *override.DevMode
					}

					if override.Dangerous != nil {
						pkgs[index].Dangerous = *override.Dangerous
					}

					if override.CheckCommand != "" {
						pkgs[index].CheckCommand = override.CheckCommand
					}
				}
			}
		}
	}

	return pkgs
}

// GenerateSnapInstallScript generates script for snap package installation.
func (sm *SnapManager) GenerateSnapInstallScript(ctx context.Context) string {
	packages := sm.GetSnapPackages()
	if len(packages) == 0 {
		return ""
	}

	var lines []string

	lines = append(lines, "# Snap package installation")
	lines = append(lines, "")

	// Ensure snapd is installed and running
	lines = append(lines, "# Ensure snapd is installed and running")
	lines = append(lines, "if ! command -v snap >/dev/null 2>&1; then")
	lines = append(lines, "    log_info 'Installing snapd'")
	lines = append(lines, "    sudo apt-get update -qq")
	lines = append(lines, "    sudo apt-get install -y snapd")
	lines = append(lines, "    sudo systemctl enable --now snapd")
	lines = append(lines, "    log_success 'snapd installed and enabled'")
	lines = append(lines, "else")
	lines = append(lines, "    log_info 'snapd already available'")
	lines = append(lines, "fi")
	lines = append(lines, "")

	// Wait for snap to be ready
	lines = append(lines, "# Wait for snap to be ready")
	lines = append(lines, "log_info 'Waiting for snap daemon to be ready'")
	lines = append(lines, "sudo snap wait system seed.loaded")
	lines = append(lines, "")

	for _, pkg := range packages {
		if !pkg.Enabled || sm.shouldSkipCondition(pkg.Condition) {
			continue
		}

		lines = append(lines, "# Install snap package: "+pkg.Name)

		// Check if already installed
		if pkg.CheckCommand != "" {
			lines = append(lines, fmt.Sprintf("if command -v %s >/dev/null 2>&1; then", pkg.CheckCommand))
			lines = append(lines, fmt.Sprintf("    log_info '%s already installed via snap or system package'", pkg.Name))
			lines = append(lines, "else")
		} else {
			lines = append(lines, fmt.Sprintf("if snap list %s >/dev/null 2>&1; then", pkg.Name))
			lines = append(lines, fmt.Sprintf("    log_info 'Snap package %s already installed'", pkg.Name))
			lines = append(lines, "else")
		}

		// Build snap install command
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

	// Refresh PATH for snap binaries
	lines = append(lines, "# Refresh PATH for snap binaries")
	lines = append(lines, "export PATH=\"/snap/bin:$PATH\"")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
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
