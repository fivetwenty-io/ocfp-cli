package commands

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// TestPreservedVolumesForInstance covers which volumes a teardown must detach
// before it destroys a guest.
//
// This is the difference between a persistent disk and a disk that merely
// exists. PVE's DeleteInstance purges with purge=true, which destroys every
// disk the VM config still references, so a data disk that is not detached
// first dies with the machine it was meant to outlive.
func TestPreservedVolumesForInstance(t *testing.T) {
	t.Parallel()

	resources := []*state.Resource{
		{
			ID:   "local-lvm-data:vm-100-data",
			Type: state.ResourceTypeVolume,
			Name: "prod-bastion-data",
			Properties: map[string]interface{}{
				"vm_id":    "100",
				"preserve": true,
			},
		},
		{
			ID:   "local-lvm-data:vm-101-data",
			Type: state.ResourceTypeVolume,
			Name: "prod-artifacts-data",
			Properties: map[string]interface{}{
				"vm_id":    "101",
				"preserve": true,
			},
		},
		{
			ID:   "local-lvm-data:vm-100-scratch",
			Type: state.ResourceTypeVolume,
			Name: "prod-scratch",
			Properties: map[string]interface{}{
				"vm_id":    "100",
				"preserve": false,
			},
		},
		{
			ID:   "100",
			Type: state.ResourceTypeInstance,
			Name: "prod-bastion",
		},
	}

	tests := []struct {
		name       string
		instanceID string
		want       []string
	}{
		{"bastion keeps its data disk", "100", []string{"local-lvm-data:vm-100-data"}},
		{"artifacts keeps its data disk", "101", []string{"local-lvm-data:vm-101-data"}},
		{"unknown instance keeps nothing", "999", nil},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := preservedVolumesForInstance(resources, tc.instanceID)

			if len(got) != len(tc.want) {
				t.Fatalf("preserved = %v, want %v", got, tc.want)
			}

			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("preserved[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestPreservedVolumesForInstance_RequiresTheFlag asserts we only spare disks
// that asked to be spared. A teardown that silently left every volume behind
// would leak storage on every bloc it ran against.
func TestPreservedVolumesForInstance_RequiresTheFlag(t *testing.T) {
	t.Parallel()

	resources := []*state.Resource{
		{
			ID:         "local-lvm-data:vm-100-other",
			Type:       state.ResourceTypeVolume,
			Name:       "prod-other",
			Properties: map[string]interface{}{"vm_id": "100"},
		},
	}

	if got := preservedVolumesForInstance(resources, "100"); len(got) != 0 {
		t.Errorf("preserved %v for a volume with no preserve flag", got)
	}
}

// TestPreserveFlagAcceptsBothEncodings covers the state file round-tripping
// through JSON, where a bool can come back as a bool or as a string.
func TestPreserveFlagAcceptsBothEncodings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value interface{}
		want  bool
	}{
		{"bool true", true, true},
		{"bool false", false, false},
		{"string true", "true", true},
		{"string false", "false", false},
		{"absent", nil, false},
		{"nonsense", 7, false},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			props := map[string]interface{}{}
			if tc.value != nil {
				props["preserve"] = tc.value
			}

			if got := shouldPreserveVolume(props); got != tc.want {
				t.Errorf("shouldPreserveVolume(%v) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}
