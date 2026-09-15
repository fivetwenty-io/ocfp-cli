package pve

// datasetScript prepares the bastion's persistent data disk and restores the
// state that lives on it, early enough that sshd and tailscaled see it.
//
// It is baked into the bastion template rather than delivered over SSH,
// because the work it does has to happen before the daemon that serves SSH
// starts. Per-VM configuration arrives in the SMBIOS SKU blob, the same way
// the firstboot script gets its tailscale config, since PVE 9.x cannot deliver
// cloud-init snippets to per-VM clones.
//
// It runs on every boot, not once. The bind mount has to be re-established
// after each reboot, and a once-only sentinel would leave the second boot
// looking at an empty home directory.
//
// The design rule it enforces, and the reason the provisioning marker moved
// off the home directory: the persistent disk carries data, never claims about
// the state of the OS.
//
//nolint:dupword // the embedded shell closes nested ifs, so `fi` follows `fi`
const datasetScript = `#!/usr/bin/env bash
#
# OCFP bastion data disk: format if blank, mount, bind the home directory,
# and restore the system state that lives outside it.
#
# Managed by ocfp. Baked into the bastion template by the template seed.
set -uo pipefail

log() { logger -t ocfp-dataset -s -- "$*" 2>&1; }

SKU="$(dmidecode -s system-sku-number 2>/dev/null || true)"
if [ -z "${SKU}" ]; then
  log "no SMBIOS SKU; nothing to do"
  exit 0
fi

DATA="$(printf '%s' "${SKU}" | jq -r '.data // empty' 2>/dev/null || true)"
if [ -z "${DATA}" ]; then
  log "no data-disk block in SKU; nothing to do"
  exit 0
fi

SERIAL="$(printf '%s' "${DATA}" | jq -r '.serial // empty')"
MOUNT="$(printf '%s' "${DATA}" | jq -r '.mountpoint // "/data"')"
FSTYPE="$(printf '%s' "${DATA}" | jq -r '.filesystem // "ext4"')"
HOMEDIR="$(printf '%s' "${DATA}" | jq -r '.home // "/home/ubuntu"')"
HOMEUSER="$(printf '%s' "${DATA}" | jq -r '.user // "ubuntu"')"
PUBKEY="$(printf '%s' "${DATA}" | jq -r '.authorized_key // empty')"

if [ -z "${SERIAL}" ]; then
  log "data block carries no disk serial; refusing to guess a device"
  exit 0
fi

# Resolve the disk by its serial rather than by /dev/sdN. The letter depends
# on enumeration order and is correct today only because a cloned template
# happens to occupy scsi0 and nothing else is attached.
#
# Two lookups, because udev does not name by-id entries the same way on every
# release. Ubuntu Noble names them after the serial
# (scsi-SQEMU_QEMU_HARDDISK_ocfpdata); Ubuntu Resolute names them after the
# slot (scsi-0QEMU_QEMU_HARDDISK_drive-scsi1). The serial is authoritative on
# both and lsblk reports it on both, so by-id is tried first as the cheaper and
# more specific match and lsblk is the fallback.
REAL=""
for candidate in /dev/disk/by-id/*"${SERIAL}"; do
  if [ -b "${candidate}" ]; then
    REAL="$(readlink -f "${candidate}")"
    break
  fi
done

if [ -z "${REAL}" ]; then
  REAL="$(lsblk -dno PATH,SERIAL 2>/dev/null | awk -v s="${SERIAL}" '$2 == s { print $1; exit }')"
fi

if [ -z "${REAL}" ] || [ ! -b "${REAL}" ]; then
  log "no block device with serial ${SERIAL}; the data disk is not attached"
  exit 0
fi
log "data disk ${SERIAL} resolved to ${REAL}"

# Refuse outright if the device backs the running root filesystem. This script
# formats, and the cost of getting the device wrong is the whole machine.
ROOTSRC="$(findmnt -n -o SOURCE / 2>/dev/null || true)"
if [ -n "${ROOTSRC}" ] && [ "$(readlink -f "${ROOTSRC}" 2>/dev/null)" = "${REAL}" ]; then
  log "FATAL: ${REAL} backs the root filesystem; refusing to touch it"
  exit 1
fi

EXISTING="$(blkid -o value -s TYPE "${REAL}" 2>/dev/null || true)"

if [ -z "${EXISTING}" ]; then
  log "formatting blank disk ${REAL} as ${FSTYPE}"
  case "${FSTYPE}" in
    ext4) mkfs.ext4 -q -L ocfp-data "${REAL}" || { log "FATAL: mkfs failed"; exit 1; } ;;
    xfs)  mkfs.xfs -q -L ocfp-data "${REAL}" || { log "FATAL: mkfs failed"; exit 1; } ;;
    *)    log "FATAL: unsupported filesystem ${FSTYPE}"; exit 1 ;;
  esac
elif [ "${EXISTING}" != "${FSTYPE}" ]; then
  # Never reformat a disk that already carries something. It holds the
  # operator's home directory and the inception vault among it.
  log "FATAL: ${REAL} carries ${EXISTING}, expected ${FSTYPE}; refusing to reformat. Wipe it by hand if that is really what you want."
  exit 1
fi

mkdir -p "${MOUNT}"

if ! findmnt -n "${MOUNT}" >/dev/null 2>&1; then
  mount "${REAL}" "${MOUNT}" || { log "FATAL: mount ${REAL} at ${MOUNT} failed"; exit 1; }
  log "mounted ${REAL} at ${MOUNT}"
fi

# Keep fstab keyed by UUID so a manual boot without this unit still mounts,
# and so the entry survives the device path changing.
UUID="$(blkid -o value -s UUID "${REAL}" 2>/dev/null || true)"
if [ -n "${UUID}" ] && ! grep -q "UUID=${UUID}" /etc/fstab 2>/dev/null; then
  printf 'UUID=%s %s %s defaults,nofail 0 2\n' "${UUID}" "${MOUNT}" "${FSTYPE}" >> /etc/fstab
  log "recorded ${MOUNT} in /etc/fstab"
fi

DATA_HOME="${MOUNT}/home/$(basename "${HOMEDIR}")"
SYSDIR="${MOUNT}/system"

mkdir -p "${DATA_HOME}" "${SYSDIR}"

# Seed the persistent home from the image's own home on a genuinely fresh
# disk, so the cloud-init user keeps its dotfiles and its authorized_keys.
if [ -z "$(ls -A "${DATA_HOME}" 2>/dev/null)" ] && [ -d "${HOMEDIR}" ]; then
  log "seeding ${DATA_HOME} from ${HOMEDIR}"
  cp -a "${HOMEDIR}/." "${DATA_HOME}/" 2>/dev/null || true
fi

chown "${HOMEUSER}:${HOMEUSER}" "${DATA_HOME}" 2>/dev/null || true

if ! findmnt -n "${HOMEDIR}" >/dev/null 2>&1; then
  mount --bind "${DATA_HOME}" "${HOMEDIR}" || { log "FATAL: bind ${DATA_HOME} onto ${HOMEDIR} failed"; exit 1; }
  log "bound ${DATA_HOME} onto ${HOMEDIR}"
fi

if ! grep -q " ${HOMEDIR} " /etc/fstab 2>/dev/null; then
  printf '%s %s none bind,nofail 0 0\n' "${DATA_HOME}" "${HOMEDIR}" >> /etc/fstab
fi

# Re-assert the bootstrap public key on every boot.
#
# This is the guard that makes persisting the whole home directory safe. The
# persistent copy brings back an authorized_keys from the retired machine, and
# if the replacement's keypair differs the bastion comes up healthy, joins the
# tailnet, and nobody can get in. Append rather than replace so keys an
# operator added by hand survive.
if [ -n "${PUBKEY}" ]; then
  AK="${HOMEDIR}/.ssh/authorized_keys"
  mkdir -p "${HOMEDIR}/.ssh"
  chmod 0700 "${HOMEDIR}/.ssh"
  touch "${AK}"
  if ! grep -qF "${PUBKEY}" "${AK}" 2>/dev/null; then
    printf '%s\n' "${PUBKEY}" >> "${AK}"
    log "re-asserted the bootstrap public key in ${AK}"
  fi
  chmod 0600 "${AK}"
  chown -R "${HOMEUSER}:${HOMEUSER}" "${HOMEDIR}/.ssh" 2>/dev/null || true
fi

# Keep a directory whose contents are pure data on the data disk, and bind
# that copy over the path the software expects to find it at.
#
# The obvious alternative, copying the directory off the disk on every boot,
# is wrong twice. Anything the running system writes there is thrown away at
# the next reboot, which for tailscale means the node key the daemon rotates on
# its own schedule and for letsencrypt means a freshly renewed certificate. And
# a copy races the daemon that reads it: restore the tailscale state a moment
# after tailscaled has started and the daemon carries on with the identity it
# already registered, so the bastion comes back as a second node at a new
# address. A bind mount has neither problem, because there is only ever one
# copy and the daemon opens it directly.
#
# Only directories that hold data belong here. Anything the operating system
# owns has to keep coming from the release we just installed.
persist_dir() {
  live="$1"; store="$2"; label="$3"

  if [ ! -d "${store}" ]; then
    mkdir -p "${store}"
    if [ -d "${live}" ]; then
      cp -a "${live}/." "${store}/" 2>/dev/null || true
      log "captured ${label} onto the data disk"
    fi
  fi

  mkdir -p "${live}"

  if ! findmnt -n "${live}" >/dev/null 2>&1; then
    mount --bind "${store}" "${live}" || { log "FATAL: bind ${store} onto ${live} failed"; exit 1; }
    log "bound ${label} from ${store} onto ${live}"
  fi

  if ! grep -q " ${live} " /etc/fstab 2>/dev/null; then
    printf '%s %s none bind,nofail 0 0\n' "${store}" "${live}" >> /etc/fstab
  fi
}

# SSH host keys are copied rather than bound, and only the key files move.
#
# Restoring them is what stops every operator, and the artifacts provisioner's
# ProxyCommand, hitting a host-key mismatch after a rebuild. But /etc/ssh also
# holds sshd_config, ssh_config, and the moduli file, and those belong to the
# release. Moving the whole directory put Ubuntu Noble's sshd_config onto
# Ubuntu Resolute the first time we cycled the lab, quietly undoing whatever
# the newer image ships. So we move the keys and leave the configuration alone.
persist_host_keys() {
  store="${SYSDIR}/ssh"
  mkdir -p "${store}"

  if ls "${store}"/ssh_host_* >/dev/null 2>&1; then
    cp -a "${store}"/ssh_host_* /etc/ssh/ 2>/dev/null && log "restored ssh host keys from ${store}"
  elif ls /etc/ssh/ssh_host_* >/dev/null 2>&1; then
    cp -a /etc/ssh/ssh_host_* "${store}/" 2>/dev/null && log "captured ssh host keys onto the data disk"
  fi
}

persist_host_keys

# Tailscale identity. Binding it back brings the replacement onto the tailnet
# as the same node at the same address rather than as a new one.
persist_dir /var/lib/tailscale "${SYSDIR}/tailscale" "tailscale state"

persist_dir /etc/letsencrypt "${SYSDIR}/letsencrypt" "letsencrypt material"

# The OS-disk state directory. Claims about how far provisioning got belong
# here, on the disposable disk, never on the persistent one.
mkdir -p /var/lib/ocfp

log "data disk ready at ${MOUNT}"
exit 0
`

// datasetService runs datasetScript on every boot, before the daemons that
// read what it restores.
//
// DefaultDependencies=no plus the sysinit ordering puts it early enough to
// win the race with sshd and tailscaled. Ordering here is correctness: the
// SSH host keys and the tailscale state directory have to be in place before
// the daemon that reads them starts, or the bastion comes back with a churned
// host key and as a new tailnet node. ocfp-firstboot is in that list for the
// same reason: it runs "tailscale up" with an auth key, and if it wins the race
// the bastion registers as a brand new node and the identity we went to the
// trouble of preserving never gets used.
const datasetService = `[Unit]
Description=OCFP bastion data disk
DefaultDependencies=no
After=systemd-udev-settle.service local-fs.target
Wants=systemd-udev-settle.service
Before=ssh.service sshd.service tailscaled.service ocfp-firstboot.service cloud-init.service sysinit.target
ConditionVirtualization=!container

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/local/sbin/ocfp-dataset

[Install]
WantedBy=sysinit.target
`

// seedUnitFile is one file the template seed installs into the image.
// keepHostKeysConfig stops cloud-init from replacing the SSH host keys that
// ocfp-dataset restores off the data disk.
//
// ocfp-dataset runs at sysinit and puts the retired machine's host keys back.
// cloud-init's ssh module then runs in the config stage and, with its default
// ssh_deletekeys, deletes them and generates a new set, so the replacement's
// first boot presents an identity nobody has seen. Every later boot is
// correct, because cloud-init does not re-run its first-boot modules, which
// makes this a confusing failure as well as a disruptive one.
const keepHostKeysConfig = `# Managed by OCFP.
# The bastion's SSH host keys are data: they live on the persistent disk and
# ocfp-dataset restores them before sshd starts. cloud-init must not replace
# them, or a recycled bastion comes up as a stranger to every known_hosts file.
ssh_deletekeys: false
`

type seedUnitFile struct {
	path    string
	content string
	mode    string
}

// seedUnitFiles is the set of scripts and units baked into the bastion
// template. Split out from seedWriteUnits so tests can assert on the set
// without driving a serial console.
func seedUnitFiles() []seedUnitFile {
	return []seedUnitFile{
		{"/usr/local/sbin/ocfp-firstboot", firstbootScript, "0755"},
		{"/usr/local/sbin/ocfp-tailscale-watchdog", watchdogScript, "0755"},
		{"/usr/local/sbin/ocfp-dataset", datasetScript, "0755"},
		{"/etc/systemd/system/ocfp-firstboot.service", firstbootService, "0644"},
		{"/etc/systemd/system/ocfp-tailscale-watchdog.service", watchdogService, "0644"},
		{"/etc/systemd/system/ocfp-tailscale-watchdog.timer", watchdogTimer, "0644"},
		{"/etc/systemd/system/ocfp-dataset.service", datasetService, "0644"},
		{"/etc/cloud/cloud.cfg.d/99-ocfp-keep-host-keys.cfg", keepHostKeysConfig, "0644"},
	}
}

// seedEnableCommands enables the units the seed installed.
func seedEnableCommands() []string {
	return []string{
		"sudo systemctl daemon-reload",
		"sudo systemctl enable ocfp-firstboot.service",
		"sudo systemctl enable ocfp-tailscale-watchdog.timer",
		"sudo systemctl enable ocfp-dataset.service",
	}
}
