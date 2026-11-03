package guestboot

import (
	"strings"
	"testing"
)

// A thin pool gets deleted blocks back only when the guest sends TRIM, so the
// data disk mounts with discard both on the first mount and from fstab. The
// bind mounts on top of it carry no discard, because it means nothing there.
func TestDatasetScriptMountsDataDiskWithDiscard(t *testing.T) {
	for _, want := range []string{
		`mount -o discard "${REAL}" "${MOUNT}"`,
		`printf 'UUID=%s %s %s defaults,nofail,discard 0 2\n'`,
	} {
		if !strings.Contains(DatasetScript, want) {
			t.Errorf("dataset script is missing %q", want)
		}
	}

	if got := strings.Count(DatasetScript, "none bind,nofail 0 0"); got != 2 {
		t.Errorf("dataset script has %d bind fstab lines without discard, want 2", got)
	}

	if strings.Contains(DatasetScript, "bind,nofail,discard") {
		t.Error("dataset script adds discard to a bind mount")
	}
}

// cloud-init's init stage runs the ssh module, so the dataset unit has to
// finish first. Ubuntu Resolute renamed that stage's unit from
// cloud-init.service to cloud-init-network.service, and ordering against a
// unit that does not exist orders against nothing. Both names stay, because
// Ubuntu Noble still ships the old one.
func TestDatasetServiceOrdersBeforeBothCloudInitNetworkUnits(t *testing.T) {
	before := map[string]bool{}

	for _, line := range strings.Split(DatasetService, "\n") {
		if names, ok := strings.CutPrefix(line, "Before="); ok {
			for _, name := range strings.Fields(names) {
				before[name] = true
			}
		}
	}

	for _, want := range []string{"cloud-init.service", "cloud-init-network.service", "ssh.service", "sshd.service", "tailscaled.service", "ocfp-firstboot.service", "sysinit.target"} {
		if !before[want] {
			t.Errorf("ocfp-dataset.service is not ordered before %s:\n%s", want, DatasetService)
		}
	}
}
