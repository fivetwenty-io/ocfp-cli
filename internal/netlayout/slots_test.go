package netlayout_test

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
)

// parseRangeSpec splits a simple "start-end" RangeSpec (as emitted by
// WorkloadTable's available/mgmt entry) into its two numeric bounds. It
// deliberately does not handle the "N->" open-ended form or comma-joined
// subranges — WorkloadTable's mgmt available entry never uses either.
func parseRangeSpec(t *testing.T, spec string) (int, int) {
	t.Helper()

	parts := strings.Split(spec, "-")
	if len(parts) != 2 {
		t.Fatalf("parseRangeSpec(%q): want exactly one \"-\", got %d parts", spec, len(parts))
	}

	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		t.Fatalf("parseRangeSpec(%q): invalid start: %v", spec, err)
	}

	end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		t.Fatalf("parseRangeSpec(%q): invalid end: %v", spec, err)
	}

	return start, end
}

// TestSlots_LayerAgreement proves the property this abstraction exists to
// guarantee: for every registered strategy, LayerASlots("ocfp", cidr, idx)'s
// available band is IDENTICAL to the mgmt-tier available band the same
// strategy's own WorkloadTable emits for Layer B. If a future retune of
// either side's definition drifts them apart, this test catches it.
func TestSlots_LayerAgreement(t *testing.T) {
	t.Parallel()

	cases := []struct {
		strategy  string
		cidr      string
		wantStart int
		wantEnd   int
	}{
		{strategy: "wide", cidr: "10.64.64.0/22", wantStart: 32, wantEnd: 63},
		{strategy: "compact", cidr: "10.64.64.0/26", wantStart: 28, wantEnd: 35},
	}

	for _, tc := range cases {
		t.Run(tc.strategy, func(t *testing.T) {
			t.Parallel()

			layout, err := netlayout.Lookup(tc.strategy)
			if err != nil {
				t.Fatalf("Lookup(%q) returned unexpected error: %v", tc.strategy, err)
			}

			table, err := layout.WorkloadTable(tc.cidr)
			if err != nil {
				t.Fatalf("WorkloadTable(%q) returned unexpected error: %v", tc.cidr, err)
			}

			mgmtAvailable, ok := table["available"]["mgmt"]
			if !ok {
				t.Fatalf("WorkloadTable(%q) missing available/mgmt range", tc.cidr)
			}

			tableStart, tableEnd := parseRangeSpec(t, mgmtAvailable.RangeSpec)
			if tableStart != tc.wantStart || tableEnd != tc.wantEnd {
				t.Fatalf("WorkloadTable(%q) available/mgmt = %d-%d, want %d-%d (test's own expectation is stale)",
					tc.cidr, tableStart, tableEnd, tc.wantStart, tc.wantEnd)
			}

			slots, err := layout.LayerASlots("ocfp", tc.cidr, 0)
			if err != nil {
				t.Fatalf("LayerASlots(\"ocfp\", %q, 0) returned unexpected error: %v", tc.cidr, err)
			}

			if slots.AvailableA != tableStart || slots.AvailableB != tableEnd {
				t.Fatalf("LayerASlots(\"ocfp\", %q, 0) available band = %d-%d, want %d-%d (WorkloadTable's own mgmt band)",
					tc.cidr, slots.AvailableA, slots.AvailableB, tableStart, tableEnd)
			}

			// The reserved complement brackets that same band: reserved_b is
			// the offset immediately below it, reserved_c the one above.
			if slots.ReservedB != tableStart-1 || slots.ReservedC != tableEnd+1 {
				t.Fatalf("LayerASlots(\"ocfp\", %q, 0) reserved b/c = %d/%d, want %d/%d (band %d-%d bracketed)",
					tc.cidr, slots.ReservedB, slots.ReservedC, tableStart-1, tableEnd+1, tableStart, tableEnd)
			}
		})
	}
}

// TestSlots_StaticAgreement proves Layer A's named statics agree with Layer
// B's table role for role: every ocfp-role NamedSlot's offset is the offset
// the same strategy's WorkloadTable assigns that role under "mgmt". This is
// the guarantee the pre-netlayout scheme lacked — bootstrap's hand-written
// idx branches placed doomsday/shout/ocfp_ui at offsets 9/10/9, while
// vault's table put them at 18/19/17.
//
// Checked at idx 0: a static PINNED to a subnet (spanning's bastion, e.g.)
// compiles to a SubnetMapping assignment rather than a plain Offset (see
// buildStatics), so its wantOffsets entry is drawn from whichever mapped
// offset includes index 0 — omitted entirely if no such offset exists,
// matching LayerASlots dropping it from idx 0's Named set too.
func TestSlots_StaticAgreement(t *testing.T) {
	t.Parallel()

	for _, strategy := range netlayout.Names() {
		t.Run(strategy, func(t *testing.T) {
			t.Parallel()

			layout, err := netlayout.Lookup(strategy)
			if err != nil {
				t.Fatalf("Lookup(%q) returned unexpected error: %v", strategy, err)
			}

			const cidr = "10.64.64.0/26"

			table, err := layout.WorkloadTable(cidr)
			if err != nil {
				t.Fatalf("WorkloadTable returned unexpected error: %v", err)
			}

			// Layer B keys a role's output as ip_key when set, else
			// "<role>_ip" — the same rule LayerASlots applies to NamedSlot.Key.
			wantOffsets := map[string]int{}

			for role, byEnvType := range table {
				assignment, ok := byEnvType["mgmt"]
				if !ok || assignment.RangeSpec != "" {
					continue
				}

				if len(assignment.SubnetMapping) > 0 {
					for offset, subnets := range assignment.SubnetMapping {
						if staticAgreementContainsIdx(subnets, 0) {
							wantOffsets[role+"_ip"] = offset
						}
					}

					continue
				}

				key := assignment.IPKey
				if key == "" {
					key = role + "_ip"
				}

				wantOffsets[key] = assignment.Offset
			}

			slots, err := layout.LayerASlots("ocfp", cidr, 0)
			if err != nil {
				t.Fatalf("LayerASlots returned unexpected error: %v", err)
			}

			for _, slot := range slots.Named {
				want, ok := wantOffsets[slot.Key]
				if !ok {
					t.Errorf("LayerASlots named %q, which WorkloadTable's mgmt tier does not assign", slot.Key)

					continue
				}

				if slot.Offset != want {
					t.Errorf("LayerASlots %q offset = %d, want %d (WorkloadTable's own mgmt offset)",
						slot.Key, slot.Offset, want)
				}
			}

			if len(slots.Named) != len(wantOffsets) {
				t.Errorf("LayerASlots named %d statics, want all %d of the mgmt tier's",
					len(slots.Named), len(wantOffsets))
			}
		})
	}
}

// staticAgreementContainsIdx reports whether subnets contains idx —
// TestSlots_StaticAgreement's own membership check over a SubnetMapping's
// pinned indices, kept local so the test doesn't need to import
// internal/reservedip only for reservedip.ContainsInt.
func staticAgreementContainsIdx(subnets []int, idx int) bool {
	for _, s := range subnets {
		if s == idx {
			return true
		}
	}

	return false
}

// TestSlots_ColocatedSameOnEveryIndex proves every COLOCATED strategy places
// its full mgmt static set on EVERY workload subnet: it declares colocated
// placement, so no static is pinned to one index, and a bloc's ocfp-0/1/2
// subnets get identical layouts relative to their own bases. This replaces
// the pre-netlayout behavior where bootstrap wrote bastion/bosh only on
// index 0, doomsday/shout only on index 1, and ocfp_ui only on index 2. A
// spanning strategy is deliberately exempt: pinning statics to specific
// indices is the whole point of spanning placement (see
// TestSpanningLayerASlots in spanning_test.go for its own per-index proof).
func TestSlots_ColocatedSameOnEveryIndex(t *testing.T) {
	t.Parallel()

	for _, strategy := range netlayout.Names() {
		t.Run(strategy, func(t *testing.T) {
			t.Parallel()

			layout, err := netlayout.Lookup(strategy)
			if err != nil {
				t.Fatalf("Lookup(%q) returned unexpected error: %v", strategy, err)
			}

			if layout.Placement() != netlayout.PlacementColocated {
				t.Skipf("Placement() = %q, this test only asserts colocated behavior", layout.Placement())
			}

			const cidr = "10.64.64.0/26"

			first, err := layout.LayerASlots("ocfp", cidr, 0)
			if err != nil {
				t.Fatalf("LayerASlots(idx 0) returned unexpected error: %v", err)
			}

			for idx := 1; idx < 3; idx++ {
				got, err := layout.LayerASlots("ocfp", cidr, idx)
				if err != nil {
					t.Fatalf("LayerASlots(idx %d) returned unexpected error: %v", idx, err)
				}

				if !reflect.DeepEqual(got, first) {
					t.Fatalf("LayerASlots(idx %d) = %+v, want identical to idx 0's %+v", idx, got, first)
				}
			}
		})
	}
}

// TestSlots_Infra proves every strategy emits the exact historical
// infra-role slot layout carried over from
// internal/bootstrap.pveSubnetStrategy.reservedIPLayout: the four named
// statics the bootstrap subnet reads before BOSH exists, and the same 12-29
// available band, regardless of strategy, subnet size, or index — the infra
// role applies to the fixed infra subnet, not a workload subnet whose layout
// varies by strategy.
func TestSlots_Infra(t *testing.T) {
	t.Parallel()

	want := netlayout.LayerASlots{
		Named: []netlayout.NamedSlot{
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

	for _, tc := range []struct {
		strategy string
		cidr     string
	}{
		{strategy: "wide", cidr: "10.64.64.0/22"},
		// A /26's host offsets run 0-63, well past the infra band's
		// highest offset (30, reservedC): 12-29 fits with room to spare,
		// so compact's infra role needs no divergence from wide's.
		{strategy: "compact", cidr: "10.64.64.0/26"},
	} {
		t.Run(tc.strategy, func(t *testing.T) {
			t.Parallel()

			layout, err := netlayout.Lookup(tc.strategy)
			if err != nil {
				t.Fatalf("Lookup(%q) returned unexpected error: %v", tc.strategy, err)
			}

			// -1 is what bootstrap passes for the infra subnet, which has no
			// workload position; 0 must give the same fixed answer.
			for _, idx := range []int{-1, 0} {
				got, err := layout.LayerASlots("infra", tc.cidr, idx)
				if err != nil {
					t.Fatalf("LayerASlots(\"infra\", %q, %d) returned unexpected error: %v", tc.cidr, idx, err)
				}

				if !reflect.DeepEqual(got, want) {
					t.Fatalf("LayerASlots(\"infra\", %q, %d) = %+v, want %+v", tc.cidr, idx, got, want)
				}
			}
		})
	}
}

// TestSlots_InfraIgnoresCIDR proves the infra role's slot layout does not
// vary by subnet size, matching subnet_strategy.go's reservedIPLayout,
// which returns defaultReservedIPLayout() unconditionally for any
// non-"ocfp" role without inspecting the CIDR at all.
func TestSlots_InfraIgnoresCIDR(t *testing.T) {
	t.Parallel()

	wide, err := netlayout.Lookup("wide")
	if err != nil {
		t.Fatalf("Lookup(\"wide\") returned unexpected error: %v", err)
	}

	small, err := wide.LayerASlots("infra", "10.0.0.0/24", 0)
	if err != nil {
		t.Fatalf("LayerASlots(\"infra\", /24) returned unexpected error: %v", err)
	}

	large, err := wide.LayerASlots("infra", "10.0.0.0/16", 0)
	if err != nil {
		t.Fatalf("LayerASlots(\"infra\", /16) returned unexpected error: %v", err)
	}

	if !reflect.DeepEqual(small, large) {
		t.Fatalf("LayerASlots(\"infra\", ...) varied with cidr: %+v != %+v", small, large)
	}
}

// TestSlots_OCFPNoSizeWidening proves the ocfp role's available band is
// unconditionally fixed to the strategy's own mgmt-tier band, regardless of
// the workload subnet's size — replacing the historical
// total/2-buffer widening pveSubnetStrategy.reservedIPLayout used to apply
// per-CIDR. A wide /22 and a hand-built oversized /16 must agree exactly.
func TestSlots_OCFPNoSizeWidening(t *testing.T) {
	t.Parallel()

	wide, err := netlayout.Lookup("wide")
	if err != nil {
		t.Fatalf("Lookup(\"wide\") returned unexpected error: %v", err)
	}

	small, err := wide.LayerASlots("ocfp", "10.64.64.0/22", 0)
	if err != nil {
		t.Fatalf("LayerASlots(\"ocfp\", /22) returned unexpected error: %v", err)
	}

	large, err := wide.LayerASlots("ocfp", "10.0.0.0/16", 0)
	if err != nil {
		t.Fatalf("LayerASlots(\"ocfp\", /16) returned unexpected error: %v", err)
	}

	if !reflect.DeepEqual(small, large) {
		t.Fatalf("LayerASlots(\"ocfp\", ...) varied with cidr: %+v != %+v", small, large)
	}

	if small.AvailableA != 32 || small.AvailableB != 63 {
		t.Fatalf("LayerASlots(\"ocfp\", ...) available band = %d-%d, want 32-63", small.AvailableA, small.AvailableB)
	}
}

// TestSlots_UnknownRole proves every strategy rejects a role that is neither
// "infra" nor "ocfp" with a defined, greppable error rather than a
// silently-wrong zero-value slot set.
func TestSlots_UnknownRole(t *testing.T) {
	t.Parallel()

	for _, strategy := range netlayout.Names() {
		t.Run(strategy, func(t *testing.T) {
			t.Parallel()

			layout, err := netlayout.Lookup(strategy)
			if err != nil {
				t.Fatalf("Lookup(%q) returned unexpected error: %v", strategy, err)
			}

			slots, err := layout.LayerASlots("bogus", "10.0.0.0/22", 0)
			if !errors.Is(err, netlayout.ErrUnknownRole) {
				t.Fatalf("LayerASlots(\"bogus\", ...) error = %v, want wrapping ErrUnknownRole", err)
			}

			if !strings.Contains(err.Error(), "bogus") {
				t.Fatalf("LayerASlots(\"bogus\", ...) error %q does not mention the offending role", err.Error())
			}

			if !reflect.DeepEqual(slots, netlayout.LayerASlots{}) {
				t.Fatalf("LayerASlots(\"bogus\", ...) slots = %+v, want zero value on error", slots)
			}
		})
	}
}
