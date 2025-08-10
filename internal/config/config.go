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

// Config represents the main configuration structure
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
	Blocs                 []Bloc                      `yaml:"blocs" mapstructure:"blocs"`
	Network               NetworkConfig               `yaml:"network" mapstructure:"network"`
	Bastion               Bastion                     `yaml:"bastion" mapstructure:"bastion"`
	Genesis               Genesis                     `yaml:"genesis" mapstructure:"genesis"`
	Deployment            Deployment                  `yaml:"deployments" mapstructure:"deployments"`
	DNS                   []string                    `yaml:"dns" mapstructure:"dns"`
	AZs                   map[string]AvailabilityZone `yaml:"azs" mapstructure:"azs"`
	SSHKeyStorageDir      string                      `yaml:"ssh_key_storage_dir" mapstructure:"ssh_key_storage_dir"`
	Routers               ComponentConfig             `yaml:"routers" mapstructure:"routers"`
	Cells                 ComponentConfig             `yaml:"cells" mapstructure:"cells"`
}

// Bloc represents a deployment bloc
type Bloc struct {
	Name        string            `yaml:"name" mapstructure:"name"`
	Provider    string            `yaml:"provider" mapstructure:"provider"`
	Type        string            `yaml:"type" mapstructure:"type"`
	Environment string            `yaml:"environment" mapstructure:"environment"`
	Network     Network           `yaml:"network" mapstructure:"network"`
	Subnets     []Subnet          `yaml:"subnets" mapstructure:"subnets"`
	Users       map[string]string `yaml:"users" mapstructure:"users"`
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

// Network represents a network (for bloc configuration)
type Network = NetworkConfig

// Subnet configuration
type Subnet struct {
	Name             string `yaml:"name" mapstructure:"name"`
	CIDR             string `yaml:"cidr" mapstructure:"cidr"`
	AvailabilityZone string `yaml:"availability_zone" mapstructure:"availability_zone"`
	Type             string `yaml:"type" mapstructure:"type"`
}

// Bastion configuration
type Bastion struct {
	Flavor     string `yaml:"flavor" mapstructure:"flavor"`
	Image      string `yaml:"image" mapstructure:"image"`
	OS         string `yaml:"os" mapstructure:"os"`
	OSVersion  string `yaml:"os_version" mapstructure:"os_version"`
	Keypair    string `yaml:"keypair" mapstructure:"keypair"`
	SSHUser    string `yaml:"ssh_user" mapstructure:"ssh_user"`
	SSHOptions string `yaml:"ssh_options" mapstructure:"ssh_options"`
	SSHKeyDir  string `yaml:"ssh_key_storage_dir" mapstructure:"ssh_key_storage_dir"`
	SSHKeyName string `yaml:"ssh_key_name" mapstructure:"ssh_key_name"`
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

// clearConfigCache clears the configuration cache (useful for testing)
func clearConfigCache() {
	configMutex.Lock()
	defer configMutex.Unlock()
	cachedConfigs = make(map[string]*cachedConfig)
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

	cfg := &Config{}

	// Apply provider defaults first
	if err := applyDefaults(cfg, viper.GetString("iaas")); err != nil {
		return nil, err
	}

	if configPath != "" {
		// Load from file
		if err := loadFromFile(configPath, cfg); err != nil {
			return nil, fmt.Errorf("failed to load config from %s: %w", configPath, err)
		}
	}

	// Override with viper values (from flags/env)
	if err := viper.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
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

	// Priority 2: Bloc name based config
	if blocName != "" {
		path := fmt.Sprintf("config/%s.yml", blocName)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	// Priority 3: Default bootstrap.yml
	if _, err := os.Stat("config/bootstrap.yml"); err == nil {
		return "config/bootstrap.yml"
	}

	return ""
}

// loadFromFile loads configuration from a YAML file
func loadFromFile(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
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
