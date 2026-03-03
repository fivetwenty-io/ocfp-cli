package bastion

import (
	"context"
	"io"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
)

// BastionInitializer defines the interface for bastion initialization.
type BastionInitializer interface {
	Validate() error
	PrepareEnvironment() map[string]string
	GetConnectionDetails() (*ConnectionDetails, error)
	Initialize(ctx context.Context) error
}

// SSHClient defines the interface for SSH operations.
type SSHClient interface {
	Connect(ctx context.Context) error
	ExecuteCommand(ctx context.Context, cmd string) (*ssh.CommandResult, error)
	TransferFile(ctx context.Context, local, remote string, opts ssh.TransferOptions) error
	CreateTunnel(ctx context.Context, localPort, remotePort int) error
	Close() error
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

// ConnectionDetails holds SSH connection information.
type ConnectionDetails struct {
	Host           string
	Port           int
	User           string
	PrivateKeyPath string
	Password       string //nolint:gosec // field name is descriptive, not a hardcoded secret
	SSHOptions     []string
	UseSSHPass     bool
}

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

// ProvisioningProgress tracks the progress of bastion provisioning.
type ProvisioningProgress struct {
	TotalSteps     int
	CompletedSteps int
	CurrentStep    string
	StartTime      time.Time
	Errors         []error
	Checkpoints    map[string]bool
}

// ProvisioningOptions configures bastion provisioning behavior.
type ProvisioningOptions struct {
	DryRun          bool
	Force           bool
	Parallel        bool
	Resume          bool
	Verbose         bool
	MaxWorkers      int
	ProgressOut     io.Writer
	LogFile         string
	OCFPOnly        bool
	ConfigOnly      bool
	GenesisOnly     bool // Install/update only Genesis and related components
	RebootAfterInit bool // Reboot bastion after successful initialization to apply updates
}
