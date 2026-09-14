package bootstrap

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// TestArtifactsDataVolumeRecord asserts the artifacts data disk is recorded as
// a preserved block volume in its own right.
//
// Without this record the teardown guard cannot see it. The artifacts VM
// recorded its disk only as a data_volume_id property on the VM's own
// resource, so a teardown would destroy the disk with the guest — and that
// disk holds every compiled release and cached stemcell for the bloc, which is
// the expensive thing to lose, not the OS.
func TestArtifactsDataVolumeRecord(t *testing.T) {
	t.Parallel()

	res := artifactsDataVolumeResource(
		"prod", "pve", "prod-artifacts", "101",
		"local-lvm-data:vm-101-data", 500, "local-lvm-data", "ext4", "/data",
	)

	if res.Type != state.ResourceTypeVolume {
		t.Errorf("Type = %q, want %q", res.Type, state.ResourceTypeVolume)
	}

	if res.Name != "prod-artifacts-data" {
		t.Errorf("Name = %q, want prod-artifacts-data", res.Name)
	}

	if res.ID != "local-lvm-data:vm-101-data" {
		t.Errorf("ID = %q, want the full volid", res.ID)
	}

	if got := res.Properties["vm_id"]; got != "101" {
		t.Errorf("vm_id = %v, want 101; the teardown guard matches the owning VMID", got)
	}

	preserve, ok := res.Properties["preserve"].(bool)
	if !ok || !preserve {
		t.Errorf("preserve = %v; without it teardown destroys the compiled releases and cached stemcells",
			res.Properties["preserve"])
	}

	if got := res.Properties["mountpoint"]; got != "/data" {
		t.Errorf("mountpoint = %v, want /data", got)
	}

	if got := res.Properties["role"]; got != "artifacts-data" {
		t.Errorf("role = %v, want artifacts-data", got)
	}
}
