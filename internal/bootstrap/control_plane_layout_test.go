package bootstrap_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/netlayout"
)

// TestControlPlaneLayerA_DefaultAndOverride proves Layer A records the
// control-plane address on every workload subnet at the strategy's offset,
// that network.statics.mgmt.controlPlane moves it to the same address
// Layer B writes into vault, and that the fixed infra subnet never gains it.
func TestControlPlaneLayerA_DefaultAndOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		override []int
		want     map[string]string
	}{
		{
			name: "default",
			want: map[string]string{
				"reserved_prod-ocfp-0_control_plane_ip": "10.64.68.23",
				"reserved_prod-ocfp-1_control_plane_ip": "10.64.72.23",
				"reserved_prod-ocfp-2_control_plane_ip": "10.64.76.23",
			},
		},
		{
			name:     "override",
			override: []int{30},
			want: map[string]string{
				"reserved_prod-ocfp-0_control_plane_ip": "10.64.68.30",
				"reserved_prod-ocfp-1_control_plane_ip": "10.64.72.30",
				"reserved_prod-ocfp-2_control_plane_ip": "10.64.76.30",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mgr, sm, cfg := newPVEBandTestManager(t)
			cfg.Network.Statics.Mgmt.ControlPlane = tc.override

			ctx := context.Background()

			if err := mgr.CreateNetwork(ctx); err != nil {
				t.Fatalf("CreateNetwork: %v", err)
			}

			if err := mgr.CreateSubnets(ctx); err != nil {
				t.Fatalf("CreateSubnets: %v", err)
			}

			for key, want := range tc.want {
				if got := getOutputString(t, sm, key); got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}

			// Every other static stays where the strategy puts it.
			if got := getOutputString(t, sm, "reserved_prod-ocfp-0_garage_ip_smoke"); got != "10.64.68.22" {
				t.Errorf("garage_ip_smoke = %q, want 10.64.68.22", got)
			}

			if _, err := sm.GetOutput("reserved_prod-infra_control_plane_ip"); err == nil {
				t.Error("infra subnet has a control_plane_ip output, want none (infra layout is fixed)")
			}
		})
	}
}

// TestControlPlaneLayerA_OverrideRejected proves an invalid override stops
// CreateSubnets with the netlayout sentinel and the config key named,
// before any workload subnet is recorded with an address the vault layer
// would refuse.
func TestControlPlaneLayerA_OverrideRejected(t *testing.T) {
	t.Parallel()

	mgr, _, cfg := newPVEBandTestManager(t)
	cfg.Network.Statics.Mgmt.ControlPlane = []int{40}

	ctx := context.Background()

	if err := mgr.CreateNetwork(ctx); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	err := mgr.CreateSubnets(ctx)
	if !errors.Is(err, netlayout.ErrStaticOverrideInBand) {
		t.Fatalf("CreateSubnets error = %v, want wrapping ErrStaticOverrideInBand", err)
	}

	if !strings.Contains(err.Error(), "network.statics.mgmt.controlPlane") {
		t.Fatalf("CreateSubnets error %q does not name the config key", err)
	}
}
