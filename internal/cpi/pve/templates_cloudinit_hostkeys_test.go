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

	var found *seedUnitFile

	for i, f := range seedUnitFiles() {
		if strings.HasPrefix(f.path, "/etc/cloud/cloud.cfg.d/") {
			found = &seedUnitFiles()[i]
		}
	}

	if found == nil {
		t.Fatal("the seed installs no cloud-init drop-in, so cloud-init will delete the host keys the dataset unit restored")
	}

	if !strings.Contains(found.content, "ssh_deletekeys: false") {
		t.Errorf("%s does not turn off ssh_deletekeys:\n%s", found.path, found.content)
	}
}
