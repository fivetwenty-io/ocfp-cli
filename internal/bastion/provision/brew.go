package provision

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// BrewManager handles Linuxbrew package installations.
type BrewManager struct {
	config   *config.Config
	provider string
	log      logger.Logger
}

// BrewPackage represents a brew package configuration.
type BrewPackage struct {
	Name         string `yaml:"name"`
	Enabled      bool   `yaml:"enabled"`
	Condition    string `yaml:"condition"`
	CheckCommand string `yaml:"checkCommand"`
	Tap          string `yaml:"tap"`
	Cask         bool   `yaml:"cask"`
	Version      string `yaml:"version"`
	Options      string `yaml:"options"`
}

// NewBrewManager creates a new brew package manager.
func NewBrewManager(provider string, cfg *config.Config) *BrewManager {
	return &BrewManager{
		config:   cfg,
		provider: provider,
		log:      logger.Get(),
	}
}

// GetBrewPackages returns brew packages configuration.
func (bm *BrewManager) GetBrewPackages() []BrewPackage {
	pkgs := bm.getDefaultBrewPackages()
	pkgs = append(pkgs, bm.getProviderBrewPackages()...)

	if bm.config != nil {
		bm.applyConfigOverrides(&pkgs)
	}

	return pkgs
}

// GenerateBrewInstallScript generates the Linuxbrew bootstrap script.
func (bm *BrewManager) GenerateBrewInstallScript(_ctx context.Context) string {
	lines := make([]string, 0, scriptBufferBrewInstall)

	lines = append(lines, "# Linuxbrew installation")
	lines = append(lines, "")
	lines = append(lines, "if ! command -v brew >/dev/null 2>&1; then")
	lines = append(lines, "    log_info 'Installing Linuxbrew'")
	lines = append(lines, `    NONINTERACTIVE=1 /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"`)
	lines = append(lines, "    if [ $? -eq 0 ]; then")
	lines = append(lines, `        eval "$(/home/linuxbrew/.linuxbrew/bin/brew shellenv)"`)
	lines = append(lines, "        log_success 'Linuxbrew installed successfully'")
	lines = append(lines, "    else")
	lines = append(lines, "        log_error 'Failed to install Linuxbrew'")
	lines = append(lines, "        exit 1")
	lines = append(lines, "    fi")
	lines = append(lines, "else")
	lines = append(lines, `    eval "$(brew shellenv)"`)
	lines = append(lines, "    log_info 'Linuxbrew already installed'")
	lines = append(lines, "fi")
	lines = append(lines, "")
	lines = append(lines, "# Disable analytics")
	lines = append(lines, "brew analytics off")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GenerateBrewPackageScript generates script for brew package installation.
func (bm *BrewManager) GenerateBrewPackageScript(_ctx context.Context) string {
	packages := bm.GetBrewPackages()
	if len(packages) == 0 {
		return ""
	}

	lines := make([]string, 0, scriptBufferBrewBase+scriptBufferBrewPerPackage*len(packages))

	lines = append(lines, "# Brew package installation")
	lines = append(lines, "")

	// Ensure brew is on PATH for this phase
	lines = append(lines, "# Ensure brew is on PATH")
	lines = append(lines, `eval "$(/home/linuxbrew/.linuxbrew/bin/brew shellenv)"`)
	lines = append(lines, "")

	// Disable auto-update during bulk install
	lines = append(lines, "# Disable auto-update for bulk install performance")
	lines = append(lines, "export HOMEBREW_NO_AUTO_UPDATE=1")
	lines = append(lines, "")

	// Collect and install taps first
	lines = append(lines, bm.generateTapInstalls(packages)...)

	// Install packages
	lines = append(lines, bm.generatePackageInstalls(packages)...)

	// Re-enable auto-update
	lines = append(lines, "# Re-enable auto-update")
	lines = append(lines, "unset HOMEBREW_NO_AUTO_UPDATE")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// getDefaultBrewPackages returns the default brew package configurations.
func (bm *BrewManager) getDefaultBrewPackages() []BrewPackage {
	return []BrewPackage{
		// From essential APT
		{Name: "rsync", Enabled: true, CheckCommand: "rsync"},
		{Name: "wget", Enabled: true, CheckCommand: "wget"},
		{Name: "unzip", Enabled: true, CheckCommand: "unzip"},
		{Name: "tig", Enabled: true, CheckCommand: "tig"},
		{Name: "ack", Enabled: true, CheckCommand: "ack"},
		{Name: "ripgrep", Enabled: true, CheckCommand: "rg"},
		{Name: "htop", Enabled: true, CheckCommand: "htop"},
		{Name: "s3cmd", Enabled: true, CheckCommand: "s3cmd"},
		{Name: "vim", Enabled: true, CheckCommand: "vim"},
		{Name: "neovim", Enabled: true, CheckCommand: "nvim"},
		{Name: "coreutils", Enabled: true, CheckCommand: "gdate"},

		// From CloudFoundry APT
		{Name: "ruby", Enabled: true, CheckCommand: "ruby"},
		{Name: "openssl", Enabled: true, CheckCommand: "openssl"},
		{Name: "sqlite3", Enabled: true, CheckCommand: "sqlite3"},
		{Name: "jq", Enabled: true, CheckCommand: "jq"},
		{Name: "tmux", Enabled: true, CheckCommand: "tmux"},
		{Name: "screen", Enabled: true, CheckCommand: "screen"},
		{Name: "tree", Enabled: true, CheckCommand: "tree"},

		// From snap
		{Name: "go", Enabled: true, CheckCommand: "go", Version: "1.27"},
		{Name: "kubectl", Enabled: true, CheckCommand: "kubectl"},
		{Name: "node", Enabled: false, CheckCommand: "node"}, // Disabled by default, use nvm

		// From advanced tools — vault is always required: the inception vault
		// (`safe local` in tmux, `vault status` health checks) runs on the vault
		// binary even when the bloc's secrets backend is openbao.
		{Name: "vault", Enabled: true, CheckCommand: "vault", Tap: "hashicorp/tap"},
		{Name: "yq", Enabled: true, CheckCommand: "yq"},
		{Name: "hl", Enabled: true, CheckCommand: "hl"},

		// CF ecosystem tools — brew tap ships macOS-only binaries; installed via binary_tools instead
		{Name: "bosh-cli", Enabled: false, CheckCommand: "bosh", Tap: "cloudfoundry/tap"},
		{Name: "cf-cli@8", Enabled: false, CheckCommand: "cf", Tap: "cloudfoundry/tap"},
		{Name: "credhub-cli", Enabled: false, CheckCommand: "credhub", Tap: "cloudfoundry/tap"},
		{Name: "uaa-cli", Enabled: false, CheckCommand: "uaa", Tap: "cloudfoundry/tap"},
		{Name: "spruce", Enabled: false, CheckCommand: "spruce", Tap: "cloudfoundry-community/cf"},

		// OpenBao
		{Name: "openbao", Enabled: true, CheckCommand: "bao"},

		// Migrated from APT essential (system build tools)
		{Name: "gcc", Enabled: true, CheckCommand: "gcc"},
		{Name: "make", Enabled: true, CheckCommand: "make"},
		{Name: "cpanminus", Enabled: true, CheckCommand: "cpanm"},
		{Name: "libtool", Enabled: true, CheckCommand: "libtool"},
		{Name: "gnupg", Enabled: true, CheckCommand: "gpg"},
		{Name: "python@3", Enabled: true, CheckCommand: "python3"},

		// Migrated from APT -dev packages (C libraries for native extensions)
		{Name: "readline", Enabled: true, CheckCommand: ""},
		{Name: "libyaml", Enabled: true, CheckCommand: ""},
		{Name: "zlib", Enabled: true, CheckCommand: ""},
		{Name: "libxml2", Enabled: true, CheckCommand: ""},
		{Name: "libxslt", Enabled: true, CheckCommand: ""},
	}
}

// getProviderBrewPackages returns provider-specific brew packages.
func (bm *BrewManager) getProviderBrewPackages() []BrewPackage {
	switch bm.provider {
	case providerAzure:
		return []BrewPackage{
			{Name: "azure-cli", Enabled: true, CheckCommand: "az", Condition: condProviderIsAzure},
		}
	case providerGCP:
		return []BrewPackage{
			{Name: "google-cloud-sdk", Enabled: true, CheckCommand: "gcloud", Condition: condProviderIsGCP, Cask: true},
		}
	case providerPVE:
		// pmx drives the Proxmox API from the bastion.
		return []BrewPackage{
			{Name: "pmx", Enabled: true, CheckCommand: "pmx", Condition: condProviderIsPVE, Cask: true, Tap: "fivetwenty-io/tap"},
		}
	default:
		return nil
	}
}

// applyConfigOverrides applies configuration overrides to brew packages.
func (bm *BrewManager) applyConfigOverrides(pkgs *[]BrewPackage) {
	bm.applyEnableDisableOverrides(pkgs)
	bm.applyBrewSpecificOverrides(pkgs)
}

// applyEnableDisableOverrides applies enable/disable overrides to packages.
func (bm *BrewManager) applyEnableDisableOverrides(pkgs *[]BrewPackage) {
	enable := bm.createEnableDisableMap(bm.config.Bastion.Brews.Enable)
	disable := bm.createEnableDisableMap(bm.config.Bastion.Brews.Disable)

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

// createEnableDisableMap creates a map for enable/disable brew names.
func (bm *BrewManager) createEnableDisableMap(names []string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, n := range names {
		result[strings.ToLower(n)] = struct{}{}
	}

	return result
}

// applyBrewSpecificOverrides applies per-brew overrides by name.
func (bm *BrewManager) applyBrewSpecificOverrides(pkgs *[]BrewPackage) {
	if bm.config.Bastion.BrewOverrides == nil {
		return
	}

	for index := range *pkgs {
		name := strings.ToLower((*pkgs)[index].Name)

		override := bm.findBrewOverride(name)
		if override != nil {
			bm.applyOverrideToPackage(&(*pkgs)[index], override)
		}
	}
}

// findBrewOverride finds the override configuration for a brew package name.
func (bm *BrewManager) findBrewOverride(name string) *config.BrewOverride {
	for key, override := range bm.config.Bastion.BrewOverrides {
		if strings.ToLower(key) == name {
			temp := override

			return &temp
		}
	}

	return nil
}

// applyOverrideToPackage applies a specific override to a brew package.
func (bm *BrewManager) applyOverrideToPackage(pkg *BrewPackage, override *config.BrewOverride) {
	if override.Tap != "" {
		pkg.Tap = override.Tap
	}

	if override.Cask != nil {
		pkg.Cask = *override.Cask
	}

	if override.Version != "" {
		pkg.Version = override.Version
	}

	if override.Options != "" {
		pkg.Options = override.Options
	}

	if override.CheckCommand != "" {
		pkg.CheckCommand = override.CheckCommand
	}
}

// generateTapInstalls generates brew tap commands for packages that need them.
func (bm *BrewManager) generateTapInstalls(packages []BrewPackage) []string {
	seen := make(map[string]bool)

	var lines []string

	for _, pkg := range packages {
		if !pkg.Enabled || pkg.Tap == "" || bm.shouldSkipCondition(pkg.Condition) {
			continue
		}

		if seen[pkg.Tap] {
			continue
		}

		seen[pkg.Tap] = true

		lines = append(lines, "# Add tap: "+pkg.Tap)
		lines = append(lines, fmt.Sprintf("if ! brew tap | grep -q '%s'; then", pkg.Tap))
		lines = append(lines, fmt.Sprintf("    log_info 'Adding brew tap: %s'", pkg.Tap))
		lines = append(lines, "    brew tap "+pkg.Tap)
		lines = append(lines, "else")
		lines = append(lines, fmt.Sprintf("    log_info 'Tap %s already added'", pkg.Tap))
		lines = append(lines, "fi")
		lines = append(lines, bm.generateTapTrust(pkg.Tap)...)
		lines = append(lines, "")
	}

	return lines
}

// generatePackageInstalls generates batched brew install commands.
// Packages are grouped into: regular formulae, casks, and those with custom
// options (which must be installed individually). Brew handles idempotency
// natively, so already-installed packages are simply skipped.
//
//nolint:funlen // batched install generation for formulae, casks, and custom options
func (bm *BrewManager) generatePackageInstalls(packages []BrewPackage) []string {
	var formulae []string

	var casks []string

	var customOptions []BrewPackage

	for _, pkg := range packages {
		if !pkg.Enabled || bm.shouldSkipCondition(pkg.Condition) {
			continue
		}

		if pkg.Options != "" {
			customOptions = append(customOptions, pkg)

			continue
		}

		if pkg.Cask {
			casks = append(casks, bm.brewPackageName(pkg))

			continue
		}

		formulae = append(formulae, bm.brewPackageName(pkg))
	}

	var lines []string

	// Batch install all regular formulae
	if len(formulae) > 0 {
		lines = append(lines, fmt.Sprintf("# Install %d brew formulae", len(formulae)))
		lines = append(lines, fmt.Sprintf("log_info 'Installing %d brew formulae'", len(formulae)))
		lines = append(lines, "brew install \\")

		for i, name := range formulae {
			if i < len(formulae)-1 {
				lines = append(lines, "    "+name+" \\")
			} else {
				lines = append(lines, "    "+name)
			}
		}

		lines = append(lines, fmt.Sprintf("log_success 'Brew formulae installed (%d packages)'", len(formulae)))
		lines = append(lines, "")
	}

	// Batch install all cask packages
	if len(casks) > 0 {
		lines = append(lines, fmt.Sprintf("# Install %d brew casks", len(casks)))
		lines = append(lines, fmt.Sprintf("log_info 'Installing %d brew casks'", len(casks)))
		lines = append(lines, "brew install --cask \\")

		for i, name := range casks {
			if i < len(casks)-1 {
				lines = append(lines, "    "+name+" \\")
			} else {
				lines = append(lines, "    "+name)
			}
		}

		lines = append(lines, fmt.Sprintf("log_success 'Brew casks installed (%d packages)'", len(casks)))
		lines = append(lines, "")
	}

	// Install packages with custom options individually
	for _, pkg := range customOptions {
		installCmd := bm.buildBrewInstallCommand(pkg)
		lines = append(lines, "# Install brew package with options: "+pkg.Name)
		lines = append(lines, fmt.Sprintf("log_info 'Installing brew package: %s'", pkg.Name))
		lines = append(lines, installCmd)
		lines = append(lines, fmt.Sprintf("log_success 'Brew package %s installed'", pkg.Name))
		lines = append(lines, "")
	}

	return lines
}

// brewPackageName returns the formula name for a brew install command,
// handling tap-qualified and version-pinned names.
func (bm *BrewManager) brewPackageName(pkg BrewPackage) string {
	if pkg.Tap != "" {
		return pkg.Tap + "/" + pkg.Name
	}

	if pkg.Version != "" {
		return pkg.Name + "@" + pkg.Version
	}

	return pkg.Name
}

// buildBrewInstallCommand builds the brew install command for a package.
func (bm *BrewManager) buildBrewInstallCommand(pkg BrewPackage) string {
	installCmd := "brew install"

	if pkg.Cask {
		installCmd += " --cask"
	}

	// Use tap-qualified name if tap is specified
	switch {
	case pkg.Tap != "":
		installCmd += " " + pkg.Tap + "/" + pkg.Name
	case pkg.Version != "":
		// Version-pinned formula (e.g., go@1.24)
		installCmd += " " + pkg.Name + "@" + pkg.Version
	default:
		installCmd += " " + pkg.Name
	}

	if pkg.Options != "" {
		installCmd += " " + pkg.Options
	}

	return installCmd
}

// generateTapTrust trusts a third-party tap. Homebrew 6 refuses to evaluate
// code from an untrusted tap, which breaks every tap-qualified install in a
// non-interactive run. Older Homebrew has no `brew trust`, so the command is
// only issued when it exists.
func (bm *BrewManager) generateTapTrust(tap string) []string {
	return []string{
		fmt.Sprintf("if brew trust --help >/dev/null 2>&1 && ! brew trust --json=v1 2>/dev/null | grep -q '\"%s\"'; then", tap),
		fmt.Sprintf("    log_info 'Trusting brew tap: %s'", tap),
		"    brew trust --tap " + tap,
		"fi",
	}
}

// shouldSkipCondition evaluates whether a condition should be skipped.
func (bm *BrewManager) shouldSkipCondition(condition string) bool {
	if condition == "" {
		return false
	}

	switch condition {
	case condProviderIsStackit:
		return bm.provider != providerStackit
	case condProviderIsAWS:
		return bm.provider != providerAWS
	case condProviderIsAzure:
		return bm.provider != providerAzure
	case condProviderIsGCP:
		return bm.provider != providerGCP
	case condProviderIsOpenstack:
		return bm.provider != providerOpenStack
	case condProviderIsVMware:
		return bm.provider != providerVMware && bm.provider != providerVsphere
	case condProviderIsPVE:
		return bm.provider != providerPVE
	default:
		return false
	}
}
