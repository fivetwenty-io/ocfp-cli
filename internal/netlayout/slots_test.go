package netlayout_test

import (
	"errors"
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

// TestSlots_LayerAgreement proves the property this task exists to
// guarantee: for every registered strategy, Slots("ocfp", cidr")'s
// available band is IDENTICAL to the mgmt-tier available band the same
// strategy's own WorkloadTable emits for Layer B. If a future retune of
// either side's constants drifts them apart, this test catches it.
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

			slots, err := layout.Slots("ocfp", tc.cidr)
			if err != nil {
				t.Fatalf("Slots(\"ocfp\", %q) returned unexpected error: %v", tc.cidr, err)
			}

			if slots.AvailableA != tableStart || slots.AvailableB != tableEnd {
				t.Fatalf("Slots(\"ocfp\", %q) available band = %d-%d, want %d-%d (WorkloadTable's own mgmt band)",
					tc.cidr, slots.AvailableA, slots.AvailableB, tableStart, tableEnd)
			}
		})
	}
}

// TestSlots_Infra proves both strategies emit the exact historical
// infra-role slot layout carried over from
// internal/bootstrap.pveSubnetStrategy.reservedIPLayout: the same named
// offsets and the same 12-29 available band, regardless of strategy or
// subnet size, since the infra role applies to the fixed infra subnet, not
// a workload subnet whose size varies by strategy.
func TestSlots_Infra(t *testing.T) {
	t.Parallel()

	want := netlayout.InfraSlots{
		Bastion:        3,
		Bosh:           4,
		Vault:          5,
		Jumpbox:        6,
		Concourse:      7,
		Prometheus:     8,
		Shield:         9,
		Blacksmith:     10,
		BlacksmithOCFP: 3,
		Doomsday:       9,
		Shout:          10,
		OCFPUI:         9,
		Artifacts:      11,
		AvailableA:     12,
		AvailableB:     29,
		ReservedB:      10,
		ReservedC:      30,
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

			got, err := layout.Slots("infra", tc.cidr)
			if err != nil {
				t.Fatalf("Slots(\"infra\", %q) returned unexpected error: %v", tc.cidr, err)
			}

			if got != want {
				t.Fatalf("Slots(\"infra\", %q) = %+v, want %+v", tc.cidr, got, want)
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

	small, err := wide.Slots("infra", "10.0.0.0/24")
	if err != nil {
		t.Fatalf("Slots(\"infra\", /24) returned unexpected error: %v", err)
	}

	large, err := wide.Slots("infra", "10.0.0.0/16")
	if err != nil {
		t.Fatalf("Slots(\"infra\", /16) returned unexpected error: %v", err)
	}

	if small != large {
		t.Fatalf("Slots(\"infra\", ...) varied with cidr: %+v != %+v", small, large)
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

	small, err := wide.Slots("ocfp", "10.64.64.0/22")
	if err != nil {
		t.Fatalf("Slots(\"ocfp\", /22) returned unexpected error: %v", err)
	}

	large, err := wide.Slots("ocfp", "10.0.0.0/16")
	if err != nil {
		t.Fatalf("Slots(\"ocfp\", /16) returned unexpected error: %v", err)
	}

	if small != large {
		t.Fatalf("Slots(\"ocfp\", ...) varied with cidr: %+v != %+v", small, large)
	}

	if small.AvailableA != 32 || small.AvailableB != 63 {
		t.Fatalf("Slots(\"ocfp\", ...) available band = %d-%d, want 32-63", small.AvailableA, small.AvailableB)
	}
}

// TestSlots_UnknownRole proves both strategies reject a role that is
// neither "infra" nor "ocfp" with a defined, greppable error rather than a
// silently-wrong zero-value InfraSlots.
func TestSlots_UnknownRole(t *testing.T) {
	t.Parallel()

	for _, strategy := range netlayout.Names() {
		t.Run(strategy, func(t *testing.T) {
			t.Parallel()

			layout, err := netlayout.Lookup(strategy)
			if err != nil {
				t.Fatalf("Lookup(%q) returned unexpected error: %v", strategy, err)
			}

			slots, err := layout.Slots("bogus", "10.0.0.0/22")
			if !errors.Is(err, netlayout.ErrUnknownRole) {
				t.Fatalf("Slots(\"bogus\", ...) error = %v, want wrapping ErrUnknownRole", err)
			}

			if !strings.Contains(err.Error(), "bogus") {
				t.Fatalf("Slots(\"bogus\", ...) error %q does not mention the offending role", err.Error())
			}

			if slots != (netlayout.InfraSlots{}) {
				t.Fatalf("Slots(\"bogus\", ...) slots = %+v, want zero value on error", slots)
			}
		})
	}
}
