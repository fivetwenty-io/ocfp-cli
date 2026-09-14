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

// TestLegacyMarkerIgnoredWhenHomeIsPersisted closes the hole the legacy
// fallback reopened.
//
// Moving the marker to the OS disk was meant to stop a claim about the state
// of the OS riding across a rebuild on the data disk. Reading the old
// home-directory marker as a fallback puts that back exactly as it was, and it
// cost us the first real cycle: the Resolute bastion came up with no brew, no
// binaries, and no genesis, and `ocfp bastion init` read a marker dated three
// weeks earlier off the persisted home and reported the bastion already fully
// provisioned.
//
// A bind-mounted home is the tell. When the home directory is a mount point,
// whatever marker sits in it came from the data disk rather than from this
// installation, and it says nothing about the operating system now running.
func TestLegacyMarkerIgnoredWhenHomeIsPersisted(t *testing.T) {
	t.Parallel()

	cmd := provisionedMarkerCheckCommand()

	if !strings.Contains(cmd, "findmnt") {
		t.Errorf("the marker check never asks whether the home directory is persisted:\n%s", cmd)
	}

	// The OS-disk marker must still be read without that question being asked
	// of it, since it cannot have come from the data disk.
	osFirst := strings.Index(cmd, ProvisionedMarkerPath)
	guard := strings.Index(cmd, "findmnt")

	if osFirst < 0 || guard < 0 {
		t.Fatalf("expected both the OS-disk marker and the guard in:\n%s", cmd)
	}

	if osFirst > guard {
		t.Errorf("the persisted-home guard precedes the OS-disk marker; the OS-disk marker is authoritative:\n%s", cmd)
	}
}
