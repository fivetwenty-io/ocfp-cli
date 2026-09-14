package bastion

import (
	"strings"
	"testing"
)

// TestProvisionedMarkerLivesOnTheOSDisk is the catch that makes persisting the
// bastion's whole home directory safe.
//
// isAlreadyProvisioned short-circuits the entire thirty-phase provisioning run
// when it finds this marker. With the home directory bind-mounted off the
// persistent data disk, a marker under ~/.ocfp would survive onto a freshly
// rebuilt machine that has no brew, no binaries, and no genesis, and
// `ocfp bastion init` would look at it and decide there was nothing to do.
//
// The rule this encodes generalises past the marker: the persistent disk
// carries data, never claims about the state of the OS. /var/lib/ocfp is on
// the OS disk, next to the firstboot sentinel that already lives there.
func TestProvisionedMarkerLivesOnTheOSDisk(t *testing.T) {
	t.Parallel()

	if ProvisionedMarkerPath != "/var/lib/ocfp/provisioned" {
		t.Errorf("ProvisionedMarkerPath = %q, want /var/lib/ocfp/provisioned", ProvisionedMarkerPath)
	}

	if strings.Contains(ProvisionedMarkerPath, "HOME") || strings.HasPrefix(ProvisionedMarkerPath, "~") {
		t.Errorf("ProvisionedMarkerPath %q is under the home directory, which is persisted across rebuilds",
			ProvisionedMarkerPath)
	}
}

// TestLegacyProvisionedMarkerStillRead asserts bastions provisioned before the
// move are still recognised, so the change does not re-run a full provision on
// every existing box the first time it is touched.
func TestLegacyProvisionedMarkerStillRead(t *testing.T) {
	t.Parallel()

	cmd := provisionedMarkerCheckCommand()

	if !strings.Contains(cmd, ProvisionedMarkerPath) {
		t.Errorf("the check does not look at the OS-disk marker %q: %s", ProvisionedMarkerPath, cmd)
	}

	if !strings.Contains(cmd, LegacyProvisionedMarkerPath) {
		t.Errorf("the check does not fall back to the legacy marker %q: %s", LegacyProvisionedMarkerPath, cmd)
	}
}

// TestProvisionedMarkerWriteCommandTargetsOSDisk asserts we write where we
// read, and that the directory is created first since /var/lib/ocfp may not
// exist on a box that never ran the firstboot unit.
func TestProvisionedMarkerWriteCommandTargetsOSDisk(t *testing.T) {
	t.Parallel()

	cmd := provisionedMarkerWriteCommand()

	if !strings.Contains(cmd, ProvisionedMarkerPath) {
		t.Errorf("the write does not target %q: %s", ProvisionedMarkerPath, cmd)
	}

	if !strings.Contains(cmd, "mkdir -p") {
		t.Errorf("the write does not create the marker directory first: %s", cmd)
	}

	if strings.Contains(cmd, "~/.ocfp/provisioned") {
		t.Errorf("the write still targets the persisted home directory: %s", cmd)
	}
}
