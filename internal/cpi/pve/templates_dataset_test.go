package pve

import (
	"strings"
	"testing"
)

// TestDatasetService_OrderedBeforeSSHAndTailscale is the load-bearing
// assertion in this file.
//
// The unit restores the SSH host keys and the tailscale state directory off
// the data disk. Both have to be in place before the daemon that reads them
// starts, or the bastion comes back with a churned host key and as a new
// tailnet node, which is exactly the disruption the persistent disk exists to
// avoid. Ordering is therefore correctness, not tidiness.
func TestDatasetService_OrderedBeforeSSHAndTailscale(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		"Before=",
		"ssh.service",
		"tailscaled.service",
		"DefaultDependencies=no",
	} {
		if !strings.Contains(datasetService, want) {
			t.Errorf("ocfp-dataset.service is missing %q:\n%s", want, datasetService)
		}
	}

	// It must run on every boot, not once. A once-only sentinel would leave
	// the bind mount unestablished after the first reboot.
	if strings.Contains(datasetService, "ConditionPathExists=!") {
		t.Error("ocfp-dataset.service carries a once-only sentinel; it must run on every boot")
	}
}

// TestDatasetScript_ResolvesDiskByID asserts the script never trusts /dev/sdb.
//
// The letter depends on enumeration order and is correct today only because a
// cloned template happens to occupy scsi0 and nothing else is attached. The
// disk carries a fixed serial precisely so the guest can look it up under
// /dev/disk/by-id instead.
func TestDatasetScript_ResolvesDiskByID(t *testing.T) {
	t.Parallel()

	if !strings.Contains(datasetScript, "/dev/disk/by-id/") {
		t.Error("the dataset script does not resolve the disk under /dev/disk/by-id")
	}

	if strings.Contains(datasetScript, "/dev/sdb") {
		t.Error("the dataset script still hardcodes /dev/sdb")
	}
}

// TestDatasetScript_FormatsOnlyWhenBlank asserts the format is guarded.
//
// This script runs on every boot against a disk holding the operator's entire
// home directory, the inception vault among it. An unguarded mkfs is the worst
// outcome in this whole feature.
func TestDatasetScript_FormatsOnlyWhenBlank(t *testing.T) {
	t.Parallel()

	if !strings.Contains(datasetScript, "blkid") {
		t.Fatal("the dataset script does not probe with blkid before formatting")
	}

	blkidAt := strings.Index(datasetScript, "blkid")
	mkfsAt := strings.Index(datasetScript, "mkfs")

	if mkfsAt < 0 {
		t.Fatal("the dataset script never formats")
	}

	if blkidAt > mkfsAt {
		t.Error("the dataset script formats before probing with blkid")
	}

	// A disk carrying a different filesystem must stop the script rather than
	// be reformatted.
	if !strings.Contains(datasetScript, "refusing") {
		t.Error("the dataset script does not refuse a disk carrying an unexpected filesystem")
	}
}

// TestDatasetScript_ReassertsAuthorizedKeys guards the lockout the whole-home
// layout would otherwise create.
//
// The persistent home brings back an authorized_keys from the retired machine.
// If the replacement's keypair differs, the bastion comes up healthy, joins
// the tailnet, and nobody can get in. The script therefore always re-adds the
// bootstrap public key, appending rather than replacing so keys an operator
// added by hand survive.
func TestDatasetScript_ReassertsAuthorizedKeys(t *testing.T) {
	t.Parallel()

	if !strings.Contains(datasetScript, "authorized_keys") {
		t.Fatal("the dataset script never touches authorized_keys; a stale copy could lock us out")
	}

	if !strings.Contains(datasetScript, ">>") {
		t.Error("the dataset script should append to authorized_keys, not replace it")
	}
}

// TestDatasetScript_MarkerLivesOnTheOSDisk pins the rule that generalises past
// this script: the persistent disk carries data, never claims about the state
// of the OS.
//
// isAlreadyProvisioned short-circuits the whole provisioning run when it finds
// a marker. With the home directory persisted, a marker written there would
// survive onto a fresh machine with no brew, no binaries, and no genesis, and
// bastion init would decide there was nothing to do.
func TestDatasetScript_MarkerLivesOnTheOSDisk(t *testing.T) {
	t.Parallel()

	if !strings.Contains(datasetScript, "/var/lib/ocfp") {
		t.Error("the dataset script does not reference the OS-disk state directory")
	}
}

// TestDatasetScript_BindMountsHome asserts the chosen layout is what the
// script actually implements.
//
// The home and system paths must be derived from the configured mountpoint
// rather than written as literal /data paths, because the mountpoint is a
// config knob and a hardcoded path would silently ignore it.
func TestDatasetScript_BindMountsHome(t *testing.T) {
	t.Parallel()

	for _, want := range []string{"--bind", "DATA_HOME=\"${MOUNT}/home/", "SYSDIR=\"${MOUNT}/system\""} {
		if !strings.Contains(datasetScript, want) {
			t.Errorf("the dataset script is missing %q", want)
		}
	}

	if strings.Contains(datasetScript, "\"/data/home") || strings.Contains(datasetScript, "\"/data/system") {
		t.Error("the dataset script hardcodes /data instead of deriving from the configured mountpoint")
	}
}

// TestSeedWritesDatasetUnit asserts the unit is actually installed into the
// template, since a unit that exists only in Go reaches no bastion.
func TestSeedWritesDatasetUnit(t *testing.T) {
	t.Parallel()

	files := seedUnitFiles()

	var sawScript, sawUnit bool

	for _, f := range files {
		switch f.path {
		case "/usr/local/sbin/ocfp-dataset":
			sawScript = true

			if f.mode != "0755" {
				t.Errorf("dataset script mode = %q, want 0755", f.mode)
			}
		case "/etc/systemd/system/ocfp-dataset.service":
			sawUnit = true
		}
	}

	if !sawScript {
		t.Error("the seed does not install /usr/local/sbin/ocfp-dataset")
	}

	if !sawUnit {
		t.Error("the seed does not install ocfp-dataset.service")
	}
}

// TestSeedEnablesDatasetUnit asserts the unit is enabled, not merely written.
func TestSeedEnablesDatasetUnit(t *testing.T) {
	t.Parallel()

	var found bool

	for _, c := range seedEnableCommands() {
		if strings.Contains(c, "ocfp-dataset.service") && strings.Contains(c, "enable") {
			found = true
		}
	}

	if !found {
		t.Errorf("the seed never enables ocfp-dataset.service: %v", seedEnableCommands())
	}
}

// TestDatasetScript_ResolvesSerialAcrossDistros covers a difference found
// between Ubuntu Noble and Ubuntu Resolute on the same hypervisor.
//
// On Noble, udev names the by-id entry after the disk's serial:
// /dev/disk/by-id/scsi-SQEMU_QEMU_HARDDISK_ocfpdata. On Resolute it names it
// after the slot instead: scsi-0QEMU_QEMU_HARDDISK_drive-scsi1. A lookup that
// only globs for the serial under by-id therefore finds nothing on Resolute,
// and the data disk goes unmounted — which is exactly how a recycled bastion
// comes up with an empty home directory.
//
// The serial is still authoritative and still visible; lsblk reports it on
// every distro. So the script tries by-id first and falls back to matching the
// serial reported by lsblk.
func TestDatasetScript_ResolvesSerialAcrossDistros(t *testing.T) {
	t.Parallel()

	if !strings.Contains(datasetScript, "lsblk") {
		t.Error("the dataset script has no lsblk fallback; it would fail on Resolute, where by-id is named after the slot")
	}

	if !strings.Contains(datasetScript, "/dev/disk/by-id/") {
		t.Error("the dataset script dropped the by-id lookup")
	}

	// The fallback must come after the by-id attempt, not instead of it.
	// Compare the code, not the prose: the explanation above the loop mentions
	// both mechanisms and would otherwise decide the ordering.
	byIDAt := strings.Index(datasetScript, `for candidate in /dev/disk/by-id/`)
	lsblkAt := strings.Index(datasetScript, `lsblk -dno PATH,SERIAL`)

	if byIDAt < 0 || lsblkAt < 0 {
		t.Fatalf("expected both lookups in the script (by-id at %d, lsblk at %d)", byIDAt, lsblkAt)
	}

	if byIDAt > lsblkAt {
		t.Error("the lsblk fallback precedes the by-id lookup; by-id is the cheaper and more specific match")
	}
}

// TestDatasetScript_CarriesHostKeysNotSSHConfig guards the line between data
// and operating system, at the place where we first crossed it.
//
// The restore used to copy the whole of /etc/ssh off the data disk. On the
// lab's first cycle onto Resolute that put Ubuntu Noble's sshd_config, and an
// ssh_import_id from 2020, onto Ubuntu 26.04, silently undoing whatever the
// newer image ships. The host keys are data and have to survive the cycle.
// Everything else in that directory belongs to the release we just installed.
func TestDatasetScript_CarriesHostKeysNotSSHConfig(t *testing.T) {
	t.Parallel()

	if !strings.Contains(datasetScript, "ssh_host_") {
		t.Error("the dataset script does not narrow the ssh restore to the host key files")
	}

	for _, forbidden := range []string{
		`cp -a "${store}/." /etc/ssh`,
		`"${SYSDIR}/ssh" /etc/ssh`,
		`--bind "${SYSDIR}/ssh" /etc/ssh`,
	} {
		if strings.Contains(datasetScript, forbidden) {
			t.Errorf("the dataset script still moves the whole of /etc/ssh (%q); it would carry the retired release's sshd_config forward", forbidden)
		}
	}
}

// TestDatasetScript_BindsPureDataDirectories covers a revert we watched happen.
//
// Copying a directory off the data disk on every boot means anything the
// running system writes there is thrown away at the next reboot. For tailscale
// that is the node key, which the daemon rotates on its own schedule; for
// letsencrypt it is a renewed certificate. Worse, a copy races the daemon: the
// tailscale state we restored by hand landed on disk while tailscaled was
// already running, so the daemon kept the identity it had registered with and
// the bastion came back as a second node at a new address.
//
// Binding the data-disk copy over the live path removes both problems, because
// then there is only ever one copy and the daemon opens it directly.
func TestDatasetScript_BindsPureDataDirectories(t *testing.T) {
	t.Parallel()

	for _, dir := range []string{"/var/lib/tailscale", "/etc/letsencrypt"} {
		if !strings.Contains(datasetScript, dir) {
			t.Fatalf("the dataset script no longer persists %s", dir)
		}
	}

	if !strings.Contains(datasetScript, "persist_dir ") {
		t.Fatal("the dataset script has no bind-mount helper for the pure-data directories")
	}

	// The helper has to actually bind, not copy.
	idx := strings.Index(datasetScript, "persist_dir() {")
	if idx < 0 {
		t.Fatal("persist_dir is called but never defined")
	}

	body := datasetScript[idx:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}

	if !strings.Contains(body, "mount --bind") {
		t.Errorf("persist_dir does not bind-mount; a per-boot copy reverts whatever the daemon wrote:\n%s", body)
	}
}

// TestDatasetService_OrderedBeforeFirstboot keeps the tailscale identity from
// being decided before the data disk has had its say.
//
// ocfp-firstboot runs "tailscale up" with an auth key. If it wins the race, the
// bastion registers as a brand new node and the identity we preserved on the
// data disk never gets used.
func TestDatasetService_OrderedBeforeFirstboot(t *testing.T) {
	t.Parallel()

	if !strings.Contains(datasetService, "ocfp-firstboot.service") {
		t.Errorf("ocfp-dataset.service does not order itself before ocfp-firstboot.service:\n%s", datasetService)
	}
}
