package guestboot

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// CloudFirstboot uses a private configuration file instead of SMBIOS. The
// connector and ingress are independent of whether Tailscale is enabled.
var CloudFirstboot = cloudFirstboot()

func fileTransport(script string) string {
	script = strings.ReplaceAll(script, ".ssh // true", `(if has("ssh") then .ssh else true end)`)
	script = strings.ReplaceAll(script, "dmidecode -s system-family", "printf ocfp-bastion")
	script = strings.ReplaceAll(script, "dmidecode -s system-serial-number", "jq -r '.authkey // empty' /etc/ocfp/guest.json")

	return strings.ReplaceAll(script, "dmidecode -s system-sku-number", "cat /etc/ocfp/guest.json")
}

func cloudFirstboot() string {
	script := fileTransport(FirstbootScript)
	start := strings.Index(script, "if [[ -z \"$authkey\" ]]")
	end := strings.Index(script[start:], "\nfi") + start + len("\nfi")
	script = script[:start] + script[end:]
	forwardStart := strings.Index(script, "# Persist IP forwarding")
	forwardEnd := strings.Index(script, "# Assemble the up invocation")
	forwarding := script[forwardStart:forwardEnd]
	script = script[:forwardStart] + script[forwardEnd:]
	start = strings.Index(script, "# Idempotent install.")
	end = strings.Index(script, "# --- cloudflared connector")
	hostname := `os_hostname=$(jq -r '.os_hostname // empty' /etc/ocfp/guest.json)
if [[ -n "$os_hostname" ]]; then hostnamectl set-hostname -- "$os_hostname"; fi
`

	return script[:start] + hostname + forwarding + "if [[ -n \"$authkey\" ]]; then\n" + script[start:end] + "fi\n\n" + script[end:]
}

// DiskResolver resolves the *attachment identity*, never the next enumerated
// disk. AWS documents the attachment name in NVMe controller bytes 384:4095.
// https://docs.aws.amazon.com/ebs/latest/userguide/identify-nvme-ebs-device.html
//
//nolint:dupword // embedded shell closes nested conditionals with repeated fi
const DiskResolver = `#!/bin/bash
set -euo pipefail
provider=$(jq -r '.provider' /etc/ocfp/guest.json)
role="${1:-bastion}"
case "$role" in bastion|artifacts) ;; *) exit 1;; esac
resolve() {
  if [[ "$provider" == gcp ]]; then
    # e.g. google-ocfp-bastion-data; the name is supplied to AttachDisk.
    path="/dev/disk/by-id/google-ocfp-${role}-data"
    [[ ! -b "$path" ]] || readlink -f "$path"
  elif [[ "$provider" == aws ]]; then
    # Xen exposes the attachment directly; Nitro exposes it in vendor data.
    for path in /dev/sdf /dev/xvdf; do
      [[ ! -b "$path" ]] || readlink -f "$path"
    done
    for path in /dev/nvme*n1; do
      [[ -b "$path" ]] || continue
      device=$(nvme id-ctrl -b "$path" 2>/dev/null | dd bs=1 skip=384 count=32 2>/dev/null | tr -d '\000 ' || true)
      [[ "${device#/dev/}" != sdf ]] || readlink -f "$path"
    done
  fi
}
for ((attempt=0; attempt<120; attempt++)); do
  mapfile -t matches < <(resolve | sort -u)
  if (( ${#matches[@]} > 1 )); then echo 'multiple data disks match attachment' >&2; exit 1; fi
  if (( ${#matches[@]} == 1 )); then
    disk="${matches[0]}"
    rootsrc=$(findmnt -n -o SOURCE /)
    if lsblk -nrpo NAME "$disk" | grep -Fxq "$(readlink -f "$rootsrc")"; then echo 'data disk backs root' >&2; exit 1; fi
    if [[ $(lsblk -nrpo TYPE "$disk" | wc -l) -ne 1 ]]; then
      recognized=false
      if [[ "$role" == artifacts && "${2:-}" == zfs ]]; then
        while read -r child; do
          kind=$(blkid -o value -s TYPE "$child" 2>/dev/null || true)
          if [[ "$kind" == zfs_member ]]; then recognized=true
          elif [[ -n "$kind" ]]; then echo 'foreign filesystem on partitioned data disk' >&2; exit 1
          fi
        done < <(lsblk -nrpo NAME "$disk")
      fi
      if ! $recognized; then echo 'partitioned data disk refused' >&2; exit 1; fi
    fi
    printf '%s\n' "$disk"
    exit 0
  fi
  sleep 5
done
echo 'data disk attachment did not appear within 600 seconds' >&2
exit 1
`

func cloudDataset() string {
	script := fileTransport(DatasetScript)
	serialStart := strings.Index(script, `if [ -z "${SERIAL}" ]; then`)
	serialEnd := strings.Index(script[serialStart:], "\nfi") + serialStart + len("\nfi")
	script = script[:serialStart] + script[serialEnd:]

	start := strings.Index(script, "REAL=\"\"")
	end := strings.Index(script[start:], "log \"data disk") + start
	script = script[:start] + `REAL=$(/usr/local/sbin/ocfp-disk-resolve bastion) || exit 1
` + script[end:]
	script = strings.Replace(script, "if [ -z \"${EXISTING}\" ]; then", `if [ -z "${EXISTING}" ]; then
  signatures=$(wipefs -n --noheadings -o TYPE "${REAL}") || { log "cannot inspect disk signatures"; exit 1; }
  if [ -n "$signatures" ]; then
    log "FATAL: disk has an unrecognized signature; refusing to format"
    exit 1
  fi`, 1)
	script = strings.Replace(script, `elif [ "${EXISTING}" != "${FSTYPE}" ]; then`, `elif [ "$(blkid -o value -s LABEL "${REAL}")" != "ocfp-data" ]; then
  log "unrecognized data filesystem; refusing to adopt foreign disk"
  exit 1
elif [ "${EXISTING}" != "${FSTYPE}" ]; then`, 1)
	script = strings.Replace(script, "# Keep fstab keyed", `mounted=$(findmnt -n -o SOURCE -M "${MOUNT}") || exit 1
if [ "$(readlink -f "$mounted")" != "${REAL}" ]; then
  log "unexpected mounted data device; refusing to use it"
  exit 1
fi
# Keep fstab keyed`, 1)

	return script
}

func cloudWatchdog() string {
	script := fileTransport(WatchdogScript)
	script = strings.Replace(script, "# --- keep ingress", `# Retry initial service jobs after a late data disk or transient download failure.
if [[ ! -f /var/lib/ocfp/firstboot-done ]]; then
  systemctl start --no-block ocfp-firstboot.service
  exit 0
fi
if [[ -f /etc/ocfp/bootstrap.sh && ! -f /var/lib/ocfp/bootstrap-done ]]; then
  systemctl start --no-block ocfp-bootstrap.service
fi
# --- keep ingress`, 1)
	start := strings.Index(script, "# --- keep cloudflared connector up ---")
	connector := strings.ReplaceAll(script[start:], `<<<"$sku"`, `<<<"$ing_sku"`)
	script = script[:start]
	pos := strings.Index(script, "status=$(tailscale status")

	return script[:pos] + connector + "\n" + `[[ -n "$(jq -r '.authkey // empty' /etc/ocfp/guest.json)" ]] || exit 0` + "\n" + script[pos:]
}

var safeUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

func cloudGuestConfig(provider string, req *cpi.InstanceRequest) map[string]any {
	osHostname := req.Hostname
	if osHostname != "" && req.DomainSuffix != "" {
		osHostname += "." + strings.TrimPrefix(req.DomainSuffix, ".")
	}

	cfg := map[string]any{"provider": provider, "hostname": req.Hostname, "os_hostname": osHostname}
	if ts := req.Tailscale; ts != nil {
		cfg["authkey"] = ts.AuthKey
		cfg["hostname"] = ts.Hostname
		cfg["tags"] = ts.Tags
		cfg["accept_dns"] = ts.AcceptDNS
		cfg["accept_routes"] = ts.AcceptRoutes
		cfg["ssh"] = ts.SSH
		cfg["exit_node"] = ts.ExitNode
		cfg["advertise_routes"] = ts.AdvertiseRoutes
	}

	if cf := req.Cloudflare; cf != nil {
		cfg["cloudflare"] = map[string]any{"token": cf.TunnelToken}
	}

	if ing := req.Ingress; ing != nil {
		cfg["ingress"] = map[string]any{"origin_ip": ing.OriginIP, "ports": ing.Ports}
	}

	if d := req.DataDisk; d != nil {
		cfg["data"] = map[string]any{"serial": d.Serial, "mountpoint": d.Mountpoint, "filesystem": d.Filesystem, "home": d.HomeDir, "user": d.User, "authorized_key": req.PublicKey}
	}

	return cfg
}

// Render returns a compressed shell payload within EC2's 16 KiB user-data
// limit. Assets and secrets are literal base64 files, not shell interpolation.
func Render(provider string, req *cpi.InstanceRequest) (string, error) {
	if provider != "aws" && provider != "gcp" {
		return req.UserData, nil
	}

	user := req.DefaultUsername
	if user == "" {
		user = "ubuntu"
	}

	if !safeUser.MatchString(user) {
		return "", fmt.Errorf("invalid guest username %q", user)
	}

	cfg := cloudGuestConfig(provider, req)

	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}

	var script strings.Builder
	script.WriteString("#!/bin/bash\nset -euo pipefail\numask 077\nexport DEBIAN_FRONTEND=noninteractive\napt-get update\napt-get install -y sudo jq curl nvme-cli xfsprogs nftables ca-certificates\ninstall -d -m 0700 /etc/ocfp\n")

	write := func(path, mode, content string) {
		delimiter := fmt.Sprintf("OCFP_%x", sha256.Sum256([]byte(content)))
		fmt.Fprintf(&script, "cat > %s <<'%s'\n%s\n%s\nchmod %s %s\n", path, delimiter, content, delimiter, mode, path)
	}
	write("/etc/ocfp/guest.json", "0600", string(raw))
	write("/usr/local/sbin/ocfp-disk-resolve", "0755", DiskResolver)
	write("/usr/local/sbin/ocfp-firstboot", "0755", CloudFirstboot)
	write("/usr/local/sbin/ocfp-tailscale-watchdog", "0755", cloudWatchdog())
	write("/etc/systemd/system/ocfp-firstboot.service", "0644", strings.Replace(FirstbootService, "[Service]", "StartLimitIntervalSec=0\n\n[Service]\nTimeoutStartSec=900\nRestart=on-failure\nRestartSec=15", 1))
	write("/etc/systemd/system/ocfp-tailscale-watchdog.service", "0644", WatchdogService)
	write("/etc/systemd/system/ocfp-tailscale-watchdog.timer", "0644", WatchdogTimer)

	if req.DataDisk != nil {
		write("/usr/local/sbin/ocfp-dataset", "0755", cloudDataset())
		write("/etc/systemd/system/ocfp-dataset.service", "0644", strings.Replace(DatasetService, "[Service]", "StartLimitIntervalSec=0\n\n[Service]\nTimeoutStartSec=660\nExecStartPost=-/bin/systemctl try-restart --no-block ssh.service tailscaled.service\nRestart=on-failure\nRestartSec=15", 1))
		write("/etc/cloud/cloud.cfg.d/99-ocfp-keep-host-keys.cfg", "0644", KeepHostKeysConfig)

		for _, service := range []string{"ssh", "sshd", "tailscaled", "ocfp-firstboot", "ocfp-tailscale-watchdog"} {
			dir := "/etc/systemd/system/" + service + ".service.d"
			fmt.Fprintf(&script, "install -d -m 0755 %s\n", dir)
			write(dir+"/ocfp-dataset.conf", "0644", "[Unit]\nRequires=ocfp-dataset.service\nAfter=ocfp-dataset.service\n")
		}
	}

	fmt.Fprintf(&script, "id -u %s >/dev/null 2>&1 || useradd -m -s /bin/bash %s\n", user, user)
	fmt.Fprintf(&script, "install -d -m 0700 -o %s -g %s /home/%s/.ssh\n", user, user, user)
	write("/etc/sudoers.d/90-ocfp-user", "0440", user+" ALL=(ALL) NOPASSWD:ALL\n")

	if req.PublicKey != "" {
		write("/home/"+user+"/.ssh/authorized_keys", "0600", req.PublicKey+"\n")
		fmt.Fprintf(&script, "chown %s:%s /home/%s/.ssh/authorized_keys\n", user, user, user)
	}

	units := "ocfp-firstboot.service ocfp-tailscale-watchdog.timer"
	if req.DataDisk != nil {
		units += " ocfp-dataset.service"
	}

	if req.UserData != "" {
		write("/etc/ocfp/bootstrap.sh", "0700", req.UserData)

		unit := "[Unit]\nDescription=OCFP cloud bootstrap\nConditionPathExists=!/var/lib/ocfp/bootstrap-done\nAfter=cloud-final.service ocfp-firstboot.service\nRequires=ocfp-firstboot.service\nStartLimitIntervalSec=0\n[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=/bin/bash -e /etc/ocfp/bootstrap.sh\nExecStartPost=/bin/touch /var/lib/ocfp/bootstrap-done\nTimeoutStartSec=1800\nRestart=on-failure\nRestartSec=30\n[Install]\nWantedBy=multi-user.target\n"
		write("/etc/systemd/system/ocfp-bootstrap.service", "0644", unit)

		units += " ocfp-bootstrap.service"
	}

	script.WriteString("systemctl daemon-reload\nsystemctl enable " + units + "\nsystemctl start --no-block " + units + "\n")

	var compressed bytes.Buffer

	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write([]byte(script.String())); err != nil {
		return "", err
	}

	if err := zw.Close(); err != nil {
		return "", err
	}

	result := "#!/bin/bash\nset -euo pipefail\nprintf %s '" + base64.StdEncoding.EncodeToString(compressed.Bytes()) + "' | base64 -d | gzip -d | bash\n"
	if len(result) > 16384 {
		return "", fmt.Errorf("guest user-data exceeds 16 KiB (%d bytes)", len(result))
	}

	return result, nil
}
