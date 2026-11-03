package guestboot

// Files written into the auto-provisioned template image during
// ProvisionTemplate. Cloned VMs inherit them; firstboot.service runs on first
// boot, gates on the SMBIOS family discriminator, and configures tailscale
// from the SMBIOS payload. The watchdog timer keeps tailscale online
// thereafter.
//
// The same scripts run on every VM cloned from this template (artifacts VMs
// too). They no-op unless `dmidecode -s system-family == ocfp-bastion`, so
// it's safe to bake them universally.

// SMBIOSFamilyBastion is the role discriminator the firstboot script checks
// in SMBIOS Type-1 Family before doing anything. Bastion VMs get this value;
// other roles (artifacts, jumpbox) get a different family value or none, and
// the firstboot script exits 0 without touching them.
const SMBIOSFamilyBastion = "ocfp-bastion"

// IngressNftTable is the nftables table both scripts install when the SMBIOS
// sku carries .ingress.origin_ip. The prerouting rule matches only traffic
// addressed to the bastion itself (fib daddr type local). The bastion also
// advertises its SDN subnet as a tailscale route, and without that match the
// DNAT would capture 80/443 bound for every other VM in the subnet. The
// shell variables are expanded by the heredoc in each script.
const IngressNftTable = `table ip ocfp_ingress {
  chain prerouting {
    type nat hook prerouting priority dstnat; policy accept;
    iifname "tailscale0" fib daddr type local tcp dport { $ing_ports } dnat to $ing_origin
  }
  chain postrouting {
    type nat hook postrouting priority srcnat; policy accept;
    ip daddr $ing_origin tcp dport { $ing_ports } masquerade
  }
}
`

// IngressLockPath is the lock file every writer of the ocfp_ingress table
// holds, through flock, while it checks or builds the table. Without it the
// watchdog, firstboot, and the boot-time ingress unit could each append the
// same rules to a table the others had just made.
const IngressLockPath = "/run/ocfp-ingress.lock"

// FirstbootScript reads the bastion config from SMBIOS, installs tailscale
// (idempotent), and runs `tailscale up`. Invoked once by
// ocfp-firstboot.service after cloud-init + network-online.
const FirstbootScript = `#!/bin/bash
# ocfp-firstboot v3 — runs once at first boot of a VM cloned from an
# OCFP-provisioned template. Reads its config from SMBIOS so the cloning
# step needs no file delivery to the PVE host (PVE 9.x API doesn't permit
# snippet uploads).
set -euo pipefail

# systemd starts this with a minimal PATH. Look in the same extra directories
# the dataset script does, after the system ones, so a tool that only linuxbrew
# supplies, such as jq, is still found.
export PATH="${PATH:-/usr/sbin:/usr/bin:/sbin:/bin}:/usr/local/sbin:/usr/local/bin:/home/linuxbrew/.linuxbrew/bin"

family=$(dmidecode -s system-family 2>/dev/null || true)
if [[ "$family" != "ocfp-bastion" ]]; then
  # Not a bastion clone — exit cleanly so artifacts/jumpbox VMs cloned from
  # the same template are unaffected.
  exit 0
fi

authkey=$(dmidecode -s system-serial-number 2>/dev/null || true)
if [[ -z "$authkey" ]]; then
  echo "ocfp-firstboot: SMBIOS serial empty; no auth key" >&2
  exit 1
fi

sku=$(dmidecode -s system-sku-number 2>/dev/null || true)
if [[ -z "$sku" ]]; then
  echo "ocfp-firstboot: SMBIOS sku empty; no config" >&2
  exit 1
fi

# Parse JSON config from SKU slot. Missing fields fall back to safe defaults.
hostname=$(jq -r '.hostname // ""' <<<"$sku")
tags=$(jq -r '.tags // [] | join(",")' <<<"$sku")
ssh_flag=$(jq -r 'if (.ssh // true) then "--ssh" else "" end' <<<"$sku")
accept_dns=$(jq -r 'if (.accept_dns // false) then "--accept-dns=true" else "--accept-dns=false" end' <<<"$sku")
accept_routes=$(jq -r 'if (.accept_routes // false) then "--accept-routes=true" else "--accept-routes=false" end' <<<"$sku")
exit_node=$(jq -r 'if (.exit_node // "") != "" then "--exit-node=" + .exit_node else "" end' <<<"$sku")
adv_routes=$(jq -r 'if (.advertise_routes // "") != "" then "--advertise-routes=" + .advertise_routes else "" end' <<<"$sku")

# Idempotent install. command -v skips when the static binary is already
# on $PATH; otherwise pull the official install script which handles both
# apt and tarball flavours.
if ! command -v tailscale >/dev/null 2>&1; then
  curl -fsSL https://tailscale.com/install.sh | sh
fi

# Best-effort jq install (cloud images ship it; this is defensive only).
command -v jq >/dev/null 2>&1 || apt-get install -y jq

# Persist IP forwarding before bringing tailscale up. The bastion advertises
# subnet routes (--advertise-routes), but the base cloud template ships
# forwarding OFF; tailscale only emits a health warning and won't set it, so
# routed SDN traffic silently fails. A sysctl drop-in survives reboots.
cat >/etc/sysctl.d/99-ocfp-ip-forward.conf <<'SYSCTL'
net.ipv4.ip_forward = 1
net.ipv6.conf.all.forwarding = 1
SYSCTL
sysctl -p /etc/sysctl.d/99-ocfp-ip-forward.conf >/dev/null 2>&1 || true

# Assemble the up invocation; quoted authkey for safety.
# shellcheck disable=SC2086
tailscale up \
  --authkey="$authkey" \
  --hostname="$hostname" \
  --advertise-tags="$tags" \
  $ssh_flag $accept_dns $accept_routes $adv_routes $exit_node

# --- cloudflared connector (remotely-managed tunnel) ---
cf_token=$(jq -r '.cloudflare.token // ""' <<<"$sku")
if [[ -n "$cf_token" ]]; then
  if ! command -v cloudflared >/dev/null 2>&1; then
    # Pinned version + sha256 — bump both together when upgrading. An
    # unexpected/tampered package aborts firstboot rather than installing.
    cfd_ver="2026.5.2"
    cfd_sha="f7378c11f55a061b4f1f7d1bccdd07bdfd947ed95634c5f6f4ba71a20d5b1d1d"
    curl -fsSL "https://github.com/cloudflare/cloudflared/releases/download/${cfd_ver}/cloudflared-linux-amd64.deb" -o /tmp/cloudflared.deb
    echo "${cfd_sha}  /tmp/cloudflared.deb" | sha256sum -c - || { echo "ocfp-firstboot: cloudflared checksum mismatch" >&2; exit 1; }
    dpkg -i /tmp/cloudflared.deb || apt-get install -f -y
  fi
  # Idempotent: reinstall the service with the current token.
  cloudflared service install "$cf_token" || { cloudflared service uninstall >/dev/null 2>&1 || true; cloudflared service install "$cf_token"; }
  systemctl enable --now cloudflared >/dev/null 2>&1 || true
fi

# --- tailscale ingress forwarding (nftables DNAT to the CF haproxy) ---
ing_origin=$(jq -r '.ingress.origin_ip // ""' <<<"$sku")
ing_ports=$(jq -r '.ingress.ports // [80,443] | join(", ")' <<<"$sku")
if [[ -n "$ing_origin" ]]; then
  command -v nft >/dev/null 2>&1 || apt-get install -y nftables
  # Idempotent: drop and recreate our own table only, under the lock the
  # watchdog and the boot-time ingress unit also take.
  (
    flock 9
    nft delete table ip ocfp_ingress >/dev/null 2>&1 || true
    nft -f - <<NFT
` + IngressNftTable + `NFT
  ) 9>` + IngressLockPath + `
fi
`

// WatchdogScript keeps the bastion's tailscale connectivity healthy. It
// re-runs `tailscale up` when the node is offline, AND — because Self.Online
// only reflects the control-plane/disco view — probes the actual tun datapath
// with a kernel-level ICMP and restarts tailscaled when that datapath is
// wedged (disco answers while inbound packets never reach the kernel).
// Triggered by ocfp-tailscale-watchdog.timer, and with --ingress-only by
// ocfp-ingress.service at boot, which stops after the ingress block. Reads SMBIOS each invocation so
// PVE-side config edits propagate without bastion restart.
const WatchdogScript = `#!/bin/bash
# ocfp-tailscale-watchdog v4 — recover tailscale connectivity automatically.
#
# Two distinct failure modes, two distinct remedies:
#   1. Offline (Self.Online=false): coordinated drops (lab observed: all
#      bastions offline within a 28s window after a PVE storage-lock storm)
#      leave tailscaled needing a re-up. Remedy: tailscale up.
#   2. Datapath wedge (Self.Online=true but tun datapath dead): userspace
#      answers disco pings so the node looks online, but inbound packets never
#      reach the guest kernel — sshd/ICMP time out (not refused). Re-running
#      tailscale up does NOT rebuild the tun device. Remedy: restart tailscaled.
set -euo pipefail

# systemd starts this with a minimal PATH. Look in the same extra directories
# the dataset script does, after the system ones, so a tool that only linuxbrew
# supplies, such as jq, is still found.
export PATH="${PATH:-/usr/sbin:/usr/bin:/sbin:/bin}:/usr/local/sbin:/usr/local/bin:/home/linuxbrew/.linuxbrew/bin"

family=$(dmidecode -s system-family 2>/dev/null || true)
[[ "$family" == "ocfp-bastion" ]] || exit 0

# The boot-time ingress unit must fail visibly when it cannot read the SKU. A
# missing or broken jq would otherwise leave no origin, build nothing, and
# still report success.
if [[ "${1:-}" == "--ingress-only" ]] && ! jq -n . >/dev/null 2>&1; then
  logger -t ocfp-tailscale-watchdog "jq is missing or cannot run; the ingress table was not built"
  exit 1
fi

# --- keep ingress forwarding rules present (lost on reboot; nft is not persisted).
# This runs BEFORE the connectivity health checks: after a clean reboot
# tailscale comes back healthy on its own, and the healthy-path exit-0s below
# would otherwise skip the reinstall on every invocation.
ing_sku=$(dmidecode -s system-sku-number 2>/dev/null || true)
ing_origin=$(jq -r '.ingress.origin_ip // ""' <<<"$ing_sku" 2>/dev/null || true)
ing_ports=$(jq -r '.ingress.ports // [80,443] | join(", ")' <<<"$ing_sku" 2>/dev/null || true)
# The check and the build run together under one lock, so concurrent writers
# cannot both see the table missing and append the same rules twice.
(
  flock 9
  ing_reason=""
  ing_current=""
  if [[ -n "$ing_origin" ]] && ! ing_current=$(nft list table ip ocfp_ingress 2>/dev/null); then
    ing_reason="ingress nft table missing; reinstalling"
  fi
  # Tables from older scripts DNAT every tailnet 80/443 packet, including
  # subnet-routed traffic bound for other VMs. Replace them.
  if [[ -n "$ing_origin" && -z "$ing_reason" && "$ing_current" != *"fib daddr type local"* ]]; then
    ing_reason="ingress nft table not scoped to local traffic; replacing"
    nft delete table ip ocfp_ingress >/dev/null 2>&1 || true
  fi
  if [[ -n "$ing_reason" ]]; then
    logger -t ocfp-tailscale-watchdog "$ing_reason"
    nft -f - <<NFT
` + IngressNftTable + `NFT
  fi
) 9>` + IngressLockPath + `

# ocfp-ingress.service runs this script with --ingress-only early in each boot,
# to rebuild the table before the timer first fires. It must not reach the
# tailscale health and repair logic below, which pings peers and may restart
# tailscaled or run tailscale up before tailscale is connected.
if [[ "${1:-}" == "--ingress-only" ]]; then
  exit 0
fi

status=$(tailscale status --json 2>/dev/null || true)

# -e makes jq exit non-zero on false/null, so an absent/false Self.Online and a
# blank status (daemon down) both count as offline.
if jq -e '.Self.Online == true' <<<"$status" >/dev/null 2>&1; then
  # Control plane says online — but verify the real datapath. Probe up to a few
  # online peers with a kernel-level ICMP (exercises the tun -> kernel path that
  # wedges independently of disco). If any peer answers, the datapath is healthy.
  mapfile -t peers < <(jq -r '[.Peer[]? | select(.Online == true) | .TailscaleIPs[0] // empty] | .[0:3][]' <<<"$status")
  if [[ ${#peers[@]} -eq 0 ]]; then
    # No online peers to probe against — can't distinguish wedge from a quiet
    # tailnet, so assume healthy and leave it alone.
    exit 0
  fi
  for ip in "${peers[@]}"; do
    if tailscale ping --icmp --timeout 5s -c 1 "$ip" >/dev/null 2>&1; then
      exit 0  # datapath healthy
    fi
  done
  # Online but every peer fails a kernel-level ICMP → tun datapath wedged.
  logger -t ocfp-tailscale-watchdog "online but tun datapath wedged; restarting tailscaled"
  systemctl restart tailscaled || true
  sleep 5
  # Fall through to re-up to re-establish advertised routes/flags post-restart.
else
  logger -t ocfp-tailscale-watchdog "tailscale offline; running tailscale up"
fi

authkey=$(dmidecode -s system-serial-number 2>/dev/null || true)
sku=$(dmidecode -s system-sku-number 2>/dev/null || true)
[[ -n "$authkey" && -n "$sku" ]] || { echo "missing SMBIOS payload" >&2; exit 1; }

hostname=$(jq -r '.hostname // ""' <<<"$sku")
tags=$(jq -r '.tags // [] | join(",")' <<<"$sku")
ssh_flag=$(jq -r 'if (.ssh // true) then "--ssh" else "" end' <<<"$sku")
accept_dns=$(jq -r 'if (.accept_dns // false) then "--accept-dns=true" else "--accept-dns=false" end' <<<"$sku")
accept_routes=$(jq -r 'if (.accept_routes // false) then "--accept-routes=true" else "--accept-routes=false" end' <<<"$sku")
exit_node=$(jq -r 'if (.exit_node // "") != "" then "--exit-node=" + .exit_node else "" end' <<<"$sku")
adv_routes=$(jq -r 'if (.advertise_routes // "") != "" then "--advertise-routes=" + .advertise_routes else "" end' <<<"$sku")

# shellcheck disable=SC2086
tailscale up \
  --authkey="$authkey" \
  --hostname="$hostname" \
  --advertise-tags="$tags" \
  $ssh_flag $accept_dns $accept_routes $adv_routes $exit_node

# --- keep cloudflared connector up ---
cf_token=$(jq -r '.cloudflare.token // ""' <<<"$sku")
if [[ -n "$cf_token" ]] && ! systemctl is-active --quiet cloudflared; then
  logger -t ocfp-tailscale-watchdog "cloudflared inactive; restarting"
  systemctl restart cloudflared || cloudflared service install "$cf_token"
fi
`

// FirstbootService runs FirstbootScript once per VM lifecycle. The sentinel
// at /var/lib/ocfp/firstboot-done blocks re-runs after a successful first
// invocation; deleting it forces a re-run (useful for ops debugging).
const FirstbootService = `[Unit]
Description=OCFP bastion first-boot tailscale setup
After=cloud-init.service network-online.target
Wants=network-online.target
ConditionPathExists=!/var/lib/ocfp/firstboot-done

[Service]
Type=oneshot
ExecStartPre=/bin/mkdir -p /var/lib/ocfp
ExecStart=/usr/local/sbin/ocfp-firstboot
ExecStartPost=/bin/touch /var/lib/ocfp/firstboot-done
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
`

// WatchdogService wraps WatchdogScript as a oneshot unit so the timer can
// trigger it on a cadence. Failures are logged via the script's `logger`
// calls and do not block subsequent invocations.
const WatchdogService = `[Unit]
Description=OCFP tailscale watchdog
After=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/ocfp-tailscale-watchdog
`

// WatchdogTimer fires the watchdog service shortly after boot, then on a
// recurring cadence. Cadence is static (5 min default) and burned into the
// template; per-VM tuning happens in SMBIOS sku JSON which the script reads
// each invocation, so the timer interval never needs to change.
const WatchdogTimer = `[Unit]
Description=OCFP tailscale watchdog timer

[Timer]
OnBootSec=2min
OnUnitActiveSec=5min
Persistent=true

[Install]
WantedBy=timers.target
`

// IngressService rebuilds the ocfp_ingress nft table early in every boot. The
// table lives only in the kernel, and ocfp-firstboot skips itself after its
// first run, so without this unit the table would wait for the watchdog timer's
// first firing, up to a few minutes into the boot.
//
// It runs the watchdog script itself, in ingress-only mode, so the table is
// built by the one ingress block the watchdog already keeps and cannot drift
// from it. Ingress-only mode exits before any tailscale check, so nothing here
// pings peers, restarts tailscaled, or runs tailscale up before tailscale is
// connected. The rule matches the interface by name, so the table loads before
// tailscale0 exists, and the unit has no need to wait for the network.
//
// The ExecCondition line skips the unit, without failing it, when a partial
// install left an older watchdog in place. That script ignores the argument and
// would run the full tailscale repair at boot. ExecCondition needs systemd 243,
// and Ubuntu 22.04 and 24.04 ship 249 and 255.
const IngressService = `[Unit]
Description=OCFP ingress forwarding table
After=ocfp-dataset.service ocfp-firstboot.service

[Service]
Type=oneshot
ExecCondition=/bin/grep -q -- --ingress-only /usr/local/sbin/ocfp-tailscale-watchdog
ExecStart=/usr/local/sbin/ocfp-tailscale-watchdog --ingress-only
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
`
