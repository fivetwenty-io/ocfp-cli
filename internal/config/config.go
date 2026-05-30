// Package config handles OCFP CLI configuration file loading, validation, and bloc management.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/ocfp/ocfp-cli-go/internal/pve/netvalidate"
	"github.com/ocfp/ocfp-cli-go/internal/security"
	"github.com/spf13/viper"
)

// FQDN configuration errors.
var (
	ErrFQDNBaseInvalidType   = errors.New("fqdns.base must be a string or list of strings")
	ErrFQDNBaseElementString = errors.New("fqdns.base list element must be a string")
)

// ErrOcfpHomeNotFound is returned when the OCFP home directory cannot be determined.
var ErrOcfpHomeNotFound = errors.New("failed to determine OCFP home directory")

// OcfpHome returns the OCFP home directory path.
// It checks OCFP_HOME env var first, then falls back to $HOME/.ocfp.
func OcfpHome() string {
	if v := os.Getenv("OCFP_HOME"); v != "" {
		return v
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return filepath.Join(home, ".ocfp")
}

// OcfpBlocDir returns the directory path for a specific bloc.
func OcfpBlocDir(blocName string) string {
	return filepath.Join(OcfpHome(), blocName)
}

// OcfpSSHKeyDir returns the SSH key directory path for a specific bloc.
func OcfpSSHKeyDir(blocName string) string {
	return filepath.Join(OcfpHome(), blocName, "ssh")
}

// testSafetyGuard panics if OCFP_TEST_SAFETY_GUARD is set and OcfpHome()
// resolves to the real user home directory. This prevents tests from
// accidentally writing to ~/.ocfp.
func testSafetyGuard(operation string) {
	if os.Getenv("OCFP_TEST_SAFETY_GUARD") == "" {
		return
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	realOcfpHome := filepath.Join(home, ".ocfp")
	if OcfpHome() == realOcfpHome {
		panic(fmt.Sprintf(
			"SAFETY GUARD: %s attempted to use real home directory %s during testing. "+
				"Set OCFP_HOME to a temp directory.",
			operation, realOcfpHome,
		))
	}
}

// PVEDefaults holds global default PVE credentials that apply to any bloc
// whose corresponding credential field is not set. Bloc-level values always
// take precedence over these defaults.
type PVEDefaults struct {
	AuthToken   string `json:"auth_token"   mapstructure:"auth_token"   yaml:"auth_token,omitempty"`   //nolint:gosec // field name is descriptive, not a hardcoded secret
	TokenSecret string `json:"token_secret" mapstructure:"token_secret" yaml:"token_secret,omitempty"` //nolint:gosec // field name is descriptive, not a hardcoded secret
	Username    string `json:"username"     mapstructure:"username"     yaml:"username,omitempty"`
	Password    string `json:"password"     mapstructure:"password"     yaml:"password,omitempty"` //nolint:gosec // field name is descriptive, not a hardcoded secret
}

// ConfigFile represents the top-level configuration file structure.
//
//revive:disable-next-line:exported stutters as config.ConfigFile but renaming would break external references
type ConfigFile struct {
	Debug      bool               `mapstructure:"debug"     yaml:"debug"`
	Verbose    bool               `mapstructure:"verbose"   yaml:"verbose"`
	PVE        *PVEDefaults       `mapstructure:"pve"       yaml:"pve,omitempty"`
	Tailscale  *TailscaleConfig   `mapstructure:"tailscale"  yaml:"tailscale,omitempty"`
	Cloudflare *CloudflareConfig  `mapstructure:"cloudflare" yaml:"cloudflare,omitempty"`
	Blocs      map[string]*Config `mapstructure:"blocs"      yaml:"blocs"`
}

// Config represents a bloc configuration.
type Config struct {
	Name     string `json:"name"     mapstructure:"name"     yaml:"name,omitempty"`
	Provider string `json:"provider" mapstructure:"provider" yaml:"provider,omitempty"`
	IaaS     string `json:"iaas"     mapstructure:"iaas"     yaml:"iaas,omitempty"`
	Region   string `json:"region"   mapstructure:"region"   yaml:"region,omitempty"`
	// Nodes lists Proxmox VE cluster nodes for multi-node AZ configuration.
	// Each entry is written as a separate vault AZ entry under net/azs/{node}.
	// PVE-specific; ignored by other providers.
	Nodes []string `json:"nodes" mapstructure:"nodes" yaml:"nodes,omitempty"`
	// Prefer snake_case to match README and user configs
	ProjectID string `json:"project_id" mapstructure:"project_id" yaml:"project_id,omitempty"`
	OrgID     string `json:"org_id"     mapstructure:"org_id"     yaml:"org_id,omitempty"`
	AuthToken string `json:"auth_token" mapstructure:"auth_token" yaml:"auth_token,omitempty"` //nolint:gosec // field name is descriptive, not a hardcoded secret
	// TokenSecret holds the PVE API token secret for API token auth. Distinct from
	// Password, which is used only for username/password auth. When AuthToken is set,
	// TokenSecret must also be set; Password is ignored for auth purposes in that mode.
	TokenSecret           string `json:"token_secret"             mapstructure:"token_secret"             yaml:"token_secret,omitempty"` //nolint:gosec // field name is descriptive, not a hardcoded secret
	ServiceAccountToken   string `json:"service_account_token"    mapstructure:"service_account_token"    yaml:"service_account_token,omitempty"`
	ServiceAccountJSON    string `json:"service_account_json"     mapstructure:"service_account_json"     yaml:"service_account_json,omitempty"`
	ServiceAccountKeyPath string `json:"service_account_key_path" mapstructure:"service_account_key_path" yaml:"service_account_key_path,omitempty"`
	// Optional: override STACKIT API endpoint (e.g., https://iaas.api.stackit.cloud)
	APIEndpoint string `json:"api_endpoint" mapstructure:"api_endpoint" yaml:"api_endpoint,omitempty"`
	// VerifySSL controls TLS certificate verification for provider API calls.
	// PVE-specific. Defaults to false (skip verification) so self-signed PVE
	// certs work out of the box. Set true when targeting a PVE host with a
	// CA-signed certificate to fail-closed on cert mismatches.
	VerifySSL bool `json:"verify_ssl" mapstructure:"verify_ssl" yaml:"verify_ssl,omitempty"`
	// IsoStorage is the PVE storage pool that hosts ISO content and
	// cloud-init snippets. PVE-specific. Used by snippet upload and by
	// template auto-provisioning to stage downloaded cloud images.
	IsoStorage string `json:"iso_storage" mapstructure:"iso_storage" yaml:"iso_storage,omitempty"`
	// VMStorage is the PVE storage pool used for ephemeral (root) VM disks.
	// PVE-specific. Maps to pve.vm_storage in the bosh-pve-cpi-release job
	// properties. When empty, configureCPI falls back to
	// Artifacts.Data.StoragePool, then to the hardcoded default "local-lvm".
	// Example: "data" (lvmthin pool), "local-lvm" (default thin LVM).
	VMStorage string `json:"vm_storage" mapstructure:"vm_storage" yaml:"vm_storage,omitempty"`
	// DiskStorage is the PVE storage pool used for persistent BOSH disks.
	// PVE-specific. Maps to pve.disk_storage in the bosh-pve-cpi-release job
	// properties. When empty, configureCPI falls back to
	// Artifacts.Data.StoragePool, then to the hardcoded default "zfs-1".
	// NOTE: zfspool backends require disk_format: raw — qcow2 is not supported.
	// Example: "zfs-1" (zfspool), "local-lvm" (lvmthin).
	DiskStorage      string                      `json:"disk_storage"        mapstructure:"disk_storage"        yaml:"disk_storage,omitempty"`
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
	Artifacts        ArtifactsConfig             `json:"artifacts"           mapstructure:"artifacts"           yaml:"artifacts,omitempty"`
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

	// AWS CPI defaults
	DefaultInstanceType string `json:"default_instance_type" mapstructure:"default_instance_type" yaml:"default_instance_type,omitempty"`
	DefaultDiskType     string `json:"default_disk_type"     mapstructure:"default_disk_type"     yaml:"default_disk_type,omitempty"`

	// Buckets configuration
	Buckets []BucketConfig `json:"buckets" mapstructure:"buckets" yaml:"buckets,omitempty"`

	// Blobstore policies (optional, for object storage buckets)
	Blobstore BlobstoreConfig `json:"blobstore" mapstructure:"blobstore" yaml:"blobstore,omitempty"`

	// SSH Keys storage for portability (bloc-name -> ed25519 private key)
	Keys map[string]string `json:"keys" mapstructure:"keys" yaml:"keys,omitempty"`

	// Tailscale carries per-bloc tailscale configuration. Per-bloc values
	// take precedence over the global ConfigFile.Tailscale defaults via
	// mergeTailscaleDefaults at load time.
	Tailscale *TailscaleConfig `json:"tailscale,omitempty" mapstructure:"tailscale" yaml:"tailscale,omitempty"`

	// Cloudflare carries per-bloc Cloudflare Tunnel configuration. Per-bloc
	// values take precedence over the global ConfigFile.Cloudflare defaults
	// via mergeCloudflareDefaults at load time.
	Cloudflare *CloudflareConfig `json:"cloudflare,omitempty" mapstructure:"cloudflare" yaml:"cloudflare,omitempty"`

	// CFCloudConfigCIDR is the subnet CIDR written into the CF cloud-config
	// network section. It must match the BOSH director network CIDR
	// (Network.CIDR) to avoid the Tailscale LAN route hazard. PVE-specific.
	// When empty, network pairing validation is skipped.
	CFCloudConfigCIDR string `json:"cf_cloud_config_cidr,omitempty" mapstructure:"cf_cloud_config_cidr" yaml:"cf_cloud_config_cidr,omitempty"`

	// VmidRangeStart is the lower bound (inclusive) of the PVE VMID range the
	// BOSH CPI may allocate. PVE-specific. When zero (unset), configureCPI uses
	// the default value 100 so the CPI never clobbers operator-reserved IDs.
	// Maps to vmid_range_start in the bosh-pve-cpi-release job properties.
	VmidRangeStart int `json:"vmid_range_start,omitempty" mapstructure:"vmid_range_start" yaml:"vmid_range_start,omitempty"`

	// VmidRangeEnd is the upper bound (inclusive) of the PVE VMID range the
	// BOSH CPI may allocate. PVE-specific. When zero (unset), configureCPI uses
	// the default value 5999. Must be greater than VmidRangeStart when both are
	// non-zero. Maps to vmid_range_end in the bosh-pve-cpi-release job properties.
	VmidRangeEnd int `json:"vmid_range_end,omitempty" mapstructure:"vmid_range_end" yaml:"vmid_range_end,omitempty"`

	// CfMaxInFlight is the maximum number of concurrent create_vm calls the
	// BOSH director issues per instance group during a CF deploy. PVE-specific.
	// Maps to cf_max_in_flight in the cloud-config update block and the
	// ops-serialize-deploy.yml ops file. Resolution order (Decision D1):
	//   1. This field when > 0 (explicit operator override).
	//   2. Live PVE node CPU count clamped to [4, 16] (requires API connectivity).
	//   3. Default 12 (sized to PVE's default storage worker thread count).
	// Set to 0 (or omit) to let the CLI derive the value automatically.
	CfMaxInFlight int `json:"cf_max_in_flight,omitempty" mapstructure:"cf_max_in_flight" yaml:"cf_max_in_flight,omitempty"`
}

// BlobstoreMode constants for PVE bloc-scoped blobstore configuration.
const (
	// BlobstoreModeLocal indicates no external object storage; bucket creation is skipped.
	BlobstoreModeLocal = "local"
	// BlobstoreModeExternal indicates an S3-compatible blobstore (Ceph RGW, RustFS, etc.).
	BlobstoreModeExternal = "external"
	// BlobstoreDefaultRegion is the default S3 region used when none is configured.
	BlobstoreDefaultRegion = "us-east-1"
)

// ErrBlobstoreEndpointRequired is returned when mode=external without an endpoint.
var ErrBlobstoreEndpointRequired = errors.New("blobstore.endpoint is required when mode is external")

// ErrBlobstoreCredentialsRequired is returned when mode=external without credentials.
var ErrBlobstoreCredentialsRequired = errors.New("blobstore.access_key and blobstore.secret_key are required when mode is external")

// ErrBlobstoreInvalidMode is returned for an unrecognized blobstore mode value.
var ErrBlobstoreInvalidMode = errors.New("blobstore.mode must be 'local' or 'external'")

// BlobstoreConfig controls versioning/lifecycle policies for expected buckets
// AND provides bloc-scoped blobstore mode/endpoint/credentials for providers
// (PVE) that lack a native object-storage layer.
//
// Mode defaults to "local". External mode points at any S3-compatible service
// (Ceph RGW, RustFS, etc.) and requires endpoint + access_key + secret_key.
type BlobstoreConfig struct {
	EnablePolicies bool `json:"enablePolicies,omitempty" mapstructure:"enablePolicies" yaml:"enablePolicies,omitempty"`

	// Per-bucket overrides
	BoshBlobstore BucketSettings `json:"boshBlobstore,omitempty" mapstructure:"boshBlobstore" yaml:"boshBlobstore,omitempty"`
	CFBuildpacks  BucketSettings `json:"cfBuildpacks,omitempty"  mapstructure:"cfBuildpacks"  yaml:"cfBuildpacks,omitempty"`
	CFDroplets    BucketSettings `json:"cfDroplets,omitempty"    mapstructure:"cfDroplets"    yaml:"cfDroplets,omitempty"`
	CFAppPackages BucketSettings `json:"cfAppPackages,omitempty" mapstructure:"cfAppPackages" yaml:"cfAppPackages,omitempty"`

	// Bloc-scoped S3-compatible blobstore configuration (used primarily by
	// the PVE provider, which has no native object store).
	Mode      string `json:"mode,omitempty"       mapstructure:"mode"       yaml:"mode,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"   mapstructure:"endpoint"   yaml:"endpoint,omitempty"`
	Region    string `json:"region,omitempty"     mapstructure:"region"     yaml:"region,omitempty"`
	AccessKey string `json:"access_key,omitempty" mapstructure:"access_key" yaml:"access_key,omitempty"`
	SecretKey string `json:"secret_key,omitempty" mapstructure:"secret_key" yaml:"secret_key,omitempty"` //nolint:gosec // field name is descriptive
	CACert    string `json:"ca_cert,omitempty"    mapstructure:"ca_cert"    yaml:"ca_cert,omitempty"`
	PathStyle *bool  `json:"path_style,omitempty" mapstructure:"path_style" yaml:"path_style,omitempty"`
}

// UnmarshalYAML accepts both camelCase and snake_case keys for the new bloc-
// scoped blobstore fields, while preserving the existing camelCase policy
// fields. Aliases handled: access_key/accessKey, secret_key/secretKey,
// path_style/pathStyle, ca_cert/caCert.
//
// Validation is intentionally NOT performed here; callers run Validate after
// load so the error path matches the rest of the config package.
func (b *BlobstoreConfig) UnmarshalYAML(data []byte) error {
	type rawBlobstore struct {
		EnablePolicies bool           `yaml:"enablePolicies,omitempty"`
		BoshBlobstore  BucketSettings `yaml:"boshBlobstore,omitempty"`
		CFBuildpacks   BucketSettings `yaml:"cfBuildpacks,omitempty"`
		CFDroplets     BucketSettings `yaml:"cfDroplets,omitempty"`
		CFAppPackages  BucketSettings `yaml:"cfAppPackages,omitempty"`

		Mode        string `yaml:"mode,omitempty"`
		Endpoint    string `yaml:"endpoint,omitempty"`
		Region      string `yaml:"region,omitempty"`
		AccessKey   string `yaml:"access_key,omitempty"`
		AccessKeyCC string `yaml:"accessKey,omitempty"`
		SecretKey   string `yaml:"secret_key,omitempty"`
		SecretKeyCC string `yaml:"secretKey,omitempty"`
		CACert      string `yaml:"ca_cert,omitempty"`
		CACertCC    string `yaml:"caCert,omitempty"`
		PathStyle   *bool  `yaml:"path_style,omitempty"`
		PathStyleCC *bool  `yaml:"pathStyle,omitempty"`
	}

	var raw rawBlobstore

	err := yaml.Unmarshal(data, &raw)
	if err != nil {
		return fmt.Errorf("decoding blobstore config: %w", err)
	}

	b.EnablePolicies = raw.EnablePolicies
	b.BoshBlobstore = raw.BoshBlobstore
	b.CFBuildpacks = raw.CFBuildpacks
	b.CFDroplets = raw.CFDroplets
	b.CFAppPackages = raw.CFAppPackages

	b.Mode = strings.ToLower(strings.TrimSpace(raw.Mode))
	b.Endpoint = raw.Endpoint
	b.Region = raw.Region
	b.AccessKey = firstSetString(raw.AccessKey, raw.AccessKeyCC)
	b.SecretKey = firstSetString(raw.SecretKey, raw.SecretKeyCC)
	b.CACert = firstSetString(raw.CACert, raw.CACertCC)

	switch {
	case raw.PathStyle != nil:
		b.PathStyle = raw.PathStyle
	case raw.PathStyleCC != nil:
		b.PathStyle = raw.PathStyleCC
	}

	return nil
}

// ResolvedMode returns the effective blobstore mode, defaulting to local when
// unset.
func (b *BlobstoreConfig) ResolvedMode() string {
	if b == nil {
		return BlobstoreModeLocal
	}

	mode := strings.ToLower(strings.TrimSpace(b.Mode))
	if mode == "" {
		return BlobstoreModeLocal
	}

	return mode
}

// ResolvedPathStyle returns the effective path_style setting, defaulting to
// true (Ceph/RustFS friendly) when not configured.
func (b *BlobstoreConfig) ResolvedPathStyle() bool {
	if b == nil || b.PathStyle == nil {
		return true
	}

	return *b.PathStyle
}

// ResolvedRegion returns the configured region or the default region when
// unset.
func (b *BlobstoreConfig) ResolvedRegion() string {
	if b == nil || strings.TrimSpace(b.Region) == "" {
		return BlobstoreDefaultRegion
	}

	return b.Region
}

// Validate ensures external mode has required fields.
func (b *BlobstoreConfig) Validate() error {
	if b == nil {
		return nil
	}

	mode := b.ResolvedMode()
	switch mode {
	case BlobstoreModeLocal, "":
		return nil
	case BlobstoreModeExternal:
		if strings.TrimSpace(b.Endpoint) == "" {
			return ErrBlobstoreEndpointRequired
		}

		if strings.TrimSpace(b.AccessKey) == "" || strings.TrimSpace(b.SecretKey) == "" {
			return ErrBlobstoreCredentialsRequired
		}

		return nil
	default:
		return fmt.Errorf("%w: %q", ErrBlobstoreInvalidMode, b.Mode)
	}
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

	// providerStackIT is the canonical lower-case provider name for STACKIT.
	providerStackIT = "stackit"
	// dnsCloudflare is Cloudflare's primary resolver, used as the default DNS.
	dnsCloudflare = "1.1.1.1"
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

// UnmarshalYAML accepts the historical snake_case key network_cidr alongside
// the documented cidr / networkCidr forms. goccy/go-yaml matches struct tags
// case-sensitively, so without this hook a config that writes network_cidr
// silently falls back to the package default and the bastion lands on the
// wrong subnet — a trap that has bitten PVE bridge-mode deployments.
//
// Precedence when more than one key is set: cidr > networkCidr > network_cidr.
// NetworkCIDR is populated for downstream consumers that look only at that
// field.
func (n *NetworkConfig) UnmarshalYAML(data []byte) error {
	type rawNetwork struct {
		ID             string   `yaml:"id,omitempty"`
		Name           string   `yaml:"name,omitempty"`
		CIDR           string   `yaml:"cidr,omitempty"`
		NetworkCIDR    string   `yaml:"networkCidr,omitempty"`
		NetworkCIDRSC  string   `yaml:"network_cidr,omitempty"`
		SubnetID       string   `yaml:"subnetId,omitempty"`
		SubnetIDSC     string   `yaml:"subnet_id,omitempty"`
		DNS            []string `yaml:"dns,omitempty"`
		DNSServers     []string `yaml:"dnsServers,omitempty"`
		DNSServersSC   []string `yaml:"dns_servers,omitempty"`
		SubnetStrategy string   `yaml:"subnetStrategy,omitempty"`
		Subnets        []Subnet `yaml:"subnets,omitempty"`
	}

	var raw rawNetwork

	err := yaml.Unmarshal(data, &raw)
	if err != nil {
		return fmt.Errorf("decoding network config: %w", err)
	}

	n.ID = raw.ID
	n.Name = raw.Name
	n.CIDR = firstSetString(raw.CIDR, raw.NetworkCIDR, raw.NetworkCIDRSC)
	n.NetworkCIDR = firstSetString(raw.NetworkCIDR, raw.NetworkCIDRSC, raw.CIDR)
	n.SubnetID = firstSetString(raw.SubnetID, raw.SubnetIDSC)
	n.DNS = raw.DNS

	if len(raw.DNSServers) > 0 {
		n.DNSServers = raw.DNSServers
	} else {
		n.DNSServers = raw.DNSServersSC
	}

	n.SubnetStrategy = raw.SubnetStrategy
	n.Subnets = raw.Subnets

	return nil
}

func firstSetString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}

// mergePVEDefaults fills empty PVE credential fields on bloc from defaults.
// Bloc values take precedence; defaults supply values only when the bloc
// field is empty. Non-credential fields on bloc are never modified.
// No-ops when either argument is nil.
func mergePVEDefaults(bloc *Config, defaults *PVEDefaults) {
	if bloc == nil || defaults == nil {
		return
	}

	bloc.AuthToken = firstSetString(bloc.AuthToken, defaults.AuthToken)
	bloc.TokenSecret = firstSetString(bloc.TokenSecret, defaults.TokenSecret)
	bloc.Username = firstSetString(bloc.Username, defaults.Username)
	bloc.Password = firstSetString(bloc.Password, defaults.Password)
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
	Brews     OverrideSets `json:"brews,omitempty"     mapstructure:"brews"     yaml:"brews,omitempty"`
	// Per-item override maps (by name)
	ToolOverrides     map[string]ToolOverride     `json:"toolOverrides,omitempty"     mapstructure:"toolOverrides"     yaml:"toolOverrides,omitempty"`
	CFPluginOverrides map[string]CFPluginOverride `json:"cfPluginOverrides,omitempty" mapstructure:"cfPluginOverrides" yaml:"cfPluginOverrides,omitempty"`
	SnapOverrides     map[string]SnapOverride     `json:"snapOverrides,omitempty"     mapstructure:"snapOverrides"     yaml:"snapOverrides,omitempty"`
	BrewOverrides     map[string]BrewOverride     `json:"brewOverrides,omitempty"     mapstructure:"brewOverrides"     yaml:"brewOverrides,omitempty"`
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

// BrewOverride allows overriding brew package properties.
type BrewOverride struct {
	Tap          string `json:"tap,omitempty"          mapstructure:"tap"          yaml:"tap,omitempty"`
	Cask         *bool  `json:"cask,omitempty"         mapstructure:"cask"         yaml:"cask,omitempty"`
	Version      string `json:"version,omitempty"      mapstructure:"version"      yaml:"version,omitempty"`
	Options      string `json:"options,omitempty"      mapstructure:"options"      yaml:"options,omitempty"`
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
func (f *FQDNConfig) UnmarshalYAML(data []byte) error {
	type aux struct {
		Base interface{}       `yaml:"base"`
		Mgmt map[string]string `yaml:"mgmt"`
		OCF  map[string]string `yaml:"ocf"`
	}

	var raw aux

	decodeErr := yaml.Unmarshal(data, &raw)
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

	// If configPath is empty, return an error
	if configPath == "" {
		return nil, ErrNoConfigFile
	}

	// If blocName is empty, return an error
	if blocName == "" {
		return nil, ErrNoBlocName
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

	// Merge keys from state file for portability
	mergeKeysFromState(cfg, blocName)

	// Cache the configuration
	cacheConfiguration(configPath, blocName, cfg)

	return cfg, nil
}

// mergeKeysFromState loads keys from state.yml and merges them into the config.
// State file keys take precedence over config file keys.
func mergeKeysFromState(cfg *Config, blocName string) {
	state, stateErr := LoadState()
	if stateErr != nil {
		return
	}

	blocState, ok := state.Blocs[blocName]
	if !ok || blocState.Keys == nil {
		return
	}

	if cfg.Keys == nil {
		cfg.Keys = make(map[string]string)
	}

	for k, v := range blocState.Keys {
		cfg.Keys[k] = v
	}
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
			Brews:             OverrideSets{Enable: []string{}, Disable: []string{}},
			ToolOverrides:     map[string]ToolOverride{},
			CFPluginOverrides: map[string]CFPluginOverride{},
			SnapOverrides:     map[string]SnapOverride{},
			BrewOverrides:     map[string]BrewOverride{},
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

	mergePVEDefaults(blocConfig, configFileData.PVE)
	mergeTailscaleDefaults(blocConfig, configFileData.Tailscale)
	mergeCloudflareDefaults(blocConfig, configFileData.Cloudflare)

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

// ListBlocNames returns the sorted names of all blocs defined in the config file.
// Returns an empty slice (not an error) if the file doesn't exist or has no blocs.
func ListBlocNames(configFile string) ([]string, error) {
	path := determineConfigPath(configFile)
	if path == "" {
		return nil, nil
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}

	var cfgFile ConfigFile

	err := loadFromFile(path, &cfgFile)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(cfgFile.Blocs))
	for name := range cfgFile.Blocs {
		names = append(names, name)
	}

	sort.Strings(names)

	return names, nil
}

// determineConfigPath determines the configuration file path.
func determineConfigPath(configFile string) string {
	// Priority 1: Explicit config file
	if configFile != "" {
		return configFile
	}

	// Priority 2: Default config file at ~/.ocfp/config.yml
	ocfpHome := OcfpHome()
	if ocfpHome != "" {
		defaultPath := filepath.Join(ocfpHome, "config.yml")

		_, err := os.Stat(defaultPath)
		if err == nil {
			return defaultPath
		}
	}

	// Priority 3: Check in local config/config.yml
	_, err := os.Stat("config/config.yml")
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
	case providerStackIT:
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
	cfg.Artifacts.Defaults()

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
		cfg.Genesis.Branch = "v3.2.x-dev"
	}

	if cfg.Genesis.VersionPrefix == "" {
		// Pack version must be semver-parseable (genesis `semver`/`new_enough`
		// reject "x"/"-dev"); the dev line is identified by Branch, not version.
		cfg.Genesis.VersionPrefix = "3.2.0"
	}
}

// applyBastionGenesisDefaults applies defaults for the bastion-specific Genesis configuration.
func applyBastionGenesisDefaults(cfg *Config) {
	if !cfg.Bastion.Genesis.Enabled {
		return
	}

	if cfg.Bastion.Genesis.Branch == "" {
		cfg.Bastion.Genesis.Branch = "v3.2.x-dev"
	}

	if cfg.Bastion.Genesis.VersionPrefix == "" {
		cfg.Bastion.Genesis.VersionPrefix = "3.2.0"
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
		cfg.DNS = []string{dnsCloudflare, "8.8.8.8"}
	}

	if len(cfg.Network.DNS) == 0 && len(cfg.Network.DNSServers) == 0 {
		cfg.Network.DNS = []string{dnsCloudflare, "8.8.8.8"}
		cfg.Network.DNSServers = []string{dnsCloudflare, "8.8.8.8"}
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
			cfg.Subnets = generateDefaultSubnets(cfg)
		}
	}
}

// FormatAvailabilityZone returns an AZ name for the given provider, region, and index.
// AWS/GCP/Azure use letter suffixes (e.g., us-east-1a, us-east-1b).
// STACKIT uses numeric suffixes with a dash (e.g., eu01-1, eu01-2).
func FormatAvailabilityZone(provider, region string, index int) string {
	if strings.EqualFold(provider, providerStackIT) {
		return fmt.Sprintf("%s-%d", region, index+1)
	}

	// AWS, GCP, Azure, and other providers use letter suffixes
	if index < 0 || index > 25 {
		return fmt.Sprintf("%s-%d", region, index)
	}

	suffix := string('a' + int32(index)) //nolint:gosec // bounds checked above

	return region + suffix
}

// generateDefaultSubnets generates 3 ocfp subnets carved from the network CIDR.
// This matches the Perl implementation behavior of creating ocfp-0, ocfp-1, ocfp-2
// subnets across 3 availability zones. AZ names are formatted per provider convention.
func generateDefaultSubnets(cfg *Config) []Subnet {
	// Determine parent network CIDR
	parentCIDR := cfg.Network.CIDR
	if parentCIDR == "" {
		parentCIDR = cfg.Network.NetworkCIDR
	}

	if parentCIDR == "" {
		parentCIDR = "10.4.0.0/20" // Default STACKIT network
	}

	// Determine provider for AZ formatting
	provider := cfg.Provider
	if provider == "" {
		provider = cfg.IaaS
	}

	// Carve parent /20 network into 4 /22 subnets, skip first (reserved)
	carvedSubnets := splitNetworkCIDR(parentCIDR, subnetSplitCount)
	if len(carvedSubnets) < subnetSplitCount {
		// Fallback: use full network if carving fails
		return []Subnet{{Name: "ocfp-0", CIDR: parentCIDR, Type: "ocfp", AvailabilityZone: FormatAvailabilityZone(provider, cfg.Region, 0)}}
	}

	// Use subnets [1], [2], [3] (skip [0] as reserved)
	subnets := make([]Subnet, 0, subnetReservedCount)

	for i := range subnetReservedCount {
		subnets = append(subnets, Subnet{
			Name:             fmt.Sprintf("ocfp-%d", i),
			CIDR:             carvedSubnets[i+1], // Skip first subnet
			Type:             "ocfp",
			AvailabilityZone: FormatAvailabilityZone(provider, cfg.Region, i),
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

		if octets[i] < 0 || octets[i] > octetBitmask {
			return nil
		}
	}

	// Convert to uint32 — each octet is now bounded to [0, 255] so the
	// cast cannot silently wrap.
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
		cfg.DNS = []string{dnsCloudflare, "8.8.8.8"}
	}

	if len(cfg.Network.DNS) == 0 && len(cfg.Network.DNSServers) == 0 {
		cfg.Network.DNS = []string{dnsCloudflare, "8.8.8.8"}
		cfg.Network.DNSServers = []string{dnsCloudflare, "8.8.8.8"}
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
	validProviders := []string{providerStackIT, "openstack", "aws", "azure", "gcp", "vmware", "pve"}
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

	// Validate bloc-scoped blobstore config (mode/endpoint/credentials).
	if err := cfg.Blobstore.Validate(); err != nil {
		return fmt.Errorf("blobstore config: %w", err)
	}

	// Validate the merged tailscale config (mutual exclusion of literal
	// auth_key vs auth_key_vault_path).
	if err := cfg.Tailscale.Validate(); err != nil {
		return err
	}

	if err := cfg.Cloudflare.Validate(); err != nil {
		return err
	}

	// Validate opt-in ocfp-artifacts VM config.
	// Bastion-enabled proxy: a configured Flavor implies bastion provisioning.
	// internalCAConfigured is unconditionally true: the bloc CA is generated on
	// demand by bootstrap (vault.LoadOrGenerateBlocCA), so the validator never
	// has to refuse internal-ca mode for missing config.
	bastionEnabled := cfg.Bastion.Flavor != ""
	if err := cfg.Artifacts.Validate(cfg.Provider, bastionEnabled, true); err != nil {
		return fmt.Errorf("artifacts config: %w", err)
	}

	// Validate PVE credential configuration (Decision D2):
	// - Neither auth mode configured: error (cannot authenticate).
	// - Both auth modes configured: warn; token wins by convention.
	// - Exactly one auth mode configured: ok.
	if strings.EqualFold(cfg.Provider, "pve") {
		if err := validatePVE(cfg); err != nil {
			return err
		}
	}

	return nil
}

// validatePVE runs all PVE-specific validation steps: auth mode, VMID range,
// and director/CF cloud-config CIDR pairing.
func validatePVE(cfg *Config) error {
	if err := validatePVEAuth(cfg, os.Stderr); err != nil {
		return err
	}

	if err := validatePVEVMIDRange(cfg); err != nil {
		return err
	}

	// Validate that the director network CIDR and CF cloud-config CIDR
	// refer to the same network when both are present. A mismatch
	// re-triggers the Tailscale LAN route hazard on PVE blocs.
	// Either field absent means the operator has not applied both
	// overrides yet — skip validation rather than fail.
	directorCIDR := cfg.Network.CIDR
	if directorCIDR == "" {
		directorCIDR = cfg.Network.NetworkCIDR
	}

	if directorCIDR != "" && cfg.CFCloudConfigCIDR != "" {
		if err := netvalidate.ValidateNetworkPairing(directorCIDR, cfg.CFCloudConfigCIDR); err != nil {
			return fmt.Errorf("invalid configuration: %w", err)
		}
	}

	return nil
}

// validatePVEAuth checks PVE-specific credential configuration.
//
// Auth modes:
//   - API token auth: both auth_token and token_secret non-empty.
//   - User/password auth: both username and password non-empty.
//
// Rules (Decision D2):
//   - Neither mode configured → ErrPVEAuthRequired.
//   - Both modes configured → write warning to warnW; token wins at runtime (not an error).
//   - Exactly one mode configured → valid.
//
// warnW receives any diagnostic text; pass os.Stderr in production callers and
// a *bytes.Buffer in tests to avoid global stderr mutation.
func validatePVEAuth(cfg *Config, warnW io.Writer) error {
	apiTokenMode := cfg.AuthToken != "" && cfg.TokenSecret != ""
	userPassMode := cfg.Username != "" && cfg.Password != ""

	switch {
	case !apiTokenMode && !userPassMode:
		return ErrPVEAuthRequired

	case apiTokenMode && userPassMode:
		// Both auth modes set; API token wins at runtime per CPI auth-selection
		// logic. Log a warning so operators know the password credentials are
		// ignored. This is not an error because the config is functional.
		fmt.Fprintf(warnW, "WARNING: pve config: both api token auth (auth_token+token_secret) and password auth (username+password) are configured; api token takes precedence — password credentials will be ignored\n")
	}

	return nil
}

// pveVMIDRangeMax is the maximum VMID PVE supports.
const pveVMIDRangeMax = 999999999

// validatePVEVMIDRange validates the VMID range configuration for PVE blocs.
//
// Rules (both fields are optional; zero means "use default"):
//   - Both zero: valid (defaults 100/5999 apply at CPI config time).
//   - VmidRangeStart < 0 or VmidRangeEnd < 0: invalid.
//   - Either field non-zero and start >= end: invalid.
//   - Either field > pveVMIDRangeMax: invalid.
//
// Only one auth-unrelated error sentinel is used so callers can errors.Is-check
// without parsing the message.
func validatePVEVMIDRange(cfg *Config) error {
	start := cfg.VmidRangeStart
	end := cfg.VmidRangeEnd

	// Both zero: defaults apply later; no error.
	if start == 0 && end == 0 {
		return nil
	}

	// Negative values are always invalid.
	if start < 0 || end < 0 {
		return ErrPVEVMIDRangeInvalid
	}

	// Upper-bound sanity: PVE rejects VMIDs above 999999999.
	if start > pveVMIDRangeMax || end > pveVMIDRangeMax {
		return ErrPVEVMIDRangeInvalid
	}

	// When either is non-zero, end must be strictly greater than start.
	// This catches: start=200, end=0 (end would default to 5999 < start if
	// start is intentionally high) — treated as invalid to force explicit config.
	if end <= start {
		return ErrPVEVMIDRangeInvalid
	}

	return nil
}

// GetLogDir returns the log directory path.
func GetLogDir() string {
	ocfpHome := OcfpHome()
	if ocfpHome == "" {
		return ""
	}

	return filepath.Join(ocfpHome, "logs")
}

// GetSSHKeyPath returns the SSH key path for a bloc.
func GetSSHKeyPath(blocName string, keypair string) string {
	ocfpHome := OcfpHome()
	if ocfpHome == "" {
		return ""
	}

	// Try new standard location first
	newPath := filepath.Join(ocfpHome, "keys", blocName+"-bastion", "id_rsa")

	_, err := os.Stat(newPath)
	if err == nil {
		return newPath
	}

	// Try legacy location in ~/.ssh
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

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
