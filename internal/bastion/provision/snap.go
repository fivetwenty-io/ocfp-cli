package provision

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// SnapManager handles snap package installations
type SnapManager struct {
	config   *config.Config
	provider string
	log      logger.Logger
}

// SnapPackage represents a snap package configuration
type SnapPackage struct {
	Name         string `yaml:"name"`
	Enabled      bool   `yaml:"enabled"`
	Condition    string `yaml:"condition"`
	CheckCommand string `yaml:"check_command"`
	Channel      string `yaml:"channel"`
	Classic      bool   `yaml:"classic"`
	DevMode      bool   `yaml:"devmode"`
	Dangerous    bool   `yaml:"dangerous"`
}

// NewSnapManager creates a new snap package manager
func NewSnapManager(provider string, cfg *config.Config) *SnapManager {
	return &SnapManager{
		config:   cfg,
		provider: provider,
		log:      logger.Get(),
	}
}

// GetSnapPackages returns snap packages configuration
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
			for i := range pkgs {
				name := strings.ToLower(pkgs[i].Name)
				if _, ok := enable[name]; ok {
					pkgs[i].Enabled = true
				}
				if _, ok := disable[name]; ok {
					pkgs[i].Enabled = false
				}
			}
		}
		// Per-snap overrides by name
		if sm.config.Bastion.SnapOverrides != nil {
			for i := range pkgs {
				name := strings.ToLower(pkgs[i].Name)
				for k, ov := range sm.config.Bastion.SnapOverrides {
					if strings.ToLower(k) != name {
						continue
					}
					if ov.Channel != "" {
						pkgs[i].Channel = ov.Channel
					}
					if ov.Classic != nil {
						pkgs[i].Classic = *ov.Classic
					}
					if ov.DevMode != nil {
						pkgs[i].DevMode = *ov.DevMode
					}
					if ov.Dangerous != nil {
						pkgs[i].Dangerous = *ov.Dangerous
					}
					if ov.CheckCommand != "" {
						pkgs[i].CheckCommand = ov.CheckCommand
					}
				}
			}
		}
	}
	return pkgs
}

// GenerateSnapInstallScript generates script for snap package installation
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

		lines = append(lines, fmt.Sprintf("# Install snap package: %s", pkg.Name))

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
		installCmd := fmt.Sprintf("sudo snap install %s", pkg.Name)

		if pkg.Channel != "" {
			installCmd += fmt.Sprintf(" --channel=%s", pkg.Channel)
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
		lines = append(lines, fmt.Sprintf("    %s", installCmd))
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

// shouldSkipCondition evaluates whether a condition should be skipped
func (sm *SnapManager) shouldSkipCondition(condition string) bool {
	if condition == "" {
		return false
	}

	switch condition {
	case "provider_is_stackit":
		return sm.provider != "stackit"
	case "provider_is_aws":
		return sm.provider != "aws"
	case "provider_is_azure":
		return sm.provider != "azure"
	case "provider_is_gcp":
		return sm.provider != "gcp"
	case "provider_is_openstack":
		return sm.provider != "openstack"
	case "provider_is_vmware":
		return sm.provider != "vmware" && sm.provider != "vsphere"
	default:
		return false
	}
}
