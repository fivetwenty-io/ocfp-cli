package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	Name     string `json:"name"     mapstructure:"name"     yaml:"name"`
	Provider string `json:"provider" mapstructure:"provider" yaml:"provider"`
	IaaS     string `json:"iaas"     mapstructure:"iaas"     yaml:"iaas"`
	Region   string `json:"region"   mapstructure:"region"   yaml:"region"`
	// Prefer snake_case to match README and user configs
	ProjectID             string `json:"project_id"               mapstructure:"project_id"               yaml:"project_id"`
	OrgID                 string `json:"org_id"                   mapstructure:"org_id"                   yaml:"org_id"`
	AuthToken             string `json:"auth_token"               mapstructure:"auth_token"               yaml:"auth_token"`
	ServiceAccountToken   string `json:"service_account_token"    mapstructure:"service_account_token"    yaml:"service_account_token"`
	ServiceAccountJSON    string `json:"service_account_json"     mapstructure:"service_account_json"     yaml:"service_account_json"`
	ServiceAccountKeyPath string `json:"service_account_key_path" mapstructure:"service_account_key_path" yaml:"service_account_key_path"`
	// Optional: override STACKIT API endpoint (e.g., https://iaas.api.stackit.cloud)
	APIEndpoint      string                      `json:"api_endpoint"        mapstructure:"api_endpoint"        yaml:"api_endpoint"`
	AccessKeyID      string                      `json:"access_key_id"       mapstructure:"access_key_id"       yaml:"access_key_id"`
	SecretAccessKey  string                      `json:"secret_access_key"   mapstructure:"secret_access_key"   yaml:"secret_access_key"`
	SubscriptionID   string                      `json:"subscription_id"     mapstructure:"subscription_id"     yaml:"subscription_id"`
	TenantID         string                      `json:"tenant_id"           mapstructure:"tenant_id"           yaml:"tenant_id"`
	ClientID         string                      `json:"client_id"           mapstructure:"client_id"           yaml:"client_id"`
	ClientSecret     string                      `json:"client_secret"       mapstructure:"client_secret"       yaml:"client_secret"`
	AuthURL          string                      `json:"auth_url"            mapstructure:"auth_url"            yaml:"auth_url"`
	Username         string                      `json:"username"            mapstructure:"username"            yaml:"username"`
	Password         string                      `json:"password"            mapstructure:"password"            yaml:"password"`
	ProjectName      string                      `json:"project_name"        mapstructure:"project_name"        yaml:"project_name"`
	DomainName       string                      `json:"domain_name"         mapstructure:"domain_name"         yaml:"domain_name"`
	SessionToken     string                      `json:"session_token"       mapstructure:"session_token"       yaml:"session_token"`
	BastionIP        string                      `json:"bastion_ip"          mapstructure:"bastion_ip"          yaml:"bastion_ip"`
	Network          NetworkConfig               `json:"network"             mapstructure:"network"             yaml:"network"`
	Bastion          Bastion                     `json:"bastion"             mapstructure:"bastion"             yaml:"bastion"`
	Genesis          Genesis                     `json:"genesis"             mapstructure:"genesis"             yaml:"genesis"`
	DeploymentsData  map[string]interface{}      `json:"deployments" mapstructure:"deployments" yaml:"deployments"`
	Deployments      *DeploymentSettings         `json:"-" mapstructure:"-" yaml:"-"`
	DNS              []string                    `json:"dns"                 mapstructure:"dns"                 yaml:"dns"`
	AZs              map[string]AvailabilityZone `json:"azs"                 mapstructure:"azs"                 yaml:"azs"`
	SSHKeyStorageDir string                      `json:"ssh_key_storage_dir" mapstructure:"ssh_key_storage_dir" yaml:"ssh_key_storage_dir"`
	Routers          ComponentConfig             `json:"routers"             mapstructure:"routers"             yaml:"routers"`
	Cells            ComponentConfig             `json:"cells"               mapstructure:"cells"               yaml:"cells"`
	// Additional fields from the example config
	FQDNs             map[string]interface{} `json:"fqdns"               mapstructure:"fqdns"               yaml:"fqdns"`
	S3                map[string]string      `json:"s3"                  mapstructure:"s3"                  yaml:"s3"`
	AllowedIngressIPs []string               `json:"allowed_ingress_ips" mapstructure:"allowed_ingress_ips" yaml:"allowed_ingress_ips"`
	Type              string                 `json:"type"                mapstructure:"type"                yaml:"type"`
	Environment       string                 `json:"environment"         mapstructure:"environment"         yaml:"environment"`
	Subnets           []Subnet               `json:"subnets"             mapstructure:"subnets"             yaml:"subnets"`
	SubnetStrategy    string                 `json:"subnet_strategy"     mapstructure:"subnet_strategy"     yaml:"subnet_strategy"`
	LBs               map[string]LBService   `json:"lbs"                 mapstructure:"lbs"                 yaml:"lbs"`
	Users             map[string]string      `json:"users"               mapstructure:"users"               yaml:"users"`
	// Public IP configurations
	RouterPublicIPs    int `json:"router_public_ips"     mapstructure:"router_public_ips"     yaml:"router_public_ips"`
	CFSSHPublicIPs     int `json:"cf_ssh_public_ips"     mapstructure:"cf_ssh_public_ips"     yaml:"cf_ssh_public_ips"`
	JumpboxPublicIPs   int `json:"jumpbox_public_ips"    mapstructure:"jumpbox_public_ips"    yaml:"jumpbox_public_ips"`
	TCPRouterPublicIPs int `json:"tcp_router_public_ips" mapstructure:"tcp_router_public_ips" yaml:"tcp_router_public_ips"`

	// Structured public IPs configuration
	PublicIPs PublicIPsConfig `json:"public_ips" mapstructure:"public_ips" yaml:"public_ips"`

	// Buckets configuration
	Buckets []BucketConfig `json:"buckets" mapstructure:"buckets" yaml:"buckets"`

	// Blobstore policies (optional, for object storage buckets)
	Blobstore BlobstoreConfig `json:"blobstore" mapstructure:"blobstore" yaml:"blobstore"`
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

// Common default network CIDR for several providers.
const defaultNetworkCIDR = "10.0.0.0/16"

// BucketSettings specify data-plane policies.
type BucketSettings struct {
	Versioning     bool `mapstructure:"versioning"     yaml:"versioning"`
	NoncurrentDays int  `mapstructure:"noncurrentDays" yaml:"noncurrentDays"`
}

// NetworkConfig represents network configuration.
type NetworkConfig struct {
	ID             string   `mapstructure:"id"             yaml:"id"`
	Name           string   `mapstructure:"name"           yaml:"name"`
	CIDR           string   `mapstructure:"cidr"           yaml:"cidr"`
	NetworkCIDR    string   `mapstructure:"networkCidr"    yaml:"networkCidr"`
	SubnetID       string   `mapstructure:"subnetId"       yaml:"subnetId"`
	DNS            []string `mapstructure:"dns"            yaml:"dns"`
	DNSServers     []string `mapstructure:"dnsServers"     yaml:"dnsServers"`     // Alternative field name
	SubnetStrategy string   `mapstructure:"subnetStrategy" yaml:"subnetStrategy"` // Network subnet strategy
	Subnets        []Subnet `mapstructure:"subnets"        yaml:"subnets"`        // Network subnets
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
	UserData   string    `mapstructure:"userData"         yaml:"userData"` // Custom user data for bastion
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

const (
	DeploymentModeDev     = "dev"
	DeploymentModeRelease = "release"
)

// DeploymentSettings captures global deployment repository configuration and per-deployment overrides.
type DeploymentSettings struct {
	URL     string
	Entries map[string]*DeploymentEntry
}

// DeploymentEntry stores per-deployment overrides (currently only mode) along with the raw configuration.
type DeploymentEntry struct {
	Mode string
	Raw  map[string]interface{}
}

// NewDeploymentSettings creates a deployment settings structure with the provided values.
func NewDeploymentSettings(url string, entries map[string]*DeploymentEntry) *DeploymentSettings {
	if entries == nil {
		entries = make(map[string]*DeploymentEntry)
	}

	return &DeploymentSettings{
		URL:     url,
		Entries: entries,
	}
}

// ModeFor returns the effective mode (dev/release) for a deployment, respecting overrides and global defaults.
func (d *DeploymentSettings) ModeFor(name string) string {
	if d == nil {
		return DeploymentModeDev
	}

	if entry, ok := d.Entries[name]; ok {
		if entry != nil && entry.Mode != "" {
			return entry.Mode
		}
	}

	if d.URL != "" {
		return DeploymentModeRelease
	}

	return DeploymentModeDev
}

// Configured returns all explicitly configured deployment names.
func (d *DeploymentSettings) Configured() []string {
	if d == nil {
		return nil
	}

	names := make([]string, 0, len(d.Entries))
	for name := range d.Entries {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// Entry returns the deployment entry if configured.
func (d *DeploymentSettings) Entry(name string) *DeploymentEntry {
	if d == nil {
		return nil
	}

	return d.Entries[name]
}

func parseDeploymentSettings(raw map[string]interface{}) (*DeploymentSettings, error) {
	settings := NewDeploymentSettings("", nil)

	if raw == nil {
		return settings, nil
	}

	for key, value := range raw {
		if key == "url" {
			if value != nil {
				settings.URL = fmt.Sprint(value)
			}
			continue
		}

		entry, err := parseDeploymentEntry(value)
		if err != nil {
			return nil, fmt.Errorf("deployment %s: %w", key, err)
		}

		settings.Entries[key] = entry
	}

	return settings, nil
}

func parseDeploymentEntry(value interface{}) (*DeploymentEntry, error) {
	entry := &DeploymentEntry{
		Mode: "",
		Raw:  make(map[string]interface{}),
	}

	switch v := value.(type) {
	case nil:
		// No overrides
	case string:
		entry.Mode = normalizeDeploymentMode(v)
	case map[string]interface{}:
		entry.Raw = copyStringInterfaceMap(v)
		if mode, ok := extractString(v["mode"]); ok {
			entry.Mode = normalizeDeploymentMode(mode)
		}
	case map[interface{}]interface{}:
		expanded := convertInterfaceKeyMap(v)
		entry.Raw = expanded
		if mode, ok := extractString(expanded["mode"]); ok {
			entry.Mode = normalizeDeploymentMode(mode)
		}
	default:
		entry.Mode = normalizeDeploymentMode(fmt.Sprint(v))
	}

	return entry, nil
}

func copyStringInterfaceMap(src map[string]interface{}) map[string]interface{} {
	if src == nil {
		return nil
	}

	out := make(map[string]interface{}, len(src))
	for k, v := range src {
		out[k] = v
	}

	return out
}

func convertInterfaceKeyMap(src map[interface{}]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(src))
	for k, v := range src {
		out[fmt.Sprint(k)] = v
	}

	return out
}

func extractString(value interface{}) (string, bool) {
	if value == nil {
		return "", false
	}

	switch v := value.(type) {
	case string:
		return v, true
	case []byte:
		return string(v), true
	default:
		return fmt.Sprint(v), true
	}
}

func normalizeDeploymentMode(value string) string {
	mode := strings.ToLower(strings.TrimSpace(value))
	switch mode {
	case DeploymentModeRelease:
		return DeploymentModeRelease
	case DeploymentModeDev:
		return DeploymentModeDev
	default:
		return ""
	}
}

// AvailabilityZone configuration.
type AvailabilityZone struct {
	Zone            string `mapstructure:"zone"            yaml:"zone"`
	CloudProperties string `mapstructure:"cloudProperties" yaml:"cloudProperties"`
}

// Configuration caching for performance optimization.
var (
	configMutex   sync.RWMutex                     //nolint:gochecknoglobals // package-level cache lock for performance
	cachedConfigs = make(map[string]*cachedConfig) //nolint:gochecknoglobals // package-level cache for configs
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
	configPath := determineConfigPath(configFile)

	// If configPath is empty, return an error
	if configPath == "" {
		return nil, fmt.Errorf("No configuration file found.  Should find ~/.ocfp/config.yml or specify -f configfile.yml")
	}

	// If blocName is empty, return an error
	if blocName == "" {
		return nil, fmt.Errorf("No blocname provided")
	}

	// Try to get from cache first
	if cachedCfg := getCachedConfig(configPath, blocName); cachedCfg != nil {
		return cachedCfg, nil
	}

	// Load configuration
	cfg, err := loadConfiguration(configPath, blocName)
	if err != nil {
		return nil, err
	}

	// Process loaded configuration
	err = processConfiguration(cfg)
	if err != nil {
		return nil, err
	}

	// Cache the configuration
	cacheConfiguration(configPath, blocName, cfg)

	return cfg, nil
}

// getCachedConfig retrieves configuration from cache if available and not stale.
func getCachedConfig(configPath, blocName string) *Config {
	if configPath == "" {
		return nil
	}

	configMutex.RLock()
	defer configMutex.RUnlock()

	cacheKey := configPath + ":" + blocName

	cached, exists := cachedConfigs[cacheKey]
	if !exists {
		return nil
	}

	// Check if file has been modified since caching
	stat, err := os.Stat(configPath)
	if err == nil {
		if stat.ModTime().Equal(cached.modTime) {
			return cached.config
		}
	}

	return nil
}

// loadConfiguration loads the configuration from file or creates empty config.
func loadConfiguration(configPath, blocName string) (*Config, error) {
	if configPath == "" {
		return createEmptyConfig(), nil
	}

	return loadConfigFromFile(configPath, blocName)
}

func createEmptyConfig() *Config {
	return &Config{
		Network: NetworkConfig{
			DNS: []string{},
		},
		Bastion: Bastion{
			Genesis:           Genesis{},
			Git:               GitConfig{User: GitUser{}},
			Tools:             OverrideSets{Enable: []string{}, Disable: []string{}},
			CFPlugins:         OverrideSets{Enable: []string{}, Disable: []string{}},
			Snaps:             OverrideSets{Enable: []string{}, Disable: []string{}},
			ToolOverrides:     map[string]ToolOverride{},
			CFPluginOverrides: map[string]CFPluginOverride{},
			SnapOverrides:     map[string]SnapOverride{},
		},
		Genesis:           Genesis{},
		Deployments:       NewDeploymentSettings("", nil),
		DNS:               []string{},
		AZs:               map[string]AvailabilityZone{},
		Routers:           ComponentConfig{},
		Cells:             ComponentConfig{},
		FQDNs:             map[string]interface{}{},
		S3:                map[string]string{},
		AllowedIngressIPs: []string{},
		Subnets:           []Subnet{},
		LBs:               map[string]LBService{},
		Users:             map[string]string{},
		Blobstore: BlobstoreConfig{
			BoshBlobstore: BucketSettings{},
			CFBuildpacks:  BucketSettings{},
			CFDroplets:    BucketSettings{},
			CFAppPackages: BucketSettings{},
		},
	}
}

func loadConfigFromFile(configPath, blocName string) (*Config, error) {
	configFileData := &ConfigFile{
		Debug:   false,
		Verbose: false,
		Blocs:   map[string]*Config{},
	}

	err := loadFromFile(configPath, configFileData)
	if err != nil {
		return nil, fmt.Errorf("failed to load config from %s: %w", configPath, err)
	}

	blocConfig, exists := configFileData.Blocs[blocName]
	if !exists {
		return nil, ErrBlocNotFound(blocName, configPath)
	}

	if blocConfig.Name == "" {
		blocConfig.Name = blocName
	}

	return blocConfig, nil
}

// processConfiguration applies defaults, overrides, and validates the config.
func processConfiguration(cfg *Config) error {
	var err error
	cfg.Deployments, err = parseDeploymentSettings(cfg.DeploymentsData)
	if err != nil {
		return fmt.Errorf("invalid deployments configuration: %w", err)
	}
	cfg.DeploymentsData = nil

	// Determine provider
	provider := cfg.Provider
	if provider == "" {
		provider = cfg.IaaS
	}

	if provider == "" {
		provider = viper.GetString("iaas")
	}

	// Apply provider defaults
	err = applyDefaults(cfg, provider)
	if err != nil {
		return err
	}

	// Override with viper values (from flags/env)
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
	err = validate(cfg)
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	return nil
}

// cacheConfiguration stores the configuration in cache.
func cacheConfiguration(configPath, blocName string, cfg *Config) {
	if configPath == "" {
		return
	}

	configMutex.Lock()
	defer configMutex.Unlock()

	cacheKey := configPath + ":" + blocName

	stat, err := os.Stat(configPath)
	if err == nil {
		cachedConfigs[cacheKey] = &cachedConfig{
			config:   cfg,
			modTime:  stat.ModTime(),
			filePath: configPath,
		}
	}
}

// determineConfigPath determines the configuration file path.
func determineConfigPath(configFile string) string {
	// Priority 1: Explicit config file
	if configFile != "" {
		return configFile
	}

	// Priority 2: Default config file at ~/.ocfp/config.yml
	homeDir, err := os.UserHomeDir()
	if err == nil {
		defaultPath := filepath.Join(homeDir, ".ocfp", "config.yml")

		_, err = os.Stat(defaultPath)
		if err == nil {
			return defaultPath
		}
	}

	// Priority 3: Check in local config/config.yml
	_, err = os.Stat("config/config.yml")
	if err == nil {
		return "config/config.yml"
	}

	return ""
}

// loadFromFile loads configuration from a YAML file.
func loadFromFile(path string, target interface{}) error {
	err := security.ValidateConfigPath(path)
	if err != nil {
		return fmt.Errorf("invalid config path: %w", err)
	}

	data, err := os.ReadFile(path) // #nosec G304 - path is validated above
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	err = yaml.Unmarshal(data, target)
	if err != nil {
		return fmt.Errorf("failed to unmarshal config: %w", err)
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

	if len(cfg.Network.DNS) == 0 && len(cfg.Network.DNSServers) == 0 {
		cfg.Network.DNS = []string{"1.1.1.1", "8.8.8.8"}
		cfg.Network.DNSServers = []string{"1.1.1.1", "8.8.8.8"}
	}

	if cfg.Bastion.Flavor == "" {
		cfg.Bastion.Flavor = "g1a.2d"
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
			"eu01-1": {Zone: "eu01-1", CloudProperties: `{"availability_zone": "eu01-1"}`},
			"eu01-2": {Zone: "eu01-2", CloudProperties: `{"availability_zone": "eu01-2"}`},
			"eu01-3": {Zone: "eu01-3", CloudProperties: `{"availability_zone": "eu01-3"}`},
		}

		// No default lifecycle/versioning; leave disabled unless configured
	}
}

// applyOpenStackDefaults applies OpenStack-specific defaults.
func applyOpenStackDefaults(cfg *Config) {
	if cfg.Network.CIDR == "" && cfg.Network.NetworkCIDR == "" {
		cfg.Network.NetworkCIDR = defaultNetworkCIDR
	}

	if cfg.Bastion.Flavor == "" {
		cfg.Bastion.Flavor = "t1.small"
	}
}

// applyAWSDefaults applies AWS-specific defaults.
func applyAWSDefaults(cfg *Config) {
	if cfg.Network.CIDR == "" && cfg.Network.NetworkCIDR == "" {
		cfg.Network.NetworkCIDR = defaultNetworkCIDR
	}

	if cfg.Bastion.Flavor == "" {
		cfg.Bastion.Flavor = "t3.small"
	}
}

// applyAzureDefaults applies Azure-specific defaults.
func applyAzureDefaults(cfg *Config) {
	if cfg.Network.CIDR == "" && cfg.Network.NetworkCIDR == "" {
		cfg.Network.NetworkCIDR = defaultNetworkCIDR
	}

	if cfg.Bastion.Flavor == "" {
		cfg.Bastion.Flavor = "Standard_B2s"
	}
}

// applyGCPDefaults applies GCP-specific defaults.
func applyGCPDefaults(cfg *Config) {
	if cfg.Network.CIDR == "" && cfg.Network.NetworkCIDR == "" {
		cfg.Network.NetworkCIDR = defaultNetworkCIDR
	}

	if cfg.Bastion.Flavor == "" {
		cfg.Bastion.Flavor = "e2-small"
	}
}

// GetDeploymentsURL returns the global deployments repository URL if configured.
func (cfg *Config) GetDeploymentsURL() string {
	if cfg == nil || cfg.Deployments == nil {
		return ""
	}

	return cfg.Deployments.URL
}

// GetDeploymentMode returns the effective deployment mode for the provided name.
func (cfg *Config) GetDeploymentMode(name string) string {
	if cfg == nil || cfg.Deployments == nil {
		return DeploymentModeDev
	}

	return cfg.Deployments.ModeFor(name)
}

// GetConfiguredDeployments returns the configured deployment identifiers.
func (cfg *Config) GetConfiguredDeployments() []string {
	if cfg == nil || cfg.Deployments == nil {
		return nil
	}

	return cfg.Deployments.Configured()
}

// validate validates the configuration.
func validate(cfg *Config) error {
	// Required fields
	if cfg.Provider == "" && cfg.IaaS == "" {
		return ErrProviderOrIaasRequired
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
		return ErrInvalidProvider(cfg.Provider)
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

	_, err = os.Stat(newPath)
	if err == nil {
		return newPath
	}

	// Try legacy location
	legacyPath := filepath.Join(home, ".ssh", fmt.Sprintf("%s-%s", blocName, keypair))

	_, err = os.Stat(legacyPath)
	if err == nil {
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

// PublicIPsConfig represents public IPs configuration.
type PublicIPsConfig struct {
	Ops       int `mapstructure:"ops"        yaml:"ops"`
	Jumpbox   int `mapstructure:"jumpbox"    yaml:"jumpbox"`
	Router    int `mapstructure:"router"     yaml:"router"`
	CFSSH     int `mapstructure:"cf_ssh"     yaml:"cf_ssh"`
	TCPRouter int `mapstructure:"tcp_router" yaml:"tcp_router"`
}

// BucketConfig represents bucket configuration.
type BucketConfig struct {
	Name string `mapstructure:"name" yaml:"name"`
}
