package netlayout_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
)

// TestWideWorkloadTable proves wide's WorkloadTable/SchemeVersion are real
// (not stub-safe placeholders): the assignment table carries the expected
// named-static offsets and range specs, and cidr is ignored per the
// strategy's documented "same table on every workload subnet" contract.
func TestWideWorkloadTable(t *testing.T) {
	t.Parallel()

	wide, err := netlayout.Lookup("wide")
	if err != nil {
		t.Fatalf("Lookup(\"wide\") returned unexpected error: %v", err)
	}

	t.Run("SchemeVersionIsReal", func(t *testing.T) {
		t.Parallel()

		if got, want := wide.SchemeVersion(), "2"; got != want {
			t.Fatalf("SchemeVersion() = %q, want %q", got, want)
		}
	})

	t.Run("WorkloadTableReturnsRealAssignments", func(t *testing.T) {
		t.Parallel()

		table, err := wide.WorkloadTable("10.64.64.0/22")
		if err != nil {
			t.Fatalf("WorkloadTable() returned unexpected error: %v", err)
		}

		bastion, ok := table["bastion"]["mgmt"]
		if !ok {
			t.Fatal("WorkloadTable() missing bastion/mgmt assignment")
		}

		if bastion.Offset != 3 {
			t.Fatalf("bastion/mgmt offset = %d, want 3", bastion.Offset)
		}

		mgmtBosh, ok := table["bosh"]["mgmt"]
		if !ok {
			t.Fatal("WorkloadTable() missing bosh/mgmt assignment")
		}

		if mgmtBosh.Offset != 4 {
			t.Fatalf("bosh/mgmt offset = %d, want 4", mgmtBosh.Offset)
		}

		ocfBosh, ok := table["bosh"]["ocf"]
		if !ok {
			t.Fatal("WorkloadTable() missing bosh/ocf assignment")
		}

		if ocfBosh.Offset != 64 {
			t.Fatalf("bosh/ocf offset = %d, want 64", ocfBosh.Offset)
		}

		haproxy, ok := table["haproxy"]["ocf"]
		if !ok {
			t.Fatal("WorkloadTable() missing haproxy/ocf assignment")
		}

		if haproxy.Offset != 97 {
			t.Fatalf("haproxy/ocf offset = %d, want 97 (ocf available band start + 1)", haproxy.Offset)
		}

		mgmtAvailable, ok := table["available"]["mgmt"]
		if !ok {
			t.Fatal("WorkloadTable() missing available/mgmt range")
		}

		if mgmtAvailable.RangeSpec != "32-63" {
			t.Fatalf("available/mgmt range = %q, want %q", mgmtAvailable.RangeSpec, "32-63")
		}
	})

	t.Run("CIDRIsIgnored", func(t *testing.T) {
		t.Parallel()

		first, err := wide.WorkloadTable("10.0.0.0/22")
		if err != nil {
			t.Fatalf("WorkloadTable() returned unexpected error: %v", err)
		}

		second, err := wide.WorkloadTable("192.168.0.0/24")
		if err != nil {
			t.Fatalf("WorkloadTable() returned unexpected error: %v", err)
		}

		if first["bastion"]["mgmt"].Offset != second["bastion"]["mgmt"].Offset {
			t.Fatalf("WorkloadTable() varied with cidr: %d != %d",
				first["bastion"]["mgmt"].Offset, second["bastion"]["mgmt"].Offset)
		}
	})
}

// TestWideValidateSubnet_RejectsSubnetSmallerThan25 proves wide's
// ValidateSubnet/MinPrefix are real (not stub-safe placeholders): a subnet
// too small to hold the strategy's highest fixed offset (97, ocf haproxy)
// is rejected with a wrapped ErrSubnetTooSmall naming the strategy, the
// offending CIDR, its prefix, the minimum prefix, and the highest offset.
func TestWideValidateSubnet_RejectsSubnetSmallerThan25(t *testing.T) {
	t.Parallel()

	wide, err := netlayout.Lookup("wide")
	if err != nil {
		t.Fatalf("Lookup(\"wide\") returned unexpected error: %v", err)
	}

	t.Run("MinPrefixIs25", func(t *testing.T) {
		t.Parallel()

		if got, want := wide.MinPrefix(), 25; got != want {
			t.Fatalf("MinPrefix() = %d, want %d", got, want)
		}
	})

	t.Run("RejectsSlash26", func(t *testing.T) {
		t.Parallel()

		const cidr = "10.64.64.0/26"

		err := wide.ValidateSubnet(cidr)
		if !errors.Is(err, netlayout.ErrSubnetTooSmall) {
			t.Fatalf("ValidateSubnet(%q) error = %v, want wrapping ErrSubnetTooSmall", cidr, err)
		}

		for _, want := range []string{"wide", cidr, "/26", "/25", "97"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("ValidateSubnet(%q) error %q does not mention %q", cidr, err.Error(), want)
			}
		}
	})

	t.Run("AcceptsSlash25", func(t *testing.T) {
		t.Parallel()

		if err := wide.ValidateSubnet("10.64.64.0/25"); err != nil {
			t.Fatalf("ValidateSubnet(/25) returned unexpected error: %v", err)
		}
	})

	t.Run("AcceptsSlash22", func(t *testing.T) {
		t.Parallel()

		if err := wide.ValidateSubnet("10.64.64.0/22"); err != nil {
			t.Fatalf("ValidateSubnet(/22) returned unexpected error: %v", err)
		}
	})

	t.Run("MalformedCIDRErrors", func(t *testing.T) {
		t.Parallel()

		if err := wide.ValidateSubnet("not-a-cidr"); err == nil {
			t.Fatal("ValidateSubnet(\"not-a-cidr\") = nil error, want error")
		}
	})
}
