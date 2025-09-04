package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// ConfigFile represents the top-level configuration file structure
type ConfigFile struct {
	Debug   bool               `yaml:"debug" mapstructure:"debug"`
	Verbose bool               `yaml:"verbose" mapstructure:"verbose"`
	Blocs   map[string]*Config `yaml:"blocs" mapstructure:"blocs"`
}

// Config represents a bloc configuration
type Config struct {
	Name                  string                      `yaml:"name" mapstructure:"name"`
	Provider              string                      `yaml:"provider" mapstructure:"provider"`
	IaaS                  string                      `yaml:"iaas" mapstructure:"iaas"`
	Region                string                      `yaml:"region" mapstructure:"region"`
	ProjectID             string                      `yaml:"project_id" mapstructure:"project_id"`
	OrgID                 string                      `yaml:"org_id" mapstructure:"org_id"`
	AuthToken             string                      `yaml:"auth_token" mapstructure:"auth_token"`
	ServiceAccountToken   string                      `yaml:"service_account_token" mapstructure:"service_account_token"`
	ServiceAccountJSON    string                      `yaml:"service_account_json" mapstructure:"service_account_json"`
	ServiceAccountKeyPath string                      `yaml:"service_account_key_path" mapstructure:"service_account_key_path"`
	AccessKeyID           string                      `yaml:"access_key_id" mapstructure:"access_key_id"`
	SecretAccessKey       string                      `yaml:"secret_access_key" mapstructure:"secret_access_key"`
	SubscriptionID        string                      `yaml:"subscription_id" mapstructure:"subscription_id"`
	TenantID              string                      `yaml:"tenant_id" mapstructure:"tenant_id"`
	ClientID              string                      `yaml:"client_id" mapstructure:"client_id"`
	ClientSecret          string                      `yaml:"client_secret" mapstructure:"client_secret"`
	AuthURL               string                      `yaml:"auth_url" mapstructure:"auth_url"`
	Username              string                      `yaml:"username" mapstructure:"username"`
	Password              string                      `yaml:"password" mapstructure:"password"`
	ProjectName           string                      `yaml:"project_name" mapstructure:"project_name"`
	DomainName            string                      `yaml:"domain_name" mapstructure:"domain_name"`
	SessionToken          string                      `yaml:"session_token" mapstructure:"session_token"`
	BastionIP             string                      `yaml:"bastion_ip" mapstructure:"bastion_ip"`
	Network               NetworkConfig               `yaml:"network" mapstructure:"network"`
	Bastion               Bastion                     `yaml:"bastion" mapstructure:"bastion"`
	Genesis               Genesis                     `yaml:"genesis" mapstructure:"genesis"`
	Deployment            Deployment                  `yaml:"deployments" mapstructure:"deployments"`
	DNS                   []string                    `yaml:"dns" mapstructure:"dns"`
	AZs                   map[string]AvailabilityZone `yaml:"azs" mapstructure:"azs"`
	SSHKeyStorageDir      string                      `yaml:"ssh_key_storage_dir" mapstructure:"ssh_key_storage_dir"`
	Routers               ComponentConfig             `yaml:"routers" mapstructure:"routers"`
	Cells                 ComponentConfig             `yaml:"cells" mapstructure:"cells"`
	// Additional fields from the example config
	FQDNs             map[string]interface{} `yaml:"fqdns" mapstructure:"fqdns"`
	S3                map[string]string      `yaml:"s3" mapstructure:"s3"`
	AllowedIngressIPs []string               `yaml:"allowed_ingress_ips" mapstructure:"allowed_ingress_ips"`
	Type              string                 `yaml:"type" mapstructure:"type"`
	Environment       string                 `yaml:"environment" mapstructure:"environment"`
	Subnets           []Subnet               `yaml:"subnets" mapstructure:"subnets"`
	SubnetStrategy    string                 `yaml:"subnet_strategy" mapstructure:"subnet_strategy"`
	LBs               map[string]LBService   `yaml:"lbs" mapstructure:"lbs"`
	Users             map[string]string      `yaml:"users" mapstructure:"users"`
	// Public IP configurations
	RouterPublicIPs    int `yaml:"router_public_ips" mapstructure:"router_public_ips"`
	CFSSHPublicIPs     int `yaml:"cf_ssh_public_ips" mapstructure:"cf_ssh_public_ips"`
	JumpboxPublicIPs   int `yaml:"jumpbox_public_ips" mapstructure:"jumpbox_public_ips"`
	TCPRouterPublicIPs int `yaml:"tcp_router_public_ips" mapstructure:"tcp_router_public_ips"`

	// Blobstore policies (optional, for object storage buckets)
	Blobstore BlobstoreConfig `yaml:"blobstore" mapstructure:"blobstore"`
}

// BlobstoreConfig controls versioning/lifecycle policies for expected buckets
type BlobstoreConfig struct {
	EnablePolicies bool `yaml:"enable_policies" mapstructure:"enable_policies"`

	// Per-bucket overrides
	BoshBlobstore BucketSettings `yaml:"bosh_blobstore" mapstructure:"bosh_blobstore"`
	CFBuildpacks  BucketSettings `yaml:"cf_buildpacks" mapstructure:"cf_buildpacks"`
	CFDroplets    BucketSettings `yaml:"cf_droplets" mapstructure:"cf_droplets"`
	CFAppPackages BucketSettings `yaml:"cf_app_packages" mapstructure:"cf_app_packages"`
}

// BucketSettings specify data-plane policies
type BucketSettings struct {
	Versioning     bool `yaml:"versioning" mapstructure:"versioning"`
	NoncurrentDays int  `yaml:"noncurrent_days" mapstructure:"noncurrent_days"`
}

// NetworkConfig represents network configuration
type NetworkConfig struct {
	ID          string   `yaml:"id" mapstructure:"id"`
	Name        string   `yaml:"name" mapstructure:"name"`
	CIDR        string   `yaml:"cidr" mapstructure:"cidr"`
	NetworkCIDR string   `yaml:"network_cidr" mapstructure:"network_cidr"`
	SubnetID    string   `yaml:"subnet_id" mapstructure:"subnet_id"`
	DNS         []string `yaml:"dns" mapstructure:"dns"`
}

// Subnet configuration
type Subnet struct {
	Name             string `yaml:"name" mapstructure:"name"`
	CIDR             string `yaml:"cidr" mapstructure:"cidr"`
	AvailabilityZone string `yaml:"availability_zone" mapstructure:"availability_zone"`
	Type             string `yaml:"type" mapstructure:"type"`
}

// Bastion configuration
type Bastion struct {
	Flavor     string    `yaml:"flavor" mapstructure:"flavor"`
	Image      string    `yaml:"image" mapstructure:"image"`
	OS         string    `yaml:"os" mapstructure:"os"`
	OSVersion  string    `yaml:"os_version" mapstructure:"os_version"`
	Keypair    string    `yaml:"keypair" mapstructure:"keypair"`
	SSHUser    string    `yaml:"ssh_user" mapstructure:"ssh_user"`
	SSHOptions string    `yaml:"ssh_options" mapstructure:"ssh_options"`
	SSHKeyDir  string    `yaml:"ssh_key_storage_dir" mapstructure:"ssh_key_storage_dir"`
	SSHKeyName string    `yaml:"ssh_key_name" mapstructure:"ssh_key_name"`
	Genesis    Genesis   `yaml:"genesis" mapstructure:"genesis"`
	Git        GitConfig `yaml:"git" mapstructure:"git"`
	// Optional overrides for tooling installation/selection
	Tools     OverrideSets `yaml:"tools" mapstructure:"tools"`
	CFPlugins OverrideSets `yaml:"cf_plugins" mapstructure:"cf_plugins"`
	Snaps     OverrideSets `yaml:"snaps" mapstructure:"snaps"`
	// Per-item override maps (by name)
	ToolOverrides     map[string]ToolOverride     `yaml:"tool_overrides" mapstructure:"tool_overrides"`
	CFPluginOverrides map[string]CFPluginOverride `yaml:"cf_plugin_overrides" mapstructure:"cf_plugin_overrides"`
	SnapOverrides     map[string]SnapOverride     `yaml:"snap_overrides" mapstructure:"snap_overrides"`
}

// ComponentConfig represents configuration for CF components
type ComponentConfig struct {
	Flavor   string `yaml:"flavor" mapstructure:"flavor"`
	Image    string `yaml:"image" mapstructure:"image"`
	Count    int    `yaml:"count" mapstructure:"count"`
	DiskSize int    `yaml:"disk_size" mapstructure:"disk_size"`
}

// Genesis configuration
type Genesis struct {
	Enabled       bool   `yaml:"enabled" mapstructure:"enabled"`
	Repo          string `yaml:"repo" mapstructure:"repo"`
	Branch        string `yaml:"branch" mapstructure:"branch"`
	Commit        string `yaml:"commit" mapstructure:"commit"`
	VersionPrefix string `yaml:"version_prefix" mapstructure:"version_prefix"`
}

// GitConfig represents Git configuration for the bastion
type GitConfig struct {
	User GitUser `yaml:"user" mapstructure:"user"`
}

// GitUser represents Git user configuration
type GitUser struct {
	Name  string `yaml:"name" mapstructure:"name"`
	Email string `yaml:"email" mapstructure:"email"`
}

// OverrideSets allows enabling or disabling named items via config
type OverrideSets struct {
	Enable  []string `yaml:"enable" mapstructure:"enable"`
	Disable []string `yaml:"disable" mapstructure:"disable"`
}

// ToolOverride allows overriding advanced tool properties
type ToolOverride struct {
	URL            string `yaml:"url" mapstructure:"url"`
	Version        string `yaml:"version" mapstructure:"version"`
	VersionURL     string `yaml:"version_url" mapstructure:"version_url"`
	VersionPattern string `yaml:"version_pattern" mapstructure:"version_pattern"`
	URLTemplate    string `yaml:"url_template" mapstructure:"url_template"`
	Dest           string `yaml:"dest" mapstructure:"dest"`
	Mode           uint32 `yaml:"mode" mapstructure:"mode"`
	Sudo           *bool  `yaml:"sudo" mapstructure:"sudo"`
	Extract        *bool  `yaml:"extract" mapstructure:"extract"`
	InstallCommand string `yaml:"install_command" mapstructure:"install_command"`
	InstallScript  string `yaml:"install_script" mapstructure:"install_script"`
	VerifyCommand  string `yaml:"verify_command" mapstructure:"verify_command"`
	PathAddition   string `yaml:"path_addition" mapstructure:"path_addition"`
	Cleanup        string `yaml:"cleanup" mapstructure:"cleanup"`
}

// CFPluginOverride allows overriding CF plugin properties
type CFPluginOverride struct {
	GitHubRepo string `yaml:"github_repo" mapstructure:"github_repo"`
	Version    string `yaml:"version" mapstructure:"version"`
	Repo       string `yaml:"repo" mapstructure:"repo"`
	RepoURL    string `yaml:"repo_url" mapstructure:"repo_url"`
	Force      *bool  `yaml:"force" mapstructure:"force"`
}

// SnapOverride allows overriding snap package properties
type SnapOverride struct {
	Channel      string `yaml:"channel" mapstructure:"channel"`
	Classic      *bool  `yaml:"classic" mapstructure:"classic"`
	DevMode      *bool  `yaml:"devmode" mapstructure:"devmode"`
	Dangerous    *bool  `yaml:"dangerous" mapstructure:"dangerous"`
	CheckCommand string `yaml:"check_command" mapstructure:"check_command"`
}

// Deployment configuration
type Deployment struct {
	HierarchyFiles      bool `yaml:"hierarchy_files" mapstructure:"hierarchy_files"`
	HierarchyVaultPaths bool `yaml:"hierarchy_vault_paths" mapstructure:"hierarchy_vault_paths"`
}

// AvailabilityZone configuration
type AvailabilityZone struct {
	Zone            string `yaml:"zone" mapstructure:"zone"`
	CloudProperties string `yaml:"cloud_properties" mapstructure:"cloud_properties"`
}

// Configuration caching for performance optimization
var (
	configMutex   sync.RWMutex
	cachedConfigs = make(map[string]*cachedConfig)
)

type cachedConfig struct {
	config   *Config
	modTime  time.Time
	filePath string
}

// Load loads configuration with default parameters
func Load() (*Config, error) {
	return LoadWithParams("", "")
}

// LoadWithParams loads configuration from file or defaults
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
		if err := loadFromFile(configPath, configFileData); err != nil {
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
	if err := applyDefaults(cfg, provider); err != nil {
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
	if err := validate(cfg); err != nil {
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

// determineConfigPath determines the configuration file path
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

// loadFromFile loads configuration from a YAML file
func loadFromFile(path string, target interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	if err := yaml.Unmarshal(data, target); err != nil {
		return err
	}

	return nil
}

// applyDefaults applies provider-specific defaults
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

// applyStackitDefaults applies STACKIT-specific defaults
func applyStackitDefaults(cfg *Config) {
	if cfg.Network.CIDR == "" && cfg.Network.NetworkCIDR == "" {
		cfg.Network.NetworkCIDR = "10.4.0.0/20"
	}

	if len(cfg.DNS) == 0 {
		cfg.DNS = []string{"1.1.1.1", "8.8.8.8"}
	}

	if cfg.Bastion.Flavor == "" {
		cfg.Bastion.Flavor = "m1.2"
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

// applyOpenStackDefaults applies OpenStack-specific defaults
func applyOpenStackDefaults(cfg *Config) {
	if cfg.Network.CIDR == "" && cfg.Network.NetworkCIDR == "" {
		cfg.Network.NetworkCIDR = "10.0.0.0/16"
	}

	if cfg.Bastion.Flavor == "" {
		cfg.Bastion.Flavor = "t1.small"
	}
}

// applyAWSDefaults applies AWS-specific defaults
func applyAWSDefaults(cfg *Config) {
	if cfg.Network.CIDR == "" && cfg.Network.NetworkCIDR == "" {
		cfg.Network.NetworkCIDR = "10.0.0.0/16"
	}

	if cfg.Bastion.Flavor == "" {
		cfg.Bastion.Flavor = "t3.small"
	}
}

// applyAzureDefaults applies Azure-specific defaults
func applyAzureDefaults(cfg *Config) {
	if cfg.Network.CIDR == "" && cfg.Network.NetworkCIDR == "" {
		cfg.Network.NetworkCIDR = "10.0.0.0/16"
	}

	if cfg.Bastion.Flavor == "" {
		cfg.Bastion.Flavor = "Standard_B2s"
	}
}

// applyGCPDefaults applies GCP-specific defaults
func applyGCPDefaults(cfg *Config) {
	if cfg.Network.CIDR == "" && cfg.Network.NetworkCIDR == "" {
		cfg.Network.NetworkCIDR = "10.0.0.0/16"
	}

	if cfg.Bastion.Flavor == "" {
		cfg.Bastion.Flavor = "e2-small"
	}
}

// validate validates the configuration
func validate(cfg *Config) error {
	// Required fields
	if cfg.Provider == "" && cfg.IaaS == "" {
		return fmt.Errorf("provider or iaas must be specified")
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

// GetLogDir returns the log directory path
func GetLogDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ocfp", "logs")
}

// GetSSHKeyPath returns the SSH key path for a bloc
func GetSSHKeyPath(blocName string, keypair string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	// Try new standard location first
	newPath := filepath.Join(home, ".ocfp", "keys", fmt.Sprintf("%s-bastion", blocName), "id_rsa")
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

// LBService describes a desired load balancer and its backend targets
type LBService struct {
	Protocol string   `yaml:"protocol" mapstructure:"protocol"`
	Port     int      `yaml:"port" mapstructure:"port"`
	Targets  []string `yaml:"targets" mapstructure:"targets"`
}
