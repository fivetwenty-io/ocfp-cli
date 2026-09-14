package provision

import (
	"strings"
	"testing"
)

// TestGeneratedScriptsMarkOnTheOSDisk asserts the shell we push to the bastion
// writes its completion marker on the OS disk.
//
// The bastion's home directory is bind-mounted off a persistent data disk so
// operator state survives an OS replacement. A marker written there survives
// too, onto a freshly rebuilt machine with none of the tooling installed, and
// the next run's early-exit check would believe it.
func TestGeneratedScriptsMarkOnTheOSDisk(t *testing.T) {
	t.Parallel()

	if ProvisionedMarker != "/var/lib/ocfp/provisioned" {
		t.Errorf("ProvisionedMarker = %q, want /var/lib/ocfp/provisioned", ProvisionedMarker)
	}

	if strings.Contains(ProvisionedMarker, "HOME") {
		t.Errorf("ProvisionedMarker %q is under the persisted home directory", ProvisionedMarker)
	}
}

// TestCompletionMarkersTargetTheOSDisk covers the verification manager's
// generated lines.
func TestCompletionMarkersTargetTheOSDisk(t *testing.T) {
	t.Parallel()

	vm := &VerificationManager{}
	joined := strings.Join(vm.generateCompletionMarkers(), "\n")

	if !strings.Contains(joined, ProvisionedMarker) {
		t.Errorf("completion markers do not write %q:\n%s", ProvisionedMarker, joined)
	}

	if strings.Contains(joined, "$HOME/.ocfp/provisioned") {
		t.Errorf("completion markers still write the provisioned marker into the persisted home:\n%s", joined)
	}
}
