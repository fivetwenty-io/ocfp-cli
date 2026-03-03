package config

// TestConfigBuilder provides methods for creating test configurations.
type TestConfigBuilder struct {
	cfg *Config
}

// NewTestConfig creates a new test configuration builder with sensible defaults.
func NewTestConfig() *TestConfigBuilder {
	return &TestConfigBuilder{
		cfg: createDefaultTestConfig(),
	}
}

func createDefaultTestConfig() *Config {
	cfg := &Config{
		Name:     "test-bloc",
		Provider: "stackit",
	}

	cfg.Network = createDefaultNetworkConfig()
	cfg.Bastion = createDefaultBastionConfig()
	cfg.Genesis = createDefaultGenesisConfig()
	cfg.Deployments = NewDeploymentSettings("", nil)
	cfg.Routers = createDefaultComponentConfig()
	cfg.Cells = createDefaultComponentConfig()
	cfg.Blobstore = createDefaultBlobstoreConfig()

	cfg.DNS = []string{}
	cfg.AZs = map[string]AvailabilityZone{}
	cfg.FQDNs = &FQDNConfig{Mgmt: map[string]string{}, OCF: map[string]string{}}
	cfg.S3 = map[string]string{}
	cfg.AllowedIngressIPs = []string{}
	cfg.Subnets = []Subnet{}
	cfg.LBs = map[string]LBService{}
	cfg.Users = map[string]string{}
	cfg.Jumpbox = Jumpbox{Users: map[string]string{}}

	return cfg
}

func createDefaultNetworkConfig() NetworkConfig {
	return NetworkConfig{
		DNS: nil,
	}
}

func createDefaultBastionConfig() Bastion {
	return Bastion{
		Genesis:           createDefaultGenesisConfig(),
		Git:               createDefaultGitConfig(),
		Tools:             createDefaultOverrideSets(),
		CFPlugins:         createDefaultOverrideSets(),
		Snaps:             createDefaultOverrideSets(),
		ToolOverrides:     nil,
		CFPluginOverrides: nil,
		SnapOverrides:     nil,
	}
}

func createDefaultGenesisConfig() Genesis {
	return Genesis{
		Enabled: false,
	}
}

func createDefaultGitConfig() GitConfig {
	return GitConfig{
		User: GitUser{},
	}
}

func createDefaultOverrideSets() OverrideSets {
	return OverrideSets{
		Enable:  nil,
		Disable: nil,
	}
}

func createDefaultComponentConfig() ComponentConfig {
	return ComponentConfig{
		Count:    0,
		DiskSize: 0,
	}
}

func createDefaultBlobstoreConfig() BlobstoreConfig {
	defaultBucketSettings := BucketSettings{
		Versioning:     false,
		NoncurrentDays: 0,
	}

	return BlobstoreConfig{
		EnablePolicies: false,
		BoshBlobstore:  defaultBucketSettings,
		CFBuildpacks:   defaultBucketSettings,
		CFDroplets:     defaultBucketSettings,
		CFAppPackages:  defaultBucketSettings,
	}
}

// WithName sets the configuration name.
func (b *TestConfigBuilder) WithName(name string) *TestConfigBuilder {
	b.cfg.Name = name

	return b
}

// WithProvider sets the provider.
func (b *TestConfigBuilder) WithProvider(provider string) *TestConfigBuilder {
	b.cfg.Provider = provider

	return b
}

// WithRegion sets the region.
func (b *TestConfigBuilder) WithRegion(region string) *TestConfigBuilder {
	b.cfg.Region = region

	return b
}

// WithProjectID sets the project ID.
func (b *TestConfigBuilder) WithProjectID(projectID string) *TestConfigBuilder {
	b.cfg.ProjectID = projectID

	return b
}

// WithBastionIP sets the bastion IP.
func (b *TestConfigBuilder) WithBastionIP(ip string) *TestConfigBuilder {
	b.cfg.BastionIP = ip

	return b
}

// WithBastion sets bastion configuration.
func (b *TestConfigBuilder) WithBastion(bastion Bastion) *TestConfigBuilder {
	b.cfg.Bastion = bastion

	return b
}

// WithBastionSSHUser sets the SSH user for bastion.
func (b *TestConfigBuilder) WithBastionSSHUser(user string) *TestConfigBuilder {
	b.cfg.Bastion.SSHUser = user

	return b
}

// WithBastionGit sets git configuration for bastion.
func (b *TestConfigBuilder) WithBastionGit(name, email string) *TestConfigBuilder {
	b.cfg.Bastion.Git = GitConfig{
		User: GitUser{
			Name:  name,
			Email: email,
		},
	}

	return b
}

// WithNetwork sets network configuration.
func (b *TestConfigBuilder) WithNetwork(network NetworkConfig) *TestConfigBuilder {
	b.cfg.Network = network

	return b
}

// WithBootstrapNetwork sets bootstrap-specific network configuration.
func (b *TestConfigBuilder) WithBootstrapNetwork() *TestConfigBuilder {
	b.cfg.Network = NetworkConfig{
		ID:          "",
		Name:        "",
		CIDR:        "",
		NetworkCIDR: "",
		SubnetID:    "",
		DNS:         []string{},
	}

	return b
}

// WithBootstrapBastion sets bootstrap-specific bastion configuration.
func (b *TestConfigBuilder) WithBootstrapBastion() *TestConfigBuilder {
	b.cfg.Bastion = Bastion{
		Flavor:     "",
		Image:      "",
		OS:         "",
		OSVersion:  "",
		Keypair:    "",
		SSHUser:    "",
		SSHOptions: "",
		SSHKeyDir:  "",
		SSHKeyName: "",
		Genesis: Genesis{
			Enabled:       false,
			Repo:          "",
			Branch:        "",
			Commit:        "",
			VersionPrefix: "",
		},
		Git: GitConfig{
			User: GitUser{
				Name:  "",
				Email: "",
			},
		},
		Tools: OverrideSets{
			Enable:  nil,
			Disable: nil,
		},
		CFPlugins: OverrideSets{
			Enable:  nil,
			Disable: nil,
		},
		Snaps: OverrideSets{
			Enable:  nil,
			Disable: nil,
		},
		ToolOverrides:     map[string]ToolOverride{},
		CFPluginOverrides: map[string]CFPluginOverride{},
		SnapOverrides:     map[string]SnapOverride{},
	}

	return b
}

// WithVaultNetwork sets vault-specific network configuration.
func (b *TestConfigBuilder) WithVaultNetwork() *TestConfigBuilder {
	b.cfg.Network = NetworkConfig{
		ID:          "",
		Name:        "",
		CIDR:        "",
		NetworkCIDR: "",
		SubnetID:    "",
		DNS:         nil,
	}

	return b
}

// WithVaultBastion sets vault-specific bastion configuration.
func (b *TestConfigBuilder) WithVaultBastion() *TestConfigBuilder {
	b.cfg.Bastion = Bastion{
		Flavor:     "",
		Image:      "",
		OS:         "",
		OSVersion:  "",
		Keypair:    "",
		SSHUser:    "",
		SSHOptions: "",
		SSHKeyDir:  "",
		SSHKeyName: "",
		Genesis: Genesis{
			Enabled:       false,
			Repo:          "",
			Branch:        "",
			Commit:        "",
			VersionPrefix: "",
		},
		Git: GitConfig{
			User: GitUser{
				Name:  "",
				Email: "",
			},
		},
		Tools: OverrideSets{
			Enable:  nil,
			Disable: nil,
		},
		CFPlugins: OverrideSets{
			Enable:  nil,
			Disable: nil,
		},
		Snaps: OverrideSets{
			Enable:  nil,
			Disable: nil,
		},
		ToolOverrides:     nil,
		CFPluginOverrides: nil,
		SnapOverrides:     nil,
	}

	return b
}

// WithVaultComponents sets vault-specific component configurations.
func (b *TestConfigBuilder) WithVaultComponents() *TestConfigBuilder {
	b.cfg.Routers = ComponentConfig{
		Flavor:   "",
		Image:    "",
		Count:    0,
		DiskSize: 0,
	}
	b.cfg.Cells = ComponentConfig{
		Flavor:   "",
		Image:    "",
		Count:    0,
		DiskSize: 0,
	}

	return b
}

// WithVaultBlobstore sets vault-specific blobstore configuration.
func (b *TestConfigBuilder) WithVaultBlobstore() *TestConfigBuilder {
	b.cfg.Blobstore = BlobstoreConfig{
		EnablePolicies: false,
		BoshBlobstore: BucketSettings{
			Versioning:     false,
			NoncurrentDays: 0,
		},
		CFBuildpacks: BucketSettings{
			Versioning:     false,
			NoncurrentDays: 0,
		},
		CFDroplets: BucketSettings{
			Versioning:     false,
			NoncurrentDays: 0,
		},
		CFAppPackages: BucketSettings{
			Versioning:     false,
			NoncurrentDays: 0,
		},
	}

	return b
}

// Build returns the built configuration.
func (b *TestConfigBuilder) Build() *Config {
	return b.cfg
}

// TestBastionConfig creates a bastion configuration with common test values.
func TestBastionConfig() Bastion {
	return Bastion{
		Flavor:     "",
		Image:      "",
		OS:         "",
		OSVersion:  "",
		Keypair:    "",
		SSHUser:    "ubuntu",
		SSHOptions: "",
		SSHKeyDir:  "",
		SSHKeyName: "",
		Genesis: Genesis{
			Enabled:       false,
			Repo:          "",
			Branch:        "",
			Commit:        "",
			VersionPrefix: "",
		},
		Git: GitConfig{
			User: GitUser{
				Name:  "Test User",
				Email: "test@example.com",
			},
		},
		Tools: OverrideSets{
			Enable:  nil,
			Disable: nil,
		},
		CFPlugins: OverrideSets{
			Enable:  nil,
			Disable: nil,
		},
		Snaps: OverrideSets{
			Enable:  nil,
			Disable: nil,
		},
		ToolOverrides:     map[string]ToolOverride{},
		CFPluginOverrides: map[string]CFPluginOverride{},
		SnapOverrides:     map[string]SnapOverride{},
	}
}
