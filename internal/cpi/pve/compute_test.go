package pve

import (
	"strings"
	"testing"
)

// T08 TestFlavorPreset_BOSH_SizingConstants verifies all three director
// constants match the lab spec (manifests/bosh/vars.yml).
func TestFlavorPreset_BOSH_SizingConstants(t *testing.T) {
	t.Parallel()

	if flavorBoshCPU != 8 {
		t.Errorf("flavorBoshCPU: got %d, want 8", flavorBoshCPU)
	}

	if flavorBoshRAM != 16384 {
		t.Errorf("flavorBoshRAM: got %d, want 16384 (MiB)", flavorBoshRAM)
	}

	if flavorBoshDisk != 131072 {
		t.Errorf("flavorBoshDisk: got %d, want 131072 (MiB)", flavorBoshDisk)
	}
}

// T08b TestFlavorBoshDirector_MemoryMiB asserts RAM constant is 16384 MiB
// (16 GiB). Separate from T08 so a future refactor of the constant group
// does not silently regress the unit.
func TestFlavorBoshDirector_MemoryMiB(t *testing.T) {
	t.Parallel()

	const wantMiB = 16384 // 16 GiB expressed in MiB

	if flavorBoshRAM != wantMiB {
		t.Errorf("flavorBoshRAM: got %d MiB, want %d MiB (16 GiB)", flavorBoshRAM, wantMiB)
	}
}

// T09 TestFlavorPreset_BOSH_DescriptionMatchesConstants verifies the human-
// readable Description in flavorPresets["bosh"] is consistent with the
// sizing constants so it does not drift after future constant updates.
func TestFlavorPreset_BOSH_DescriptionMatchesConstants(t *testing.T) {
	t.Parallel()

	f, ok := flavorPresets["bosh"]
	if !ok {
		t.Fatal("flavorPresets[\"bosh\"] not found")
	}

	if f.VCPUs != flavorBoshCPU {
		t.Errorf("bosh preset VCPUs: got %d, want %d", f.VCPUs, flavorBoshCPU)
	}

	if f.RAM != flavorBoshRAM {
		t.Errorf("bosh preset RAM: got %d, want %d", f.RAM, flavorBoshRAM)
	}

	if f.Disk != flavorBoshDisk {
		t.Errorf("bosh preset Disk: got %d, want %d", f.Disk, flavorBoshDisk)
	}

	desc := f.Description
	if !strings.Contains(desc, "8 vCPU") {
		t.Errorf("Description %q does not mention 8 vCPU", desc)
	}

	if !strings.Contains(desc, "16GB") {
		t.Errorf("Description %q does not mention 16GB RAM", desc)
	}

	if !strings.Contains(desc, "128GB") {
		t.Errorf("Description %q does not mention 128GB disk", desc)
	}
}

// TestFlavorBoshDirector_DiskUnitIsMiB asserts flavorBoshDisk == 131072
// (128 GiB expressed in MiB). TestFlavorPreset_BOSH_SizingConstants also
// covers this constant; this test is a focused pin for the disk value alone.
func TestFlavorBoshDirector_DiskUnitIsMiB(t *testing.T) {
	t.Parallel()

	const wantMiB = 131072

	if flavorBoshDisk != wantMiB {
		t.Errorf("flavorBoshDisk: got %d, want %d (128 GiB in MiB)", flavorBoshDisk, wantMiB)
	}
}
