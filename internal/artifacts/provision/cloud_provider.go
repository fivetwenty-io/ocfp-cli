package provision

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/ocfp/ocfp-cli-go/internal/guestboot"
)

func cloudArtifactsScript(in ArtifactsCloudInitInputs, script string) string {
	// Install the identity resolver before the filesystem step. Never format
	// a disk until the provider's stable attachment name has appeared.
	setup := fmt.Sprintf("\ninstall -d -m 0700 /etc/ocfp\nprintf '%%s' '{\"provider\":\"%s\"}' > /etc/ocfp/guest.json\nchmod 0600 /etc/ocfp/guest.json\nprintf '%%s' '%s' | base64 -d > /usr/local/sbin/ocfp-disk-resolve\nchmod 0755 /usr/local/sbin/ocfp-disk-resolve\ndisk=$(/usr/local/sbin/ocfp-disk-resolve artifacts)\n", in.Provider, base64.StdEncoding.EncodeToString([]byte(guestboot.DiskResolver)))
	script = strings.Replace(script, "unzip jq", "unzip jq curl ca-certificates nvme-cli", 1)
	script = strings.ReplaceAll(script, "/dev/sdb", `"$disk"`)

	marker := "# " + in.FS() + " filesystem"
	if in.FS() == "zfs" {
		marker = "# ZFS pool"
	}

	if in.FS() == "zfs" {
		setup = strings.Replace(setup, "ocfp-disk-resolve artifacts)", "ocfp-disk-resolve artifacts zfs)", 1)
		script = strings.Replace(script, "zpool import -a >/dev/null 2>&1 || true", cloudZFSImport, 1)
	}

	script = strings.Replace(script, marker, setup+marker, 1)
	script = strings.Replace(script, `if ! blkid "$disk" >/dev/null 2>&1; then`, `existing=$(blkid -o value -s TYPE "$disk" || true)
if [[ -n "$existing" && "$(blkid -o value -s LABEL "$disk")" != ocfp-artdata ]]; then echo 'unrecognized artifacts filesystem' >&2; exit 1; fi
if [[ -n "$existing" && "$existing" != "`+in.FS()+`" ]]; then echo 'unexpected filesystem on artifacts disk' >&2; exit 1; fi
if [[ -z "$existing" ]]; then
  signatures=$(wipefs -n --noheadings -o TYPE "$disk") || { echo "cannot inspect disk signatures" >&2; exit 1; }
  if [[ -n "$signatures" ]]; then echo 'unknown disk signature' >&2; exit 1; fi`, 1)
	// Never force a pool over an existing foreign filesystem.
	script = strings.Replace(script, `zpool create -f -o`, `signatures=$(wipefs -n --noheadings -o TYPE "$disk") || { echo "cannot inspect disk signatures" >&2; exit 1; }
  if [[ -n "$signatures" ]]; then echo 'unrecognized ZFS data disk' >&2; exit 1; fi
  zpool create -o`, 1)

	script = strings.Replace(script, "mkfs."+in.FS()+" ", "mkfs."+in.FS()+" -L ocfp-artdata ", 1)
	if in.FS() != "zfs" {
		marker := "mountpoint -q " + in.Mountpoint + " || mount " + in.Mountpoint
		script = strings.Replace(script, marker, marker+`
mounted=$(findmnt -n -o SOURCE -M `+in.Mountpoint+`) || exit 1
if [[ "$(readlink -f "$mounted")" != "$disk" ]]; then echo 'unexpected mounted artifacts device' >&2; exit 1; fi`, 1)
	}

	script = strings.Replace(script, "Wants=network-online.target", "Wants=network-online.target\nRequiresMountsFor="+in.Mountpoint+"\nConditionPathIsMountPoint="+in.Mountpoint, 1)

	return script
}

func renderCloudArtifacts(in ArtifactsCloudInitInputs) (string, error) {
	script, err := RenderArtifactsProvisionScript(in)
	if err != nil {
		return "", err
	}
	// The retry unit orders itself after cloud-final; no cloud-init polling is needed.
	script = strings.Replace(script, "cloud-init status --wait >/dev/null 2>&1 || true", "# Running inside cloud-init; do not wait for ourselves.", 1)

	var compressed bytes.Buffer

	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write([]byte(script)); err != nil {
		return "", err
	}

	if err := zw.Close(); err != nil {
		return "", err
	}

	unit := `[Unit]
Description=OCFP artifacts installation
ConditionPathExists=!/var/lib/ocfp/artifacts-installed
After=cloud-final.service network-online.target
Wants=network-online.target
StartLimitIntervalSec=0
[Service]
Type=oneshot
RemainAfterExit=yes
ExecStartPre=/bin/mkdir -p /var/lib/ocfp
ExecStart=/bin/bash /usr/local/sbin/ocfp-artifacts-install
ExecStartPost=/bin/touch /var/lib/ocfp/artifacts-installed
TimeoutStartSec=1800
Restart=on-failure
RestartSec=30
[Install]
WantedBy=multi-user.target
`
	data := map[string]any{
		"write_files": []map[string]any{
			{"path": "/usr/local/sbin/ocfp-artifacts-install", "permissions": "0700", "encoding": "gz+b64", "content": base64.StdEncoding.EncodeToString(compressed.Bytes())},
			{"path": "/etc/systemd/system/ocfp-artifacts-install.service", "permissions": "0644", "content": unit},
		},
		"runcmd": [][]string{{"systemctl", "daemon-reload"}, {"systemctl", "enable", "ocfp-artifacts-install.service"}, {"systemctl", "start", "--no-block", "ocfp-artifacts-install.service"}},
	}

	username := in.Username
	if username == "" {
		username = "ubuntu"
	}

	account := map[string]any{"name": username, "shell": "/bin/bash", "sudo": "ALL=(ALL) NOPASSWD:ALL", "lock_passwd": true}
	if in.PublicKey != "" {
		account["ssh_authorized_keys"] = []string{in.PublicKey}
	}

	data["users"] = []any{"default", account}
	if in.Hostname != "" {
		data["fqdn"] = in.Hostname
		data["hostname"] = strings.SplitN(in.Hostname, ".", 2)[0]
		data["preserve_hostname"] = false
	}

	out, err := yaml.Marshal(data)
	if err != nil {
		return "", err
	}

	return "#cloud-config\n" + string(out), nil
}

// Import only a pool discoverable on the resolved attachment, then prove every
// leaf vdev belongs to that disk before touching an existing pool named rpool.
// OpenZFS supports -d with either a directory or an individual device.
//
//nolint:dupword // embedded shell closes nested conditionals with repeated fi
const cloudZFSImport = `if ! zpool list rpool >/dev/null 2>&1; then
  search=()
  has_zfs=false
  while read -r child; do
    if [[ "$(blkid -o value -s TYPE "$child" 2>/dev/null || true)" == zfs_member ]]; then
      search+=(-d "$child")
      has_zfs=true
    fi
  done < <(lsblk -nrpo NAME "$disk")
  if $has_zfs; then zpool import -d "$disk" "${search[@]}" rpool; fi
fi
if zpool list rpool >/dev/null 2>&1; then
  mapfile -t leaves < <(zpool status -P rpool | awk '$1 ~ /^\/dev\// {print $1}')
  if (( ${#leaves[@]} == 0 )); then echo 'cannot identify artifacts pool devices' >&2; exit 1; fi
  for leaf in "${leaves[@]}"; do
    if ! lsblk -nrpo NAME "$disk" | grep -Fxq "$(readlink -f "$leaf")"; then
      echo 'foreign pool named rpool; refusing to modify it' >&2
      exit 1
    fi
  done
fi`
