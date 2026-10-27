package ownedvm_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/ownedvm"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/pve"
)

// pveLister filters the way PVE's ListInstances does.
type pveLister struct{ vms []*cpi.Instance }

func (l pveLister) ListInstances(_ context.Context, filters map[string]string) ([]*cpi.Instance, error) {
	var out []*cpi.Instance

	for _, vm := range l.vms {
		if name, ok := filters["name"]; ok && vm.Name != name {
			continue
		}

		var tags []string
		for tag := range vm.Tags {
			tags = append(tags, tag)
		}

		if pve.MatchesLabelFilters(tags, filters) {
			out = append(out, vm)
		}
	}

	return out, nil
}

func pveVM(id, name string, tags map[string]string) *cpi.Instance {
	t := map[string]string{}
	for k, v := range tags {
		t[pve.FormatTag(k, v)] = ""
	}

	return &cpi.Instance{ID: id, Name: name, Tags: t}
}

func TestFind_OnlyOwnTags(t *testing.T) {
	tags := map[string]string{"bloc": "a", "managed-by": "ocfp"}
	l := pveLister{vms: []*cpi.Instance{
		pveVM("1", "a-bastion", map[string]string{"bloc": "b", "managed-by": "ocfp"}),
		pveVM("2", "a-bastion", tags),
	}}

	got, err := ownedvm.Find(context.Background(), l, "a-bastion", tags)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "2", got.ID)
}

func TestFind_Ambiguous(t *testing.T) {
	tags := map[string]string{"bloc": "a"}
	l := pveLister{vms: []*cpi.Instance{pveVM("1", "n", tags), pveVM("2", "n", tags)}}

	_, err := ownedvm.Find(context.Background(), l, "n", tags)
	require.ErrorIs(t, err, ownedvm.ErrAmbiguous)
}

func TestFindUntagged(t *testing.T) {
	tags := map[string]string{"bloc": "a", "managed-by": "ocfp"}
	l := pveLister{vms: []*cpi.Instance{
		pveVM("1", "a-bastion", map[string]string{"bloc": "b"}),
		pveVM("2", "a-bastion", nil),
	}}

	got, err := ownedvm.FindUntagged(context.Background(), l, "a-bastion", tags)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "2", got.ID)
}
