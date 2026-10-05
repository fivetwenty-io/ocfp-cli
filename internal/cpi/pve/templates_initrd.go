package pve

// Files the template seed writes into a dracut-based image (Ubuntu 26.04
// onward) to keep networking out of the initramfs. See
// TemplateSpec.DisableInitrdNetwork for the failure this prevents.

// initrdNoNetworkConfPath is the dracut drop-in the seed writes. It stays in
// the template, so every clone's later initramfs rebuilds honour it.
const initrdNoNetworkConfPath = "/etc/dracut.conf.d/90-ocfp-no-network.conf"

// initrdNoNetworkConf leaves dracut's network modules out of the initramfs.
// The module list matches Ubuntu's opt-in profile, which notes that every
// dracut network module depends on net-lib or systemd-networkd. dyn-netconf
// is Ubuntu's generator for the default DHCP .network file; it depends on
// systemd-networkd, so dracut would drop it anyway, but naming it here keeps
// dracut from logging an error about it on every rebuild.
const initrdNoNetworkConf = `# Written by the OCFP template seed. The initramfs must not bring the NIC
# up: cloud-init renames the link to eth0 later in boot, the rename fails
# while the link is up, and the static address never applies. Mirrors
# /usr/lib/dracut/dracut.conf.d/no-network/10-no-network.conf.
omit_dracutmodules+=" net-lib systemd-networkd dyn-netconf "
`

// initrdNoNetworkScriptPath is where the seed stages initrdNoNetworkScript.
// The seed deletes it after a successful run.
const initrdNoNetworkScriptPath = "/tmp/ocfp-initrd-no-network"

// initrdNoNetworkScript rebuilds every installed kernel's initramfs with the
// drop-in in place, then reads each image's module list back. It fails when
// an image is missing, when its module list can't be read, or when any
// network module survived, so the seed never converts a template whose
// initramfs could still DHCP the NIC.
//
// dracut stages each image under a private tmpfs. Its default staging
// directory is /var/tmp, on the template's root filesystem, and the template
// disk is only as large as the cloud image, so a rebuild there runs out of
// space partway through depmod.
const initrdNoNetworkScript = `#!/bin/bash
# ocfp-initrd-no-network: run once by the OCFP template seed, then deleted.
set -euo pipefail

stage=$(mktemp -d /run/ocfp-dracut.XXXXXX)
mount -t tmpfs -o size=1g,mode=0700 ocfp-dracut "$stage"
trap 'umount "$stage"; rmdir "$stage"' EXIT

dracut --quiet --force --regenerate-all --tmpdir "$stage"

shopt -s nullglob
images=(/boot/initrd.img-*)
if (( ${#images[@]} == 0 )); then
  echo "ocfp-initrd-no-network: no initramfs images under /boot" >&2
  exit 1
fi

for img in "${images[@]}"; do
  modules=$(lsinitrd -m "$img" | sed -n '/^dracut modules:$/,$p')
  if ! grep -qx systemd <<<"$modules"; then
    echo "ocfp-initrd-no-network: cannot read the module list of $img" >&2
    exit 1
  fi
  if grep -xE 'net-lib|systemd-networkd|dyn-netconf|network|network-legacy|network-manager' <<<"$modules"; then
    echo "ocfp-initrd-no-network: $img still carries network modules" >&2
    exit 1
  fi
done
`
