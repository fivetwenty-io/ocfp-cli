package pve

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// ArtifactsCloudInitInputs holds the data needed to render the artifacts VM user-data.
type ArtifactsCloudInitInputs struct {
	AccessKey   string
	SecretKey   string
	DownloadURL string
	S3Port      int
	ConsolePort int
	Mountpoint  string
	ZFSDataset  string // e.g., rpool/myblock
	TLSEnabled  bool
	CertPEM     string
	KeyPEM      string
}

// validate returns an error when required fields are missing.
func (in ArtifactsCloudInitInputs) validate() error {
	var missing []string

	if strings.TrimSpace(in.AccessKey) == "" {
		missing = append(missing, "AccessKey")
	}

	if strings.TrimSpace(in.SecretKey) == "" {
		missing = append(missing, "SecretKey")
	}

	if strings.TrimSpace(in.DownloadURL) == "" {
		missing = append(missing, "DownloadURL")
	}

	if in.S3Port == 0 {
		missing = append(missing, "S3Port")
	}

	if in.ConsolePort == 0 {
		missing = append(missing, "ConsolePort")
	}

	if strings.TrimSpace(in.Mountpoint) == "" {
		missing = append(missing, "Mountpoint")
	}

	if strings.TrimSpace(in.ZFSDataset) == "" {
		missing = append(missing, "ZFSDataset")
	}

	if in.TLSEnabled {
		if strings.TrimSpace(in.CertPEM) == "" {
			missing = append(missing, "CertPEM")
		}

		if strings.TrimSpace(in.KeyPEM) == "" {
			missing = append(missing, "KeyPEM")
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("ArtifactsCloudInitInputs: missing required fields: %s", strings.Join(missing, ", ")) //nolint:err113 // descriptive error, not caller-testable
	}

	return nil
}

// indentPEM indents each line of a PEM block by n spaces. cloud-init
// write_files with "content: |" requires all PEM lines to be indented at
// least as far as the content key plus two spaces — callers provide n to
// match the nesting depth.
func indentPEM(n int, pem string) string {
	prefix := strings.Repeat(" ", n)
	var b strings.Builder

	for _, line := range strings.Split(strings.TrimRight(pem, "\n"), "\n") {
		b.WriteString(prefix)
		b.WriteString(line)
		b.WriteByte('\n')
	}

	return b.String()
}

// artifactsCloudInitTmpl is the cloud-init user-data template for the
// ocfp-artifacts VM. It installs RustFS on an Ubuntu 22.04 base image,
// creates a ZFS pool on the attached /dev/sdb data disk, mounts the dataset,
// and starts the rustfs systemd service.
//
// Template function "indentpem" indents a multiline PEM string by N spaces so
// YAML block-scalar "content: |" output stays valid.
var artifactsCloudInitTmpl = template.Must(
	template.New("artifacts-cloud-init").
		Funcs(template.FuncMap{
			"indentpem": indentPEM,
		}).
		Parse(`#cloud-config
package_update: true
packages:
  - zfsutils-linux
  - unzip
  - jq

write_files:
  - path: /etc/default/rustfs
    permissions: '0640'
    owner: rustfs:rustfs
    content: |
      RUSTFS_ADDRESS=:{{ .S3Port }}
      RUSTFS_CONSOLE_ADDRESS=:{{ .ConsolePort }}
      RUSTFS_ACCESS_KEY={{ .AccessKey }}
      RUSTFS_SECRET_KEY={{ .SecretKey }}
      RUSTFS_VOLUMES={{ .Mountpoint }}
{{ if .TLSEnabled }}      RUSTFS_TLS_PATH=/opt/rustfs/tls
{{ end }}
  - path: /etc/systemd/system/rustfs.service
    permissions: '0644'
    content: |
      [Unit]
      Description=RustFS S3 server
      After=network-online.target zfs-mount.service
      Wants=network-online.target

      [Service]
      Type=simple
      User=rustfs
      Group=rustfs
      EnvironmentFile=/etc/default/rustfs
      ExecStart=/usr/local/bin/rustfs
      Restart=on-failure
      RestartSec=5s

      [Install]
      WantedBy=multi-user.target
{{ if .TLSEnabled }}
  - path: /opt/rustfs/tls/rustfs_cert.pem
    permissions: '0640'
    owner: rustfs:rustfs
    content: |
{{ indentpem 6 .CertPEM }}
  - path: /opt/rustfs/tls/rustfs_key.pem
    permissions: '0600'
    owner: rustfs:rustfs
    content: |
{{ indentpem 6 .KeyPEM }}
{{ end }}
runcmd:
  - useradd --system --home /var/lib/rustfs --shell /usr/sbin/nologin rustfs
  - zpool import -a || true
  - |
    if ! zpool list rpool > /dev/null 2>&1; then
      zpool create -f -o ashift=12 rpool /dev/sdb
    fi
  - zfs create -o mountpoint={{ .Mountpoint }} -o compression=lz4 {{ .ZFSDataset }}
  - chown -R rustfs:rustfs {{ .Mountpoint }}
  - mkdir -p /opt/rustfs/tls /var/log/rustfs
  - chown -R rustfs:rustfs /opt/rustfs /var/log/rustfs
  - curl -fsSL {{ .DownloadURL }} -o /tmp/rustfs.zip
  - unzip -d /usr/local/bin /tmp/rustfs.zip
  - chmod +x /usr/local/bin/rustfs
  - systemctl daemon-reload
  - systemctl enable --now rustfs
`))

// RenderArtifactsCloudInit produces the cloud-init user-data YAML for the
// ocfp-artifacts VM. The output installs RustFS, creates the ZFS pool/dataset
// on the attached disk (/dev/sdb), and enables the rustfs systemd service.
//
// All fields in ArtifactsCloudInitInputs are required except CertPEM and
// KeyPEM, which are only required when TLSEnabled is true. An error is
// returned when any required field is empty or zero.
func RenderArtifactsCloudInit(in ArtifactsCloudInitInputs) (string, error) {
	if err := in.validate(); err != nil {
		return "", err
	}

	var buf bytes.Buffer

	if err := artifactsCloudInitTmpl.Execute(&buf, in); err != nil {
		return "", fmt.Errorf("render artifacts cloud-init: %w", err)
	}

	return buf.String(), nil
}
