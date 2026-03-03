// Package config handles OCFP CLI configuration file loading, validation, and bloc management.
package config

import (
	"errors"
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

// FQDN configuration errors.
var (
	ErrFQDNBaseInvalidType   = errors.New("fqdns.base must be a string or list of strings")
	ErrFQDNBaseElementString = errors.New("fqdns.base list element must be a string")
)

// ConfigFile represents the top-level configuration file structure.
//
//revive:disable-next-line:exported stutters as config.ConfigFile but renaming would break external references
type ConfigFile struct {
	Debug   bool               `mapstructure:"debug"   yaml:"debug"`
	Verbose bool               `mapstructure:"verbose" yaml:"verbose"`
	Blocs   map[string]*Config `mapstructure:"blocs"   yaml:"blocs"`
}

// Config represents a bloc configuration.
type Config struct {
	Name     string `json:"name"     mapstructure:"name"     yaml:"name,omitempty"`
	Provider string `json:"provider" mapstructure:"provider" yaml:"provider,omitempty"`
	IaaS     string `json:"iaas"     mapstructure:"iaas"     yaml:"iaas,omitempty"`
	Region   string `json:"region"   mapstructure:"region"   yaml:"region,omitempty"`
	// Prefer snake_case to match README and user configs
	ProjectID             string `json:"project_id"               mapstructure:"project_id"               yaml:"project_id,omitempty"`
	OrgID                 string `json:"org_id"                   mapstructure:"org_id"                   yaml:"org_id,omitempty"`
	AuthToken             string `json:"auth_token"               mapstructure:"auth_token"               yaml:"auth_token,omitempty"` //nolint:gosec // field name is descriptive, not a hardcoded secret
	ServiceAccountToken   string `json:"service_account_token"    mapstructure:"service_account_token"    yaml:"service_account_token,omitempty"`
	ServiceAccountJSON    string `json:"service_account_json"     mapstructure:"service_account_json"     yaml:"service_account_json,omitempty"`
	ServiceAccountKeyPath string `json:"service_account_key_path" mapstructure:"service_account_key_path" yaml:"service_account_key_path,omitempty"`
	// Optional: override STACKIT API endpoint (e.g., https://iaas.api.stackit.cloud)
	APIEndpoint      string                      `json:"api_endpoint"        mapstructure:"api_endpoint"        yaml:"api_endpoint,omitempty"`
	AccessKeyID      string                      `json:"access_key_id"       mapstructure:"access_key_id"       yaml:"access_key_id,omitempty"`
	SecretAccessKey  string                      `json:"secret_access_key"   mapstructure:"secret_access_key"   yaml:"secret_access_key,omitempty"`
	SubscriptionID   string                      `json:"subscription_id"     mapstructure:"subscription_id"     yaml:"subscription_id,omitempty"`
	TenantID         string                      `json:"tenant_id"           mapstructure:"tenant_id"           yaml:"tenant_id,omitempty"`
	ClientID         string                      `json:"client_id"           mapstructure:"client_id"           yaml:"client_id,omitempty"`
	ClientSecret     string                      `json:"client_secret"       mapstructure:"client_secret"       yaml:"client_secret,omitempty"` //nolint:gosec // field name is descriptive, not a hardcoded secret
	AuthURL          string                      `json:"auth_url"            mapstructure:"auth_url"            yaml:"auth_url,omitempty"`
	Username         string                      `json:"username"            mapstructure:"username"            yaml:"username,omitempty"`
	Password         string                      `json:"password"            mapstructure:"password"            yaml:"password,omitempty"` //nolint:gosec // field name is descriptive, not a hardcoded secret
	ProjectName      string                      `json:"project_name"        mapstructure:"project_name"        yaml:"project_name,omitempty"`
	DomainName       string                      `json:"domain_name"         mapstructure:"domain_name"         yaml:"domain_name,omitempty"`
	SessionToken     string                      `json:"session_token"       mapstructure:"session_token"       yaml:"session_token,omitempty"` //nolint:gosec // field name is descriptive, not a hardcoded secret
	BastionIP        string                      `json:"bastion_ip"          mapstructure:"bastion_ip"          yaml:"bastion_ip,omitempty"`
	VPCCIDRBlock     string                      `json:"vpc_cidr_block"      mapstructure:"vpc_cidr_block"      yaml:"vpc_cidr_block,omitempty"` // AWS-specific network CIDR
	Network          NetworkConfig               `json:"network"             mapstructure:"network"             yaml:"network,omitempty"`
	Bastion          Bastion                     `json:"bastion"             mapstructure:"bastion"             yaml:"bastion,omitempty"`
	Jumpbox          Jumpbox                     `json:"jumpbox"             mapstructure:"jumpbox"             yaml:"jumpbox,omitempty"`
	Genesis          Genesis                     `json:"genesis"             mapstructure:"genesis"             yaml:"genesis,omitempty"`
	DeploymentsData  map[string]interface{}      `json:"deployments"         mapstructure:"deployments"         yaml:"deployments,omitempty"`
	Deployments      *DeploymentSettings         `json:"-"                   mapstructure:"-"                   yaml:"-"`
	DNS              []string                    `json:"dns"                 mapstructure:"dns"                 yaml:"dns,omitempty"`
	AZs              map[string]AvailabilityZone `json:"azs"                 mapstructure:"azs"                 yaml:"azs,omitempty"`
	SSHKeyStorageDir string                      `json:"ssh_key_storage_dir" mapstructure:"ssh_key_storage_dir" yaml:"ssh_key_storage_dir,omitempty"`
	Routers          ComponentConfig             `json:"routers"             mapstructure:"routers"             yaml:"routers,omitempty"`
	Cells            ComponentConfig             `json:"cells"               mapstructure:"cells"               yaml:"cells,omitempty"`
	// Additional fields from the example config
	FQDNs             *FQDNConfig          `json:"fqdns"               mapstructure:"fqdns"               yaml:"fqdns,omitempty"`
	S3                map[string]string    `json:"s3"                  mapstructure:"s3"                  yaml:"s3,omitempty"`
	AllowedIngressIPs []string             `json:"allowed_ingress_ips" mapstructure:"allowed_ingress_ips" yaml:"allowed_ingress_ips,omitempty"`
	Type              string               `json:"type"                mapstructure:"type"                yaml:"type,omitempty"`
	Environment       string               `json:"environment"         mapstructure:"environment"         yaml:"environment,omitempty"`
	Subnets           []Subnet             `json:"subnets"             mapstructure:"subnets"             yaml:"subnets,omitempty"`
	SubnetStrategy    string               `json:"subnet_strategy"     mapstructure:"subnet_strategy"     yaml:"subnet_strategy,omitempty"`
	LBs               map[string]LBService `json:"lbs"                 mapstructure:"lbs"                 yaml:"lbs,omitempty"`
	Users             map[string]string    `json:"users"               mapstructure:"users"               yaml:"users,omitempty"`
	// Public IP configurations
	RouterPublicIPs    int `json:"router_public_ips"     mapstructure:"router_public_ips"     yaml:"router_public_ips,omitempty"`
	CFSSHPublicIPs     int `json:"cf_ssh_public_ips"     mapstructure:"cf_ssh_public_ips"     yaml:"cf_ssh_public_ips,omitempty"`
	JumpboxPublicIPs   int `json:"jumpbox_public_ips"    mapstructure:"jumpbox_public_ips"    yaml:"jumpbox_public_ips,omitempty"`
	TCPRouterPublicIPs int `json:"tcp_router_public_ips" mapstructure:"tcp_router_public_ips" yaml:"tcp_router_public_ips,omitempty"`

	// Structured public IPs configuration
	PublicIPs PublicIPsConfig `json:"public_ips" mapstructure:"public_ips" yaml:"public_ips,omitempty"`

	// Buckets configuration
	Buckets []BucketConfig `json:"buckets" mapstructure:"buckets" yaml:"buckets,omitempty"`

	// Blobstore policies (optional, for object storage buckets)
	Blobstore BlobstoreConfig `json:"blobstore" mapstructure:"blobstore" yaml:"blobstore,omitempty"`

	// SSH Keys storage for portability (bloc-name -> ed25519 private key)
	Keys map[string]string `json:"keys" mapstructure:"keys" yaml:"keys,omitempty"`
}

// BlobstoreConfig controls versioning/lifecycle policies for expected buckets.
type BlobstoreConfig struct {
	EnablePolicies bool `json:"enablePolicies,omitempty" mapstructure:"enablePolicies" yaml:"enablePolicies,omitempty"`

	// Per-bucket overrides
	BoshBlobstore BucketSettings `json:"boshBlobstore,omitempty" mapstructure:"boshBlobstore" yaml:"boshBlobstore,omitempty"`
	CFBuildpacks  BucketSettings `json:"cfBuildpacks,omitempty"  mapstructure:"cfBuildpacks"  yaml:"cfBuildpacks,omitempty"`
	CFDroplets    BucketSettings `json:"cfDroplets,omitempty"    mapstructure:"cfDroplets"    yaml:"cfDroplets,omitempty"`
	CFAppPackages BucketSettings `json:"cfAppPackages,omitempty" mapstructure:"cfAppPackages" yaml:"cfAppPackages,omitempty"`
}

// Common default network CIDR for several providers.
const (
	defaultNetworkCIDR = "10.0.0.0/16"
	configFileMode     = 0o600

	// Network CIDR splitting constants.
	subnetSplitCount    = 4  // Number of subnets to carve from a /20 network
	subnetReservedCount = 3  // Number of usable subnets (skip first reserved)
	cidrPartCount       = 2  // Expected parts when splitting CIDR by "/"
	ipOctetCount        = 4  // Number of octets in an IPv4 address
	maxPrefixLen        = 32 // Maximum IPv4 prefix length
	octetBitmask        = 0xFF
	octetShift24        = 24
	octetShift16        = 16
	octetShift8         = 8
)

// BucketSettings specify data-plane policies.
type BucketSettings struct {
	Versioning     bool `json:"versioning,omitempty"     mapstructure:"versioning"     yaml:"versioning,omitempty"`
	NoncurrentDays int  `json:"noncurrentDays,omitempty" mapstructure:"noncurrentDays" yaml:"noncurrentDays,omitempty"`
}

// NetworkConfig represents network configuration.
type NetworkConfig struct {
	ID             string   `json:"id,omitempty"             mapstructure:"id"             yaml:"id,omitempty"`
	Name           string   `json:"name,omitempty"           mapstructure:"name"           yaml:"name,omitempty"`
	CIDR           string   `json:"cidr,omitempty"           mapstructure:"cidr"           yaml:"cidr,omitempty"`
	NetworkCIDR    string   `json:"networkCidr,omitempty"    mapstructure:"networkCidr"    yaml:"networkCidr,omitempty"`
	SubnetID       string   `json:"subnetId,omitempty"       mapstructure:"subnetId"       yaml:"subnetId,omitempty"`
	DNS            []string `json:"dns,omitempty"            mapstructure:"dns"            yaml:"dns,omitempty"`
	DNSServers     []string `json:"dnsServers,omitempty"     mapstructure:"dnsServers"     yaml:"dnsServers,omitempty"`
	SubnetStrategy string   `json:"subnetStrategy,omitempty" mapstructure:"subnetStrategy" yaml:"subnetStrategy,omitempty"`
	Subnets        []Subnet `json:"subnets,omitempty"        mapstructure:"subnets"        yaml:"subnets,omitempty"`
}

// Subnet configuration.
type Subnet struct {
	Name             string `json:"name,omitempty"             mapstructure:"name"             yaml:"name,omitempty"`
	CIDR             string `json:"cidr,omitempty"             mapstructure:"cidr"             yaml:"cidr,omitempty"`
	AvailabilityZone string `json:"availabilityZone,omitempty" mapstructure:"availabilityZone" yaml:"availabilityZone,omitempty"`
	Type             string `json:"type,omitempty"             mapstructure:"type"             yaml:"type,omitempty"`
}

// Bastion configuration.
type Bastion struct {
	Flavor       string    `json:"flavor,omitempty"           mapstructure:"flavor"           yaml:"flavor,omitempty"`
	InstanceType string    `json:"instanceType,omitempty"     mapstructure:"instanceType"     yaml:"instanceType,omitempty"`
	Image        string    `json:"image,omitempty"            mapstructure:"image"            yaml:"image,omitempty"`
	OS           string    `json:"os,omitempty"               mapstructure:"os"               yaml:"os,omitempty"`
	OSVersion    string    `json:"osVersion,omitempty"        mapstructure:"osVersion"        yaml:"osVersion,omitempty"`
	Keypair      string    `json:"keypair,omitempty"          mapstructure:"keypair"          yaml:"keypair,omitempty"`
	SSHUser      string    `json:"sshUser,omitempty"          mapstructure:"sshUser"          yaml:"sshUser,omitempty"`
	SSHOptions   string    `json:"sshOptions,omitempty"       mapstructure:"sshOptions"       yaml:"sshOptions,omitempty"`
	SSHKeyDir    string    `json:"sshKeyStorageDir,omitempty" mapstructure:"sshKeyStorageDir" yaml:"sshKeyStorageDir,omitempty"`
	SSHKeyName   string    `json:"sshKeyName,omitempty"       mapstructure:"sshKeyName"       yaml:"sshKeyName,omitempty"`
	UserData     string    `json:"userData,omitempty"         mapstructure:"userData"         yaml:"userData,omitempty"`
	RootDiskSize int       `json:"rootDiskSize,omitempty"     mapstructure:"rootDiskSize"     yaml:"rootDiskSize,omitempty"`
	DataDiskSize int       `json:"dataDiskSize,omitempty"     mapstructure:"dataDiskSize"     yaml:"dataDiskSize,omitempty"`
	Genesis      Genesis   `json:"genesis,omitempty"          mapstructure:"genesis"          yaml:"genesis,omitempty"`
	Git          GitConfig `json:"git,omitempty"              mapstructure:"git"              yaml:"git,omitempty"`
	// Optional overrides for tooling installation/selection
	Tools     OverrideSets `json:"tools,omitempty"     mapstructure:"tools"     yaml:"tools,omitempty"`
	CFPlugins OverrideSets `json:"cfPlugins,omitempty" mapstructure:"cfPlugins" yaml:"cfPlugins,omitempty"`
	Snaps     OverrideSets `json:"snaps,omitempty"     mapstructure:"snaps"     yaml:"snaps,omitempty"`
	// Per-item override maps (by name)
	ToolOverrides     map[string]ToolOverride     `json:"toolOverrides,omitempty"     mapstructure:"toolOverrides"     yaml:"toolOverrides,omitempty"`
	CFPluginOverrides map[string]CFPluginOverride `json:"cfPluginOverrides,omitempty" mapstructure:"cfPluginOverrides" yaml:"cfPluginOverrides,omitempty"`
	SnapOverrides     map[string]SnapOverride     `json:"snapOverrides,omitempty"     mapstructure:"snapOverrides"     yaml:"snapOverrides,omitempty"`
	// SSH keys to add to the bastion's authorized_keys.
	// Values: direct public key, "github/<username>", or "gitlab/<username>".
	Keys map[string]string `json:"keys,omitempty" mapstructure:"keys" yaml:"keys,omitempty"`
}

// Jumpbox configuration for jumpbox user accounts.
type Jumpbox struct {
	Users map[string]string `json:"users,omitempty" mapstructure:"users" yaml:"users,omitempty"`
}

// ComponentConfig represents configuration for CF components.
type ComponentConfig struct {
	Flavor   string `json:"flavor,omitempty"   mapstructure:"flavor"   yaml:"flavor,omitempty"`
	Image    string `json:"image,omitempty"    mapstructure:"image"    yaml:"image,omitempty"`
	Count    int    `json:"count,omitempty"    mapstructure:"count"    yaml:"count,omitempty"`
	DiskSize int    `json:"diskSize,omitempty" mapstructure:"diskSize" yaml:"diskSize,omitempty"`
}

// Genesis configuration.
type Genesis struct {
	Enabled       bool   `json:"enabled,omitempty"       mapstructure:"enabled"       yaml:"enabled,omitempty"`
	Repo          string `json:"repo,omitempty"          mapstructure:"repo"          yaml:"repo,omitempty"`
	Branch        string `json:"branch,omitempty"        mapstructure:"branch"        yaml:"branch,omitempty"`
	Commit        string `json:"commit,omitempty"        mapstructure:"commit"        yaml:"commit,omitempty"`
	VersionPrefix string `json:"versionPrefix,omitempty" mapstructure:"versionPrefix" yaml:"versionPrefix,omitempty"`
}

// GitConfig represents Git configuration for the bastion.
type GitConfig struct {
	User GitUser `json:"user,omitempty" mapstructure:"user" yaml:"user,omitempty"`
}

// GitUser represents Git user configuration.
type GitUser struct {
	Name  string `json:"name,omitempty"  mapstructure:"name"  yaml:"name,omitempty"`
	Email string `json:"email,omitempty" mapstructure:"email" yaml:"email,omitempty"`
}

// OverrideSets allows enabling or disabling named items via config.
type OverrideSets struct {
	Enable  []string `json:"enable,omitempty"  mapstructure:"enable"  yaml:"enable,omitempty"`
	Disable []string `json:"disable,omitempty" mapstructure:"disable" yaml:"disable,omitempty"`
}

// ToolOverride allows overriding advanced tool properties.
type ToolOverride struct {
	URL            string `json:"url,omitempty"            mapstructure:"url"            yaml:"url,omitempty"`
	Version        string `json:"version,omitempty"        mapstructure:"version"        yaml:"version,omitempty"`
	VersionURL     string `json:"versionUrl,omitempty"     mapstructure:"versionUrl"     yaml:"versionUrl,omitempty"`
	VersionPattern string `json:"versionPattern,omitempty" mapstructure:"versionPattern" yaml:"versionPattern,omitempty"`
	URLTemplate    string `json:"urlTemplate,omitempty"    mapstructure:"urlTemplate"    yaml:"urlTemplate,omitempty"`
	Dest           string `json:"dest,omitempty"           mapstructure:"dest"           yaml:"dest,omitempty"`
	Mode           uint32 `json:"mode,omitempty"           mapstructure:"mode"           yaml:"mode,omitempty"`
	Sudo           *bool  `json:"sudo,omitempty"           mapstructure:"sudo"           yaml:"sudo,omitempty"`
	Extract        *bool  `json:"extract,omitempty"        mapstructure:"extract"        yaml:"extract,omitempty"`
	InstallCommand string `json:"installCommand,omitempty" mapstructure:"installCommand" yaml:"installCommand,omitempty"`
	InstallScript  string `json:"installScript,omitempty"  mapstructure:"installScript"  yaml:"installScript,omitempty"`
	VerifyCommand  string `json:"verifyCommand,omitempty"  mapstructure:"verifyCommand"  yaml:"verifyCommand,omitempty"`
	PathAddition   string `json:"pathAddition,omitempty"   mapstructure:"pathAddition"   yaml:"pathAddition,omitempty"`
	Cleanup        string `json:"cleanup,omitempty"        mapstructure:"cleanup"        yaml:"cleanup,omitempty"`
}

// CFPluginOverride allows overriding CF plugin properties.
type CFPluginOverride struct {
	GitHubRepo string `json:"githubRepo,omitempty" mapstructure:"githubRepo" yaml:"githubRepo,omitempty"`
	Version    string `json:"version,omitempty"    mapstructure:"version"    yaml:"version,omitempty"`
	Repo       string `json:"repo,omitempty"       mapstructure:"repo"       yaml:"repo,omitempty"`
	RepoURL    string `json:"repoUrl,omitempty"    mapstructure:"repoUrl"    yaml:"repoUrl,omitempty"`
	Force      *bool  `json:"force,omitempty"      mapstructure:"force"      yaml:"force,omitempty"`
}

// SnapOverride allows overriding snap package properties.
type SnapOverride struct {
	Channel      string `json:"channel,omitempty"      mapstructure:"channel"      yaml:"channel,omitempty"`
	Classic      *bool  `json:"classic,omitempty"      mapstructure:"classic"      yaml:"classic,omitempty"`
	DevMode      *bool  `json:"devMode,omitempty"      mapstructure:"devMode"      yaml:"devMode,omitempty"`
	Dangerous    *bool  `json:"dangerous,omitempty"    mapstructure:"dangerous"    yaml:"dangerous,omitempty"`
	CheckCommand string `json:"checkCommand,omitempty" mapstructure:"checkCommand" yaml:"checkCommand,omitempty"`
}

// Deployment mode constants for development and release configurations.
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

// serializeDeploymentSettings converts DeploymentSettings back to map[string]interface{}
// for marshaling to YAML. This is the inverse operation of parseDeploymentSettings.
func serializeDeploymentSettings(settings *DeploymentSettings) map[string]interface{} {
	if settings == nil {
		return nil
	}

	result := make(map[string]interface{})

	// Add the URL if present
	if settings.URL != "" {
		result["url"] = settings.URL
	}

	// Add each deployment entry
	for name, entry := range settings.Entries {
		if entry == nil {
			continue
		}

		// If there's raw data, use it (preserving any unknown fields)
		switch {
		case len(entry.Raw) > 0:
			entryMap := make(map[string]interface{})
			for k, v := range entry.Raw {
				entryMap[k] = v
			}

			// Override or add mode if explicitly set
			if entry.Mode != "" {
				entryMap["mode"] = entry.Mode
			}

			result[name] = entryMap
		case entry.Mode != "":
			// If only mode is set, just use the string
			result[name] = entry.Mode
		default:
			// Empty entry, use nil
			result[name] = nil
		}
	}

	// Return nil if the result would be empty
	if len(result) == 0 {
		return nil
	}

	return result
}

//nolint:unparam // error return for future validation
func parseDeploymentEntry(value interface{}) (*DeploymentEntry, error) {
	entry := &DeploymentEntry{
		Mode: "",
		Raw:  make(map[string]interface{}),
	}

	//nolint:varnamelen // v is idiomatic for type switch
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

	//nolint:varnamelen // v is idiomatic for type switch
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
	Zone            string `json:"zone,omitempty"            mapstructure:"zone"            yaml:"zone,omitempty"`
	CloudProperties string `json:"cloudProperties,omitempty" mapstructure:"cloudProperties" yaml:"cloudProperties,omitempty"`
}

// FQDNConfig represents the FQDN configuration with base domain and per-environment FQDNs.
type FQDNConfig struct {
	Base string            `json:"base" mapstructure:"base" yaml:"base,omitempty"`
	Mgmt map[string]string `json:"mgmt" mapstructure:"mgmt" yaml:"mgmt,omitempty"`
	OCF  map[string]string `json:"ocf"  mapstructure:"ocf"  yaml:"ocf,omitempty"`
}

// UnmarshalYAML handles fqdns.base being either a string or a single-element
// YAML sequence, coercing the latter into a plain string so callers always see
// FQDNConfig.Base as a string.
func (f *FQDNConfig) UnmarshalYAML(value *yaml.Node) error {
	type aux struct {
		Base interface{}       `yaml:"base"`
		Mgmt map[string]string `yaml:"mgmt"`
		OCF  map[string]string `yaml:"ocf"`
	}

	var raw aux

	decodeErr := value.Decode(&raw)
	if decodeErr != nil {
		return fmt.Errorf("decoding fqdns config: %w", decodeErr)
	}

	f.Mgmt = raw.Mgmt
	f.OCF = raw.OCF

	switch baseValue := raw.Base.(type) {
	case nil:
		f.Base = ""
	case string:
		f.Base = baseValue
	case []interface{}:
		if len(baseValue) == 0 {
			f.Base = ""
		} else {
			element, ok := baseValue[0].(string)
			if !ok {
				return fmt.Errorf("%w: got %T", ErrFQDNBaseElementString, baseValue[0])
			}

			f.Base = element
		}
	default:
		return fmt.Errorf("%w: got %T", ErrFQDNBaseInvalidType, baseValue)
	}

	return nil
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
		FQDNs:             &FQDNConfig{Mgmt: map[string]string{}, OCF: map[string]string{}},
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
		Keys: map[string]string{},
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

	// Migrate deprecated top-level users to jumpbox.users
	migrateDeprecatedUsers(cfg)

	// Validate configuration
	err = validate(cfg)
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	return nil
}

// migrateDeprecatedUsers copies top-level Users to Jumpbox.Users when the new
// field is empty, logging a deprecation warning.
func migrateDeprecatedUsers(cfg *Config) {
	if len(cfg.Users) == 0 || len(cfg.Jumpbox.Users) > 0 {
		return
	}

	cfg.Jumpbox.Users = make(map[string]string, len(cfg.Users))
	for k, v := range cfg.Users {
		cfg.Jumpbox.Users[k] = v
	}

	fmt.Fprintf(os.Stderr, "WARNING: top-level 'users' is deprecated; move entries under 'jumpbox.users'\n")
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

	applyGenesisDefaults(cfg)
	applyBastionGenesisDefaults(cfg)

	return nil
}

// applyGenesisDefaults applies defaults for the global Genesis configuration.
func applyGenesisDefaults(cfg *Config) {
	// Enable Genesis by default if not explicitly configured
	if !cfg.Genesis.Enabled && cfg.Bastion.Genesis.Enabled {
		// Bastion-specific Genesis config takes precedence
		cfg.Genesis = cfg.Bastion.Genesis
	} else if !cfg.Genesis.Enabled && !cfg.Bastion.Genesis.Enabled {
		// Neither global nor bastion-specific is explicitly enabled, enable by default
		cfg.Genesis.Enabled = true
	}

	if cfg.Genesis.Branch == "" {
		cfg.Genesis.Branch = "v3.1.x-dev"
	}

	if cfg.Genesis.VersionPrefix == "" {
		cfg.Genesis.VersionPrefix = "3.1.0"
	}
}

// applyBastionGenesisDefaults applies defaults for the bastion-specific Genesis configuration.
func applyBastionGenesisDefaults(cfg *Config) {
	if !cfg.Bastion.Genesis.Enabled {
		return
	}

	if cfg.Bastion.Genesis.Branch == "" {
		cfg.Bastion.Genesis.Branch = "v3.1.x-dev"
	}

	if cfg.Bastion.Genesis.VersionPrefix == "" {
		cfg.Bastion.Genesis.VersionPrefix = "3.1.0"
	}
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

	// Auto-generate 3 ocfp subnets if not configured
	// This ensures vault populate has the correct subnet structure
	if len(cfg.Subnets) == 0 {
		// First try to copy from Network.Subnets (populated by bootstrap)
		if len(cfg.Network.Subnets) > 0 {
			cfg.Subnets = cfg.Network.Subnets
		} else {
			// Generate 3 default ocfp subnets carved from network CIDR
			cfg.Subnets = generateDefaultStackitSubnets(cfg)
		}
	}
}

// generateDefaultStackitSubnets generates 3 ocfp subnets carved from the network CIDR.
// This matches the Perl implementation behavior of creating ocfp-0, ocfp-1, ocfp-2
// subnets across 3 availability zones.
func generateDefaultStackitSubnets(cfg *Config) []Subnet {
	// Determine parent network CIDR
	parentCIDR := cfg.Network.CIDR
	if parentCIDR == "" {
		parentCIDR = cfg.Network.NetworkCIDR
	}

	if parentCIDR == "" {
		parentCIDR = "10.4.0.0/20" // Default STACKIT network
	}

	// Carve parent /20 network into 4 /22 subnets, skip first (reserved)
	carvedSubnets := splitNetworkCIDR(parentCIDR, subnetSplitCount)
	if len(carvedSubnets) < subnetSplitCount {
		// Fallback: use full network if carving fails
		return []Subnet{{Name: "ocfp-0", CIDR: parentCIDR, Type: "ocfp", AvailabilityZone: cfg.Region + "-1"}}
	}

	// Use subnets [1], [2], [3] (skip [0] as reserved)
	subnets := make([]Subnet, 0, subnetReservedCount)
	azSuffixes := []string{"1", "2", "3"}

	for i := range subnetReservedCount {
		subnets = append(subnets, Subnet{
			Name:             fmt.Sprintf("ocfp-%d", i),
			CIDR:             carvedSubnets[i+1], // Skip first subnet
			Type:             "ocfp",
			AvailabilityZone: cfg.Region + "-" + azSuffixes[i],
		})
	}

	return subnets
}

// splitNetworkCIDR splits a parent CIDR into N equal subnets.
// This is a simplified version for config layer use without circular dependencies.
func splitNetworkCIDR(parentCIDR string, count int) []string {
	// Parse parent CIDR
	parts := strings.Split(parentCIDR, "/")
	if len(parts) != cidrPartCount {
		return nil
	}

	// Parse prefix length
	var prefixLen int

	_, err := fmt.Sscanf(parts[1], "%d", &prefixLen)
	if err != nil {
		return nil
	}

	// Calculate new prefix length for carved subnets
	// For /20 split into 4 subnets = /22 (20 + 2 bits)
	bitsNeeded := 0
	for (1 << bitsNeeded) < count {
		bitsNeeded++
	}

	newPrefixLen := prefixLen + bitsNeeded

	if newPrefixLen > maxPrefixLen {
		return nil // Invalid split
	}

	// Parse base IP
	ipParts := strings.Split(parts[0], ".")
	if len(ipParts) != ipOctetCount {
		return nil
	}

	var octets [ipOctetCount]int
	for i, part := range ipParts {
		_, err := fmt.Sscanf(part, "%d", &octets[i])
		if err != nil {
			return nil
		}
	}

	// Convert to uint32
	baseIP := uint32(octets[0])<<octetShift24 | uint32(octets[1])<<octetShift16 | uint32(octets[2])<<octetShift8 | uint32(octets[3])

	// Calculate subnet size
	subnetSize := uint32(1) << (maxPrefixLen - newPrefixLen)

	// Generate carved subnets
	result := make([]string, count)
	for i := range count {
		subnetIP := baseIP + uint32(i)*subnetSize
		result[i] = fmt.Sprintf("%d.%d.%d.%d/%d",
			(subnetIP>>octetShift24)&octetBitmask,
			(subnetIP>>octetShift16)&octetBitmask,
			(subnetIP>>octetShift8)&octetBitmask,
			subnetIP&octetBitmask,
			newPrefixLen,
		)
	}

	return result
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
	// Map vpc_cidr_block to Network.CIDR if specified
	if cfg.VPCCIDRBlock != "" && cfg.Network.CIDR == "" {
		cfg.Network.CIDR = cfg.VPCCIDRBlock
	}

	if cfg.Network.CIDR == "" && cfg.Network.NetworkCIDR == "" {
		cfg.Network.NetworkCIDR = defaultNetworkCIDR
	}

	if len(cfg.DNS) == 0 {
		cfg.DNS = []string{"1.1.1.1", "8.8.8.8"}
	}

	if len(cfg.Network.DNS) == 0 && len(cfg.Network.DNSServers) == 0 {
		cfg.Network.DNS = []string{"1.1.1.1", "8.8.8.8"}
		cfg.Network.DNSServers = []string{"1.1.1.1", "8.8.8.8"}
	}

	// Apply instanceType alias for AWS (prefer instanceType over flavor if both set)
	if cfg.Bastion.InstanceType != "" {
		cfg.Bastion.Flavor = cfg.Bastion.InstanceType
	}

	if cfg.Bastion.Flavor == "" {
		cfg.Bastion.Flavor = "t3.large"
	}

	if cfg.Bastion.RootDiskSize == 0 {
		cfg.Bastion.RootDiskSize = 10
	}

	if cfg.Bastion.DataDiskSize == 0 {
		cfg.Bastion.DataDiskSize = 50
	}

	if cfg.Bastion.Image == "" && cfg.Bastion.OS == "" {
		cfg.Bastion.OS = "Ubuntu"
		cfg.Bastion.OSVersion = "24.04"
		cfg.Bastion.Image = "Ubuntu 24.04"
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
	Protocol string   `json:"protocol,omitempty" mapstructure:"protocol" yaml:"protocol,omitempty"`
	Port     int      `json:"port,omitempty"     mapstructure:"port"     yaml:"port,omitempty"`
	Targets  []string `json:"targets,omitempty"  mapstructure:"targets"  yaml:"targets,omitempty"`
}

// PublicIPsConfig represents public IPs configuration.
type PublicIPsConfig struct {
	Ops       int `json:"ops,omitempty"        mapstructure:"ops"        yaml:"ops,omitempty"`
	Jumpbox   int `json:"jumpbox,omitempty"    mapstructure:"jumpbox"    yaml:"jumpbox,omitempty"`
	Router    int `json:"router,omitempty"     mapstructure:"router"     yaml:"router,omitempty"`
	CFSSH     int `json:"cf_ssh,omitempty"     mapstructure:"cf_ssh"     yaml:"cf_ssh,omitempty"`
	TCPRouter int `json:"tcp_router,omitempty" mapstructure:"tcp_router" yaml:"tcp_router,omitempty"`
}

// BucketConfig represents bucket configuration.
type BucketConfig struct {
	Name string `json:"name,omitempty" mapstructure:"name" yaml:"name,omitempty"`
}

// SaveConfig saves the config back to the YAML file.
// It updates the specific bloc within the config file while preserving other blocs.
func SaveConfig(configPath, blocName string, cfg *Config) error {
	if configPath == "" {
		configPath = determineConfigPath("")
		if configPath == "" {
			return ErrNoConfigPath
		}
	}

	// Load the entire config file to preserve all blocs
	configFileData := &ConfigFile{
		Debug:   false,
		Verbose: false,
		Blocs:   map[string]*Config{},
	}

	// Try to load existing config file
	data, err := os.ReadFile(configPath) // #nosec G304 - path is controlled
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	if len(data) > 0 {
		err = yaml.Unmarshal(data, configFileData)
		if err != nil {
			return fmt.Errorf("failed to unmarshal existing config: %w", err)
		}
	}

	// Update the specific bloc
	configFileData.Blocs[blocName] = cfg

	// Serialize Deployments back to DeploymentsData before marshaling
	// This ensures the deployments configuration is preserved when saving
	if cfg.Deployments != nil {
		cfg.DeploymentsData = serializeDeploymentSettings(cfg.Deployments)
	}

	// Marshal back to YAML
	updatedData, err := yaml.Marshal(configFileData)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write to file with secure permissions
	err = os.WriteFile(configPath, updatedData, configFileMode)
	if err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	// Invalidate cache for this config
	configMutex.Lock()

	cacheKey := configPath + ":" + blocName
	delete(cachedConfigs, cacheKey)
	configMutex.Unlock()

	return nil
}
