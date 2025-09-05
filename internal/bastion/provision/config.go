package provision

import (
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// Config implements the ProvisionConfig interface.
type Config struct {
	provider string
	config   *config.Config
}

// NewConfig creates a new provisioning configuration.
func NewConfig(provider string, cfg *config.Config) *Config {
	return &Config{
		provider: provider,
		config:   cfg,
	}
}

// GetSystemConfig returns system configuration.
func (c *Config) GetSystemConfig() SystemConfig {
	return SystemConfig{
		Hostname: HostnameConfig{
			Enabled: true,
			Pattern: "${OCFP_BLOC_NAME}-bastion",
		},
		WaitTime:    10,
		Timezone:    "UTC",
		Locale:      "en_US.UTF-8",
		UpdateCache: true,
	}
}

// GetDirectories returns directories to create.
func (c *Config) GetDirectories() []DirectoryConfig {
	return []DirectoryConfig{
		{Path: "${HOME}/ocfp/cli", Mode: 0755},
		{Path: "${HOME}/.ocfp/config", Mode: 0755},
		{Path: "${HOME}/bin", Mode: 0755},
		{Path: "${HOME}/.ocfp/logs/provision", Mode: 0755},
		{Path: "${HOME}/deployments", Mode: 0755},
		{Path: "${HOME}/.ssh", Mode: 0700},
	}
}

// GetPackages returns package groups to install.
func (c *Config) GetPackages() map[string]PackageGroup {
	packages := map[string]PackageGroup{
		"essential": {
			Enabled: true,
			Packages: []string{
				"rsync", "curl", "wget", "git", "unzip", "tig", "ack-grep", "ripgrep",
				"python3", "python3-pip", "ca-certificates", "gnupg", "lsb-release",
				"tar", "gzip", "gawk", "sed", "grep", "coreutils", "cpanminus",
				"perl-doc", "libperl-dev", "make", "gcc", "build-essential",
				"libnet-ip-perl", "libnetaddr-ip-perl", "libjson-perl", "libnet-cidr-perl",
				"snapd", "libreadline-dev", "apt-rdepends", "gpg", "htop",
				"libssl-dev", "libtool", "libyaml-dev", "libyaml-libyaml-perl",
				"libyaml-perl", "python3-dev", "python3-setuptools", "s3cmd",
				"vim", "vim-common", "libfuse2",
			},
		},
		"cloudfoundry": {
			Enabled:   true,
			DependsOn: []string{"cloudfoundry-community"},
			Packages: []string{
				"zlib1g-dev", "ruby", "ruby-dev", "openssl",
				"libxslt1-dev", "libxml2-dev", "libssl-dev", "libyaml-dev",
				"libsqlite3-dev", "sqlite3", "safe", "spruce", "vault", "jq",
				"credhub-cli", "bosh-cli", "cf7-cli", "uaa-cli", "tmux", "screen", "tree",
			},
			Verify: []string{"safe", "spruce", "vault", "jq", "bosh", "cf", "credhub"},
		},
	}

	// Add provider-specific packages
	switch c.provider {
	case "stackit":
		packages["stackit"] = PackageGroup{
			Enabled:     true,
			Condition:   "provider_is_stackit",
			DependsOn:   []string{"stackit"},
			Packages:    []string{"stackit"},
			PostInstall: "configure_stackit_cli",
		}
	case "aws":
		packages["aws"] = PackageGroup{
			Enabled:     true,
			Condition:   "provider_is_aws",
			Packages:    []string{"awscli"},
			Verify:      []string{"aws"},
			PostInstall: "configure_aws_cli",
		}
	case "azure":
		packages["azure"] = PackageGroup{
			Enabled:     true,
			Condition:   "provider_is_azure",
			DependsOn:   []string{"azure-cli"},
			Packages:    []string{"azure-cli"},
			Verify:      []string{"az"},
			PostInstall: "configure_azure_cli",
		}
	case "gcp":
		packages["gcp"] = PackageGroup{
			Enabled:     true,
			Condition:   "provider_is_gcp",
			DependsOn:   []string{"google-cloud-sdk"},
			Packages:    []string{"google-cloud-sdk"},
			Verify:      []string{"gcloud"},
			PostInstall: "configure_gcp_cli",
		}
	case "openstack":
		packages["openstack"] = PackageGroup{
			Enabled:   true,
			Condition: "provider_is_openstack",
			Packages: []string{
				"python3-openstackclient",
				"python3-neutronclient",
				"python3-octaviaclient",
				"python3-cinderclient",
			},
			Verify:      []string{"openstack"},
			PostInstall: "configure_openstack_cli",
		}
	case "vmware", "vsphere":
		packages["vmware"] = PackageGroup{
			Enabled:   true,
			Condition: "provider_is_vmware",
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

	return packages
}

// GetGenesisDeployments returns Genesis deployments to initialize.
func (c *Config) GetGenesisDeployments() []GenesisDeployment {
	deployments := []GenesisDeployment{
		{Name: "bosh", Kit: "bosh", Repo: "bosh-genesis-kit", Branch: "develop", Enabled: true},
		{Name: "vault", Kit: "vault", Repo: "vault-genesis-kit", Enabled: true},
		{Name: "concourse", Kit: "concourse", Repo: "concourse-genesis-kit", Enabled: true},
		{Name: "cf", Kit: "cf", Repo: "cf-genesis-kit", Enabled: true},
		{Name: "blacksmith", Kit: "blacksmith", Repo: "blacksmith-genesis-kit", Enabled: true},
		{Name: "shield", Kit: "shield", Repo: "shield-genesis-kit", Enabled: true},
		{Name: "prometheus", Kit: "prometheus", Repo: "prometheus-genesis-kit", Enabled: true},
		{Name: "doomsday", Kit: "doomsday", Repo: "doomsday-genesis-kit", Enabled: true},
		{Name: "scheduler", Kit: "ocf-scheduler", Repo: "ocf-scheduler-genesis-kit", Enabled: true},
		{Name: "autoscaler", Kit: "cf-app-autoscaler", Repo: "cf-app-autoscaler-genesis-kit", Enabled: true},
		{Name: "jumpbox", Kit: "jumpbox", Repo: "jumpbox-genesis-kit", Enabled: true},
	}

	return deployments
}

// GetBinaryTools returns binary tools to install.
func (c *Config) GetBinaryTools() []BinaryTool {
	return []BinaryTool{
		{
			Name:           "genesis",
			Enabled:        true,
			VersionURL:     "https://api.github.com/repos/genesis-community/genesis/releases/latest",
			VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
			URLTemplate:    "https://github.com/genesis-community/genesis/releases/download/v${VERSION}/genesis-${VERSION}-linux-amd64",
			Dest:           "/usr/local/bin/genesis",
			Mode:           0755,
			Sudo:           true,
			Verify:         "genesis --version",
		},
		{
			Name:           "safe",
			Enabled:        true,
			VersionURL:     "https://api.github.com/repos/starkandwayne/safe/releases/latest",
			VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
			URLTemplate:    "https://github.com/starkandwayne/safe/releases/download/v${VERSION}/safe-linux-amd64",
			Dest:           "/usr/local/bin/safe",
			Mode:           0755,
			Sudo:           true,
			Verify:         "safe --version",
		},
		{
			Name:           "spruce",
			Enabled:        true,
			VersionURL:     "https://api.github.com/repos/geofffranks/spruce/releases/latest",
			VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
			URLTemplate:    "https://github.com/geofffranks/spruce/releases/download/v${VERSION}/spruce-linux-amd64",
			Dest:           "/usr/local/bin/spruce",
			Mode:           0755,
			Sudo:           true,
			Verify:         "spruce --version",
		},
		{
			Name:           "vault",
			Enabled:        true,
			VersionURL:     "https://api.github.com/repos/hashicorp/vault/releases/latest",
			VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
			URLTemplate:    "https://releases.hashicorp.com/vault/${VERSION}/vault_${VERSION}_linux_amd64.zip",
			Dest:           "/usr/local/bin/vault",
			Mode:           0755,
			Sudo:           true,
			Extract:        true,
			Verify:         "vault --version",
		},
		{
			Name:    "bosh",
			Enabled: true,
			URL:     "https://github.com/cloudfoundry/bosh-cli/releases/latest/download/bosh-cli-*-linux-amd64",
			Dest:    "/usr/local/bin/bosh",
			Mode:    0755,
			Sudo:    true,
			Verify:  "bosh --version",
		},
		{
			Name:           "cf",
			Enabled:        true,
			VersionURL:     "https://api.github.com/repos/cloudfoundry/cli/releases/latest",
			VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
			URLTemplate:    "https://github.com/cloudfoundry/cli/releases/download/v${VERSION}/cf7-cli_${VERSION}_linux_x86-64.tgz",
			Dest:           "/usr/local/bin/cf",
			Mode:           0755,
			Sudo:           true,
			Extract:        true,
			Verify:         "cf --version",
		},
		{
			Name:           "credhub",
			Enabled:        true,
			VersionURL:     "https://api.github.com/repos/cloudfoundry-incubator/credhub-cli/releases/latest",
			VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
			URLTemplate:    "https://github.com/cloudfoundry-incubator/credhub-cli/releases/download/${VERSION}/credhub-linux-${VERSION}.tgz",
			Dest:           "/usr/local/bin/credhub",
			Mode:           0755,
			Sudo:           true,
			Extract:        true,
			Verify:         "credhub --version",
		},
		{
			Name:           "uaa",
			Enabled:        true,
			VersionURL:     "https://api.github.com/repos/cloudfoundry-incubator/uaa-cli/releases/latest",
			VersionPattern: `"tag_name":\s*"v?([^"]+)"`,
			URLTemplate:    "https://github.com/cloudfoundry-incubator/uaa-cli/releases/download/${VERSION}/uaa-linux-amd64-${VERSION}",
			Dest:           "/usr/local/bin/uaa",
			Mode:           0755,
			Sudo:           true,
			Verify:         "uaa --version",
		},
	}
}

// GetAPTRepositories returns APT repositories to add.
func (c *Config) GetAPTRepositories() []APTRepository {
	repos := []APTRepository{
		{
			Name:    "cloudfoundry-community",
			Enabled: true,
			GPGKey: GPGKey{
				URL:  "https://raw.githubusercontent.com/cloudfoundry-community/homebrew-cf/master/public.key",
				Dest: "/etc/apt/keyrings/cloudfoundry-community.key",
			},
			SourceLine: "deb [signed-by=/etc/apt/keyrings/cloudfoundry-community.key] http://apt.community.cloudfoundry.org stable main",
			SourceFile: "/etc/apt/sources.list.d/community.list",
		},
	}

	// Add provider-specific repositories
	switch c.provider {
	case "stackit":
		repos = append(repos, APTRepository{
			Name:      "stackit",
			Enabled:   true,
			Condition: "provider_is_stackit",
			GPGKey: GPGKey{
				URL:     "https://packages.stackit.cloud/keys/key.gpg",
				Dest:    "/usr/share/keyrings/stackit.gpg",
				Dearmor: true,
			},
			SourceLine: "deb [signed-by=/usr/share/keyrings/stackit.gpg] https://packages.stackit.cloud/apt/cli stackit main",
			SourceFile: "/etc/apt/sources.list.d/stackit.list",
		})
	case "azure":
		repos = append(repos, APTRepository{
			Name:      "azure-cli",
			Enabled:   true,
			Condition: "provider_is_azure",
			GPGKey: GPGKey{
				URL:  "https://packages.microsoft.com/keys/microsoft.asc",
				Dest: "/etc/apt/keyrings/microsoft.key",
			},
			SourceLine: "deb [arch=amd64 signed-by=/etc/apt/keyrings/microsoft.key] https://packages.microsoft.com/repos/azure-cli/ jammy main",
			SourceFile: "/etc/apt/sources.list.d/azure-cli.list",
		})
	case "gcp":
		repos = append(repos, APTRepository{
			Name:      "google-cloud-sdk",
			Enabled:   true,
			Condition: "provider_is_gcp",
			GPGKey: GPGKey{
				URL:  "https://packages.cloud.google.com/apt/doc/apt-key.gpg",
				Dest: "/etc/apt/keyrings/google-cloud.key",
			},
			SourceLine: "deb [signed-by=/etc/apt/keyrings/google-cloud.key] https://packages.cloud.google.com/apt cloud-sdk main",
			SourceFile: "/etc/apt/sources.list.d/google-cloud-sdk.list",
		})
	}

	return repos
}

// GetGitRepositories returns Git repositories to clone.
func (c *Config) GetGitRepositories() []GitRepository {
	repos := []GitRepository{
		{
			Name:    "genesis-community-kits",
			Enabled: true,
			URL:     "https://github.com/genesis-community/genesis-community-kits.git",
			Branch:  "main",
			Dest:    "${HOME}/genesis-community-kits",
			Depth:   1,
		},
	}

	// Add genesis repository if configured
	if c.config.Bastion.Genesis.Enabled {
		repo := GitRepository{
			Name:    "genesis",
			Enabled: true,
			URL:     c.config.Bastion.Genesis.Repo,
			Branch:  c.config.Bastion.Genesis.Branch,
			Dest:    "${HOME}/ocfp/genesis",
			Depth:   1,
		}

		if repo.URL == "" {
			repo.URL = "git@github.com:genesis-community/genesis.git"
		}

		if repo.Branch == "" {
			repo.Branch = "v3.1.x-dev"
		}

		repos = append(repos, repo)
	}

	return repos
}

// GetCustomScripts returns custom scripts to execute.
func (c *Config) GetCustomScripts() []CustomScript {
	scripts := []CustomScript{
		{
			Name:    "setup-environment",
			Enabled: true,
			Content: c.generateEnvironmentScript(),
			Execute: true,
		},
		{
			Name:    "configure-git",
			Enabled: c.hasGitConfig(),
			Content: c.generateGitConfigScript(),
			Execute: true,
		},
	}

	// Add provider-specific scripts
	providerScript := c.generateProviderScript()
	if providerScript != "" {
		scripts = append(scripts, CustomScript{
			Name:    "configure-" + c.provider,
			Enabled: true,
			Content: providerScript,
			Execute: true,
		})
	}

	return scripts
}

// Helper methods for script generation

func (c *Config) generateEnvironmentScript() string {
	var lines []string

	lines = append(lines, "#!/bin/bash")
	lines = append(lines, "# Environment setup script")
	lines = append(lines, "")

	// Add OCFP environment variables
	lines = append(lines, fmt.Sprintf("export OCFP_BLOC_NAME='%s'", c.config.Name))
	lines = append(lines, fmt.Sprintf("export OCFP_PROVIDER='%s'", c.provider))

	// Add provider-specific environment variables
	envVars := c.getProviderEnvironmentVars()
	for key, value := range envVars {
		lines = append(lines, fmt.Sprintf("export %s='%s'", key, value))
	}

	// Add PATH updates
	lines = append(lines, "export PATH=\"$HOME/bin:/usr/local/bin:$PATH\"")

	return strings.Join(lines, "\n")
}

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

func (c *Config) generateAWSScript() string {
	var lines []string

	lines = append(lines, "#!/bin/bash")
	lines = append(lines, "# AWS CLI configuration")
	lines = append(lines, "")
	lines = append(lines, "# AWS CLI is configured via environment variables")

	return strings.Join(lines, "\n")
}

func (c *Config) generateAzureScript() string {
	var lines []string

	lines = append(lines, "#!/bin/bash")
	lines = append(lines, "# Azure CLI configuration")
	lines = append(lines, "")
	lines = append(lines, "# Azure CLI is configured via environment variables")

	return strings.Join(lines, "\n")
}

func (c *Config) generateGCPScript() string {
	var lines []string

	lines = append(lines, "#!/bin/bash")
	lines = append(lines, "# GCP CLI configuration")
	lines = append(lines, "")

	if c.config.ProjectID != "" {
		lines = append(lines, fmt.Sprintf("gcloud config set project '%s'", c.config.ProjectID))
	}

	return strings.Join(lines, "\n")
}

func (c *Config) generateOpenStackScript() string {
	var lines []string

	lines = append(lines, "#!/bin/bash")
	lines = append(lines, "# OpenStack CLI configuration")
	lines = append(lines, "")
	lines = append(lines, "# OpenStack CLI is configured via environment variables")

	return strings.Join(lines, "\n")
}

func (c *Config) hasGitConfig() bool {
	return c.config.Bastion.Git.User.Name != "" || c.config.Bastion.Git.User.Email != ""
}

func (c *Config) getProviderEnvironmentVars() map[string]string {
	vars := make(map[string]string)

	switch c.provider {
	case "stackit":
		if c.config.ProjectID != "" {
			vars["STACKIT_PROJECT_ID"] = c.config.ProjectID
		}

		if c.config.OrgID != "" {
			vars["STACKIT_ORG_ID"] = c.config.OrgID
		}

		if c.config.Region != "" {
			vars["STACKIT_REGION"] = c.config.Region
		}
	case "aws":
		if c.config.AccessKeyID != "" {
			vars["AWS_ACCESS_KEY_ID"] = c.config.AccessKeyID
		}

		if c.config.SecretAccessKey != "" {
			vars["AWS_SECRET_ACCESS_KEY"] = c.config.SecretAccessKey
		}

		if c.config.Region != "" {
			vars["AWS_DEFAULT_REGION"] = c.config.Region
		}
	case "azure":
		if c.config.SubscriptionID != "" {
			vars["AZURE_SUBSCRIPTION_ID"] = c.config.SubscriptionID
		}

		if c.config.TenantID != "" {
			vars["AZURE_TENANT_ID"] = c.config.TenantID
		}

		if c.config.ClientID != "" {
			vars["AZURE_CLIENT_ID"] = c.config.ClientID
		}

		if c.config.ClientSecret != "" {
			vars["AZURE_CLIENT_SECRET"] = c.config.ClientSecret
		}
	case "gcp":
		if c.config.ProjectID != "" {
			vars["GCP_PROJECT_ID"] = c.config.ProjectID
		}
	case "openstack":
		if c.config.AuthURL != "" {
			vars["OS_AUTH_URL"] = c.config.AuthURL
		}

		if c.config.Username != "" {
			vars["OS_USERNAME"] = c.config.Username
		}

		if c.config.Password != "" {
			vars["OS_PASSWORD"] = c.config.Password
		}

		if c.config.ProjectName != "" {
			vars["OS_PROJECT_NAME"] = c.config.ProjectName
		}

		if c.config.DomainName != "" {
			vars["OS_USER_DOMAIN_NAME"] = c.config.DomainName
			vars["OS_PROJECT_DOMAIN_NAME"] = c.config.DomainName
		}
	}

	return vars
}
