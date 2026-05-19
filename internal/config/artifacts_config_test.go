package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// TestArtifactsConfigDefaults verifies that Defaults fills in every zero field.
func TestArtifactsConfigDefaults(t *testing.T) {
	var a ArtifactsConfig
	a.Defaults()

	tests := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"Flavor", a.Flavor, "artifacts"},
		{"Template", a.Template, "ubuntu-2204-cloudinit"},
		{"Rustfs.Version", a.Rustfs.Version, "1.0.0-beta.3"},
		{"Rustfs.S3Port", a.Rustfs.S3Port, 9000},
		{"Rustfs.ConsolePort", a.Rustfs.ConsolePort, 9001},
		{"Data.DiskSizeGiB", a.Data.DiskSizeGiB, 500},
		{"Data.StoragePool", a.Data.StoragePool, "local-zfs"},
		{"Data.Mountpoint", a.Data.Mountpoint, "/data"},
		{"TLS.Mode", a.TLS.Mode, ArtifactsTLSModeSelfSigned},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %v, want %v", tc.got, tc.want)
			}
		})
	}
}

// TestArtifactsConfigDefaultsIdempotent verifies that Defaults does not overwrite
// explicitly set values.
func TestArtifactsConfigDefaultsIdempotent(t *testing.T) {
	a := ArtifactsConfig{
		Flavor:   "custom-flavor",
		Template: "debian-11",
		Rustfs: RustfsConfig{
			Version:     "2.0.0",
			S3Port:      8000,
			ConsolePort: 8001,
		},
		Data: ArtifactsDataConfig{
			DiskSizeGiB: 100,
			StoragePool: "ceph",
			Mountpoint:  "/mnt/data",
		},
		TLS: ArtifactsTLSConfig{
			Mode: ArtifactsTLSModeSelfSigned,
		},
	}

	a.Defaults()

	if a.Flavor != "custom-flavor" {
		t.Errorf("Defaults overwrote Flavor: got %q", a.Flavor)
	}

	if a.Rustfs.Version != "2.0.0" {
		t.Errorf("Defaults overwrote Rustfs.Version: got %q", a.Rustfs.Version)
	}

	if a.Rustfs.S3Port != 8000 {
		t.Errorf("Defaults overwrote Rustfs.S3Port: got %d", a.Rustfs.S3Port)
	}

	if a.Data.DiskSizeGiB != 100 {
		t.Errorf("Defaults overwrote Data.DiskSizeGiB: got %d", a.Data.DiskSizeGiB)
	}

	if a.TLS.Mode != ArtifactsTLSModeSelfSigned {
		t.Errorf("Defaults overwrote TLS.Mode: got %q", a.TLS.Mode)
	}
}

// TestArtifactsConfigYAMLRoundtripSnakeCase verifies marshal/unmarshal with snake_case keys.
func TestArtifactsConfigYAMLRoundtripSnakeCase(t *testing.T) {
	input := `
enabled: true
flavor: artifacts
template: ubuntu-2204-cloudinit
rustfs:
  version: 1.0.0-beta.3
  download_url: https://example.com/rustfs.zip
  s3_port: 9000
  console_port: 9001
  access_key: myaccesskey
  secret_key: mysecretkey
data:
  disk_size_gib: 500
  storage_pool: local-zfs
  zfs_dataset: rpool/mybloc
  mountpoint: /data
tls:
  mode: self-signed
  common_name: artifacts.example.com
`

	var a ArtifactsConfig
	if err := yaml.Unmarshal([]byte(input), &a); err != nil {
		t.Fatalf("unmarshal snake_case: %v", err)
	}

	if !a.Enabled {
		t.Error("Enabled: got false, want true")
	}

	if a.Rustfs.DownloadURL != "https://example.com/rustfs.zip" {
		t.Errorf("Rustfs.DownloadURL: got %q", a.Rustfs.DownloadURL)
	}

	if a.Rustfs.S3Port != 9000 {
		t.Errorf("Rustfs.S3Port: got %d", a.Rustfs.S3Port)
	}

	if a.Rustfs.ConsolePort != 9001 {
		t.Errorf("Rustfs.ConsolePort: got %d", a.Rustfs.ConsolePort)
	}

	if a.Rustfs.AccessKey != "myaccesskey" {
		t.Errorf("Rustfs.AccessKey: got %q", a.Rustfs.AccessKey)
	}

	if a.Rustfs.SecretKey != "mysecretkey" {
		t.Errorf("Rustfs.SecretKey: got %q", a.Rustfs.SecretKey)
	}

	if a.Data.DiskSizeGiB != 500 {
		t.Errorf("Data.DiskSizeGiB: got %d", a.Data.DiskSizeGiB)
	}

	if a.Data.ZFSDataset != "rpool/mybloc" {
		t.Errorf("Data.ZFSDataset: got %q", a.Data.ZFSDataset)
	}

	if a.TLS.Mode != "self-signed" {
		t.Errorf("TLS.Mode: got %q", a.TLS.Mode)
	}

	if a.TLS.CommonName != "artifacts.example.com" {
		t.Errorf("TLS.CommonName: got %q", a.TLS.CommonName)
	}

	// Marshal back and spot-check key presence.
	out, err := yaml.Marshal(&a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	outStr := string(out)
	for _, want := range []string{"enabled: true", "flavor: artifacts", "version: 1.0.0-beta.3"} {
		if !strings.Contains(outStr, want) {
			t.Errorf("marshaled YAML missing %q", want)
		}
	}
}

// TestArtifactsConfigYAMLRoundtripCamelCase verifies unmarshal with camelCase keys.
func TestArtifactsConfigYAMLRoundtripCamelCase(t *testing.T) {
	input := `
enabled: true
rustfs:
  downloadURL: https://example.com/rustfs-cc.zip
  s3Port: 8080
  consolePort: 8081
  accessKey: ccaccess
  secretKey: ccsecret
data:
  diskSizeGiB: 250
  storagePool: ceph-pool
  zfsDataset: rpool/ccbloc
tls:
  mode: disabled
  commonName: cc.example.com
`

	var a ArtifactsConfig
	if err := yaml.Unmarshal([]byte(input), &a); err != nil {
		t.Fatalf("unmarshal camelCase: %v", err)
	}

	if a.Rustfs.DownloadURL != "https://example.com/rustfs-cc.zip" {
		t.Errorf("Rustfs.DownloadURL (camelCase): got %q", a.Rustfs.DownloadURL)
	}

	if a.Rustfs.S3Port != 8080 {
		t.Errorf("Rustfs.S3Port (camelCase): got %d", a.Rustfs.S3Port)
	}

	if a.Rustfs.ConsolePort != 8081 {
		t.Errorf("Rustfs.ConsolePort (camelCase): got %d", a.Rustfs.ConsolePort)
	}

	if a.Rustfs.AccessKey != "ccaccess" {
		t.Errorf("Rustfs.AccessKey (camelCase): got %q", a.Rustfs.AccessKey)
	}

	if a.Rustfs.SecretKey != "ccsecret" {
		t.Errorf("Rustfs.SecretKey (camelCase): got %q", a.Rustfs.SecretKey)
	}

	if a.Data.DiskSizeGiB != 250 {
		t.Errorf("Data.DiskSizeGiB (camelCase): got %d", a.Data.DiskSizeGiB)
	}

	if a.Data.StoragePool != "ceph-pool" {
		t.Errorf("Data.StoragePool (camelCase): got %q", a.Data.StoragePool)
	}

	if a.Data.ZFSDataset != "rpool/ccbloc" {
		t.Errorf("Data.ZFSDataset (camelCase): got %q", a.Data.ZFSDataset)
	}

	if a.TLS.Mode != "disabled" {
		t.Errorf("TLS.Mode (camelCase): got %q", a.TLS.Mode)
	}

	if a.TLS.CommonName != "cc.example.com" {
		t.Errorf("TLS.CommonName (camelCase): got %q", a.TLS.CommonName)
	}
}

// TestArtifactsConfigValidate covers all validation branches.
func TestArtifactsConfigValidate(t *testing.T) {
	// Helper to build a valid enabled config (PVE, bastion on, internal-ca with CA).
	validEnabled := func() ArtifactsConfig {
		a := ArtifactsConfig{
			Enabled:  true,
			Flavor:   "artifacts",
			Template: "ubuntu-2204-cloudinit",
			Rustfs:   RustfsConfig{Version: "1.0.0-beta.3", S3Port: 9000, ConsolePort: 9001},
			Data:     ArtifactsDataConfig{DiskSizeGiB: 500, StoragePool: "local-zfs", Mountpoint: "/data"},
			TLS:      ArtifactsTLSConfig{Mode: ArtifactsTLSModeInternalCA},
		}
		return a
	}

	tests := []struct {
		name                 string
		cfg                  ArtifactsConfig
		provider             string
		bastionEnabled       bool
		internalCAConfigured bool
		wantErr              error
	}{
		{
			name:                 "disabled always passes",
			cfg:                  ArtifactsConfig{Enabled: false},
			provider:             "aws",
			bastionEnabled:       false,
			internalCAConfigured: false,
			wantErr:              nil,
		},
		{
			name:                 "valid pve internal-ca",
			cfg:                  validEnabled(),
			provider:             "pve",
			bastionEnabled:       true,
			internalCAConfigured: true,
			wantErr:              nil,
		},
		{
			name:                 "valid pve self-signed",
			cfg:                  func() ArtifactsConfig { a := validEnabled(); a.TLS.Mode = ArtifactsTLSModeSelfSigned; return a }(),
			provider:             "pve",
			bastionEnabled:       true,
			internalCAConfigured: false,
			wantErr:              nil,
		},
		{
			name:                 "valid pve disabled tls",
			cfg:                  func() ArtifactsConfig { a := validEnabled(); a.TLS.Mode = ArtifactsTLSModeDisabled; return a }(),
			provider:             "pve",
			bastionEnabled:       true,
			internalCAConfigured: false,
			wantErr:              nil,
		},
		{
			name:                 "non-pve provider rejected",
			cfg:                  validEnabled(),
			provider:             "aws",
			bastionEnabled:       true,
			internalCAConfigured: true,
			wantErr:              ErrArtifactsRequiresPVE,
		},
		{
			name:                 "bastion disabled rejected",
			cfg:                  validEnabled(),
			provider:             "pve",
			bastionEnabled:       false,
			internalCAConfigured: true,
			wantErr:              ErrArtifactsRequiresBastion,
		},
		{
			name:                 "disk size zero rejected",
			cfg:                  func() ArtifactsConfig { a := validEnabled(); a.Data.DiskSizeGiB = 0; return a }(),
			provider:             "pve",
			bastionEnabled:       true,
			internalCAConfigured: true,
			wantErr:              ErrArtifactsInvalidDiskSize,
		},
		{
			name:                 "disk size negative rejected",
			cfg:                  func() ArtifactsConfig { a := validEnabled(); a.Data.DiskSizeGiB = -1; return a }(),
			provider:             "pve",
			bastionEnabled:       true,
			internalCAConfigured: true,
			wantErr:              ErrArtifactsInvalidDiskSize,
		},
		{
			name:                 "invalid tls mode rejected",
			cfg:                  func() ArtifactsConfig { a := validEnabled(); a.TLS.Mode = "bogus"; return a }(),
			provider:             "pve",
			bastionEnabled:       true,
			internalCAConfigured: true,
			wantErr:              ErrArtifactsInvalidTLSMode,
		},
		{
			name:                 "internal-ca without ca rejected",
			cfg:                  validEnabled(),
			provider:             "pve",
			bastionEnabled:       true,
			internalCAConfigured: false,
			wantErr:              ErrArtifactsTLSInternalCARequiresCA,
		},
		{
			name:                 "provider case-insensitive PVE accepted",
			cfg:                  validEnabled(),
			provider:             "PVE",
			bastionEnabled:       true,
			internalCAConfigured: true,
			wantErr:              nil,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate(tc.provider, tc.bastionEnabled, tc.internalCAConfigured)
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}

				return
			}

			if err == nil {
				t.Errorf("expected error wrapping %v, got nil", tc.wantErr)
				return
			}

			if !errors.Is(err, tc.wantErr) {
				t.Errorf("expected error %v, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestArtifactsConfigResolvedDataset verifies fallback and explicit dataset paths.
func TestArtifactsConfigResolvedDataset(t *testing.T) {
	tests := []struct {
		name       string
		zfsDataset string
		blocName   string
		want       string
	}{
		{
			name:       "explicit dataset returned as-is",
			zfsDataset: "tank/custom",
			blocName:   "mybloc",
			want:       "tank/custom",
		},
		{
			name:       "empty dataset falls back to rpool/<blocName>",
			zfsDataset: "",
			blocName:   "mybloc",
			want:       "rpool/mybloc",
		},
		{
			name:       "empty dataset with different bloc name",
			zfsDataset: "",
			blocName:   "prod-east",
			want:       "rpool/prod-east",
		},
		{
			name:       "whitespace-only dataset treated as explicit",
			zfsDataset: "rpool/override",
			blocName:   "irrelevant",
			want:       "rpool/override",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			a := ArtifactsConfig{
				Data: ArtifactsDataConfig{ZFSDataset: tc.zfsDataset},
			}

			got := a.ResolvedDataset(tc.blocName)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestArtifactsConfigResolvedDownloadURL verifies URL construction and override.
func TestArtifactsConfigResolvedDownloadURL(t *testing.T) {
	tests := []struct {
		name        string
		downloadURL string
		version     string
		want        string
	}{
		{
			name:        "explicit URL returned as-is",
			downloadURL: "https://example.com/custom/rustfs.zip",
			version:     "1.0.0-beta.3",
			want:        "https://example.com/custom/rustfs.zip",
		},
		{
			name:        "empty URL builds default URL with version",
			downloadURL: "",
			version:     "1.0.0-beta.3",
			want:        "https://dl.rustfs.com/artifacts/rustfs/release/rustfs-linux-x86_64-musl-v1.0.0-beta.3.zip",
		},
		{
			name:        "version substituted in default URL",
			downloadURL: "",
			version:     "2.1.0",
			want:        "https://dl.rustfs.com/artifacts/rustfs/release/rustfs-linux-x86_64-musl-v2.1.0.zip",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			a := ArtifactsConfig{
				Rustfs: RustfsConfig{
					DownloadURL: tc.downloadURL,
					Version:     tc.version,
				},
			}

			got := a.ResolvedDownloadURL()
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
