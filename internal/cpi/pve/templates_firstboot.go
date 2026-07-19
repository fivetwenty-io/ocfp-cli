package pve

// Files written into the auto-provisioned template image during
// ProvisionTemplate. Cloned VMs inherit them; firstboot.service runs on first
// boot, gates on the SMBIOS family discriminator, and configures tailscale
// from the SMBIOS payload. The watchdog timer keeps tailscale online
// thereafter.
//
// The same scripts run on every VM cloned from this template (artifacts VMs
// too). They no-op unless `dmidecode -s system-family == ocfp-bastion`, so
// it's safe to bake them universally.

// smbiosFamilyBastion is the role discriminator the firstboot script checks
// in SMBIOS Type-1 Family before doing anything. Bastion VMs get this value;
// other roles (artifacts, jumpbox) get a different family value or none, and
// the firstboot script exits 0 without touching them.
const smbiosFamilyBastion = "ocfp-bastion"

// firstbootScript reads the bastion config from SMBIOS, installs tailscale
// (idempotent), and runs `tailscale up`. Invoked once by
// ocfp-firstboot.service after cloud-init + network-online.
const firstbootScript = `#!/bin/bash
# ocfp-firstboot v2 — runs once at first boot of a VM cloned from an
# OCFP-provisioned template. Reads its config from SMBIOS so the cloning
# step needs no file delivery to the PVE host (PVE 9.x API doesn't permit
# snippet uploads).
set -euo pipefail

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
  # Idempotent: drop and recreate our own table only.
  nft delete table ip ocfp_ingress >/dev/null 2>&1 || true
  nft -f - <<NFT
table ip ocfp_ingress {
  chain prerouting {
    type nat hook prerouting priority dstnat; policy accept;
    iifname "tailscale0" tcp dport { $ing_ports } dnat to $ing_origin
  }
  chain postrouting {
    type nat hook postrouting priority srcnat; policy accept;
    ip daddr $ing_origin tcp dport { $ing_ports } masquerade
  }
}
NFT
fi
`

// watchdogScript keeps the bastion's tailscale connectivity healthy. It
// re-runs `tailscale up` when the node is offline, AND — because Self.Online
// only reflects the control-plane/disco view — probes the actual tun datapath
// with a kernel-level ICMP and restarts tailscaled when that datapath is
// wedged (disco answers while inbound packets never reach the kernel).
// Triggered by ocfp-tailscale-watchdog.timer. Reads SMBIOS each invocation so
// PVE-side config edits propagate without bastion restart.
const watchdogScript = `#!/bin/bash
# ocfp-tailscale-watchdog v3 — recover tailscale connectivity automatically.
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

family=$(dmidecode -s system-family 2>/dev/null || true)
[[ "$family" == "ocfp-bastion" ]] || exit 0

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

# --- keep ingress forwarding rules present (lost on reboot; nft is not persisted) ---
ing_origin=$(jq -r '.ingress.origin_ip // ""' <<<"$sku")
ing_ports=$(jq -r '.ingress.ports // [80,443] | join(", ")' <<<"$sku")
if [[ -n "$ing_origin" ]] && ! nft list table ip ocfp_ingress >/dev/null 2>&1; then
  logger -t ocfp-tailscale-watchdog "ingress nft table missing; reinstalling"
  nft -f - <<NFT
table ip ocfp_ingress {
  chain prerouting {
    type nat hook prerouting priority dstnat; policy accept;
    iifname "tailscale0" tcp dport { $ing_ports } dnat to $ing_origin
  }
  chain postrouting {
    type nat hook postrouting priority srcnat; policy accept;
    ip daddr $ing_origin tcp dport { $ing_ports } masquerade
  }
}
NFT
fi
`

// firstbootService runs firstbootScript once per VM lifecycle. The sentinel
// at /var/lib/ocfp/firstboot-done blocks re-runs after a successful first
// invocation; deleting it forces a re-run (useful for ops debugging).
const firstbootService = `[Unit]
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

// watchdogService wraps watchdogScript as a oneshot unit so the timer can
// trigger it on a cadence. Failures are logged via the script's `logger`
// calls and do not block subsequent invocations.
const watchdogService = `[Unit]
Description=OCFP tailscale watchdog
After=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/ocfp-tailscale-watchdog
`

// watchdogTimer fires the watchdog service shortly after boot, then on a
// recurring cadence. Cadence is static (5 min default) and burned into the
// template; per-VM tuning happens in SMBIOS sku JSON which the script reads
// each invocation, so the timer interval never needs to change.
const watchdogTimer = `[Unit]
Description=OCFP tailscale watchdog timer

[Timer]
OnBootSec=2min
OnUnitActiveSec=5min
Persistent=true

[Install]
WantedBy=timers.target
`
