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

// TestReservedBandOverride_RecomputesReservedFloor verifies that an
// available-band override moves reserved_b (the floor of the reserved
// complement below the band) to start-1, not just available_a/b and
// reserved_c (end+1). Before this fix, applyAvailableBandOverride left
// ReservedB at whatever the strategy's default layout computed (10 for the
// infra role, 31 for wide's ocfp-0 mgmt band) — well below this override's
// start of 100 — so offsets between the stale floor and the new band start
// were described as neither reserved nor available. The override (100,200)
// is chosen well above both roles' own default bands so a stale ReservedB
// is visibly wrong rather than accidentally still correct.
func TestReservedBandOverride_RecomputesReservedFloor(t *testing.T) {
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

	if got := getOutputString(t, sm, "reserved_prod-infra_reserved_b"); got != "10.64.64.99" {
		t.Errorf("infra reserved_b = %q, want 10.64.64.99 (override start-1)", got)
	}

	if got := getOutputString(t, sm, "reserved_prod-ocfp-0_reserved_b"); got != "10.64.68.99" {
		t.Errorf("ocfp-0 reserved_b = %q, want 10.64.68.99 (override start-1)", got)
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

// newStackitTestManager builds a stackit-provider Manager over a fresh
// state store with subnetStrategy set to select the stackit-single strategy
// (selectVirtualSubnetStrategy: Network.SubnetStrategy contains "single").
// cfg.Network.CIDR is the whole parent CIDR: createStackitSingleSubnet
// records it, unsplit, as the bloc's one workload subnet (see
// internal/bootstrap/network.go), unlike the triple strategy which carves
// it into three.
func newStackitTestManager(t *testing.T) (*bootstrap.Manager, *state.Manager, *config.Config) {
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
	cfg.Network.NetworkCIDR = "10.4.0.0/20"
	cfg.Network.SubnetStrategy = "single"

	fakeNetwork := &fakeNet{}
	fakeProvider := &fakeProv{n: fakeNetwork, c: &fakeCompute{}}

	mgr := bootstrap.NewManager(cfg, fakeProvider, sm, &bootstrap.Options{
		BlocName: "prod",
		Provider: "stackit",
		Region:   "eu01",
	})

	return mgr, sm, cfg
}

// TestStackitSingleSubnet_WideDefault_LocksNegativeIndexKeySet pins the
// Layer A key set internal/bootstrap's createStackitSingleSubnet writes for
// a stackit-single bloc's one workload subnet, deliberately rather than by
// accident (see Task 6 review, Important 3: this path passes idx -1 to
// netlayout.Layout.LayerASlots, and under the colocated wide built-in
// placedOn(nil, -1) is true for every static — none is pinned to a subnet
// index it doesn't occupy — so all 20 mgmt statics land here, including
// bastion_ip/bosh_ip, which the pre-Task-6 idx-branching writer never
// produced for this path). This is a Layer A (bootstrap) characterization,
// distinct from the STACKIT vault provider's own single-subnet path (Layer
// B), which always calls configureSubnetReservedIPs with a real index (0)
// and never sees idx -1 — see
// internal/vault/stackit_reserved_ips_test.go's
// TestStackitReservedIPs_SingleSubnetWideDefault for that contract.
func TestStackitSingleSubnet_WideDefault_LocksNegativeIndexKeySet(t *testing.T) {
	t.Parallel()

	mgr, sm, _ := newStackitTestManager(t)
	ctx := context.Background()

	if err := mgr.CreateNetwork(ctx); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	if err := mgr.CreateSubnets(ctx); err != nil {
		t.Fatalf("CreateSubnets: %v", err)
	}

	// prod-subnet is the whole 10.4.0.0/20 parent CIDR, unsplit.
	for _, tc := range []struct {
		key  string
		want string
	}{
		{key: "reserved_prod-subnet_bastion_ip", want: "10.4.0.3"},
		{key: "reserved_prod-subnet_bosh_ip", want: "10.4.0.4"},
		{key: "reserved_prod-subnet_vault_ip", want: "10.4.0.5"},
		{key: "reserved_prod-subnet_jumpbox_ip", want: "10.4.0.6"},
		{key: "reserved_prod-subnet_concourse_ip", want: "10.4.0.7"},
		{key: "reserved_prod-subnet_prometheus_ip", want: "10.4.0.8"},
		{key: "reserved_prod-subnet_shield_ip", want: "10.4.0.9"},
		{key: "reserved_prod-subnet_blacksmith_ip", want: "10.4.0.10"},
		{key: "reserved_prod-subnet_artifacts_ip", want: "10.4.0.11"},
		{key: "reserved_prod-subnet_wireguard_ip", want: "10.4.0.12"},
		{key: "reserved_prod-subnet_ovpn_ip", want: "10.4.0.13"},
		{key: "reserved_prod-subnet_rustfs_ip", want: "10.4.0.14"},
		{key: "reserved_prod-subnet_proxycache_ip", want: "10.4.0.15"},
		{key: "reserved_prod-subnet_nfs_ip", want: "10.4.0.16"},
		{key: "reserved_prod-subnet_ocfp_ui_ip", want: "10.4.0.17"},
		{key: "reserved_prod-subnet_doomsday_ip", want: "10.4.0.18"},
		{key: "reserved_prod-subnet_shout_ip", want: "10.4.0.19"},
		{key: "reserved_prod-subnet_garage_ip", want: "10.4.0.20"},
		{key: "reserved_prod-subnet_rustfs_ip_smoke", want: "10.4.0.21"},
		{key: "reserved_prod-subnet_garage_ip_smoke", want: "10.4.0.22"},
		{key: "reserved_prod-subnet_available_a", want: "10.4.0.32"},
		{key: "reserved_prod-subnet_available_b", want: "10.4.0.63"},
		{key: "reserved_prod-subnet_reserved_b", want: "10.4.0.31"},
		{key: "reserved_prod-subnet_reserved_c", want: "10.4.0.64"},
	} {
		if got := getOutputString(t, sm, tc.key); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// TestStackitSingleSubnet_SpanningDropsPinnedStatics traces the idx -1 slot
// resolution a stackit-single bloc explicitly set to network.strategy:
// spanning would get (Task 6 review, Concern 3): spanning's mgmt tier pins
// 13 of its 20 statics (bastion/bosh/shield/blacksmith/wireguard/ovpn/
// rustfs/proxycache/nfs/garage to subnet 0, ocfp_ui to subnet 2, doomsday/
// shout to subnet 1); at idx -1 none of those pinned entries match (see
// netlayout's TestLayerASlotsOCFPNegativeIndexDropsPinned), so they are
// silently absent here rather than erroring. The mgmt tier's available band
// (32-63) has no subnet pinning of its own, so bandFor still finds exactly
// one match at idx -1 and the band populates normally.
//
// This test now calls cfg.ResolveReservedIPLayout + layout.LayerASlots
// directly rather than driving the full CreateNetwork/CreateSubnets flow:
// Task 12 wired netlayout.Layout.ValidateSubnetSet enforcement into
// createStackitSingleSubnet, so a stackit-single bloc (one workload
// subnet) explicitly set to spanning (MinSubnets 3) now fails CreateSubnets
// with netlayout.ErrTooFewSubnets before any subnet is recorded, rather
// than reaching the sparse idx -1 table this test characterizes (see
// TestCreateSubnets_StackitSingle_SpanningRejectsTooFewSubnets for that
// rejection). The LayerASlots resolution itself is unchanged by Task 12, so
// it is exercised directly here, mirroring netlayout's own
// TestLayerASlotsOCFPNegativeIndexDropsPinned.
func TestStackitSingleSubnet_SpanningDropsPinnedStatics(t *testing.T) {
	t.Parallel()

	_, _, cfg := newStackitTestManager(t)
	cfg.Network.Strategy = "spanning"

	layout, err := cfg.ResolveReservedIPLayout()
	if err != nil {
		t.Fatalf("ResolveReservedIPLayout: %v", err)
	}

	const cidr = "10.4.0.0/20"

	slots, err := layout.LayerASlots("ocfp", cidr, -1)
	if err != nil {
		t.Fatalf("LayerASlots(ocfp, idx -1): %v", err)
	}

	// The 7 unpinned mgmt statics still apply to every index, idx -1 included.
	for _, tc := range []struct {
		key    string
		offset int
	}{
		{key: "vault_ip", offset: 5},
		{key: "jumpbox_ip", offset: 6},
		{key: "concourse_ip", offset: 7},
		{key: "prometheus_ip", offset: 8},
		{key: "artifacts_ip", offset: 11},
		{key: "rustfs_ip_smoke", offset: 21},
		{key: "garage_ip_smoke", offset: 22},
	} {
		offset, ok := namedSlotOffset(slots.Named, tc.key)
		if !ok {
			t.Errorf("Named missing unpinned key %s", tc.key)

			continue
		}

		if offset != tc.offset {
			t.Errorf("%s offset = %d, want %d", tc.key, offset, tc.offset)
		}
	}

	// The unpinned mgmt band (32-63) resolves cleanly at idx -1 too.
	if slots.AvailableA != 32 { //nolint:mnd
		t.Errorf("AvailableA = %d, want 32", slots.AvailableA)
	}

	if slots.AvailableB != 63 { //nolint:mnd
		t.Errorf("AvailableB = %d, want 63", slots.AvailableB)
	}

	if slots.ReservedB != 31 { //nolint:mnd
		t.Errorf("ReservedB = %d, want 31", slots.ReservedB)
	}

	if slots.ReservedC != 64 { //nolint:mnd
		t.Errorf("ReservedC = %d, want 64", slots.ReservedC)
	}

	// The 13 subnet-pinned mgmt statics never occupy index -1's position,
	// so none of them appear.
	for _, key := range []string{
		"bastion_ip",
		"bosh_ip",
		"shield_ip",
		"blacksmith_ip",
		"wireguard_ip",
		"ovpn_ip",
		"rustfs_ip",
		"proxycache_ip",
		"nfs_ip",
		"ocfp_ui_ip",
		"doomsday_ip",
		"shout_ip",
		"garage_ip",
	} {
		if _, ok := namedSlotOffset(slots.Named, key); ok {
			t.Errorf("Named contains pinned key %s, want absent (pinned to a subnet index -1 does not occupy)", key)
		}
	}
}

// namedSlotOffset looks up key in named, mirroring netlayout's own
// unexported hasNamedKey test helper (compiled_test.go) for callers outside
// that package.
func namedSlotOffset(named []netlayout.NamedSlot, key string) (int, bool) {
	for _, n := range named {
		if n.Key == key {
			return n.Offset, true
		}
	}

	return 0, false
}

// TestCreateSubnets_StackitSingle_SpanningRejectsTooFewSubnets proves Layer
// A enforcement (Task 12): a stackit-single bloc explicitly set to the
// spanning strategy (MinSubnets 3) carves exactly one workload subnet, so
// CreateSubnets must fail with netlayout.ErrTooFewSubnets before recording
// anything to state — the full-flow counterpart to
// TestStackitSingleSubnet_SpanningDropsPinnedStatics above, which now
// exercises the same idx -1 slot resolution directly instead of relying on
// this rejected combination reaching it.
func TestCreateSubnets_StackitSingle_SpanningRejectsTooFewSubnets(t *testing.T) {
	t.Parallel()

	mgr, sm, cfg := newStackitTestManager(t)
	cfg.Network.Strategy = "spanning"

	ctx := context.Background()

	if err := mgr.CreateNetwork(ctx); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	err := mgr.CreateSubnets(ctx)
	if err == nil {
		t.Fatal("CreateSubnets: want error, got nil")
	}

	if !errors.Is(err, netlayout.ErrTooFewSubnets) {
		t.Errorf("CreateSubnets error = %v, want wrapping netlayout.ErrTooFewSubnets", err)
	}

	// No subnet was recorded to state before the error.
	if _, err := sm.GetResource("subnet", "prod-subnet"); err == nil {
		t.Error("subnet resource recorded despite ErrTooFewSubnets, want no state mutation")
	}

	if _, err := sm.GetOutput("reserved_prod-subnet_vault_ip"); err == nil {
		t.Error("reserved-ip outputs written despite ErrTooFewSubnets, want no state mutation")
	}
}

// newAWSTestManager builds an AWS-provider Manager over a fresh state store
// with explicit Network.Subnets, exercising the createStandardSubnets path
// (useVirtualSubnets is false for AWS without the triple subnet strategy).
// The returned fakeNet records every CreateSubnet request so tests can
// assert nothing was created. The returned *config.Config is the same
// pointer wired into the Manager.
func newAWSTestManager(t *testing.T, subnets []config.Subnet) (*bootstrap.Manager, *fakeNet, *config.Config) {
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
	cfg.Provider = "aws"
	cfg.Network.NetworkCIDR = "10.4.0.0/20"
	cfg.Network.Subnets = subnets

	fakeNetwork := &fakeNet{}
	fakeProvider := &fakeProv{n: fakeNetwork, c: &fakeCompute{}}

	mgr := bootstrap.NewManager(cfg, fakeProvider, sm, &bootstrap.Options{
		BlocName: "prod",
		Provider: "aws",
		Region:   "us-east-1",
	})

	return mgr, fakeNetwork, cfg
}

// TestCreateSubnets_AWS_SpanningRejectsTooFewSubnets proves Layer A
// enforcement on the AWS standard-subnet path: AWS defaults to the spanning
// strategy (MinSubnets 3), so a bloc explicitly configured with only two
// workload subnets must fail CreateSubnets with netlayout.ErrTooFewSubnets
// before any cloud subnet is created — the createStandardSubnets
// counterpart to TestCreateSubnets_StackitSingle_SpanningRejectsTooFewSubnets.
func TestCreateSubnets_AWS_SpanningRejectsTooFewSubnets(t *testing.T) {
	t.Parallel()

	mgr, fakeNetwork, _ := newAWSTestManager(t, []config.Subnet{
		{Name: "prod-ocfp-0", CIDR: "10.4.4.0/22", Type: "public", AvailabilityZone: "us-east-1a"},
		{Name: "prod-ocfp-1", CIDR: "10.4.8.0/22", Type: "public", AvailabilityZone: "us-east-1b"},
	})

	ctx := context.Background()

	if err := mgr.CreateNetwork(ctx); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	err := mgr.CreateSubnets(ctx)
	if err == nil {
		t.Fatal("CreateSubnets: want error, got nil")
	}

	if !errors.Is(err, netlayout.ErrTooFewSubnets) {
		t.Errorf("CreateSubnets error = %v, want wrapping netlayout.ErrTooFewSubnets", err)
	}

	if len(fakeNetwork.createdSubnetReqs) != 0 {
		t.Errorf("CreateSubnet called %d times despite ErrTooFewSubnets, want no cloud mutation",
			len(fakeNetwork.createdSubnetReqs))
	}
}

// TestCreateSubnets_AWS_SpanningAcceptsThreeSubnets is the passing
// counterpart: three workload subnets satisfy spanning's MinSubnets and all
// three reach the provider.
func TestCreateSubnets_AWS_SpanningAcceptsThreeSubnets(t *testing.T) {
	t.Parallel()

	mgr, fakeNetwork, _ := newAWSTestManager(t, []config.Subnet{
		{Name: "prod-ocfp-0", CIDR: "10.4.4.0/22", Type: "public", AvailabilityZone: "us-east-1a"},
		{Name: "prod-ocfp-1", CIDR: "10.4.8.0/22", Type: "public", AvailabilityZone: "us-east-1b"},
		{Name: "prod-ocfp-2", CIDR: "10.4.12.0/22", Type: "public", AvailabilityZone: "us-east-1c"},
	})

	ctx := context.Background()

	if err := mgr.CreateNetwork(ctx); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	if err := mgr.CreateSubnets(ctx); err != nil {
		t.Fatalf("CreateSubnets: %v", err)
	}

	if len(fakeNetwork.createdSubnetReqs) != 3 {
		t.Errorf("CreateSubnet called %d times, want 3", len(fakeNetwork.createdSubnetReqs))
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
