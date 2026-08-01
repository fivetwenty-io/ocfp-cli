package netlayout_test

import (
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
