package pve

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// TestShouldStartAfterCreate covers the create-stopped option the recycle
// operation needs.
//
// A replacement VM is built while the machine it replaces is only stopped, so
// both exist at once. Starting the replacement immediately would put a second
// guest on the same static address, and it would boot against a data disk it
// does not own yet. It has to be built stopped and started only after the
// handover and the rename.
func TestShouldStartAfterCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		req  *cpi.InstanceRequest
		want bool
	}{
		{"default starts, preserving existing behaviour", &cpi.InstanceRequest{}, true},
		{"explicitly stopped", &cpi.InstanceRequest{CreateStopped: true}, false},
		{"nil request starts", nil, true},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := shouldStartAfterCreate(tc.req); got != tc.want {
				t.Errorf("shouldStartAfterCreate = %v, want %v", got, tc.want)
			}
		})
	}
}
