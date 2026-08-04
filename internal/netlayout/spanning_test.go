package netlayout_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
)

// TestSpanningWorkloadTable proves spanning's WorkloadTable/SchemeVersion
// are real (not stub-safe placeholders): pinned statics compile to a
// SubnetMapping assignment (not a plain Offset), unpinned statics still
// compile to a plain Offset, and the mgmt/ocf available bands match wide's
// numbering exactly (spanning reuses wide's offset catalog, only adding
// per-static subnet pinning).
func TestSpanningWorkloadTable(t *testing.T) {
	t.Parallel()

	spanning, err := netlayout.Lookup("spanning")
	if err != nil {
		t.Fatalf("Lookup(\"spanning\") returned unexpected error: %v", err)
	}

	t.Run("SchemeVersionIsReal", func(t *testing.T) {
		t.Parallel()

		if got, want := spanning.SchemeVersion(), "4-spanning"; got != want {
			t.Fatalf("SchemeVersion() = %q, want %q", got, want)
		}
	})

	t.Run("PlacementIsSpanning", func(t *testing.T) {
		t.Parallel()

		if got := spanning.Placement(); got != netlayout.PlacementSpanning {
			t.Fatalf("Placement() = %q, want %q", got, netlayout.PlacementSpanning)
		}
	})

	t.Run("MinSubnetsIsThree", func(t *testing.T) {
		t.Parallel()

		if got, want := spanning.MinSubnets(), 3; got != want {
			t.Fatalf("MinSubnets() = %d, want %d", got, want)
		}
	})

	t.Run("PinnedStaticIsSubnetMapping", func(t *testing.T) {
		t.Parallel()

		table, err := spanning.WorkloadTable("10.4.4.0/22")
		if err != nil {
			t.Fatalf("WorkloadTable() returned unexpected error: %v", err)
		}

		bastion, ok := table["bastion"]["mgmt"]
		if !ok {
			t.Fatal("WorkloadTable() missing bastion/mgmt assignment")
		}

		if bastion.Offset != 0 {
			t.Fatalf("bastion/mgmt Offset = %d, want 0 (pinned static compiles to SubnetMapping, not Offset)", bastion.Offset)
		}

		subnets, ok := bastion.SubnetMapping[3]
		if !ok {
			t.Fatalf("bastion/mgmt SubnetMapping = %+v, want an entry for offset 3", bastion.SubnetMapping)
		}

		if len(subnets) != 1 || subnets[0] != 0 {
			t.Fatalf("bastion/mgmt SubnetMapping[3] = %v, want [0]", subnets)
		}
	})

	t.Run("UnpinnedStaticIsOffset", func(t *testing.T) {
		t.Parallel()

		table, err := spanning.WorkloadTable("10.4.4.0/22")
		if err != nil {
			t.Fatalf("WorkloadTable() returned unexpected error: %v", err)
		}

		vault, ok := table["vault"]["mgmt"]
		if !ok {
			t.Fatal("WorkloadTable() missing vault/mgmt assignment")
		}

		if vault.Offset != 5 {
			t.Fatalf("vault/mgmt Offset = %d, want 5 (unpinned static)", vault.Offset)
		}
	})

	t.Run("UnpinnedSmokeStaticsKeepIPKey", func(t *testing.T) {
		t.Parallel()

		table, err := spanning.WorkloadTable("10.4.4.0/22")
		if err != nil {
			t.Fatalf("WorkloadTable() returned unexpected error: %v", err)
		}

		rustfsSmoke, ok := table["rustfs_smoke"]["mgmt"]
		if !ok {
			t.Fatal("WorkloadTable() missing rustfs_smoke/mgmt assignment")
		}

		if rustfsSmoke.Offset != 21 || rustfsSmoke.IPKey != "rustfs_ip_smoke" {
			t.Fatalf("rustfs_smoke/mgmt = %+v, want Offset 21 IPKey rustfs_ip_smoke", rustfsSmoke)
		}

		garageSmoke, ok := table["garage_smoke"]["mgmt"]
		if !ok {
			t.Fatal("WorkloadTable() missing garage_smoke/mgmt assignment")
		}

		if garageSmoke.Offset != 22 || garageSmoke.IPKey != "garage_ip_smoke" {
			t.Fatalf("garage_smoke/mgmt = %+v, want Offset 22 IPKey garage_ip_smoke", garageSmoke)
		}
	})

	t.Run("MgmtAvailableMatchesWide", func(t *testing.T) {
		t.Parallel()

		table, err := spanning.WorkloadTable("10.4.4.0/22")
		if err != nil {
			t.Fatalf("WorkloadTable() returned unexpected error: %v", err)
		}

		mgmtAvailable, ok := table["available"]["mgmt"]
		if !ok {
			t.Fatal("WorkloadTable() missing available/mgmt range")
		}

		if mgmtAvailable.RangeSpec != "32-63" {
			t.Fatalf("available/mgmt range = %q, want %q", mgmtAvailable.RangeSpec, "32-63")
		}
	})
}

// TestSpanningValidateSubnet proves spanning's ValidateSubnet/MinPrefix
// reject a subnet too small for the highest fixed offset (97, ocf
// haproxy), matching wide's own /25 floor since spanning reuses wide's
// offset catalog.
func TestSpanningValidateSubnet(t *testing.T) {
	t.Parallel()

	spanning, err := netlayout.Lookup("spanning")
	if err != nil {
		t.Fatalf("Lookup(\"spanning\") returned unexpected error: %v", err)
	}

	t.Run("MinPrefixIs25", func(t *testing.T) {
		t.Parallel()

		if got, want := spanning.MinPrefix(), 25; got != want {
			t.Fatalf("MinPrefix() = %d, want %d", got, want)
		}
	})

	t.Run("RejectsSlash26", func(t *testing.T) {
		t.Parallel()

		const cidr = "10.4.4.0/26"

		err := spanning.ValidateSubnet(cidr)
		if !errors.Is(err, netlayout.ErrSubnetTooSmall) {
			t.Fatalf("ValidateSubnet(%q) error = %v, want wrapping ErrSubnetTooSmall", cidr, err)
		}
	})

	t.Run("AcceptsSlash22", func(t *testing.T) {
		t.Parallel()

		if err := spanning.ValidateSubnet("10.4.4.0/22"); err != nil {
			t.Fatalf("ValidateSubnet(/22) returned unexpected error: %v", err)
		}
	})
}

// TestSpanningLayerASlots proves the ocfp role's Layer A slots track each
// static's own pinning: a static pinned to subnet 0 (bastion) is named only
// at idx 0, a static pinned to subnet 1 (doomsday/shout) is named only at
// idx 1, and an unpinned static (vault) is named on every index — the
// spanning built-in is the first strategy to exercise the pinned branch of
// ocfpLayerASlots through the registry, not a hand-built test Definition.
func TestSpanningLayerASlots(t *testing.T) {
	t.Parallel()

	spanning, err := netlayout.Lookup("spanning")
	if err != nil {
		t.Fatalf("Lookup(\"spanning\") returned unexpected error: %v", err)
	}

	const cidr = "10.4.4.0/22"

	t.Run("Idx0HasBastionPinnedToSubnetZero", func(t *testing.T) {
		t.Parallel()

		slots, err := spanning.LayerASlots("ocfp", cidr, 0)
		if err != nil {
			t.Fatalf("LayerASlots(idx 0) returned unexpected error: %v", err)
		}

		if !hasNamedKeyOffset(slots.Named, "bastion_ip", 3) {
			t.Fatalf("LayerASlots(idx 0) Named = %+v, want bastion_ip at offset 3", slots.Named)
		}
	})

	t.Run("Idx1HasDoomsdayShoutNotBastion", func(t *testing.T) {
		t.Parallel()

		slots, err := spanning.LayerASlots("ocfp", cidr, 1)
		if err != nil {
			t.Fatalf("LayerASlots(idx 1) returned unexpected error: %v", err)
		}

		if !hasNamedKeyOffset(slots.Named, "doomsday_ip", 18) {
			t.Fatalf("LayerASlots(idx 1) Named = %+v, want doomsday_ip at offset 18", slots.Named)
		}

		if !hasNamedKeyOffset(slots.Named, "shout_ip", 19) {
			t.Fatalf("LayerASlots(idx 1) Named = %+v, want shout_ip at offset 19", slots.Named)
		}

		for _, n := range slots.Named {
			if n.Key == "bastion_ip" {
				t.Fatalf("LayerASlots(idx 1) Named = %+v, want bastion_ip absent (pinned to subnet 0)", slots.Named)
			}
		}
	})

	t.Run("VaultPresentOnEveryIndex", func(t *testing.T) {
		t.Parallel()

		for idx := 0; idx < 3; idx++ {
			slots, err := spanning.LayerASlots("ocfp", cidr, idx)
			if err != nil {
				t.Fatalf("LayerASlots(idx %d) returned unexpected error: %v", idx, err)
			}

			if !hasNamedKeyOffset(slots.Named, "vault_ip", 5) {
				t.Fatalf("LayerASlots(idx %d) Named = %+v, want vault_ip at offset 5 (unpinned)", idx, slots.Named)
			}
		}
	})
}

// hasNamedKeyOffset reports whether named contains a NamedSlot with the
// given key AND offset (a stricter check than the package's existing
// hasNamedKey, which only checks presence).
func hasNamedKeyOffset(named []netlayout.NamedSlot, key string, offset int) bool {
	for _, n := range named {
		if n.Key == key && n.Offset == offset {
			return true
		}
	}

	return false
}

// TestSpanningRegistered proves the spanning strategy is reachable through
// the same registry every other built-in strategy uses — Lookup by name,
// and membership in Names().
func TestSpanningRegistered(t *testing.T) {
	t.Parallel()

	if _, err := netlayout.Lookup("spanning"); err != nil {
		t.Fatalf("Lookup(\"spanning\") returned unexpected error: %v", err)
	}

	found := false

	for _, name := range netlayout.Names() {
		if name == "spanning" {
			found = true

			break
		}
	}

	if !found {
		t.Fatalf("Names() = %v, want it to contain %q", netlayout.Names(), "spanning")
	}

	if !strings.Contains(strings.Join(netlayout.Names(), ","), "spanning") {
		t.Fatal("Names() joined does not contain \"spanning\"")
	}
}
