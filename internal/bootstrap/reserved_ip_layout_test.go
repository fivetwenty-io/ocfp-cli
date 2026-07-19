package bootstrap_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// newPVEBandTestManager builds a PVE-provider Manager over a fresh state
// store, carving "10.64.64.0/18" into 1 infra + 3 ocfp /22 children (infra
// 10.64.64.0/22, ocfp-0 10.64.68.0/22, ocfp-1 10.64.72.0/22, ocfp-2
// 10.64.76.0/22), matching TestCreateSubnets_PVE_CreatesRealPer22SDNSubnets.
// The returned *config.Config is the same pointer wired into the Manager, so
// callers can mutate cfg.Network (e.g. to set an available-band override)
// any time before invoking CreateNetwork/CreateSubnets.
func newPVEBandTestManager(t *testing.T) (*bootstrap.Manager, *state.Manager, *config.Config) {
	t.Helper()

	tmp := t.TempDir()

	sm, err := state.NewManager(filepath.Join(tmp, ".state"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err = sm.Load("prod"); err != nil {
		t.Fatal(err)
	}

	cfg := createTestConfig()
	cfg.Network.CIDR = "10.64.64.0/18"

	fakeNetwork := &fakeNet{}
	fakeProvider := &fakeProv{n: fakeNetwork, c: &fakeCompute{}}

	mgr := bootstrap.NewManager(cfg, fakeProvider, sm, &bootstrap.Options{
		BlocName: "prod",
		Provider: "pve",
		Region:   "pve",
	})

	return mgr, sm, cfg
}

func getOutputString(t *testing.T, sm *state.Manager, key string) string {
	t.Helper()

	got, err := sm.GetOutput(key)
	if err != nil {
		t.Fatalf("missing output %s: %v", key, err)
	}

	s, ok := got.(string)
	if !ok {
		t.Fatalf("output %s = %v (%T), want string", key, got, got)
	}

	return s
}

// TestPVEWorkloadBand_WidensOnWide22_InfraKeepsDefault verifies that
// pveSubnetStrategy widens the available band on a /22 workload subnet to
// roughly half the subnet (offsets 12..509, reservedC=510) while the infra
// subnet — which hosts bastion/director/shield/blacksmith and has no room to
// widen — keeps the constant 12..29 layout.
func TestPVEWorkloadBand_WidensOnWide22_InfraKeepsDefault(t *testing.T) {
	t.Parallel()

	mgr, sm, _ := newPVEBandTestManager(t)
	ctx := context.Background()

	if err := mgr.CreateNetwork(ctx); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	if err := mgr.CreateSubnets(ctx); err != nil {
		t.Fatalf("CreateSubnets: %v", err)
	}

	// infra subnet is 10.64.64.0/22: unchanged 12..29 band.
	if got := getOutputString(t, sm, "reserved_prod-infra_available_a"); got != "10.64.64.12" {
		t.Errorf("infra available_a = %q, want 10.64.64.12", got)
	}

	if got := getOutputString(t, sm, "reserved_prod-infra_available_b"); got != "10.64.64.29" {
		t.Errorf("infra available_b = %q, want 10.64.64.29", got)
	}

	// ocfp-0 workload subnet is 10.64.68.0/22: widened band. Offset 509 from
	// 10.64.68.0 crosses into the next octet (509 = 256 + 253): 10.64.69.253.
	// reservedC = availableB + 1 = 10.64.69.254.
	if got := getOutputString(t, sm, "reserved_prod-ocfp-0_available_a"); got != "10.64.68.12" {
		t.Errorf("ocfp-0 available_a = %q, want 10.64.68.12", got)
	}

	if got := getOutputString(t, sm, "reserved_prod-ocfp-0_available_b"); got != "10.64.69.253" {
		t.Errorf("ocfp-0 available_b = %q, want 10.64.69.253 (offset 509)", got)
	}

	if got := getOutputString(t, sm, "reserved_prod-ocfp-0_reserved_c"); got != "10.64.69.254" {
		t.Errorf("ocfp-0 reserved_c = %q, want 10.64.69.254 (offset 510)", got)
	}
}

// TestReservedBandOverride_AppliesToBothRoles verifies that a config-level
// network.availableBandStart/availableBandEnd override replaces the strategy
// layout's available band on BOTH the infra and ocfp roles, forcing
// reservedC to end+1.
func TestReservedBandOverride_AppliesToBothRoles(t *testing.T) {
	t.Parallel()

	mgr, sm, cfg := newPVEBandTestManager(t)
	cfg.Network.AvailableBandStart = 100
	cfg.Network.AvailableBandEnd = 200

	ctx := context.Background()

	if err := mgr.CreateNetwork(ctx); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	if err := mgr.CreateSubnets(ctx); err != nil {
		t.Fatalf("CreateSubnets: %v", err)
	}

	if got := getOutputString(t, sm, "reserved_prod-infra_available_a"); got != "10.64.64.100" {
		t.Errorf("infra available_a = %q, want 10.64.64.100 (override)", got)
	}

	if got := getOutputString(t, sm, "reserved_prod-infra_available_b"); got != "10.64.64.200" {
		t.Errorf("infra available_b = %q, want 10.64.64.200 (override)", got)
	}

	if got := getOutputString(t, sm, "reserved_prod-infra_reserved_c"); got != "10.64.64.201" {
		t.Errorf("infra reserved_c = %q, want 10.64.64.201 (override end+1)", got)
	}

	if got := getOutputString(t, sm, "reserved_prod-ocfp-0_available_a"); got != "10.64.68.100" {
		t.Errorf("ocfp-0 available_a = %q, want 10.64.68.100 (override)", got)
	}

	if got := getOutputString(t, sm, "reserved_prod-ocfp-0_available_b"); got != "10.64.68.200" {
		t.Errorf("ocfp-0 available_b = %q, want 10.64.68.200 (override)", got)
	}

	if got := getOutputString(t, sm, "reserved_prod-ocfp-0_reserved_c"); got != "10.64.68.201" {
		t.Errorf("ocfp-0 reserved_c = %q, want 10.64.68.201 (override end+1)", got)
	}
}

// TestReservedBandOverride_AppliesToStackitStrategyToo verifies the override
// is honored by the STACKIT triple strategy as well as PVE, since it is
// applied uniformly in resolveReservedIPLayout regardless of which strategy
// computed the base layout.
func TestReservedBandOverride_AppliesToStackitStrategyToo(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	sm, err := state.NewManager(filepath.Join(tmp, ".state"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err = sm.Load("prod"); err != nil {
		t.Fatal(err)
	}

	cfg := createTestConfig()
	cfg.Network.NetworkCIDR = "10.4.0.0/20"

	fakeNetwork := &fakeNet{}
	fakeProvider := &fakeProv{n: fakeNetwork, c: &fakeCompute{}}

	cfg.Network.AvailableBandStart = 100
	cfg.Network.AvailableBandEnd = 200

	mgr := bootstrap.NewManager(cfg, fakeProvider, sm, &bootstrap.Options{
		BlocName: "prod",
		Provider: "stackit",
		Region:   "eu01",
	})

	ctx := context.Background()

	if err := mgr.CreateNetwork(ctx); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	if err := mgr.CreateSubnets(ctx); err != nil {
		t.Fatalf("CreateSubnets: %v", err)
	}

	// prod-ocfp-0 = 10.4.4.0/22 (see TestArtifactsIPSlot_ResolvesToDotEleven).
	if got := getOutputString(t, sm, "reserved_prod-ocfp-0_available_a"); got != "10.4.4.100" {
		t.Errorf("ocfp-0 available_a = %q, want 10.4.4.100 (override)", got)
	}

	if got := getOutputString(t, sm, "reserved_prod-ocfp-0_available_b"); got != "10.4.4.200" {
		t.Errorf("ocfp-0 available_b = %q, want 10.4.4.200 (override)", got)
	}

	if got := getOutputString(t, sm, "reserved_prod-ocfp-0_reserved_c"); got != "10.4.4.201" {
		t.Errorf("ocfp-0 reserved_c = %q, want 10.4.4.201 (override end+1)", got)
	}
}

// TestReservedBandOverride_ValidationErrors covers the three rejected
// override shapes: a start that would collide with the fixed named-IP slots
// (0-11), an end that doesn't fall strictly after start, and an end beyond
// the target subnet's usable range. Each must surface its documented
// sentinel error via errors.Is so callers can distinguish failure reasons.
func TestReservedBandOverride_ValidationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		start   int
		end     int
		wantErr error
	}{
		{name: "start below 12 collides with named slots", start: 5, end: 50, wantErr: bootstrap.ErrBandOverrideStartTooLow},
		{name: "end equal to start", start: 100, end: 100, wantErr: bootstrap.ErrBandOverrideEndNotAfterStart},
		{name: "end before start", start: 100, end: 50, wantErr: bootstrap.ErrBandOverrideEndNotAfterStart},
		{name: "end beyond /22 usable range", start: 12, end: 2000, wantErr: bootstrap.ErrBandOverrideEndBeyondSubnet},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mgr, _, cfg := newPVEBandTestManager(t)
			cfg.Network.AvailableBandStart = tt.start
			cfg.Network.AvailableBandEnd = tt.end

			ctx := context.Background()

			if err := mgr.CreateNetwork(ctx); err != nil {
				t.Fatalf("CreateNetwork: %v", err)
			}

			err := mgr.CreateSubnets(ctx)
			if err == nil {
				t.Fatalf("CreateSubnets: want error %v, got nil", tt.wantErr)
			}

			if !errors.Is(err, tt.wantErr) {
				t.Errorf("CreateSubnets error = %v, want wrapping %v", err, tt.wantErr)
			}
		})
	}
}

// TestReservedBandOverride_PartialConfigRejected verifies that setting only
// one of AvailableBandStart/AvailableBandEnd (rather than both, or neither)
// is treated as a misconfiguration rather than silently defaulting one side.
func TestReservedBandOverride_PartialConfigRejected(t *testing.T) {
	t.Parallel()

	mgr, _, cfg := newPVEBandTestManager(t)
	cfg.Network.AvailableBandStart = 100
	cfg.Network.AvailableBandEnd = 0

	ctx := context.Background()

	if err := mgr.CreateNetwork(ctx); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	err := mgr.CreateSubnets(ctx)
	if err == nil {
		t.Fatal("CreateSubnets: want error for partial band override, got nil")
	}

	if !errors.Is(err, bootstrap.ErrBandOverridePartial) {
		t.Errorf("CreateSubnets error = %v, want wrapping ErrBandOverridePartial", err)
	}
}
