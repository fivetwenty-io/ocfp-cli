package artifacts

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/ownedvm"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/pve"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// pveCompute filters the way PVE's ListInstances does: "name" must equal the
// VM's name, each "label.<key>" filter goes through PVE's own matcher, and
// every other key is ignored.
type pveCompute struct {
	cpi.ComputeManager
	vms []*cpi.Instance
}

func (c pveCompute) ListInstances(_ context.Context, filters map[string]string) ([]*cpi.Instance, error) {
	var out []*cpi.Instance

	for _, vm := range c.vms {
		if name, ok := filters["name"]; ok && vm.Name != name {
			continue
		}

		tags := make([]string, 0, len(vm.Tags))
		for tag := range vm.Tags {
			tags = append(tags, tag)
		}

		if pve.MatchesLabelFilters(tags, filters) {
			out = append(out, vm)
		}
	}

	return out, nil
}

type pveProvider struct {
	cpi.Provider
	compute pveCompute
}

func (p pveProvider) ComputeManager() cpi.ComputeManager { return p.compute }

func pveVM(id, name, bloc string) *cpi.Instance {
	tags := map[string]string{}
	for k, v := range map[string]string{"ocfp:role": "artifacts", "ocfp:bloc": bloc} {
		tags[pve.FormatTag(k, v)] = ""
	}

	return &cpi.Instance{ID: id, Name: name, Tags: tags}
}

func emptyState(t *testing.T) *state.Manager {
	t.Helper()

	sm, err := state.NewManager(t.TempDir())
	require.NoError(t, err)

	return sm
}

func TestLookup_PVESkipsOtherBlocsVM(t *testing.T) {
	t.Parallel()

	p := pveProvider{compute: pveCompute{vms: []*cpi.Instance{
		pveVM("101", "lab-artifacts", "other"),
		pveVM("102", "lab-artifacts", "lab"),
	}}}

	got, err := Lookup(context.Background(), emptyState(t), p, "lab")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "102", got.VMID)
}

func TestLookup_PVENoOwnTaggedVM(t *testing.T) {
	t.Parallel()

	p := pveProvider{compute: pveCompute{vms: []*cpi.Instance{
		pveVM("101", "lab-artifacts", "other"),
	}}}

	got, err := Lookup(context.Background(), emptyState(t), p, "lab")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestLookup_PVEAmbiguousIsAnError(t *testing.T) {
	t.Parallel()

	p := pveProvider{compute: pveCompute{vms: []*cpi.Instance{
		pveVM("101", "lab-artifacts", "lab"),
		pveVM("102", "lab-artifacts", "lab"),
	}}}

	got, err := Lookup(context.Background(), emptyState(t), p, "lab")
	require.ErrorIs(t, err, ownedvm.ErrAmbiguous)
	assert.Nil(t, got)
}
