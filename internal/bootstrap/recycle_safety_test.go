package bootstrap

import (
	"strings"
	"testing"
)

// TestSafetyCopyIsAnArchiveNotASnapshot documents a constraint discovered on a
// live PVE 9 cluster, and the design change it forces.
//
// A VM snapshot pins every disk in the VM's config. Proxmox then refuses to
// move or detach any of them: "unable to delete '<volid>' - volume is still in
// use (snapshot?)". So a recycle that snapshots the VM before handing the data
// disk over deadlocks itself — its own safety step blocks the very operation
// it was protecting.
//
// The safety copy for a recycle therefore has to be a vzdump archive rather
// than a snapshot. An archive pins nothing, and with the data disk marked
// backup=0 it captures exactly the half that is about to be thrown away: the
// OS disk. The data disk needs no copy, because it is not destroyed — it is
// carried across.
func TestSafetyCopyIsAnArchiveNotASnapshot(t *testing.T) {
	t.Parallel()

	reason := recycleSafetyCopyRationale()

	for _, want := range []string{"snapshot", "pins", "archive"} {
		if !strings.Contains(strings.ToLower(reason), want) {
			t.Errorf("the rationale does not mention %q; a future reader will re-introduce the deadlock:\n%s",
				want, reason)
		}
	}
}

// TestRecycleSafetyCopyRequiresStorage asserts we refuse to proceed with no
// safety copy at all rather than quietly continuing.
func TestRecycleSafetyCopyRequiresStorage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		storage     string
		operatorAck bool
		wantErr     bool
	}{
		{"a storage is given", "nfs-backup", false, false},
		{"no storage and no acknowledgement", "", false, true},
		{"no storage but the operator says they took one", "", true, false},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := checkSafetyCopyPlan(tc.storage, tc.operatorAck)

			if tc.wantErr && err == nil {
				t.Error("expected a refusal when nothing would protect the OS disk")
			}

			if !tc.wantErr && err != nil {
				t.Errorf("unexpected refusal: %v", err)
			}
		})
	}
}
