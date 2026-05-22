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
# ocfp-firstboot v1 — runs once at first boot of a VM cloned from an
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

# Assemble the up invocation; quoted authkey for safety.
# shellcheck disable=SC2086
tailscale up \
  --authkey="$authkey" \
  --hostname="$hostname" \
  --advertise-tags="$tags" \
  $ssh_flag $accept_dns $accept_routes $adv_routes $exit_node
`

// watchdogScript re-runs `tailscale up` whenever tailnet shows the node as
// offline. Triggered by ocfp-tailscale-watchdog.timer. Reads SMBIOS each
// invocation so PVE-side config edits propagate without bastion restart.
const watchdogScript = `#!/bin/bash
# ocfp-tailscale-watchdog v1 — re-up tailscale when Self.Online=false.
# Coordinated drops (lab observed: all bastions offline within a 28s
# window after a PVE storage-lock storm) leave tailscaled wedged; this
# brings it back without operator intervention.
set -euo pipefail

family=$(dmidecode -s system-family 2>/dev/null || true)
[[ "$family" == "ocfp-bastion" ]] || exit 0

# Self.Online absent or false → re-up. -e makes jq exit non-zero on false/null.
if tailscale status --json 2>/dev/null | jq -e '.Self.Online == true' >/dev/null 2>&1; then
  exit 0
fi

logger -t ocfp-tailscale-watchdog "tailscale offline; running tailscale up"

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
