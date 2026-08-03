package netlayout

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// wideDefinitionLiteral transcribes wide.go's exact offsets (wide.go:20-81)
// into a Definition, so TestCompiledWideTableMatchesLegacy can prove Compile
// produces byte-for-byte the same table as wideLayout's hand-written one.
// Every offset below is copied from the pveXxxOffset constants in wide.go —
// keep the two in lockstep if wide.go's offsets ever change.
func wideDefinitionLiteral() Definition {
	return Definition{
		Name:          "wide",
		SchemeVersion: wideSchemeVersion,
		Placement:     PlacementColocated,
		MinPrefix:     wideMinPrefix,
		MinSubnets:    1,
		Source:        "test",
		Tiers: map[Tier]TierDef{
			TierMgmt: {
				Statics: map[string]StaticPlacement{
					"bastion":      {Offset: 3},
					"bosh":         {Offset: 4},
					"vault":        {Offset: 5},
					"jumpbox":      {Offset: 6},
					"concourse":    {Offset: 7},
					"prometheus":   {Offset: 8},
					"shield":       {Offset: 9},
					"blacksmith":   {Offset: 10},
					"artifacts":    {Offset: 11},
					"wireguard":    {Offset: 12},
					"ovpn":         {Offset: 13},
					"rustfs":       {Offset: 14},
					"proxycache":   {Offset: 15},
					"nfs":          {Offset: 16},
					"ocfp_ui":      {Offset: 17},
					"doomsday":     {Offset: 18},
					"shout":        {Offset: 19},
					"garage":       {Offset: 20},
					"rustfs_smoke": {Offset: 21, IPKey: "rustfs_ip_smoke"},
					"garage_smoke": {Offset: 22, IPKey: "garage_ip_smoke"},
				},
				Available: []BandPlacement{{Start: 32, End: 63}},
			},
			TierOCF: {
				Statics: map[string]StaticPlacement{
					"bosh":       {Offset: 64},
					"vault":      {Offset: 65},
					"jumpbox":    {Offset: 66},
					"blacksmith": {Offset: 67},
					"haproxy":    {Offset: 97},
				},
				Available: []BandPlacement{{Start: 96}},
			},
		},
	}
}

// TestCompiledWideTableMatchesLegacy proves Compile+WorkloadTable produce
// exactly the same reservedip.AssignmentTable as the hand-written
// wideLayout, for a Definition transcribing wide.go's offsets verbatim.
func TestCompiledWideTableMatchesLegacy(t *testing.T) {
	def := wideDefinitionLiteral()

	layout, err := Compile(def)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	got, err := layout.WorkloadTable("10.0.0.0/22")
	if err != nil {
		t.Fatalf("WorkloadTable: %v", err)
	}

	want, _ := wideLayout{}.WorkloadTable("10.0.0.0/22")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("compiled wide != legacy wide\ngot:  %#v\nwant: %#v", got, want)
	}
}

// pinnedSpanningDef is a minimal valid spanning definition with one pinned
// static, used to exercise the SubnetMapping compilation path.
func pinnedSpanningDef() Definition {
	return Definition{
		Name: "pin", SchemeVersion: "x", Placement: PlacementSpanning,
		MinPrefix: 25, MinSubnets: 3, Source: "test",
		Tiers: map[Tier]TierDef{
			TierMgmt: {
				Statics: map[string]StaticPlacement{
					"app": {Offset: 10, Subnets: []int{0, 2}},
				},
				Available: []BandPlacement{{Start: 32, End: 63}},
			},
		},
	}
}

// TestCompiledPinnedStaticProducesSubnetMapping proves a pinned static
// (Subnets != nil) compiles to Assignment.SubnetMapping, not Offset.
func TestCompiledPinnedStaticProducesSubnetMapping(t *testing.T) {
	layout, err := Compile(pinnedSpanningDef())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	table, err := layout.WorkloadTable("")
	if err != nil {
		t.Fatalf("WorkloadTable: %v", err)
	}

	app, ok := table["app"]["mgmt"]
	if !ok {
		t.Fatal("WorkloadTable() missing app/mgmt assignment")
	}

	want := map[int][]int{10: {0, 2}}
	if !reflect.DeepEqual(app.SubnetMapping, want) {
		t.Fatalf("app/mgmt SubnetMapping = %#v, want %#v", app.SubnetMapping, want)
	}

	if app.Offset != 0 {
		t.Fatalf("app/mgmt Offset = %d, want 0 (pinned statics use SubnetMapping, not Offset)", app.Offset)
	}
}

// perIndexBandsDef is a minimal valid spanning definition whose mgmt tier
// available band is split by subnet index, used to exercise the
// SubnetRanges compilation path for both "available" and derived
// "reserved".
func perIndexBandsDef() Definition {
	return Definition{
		Name: "spans", SchemeVersion: "x", Placement: PlacementSpanning,
		MinPrefix: 25, MinSubnets: 3, Source: "test",
		Tiers: map[Tier]TierDef{
			TierMgmt: {
				Available: []BandPlacement{
					{Start: 38, Subnets: []int{0}},
					{Start: 37, Subnets: []int{1, 2}},
				},
			},
		},
	}
}

// TestCompiledPerIndexBandsProduceSubnetRanges proves per-index bands (more
// than one band entry, or a single pinned entry) compile to
// Assignment.SubnetRanges for both "available" and the derived "reserved"
// complement, rather than a single RangeSpec.
func TestCompiledPerIndexBandsProduceSubnetRanges(t *testing.T) {
	layout, err := Compile(perIndexBandsDef())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	table, err := layout.WorkloadTable("")
	if err != nil {
		t.Fatalf("WorkloadTable: %v", err)
	}

	available, ok := table["available"]["mgmt"]
	if !ok {
		t.Fatal("WorkloadTable() missing available/mgmt assignment")
	}

	wantAvailable := map[string][]int{"38->": {0}, "37->": {1, 2}}
	if !reflect.DeepEqual(available.SubnetRanges, wantAvailable) {
		t.Fatalf("available/mgmt SubnetRanges = %#v, want %#v", available.SubnetRanges, wantAvailable)
	}

	reserved, ok := table["reserved"]["mgmt"]
	if !ok {
		t.Fatal("WorkloadTable() missing reserved/mgmt assignment")
	}

	wantReserved := map[string][]int{"0-37": {0}, "0-36": {1, 2}}
	if !reflect.DeepEqual(reserved.SubnetRanges, wantReserved) {
		t.Fatalf("reserved/mgmt SubnetRanges = %#v, want %#v", reserved.SubnetRanges, wantReserved)
	}
}

// TestCompileRejectsPinnedStaticWithIPKey proves Compile surfaces
// validateDefinition's rejection of a pinned static (Subnets != nil) that
// also sets IPKey: the engine's SubnetMapping path (processSubnetMapping
// Assignment) never reads Assignment.IPKey, so such a definition can never
// produce the key its author asked for.
func TestCompileRejectsPinnedStaticWithIPKey(t *testing.T) {
	def := pinnedSpanningDef()
	tier := def.Tiers[TierMgmt]
	tier.Statics["app"] = StaticPlacement{Offset: 10, Subnets: []int{0, 2}, IPKey: "app_ip_custom"}
	def.Tiers[TierMgmt] = tier

	_, err := Compile(def)
	if !errors.Is(err, ErrBadPinning) {
		t.Fatalf("Compile() error = %v, want wrapping ErrBadPinning", err)
	}
}

// TestCompiledAccessorsReturnDefinitionFields proves the simple accessors
// pass through the compiled Definition's fields unchanged.
func TestCompiledAccessorsReturnDefinitionFields(t *testing.T) {
	layout, err := Compile(wideDefinitionLiteral())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if got := layout.Name(); got != "wide" {
		t.Errorf("Name() = %q, want %q", got, "wide")
	}

	if got := layout.SchemeVersion(); got != wideSchemeVersion {
		t.Errorf("SchemeVersion() = %q, want %q", got, wideSchemeVersion)
	}

	if got := layout.Placement(); got != PlacementColocated {
		t.Errorf("Placement() = %q, want %q", got, PlacementColocated)
	}

	if got := layout.MinPrefix(); got != wideMinPrefix {
		t.Errorf("MinPrefix() = %d, want %d", got, wideMinPrefix)
	}

	if got := layout.MinSubnets(); got != 1 {
		t.Errorf("MinSubnets() = %d, want %d", got, 1)
	}
}

// TestLayerASlotsInfraIsFixed proves the infra role's LayerASlots is the same
// fixed table regardless of strategy, cidr, or idx.
func TestLayerASlotsInfraIsFixed(t *testing.T) {
	layout, err := Compile(wideDefinitionLiteral())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	got, err := layout.LayerASlots("infra", "10.0.0.0/25", 0)
	if err != nil {
		t.Fatalf("LayerASlots: %v", err)
	}

	want := LayerASlots{
		Named: []NamedSlot{
			{Key: "bastion_ip", Offset: 3},
			{Key: "bosh_ip", Offset: 4},
			{Key: "shield_ip", Offset: 9},
			{Key: "blacksmith_ip", Offset: 10},
		},
		AvailableA: 12,
		AvailableB: 29,
		ReservedB:  10,
		ReservedC:  30,
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LayerASlots(infra) = %#v, want %#v", got, want)
	}
}

// TestLayerASlotsOCFPCompiledWide proves the ocfp role's LayerASlots for the
// compiled wide definition names every one of wide's 20 mgmt statics
// (wideDefinitionLiteral's mgmt tier is unpinned, so all apply to idx 0) and
// derives ReservedB from the mgmt band's own start (32-1=31) rather than
// reusing the infra role's ReservedB (10) — the historical
// ocfpSlots(32, 63) never corrected this, so 31 is the NEW value this method
// must produce.
func TestLayerASlotsOCFPCompiledWide(t *testing.T) {
	layout, err := Compile(wideDefinitionLiteral())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	got, err := layout.LayerASlots("ocfp", "10.0.0.0/25", 0)
	if err != nil {
		t.Fatalf("LayerASlots: %v", err)
	}

	if got.AvailableA != 32 || got.AvailableB != 63 || got.ReservedB != 31 || got.ReservedC != 64 {
		t.Fatalf("LayerASlots(ocfp) band = {%d %d %d %d}, want {32 63 31 64}",
			got.AvailableA, got.AvailableB, got.ReservedB, got.ReservedC)
	}

	if len(got.Named) != 20 {
		t.Fatalf("LayerASlots(ocfp) Named has %d entries, want 20", len(got.Named))
	}

	wantKeys := []string{
		"artifacts_ip", "bastion_ip", "blacksmith_ip", "bosh_ip", "concourse_ip",
		"doomsday_ip", "garage_ip", "garage_ip_smoke", "jumpbox_ip", "nfs_ip",
		"ocfp_ui_ip", "ovpn_ip", "prometheus_ip", "proxycache_ip", "rustfs_ip",
		"rustfs_ip_smoke", "shield_ip", "shout_ip", "vault_ip", "wireguard_ip",
	}

	gotKeys := make([]string, len(got.Named))
	for i, n := range got.Named {
		gotKeys[i] = n.Key
	}

	sort.Strings(gotKeys)

	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("LayerASlots(ocfp) keys = %v, want %v", gotKeys, wantKeys)
	}

	// Named must be sorted by offset then key: bastion (3) sorts before
	// bosh (4), which sorts before vault (5), ...
	for i := 1; i < len(got.Named); i++ {
		prev, cur := got.Named[i-1], got.Named[i]
		if prev.Offset > cur.Offset || (prev.Offset == cur.Offset && prev.Key > cur.Key) {
			t.Fatalf("LayerASlots(ocfp) Named not sorted at index %d: %+v then %+v", i, prev, cur)
		}
	}
}

// TestLayerASlotsOCFPSpanningRespectsPinning proves the ocfp role's Named
// set only includes a pinned static on the indices it's pinned to:
// validSpanningDef's mgmt bastion is pinned to subnet 0 only.
func TestLayerASlotsOCFPSpanningRespectsPinning(t *testing.T) {
	layout, err := Compile(validSpanningDef())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	idx0, err := layout.LayerASlots("ocfp", "10.0.0.0/25", 0)
	if err != nil {
		t.Fatalf("LayerASlots(idx 0): %v", err)
	}

	if !hasNamedKey(idx0.Named, "bastion_ip") {
		t.Fatalf("LayerASlots(ocfp, idx 0) Named = %+v, want bastion_ip present", idx0.Named)
	}

	idx1, err := layout.LayerASlots("ocfp", "10.0.0.0/25", 1)
	if err != nil {
		t.Fatalf("LayerASlots(idx 1): %v", err)
	}

	if hasNamedKey(idx1.Named, "bastion_ip") {
		t.Fatalf("LayerASlots(ocfp, idx 1) Named = %+v, want bastion_ip absent", idx1.Named)
	}
}

func hasNamedKey(named []NamedSlot, key string) bool {
	for _, n := range named {
		if n.Key == key {
			return true
		}
	}

	return false
}

// TestLayerASlotsUnknownRole proves an unrecognized role is rejected with
// ErrUnknownRole.
func TestLayerASlotsUnknownRole(t *testing.T) {
	layout, err := Compile(wideDefinitionLiteral())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	_, err = layout.LayerASlots("bogus", "10.0.0.0/25", 0)
	if !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("LayerASlots(bogus) error = %v, want wrapping ErrUnknownRole", err)
	}
}

// TestValidateSubnetRejectsTooSmall proves ValidateSubnet wraps
// ErrSubnetTooSmall with the strategy's own name and highest static offset
// when cidr's prefix is longer than MinPrefix.
func TestValidateSubnetRejectsTooSmall(t *testing.T) {
	layout, err := Compile(wideDefinitionLiteral())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	err = layout.ValidateSubnet("10.0.0.0/26")
	if !errors.Is(err, ErrSubnetTooSmall) {
		t.Fatalf("ValidateSubnet(/26) error = %v, want wrapping ErrSubnetTooSmall", err)
	}

	if err := layout.ValidateSubnet("10.0.0.0/25"); err != nil {
		t.Fatalf("ValidateSubnet(/25) = %v, want nil", err)
	}
}

// TestValidateSubnetSetRejectsTooFew proves ValidateSubnetSet wraps
// ErrTooFewSubnets when fewer cidrs are given than the strategy's
// MinSubnets, and otherwise delegates to ValidateSubnet per cidr.
func TestValidateSubnetSetRejectsTooFew(t *testing.T) {
	layout, err := Compile(validSpanningDef()) // MinSubnets: 3
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	err = layout.ValidateSubnetSet([]string{"10.0.0.0/25", "10.0.0.128/25"})
	if !errors.Is(err, ErrTooFewSubnets) {
		t.Fatalf("ValidateSubnetSet(2 of 3) error = %v, want wrapping ErrTooFewSubnets", err)
	}

	err = layout.ValidateSubnetSet([]string{"10.0.0.0/25", "10.0.0.128/25", "10.0.1.0/25"})
	if err != nil {
		t.Fatalf("ValidateSubnetSet(3 of 3) = %v, want nil", err)
	}

	err = layout.ValidateSubnetSet([]string{"10.0.0.0/25", "10.0.0.128/25", "10.0.1.0/26"})
	if !errors.Is(err, ErrSubnetTooSmall) {
		t.Fatalf("ValidateSubnetSet(one too small) error = %v, want wrapping ErrSubnetTooSmall", err)
	}
}

// TestValidateBandDelegatesInfra proves TierInfra delegates to the shared
// validateBand (band.go) unchanged.
func TestValidateBandDelegatesInfra(t *testing.T) {
	layout, err := Compile(wideDefinitionLiteral())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if err := layout.ValidateBand(TierInfra, "10.0.0.0/22", 40, 50); err != nil {
		t.Fatalf("ValidateBand(infra, 40, 50) = %v, want nil", err)
	}

	err = layout.ValidateBand(TierInfra, "10.0.0.0/22", 5, 50)
	if !errors.Is(err, ErrBandOverrideStartTooLow) {
		t.Fatalf("ValidateBand(infra, 5, 50) error = %v, want wrapping ErrBandOverrideStartTooLow", err)
	}
}

// TestValidateBandMgmtWide covers the mgmt-tier override matrix against the
// compiled wide definition: within its own available band and clear of both
// tiers' statics succeeds; overlapping either tier's statics fails with
// ErrBandOverrideCollidesStatic (and names the colliding tier/static so the
// "either tier" behavior is pinned, not just the sentinel); overlapping the
// ocf tier's available band without touching any static fails with
// ErrBandOverrideCrossTier.
func TestValidateBandMgmtWide(t *testing.T) {
	layout, err := Compile(wideDefinitionLiteral())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	const cidr = "10.0.0.0/22"

	cases := []struct {
		name          string
		start, end    int
		wantErr       error
		wantMsgSubstr string
	}{
		{"inside mgmt band, no collisions", 40, 50, nil, ""},
		{"inside mgmt band, no collisions (2)", 32, 40, nil, ""},
		{"collides with mgmt statics 20-22", 20, 40, ErrBandOverrideCollidesStatic, "mgmt"},
		{"collides with ocf statics 64-67", 30, 70, ErrBandOverrideCollidesStatic, "ocf"},
		{"crosses into ocf's open band, no statics", 98, 120, ErrBandOverrideCrossTier, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := layout.ValidateBand(TierMgmt, cidr, tc.start, tc.end)

			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidateBand(%d, %d) = %v, want nil", tc.start, tc.end, err)
				}

				return
			}

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateBand(%d, %d) error = %v, want wrapping %v", tc.start, tc.end, err, tc.wantErr)
			}

			if tc.wantMsgSubstr != "" && !strings.Contains(err.Error(), tc.wantMsgSubstr) {
				t.Fatalf("ValidateBand(%d, %d) error = %q, want containing %q", tc.start, tc.end, err.Error(), tc.wantMsgSubstr)
			}
		})
	}
}

// TestValidateBandPartialAndMalformed covers ValidateBand's shared
// input-shape checks for a non-infra tier: a partial override (only one of
// start/end set, or neither) and end beyond the subnet's usable range.
func TestValidateBandPartialAndMalformed(t *testing.T) {
	layout, err := Compile(wideDefinitionLiteral())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	const cidr = "10.0.0.0/22"

	if err := layout.ValidateBand(TierMgmt, cidr, 40, 0); !errors.Is(err, ErrBandOverridePartial) {
		t.Fatalf("ValidateBand(40, 0) error = %v, want wrapping ErrBandOverridePartial", err)
	}

	if err := layout.ValidateBand(TierMgmt, cidr, 0, 0); !errors.Is(err, ErrBandOverridePartial) {
		t.Fatalf("ValidateBand(0, 0) error = %v, want wrapping ErrBandOverridePartial", err)
	}

	if err := layout.ValidateBand(TierMgmt, cidr, 50, 40); !errors.Is(err, ErrBandOverrideEndNotAfterStart) {
		t.Fatalf("ValidateBand(50, 40) error = %v, want wrapping ErrBandOverrideEndNotAfterStart", err)
	}

	if err := layout.ValidateBand(TierMgmt, cidr, 40, 2000); !errors.Is(err, ErrBandOverrideEndBeyondSubnet) {
		t.Fatalf("ValidateBand(40, 2000) error = %v, want wrapping ErrBandOverrideEndBeyondSubnet", err)
	}
}
