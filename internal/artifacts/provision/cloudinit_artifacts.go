package provision

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
	Filesystem  string // "ext4" (default), "xfs", or "zfs"
	ZFSDataset  string // e.g., rpool/myblock (zfs only)
	TLSEnabled  bool
	CertPEM     string
	KeyPEM      string
	// CAPEM is the trust anchor to install into the VM's own OS trust store
	// (task 4.4, VM self-trust): the bloc CA cert for internal-ca mode, or
	// the leaf itself for self-signed (which is its own trust anchor). Empty
	// when TLS is disabled. Optional — never validated by validate() — so
	// older callers that don't set it still render a valid (CA-less)
	// artifact VM, falling back to unverified on-VM clients.
	CAPEM string
}

// FS returns the normalized data-disk filesystem, defaulting to ext4 when
// unset. Templates branch on this instead of the raw Filesystem field.
func (in ArtifactsCloudInitInputs) FS() string {
	fs := strings.ToLower(strings.TrimSpace(in.Filesystem))
	if fs == "" {
		return "ext4"
	}

	return fs
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

	switch in.FS() {
	case "ext4", "xfs":
		// no extra inputs required
	case "zfs":
		if strings.TrimSpace(in.ZFSDataset) == "" {
			missing = append(missing, "ZFSDataset")
		}
	default:
		return fmt.Errorf("ArtifactsCloudInitInputs: unsupported Filesystem %q (ext4|xfs|zfs)", in.Filesystem) //nolint:err113 // descriptive error, not caller-testable
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
// formats the attached /dev/sdb data disk (ext4/xfs, or a ZFS pool + dataset
// when Filesystem=zfs), mounts it, and starts the rustfs systemd service.
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
{{- if eq .FS "zfs" }}
  - zfsutils-linux
{{- else if eq .FS "xfs" }}
  - xfsprogs
{{- end }}
  - unzip
  - jq
{{- if .CAPEM }}
  - ca-certificates
{{- end }}

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
      After=network-online.target{{ if eq .FS "zfs" }} zfs-mount.service{{ end }}
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
{{ if .CAPEM }}
  - path: /usr/local/share/ca-certificates/ocfp-ca.crt
    permissions: '0644'
    content: |
{{ indentpem 6 .CAPEM }}
{{ end }}
runcmd:
  - useradd --system --home /var/lib/rustfs --shell /usr/sbin/nologin rustfs
{{- if eq .FS "zfs" }}
  - zpool import -a || true
  - |
    if ! zpool list rpool > /dev/null 2>&1; then
      zpool create -f -o ashift=12 rpool /dev/sdb
    fi
  - zfs create -o mountpoint={{ .Mountpoint }} -o compression=lz4 {{ .ZFSDataset }}
{{- else }}
  - |
    if ! blkid /dev/sdb > /dev/null 2>&1; then
      mkfs.{{ .FS }} /dev/sdb
    fi
  - mkdir -p {{ .Mountpoint }}
  - |
    uuid="$(blkid -o value -s UUID /dev/sdb)"
    if ! grep -q "UUID=${uuid} " /etc/fstab; then
      echo "UUID=${uuid} {{ .Mountpoint }} {{ .FS }} defaults,nofail 0 2" >> /etc/fstab
    fi
  - mountpoint -q {{ .Mountpoint }} || mount {{ .Mountpoint }}
{{- end }}
  - chown -R rustfs:rustfs {{ .Mountpoint }}
  - mkdir -p /opt/rustfs/tls /var/log/rustfs
  - chown -R rustfs:rustfs /opt/rustfs /var/log/rustfs
{{- if .CAPEM }}
  - update-ca-certificates
{{- end }}
  - curl -fsSL {{ .DownloadURL }} -o /tmp/rustfs.zip
  - unzip -d /usr/local/bin /tmp/rustfs.zip
  - chmod +x /usr/local/bin/rustfs
  - systemctl daemon-reload
  - systemctl enable --now rustfs
`))

// RenderArtifactsCloudInit produces the cloud-init user-data YAML for the
// ocfp-artifacts VM. The output installs RustFS, formats + mounts the attached
// data disk (/dev/sdb) per Filesystem (ext4 default, xfs, or a ZFS
// pool/dataset), and enables the rustfs systemd service. When CAPEM is set,
// it is also installed into the VM's own OS trust store
// (/usr/local/share/ca-certificates/ocfp-ca.crt + update-ca-certificates) so
// on-VM clients (loopback health checks, mc/aws run locally) verify the
// served cert instead of needing skip-verify.
//
// All fields in ArtifactsCloudInitInputs are required except Filesystem
// (defaults to ext4), ZFSDataset (required only when Filesystem is zfs), and
// CertPEM/KeyPEM (required only when TLSEnabled is true) and CAPEM (always
// optional — a clean no-op when empty). An error is returned when any
// required field is empty or zero.
func RenderArtifactsCloudInit(in ArtifactsCloudInitInputs) (string, error) {
	err := in.validate()
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer

	err = artifactsCloudInitTmpl.Execute(&buf, in)
	if err != nil {
		return "", fmt.Errorf("render artifacts cloud-init: %w", err)
	}

	return buf.String(), nil
}
