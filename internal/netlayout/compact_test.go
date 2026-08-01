package netlayout_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
)

// TestCompactWorkloadTable proves compact's WorkloadTable/SchemeVersion are
// real (not stub-safe placeholders): the assignment table carries the
// expected named-static offsets and range specs, mgmt matches wide's
// numbering exactly, and cidr is ignored per the strategy's documented
// "same table on every workload subnet" contract.
func TestCompactWorkloadTable(t *testing.T) {
	t.Parallel()

	compact, err := netlayout.Lookup("compact")
	if err != nil {
		t.Fatalf("Lookup(\"compact\") returned unexpected error: %v", err)
	}

	t.Run("SchemeVersionIsReal", func(t *testing.T) {
		t.Parallel()

		if got, want := compact.SchemeVersion(), "3-compact"; got != want {
			t.Fatalf("SchemeVersion() = %q, want %q", got, want)
		}
	})

	t.Run("WorkloadTableReturnsRealAssignments", func(t *testing.T) {
		t.Parallel()

		table, err := compact.WorkloadTable("10.64.64.0/26")
		if err != nil {
			t.Fatalf("WorkloadTable() returned unexpected error: %v", err)
		}

		bastion, ok := table["bastion"]["mgmt"]
		if !ok {
			t.Fatal("WorkloadTable() missing bastion/mgmt assignment")
		}

		if bastion.Offset != 3 {
			t.Fatalf("bastion/mgmt offset = %d, want 3 (matches wide)", bastion.Offset)
		}

		mgmtBosh, ok := table["bosh"]["mgmt"]
		if !ok {
			t.Fatal("WorkloadTable() missing bosh/mgmt assignment")
		}

		if mgmtBosh.Offset != 4 {
			t.Fatalf("bosh/mgmt offset = %d, want 4 (matches wide)", mgmtBosh.Offset)
		}

		ocfBosh, ok := table["bosh"]["ocf"]
		if !ok {
			t.Fatal("WorkloadTable() missing bosh/ocf assignment")
		}

		if ocfBosh.Offset != 23 {
			t.Fatalf("bosh/ocf offset = %d, want 23", ocfBosh.Offset)
		}

		ocfVault, ok := table["vault"]["ocf"]
		if !ok {
			t.Fatal("WorkloadTable() missing vault/ocf assignment")
		}

		if ocfVault.Offset != 24 {
			t.Fatalf("vault/ocf offset = %d, want 24", ocfVault.Offset)
		}

		ocfJumpbox, ok := table["jumpbox"]["ocf"]
		if !ok {
			t.Fatal("WorkloadTable() missing jumpbox/ocf assignment")
		}

		if ocfJumpbox.Offset != 25 {
			t.Fatalf("jumpbox/ocf offset = %d, want 25", ocfJumpbox.Offset)
		}

		ocfBlacksmith, ok := table["blacksmith"]["ocf"]
		if !ok {
			t.Fatal("WorkloadTable() missing blacksmith/ocf assignment")
		}

		if ocfBlacksmith.Offset != 26 {
			t.Fatalf("blacksmith/ocf offset = %d, want 26", ocfBlacksmith.Offset)
		}

		haproxy, ok := table["haproxy"]["ocf"]
		if !ok {
			t.Fatal("WorkloadTable() missing haproxy/ocf assignment")
		}

		if haproxy.Offset != 37 {
			t.Fatalf("haproxy/ocf offset = %d, want 37 (ocf available band start + 1)", haproxy.Offset)
		}

		mgmtAvailable, ok := table["available"]["mgmt"]
		if !ok {
			t.Fatal("WorkloadTable() missing available/mgmt range")
		}

		if mgmtAvailable.RangeSpec != "28-35" {
			t.Fatalf("available/mgmt range = %q, want %q", mgmtAvailable.RangeSpec, "28-35")
		}

		ocfAvailable, ok := table["available"]["ocf"]
		if !ok {
			t.Fatal("WorkloadTable() missing available/ocf range")
		}

		if ocfAvailable.RangeSpec != "36->" {
			t.Fatalf("available/ocf range = %q, want %q", ocfAvailable.RangeSpec, "36->")
		}

		mgmtReserved, ok := table["reserved"]["mgmt"]
		if !ok {
			t.Fatal("WorkloadTable() missing reserved/mgmt range")
		}

		if mgmtReserved.RangeSpec != "0-27,36->" {
			t.Fatalf("reserved/mgmt range = %q, want %q", mgmtReserved.RangeSpec, "0-27,36->")
		}

		ocfReserved, ok := table["reserved"]["ocf"]
		if !ok {
			t.Fatal("WorkloadTable() missing reserved/ocf range")
		}

		if ocfReserved.RangeSpec != "0-35" {
			t.Fatalf("reserved/ocf range = %q, want %q", ocfReserved.RangeSpec, "0-35")
		}
	})

	t.Run("CIDRIsIgnored", func(t *testing.T) {
		t.Parallel()

		first, err := compact.WorkloadTable("10.0.0.0/26")
		if err != nil {
			t.Fatalf("WorkloadTable() returned unexpected error: %v", err)
		}

		second, err := compact.WorkloadTable("192.168.0.0/24")
		if err != nil {
			t.Fatalf("WorkloadTable() returned unexpected error: %v", err)
		}

		if first["bastion"]["mgmt"].Offset != second["bastion"]["mgmt"].Offset {
			t.Fatalf("WorkloadTable() varied with cidr: %d != %d",
				first["bastion"]["mgmt"].Offset, second["bastion"]["mgmt"].Offset)
		}
	})
}

// TestCompactValidateSubnet proves compact's ValidateSubnet/MinPrefix are
// real (not stub-safe placeholders): a subnet too small to hold the
// strategy's highest fixed offset (37, ocf haproxy) is rejected with a
// wrapped ErrSubnetTooSmall naming the strategy, the offending CIDR, its
// prefix, the minimum prefix, and the highest offset.
func TestCompactValidateSubnet(t *testing.T) {
	t.Parallel()

	compact, err := netlayout.Lookup("compact")
	if err != nil {
		t.Fatalf("Lookup(\"compact\") returned unexpected error: %v", err)
	}

	t.Run("MinPrefixIs26", func(t *testing.T) {
		t.Parallel()

		if got, want := compact.MinPrefix(), 26; got != want {
			t.Fatalf("MinPrefix() = %d, want %d", got, want)
		}
	})

	t.Run("RejectsSlash27", func(t *testing.T) {
		t.Parallel()

		const cidr = "10.64.64.0/27"

		err := compact.ValidateSubnet(cidr)
		if !errors.Is(err, netlayout.ErrSubnetTooSmall) {
			t.Fatalf("ValidateSubnet(%q) error = %v, want wrapping ErrSubnetTooSmall", cidr, err)
		}

		for _, want := range []string{"compact", cidr, "/27", "/26", "37"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("ValidateSubnet(%q) error %q does not mention %q", cidr, err.Error(), want)
			}
		}
	})

	t.Run("AcceptsSlash26", func(t *testing.T) {
		t.Parallel()

		if err := compact.ValidateSubnet("10.64.64.0/26"); err != nil {
			t.Fatalf("ValidateSubnet(/26) returned unexpected error: %v", err)
		}
	})

	t.Run("AcceptsSlash22", func(t *testing.T) {
		t.Parallel()

		if err := compact.ValidateSubnet("10.64.64.0/22"); err != nil {
			t.Fatalf("ValidateSubnet(/22) returned unexpected error: %v", err)
		}
	})

	t.Run("MalformedCIDRErrors", func(t *testing.T) {
		t.Parallel()

		if err := compact.ValidateSubnet("not-a-cidr"); err == nil {
			t.Fatal("ValidateSubnet(\"not-a-cidr\") = nil error, want error")
		}
	})
}
