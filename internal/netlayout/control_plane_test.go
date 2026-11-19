package netlayout_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/netlayout"
	"github.com/fivetwenty-io/ocfp-cli/internal/reservedip"
)

// controlPlaneHeadroom is the offset range wide and spanning hold for the
// control-plane role: 23 today, and 24-25 kept free so a later list of
// control-plane addresses can sit beside the single one without moving it.
var controlPlaneHeadroom = []int{24, 25}

// TestControlPlaneStaticPlacement pins where each built-in strategy puts the
// control-plane role. wide places it at 23 on every subnet, and spanning
// places it at 23 on subnet 0 only, the way the core mgmt roles bastion and
// bosh are pinned. compact has no mgmt offset outside its bands, so it
// carries no control-plane static at all.
func TestControlPlaneStaticPlacement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		strategy string
		want     *reservedip.Assignment
	}{
		{strategy: "wide", want: &reservedip.Assignment{Offset: 23}},
		{strategy: "spanning", want: &reservedip.Assignment{SubnetMapping: map[int][]int{23: {0}}}},
		{strategy: "compact", want: nil},
	}

	for _, tc := range tests {
		t.Run(tc.strategy, func(t *testing.T) {
			t.Parallel()

			layout, err := netlayout.Lookup(tc.strategy)
			if err != nil {
				t.Fatalf("Lookup(%q): %v", tc.strategy, err)
			}

			table, err := layout.WorkloadTable("10.64.64.0/22")
			if err != nil {
				t.Fatalf("WorkloadTable: %v", err)
			}

			got := table[netlayout.ControlPlaneRole]["mgmt"]
			if tc.want == nil {
				if got != nil {
					t.Fatalf("%s places control_plane at %+v, want no control-plane static", tc.strategy, got)
				}

				return
			}

			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%s control_plane/mgmt = %+v, want %+v", tc.strategy, got, tc.want)
			}

			if ocf := table[netlayout.ControlPlaneRole]["ocf"]; ocf != nil {
				t.Fatalf("%s places control_plane in the ocf tier (%+v), want mgmt only", tc.strategy, ocf)
			}
		})
	}
}

// TestControlPlaneListHeadroomIsFree proves offsets 24 and 25 stay
// unclaimed by every static and band of both tiers, on every subnet index,
// in wide and spanning. A later list of control-plane addresses grows into
// 23-25, so nothing else may take those offsets in the meantime.
func TestControlPlaneListHeadroomIsFree(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"wide", "spanning"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			layout, err := netlayout.Lookup(name)
			if err != nil {
				t.Fatalf("Lookup(%q): %v", name, err)
			}

			const cidr = "10.64.64.0/22"

			for _, offset := range controlPlaneHeadroom {
				// Moving the role onto a headroom offset is accepted only
				// when no other static holds that offset and no band of
				// either tier covers it, on every index the role sits on.
				if _, err := layout.WithStaticOverride(netlayout.TierMgmt, netlayout.ControlPlaneRole,
					cidr, 0, []int{offset}, nil); err != nil {
					t.Errorf("offset %d is not free: %v", offset, err)
				}
			}

			table, err := layout.WorkloadTable(cidr)
			if err != nil {
				t.Fatalf("WorkloadTable: %v", err)
			}

			for role, tiers := range table {
				for envType, a := range tiers {
					for _, offset := range controlPlaneHeadroom {
						if a.Offset == offset {
							t.Errorf("%s/%s holds headroom offset %d", role, envType, offset)
						}

						if _, pinned := a.SubnetMapping[offset]; pinned {
							t.Errorf("%s/%s holds headroom offset %d", role, envType, offset)
						}
					}
				}
			}
		})
	}
}

// TestWithStaticOverrideRejects walks every rule a control-plane override
// must satisfy on a wide /22 workload subnet, whose last usable offset is
// 1022 and whose broadcast offset is 1023.
func TestWithStaticOverrideRejects(t *testing.T) {
	t.Parallel()

	wide, err := netlayout.Lookup("wide")
	if err != nil {
		t.Fatalf("Lookup(wide): %v", err)
	}

	const cidr = "10.64.64.0/22"

	tests := []struct {
		name    string
		offsets []int
		wantErr error
		wantMsg string
	}{
		{name: "two entries", offsets: []int{23, 24}, wantErr: netlayout.ErrStaticOverrideCount, wantMsg: "got 2"},
		{name: "negative", offsets: []int{-1}, wantErr: netlayout.ErrStaticOverrideOutsideSubnet, wantMsg: "-1"},
		{name: "past broadcast", offsets: []int{1024}, wantErr: netlayout.ErrStaticOverrideOutsideSubnet, wantMsg: "1024"},
		{name: "network address", offsets: []int{0}, wantErr: netlayout.ErrStaticOverrideNetworkAddress, wantMsg: "10.64.64.0"},
		{name: "broadcast address", offsets: []int{1023}, wantErr: netlayout.ErrStaticOverrideBroadcastAddress, wantMsg: "10.64.67.255"},
		{name: "gateway", offsets: []int{1}, wantErr: netlayout.ErrStaticOverrideGateway, wantMsg: "10.64.64.1"},
		{name: "below floor", offsets: []int{2}, wantErr: netlayout.ErrStaticOverrideBelowFloor, wantMsg: "below 3"},
		{name: "mgmt band start", offsets: []int{32}, wantErr: netlayout.ErrStaticOverrideInBand, wantMsg: `tier "mgmt" band [32,63]`},
		{name: "mgmt band end", offsets: []int{63}, wantErr: netlayout.ErrStaticOverrideInBand, wantMsg: `tier "mgmt" band [32,63]`},
		{name: "ocf open band", offsets: []int{500}, wantErr: netlayout.ErrStaticOverrideInBand, wantMsg: `tier "ocf" band [96,1022]`},
		{name: "ocf haproxy", offsets: []int{97}, wantErr: netlayout.ErrStaticOverrideInBand, wantMsg: `tier "ocf" band`},
		{name: "mgmt static", offsets: []int{4}, wantErr: netlayout.ErrStaticOverrideCollidesStatic, wantMsg: `tier "mgmt" static "bosh"`},
		{name: "mgmt ip_key static", offsets: []int{22}, wantErr: netlayout.ErrStaticOverrideCollidesStatic, wantMsg: `"garage_smoke"`},
		{name: "ocf static", offsets: []int{64}, wantErr: netlayout.ErrStaticOverrideCollidesStatic, wantMsg: `tier "ocf" static "bosh"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := wide.WithStaticOverride(netlayout.TierMgmt, netlayout.ControlPlaneRole, cidr, 0, tc.offsets, nil)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("WithStaticOverride(%v) error = %v, want wrapping %v", tc.offsets, err, tc.wantErr)
			}

			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("WithStaticOverride(%v) error = %q, want it to mention %q", tc.offsets, err, tc.wantMsg)
			}

			if got != nil {
				t.Fatalf("WithStaticOverride(%v) returned a layout alongside its error", tc.offsets)
			}
		})
	}
}

// TestWithStaticOverrideRejectsUnknownRole proves an override can only move
// a static the strategy already places: compact places no control-plane
// static, and no strategy places one in the ocf tier.
func TestWithStaticOverrideRejectsUnknownRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		strategy string
		tier     netlayout.Tier
	}{
		{strategy: "compact", tier: netlayout.TierMgmt},
		{strategy: "wide", tier: netlayout.TierOCF},
		{strategy: "wide", tier: netlayout.TierInfra},
	}

	for _, tc := range tests {
		t.Run(tc.strategy+"/"+string(tc.tier), func(t *testing.T) {
			t.Parallel()

			layout, err := netlayout.Lookup(tc.strategy)
			if err != nil {
				t.Fatalf("Lookup(%q): %v", tc.strategy, err)
			}

			_, err = layout.WithStaticOverride(tc.tier, netlayout.ControlPlaneRole, "10.64.64.0/22", 0, []int{30}, nil)
			if !errors.Is(err, netlayout.ErrStaticOverrideUnknownRole) {
				t.Fatalf("error = %v, want wrapping ErrStaticOverrideUnknownRole", err)
			}

			if !strings.Contains(err.Error(), tc.strategy) {
				t.Fatalf("error %q does not name strategy %q", err, tc.strategy)
			}
		})
	}
}

// TestWithStaticOverrideAccepts proves an accepted override moves only the
// overridden role: the returned layout carries the new offset (keeping a
// spanning pin), every other entry of the table is untouched, and the
// original layout is not modified.
func TestWithStaticOverrideAccepts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		strategy string
		offset   int
		want     *reservedip.Assignment
	}{
		{strategy: "wide", offset: 30, want: &reservedip.Assignment{Offset: 30}},
		{strategy: "wide", offset: 80, want: &reservedip.Assignment{Offset: 80}},
		{strategy: "spanning", offset: 30, want: &reservedip.Assignment{SubnetMapping: map[int][]int{30: {0}}}},
	}

	const cidr = "10.64.64.0/22"

	for _, tc := range tests {
		t.Run(tc.strategy, func(t *testing.T) {
			t.Parallel()

			base, err := netlayout.Lookup(tc.strategy)
			if err != nil {
				t.Fatalf("Lookup(%q): %v", tc.strategy, err)
			}

			before, err := base.WorkloadTable(cidr)
			if err != nil {
				t.Fatalf("WorkloadTable: %v", err)
			}

			derived, err := base.WithStaticOverride(netlayout.TierMgmt, netlayout.ControlPlaneRole, cidr, 0, []int{tc.offset}, nil)
			if err != nil {
				t.Fatalf("WithStaticOverride(%d): %v", tc.offset, err)
			}

			got, err := derived.WorkloadTable(cidr)
			if err != nil {
				t.Fatalf("derived WorkloadTable: %v", err)
			}

			if a := got[netlayout.ControlPlaneRole]["mgmt"]; !reflect.DeepEqual(a, tc.want) {
				t.Fatalf("derived control_plane/mgmt = %+v, want %+v", a, tc.want)
			}

			for role, tiers := range before {
				if role == netlayout.ControlPlaneRole {
					continue
				}

				if !reflect.DeepEqual(got[role], tiers) {
					t.Errorf("override moved %s: got %+v, want %+v", role, got[role], tiers)
				}
			}

			after, err := base.WorkloadTable(cidr)
			if err != nil {
				t.Fatalf("WorkloadTable after override: %v", err)
			}

			if !reflect.DeepEqual(after, before) {
				t.Fatal("WithStaticOverride modified the layout it was called on")
			}

			if derived.Name() != base.Name() || derived.SchemeVersion() != base.SchemeVersion() {
				t.Fatalf("derived layout is %q/%q, want %q/%q",
					derived.Name(), derived.SchemeVersion(), base.Name(), base.SchemeVersion())
			}
		})
	}
}

// TestWithStaticOverrideUsesEffectiveBand proves the band check runs
// against the band the bloc actually uses. With network.bands.mgmt moving
// the mgmt band to 41-63, offset 40 lies outside every band and is
// accepted, while offset 45 lands inside the moved band and is rejected,
// even though the strategy's own band is 32-63 in both cases.
func TestWithStaticOverrideUsesEffectiveBand(t *testing.T) {
	t.Parallel()

	wide, err := netlayout.Lookup("wide")
	if err != nil {
		t.Fatalf("Lookup(wide): %v", err)
	}

	const cidr = "10.64.64.0/22"

	bands := map[netlayout.Tier]netlayout.BandRange{netlayout.TierMgmt: {Start: 41, End: 63}}

	derived, err := wide.WithStaticOverride(netlayout.TierMgmt, netlayout.ControlPlaneRole, cidr, 0, []int{40}, bands)
	if err != nil {
		t.Fatalf("offset 40 beside band 41-63: %v", err)
	}

	if err := derived.ValidateBand(netlayout.TierMgmt, cidr, 41, 63); err != nil {
		t.Fatalf("derived ValidateBand(41, 63) = %v, want nil", err)
	}

	_, err = wide.WithStaticOverride(netlayout.TierMgmt, netlayout.ControlPlaneRole, cidr, 0, []int{45}, bands)
	if !errors.Is(err, netlayout.ErrStaticOverrideInBand) {
		t.Fatalf("offset 45 inside band 41-63: error = %v, want wrapping ErrStaticOverrideInBand", err)
	}

	if !strings.Contains(err.Error(), "[41,63]") {
		t.Fatalf("error %q does not name the effective band [41,63]", err)
	}
}

// TestValidateBandSeesMovedControlPlane proves a band override is checked
// against where the control-plane role actually sits. The default role at
// 23 blocks a band starting at 23; once the role moves to 31, the same band
// collides with it at its new offset instead.
func TestValidateBandSeesMovedControlPlane(t *testing.T) {
	t.Parallel()

	wide, err := netlayout.Lookup("wide")
	if err != nil {
		t.Fatalf("Lookup(wide): %v", err)
	}

	const cidr = "10.64.64.0/22"

	err = wide.ValidateBand(netlayout.TierMgmt, cidr, 23, 63)
	if !errors.Is(err, netlayout.ErrBandOverrideCollidesStatic) || !strings.Contains(err.Error(), `"control_plane" at offset 23`) {
		t.Fatalf("ValidateBand(23, 63) = %v, want a collision with control_plane at 23", err)
	}

	derived, err := wide.WithStaticOverride(netlayout.TierMgmt, netlayout.ControlPlaneRole, cidr, 0, []int{31}, nil)
	if err != nil {
		t.Fatalf("WithStaticOverride(31): %v", err)
	}

	if err := derived.ValidateBand(netlayout.TierMgmt, cidr, 24, 30); err != nil {
		t.Fatalf("derived ValidateBand(24, 30) = %v, want nil once the role sits at 31", err)
	}

	err = derived.ValidateBand(netlayout.TierMgmt, cidr, 24, 63)
	if !errors.Is(err, netlayout.ErrBandOverrideCollidesStatic) || !strings.Contains(err.Error(), "offset 31") {
		t.Fatalf("derived ValidateBand(24, 63) = %v, want a collision with control_plane at 31", err)
	}
}

// staticOffsetsOn returns every offset a named static of either tier
// occupies on subnet index idx, keyed by offset, and reports any offset two
// statics share. An unpinned static (Offset) applies to every index, and a
// pinned static (SubnetMapping) applies only to the indices it lists.
func staticOffsetsOn(t *testing.T, table reservedip.AssignmentTable, idx int) map[int]string {
	t.Helper()

	seen := map[int]string{}

	record := func(offset int, owner string) {
		if other, dup := seen[offset]; dup {
			t.Errorf("index %d: offset %d assigned to both %s and %s", idx, offset, other, owner)
		}

		seen[offset] = owner
	}

	for role, tiers := range table {
		if role == "available" || role == "reserved" {
			continue
		}

		for envType, a := range tiers {
			owner := role + "/" + envType

			if len(a.SubnetMapping) == 0 {
				record(a.Offset, owner)

				continue
			}

			for offset, subnets := range a.SubnetMapping {
				if reservedip.ContainsInt(subnets, idx) {
					record(offset, owner)
				}
			}
		}
	}

	return seen
}

// TestStaticOffsetsAreUniqueAcrossTiers extends the ocf-only uniqueness
// check to every named static of both tiers on every subnet index of every
// built-in strategy. Both tiers share one address space per workload
// subnet, so the control-plane static must not alias any role of either
// tier, and no other pair may alias each other either.
func TestStaticOffsetsAreUniqueAcrossTiers(t *testing.T) {
	t.Parallel()

	wantControlPlane := map[string]map[int]bool{
		"wide":     {0: true},
		"spanning": {0: true, 1: false, 2: false},
		"compact":  {0: false},
	}

	for _, name := range []string{"wide", "spanning", "compact"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			layout, err := netlayout.Lookup(name)
			if err != nil {
				t.Fatalf("Lookup(%q): %v", name, err)
			}

			table, err := layout.WorkloadTable("10.64.64.0/22")
			if err != nil {
				t.Fatalf("WorkloadTable: %v", err)
			}

			for idx := range layout.MinSubnets() {
				seen := staticOffsetsOn(t, table, idx)

				held := seen[23] == "control_plane/mgmt"
				if want := wantControlPlane[name][idx]; held != want {
					t.Errorf("index %d: control_plane/mgmt at 23 = %v, want %v (offset 23 held by %q)",
						idx, held, want, seen[23])
				}
			}
		})
	}
}

// mixedSizeSpanningYAML is a bring-your-own spanning strategy whose ocf
// band is closed, so an offset above the ocf band is a valid static on a
// wide enough subnet. The builtin spanning strategy cannot show the case
// below: its min_prefix of 25 covers offsets 0-127 on every subnet, and its
// open ocf band from 96 rejects every offset at or above 96 on subnet 0, so
// any offset subnet 0 accepts also fits every other subnet.
const mixedSizeSpanningYAML = `
name: mixed-size-spanning
scheme_version: "test-mixed-size-spanning"
placement: spanning
min_prefix: 25
min_subnets: 2
tiers:
  mgmt:
    statics:
      bastion:       { offset: 3, subnets: [0] }
      control_plane: { offset: 23, subnets: [0] }
    available: { start: 32, end: 63 }
  ocf:
    statics:
      bosh: { offset: 64, subnets: [0] }
    available: { start: 96, end: 120 }
`

// TestWithStaticOverrideChecksOnlyPlacingSubnets proves a spanning
// override is checked only against the subnets that place the role. Subnet
// 0 is a /24 and places control_plane, so offset 200 is valid there. Subnet
// 1 is a /25 whose broadcast is offset 127, so 200 lies outside it, but the
// role is not placed on subnet 1 and its write must not reject the
// override.
func TestWithStaticOverrideChecksOnlyPlacingSubnets(t *testing.T) {
	t.Parallel()

	def, err := netlayout.LoadDefinition([]byte(mixedSizeSpanningYAML), "mixed-size-spanning.yaml")
	if err != nil {
		t.Fatalf("LoadDefinition: %v", err)
	}

	layout, err := netlayout.Compile(def)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	const (
		subnet0 = "10.0.0.0/24"
		subnet1 = "10.0.1.0/25"
	)

	if _, err := layout.WithStaticOverride(netlayout.TierMgmt, netlayout.ControlPlaneRole,
		subnet0, 0, []int{200}, nil); err != nil {
		t.Fatalf("subnet 0 (%s) rejected offset 200: %v", subnet0, err)
	}

	derived, err := layout.WithStaticOverride(netlayout.TierMgmt, netlayout.ControlPlaneRole,
		subnet1, 1, []int{200}, nil)
	if err != nil {
		t.Fatalf("subnet 1 (%s), which does not place the role, rejected offset 200: %v", subnet1, err)
	}

	table, err := derived.WorkloadTable(subnet1)
	if err != nil {
		t.Fatalf("WorkloadTable(%s): %v", subnet1, err)
	}

	want := &reservedip.Assignment{SubnetMapping: map[int][]int{200: {0}}}
	if a := table[netlayout.ControlPlaneRole]["mgmt"]; !reflect.DeepEqual(a, want) {
		t.Fatalf("control_plane/mgmt = %+v, want %+v (still pinned to subnet 0)", a, want)
	}

	// The subnet that places the role still enforces the address rules.
	_, err = layout.WithStaticOverride(netlayout.TierMgmt, netlayout.ControlPlaneRole,
		subnet1, 0, []int{200}, nil)
	if !errors.Is(err, netlayout.ErrStaticOverrideOutsideSubnet) {
		t.Fatalf("subnet index 0 on %s: error = %v, want wrapping ErrStaticOverrideOutsideSubnet", subnet1, err)
	}

	// Config-level errors fail on every subnet, placing or not.
	_, err = layout.WithStaticOverride(netlayout.TierMgmt, netlayout.ControlPlaneRole,
		subnet1, 1, []int{200, 201}, nil)
	if !errors.Is(err, netlayout.ErrStaticOverrideCount) {
		t.Fatalf("subnet index 1 with two offsets: error = %v, want wrapping ErrStaticOverrideCount", err)
	}
}
