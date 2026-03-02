package provision

// SystemConfig holds system-level configuration.
type SystemConfig struct {
	Hostname    HostnameConfig `yaml:"hostname"`
	WaitTime    int            `yaml:"waitTime"`
	Timezone    string         `yaml:"timezone"`
	Locale      string         `yaml:"locale"`
	UpdateCache bool           `yaml:"updateCache"`
}

// HostnameConfig configures hostname settings.
type HostnameConfig struct {
	Enabled bool   `yaml:"enabled"`
	Pattern string `yaml:"pattern"`
}

// DirectoryConfig defines a directory to create.
type DirectoryConfig struct {
	Path      string `yaml:"path"`
	Mode      uint32 `yaml:"mode"`
	Owner     string `yaml:"owner"`
	Group     string `yaml:"group"`
	Condition string `yaml:"condition"`
}

// PackageGroup defines a group of packages to install.
type PackageGroup struct {
	Enabled     bool     `yaml:"enabled"`
	Condition   string   `yaml:"condition"`
	DependsOn   []string `yaml:"dependsOn"`
	Packages    []string `yaml:"packages"`
	PipPackages []string `yaml:"pipPackages"`
	Verify      []string `yaml:"verify"`
	PostInstall string   `yaml:"postInstall"`
}

// GenesisDeployment defines a Genesis deployment to initialize.
type GenesisDeployment struct {
	Name      string `yaml:"name"`
	Kit       string `yaml:"kit"`
	Repo      string `yaml:"repo"`
	Branch    string `yaml:"branch"`
	Enabled   bool   `yaml:"enabled"`
	Condition string `yaml:"condition"`
}

// BinaryTool defines a binary tool to download and install.
type BinaryTool struct {
	Name           string `yaml:"name"`
	Enabled        bool   `yaml:"enabled"`
	Condition      string `yaml:"condition"`
	URL            string `yaml:"url"`
	VersionURL     string `yaml:"versionUrl"`
	VersionPattern string `yaml:"versionPattern"`
	URLTemplate    string `yaml:"urlTemplate"`
	Dest           string `yaml:"dest"`
	Mode           uint32 `yaml:"mode"`
	Extract        bool   `yaml:"extract"`
	InstallCommand string `yaml:"installCommand"`
	Verify         string `yaml:"verify"`
	Sudo           bool   `yaml:"sudo"`
}

// APTRepository defines an APT repository to add.
type APTRepository struct {
	Name       string `yaml:"name"`
	Enabled    bool   `yaml:"enabled"`
	Condition  string `yaml:"condition"`
	GPGKey     GPGKey `yaml:"gpgKey"`
	SourceLine string `yaml:"sourceLine"`
	SourceFile string `yaml:"sourceFile"`
}

// GPGKey defines GPG key configuration.
type GPGKey struct {
	URL     string `yaml:"url"`
	Dest    string `yaml:"dest"`
	Dearmor bool   `yaml:"dearmor"`
}

// GitRepository defines a Git repository to clone.
type GitRepository struct {
	Name      string `yaml:"name"`
	Enabled   bool   `yaml:"enabled"`
	Condition string `yaml:"condition"`
	URL       string `yaml:"url"`
	Branch    string `yaml:"branch"`
	Commit    string `yaml:"commit"`
	Dest      string `yaml:"dest"`
	Depth     int    `yaml:"depth"`
}

// CustomScript defines a custom script to execute.
type CustomScript struct {
	Name      string `yaml:"name"`
	Enabled   bool   `yaml:"enabled"`
	Condition string `yaml:"condition"`
	Content   string `yaml:"content"`
	Path      string `yaml:"path"`
	Mode      uint32 `yaml:"mode"`
	Execute   bool   `yaml:"execute"`
}

// ProvisionConfig defines the interface for provisioning configuration.
type ProvisionConfig interface {
	GetSystemConfig() SystemConfig
	GetDirectories() []DirectoryConfig
	GetPackages() map[string]PackageGroup
	GetGenesisDeployments() []GenesisDeployment
	GetBinaryTools() []BinaryTool
	GetAPTRepositories() []APTRepository
	GetGitRepositories() []GitRepository
	GetCustomScripts() []CustomScript
}
