package pve

import (
	"strings"
	"testing"
)

// TestSeedKeepsCloudInitFromRegeneratingHostKeys covers the failure that made
// a recycled bastion present a host key nobody had ever seen.
//
// ocfp-dataset runs at sysinit, before the hostname is even set, and restores
// the SSH host keys off the data disk. cloud-init's ssh module then runs in the
// config stage with its default ssh_deletekeys, deletes what it finds, and
// writes a fresh set about two seconds later. The replacement therefore comes
// up on its first boot with a brand new identity, and every operator meets a
// host-key-changed warning. Worse, anyone who clears their known_hosts entry
// to get past it is warned a second time after the next reboot, when
// cloud-init no longer runs its first-boot modules and the real key returns.
//
// The fix belongs here rather than in the unit ordering. Running ocfp-dataset
// after cloud-init would also mean the home bind and the tailscale state
// arrive after sshd and tailscaled, which is the very problem the early
// ordering exists to solve.
func TestSeedKeepsCloudInitFromRegeneratingHostKeys(t *testing.T) {
	t.Parallel()

	found := seedFile("/etc/cloud/cloud.cfg.d/99-ocfp-keep-host-keys.cfg")
	if found == nil {
		t.Fatal("the seed installs no keep-host-keys drop-in, so cloud-init will delete the host keys the dataset unit restored")
	}

	if !strings.Contains(found.content, "ssh_deletekeys: false") {
		t.Errorf("%s does not turn off ssh_deletekeys:\n%s", found.path, found.content)
	}
}

// TestSeedTrustsCloudInitInstanceCache covers a bastion that turned into a
// new instance at its first cold start after the recycle flow renamed it.
//
// PVE derives the NoCloud instance-id from the user-data it generates, and
// that user-data carries the VM name as the hostname. Renaming the VM, or
// changing any other cloud-init setting on it, therefore changes the
// instance-id the next time QEMU starts. cloud-init then re-runs every
// per-instance module. It regenerates the host keys, resets the password from
// cipassword, and runs a full apt upgrade during boot. manual_cache_clean
// makes it trust its cached instance instead. A fresh clone still gets a
// normal first boot, because the seed's cloud-init clean leaves it no cache.
func TestSeedTrustsCloudInitInstanceCache(t *testing.T) {
	t.Parallel()

	found := seedFile("/etc/cloud/cloud.cfg.d/99-ocfp-trust-instance-cache.cfg")
	if found == nil {
		t.Fatal("the seed installs no trust-instance-cache drop-in, so a renamed bastion becomes a new cloud-init instance")
	}

	want := `# Managed by OCFP.
# A PVE bastion is the same machine after its VM config changes. PVE derives
# the NoCloud instance-id from the user-data it generates, which carries the
# VM name, so a rename would make the next cold start a new instance, with
# new host keys, a reset password, and an apt upgrade during boot. Trust the
# cached instance instead. A fresh clone has no cache, because the template
# seed runs cloud-init clean, so it still gets a normal first boot.
manual_cache_clean: true
`
	if found.content != want {
		t.Errorf("%s content:\n%s\nwant:\n%s", found.path, found.content, want)
	}

	if found.mode != "0644" {
		t.Errorf("%s mode = %q, want 0644", found.path, found.mode)
	}

	if !strings.Contains(seedFinalizeCommand, "sudo cloud-init clean") {
		t.Error("the seed no longer runs cloud-init clean, so every clone would trust the template's cached instance")
	}
}

// seedFile returns the file the seed installs at path, or nil.
func seedFile(path string) *seedUnitFile {
	for _, f := range seedUnitFiles() {
		if f.path == path {
			return &f
		}
	}

	return nil
}
