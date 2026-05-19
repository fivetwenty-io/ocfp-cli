// Package config handles OCFP CLI configuration file loading, validation, and bloc management.
package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"
)

// TLS mode constants for the artifacts VM.
const (
	// ArtifactsTLSModeInternalCA issues certificates from the bloc internal CA.
	ArtifactsTLSModeInternalCA = "internal-ca"
	// ArtifactsTLSModeSelfSigned generates a self-signed certificate on the VM.
	ArtifactsTLSModeSelfSigned = "self-signed"
	// ArtifactsTLSModeDisabled skips TLS entirely (not recommended for production).
	ArtifactsTLSModeDisabled = "disabled"
)

// Sentinel errors for ArtifactsConfig validation.
var (
	// ErrArtifactsRequiresPVE is returned when artifacts is enabled on a non-PVE provider.
	ErrArtifactsRequiresPVE = errors.New("artifacts VM requires provider=pve")

	// ErrArtifactsRequiresBastion is returned when artifacts is enabled but bastion is disabled.
	ErrArtifactsRequiresBastion = errors.New("artifacts VM requires bastion to be enabled")

	// ErrArtifactsTLSInternalCARequiresCA is returned when tls.mode=internal-ca but no CA is configured.
	ErrArtifactsTLSInternalCARequiresCA = errors.New("artifacts tls.mode=internal-ca requires an internal CA to be configured")

	// ErrArtifactsInvalidDiskSize is returned when data.disk_size_gib is not positive.
	ErrArtifactsInvalidDiskSize = errors.New("artifacts data.disk_size_gib must be greater than zero")

	// ErrArtifactsInvalidTLSMode is returned for an unrecognized tls.mode value.
	ErrArtifactsInvalidTLSMode = errors.New("artifacts tls.mode must be 'internal-ca', 'self-signed', or 'disabled'")
)

// rustfsDefaultDownloadURLTemplate is the template for the RustFS binary download URL.
// The literal {version} placeholder is replaced by ResolvedDownloadURL.
const rustfsDefaultDownloadURLTemplate = "https://dl.rustfs.com/artifacts/rustfs/release/rustfs-linux-x86_64-musl-v{version}.zip"

// RustfsConfig holds version and connectivity settings for the RustFS S3 server.
type RustfsConfig struct {
	// Version is the RustFS release to install (e.g., "1.0.0-beta.3").
	Version string `json:"version,omitempty" mapstructure:"version" yaml:"version,omitempty"`

	// DownloadURL overrides the default download location. When empty,
	// ResolvedDownloadURL builds the URL from Version.
	DownloadURL string `json:"download_url,omitempty" mapstructure:"download_url" yaml:"download_url,omitempty"`

	// S3Port is the port RustFS listens on for S3 API requests. Defaults to 9000.
	S3Port int `json:"s3_port,omitempty" mapstructure:"s3_port" yaml:"s3_port,omitempty"`

	// ConsolePort is the port RustFS listens on for the web console. Defaults to 9001.
	ConsolePort int `json:"console_port,omitempty" mapstructure:"console_port" yaml:"console_port,omitempty"`

	// AccessKey is the S3 access key credential for RustFS.
	AccessKey string `json:"access_key,omitempty" mapstructure:"access_key" yaml:"access_key,omitempty"`

	// SecretKey is the S3 secret key credential for RustFS.
	SecretKey string `json:"secret_key,omitempty" mapstructure:"secret_key" yaml:"secret_key,omitempty"` //nolint:gosec // field name is descriptive, not a hardcoded secret
}

// ArtifactsDataConfig controls the ZFS data disk attached to the artifacts VM.
type ArtifactsDataConfig struct {
	// DiskSizeGiB is the size in gibibytes of the ZFS data disk. Defaults to 500.
	DiskSizeGiB int `json:"disk_size_gib,omitempty" mapstructure:"disk_size_gib" yaml:"disk_size_gib,omitempty"`

	// StoragePool is the Proxmox storage pool used for the data disk. Defaults to "local-zfs".
	StoragePool string `json:"storage_pool,omitempty" mapstructure:"storage_pool" yaml:"storage_pool,omitempty"`

	// ZFSDataset overrides the ZFS dataset path. When empty, ResolvedDataset returns "rpool/<bloc-name>".
	ZFSDataset string `json:"zfs_dataset,omitempty" mapstructure:"zfs_dataset" yaml:"zfs_dataset,omitempty"`

	// Mountpoint is the filesystem path where the ZFS dataset is mounted. Defaults to "/data".
	Mountpoint string `json:"mountpoint,omitempty" mapstructure:"mountpoint" yaml:"mountpoint,omitempty"`
}

// ArtifactsTLSConfig controls TLS certificate provisioning on the artifacts VM.
type ArtifactsTLSConfig struct {
	// Mode selects the certificate source. Valid values are "internal-ca",
	// "self-signed", and "disabled". Defaults to "internal-ca".
	Mode string `json:"mode,omitempty" mapstructure:"mode" yaml:"mode,omitempty"`

	// CommonName overrides the TLS certificate common name. When empty the
	// cloud-init template derives a name from the bloc and VM hostname.
	CommonName string `json:"common_name,omitempty" mapstructure:"common_name" yaml:"common_name,omitempty"`
}

// ArtifactsConfig is an opt-in feature that provisions an ocfp-artifacts VM
// running RustFS (an S3-compatible server) on a ZFS data disk. Only supported
// on the PVE provider and requires a bastion VM.
type ArtifactsConfig struct {
	// Enabled controls whether the artifacts VM is provisioned.
	Enabled bool `json:"enabled,omitempty" mapstructure:"enabled" yaml:"enabled,omitempty"`

	// Flavor is the VM flavor (CPU/RAM profile) for the artifacts VM.
	// Defaults to "artifacts".
	Flavor string `json:"flavor,omitempty" mapstructure:"flavor" yaml:"flavor,omitempty"`

	// Template is the cloud-init image template name used to create the VM.
	// Defaults to "ubuntu-2204-cloudinit".
	Template string `json:"template,omitempty" mapstructure:"template" yaml:"template,omitempty"`

	// Rustfs holds RustFS version and port configuration.
	Rustfs RustfsConfig `json:"rustfs,omitempty" mapstructure:"rustfs" yaml:"rustfs,omitempty"`

	// Data holds ZFS data disk configuration.
	Data ArtifactsDataConfig `json:"data,omitempty" mapstructure:"data" yaml:"data,omitempty"`

	// TLS holds TLS certificate configuration.
	TLS ArtifactsTLSConfig `json:"tls,omitempty" mapstructure:"tls" yaml:"tls,omitempty"`
}

// Defaults applies default values to any field that has not been set.
// Call this during config loading before validation.
func (a *ArtifactsConfig) Defaults() {
	if a.Flavor == "" {
		a.Flavor = "artifacts"
	}

	if a.Template == "" {
		a.Template = "ubuntu-2204-cloudinit"
	}

	if a.Rustfs.Version == "" {
		a.Rustfs.Version = "1.0.0-beta.3"
	}

	if a.Rustfs.S3Port == 0 {
		a.Rustfs.S3Port = 9000
	}

	if a.Rustfs.ConsolePort == 0 {
		a.Rustfs.ConsolePort = 9001
	}

	if a.Data.DiskSizeGiB == 0 {
		a.Data.DiskSizeGiB = 500
	}

	if a.Data.StoragePool == "" {
		a.Data.StoragePool = "local-zfs"
	}

	if a.Data.Mountpoint == "" {
		a.Data.Mountpoint = "/data"
	}

	if a.TLS.Mode == "" {
		// Default to self-signed so opting into artifacts works without an
		// internal CA wired up. Operators with a CA can override to
		// internal-ca explicitly. (RustFS terminates TLS natively.)
		a.TLS.Mode = ArtifactsTLSModeSelfSigned
	}
}

// ResolvedDataset returns the ZFS dataset path to use. When Data.ZFSDataset is
// set it is returned as-is; otherwise "rpool/<blocName>" is returned.
func (a *ArtifactsConfig) ResolvedDataset(blocName string) string {
	if a.Data.ZFSDataset != "" {
		return a.Data.ZFSDataset
	}

	return "rpool/" + blocName
}

// ResolvedDownloadURL returns the RustFS download URL. When Rustfs.DownloadURL
// is set it is returned as-is; otherwise the default template URL is returned
// with {version} replaced by Rustfs.Version.
func (a *ArtifactsConfig) ResolvedDownloadURL() string {
	if a.Rustfs.DownloadURL != "" {
		return a.Rustfs.DownloadURL
	}

	return strings.ReplaceAll(rustfsDefaultDownloadURLTemplate, "{version}", a.Rustfs.Version)
}

// Validate checks that the artifacts configuration is consistent and complete.
//
// provider must be the resolved provider string (e.g., "pve").
// bastionEnabled must be true when bastion.enabled is set in the parent config.
// internalCAConfigured must be true when an internal CA is configured in the bloc.
//
// TODO: wire internalCAConfigured from actual CA detection in the parent Config.Validate call.
func (a *ArtifactsConfig) Validate(provider string, bastionEnabled bool, internalCAConfigured bool) error {
	if !a.Enabled {
		return nil
	}

	if !strings.EqualFold(strings.TrimSpace(provider), "pve") {
		return fmt.Errorf("%w: got %q", ErrArtifactsRequiresPVE, provider)
	}

	if !bastionEnabled {
		return ErrArtifactsRequiresBastion
	}

	if a.Data.DiskSizeGiB <= 0 {
		return fmt.Errorf("%w: got %d", ErrArtifactsInvalidDiskSize, a.Data.DiskSizeGiB)
	}

	mode := strings.ToLower(strings.TrimSpace(a.TLS.Mode))
	switch mode {
	case ArtifactsTLSModeInternalCA:
		if !internalCAConfigured {
			return ErrArtifactsTLSInternalCARequiresCA
		}
	case ArtifactsTLSModeSelfSigned, ArtifactsTLSModeDisabled:
		// valid, no additional check required
	default:
		return fmt.Errorf("%w: got %q", ErrArtifactsInvalidTLSMode, a.TLS.Mode)
	}

	return nil
}

// UnmarshalYAML accepts both snake_case and camelCase keys for ArtifactsConfig
// fields, mirroring the BlobstoreConfig pattern. Aliases handled:
// download_url/downloadURL, s3_port/s3Port, console_port/consolePort,
// disk_size_gib/diskSizeGiB, storage_pool/storagePool, zfs_dataset/zfsDataset,
// common_name/commonName.
func (a *ArtifactsConfig) UnmarshalYAML(data []byte) error {
	type rawRustfs struct {
		Version        string `yaml:"version,omitempty"`
		DownloadURL    string `yaml:"download_url,omitempty"`
		DownloadURLCC  string `yaml:"downloadURL,omitempty"`
		S3Port         int    `yaml:"s3_port,omitempty"`
		S3PortCC       int    `yaml:"s3Port,omitempty"`
		ConsolePort    int    `yaml:"console_port,omitempty"`
		ConsolePortCC  int    `yaml:"consolePort,omitempty"`
		AccessKey      string `yaml:"access_key,omitempty"`
		AccessKeyCC    string `yaml:"accessKey,omitempty"`
		SecretKey      string `yaml:"secret_key,omitempty"`
		SecretKeyCC    string `yaml:"secretKey,omitempty"`
	}

	type rawData struct {
		DiskSizeGiB    int    `yaml:"disk_size_gib,omitempty"`
		DiskSizeGiBCC  int    `yaml:"diskSizeGiB,omitempty"`
		StoragePool    string `yaml:"storage_pool,omitempty"`
		StoragePoolCC  string `yaml:"storagePool,omitempty"`
		ZFSDataset     string `yaml:"zfs_dataset,omitempty"`
		ZFSDatasetCC   string `yaml:"zfsDataset,omitempty"`
		Mountpoint     string `yaml:"mountpoint,omitempty"`
	}

	type rawTLS struct {
		Mode          string `yaml:"mode,omitempty"`
		CommonName    string `yaml:"common_name,omitempty"`
		CommonNameCC  string `yaml:"commonName,omitempty"`
	}

	type rawArtifacts struct {
		Enabled  bool       `yaml:"enabled,omitempty"`
		Flavor   string     `yaml:"flavor,omitempty"`
		Template string     `yaml:"template,omitempty"`
		Rustfs   rawRustfs  `yaml:"rustfs,omitempty"`
		Data     rawData    `yaml:"data,omitempty"`
		TLS      rawTLS     `yaml:"tls,omitempty"`
	}

	var raw rawArtifacts

	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("decoding artifacts config: %w", err)
	}

	a.Enabled = raw.Enabled
	a.Flavor = raw.Flavor
	a.Template = raw.Template

	a.Rustfs = RustfsConfig{
		Version:     raw.Rustfs.Version,
		DownloadURL: firstSetString(raw.Rustfs.DownloadURL, raw.Rustfs.DownloadURLCC),
		AccessKey:   firstSetString(raw.Rustfs.AccessKey, raw.Rustfs.AccessKeyCC),
		SecretKey:   firstSetString(raw.Rustfs.SecretKey, raw.Rustfs.SecretKeyCC),
	}

	if raw.Rustfs.S3Port != 0 {
		a.Rustfs.S3Port = raw.Rustfs.S3Port
	} else if raw.Rustfs.S3PortCC != 0 {
		a.Rustfs.S3Port = raw.Rustfs.S3PortCC
	}

	if raw.Rustfs.ConsolePort != 0 {
		a.Rustfs.ConsolePort = raw.Rustfs.ConsolePort
	} else if raw.Rustfs.ConsolePortCC != 0 {
		a.Rustfs.ConsolePort = raw.Rustfs.ConsolePortCC
	}

	a.Data = ArtifactsDataConfig{
		StoragePool: firstSetString(raw.Data.StoragePool, raw.Data.StoragePoolCC),
		ZFSDataset:  firstSetString(raw.Data.ZFSDataset, raw.Data.ZFSDatasetCC),
		Mountpoint:  raw.Data.Mountpoint,
	}

	if raw.Data.DiskSizeGiB != 0 {
		a.Data.DiskSizeGiB = raw.Data.DiskSizeGiB
	} else if raw.Data.DiskSizeGiBCC != 0 {
		a.Data.DiskSizeGiB = raw.Data.DiskSizeGiBCC
	}

	a.TLS = ArtifactsTLSConfig{
		Mode:       raw.TLS.Mode,
		CommonName: firstSetString(raw.TLS.CommonName, raw.TLS.CommonNameCC),
	}

	return nil
}
