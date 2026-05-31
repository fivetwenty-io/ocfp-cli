package pve

import (
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// TestFlavorArtifacts_DefaultRAM4GB pins the artifacts preset to 4 GiB so a
// future tweak does not silently regress the default the operator sees.
func TestFlavorArtifacts_DefaultRAM4GB(t *testing.T) {
	t.Parallel()

	const wantMiB = 4096

	if flavorArtifactsRAM != wantMiB {
		t.Errorf("flavorArtifactsRAM: got %d, want %d (4 GiB in MiB)", flavorArtifactsRAM, wantMiB)
	}

	f, ok := flavorPresets["artifacts"]
	if !ok {
		t.Fatal(`flavorPresets["artifacts"] missing`)
	}

	if f.RAM != wantMiB {
		t.Errorf(`flavorPresets["artifacts"].RAM: got %d, want %d`, f.RAM, wantMiB)
	}

	if !strings.Contains(f.Description, "4GB RAM") {
		t.Errorf("Description %q should mention 4GB RAM", f.Description)
	}
}

// TestFlavorArtifacts_DefaultCPU2 pins the artifacts preset to 2 vCPUs.
// RustFS is memory-bound, not CPU-bound: a single-node artifacts VM serves
// low-concurrency BOSH release/stemcell uploads and pulls, so 2 vCPUs is
// the right baseline. Operators that need more can override via
// config.Artifacts.CPU.
func TestFlavorArtifacts_DefaultCPU2(t *testing.T) {
	t.Parallel()

	const wantCPU = 2

	if flavorArtifactsCPU != wantCPU {
		t.Errorf("flavorArtifactsCPU: got %d, want %d", flavorArtifactsCPU, wantCPU)
	}

	f, ok := flavorPresets["artifacts"]
	if !ok {
		t.Fatal(`flavorPresets["artifacts"] missing`)
	}

	if f.VCPUs != wantCPU {
		t.Errorf(`flavorPresets["artifacts"].VCPUs: got %d, want %d`, f.VCPUs, wantCPU)
	}

	if !strings.Contains(f.Description, "2 vCPUs") {
		t.Errorf("Description %q should mention 2 vCPUs", f.Description)
	}
}

// TestFlavorArtifacts_CPUOverride confirms an operator-supplied CPU value
// (config.Artifacts.CPU → InstanceRequest.VCPUsOverride) replaces the
// preset default without mutating the shared preset.
func TestFlavorArtifacts_CPUOverride(t *testing.T) {
	t.Parallel()

	base := flavorPresets["artifacts"]
	presetCPU := base.VCPUs

	got := effectiveFlavor(base, &cpi.InstanceRequest{VCPUsOverride: 6})
	if got.VCPUs != 6 {
		t.Errorf("override VCPUs: got %d, want 6", got.VCPUs)
	}

	if base.VCPUs != presetCPU {
		t.Errorf("preset mutated: got %d, want %d", base.VCPUs, presetCPU)
	}
}

// TestEffectiveFlavor_NoOverrides returns the original preset pointer when no
// per-request overrides are set, avoiding an allocation in the common path.
func TestEffectiveFlavor_NoOverrides(t *testing.T) {
	t.Parallel()

	base := &cpi.Flavor{ID: "x", VCPUs: 4, RAM: 4096, Disk: 50}
	got := effectiveFlavor(base, &cpi.InstanceRequest{})

	if got != base {
		t.Errorf("expected same pointer when no overrides set")
	}
}

// TestEffectiveFlavor_OverridesCopy applies CPU and Memory overrides without
// mutating the underlying preset (which is package-global and shared).
func TestEffectiveFlavor_OverridesCopy(t *testing.T) {
	t.Parallel()

	base := &cpi.Flavor{ID: "artifacts", VCPUs: 4, RAM: 4096, Disk: 50}
	req := &cpi.InstanceRequest{VCPUsOverride: 8, MemoryMiBOverride: 16384}

	got := effectiveFlavor(base, req)
	if got == base {
		t.Fatal("expected a copy when overrides set; got same pointer")
	}

	if got.VCPUs != 8 {
		t.Errorf("VCPUs: got %d, want 8", got.VCPUs)
	}

	if got.RAM != 16384 {
		t.Errorf("RAM: got %d, want 16384", got.RAM)
	}

	if got.Disk != 50 {
		t.Errorf("Disk leaked override change: got %d, want 50", got.Disk)
	}

	if base.VCPUs != 4 || base.RAM != 4096 {
		t.Errorf("base preset was mutated: %+v", base)
	}
}

// TestEffectiveFlavor_PartialOverride keeps preset values for any field whose
// override is zero.
func TestEffectiveFlavor_PartialOverride(t *testing.T) {
	t.Parallel()

	base := &cpi.Flavor{ID: "artifacts", VCPUs: 4, RAM: 4096, Disk: 50}

	cpuOnly := effectiveFlavor(base, &cpi.InstanceRequest{VCPUsOverride: 6})
	if cpuOnly.VCPUs != 6 || cpuOnly.RAM != 4096 {
		t.Errorf("CPU-only override: got vcpus=%d ram=%d, want 6/4096", cpuOnly.VCPUs, cpuOnly.RAM)
	}

	memOnly := effectiveFlavor(base, &cpi.InstanceRequest{MemoryMiBOverride: 8192})
	if memOnly.VCPUs != 4 || memOnly.RAM != 8192 {
		t.Errorf("memory-only override: got vcpus=%d ram=%d, want 4/8192", memOnly.VCPUs, memOnly.RAM)
	}
}
