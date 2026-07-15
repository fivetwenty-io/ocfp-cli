package provision

import (
	"bytes"
	"fmt"
	"text/template"
)

// artifactsProvisionScriptTmpl renders a self-contained, idempotent bash
// script that installs and starts RustFS on the ocfp-artifacts VM. It is the
// SSH-delivered equivalent of artifactsCloudInitTmpl: PVE 9.x rejects the
// cloud-init snippet upload (the /storage/<pool>/upload API forbids
// content=snippets), so bootstrap pushes this script over SSH (through the
// bastion) and runs it instead of relying on cloud-init user-data.
//
// The script is idempotent — every mutating step is guarded — so it is safe to
// re-run against a partially-provisioned VM. Heredocs use a quoted delimiter so
// secret values and PEM blocks are written literally without shell expansion.
var artifactsProvisionScriptTmpl = template.Must(
	template.New("artifacts-provision").Parse(`#!/usr/bin/env bash
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

# Wait out cloud-init's first-boot apt activity so we don't race the dpkg lock.
cloud-init status --wait >/dev/null 2>&1 || true

apt-get update -q
apt-get install -y -q{{ if eq .FS "zfs" }} zfsutils-linux{{ else if eq .FS "xfs" }} xfsprogs{{ end }} unzip jq{{ if .CAPEM }} ca-certificates{{ end }}

# Dedicated service account (idempotent).
id -u rustfs >/dev/null 2>&1 || useradd --system --home /var/lib/rustfs --shell /usr/sbin/nologin rustfs

install -d -m 0750 -o rustfs -g rustfs /opt/rustfs /opt/rustfs/tls /var/log/rustfs

cat > /etc/default/rustfs <<'OCFP_RUSTFS_ENV'
RUSTFS_ADDRESS=:{{ .S3Port }}
RUSTFS_CONSOLE_ADDRESS=:{{ .ConsolePort }}
RUSTFS_ACCESS_KEY={{ .AccessKey }}
RUSTFS_SECRET_KEY={{ .SecretKey }}
RUSTFS_VOLUMES={{ .Mountpoint }}
{{- if .TLSEnabled }}
RUSTFS_TLS_PATH=/opt/rustfs/tls
{{- end }}
OCFP_RUSTFS_ENV
chown rustfs:rustfs /etc/default/rustfs
chmod 0640 /etc/default/rustfs

cat > /etc/systemd/system/rustfs.service <<'OCFP_RUSTFS_SVC'
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
OCFP_RUSTFS_SVC
chmod 0644 /etc/systemd/system/rustfs.service
{{- if .TLSEnabled }}

cat > /opt/rustfs/tls/rustfs_cert.pem <<'OCFP_RUSTFS_CERT'
{{ .CertPEM }}
OCFP_RUSTFS_CERT

cat > /opt/rustfs/tls/rustfs_key.pem <<'OCFP_RUSTFS_KEY'
{{ .KeyPEM }}
OCFP_RUSTFS_KEY
chown rustfs:rustfs /opt/rustfs/tls/rustfs_cert.pem /opt/rustfs/tls/rustfs_key.pem
chmod 0640 /opt/rustfs/tls/rustfs_cert.pem
chmod 0600 /opt/rustfs/tls/rustfs_key.pem
{{- end }}
{{- if .CAPEM }}

# VM self-trust (task 4.4): install the trust anchor (bloc CA for
# internal-ca, the leaf itself for self-signed) into this VM's own OS trust
# store, so on-VM clients (this script's own health probe below, mc/aws run
# locally) verify the served cert instead of needing --no-verify-ssl.
# Idempotent: only rewrite + refresh the trust store when the content
# actually changed.
OCFP_CA_FILE=/usr/local/share/ca-certificates/ocfp-ca.crt
OCFP_CA_TMP="$(mktemp)"
cat > "${OCFP_CA_TMP}" <<'OCFP_ARTIFACTS_CA'
{{ .CAPEM }}
OCFP_ARTIFACTS_CA
if [ ! -f "${OCFP_CA_FILE}" ] || ! cmp -s "${OCFP_CA_TMP}" "${OCFP_CA_FILE}"; then
  install -m 0644 "${OCFP_CA_TMP}" "${OCFP_CA_FILE}"
  update-ca-certificates
fi
rm -f "${OCFP_CA_TMP}"
{{- end }}

{{- if eq .FS "zfs" }}
# ZFS pool on the attached data disk (idempotent).
zpool import -a >/dev/null 2>&1 || true
if ! zpool list rpool >/dev/null 2>&1; then
  zpool create -f -o ashift=12 rpool /dev/sdb
fi
if ! zfs list {{ .ZFSDataset }} >/dev/null 2>&1; then
  zfs create -o mountpoint={{ .Mountpoint }} -o compression=lz4 {{ .ZFSDataset }}
fi
{{- else }}
# {{ .FS }} filesystem on the attached data disk (idempotent).
if ! blkid /dev/sdb >/dev/null 2>&1; then
  mkfs.{{ .FS }} /dev/sdb
fi
mkdir -p {{ .Mountpoint }}
disk_uuid="$(blkid -o value -s UUID /dev/sdb)"
if ! grep -q "UUID=${disk_uuid} " /etc/fstab; then
  echo "UUID=${disk_uuid} {{ .Mountpoint }} {{ .FS }} defaults,nofail 0 2" >> /etc/fstab
  systemctl daemon-reload
fi
mountpoint -q {{ .Mountpoint }} || mount {{ .Mountpoint }}
{{- end }}
chown -R rustfs:rustfs {{ .Mountpoint }}

# RustFS binary (idempotent: only fetch when missing).
if [ ! -x /usr/local/bin/rustfs ]; then
  curl -fsSL {{ .DownloadURL }} -o /tmp/rustfs.zip
  unzip -o -d /usr/local/bin /tmp/rustfs.zip
  chmod +x /usr/local/bin/rustfs
fi

systemctl daemon-reload
systemctl enable --now rustfs
`))

// RenderArtifactsProvisionScript produces the bash provisioning script for the
// ocfp-artifacts VM, delivered over SSH (through the bastion) instead of via
// cloud-init because PVE 9.x blocks the snippet-upload path. It performs the
// same work as RenderArtifactsCloudInit — install RustFS, format + mount the
// /dev/sdb data disk per Filesystem (ext4 default, xfs, or a ZFS
// pool/dataset), write TLS material, install the CAPEM trust anchor into the
// VM's own OS trust store when set (task 4.4, VM self-trust), and enable the
// rustfs service — but as an idempotent, re-runnable script.
//
// Validation matches RenderArtifactsCloudInit: all fields are required except
// CertPEM/KeyPEM (required only when TLSEnabled is true) and CAPEM (always
// optional — a clean no-op when empty).
func RenderArtifactsProvisionScript(in ArtifactsCloudInitInputs) (string, error) {
	err := in.validate()
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer

	err = artifactsProvisionScriptTmpl.Execute(&buf, in)
	if err != nil {
		return "", fmt.Errorf("render artifacts provision script: %w", err)
	}

	return buf.String(), nil
}
