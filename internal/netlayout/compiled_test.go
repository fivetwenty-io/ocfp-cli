package netlayout

import (
	"errors"
	"reflect"
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
