package bootstrap

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// TestIsPreservedDataVolume covers the identity problem a handover creates.
//
// The data disk starts life named after the bloc, so a "-data" suffix finds
// it. Proxmox's move_disk then renames it after its new owner, and the disk
// the recycle exists to preserve becomes "vm-102-disk-1": no suffix, and
// nothing about the name says what it is any more.
//
// That broke both halves of a resumed run. Observe could no longer see who
// held the disk and reported it orphaned, and the handover fell back to the
// recorded id, which named a volume that no longer existed. The recorded id is
// therefore consulted first, and the suffix stays only as the fallback for a
// disk this bloc has not recorded yet.
func TestIsPreservedDataVolume(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		volume   *cpi.Volume
		recorded string
		want     bool
	}{
		{
			name:     "freshly created, named after the bloc",
			volume:   &cpi.Volume{ID: "local-lvm-data:vm-100-data", Name: "vm-100-data"},
			recorded: "",
			want:     true,
		},
		{
			name:     "renamed by the handover, matched by the recorded id",
			volume:   &cpi.Volume{ID: "local-lvm-data:vm-102-disk-1", Name: "vm-102-disk-1"},
			recorded: "local-lvm-data:vm-102-disk-1",
			want:     true,
		},
		{
			name:     "renamed by the handover, with a stale record",
			volume:   &cpi.Volume{ID: "local-lvm-data:vm-102-disk-1", Name: "vm-102-disk-1"},
			recorded: "local-lvm-data:vm-100-data",
			want:     false,
		},
		{
			name:     "the OS disk is never the data disk",
			volume:   &cpi.Volume{ID: "local-lvm-data:vm-102-disk-0", Name: "vm-102-disk-0"},
			recorded: "local-lvm-data:vm-102-disk-1",
			want:     false,
		},
		{
			name:     "nil volume",
			volume:   nil,
			recorded: "local-lvm-data:vm-102-disk-1",
			want:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := isPreservedDataVolume(tc.volume, tc.recorded)
			if got != tc.want {
				t.Errorf("isPreservedDataVolume(%+v, %q) = %v, want %v", tc.volume, tc.recorded, got, tc.want)
			}
		})
	}
}

// TestNeedsStart covers the last phase that could not be re-entered.
//
// Adopting the replacement renames it and starts it. The journal records a
// phase before it runs, so a resume lands here after a start that already
// succeeded, and asking a running VM to start is an error on Proxmox rather
// than a no-op: the run died on "VM 102 already running" with the recycle
// otherwise complete.
func TestNeedsStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		state cpi.ResourceState
		want  bool
	}{
		{cpi.ResourceStateStopped, true},
		{cpi.ResourceStateActive, false},
		{cpi.ResourceStateInUse, false},
		{cpi.ResourceStateCreating, true},
		{cpi.ResourceStateUnknown, true},
	}

	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			t.Parallel()

			if got := needsStart(tc.state); got != tc.want {
				t.Errorf("needsStart(%q) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}
