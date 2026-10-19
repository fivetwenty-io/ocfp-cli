package bootstrap

import (
	"context"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"testing"
)

type disklessCloudCompute struct {
	cpi.ComputeManager
	calls int
}

func (c *disklessCloudCompute) GetFlavor(_ context.Context, id string) (*cpi.Flavor, error) {
	c.calls++
	return &cpi.Flavor{ID: id, Disk: 0}, nil
}
func TestGCPDisklessFlavorsUseSeparateBootDisk(t *testing.T) {
	m := &Manager{options: &Options{Provider: "gcp"}}
	comp := &disklessCloudCompute{}
	id, ok := m.tryGetFlavorDirect(context.Background(), comp, "e2-standard-2", 3)
	if !ok || id != "e2-standard-2" || comp.calls != 1 {
		t.Errorf("direct lookup: %s %v calls=%d", id, ok, comp.calls)
	}
	flavors := []*cpi.Flavor{{ID: "e2-standard-2", Disk: 0, VCPUs: 2, RAM: 8192}}
	if id, ok = m.findMatchingFlavor(flavors, "e2-standard-2", 3); !ok || id != "e2-standard-2" {
		t.Errorf("list match: %s %v", id, ok)
	}
	if got := m.selectSmallestSuitableFlavor(flavors, 3); got == nil {
		t.Error("diskless GCP flavor excluded")
	}
}
