package provision

import (
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/deployments"
	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// Config implements the ProvisionConfig interface.
type Config struct {
	provider string
	config   *config.Config
	modes    *deployments.Resolver
}

// NewConfig creates a new provisioning configuration.
func NewConfig(provider string, cfg *config.Config, modes *deployments.Resolver) *Config {
	if modes == nil {
		modes = deployments.NewResolver(cfg)
	}

	return &Config{
		provider: provider,
		config:   cfg,
		modes:    modes,
	}
}

// DeploymentResolver returns the deployment mode resolver.
func (c *Config) DeploymentResolver() *deployments.Resolver {
	return c.modes
}

// GetSystemConfig returns system configuration.
func (c *Config) GetSystemConfig() SystemConfig {
	return SystemConfig{
		Hostname: HostnameConfig{
			Enabled: true,
			Pattern: "${OCFP_BLOC}-bastion",
		},
		WaitTime:    systemWaitTimeSeconds,
		Timezone:    "UTC",
		Locale:      "en_US.UTF-8",
		UpdateCache: true,
	}
}

// GetDirectories returns directories to create.
func (c *Config) GetDirectories() []DirectoryConfig {
	return []DirectoryConfig{
		{Path: "${HOME}/ocfp/cli", Mode: directoryModeStandard, Owner: "", Group: "", Condition: ""},
		{Path: "${HOME}/.ocfp", Mode: directoryModeStandard, Owner: "", Group: "", Condition: ""},
		{Path: "${HOME}/bin", Mode: directoryModeStandard, Owner: "", Group: "", Condition: ""},
		{Path: "${HOME}/.ocfp/logs/provision", Mode: directoryModeStandard, Owner: "", Group: "", Condition: ""},
		{Path: "${HOME}/.ssh", Mode: directoryModeSSH, Owner: "", Group: "", Condition: ""},
	}
}

// GetPackages returns package groups to install.
func (c *Config) GetPackages() map[string]PackageGroup {
	packages := c.getCorePackages()
	c.addProviderPackages(packages)

	return packages
}

// GetGenesisDeployments returns Genesis deployments to initialize.
func (c *Config) GetGenesisDeployments() []GenesisDeployment {
	items := []GenesisDeployment{
		{Name: "bosh", Kit: "bosh", Repo: "bosh-genesis-kit", Branch: "develop", Enabled: true, Condition: ""},
		{Name: "vault", Kit: "vault", Repo: "vault-genesis-kit", Branch: "", Enabled: true, Condition: ""},
		{Name: "concourse", Kit: "concourse", Repo: "concourse-genesis-kit", Branch: "", Enabled: true, Condition: ""},
		{Name: "cf", Kit: "cf", Repo: "cf-genesis-kit", Branch: "", Enabled: true, Condition: ""},
		{Name: "blacksmith", Kit: "blacksmith", Repo: "blacksmith-genesis-kit", Branch: "", Enabled: true, Condition: ""},
		{Name: "shield", Kit: "shield", Repo: "shield-genesis-kit", Branch: "", Enabled: true, Condition: ""},
		{Name: "prometheus", Kit: "prometheus", Repo: "prometheus-genesis-kit", Branch: "", Enabled: true, Condition: ""},
		{Name: "doomsday", Kit: "doomsday", Repo: "doomsday-genesis-kit", Branch: "", Enabled: true, Condition: ""},
		{Name: "scheduler", Kit: "ocf-scheduler", Repo: "ocf-scheduler-genesis-kit", Branch: "", Enabled: true, Condition: ""},
		{Name: "autoscaler", Kit: "cf-app-autoscaler", Repo: "cf-app-autoscaler-genesis-kit", Branch: "", Enabled: true, Condition: ""},
		{Name: "jumpbox", Kit: "jumpbox", Repo: "jumpbox-genesis-kit", Branch: "", Enabled: true, Condition: ""},
	}

	modes := c.modes
	if modes == nil {
		modes = deployments.NewResolver(c.config)
	}

	for i := range items {
		if !modes.IsDev(items[i].Name) {
			items[i].Enabled = false
		}
	}

	return items
}

// GetBinaryTools returns binary tools to install.
func (c *Config) GetBinaryTools() []BinaryTool {
	genesisTools := c.getGenesisTools()
	cfTools := c.getCloudFoundryTools()

	tools := make([]BinaryTool, 0, len(genesisTools)+len(cfTools))
	tools = append(tools, genesisTools...)
	tools = append(tools, cfTools...)

	return tools
}

// GetAPTRepositories returns APT repositories to add.
func (c *Config) GetAPTRepositories() []APTRepository {
	repos := []APTRepository{
		{
			Name:      "cloudfoundry",
			Enabled:   true,
			Condition: "",
			GPGKey: GPGKey{
				URL:     "https://packages.cloudfoundry.org/debian/cli.cloudfoundry.org.key",
				Dest:    "/usr/share/keyrings/cli.cloudfoundry.org.gpg",
				Dearmor: true,
			},
			SourceLine: "deb [signed-by=/usr/share/keyrings/cli.cloudfoundry.org.gpg] https://packages.cloudfoundry.org/debian stable main",
			SourceFile: "/etc/apt/sources.list.d/cloudfoundry-cli.list",
		},
	}

	// Add provider-specific repositories
	// Azure and GCP repos removed: azure-cli and google-cloud-sdk moved to brew.
	if c.provider == providerStackit {
		repos = append(repos, APTRepository{
			Name:      "stackit",
			Enabled:   true,
			Condition: condProviderIsStackit,
			GPGKey: GPGKey{
				URL:     "https://packages.stackit.cloud/keys/key.gpg",
				Dest:    "/usr/share/keyrings/stackit.gpg",
				Dearmor: true,
			},
			SourceLine: "deb [signed-by=/usr/share/keyrings/stackit.gpg] https://packages.stackit.cloud/apt/cli stackit main",
			SourceFile: "/etc/apt/sources.list.d/stackit.list",
		})
	}

	return repos
}

// GetGitRepositories returns Git repositories to clone.
func (c *Config) GetGitRepositories() []GitRepository {
	repos := []GitRepository{}

	// Add Genesis repository if using source-based installation
	if c.shouldInstallGenesisFromSource() {
		genesisRepo := c.getGenesisRepository()
		if genesisRepo.Enabled {
			repos = append(repos, genesisRepo)
		}
	}

	return repos
}

// GetCustomScripts returns custom scripts to execute.
func (c *Config) GetCustomScripts() []CustomScript {
	scripts := []CustomScript{
		{
			Name:      "setup-environment",
			Enabled:   true,
			Condition: "",
			Content:   c.generateEnvironmentScript(),
			Path:      "",
			Mode:      0,
			Execute:   true,
		},
		{
			Name:      "configure-git",
			Enabled:   c.hasGitConfig(),
			Condition: "",
			Content:   c.generateGitConfigScript(),
			Path:      "",
			Mode:      0,
			Execute:   true,
		},
	}

	// Add provider-specific scripts
	providerScript := c.generateProviderScript()
	if providerScript != "" {
		scripts = append(scripts, CustomScript{
			Name:      "configure-" + c.provider,
			Enabled:   true,
			Condition: "",
			Content:   providerScript,
			Path:      "",
			Mode:      0,
			Execute:   true,
		})
	}

	return scripts
}

// GetPostBrewPackages returns packages to install via APT after Linuxbrew.
func (c *Config) GetPostBrewPackages() PackageGroup {
	return c.getPostBrewPackages()
}

// shouldInstallGenesisFromSource determines if Genesis should be installed from source.
// Returns true if:
// 1. Genesis is enabled in config
// 2. No binary URL override is specified
// 3. Branch or commit configuration indicates source-based installation.
func (c *Config) shouldInstallGenesisFromSource() bool {
	genesisConfig := c.getGenesisConfig()

	if !genesisConfig.Enabled {
		return false
	}

	// Check for binary download override
	if override, exists := c.config.Bastion.ToolOverrides["genesis"]; exists {
		if override.URL != "" {
			return false // Binary download mode
		}
	}

	return true // Default to source-based
}

// getGenesisConfig returns the effective Genesis configuration.
// Bastion-specific config takes precedence over global config.
func (c *Config) getGenesisConfig() config.Genesis {
	genesisConfig := c.config.Genesis

	// Bastion-specific overrides take precedence
	if c.config.Bastion.Genesis.Enabled {
		genesisConfig = c.config.Bastion.Genesis
	}

	// Apply default values for Branch and Repo if not set (but don't override Enabled)
	if genesisConfig.Branch == "" {
		genesisConfig.Branch = "v3.1.x-dev"
	}

	if genesisConfig.Repo == "" {
		genesisConfig.Repo = "git@github.com:genesis-community/genesis"
	}

	return genesisConfig
}

// getGenesisRepository returns Git repository configuration for Genesis.
func (c *Config) getGenesisRepository() GitRepository {
	genesisConfig := c.getGenesisConfig()

	repo := "git@github.com:genesis-community/genesis"
	if genesisConfig.Repo != "" {
		repo = genesisConfig.Repo
	}

	branch := genesisConfig.Branch
	if branch == "" {
		branch = "v3.1.x-dev" // Default from applyDefaults
	}

	return GitRepository{
		Name:      "genesis",
		Enabled:   true,
		Condition: "",
		URL:       repo,
		Branch:    branch,
		Commit:    genesisConfig.Commit,
		Dest:      "${HOME}/ocfp/genesis",
		Depth:     0, // Full clone for building
	}
}

// getCorePackages returns core package groups.
func (c *Config) getCorePackages() map[string]PackageGroup {
	return map[string]PackageGroup{
		"essential":    c.getEssentialPackages(),
		"cloudfoundry": c.getCloudFoundryPackages(),
	}
}

// getEssentialPackages returns brew prerequisite packages only.
// All other packages moved to brew (system tools, dev libs) or CPAN (Perl modules).
func (c *Config) getEssentialPackages() PackageGroup {
	return PackageGroup{
		Enabled:     true,
		Condition:   "",
		DependsOn:   []string{},
		Packages:    []string{"build-essential", "procps", "curl", "file", "git", "ca-certificates", "ncurses-term"},
		PipPackages: []string{},
		Verify:      []string{},
		PostInstall: "",
	}
}

// getCloudFoundryPackages returns CloudFoundry-related packages.
// Dev libs moved to brew: zlib, libxslt, libxml2, sqlite3.
func (c *Config) getCloudFoundryPackages() PackageGroup {
	return PackageGroup{
		Enabled:     true,
		Condition:   "",
		DependsOn:   []string{},
		Packages:    []string{},
		PipPackages: []string{},
		Verify:      []string{},
		PostInstall: "",
	}
}

// getPostBrewPackages returns packages that have no brew formula and must be
// installed via APT after Linuxbrew is available.
func (c *Config) getPostBrewPackages() PackageGroup {
	return PackageGroup{
		Enabled:     true,
		Condition:   "",
		DependsOn:   []string{},
		Packages:    []string{"libperl-dev", "libfuse2", "apt-rdepends", "lsb-release", "perl-doc"},
		PipPackages: []string{},
		Verify:      []string{},
		PostInstall: "",
	}
}

// addProviderPackages adds provider-specific package groups.
func (c *Config) addProviderPackages(packages map[string]PackageGroup) {
	switch c.provider {
	case providerStackit:
		packages["stackit"] = c.getStackitPackages()
	case providerAWS:
		packages["aws"] = c.getAWSPackages()
	case providerAzure:
		packages["azure"] = c.getAzurePackages()
	case providerGCP:
		packages["gcp"] = c.getGCPPackages()
	case providerOpenStack:
		packages["openstack"] = c.getOpenStackPackages()
	case providerVMware, providerVsphere:
		packages["vmware"] = c.getVMwarePackages()
	}
}

// getStackitPackages returns STACKIT-specific packages.
func (c *Config) getStackitPackages() PackageGroup {
	return PackageGroup{
		Enabled:     true,
		Condition:   condProviderIsStackit,
		DependsOn:   []string{"stackit"},
		Packages:    []string{"stackit"},
		PipPackages: []string{},
		Verify:      []string{},
		PostInstall: "configure_stackit_cli",
	}
}

// getAWSPackages returns AWS-specific packages.
func (c *Config) getAWSPackages() PackageGroup {
	return PackageGroup{
		Enabled:     true,
		Condition:   condProviderIsAWS,
		DependsOn:   []string{},
		Packages:    []string{"unzip"}, // Required for AWS CLI v2 installation
		PipPackages: []string{},
		Verify:      []string{"aws"},
		PostInstall: "install_aws_cli_v2",
	}
}

// getAzurePackages returns Azure-specific packages.
// azure-cli moved to brew.
func (c *Config) getAzurePackages() PackageGroup {
	return PackageGroup{
		Enabled:     true,
		Condition:   condProviderIsAzure,
		DependsOn:   []string{},
		Packages:    []string{},
		PipPackages: []string{},
		Verify:      []string{"az"},
		PostInstall: "configure_azure_cli",
	}
}

// getGCPPackages returns GCP-specific packages.
// google-cloud-sdk moved to brew.
func (c *Config) getGCPPackages() PackageGroup {
	return PackageGroup{
		Enabled:     true,
		Condition:   condProviderIsGCP,
		DependsOn:   []string{},
		Packages:    []string{},
		PipPackages: []string{},
		Verify:      []string{"gcloud"},
		PostInstall: "configure_gcp_cli",
	}
}

// getOpenStackPackages returns OpenStack-specific packages.
func (c *Config) getOpenStackPackages() PackageGroup {
	return PackageGroup{
		Enabled:   true,
		Condition: condProviderIsOpenstack,
		DependsOn: []string{},
		Packages: []string{
			"python3-openstackclient",
			"python3-neutronclient",
			"python3-octaviaclient",
			"python3-cinderclient",
		},
		PipPackages: []string{},
		Verify:      []string{"openstack"},
		PostInstall: "configure_openstack_cli",
	}
}

// getVMwarePackages returns VMware-specific packages.
func (c *Config) getVMwarePackages() PackageGroup {
	return PackageGroup{
		Enabled:   true,
		Condition: condProviderIsVMware,
		DependsOn: []string{},
		Packages: []string{
			"build-essential",
			"python3-dev",
			"python3-pip",
		},
		PipPackages: []string{
			"vsphere-automation-sdk-python",
			"pyvmomi",
		},
		Verify:      []string{`python3 -c "import vsphere_automation_sdk"`},
		PostInstall: "configure_vmware_cli",
	}
}

// getGenesisTools returns Genesis-related binary tools.
func (c *Config) getGenesisTools() []BinaryTool {
	return []BinaryTool{
		c.getGenesisTool(),
		c.getSafeTool(),
		c.getSpruceTool(),
		c.getVaultTool(),
		c.getBaoTool(),
	}
}

// getCloudFoundryTools returns CloudFoundry-related binary tools.
func (c *Config) getCloudFoundryTools() []BinaryTool {
	return []BinaryTool{
		c.getBoshTool(),
		c.getCFTool(),
		c.getCredHubTool(),
		c.getUAATool(),
	}
}

// getGenesisTool returns Genesis tool configuration.
// Downloads genesis v3.1.0-rc.1 binary directly from GitHub release.
func (c *Config) getGenesisTool() BinaryTool {
	genesisConfig := c.getGenesisConfig()

	version := genesisConfig.VersionPrefix
	if version == "" {
		version = "3.1.0"
	}

	// Check for binary download mode (tool override with URL)
	if override, exists := c.config.Bastion.ToolOverrides["genesis"]; exists {
		if override.URL != "" {
			return c.getGenesisBinaryDownload(override)
		}

		if override.InstallCommand != "" {
			return c.getGenesisCustomInstall(override, version)
		}
	}

	// Default: source-based installation
	return c.getGenesisSourceBuild(version)
}

// getGenesisSourceBuild returns Genesis tool configuration for source-based installation.
func (c *Config) getGenesisSourceBuild(version string) BinaryTool {
	installCmd := fmt.Sprintf(`# Genesis source-based installation
if [ ! -d ~/ocfp/genesis/.git ]; then
    log_error "Genesis repository not cloned. This should not happen."
    exit 1
fi

pushd ~/ocfp/genesis > /dev/null

# Clean previous builds
rm -rf genesis-*

# Build genesis
log_info "Building genesis version %s"
if ! ./pack %s; then
    log_error "Failed to build genesis"
    popd > /dev/null
    exit 1
fi

# Install genesis binary
GENESIS_BIN="genesis-%s"
if [ ! -f "$GENESIS_BIN" ]; then
    log_error "Genesis binary not found: $GENESIS_BIN"
    popd > /dev/null
    exit 1
fi

# Determine installation path
if command -v genesis > /dev/null 2>&1; then
    INSTALL_PATH=$(command -v genesis)
else
    INSTALL_PATH="/usr/local/bin/genesis"
fi

log_info "Installing genesis to $INSTALL_PATH"
sudo cp "$GENESIS_BIN" "$INSTALL_PATH"
sudo chmod +x "$INSTALL_PATH"

log_info "Creating symbolic link 'g' for genesis"
sudo ln -sf /usr/local/bin/genesis /usr/local/bin/g

popd > /dev/null`, version, version, version)

	return BinaryTool{
		Name:           "genesis",
		Enabled:        true,
		Condition:      "",
		URL:            "",
		VersionURL:     "",
		VersionPattern: "",
		URLTemplate:    "",
		Dest:           "/usr/local/bin/genesis",
		Mode:           fileModeExecutable,
		Extract:        false,
		InstallCommand: installCmd,
		Verify:         fmt.Sprintf("genesis --version | grep -q '%s'", version),
		Sudo:           false, // Sudo handled within InstallCommand
	}
}

// getGenesisBinaryDownload returns Genesis tool configuration for binary download mode.
// This mode is intended for future use when Genesis releases stable binaries.
func (c *Config) getGenesisBinaryDownload(override config.ToolOverride) BinaryTool {
	verifyCmd := override.VerifyCommand
	if verifyCmd == "" {
		verifyCmd = "genesis --version"
	}

	return BinaryTool{
		Name:           "genesis",
		Enabled:        true,
		Condition:      "",
		URL:            override.URL,
		VersionURL:     override.VersionURL,
		VersionPattern: override.VersionPattern,
		URLTemplate:    override.URLTemplate,
		Dest:           "/usr/local/bin/genesis",
		Mode:           fileModeExecutable,
		Extract:        false,
		InstallCommand: "",
		Verify:         verifyCmd,
		Sudo:           true,
	}
}

// getGenesisCustomInstall returns Genesis tool configuration for custom installation.
// This allows users to provide their own installation script via config.
func (c *Config) getGenesisCustomInstall(override config.ToolOverride, version string) BinaryTool {
	verifyCmd := override.VerifyCommand
	if verifyCmd == "" {
		verifyCmd = fmt.Sprintf("genesis --version | grep -q '%s'", version)
	}

	return BinaryTool{
		Name:           "genesis",
		Enabled:        true,
		Condition:      "",
		URL:            "",
		VersionURL:     "",
		VersionPattern: "",
		URLTemplate:    "",
		Dest:           "/usr/local/bin/genesis",
		Mode:           fileModeExecutable,
		Extract:        false,
		InstallCommand: override.InstallCommand,
		Verify:         verifyCmd,
		Sudo:           false,
	}
}

// getSafeTool returns Safe tool configuration.
func (c *Config) getSafeTool() BinaryTool {
	return BinaryTool{
		Name:           "safe",
		Enabled:        true,
		Condition:      "",
		URL:            "",
		VersionURL:     "https://api.github.com/repos/cloudfoundry-community/safe/releases/latest",
		VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
		URLTemplate:    "https://github.com/cloudfoundry-community/safe/releases/download/v${VERSION}/safe-${VERSION}-linux-amd64",
		Dest:           "/usr/local/bin/safe",
		Mode:           fileModeExecutable,
		Extract:        false,
		InstallCommand: "",
		Verify:         "safe --version",
		Sudo:           true,
	}
}

// getSpruceTool returns Spruce tool configuration.
// Installed via brew (cloudfoundry-community/cf).
func (c *Config) getSpruceTool() BinaryTool {
	return BinaryTool{
		Name:           "spruce",
		Enabled:        false,
		Condition:      "",
		URL:            "",
		VersionURL:     "https://api.github.com/repos/geofffranks/spruce/releases/latest",
		VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
		URLTemplate:    "https://github.com/geofffranks/spruce/releases/download/v${VERSION}/spruce-linux-amd64",
		Dest:           "/usr/local/bin/spruce",
		Mode:           fileModeExecutable,
		Extract:        false,
		InstallCommand: "",
		Verify:         "spruce --version",
		Sudo:           true,
	}
}

// getVaultTool returns Vault tool configuration.
// Installed via brew (hashicorp/tap).
func (c *Config) getVaultTool() BinaryTool {
	return BinaryTool{
		Name:           "vault",
		Enabled:        false,
		Condition:      "",
		URL:            "",
		VersionURL:     "https://api.github.com/repos/hashicorp/vault/releases/latest",
		VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
		URLTemplate:    "https://releases.hashicorp.com/vault/${VERSION}/vault_${VERSION}_linux_amd64.zip",
		Dest:           "/usr/local/bin/vault",
		Mode:           fileModeExecutable,
		Extract:        true,
		InstallCommand: "",
		Verify:         "vault --version",
		Sudo:           true,
	}
}

// getBoshTool returns BOSH tool configuration.
// Installed via brew (cloudfoundry/tap).
func (c *Config) getBoshTool() BinaryTool {
	return BinaryTool{
		Name:           "bosh",
		Enabled:        false,
		Condition:      "",
		URL:            "",
		VersionURL:     "https://api.github.com/repos/cloudfoundry/bosh-cli/releases/latest",
		VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
		URLTemplate:    "https://github.com/cloudfoundry/bosh-cli/releases/download/v${VERSION}/bosh-cli-${VERSION}-linux-amd64",
		Dest:           "/usr/local/bin/bosh",
		Mode:           fileModeExecutable,
		Extract:        false,
		InstallCommand: "",
		Verify:         "bosh --version",
		Sudo:           true,
	}
}

// getCredHubTool returns CredHub tool configuration.
// Installed via brew (cloudfoundry/tap).
func (c *Config) getCredHubTool() BinaryTool {
	return BinaryTool{
		Name:           "credhub",
		Enabled:        false,
		Condition:      "",
		URL:            "",
		VersionURL:     "https://api.github.com/repos/cloudfoundry/credhub-cli/releases/latest",
		VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
		URLTemplate:    "https://github.com/cloudfoundry/credhub-cli/releases/download/${VERSION}/credhub-linux-amd64-${VERSION}.tgz",
		Dest:           "/usr/local/bin/credhub",
		Mode:           fileModeExecutable,
		Extract:        true,
		InstallCommand: "",
		Verify:         "credhub --version",
		Sudo:           true,
	}
}

// getUAATool returns UAA tool configuration.
// Installed via brew (cloudfoundry/tap).
func (c *Config) getUAATool() BinaryTool {
	return BinaryTool{
		Name:           "uaa",
		Enabled:        false,
		Condition:      "",
		URL:            "",
		VersionURL:     "https://api.github.com/repos/cloudfoundry/uaa-cli/releases/latest",
		VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
		URLTemplate:    "https://github.com/cloudfoundry/uaa-cli/releases/download/${VERSION}/uaa-linux-amd64-${VERSION}",
		Dest:           "/usr/local/bin/uaa",
		Mode:           fileModeExecutable,
		Extract:        false,
		InstallCommand: "",
		Verify:         "uaa --version",
		Sudo:           true,
	}
}

// getBaoTool returns Bao (OpenBao) tool configuration.
// Installed via brew.
func (c *Config) getBaoTool() BinaryTool {
	return BinaryTool{
		Name:           "bao",
		Enabled:        false,
		Condition:      "",
		URL:            "",
		VersionURL:     "https://api.github.com/repos/openbao/openbao/releases/latest",
		VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
		URLTemplate:    "https://github.com/openbao/openbao/releases/download/v${VERSION}/bao_${VERSION}_linux_amd64.deb",
		Dest:           "/usr/local/bin/bao",
		Mode:           fileModeExecutable,
		Extract:        true,
		InstallCommand: "",
		Verify:         "bao --version",
		Sudo:           true,
	}
}

// getCFTool returns CF CLI tool configuration.
// Installed via brew (cloudfoundry/tap).
func (c *Config) getCFTool() BinaryTool {
	return BinaryTool{
		Name:           "cf",
		Enabled:        false,
		Condition:      "",
		URL:            "",
		VersionURL:     "https://api.github.com/repos/cloudfoundry/cli/releases/latest",
		VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
		URLTemplate:    "https://github.com/cloudfoundry/cli/releases/download/v${VERSION}/cf8-cli_${VERSION}_linux_x86-64.tgz",
		Dest:           "/usr/local/bin/cf",
		Mode:           fileModeExecutable,
		Extract:        true,
		InstallCommand: "",
		Verify:         "cf --version",
		Sudo:           true,
	}
}

// generateEnvironmentScript generates environment setup script content.
func (c *Config) generateEnvironmentScript() string {
	// Preallocate lines: shebang+comment+blank (3) + two exports (2) + PATH (1) + env vars
	envVars := c.getProviderEnvironmentVars()
	lines := make([]string, 0, scriptBufferEnvVars+len(envVars))

	lines = append(lines, "#!/bin/bash")
	lines = append(lines, "# Environment setup script")
	lines = append(lines, "")

	// Add OCFP environment variables
	lines = append(lines, fmt.Sprintf("export OCFP_BLOC='%s'", c.config.Name))
	lines = append(lines, fmt.Sprintf("export OCFP_PROVIDER='%s'", c.provider))

	// Add provider-specific environment variables
	for key, value := range envVars {
		lines = append(lines, fmt.Sprintf("export %s='%s'", key, value))
	}

	// Add PATH updates
	lines = append(lines, "export PATH=\"$HOME/bin:/usr/local/bin:$PATH\"")

	return strings.Join(lines, "\n")
}

// generateGitConfigScript generates Git configuration script content.
func (c *Config) generateGitConfigScript() string {
	if !c.hasGitConfig() {
		return ""
	}

	var lines []string

	lines = append(lines, "#!/bin/bash")
	lines = append(lines, "# Git configuration script")
	lines = append(lines, "")

	if c.config.Bastion.Git.User.Name != "" {
		lines = append(lines, fmt.Sprintf("git config --global user.name '%s'",
			c.config.Bastion.Git.User.Name))
	}

	if c.config.Bastion.Git.User.Email != "" {
		lines = append(lines, fmt.Sprintf("git config --global user.email '%s'",
			c.config.Bastion.Git.User.Email))
	}

	return strings.Join(lines, "\n")
}

// generateProviderScript generates provider-specific configuration script.
func (c *Config) generateProviderScript() string {
	switch c.provider {
	case "stackit":
		return c.generateStackitScript()
	case "aws":
		return c.generateAWSScript()
	case "azure":
		return c.generateAzureScript()
	case "gcp":
		return c.generateGCPScript()
	case "openstack":
		return c.generateOpenStackScript()
	default:
		return ""
	}
}

// generateStackitScript generates STACKIT-specific configuration script.
func (c *Config) generateStackitScript() string {
	var lines []string

	lines = append(lines, "#!/bin/bash")
	lines = append(lines, "# STACKIT CLI configuration")
	lines = append(lines, "")

	if c.config.ProjectID != "" {
		lines = append(lines, "# Configure STACKIT CLI")
		lines = append(lines, fmt.Sprintf("stackit config set --project-id '%s'", c.config.ProjectID))
	}

	return strings.Join(lines, "\n")
}

// generateAWSScript generates AWS-specific configuration script.
func (c *Config) generateAWSScript() string {
	lines := make([]string, 0, 4) //nolint:mnd
	lines = append(lines, "#!/bin/bash")
	lines = append(lines, "# AWS CLI configuration")
	lines = append(lines, "")
	lines = append(lines, "# AWS CLI is configured via environment variables")

	return strings.Join(lines, "\n")
}

// generateAzureScript generates Azure-specific configuration script.
func (c *Config) generateAzureScript() string {
	lines := make([]string, 0, 4) //nolint:mnd
	lines = append(lines, "#!/bin/bash")
	lines = append(lines, "# Azure CLI configuration")
	lines = append(lines, "")
	lines = append(lines, "# Azure CLI is configured via environment variables")

	return strings.Join(lines, "\n")
}

// generateGCPScript generates GCP-specific configuration script.
func (c *Config) generateGCPScript() string {
	lines := make([]string, 0, 4) //nolint:mnd
	lines = append(lines, "#!/bin/bash")
	lines = append(lines, "# GCP CLI configuration")
	lines = append(lines, "")

	if c.config.ProjectID != "" {
		lines = append(lines, fmt.Sprintf("gcloud config set project '%s'", c.config.ProjectID))
	}

	return strings.Join(lines, "\n")
}

// generateOpenStackScript generates OpenStack-specific configuration script.
func (c *Config) generateOpenStackScript() string {
	lines := make([]string, 0, 4) //nolint:mnd
	lines = append(lines, "#!/bin/bash")
	lines = append(lines, "# OpenStack CLI configuration")
	lines = append(lines, "")
	lines = append(lines, "# OpenStack CLI is configured via environment variables")

	return strings.Join(lines, "\n")
}

// hasGitConfig checks if Git configuration is available.
func (c *Config) hasGitConfig() bool {
	return c.config.Bastion.Git.User.Name != "" || c.config.Bastion.Git.User.Email != ""
}

// getProviderEnvironmentVars returns provider-specific environment variables.
func (c *Config) getProviderEnvironmentVars() map[string]string {
	vars := make(map[string]string)

	switch c.provider {
	case "stackit":
		c.addStackitVars(vars)
	case "aws":
		c.addAWSVars(vars)
	case "azure":
		c.addAzureVars(vars)
	case "gcp":
		c.addGCPVars(vars)
	case "openstack":
		c.addOpenStackVars(vars)
	}

	return vars
}

// addStackitVars adds STACKIT-specific environment variables.
func (c *Config) addStackitVars(vars map[string]string) {
	c.addVarIfNotEmpty(vars, "STACKIT_PROJECT_ID", c.config.ProjectID)
	c.addVarIfNotEmpty(vars, "STACKIT_ORG_ID", c.config.OrgID)
	c.addVarIfNotEmpty(vars, "STACKIT_REGION", c.config.Region)
}

// addAWSVars adds AWS-specific environment variables.
func (c *Config) addAWSVars(vars map[string]string) {
	c.addVarIfNotEmpty(vars, "AWS_ACCESS_KEY_ID", c.config.AccessKeyID)
	c.addVarIfNotEmpty(vars, "AWS_SECRET_ACCESS_KEY", c.config.SecretAccessKey)
	c.addVarIfNotEmpty(vars, "AWS_DEFAULT_REGION", c.config.Region)
}

// addAzureVars adds Azure-specific environment variables.
func (c *Config) addAzureVars(vars map[string]string) {
	c.addVarIfNotEmpty(vars, "AZURE_SUBSCRIPTION_ID", c.config.SubscriptionID)
	c.addVarIfNotEmpty(vars, "AZURE_TENANT_ID", c.config.TenantID)
	c.addVarIfNotEmpty(vars, "AZURE_CLIENT_ID", c.config.ClientID)
	c.addVarIfNotEmpty(vars, "AZURE_CLIENT_SECRET", c.config.ClientSecret)
}

// addGCPVars adds GCP-specific environment variables.
func (c *Config) addGCPVars(vars map[string]string) {
	c.addVarIfNotEmpty(vars, "GCP_PROJECT_ID", c.config.ProjectID)
}

// addOpenStackVars adds OpenStack-specific environment variables.
func (c *Config) addOpenStackVars(vars map[string]string) {
	c.addVarIfNotEmpty(vars, "OS_AUTH_URL", c.config.AuthURL)
	c.addVarIfNotEmpty(vars, "OS_USERNAME", c.config.Username)
	c.addVarIfNotEmpty(vars, "OS_PASSWORD", c.config.Password)
	c.addVarIfNotEmpty(vars, "OS_PROJECT_NAME", c.config.ProjectName)

	if c.config.DomainName != "" {
		vars["OS_USER_DOMAIN_NAME"] = c.config.DomainName
		vars["OS_PROJECT_DOMAIN_NAME"] = c.config.DomainName
	}
}

// addVarIfNotEmpty adds a variable to the map if the value is not empty.
func (c *Config) addVarIfNotEmpty(vars map[string]string, key, value string) {
	if value != "" {
		vars[key] = value
	}
}
