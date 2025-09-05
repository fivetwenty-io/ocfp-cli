package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/security"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// ConfigFile represents the top-level configuration file structure.
type ConfigFile struct {
	Debug   bool               `mapstructure:"debug"   yaml:"debug"`
	Verbose bool               `mapstructure:"verbose" yaml:"verbose"`
	Blocs   map[string]*Config `mapstructure:"blocs"   yaml:"blocs"`
}

// Config represents a bloc configuration.
type Config struct {
	Name     string `mapstructure:"name"     yaml:"name"`
	Provider string `mapstructure:"provider" yaml:"provider"`
	IaaS     string `mapstructure:"iaas"     yaml:"iaas"`
	Region   string `mapstructure:"region"   yaml:"region"`
	// Prefer snake_case to match README and user configs
	ProjectID             string `mapstructure:"project_id"               yaml:"project_id"`
	OrgID                 string `mapstructure:"org_id"                   yaml:"org_id"`
	AuthToken             string `mapstructure:"auth_token"               yaml:"auth_token"`
	ServiceAccountToken   string `mapstructure:"service_account_token"    yaml:"service_account_token"`
	ServiceAccountJSON    string `mapstructure:"service_account_json"     yaml:"service_account_json"`
	ServiceAccountKeyPath string `mapstructure:"service_account_key_path" yaml:"service_account_key_path"`
	// Optional: override STACKIT API endpoint (e.g., https://iaas.api.stackit.cloud)
	APIEndpoint      string                      `mapstructure:"api_endpoint"        yaml:"api_endpoint"`
	AccessKeyID      string                      `mapstructure:"access_key_id"       yaml:"access_key_id"`
	SecretAccessKey  string                      `mapstructure:"secret_access_key"   yaml:"secret_access_key"`
	SubscriptionID   string                      `mapstructure:"subscription_id"     yaml:"subscription_id"`
	TenantID         string                      `mapstructure:"tenant_id"           yaml:"tenant_id"`
	ClientID         string                      `mapstructure:"client_id"           yaml:"client_id"`
	ClientSecret     string                      `mapstructure:"client_secret"       yaml:"client_secret"`
	AuthURL          string                      `mapstructure:"auth_url"            yaml:"auth_url"`
	Username         string                      `mapstructure:"username"            yaml:"username"`
	Password         string                      `mapstructure:"password"            yaml:"password"`
	ProjectName      string                      `mapstructure:"project_name"        yaml:"project_name"`
	DomainName       string                      `mapstructure:"domain_name"         yaml:"domain_name"`
	SessionToken     string                      `mapstructure:"session_token"       yaml:"session_token"`
	BastionIP        string                      `mapstructure:"bastion_ip"          yaml:"bastion_ip"`
	Network          NetworkConfig               `mapstructure:"network"             yaml:"network"`
	Bastion          Bastion                     `mapstructure:"bastion"             yaml:"bastion"`
	Genesis          Genesis                     `mapstructure:"genesis"             yaml:"genesis"`
	Deployment       Deployment                  `mapstructure:"deployments"         yaml:"deployments"`
	DNS              []string                    `mapstructure:"dns"                 yaml:"dns"`
	AZs              map[string]AvailabilityZone `mapstructure:"azs"                 yaml:"azs"`
	SSHKeyStorageDir string                      `mapstructure:"ssh_key_storage_dir" yaml:"ssh_key_storage_dir"`
	Routers          ComponentConfig             `mapstructure:"routers"             yaml:"routers"`
	Cells            ComponentConfig             `mapstructure:"cells"               yaml:"cells"`
	// Additional fields from the example config
	FQDNs             map[string]interface{} `mapstructure:"fqdns"               yaml:"fqdns"`
	S3                map[string]string      `mapstructure:"s3"                  yaml:"s3"`
	AllowedIngressIPs []string               `mapstructure:"allowed_ingress_ips" yaml:"allowed_ingress_ips"`
	Type              string                 `mapstructure:"type"                yaml:"type"`
	Environment       string                 `mapstructure:"environment"         yaml:"environment"`
	Subnets           []Subnet               `mapstructure:"subnets"             yaml:"subnets"`
	SubnetStrategy    string                 `mapstructure:"subnet_strategy"     yaml:"subnet_strategy"`
	LBs               map[string]LBService   `mapstructure:"lbs"                 yaml:"lbs"`
	Users             map[string]string      `mapstructure:"users"               yaml:"users"`
	// Public IP configurations
	RouterPublicIPs    int `mapstructure:"router_public_ips"     yaml:"router_public_ips"`
	CFSSHPublicIPs     int `mapstructure:"cf_ssh_public_ips"     yaml:"cf_ssh_public_ips"`
	JumpboxPublicIPs   int `mapstructure:"jumpbox_public_ips"    yaml:"jumpbox_public_ips"`
	TCPRouterPublicIPs int `mapstructure:"tcp_router_public_ips" yaml:"tcp_router_public_ips"`

	// Blobstore policies (optional, for object storage buckets)
	Blobstore BlobstoreConfig `mapstructure:"blobstore" yaml:"blobstore"`
}

// BlobstoreConfig controls versioning/lifecycle policies for expected buckets.
type BlobstoreConfig struct {
	EnablePolicies bool `mapstructure:"enablePolicies" yaml:"enablePolicies"`

	// Per-bucket overrides
	BoshBlobstore BucketSettings `mapstructure:"boshBlobstore" yaml:"boshBlobstore"`
	CFBuildpacks  BucketSettings `mapstructure:"cfBuildpacks"  yaml:"cfBuildpacks"`
	CFDroplets    BucketSettings `mapstructure:"cfDroplets"    yaml:"cfDroplets"`
	CFAppPackages BucketSettings `mapstructure:"cfAppPackages" yaml:"cfAppPackages"`
}

// BucketSettings specify data-plane policies.
type BucketSettings struct {
	Versioning     bool `mapstructure:"versioning"     yaml:"versioning"`
	NoncurrentDays int  `mapstructure:"noncurrentDays" yaml:"noncurrentDays"`
}

// NetworkConfig represents network configuration.
type NetworkConfig struct {
	ID          string   `mapstructure:"id"          yaml:"id"`
	Name        string   `mapstructure:"name"        yaml:"name"`
	CIDR        string   `mapstructure:"cidr"        yaml:"cidr"`
	NetworkCIDR string   `mapstructure:"networkCidr" yaml:"networkCidr"`
	SubnetID    string   `mapstructure:"subnetId"    yaml:"subnetId"`
	DNS         []string `mapstructure:"dns"         yaml:"dns"`
}

// Subnet configuration.
type Subnet struct {
	Name             string `mapstructure:"name"             yaml:"name"`
	CIDR             string `mapstructure:"cidr"             yaml:"cidr"`
	AvailabilityZone string `mapstructure:"availabilityZone" yaml:"availabilityZone"`
	Type             string `mapstructure:"type"             yaml:"type"`
}

// Bastion configuration.
type Bastion struct {
	Flavor     string    `mapstructure:"flavor"           yaml:"flavor"`
	Image      string    `mapstructure:"image"            yaml:"image"`
	OS         string    `mapstructure:"os"               yaml:"os"`
	OSVersion  string    `mapstructure:"osVersion"        yaml:"osVersion"`
	Keypair    string    `mapstructure:"keypair"          yaml:"keypair"`
	SSHUser    string    `mapstructure:"sshUser"          yaml:"sshUser"`
	SSHOptions string    `mapstructure:"sshOptions"       yaml:"sshOptions"`
	SSHKeyDir  string    `mapstructure:"sshKeyStorageDir" yaml:"sshKeyStorageDir"`
	SSHKeyName string    `mapstructure:"sshKeyName"       yaml:"sshKeyName"`
	Genesis    Genesis   `mapstructure:"genesis"          yaml:"genesis"`
	Git        GitConfig `mapstructure:"git"              yaml:"git"`
	// Optional overrides for tooling installation/selection
	Tools     OverrideSets `mapstructure:"tools"     yaml:"tools"`
	CFPlugins OverrideSets `mapstructure:"cfPlugins" yaml:"cfPlugins"`
	Snaps     OverrideSets `mapstructure:"snaps"     yaml:"snaps"`
	// Per-item override maps (by name)
	ToolOverrides     map[string]ToolOverride     `mapstructure:"toolOverrides"     yaml:"toolOverrides"`
	CFPluginOverrides map[string]CFPluginOverride `mapstructure:"cfPluginOverrides" yaml:"cfPluginOverrides"`
	SnapOverrides     map[string]SnapOverride     `mapstructure:"snapOverrides"     yaml:"snapOverrides"`
}

// ComponentConfig represents configuration for CF components.
type ComponentConfig struct {
	Flavor   string `mapstructure:"flavor"   yaml:"flavor"`
	Image    string `mapstructure:"image"    yaml:"image"`
	Count    int    `mapstructure:"count"    yaml:"count"`
	DiskSize int    `mapstructure:"diskSize" yaml:"diskSize"`
}

// Genesis configuration.
type Genesis struct {
	Enabled       bool   `mapstructure:"enabled"       yaml:"enabled"`
	Repo          string `mapstructure:"repo"          yaml:"repo"`
	Branch        string `mapstructure:"branch"        yaml:"branch"`
	Commit        string `mapstructure:"commit"        yaml:"commit"`
	VersionPrefix string `mapstructure:"versionPrefix" yaml:"versionPrefix"`
}

// GitConfig represents Git configuration for the bastion.
type GitConfig struct {
	User GitUser `mapstructure:"user" yaml:"user"`
}

// GitUser represents Git user configuration.
type GitUser struct {
	Name  string `mapstructure:"name"  yaml:"name"`
	Email string `mapstructure:"email" yaml:"email"`
}

// OverrideSets allows enabling or disabling named items via config.
type OverrideSets struct {
	Enable  []string `mapstructure:"enable"  yaml:"enable"`
	Disable []string `mapstructure:"disable" yaml:"disable"`
}

// ToolOverride allows overriding advanced tool properties.
type ToolOverride struct {
	URL            string `mapstructure:"url"            yaml:"url"`
	Version        string `mapstructure:"version"        yaml:"version"`
	VersionURL     string `mapstructure:"versionUrl"     yaml:"versionUrl"`
	VersionPattern string `mapstructure:"versionPattern" yaml:"versionPattern"`
	URLTemplate    string `mapstructure:"urlTemplate"    yaml:"urlTemplate"`
	Dest           string `mapstructure:"dest"           yaml:"dest"`
	Mode           uint32 `mapstructure:"mode"           yaml:"mode"`
	Sudo           *bool  `mapstructure:"sudo"           yaml:"sudo"`
	Extract        *bool  `mapstructure:"extract"        yaml:"extract"`
	InstallCommand string `mapstructure:"installCommand" yaml:"installCommand"`
	InstallScript  string `mapstructure:"installScript"  yaml:"installScript"`
	VerifyCommand  string `mapstructure:"verifyCommand"  yaml:"verifyCommand"`
	PathAddition   string `mapstructure:"pathAddition"   yaml:"pathAddition"`
	Cleanup        string `mapstructure:"cleanup"        yaml:"cleanup"`
}

// CFPluginOverride allows overriding CF plugin properties.
type CFPluginOverride struct {
	GitHubRepo string `mapstructure:"githubRepo" yaml:"githubRepo"`
	Version    string `mapstructure:"version"    yaml:"version"`
	Repo       string `mapstructure:"repo"       yaml:"repo"`
	RepoURL    string `mapstructure:"repoUrl"    yaml:"repoUrl"`
	Force      *bool  `mapstructure:"force"      yaml:"force"`
}

// SnapOverride allows overriding snap package properties.
type SnapOverride struct {
	Channel      string `mapstructure:"channel"      yaml:"channel"`
	Classic      *bool  `mapstructure:"classic"      yaml:"classic"`
	DevMode      *bool  `mapstructure:"devMode"      yaml:"devMode"`
	Dangerous    *bool  `mapstructure:"dangerous"    yaml:"dangerous"`
	CheckCommand string `mapstructure:"checkCommand" yaml:"checkCommand"`
}

// Deployment configuration.
type Deployment struct {
	HierarchyFiles      bool `mapstructure:"hierarchyFiles"      yaml:"hierarchyFiles"`
	HierarchyVaultPaths bool `mapstructure:"hierarchyVaultPaths" yaml:"hierarchyVaultPaths"`
}

// AvailabilityZone configuration.
type AvailabilityZone struct {
	Zone            string `mapstructure:"zone"            yaml:"zone"`
	CloudProperties string `mapstructure:"cloudProperties" yaml:"cloudProperties"`
}

// Configuration caching for performance optimization.
var (
	configMutex   sync.RWMutex
	cachedConfigs = make(map[string]*cachedConfig)
)

type cachedConfig struct {
	config   *Config
	modTime  time.Time
	filePath string
}

// Load loads configuration with default parameters.
func Load() (*Config, error) {
	return LoadWithParams("", "")
}

// LoadWithParams loads configuration from file or defaults.
func LoadWithParams(configFile string, blocName string) (*Config, error) {
	// Determine config file path
	configPath := determineConfigPath(configFile, blocName)

	// Check cache first if we have a config file
	if configPath != "" {
		configMutex.RLock()

		cacheKey := configPath + ":" + blocName
		if cached, exists := cachedConfigs[cacheKey]; exists {
			// Check if file has been modified since caching
			if stat, err := os.Stat(configPath); err == nil {
				if stat.ModTime().Equal(cached.modTime) {
					configMutex.RUnlock()

					return cached.config, nil
				}
			}
		}

		configMutex.RUnlock()
	}

	var cfg *Config

	if configPath != "" {
		// Load the entire config file
		configFileData := &ConfigFile{}
		err := loadFromFile(configPath, configFileData)
		if err != nil {
			return nil, fmt.Errorf("failed to load config from %s: %w", configPath, err)
		}

		// Find the matching bloc
		if blocConfig, exists := configFileData.Blocs[blocName]; exists {
			cfg = blocConfig
			// Ensure the bloc name is set
			if cfg.Name == "" {
				cfg.Name = blocName
			}
		}

		if cfg == nil {
			return nil, fmt.Errorf("bloc '%s' not found in configuration file %s", blocName, configPath)
		}
	} else {
		// No config file, create empty config
		cfg = &Config{}
	}

	// Apply provider defaults
	provider := cfg.Provider
	if provider == "" {
		provider = cfg.IaaS
	}

	if provider == "" {
		provider = viper.GetString("iaas")
	}
	err := applyDefaults(cfg, provider)

	if err != nil {
		return nil, err
	}

	// Override with viper values (from flags/env) for specific fields only
	if viper.GetString("iaas") != "" {
		cfg.IaaS = viper.GetString("iaas")
	}

	if viper.GetString("provider") != "" {
		cfg.Provider = viper.GetString("provider")
	}

	if viper.GetString("region") != "" {
		cfg.Region = viper.GetString("region")
	}

	// Validate configuration
	err := validate(cfg)
	if err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	// Cache the configuration for future use
	if configPath != "" {
		configMutex.Lock()

		cacheKey := configPath + ":" + blocName
		if stat, err := os.Stat(configPath); err == nil {
			cachedConfigs[cacheKey] = &cachedConfig{
				config:   cfg,
				modTime:  stat.ModTime(),
				filePath: configPath,
			}
		}

		configMutex.Unlock()
	}

	return cfg, nil
}

// determineConfigPath determines the configuration file path.
func determineConfigPath(configFile string, blocName string) string {
	// Priority 1: Explicit config file
	if configFile != "" {
		return configFile
	}

	// Priority 2: Default config file at ~/.ocfp/config.yml
	homeDir, err := os.UserHomeDir()
	if err == nil {
		defaultPath := filepath.Join(homeDir, ".ocfp", "config.yml")
		if _, err := os.Stat(defaultPath); err == nil {
			return defaultPath
		}
	}

	// Priority 3: Check in local config/config.yml
	if _, err := os.Stat("config/config.yml"); err == nil {
		return "config/config.yml"
	}

	return ""
}

// loadFromFile loads configuration from a YAML file.
func loadFromFile(path string, target interface{}) error {
	if err := security.ValidateConfigPath(path); err != nil {
		return fmt.Errorf("invalid config path: %w", err)
	}

	data, err := os.ReadFile(path) // #nosec G304 - path is validated above
	if err != nil {
		return err
	}

	if err := yaml.Unmarshal(data, target); err != nil {
		return err
	}

	return nil
}

// applyDefaults applies provider-specific defaults.
func applyDefaults(cfg *Config, provider string) error {
	switch strings.ToLower(provider) {
	case "stackit":
		applyStackitDefaults(cfg)
	case "openstack":
		applyOpenStackDefaults(cfg)
	case "aws":
		applyAWSDefaults(cfg)
	case "azure":
		applyAzureDefaults(cfg)
	case "gcp":
		applyGCPDefaults(cfg)
	}

	// Apply common defaults
	if cfg.Bastion.SSHUser == "" {
		cfg.Bastion.SSHUser = "ubuntu"
	}

	if cfg.Genesis.Enabled && cfg.Genesis.Branch == "" {
		cfg.Genesis.Branch = "v3.1.x-dev"
	}

	if cfg.Genesis.VersionPrefix == "" {
		cfg.Genesis.VersionPrefix = "3.1.0"
	}

	return nil
}

// applyStackitDefaults applies STACKIT-specific defaults.
func applyStackitDefaults(cfg *Config) {
	// Default region for STACKIT if not specified via config or flags
	if cfg.Region == "" {
		cfg.Region = "eu01"
	}

	if cfg.Network.CIDR == "" && cfg.Network.NetworkCIDR == "" {
		cfg.Network.NetworkCIDR = "10.4.0.0/20"
	}

	if len(cfg.DNS) == 0 {
		cfg.DNS = []string{"1.1.1.1", "8.8.8.8"}
	}

	if cfg.Bastion.Flavor == "" {
		cfg.Bastion.Flavor = "m1a.2d"
	}

	if cfg.Bastion.Image == "" && cfg.Bastion.OS != "" && cfg.Bastion.OSVersion != "" {
		cfg.Bastion.Image = fmt.Sprintf("%s %s", cfg.Bastion.OS, cfg.Bastion.OSVersion)
	} else if cfg.Bastion.Image == "" {
		cfg.Bastion.Image = "Ubuntu 22.04"
		cfg.Bastion.OS = "Ubuntu"
		cfg.Bastion.OSVersion = "22.04"
	}

	// Default availability zones for STACKIT
	if len(cfg.AZs) == 0 {
		cfg.AZs = map[string]AvailabilityZone{
			"eu01-1": {CloudProperties: `{"availability_zone": "eu01-1"}`},
			"eu01-2": {CloudProperties: `{"availability_zone": "eu01-2"}`},
			"eu01-3": {CloudProperties: `{"availability_zone": "eu01-3"}`},
		}

		// No default lifecycle/versioning; leave disabled unless configured
	}
}

// applyOpenStackDefaults applies OpenStack-specific defaults.
func applyOpenStackDefaults(cfg *Config) {
	if cfg.Network.CIDR == "" && cfg.Network.NetworkCIDR == "" {
		cfg.Network.NetworkCIDR = "10.0.0.0/16"
	}

	if cfg.Bastion.Flavor == "" {
		cfg.Bastion.Flavor = "t1.small"
	}
}

// applyAWSDefaults applies AWS-specific defaults.
func applyAWSDefaults(cfg *Config) {
	if cfg.Network.CIDR == "" && cfg.Network.NetworkCIDR == "" {
		cfg.Network.NetworkCIDR = "10.0.0.0/16"
	}

	if cfg.Bastion.Flavor == "" {
		cfg.Bastion.Flavor = "t3.small"
	}
}

// applyAzureDefaults applies Azure-specific defaults.
func applyAzureDefaults(cfg *Config) {
	if cfg.Network.CIDR == "" && cfg.Network.NetworkCIDR == "" {
		cfg.Network.NetworkCIDR = "10.0.0.0/16"
	}

	if cfg.Bastion.Flavor == "" {
		cfg.Bastion.Flavor = "Standard_B2s"
	}
}

// applyGCPDefaults applies GCP-specific defaults.
func applyGCPDefaults(cfg *Config) {
	if cfg.Network.CIDR == "" && cfg.Network.NetworkCIDR == "" {
		cfg.Network.NetworkCIDR = "10.0.0.0/16"
	}

	if cfg.Bastion.Flavor == "" {
		cfg.Bastion.Flavor = "e2-small"
	}
}

// validate validates the configuration.
func validate(cfg *Config) error {
	// Required fields
	if cfg.Provider == "" && cfg.IaaS == "" {
		return errors.New("provider or iaas must be specified")
	}

	// Set provider from iaas if not set
	if cfg.Provider == "" {
		cfg.Provider = cfg.IaaS
	}

	if cfg.IaaS == "" {
		cfg.IaaS = cfg.Provider
	}

	// Validate provider
	validProviders := []string{"stackit", "openstack", "aws", "azure", "gcp", "vmware"}
	providerValid := false

	for _, p := range validProviders {
		if strings.EqualFold(cfg.Provider, p) {
			providerValid = true

			break
		}
	}

	if !providerValid {
		return fmt.Errorf("invalid provider: %s", cfg.Provider)
	}

	return nil
}

// GetLogDir returns the log directory path.
func GetLogDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return filepath.Join(home, ".ocfp", "logs")
}

// GetSSHKeyPath returns the SSH key path for a bloc.
func GetSSHKeyPath(blocName string, keypair string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	// Try new standard location first
	newPath := filepath.Join(home, ".ocfp", "keys", blocName+"-bastion", "id_rsa")
	if _, err := os.Stat(newPath); err == nil {
		return newPath
	}

	// Try legacy location
	legacyPath := filepath.Join(home, ".ssh", fmt.Sprintf("%s-%s", blocName, keypair))
	if _, err := os.Stat(legacyPath); err == nil {
		return legacyPath
	}

	return ""
}

// LBService describes a desired load balancer and its backend targets.
type LBService struct {
	Protocol string   `mapstructure:"protocol" yaml:"protocol"`
	Port     int      `mapstructure:"port"     yaml:"port"`
	Targets  []string `mapstructure:"targets"  yaml:"targets"`
}
