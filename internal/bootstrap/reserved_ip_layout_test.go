package bootstrap_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
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

// TestPVEWorkloadBand_OCFPUnifiesToStrategyBand_InfraKeepsDefault verifies
// that the ocfp role's available band now comes unconditionally from the
// bloc's netlayout strategy ("wide" by default: mgmt available band
// 32-63, reservedC 64) rather than a CIDR-size-derived widening computed by
// pveSubnetStrategy — Layer A (this bootstrap resolution) and Layer B
// (internal/vault's reserved-ips population) read the identical table, so
// they can never disagree about where the ocfp band sits. The infra
// subnet — which hosts bastion/director/shield/blacksmith and has no room
// to widen — keeps the constant 12..29 layout, unaffected by strategy.
func TestPVEWorkloadBand_OCFPUnifiesToStrategyBand_InfraKeepsDefault(t *testing.T) {
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

	// ocfp-0 workload subnet is 10.64.68.0/22: the wide strategy's own
	// mgmt-tier band (32-63), unified regardless of subnet size.
	if got := getOutputString(t, sm, "reserved_prod-ocfp-0_available_a"); got != "10.64.68.32" {
		t.Errorf("ocfp-0 available_a = %q, want 10.64.68.32", got)
	}

	if got := getOutputString(t, sm, "reserved_prod-ocfp-0_available_b"); got != "10.64.68.63" {
		t.Errorf("ocfp-0 available_b = %q, want 10.64.68.63", got)
	}

	if got := getOutputString(t, sm, "reserved_prod-ocfp-0_reserved_c"); got != "10.64.68.64" {
		t.Errorf("ocfp-0 reserved_c = %q, want 10.64.68.64", got)
	}

	// reserved_b brackets the band from below: the offset immediately under
	// available_a (32-1=31), not the infra role's fixed 10. Layer A used to
	// inherit the infra value here even after widening the ocfp band, leaving
	// 11..31 described as neither reserved nor available.
	if got := getOutputString(t, sm, "reserved_prod-ocfp-0_reserved_b"); got != "10.64.68.31" {
		t.Errorf("ocfp-0 reserved_b = %q, want 10.64.68.31 (available_a - 1)", got)
	}

	// The infra subnet keeps its own historical bracket (offset 10).
	if got := getOutputString(t, sm, "reserved_prod-infra_reserved_b"); got != "10.64.64.10" {
		t.Errorf("infra reserved_b = %q, want 10.64.64.10", got)
	}
}

// TestPVEWorkloadStatics_ColocatedOnEveryIndex verifies the Layer A
// consequence of resolving statics from the bloc's strategy instead of
// bootstrap's own idx branches: "wide" declares colocated placement, so
// every workload subnet gets the full mgmt static set at the strategy's own
// offsets, relative to its own base. Previously bastion/bosh landed only on
// ocfp-0, doomsday/shout only on ocfp-1 (at 9/10, colliding with
// shield/blacksmith), and ocfp_ui only on ocfp-2 (at 9) — none of which
// matched the offsets Layer B writes into vault for the same roles.
func TestPVEWorkloadStatics_ColocatedOnEveryIndex(t *testing.T) {
	t.Parallel()

	mgr, sm, _ := newPVEBandTestManager(t)
	ctx := context.Background()

	if err := mgr.CreateNetwork(ctx); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	if err := mgr.CreateSubnets(ctx); err != nil {
		t.Fatalf("CreateSubnets: %v", err)
	}

	// wide's mgmt statics, at their own offsets, on every workload subnet.
	for _, tc := range []struct {
		key  string
		want string
	}{
		{key: "reserved_prod-ocfp-0_bastion_ip", want: "10.64.68.3"},
		{key: "reserved_prod-ocfp-0_bosh_ip", want: "10.64.68.4"},
		{key: "reserved_prod-ocfp-0_doomsday_ip", want: "10.64.68.18"},
		{key: "reserved_prod-ocfp-0_ocfp_ui_ip", want: "10.64.68.17"},
		{key: "reserved_prod-ocfp-1_bastion_ip", want: "10.64.72.3"},
		{key: "reserved_prod-ocfp-1_doomsday_ip", want: "10.64.72.18"},
		{key: "reserved_prod-ocfp-1_shout_ip", want: "10.64.72.19"},
		{key: "reserved_prod-ocfp-2_bosh_ip", want: "10.64.76.4"},
		{key: "reserved_prod-ocfp-2_ocfp_ui_ip", want: "10.64.76.17"},
		// ip_key statics keep their overridden output stem.
		{key: "reserved_prod-ocfp-0_rustfs_ip_smoke", want: "10.64.68.21"},
		{key: "reserved_prod-ocfp-0_garage_ip_smoke", want: "10.64.68.22"},
	} {
		if got := getOutputString(t, sm, tc.key); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
		}
	}

	// The infra subnet is NOT a workload subnet: it keeps its own four fixed
	// statics and gains none of the workload set.
	if _, err := sm.GetOutput("reserved_prod-infra_doomsday_ip"); err == nil {
		t.Error("infra subnet has a doomsday_ip output, want none (infra layout is fixed at 4 statics)")
	}
}

// TestReservedIPConsumerKeys_Unchanged pins the two reserved_* keys with
// live non-Genesis consumers — internal/commands/bastion_lookup.go's
// last-resort bastion fallback and internal/commands/init.go's legacy BOSH
// manifest path — to the exact addresses they resolved to before the Layer A
// cutover. Everything else about ocfp-0's static set may have grown; these
// two must not move.
func TestReservedIPConsumerKeys_Unchanged(t *testing.T) {
	t.Parallel()

	mgr, sm, _ := newPVEBandTestManager(t)
	ctx := context.Background()

	if err := mgr.CreateNetwork(ctx); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	if err := mgr.CreateSubnets(ctx); err != nil {
		t.Fatalf("CreateSubnets: %v", err)
	}

	if got := getOutputString(t, sm, "reserved_prod-ocfp-0_bastion_ip"); got != "10.64.68.3" {
		t.Errorf("ocfp-0 bastion_ip = %q, want 10.64.68.3 (bastion_lookup.go consumer)", got)
	}

	if got := getOutputString(t, sm, "reserved_prod-ocfp-0_bosh_ip"); got != "10.64.68.4" {
		t.Errorf("ocfp-0 bosh_ip = %q, want 10.64.68.4 (init.go createBOSHManifest consumer)", got)
	}
}

// TestReservedBandOverride_AppliesToBothRoles verifies that a config-level
// network.bands.infra override replaces the strategy layout's available
// band on BOTH the infra and ocfp roles, forcing reservedC to end+1.
func TestReservedBandOverride_AppliesToBothRoles(t *testing.T) {
	t.Parallel()

	mgr, sm, cfg := newPVEBandTestManager(t)
	cfg.Network.Bands.Infra = config.Band{Start: 100, End: 200}

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

// TestReservedBandOverride_Unset verifies that leaving network.bands.infra
// at its zero value applies no override at all: resolveReservedIPLayout
// returns the strategy's layout unchanged, and layout.ValidateBand is never
// invoked (a malformed subnetCIDR would otherwise surface as a
// ValidateBand error, so this also proves the short-circuit happens before
// any CIDR parsing tied to the override path).
func TestReservedBandOverride_Unset(t *testing.T) {
	t.Parallel()

	mgr, sm, cfg := newPVEBandTestManager(t)

	if cfg.Network.Bands.Infra != (config.Band{}) {
		t.Fatalf("test precondition: Bands.Infra = %+v, want zero value", cfg.Network.Bands.Infra)
	}

	ctx := context.Background()

	if err := mgr.CreateNetwork(ctx); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	if err := mgr.CreateSubnets(ctx); err != nil {
		t.Fatalf("CreateSubnets: %v", err)
	}

	// Strategy default layout applied, unchanged: infra keeps 12..29, ocfp-0
	// keeps wide's own mgmt band (32-63, reservedC 64).
	if got := getOutputString(t, sm, "reserved_prod-infra_available_a"); got != "10.64.64.12" {
		t.Errorf("infra available_a = %q, want 10.64.64.12 (no override)", got)
	}

	if got := getOutputString(t, sm, "reserved_prod-ocfp-0_available_a"); got != "10.64.68.32" {
		t.Errorf("ocfp-0 available_a = %q, want 10.64.68.32 (no override)", got)
	}

	if got := getOutputString(t, sm, "reserved_prod-ocfp-0_reserved_c"); got != "10.64.68.64" {
		t.Errorf("ocfp-0 reserved_c = %q, want 10.64.68.64 (no override)", got)
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

	cfg.Network.Bands.Infra = config.Band{Start: 100, End: 200}

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
// netlayout sentinel error via errors.Is so callers can distinguish failure
// reasons. Validation itself now lives in netlayout.Layout.ValidateBand
// (see internal/netlayout/band.go); bootstrap only wires the config values
// through.
func TestReservedBandOverride_ValidationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		start   int
		end     int
		wantErr error
	}{
		{name: "start below 12 collides with named slots", start: 5, end: 50, wantErr: netlayout.ErrBandOverrideStartTooLow},
		{name: "end equal to start", start: 100, end: 100, wantErr: netlayout.ErrBandOverrideEndNotAfterStart},
		{name: "end before start", start: 100, end: 50, wantErr: netlayout.ErrBandOverrideEndNotAfterStart},
		{name: "end beyond /22 usable range", start: 12, end: 2000, wantErr: netlayout.ErrBandOverrideEndBeyondSubnet},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mgr, _, cfg := newPVEBandTestManager(t)
			cfg.Network.Bands.Infra = config.Band{Start: tt.start, End: tt.end}

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
// one of Bands.Infra.Start/End (rather than both, or neither) is treated as
// a misconfiguration rather than silently defaulting one side.
func TestReservedBandOverride_PartialConfigRejected(t *testing.T) {
	t.Parallel()

	mgr, _, cfg := newPVEBandTestManager(t)
	cfg.Network.Bands.Infra = config.Band{Start: 100, End: 0}

	ctx := context.Background()

	if err := mgr.CreateNetwork(ctx); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	err := mgr.CreateSubnets(ctx)
	if err == nil {
		t.Fatal("CreateSubnets: want error for partial band override, got nil")
	}

	if !errors.Is(err, netlayout.ErrBandOverridePartial) {
		t.Errorf("CreateSubnets error = %v, want wrapping ErrBandOverridePartial", err)
	}
}
