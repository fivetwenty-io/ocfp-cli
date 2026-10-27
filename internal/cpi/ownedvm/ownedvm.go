// Package ownedvm finds a VM by name among those that carry a bloc's
// ownership tags, whichever provider holds it.
package ownedvm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/gcp"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/pve"
)

// ErrAmbiguous is returned when VMs with different ids match one name.
var ErrAmbiguous = errors.New("multiple VMs match")

// Lister is the one compute call the search needs.
type Lister interface {
	ListInstances(ctx context.Context, filters map[string]string) ([]*cpi.Instance, error)
}

// LabelFilters turns tags into the "label.<key>" filters every provider
// reads as a tag or label to match. PVE reads no other form.
func LabelFilters(tags map[string]string) map[string]string {
	filters := make(map[string]string, len(tags))
	for key, value := range tags {
		filters["label."+key] = value
	}

	return filters
}

// Find lists the VMs that carry tags and returns the one named vmName. A
// provider can return a VM the filters should have excluded, so each VM must
// carry the tags itself to count. It returns nil with no error when no VM
// matches, and ErrAmbiguous when VMs with different ids do.
func Find(ctx context.Context, lister Lister, vmName string, tags map[string]string) (*cpi.Instance, error) {
	instances, err := lister.ListInstances(ctx, LabelFilters(tags))
	if err != nil {
		return nil, err
	}

	return pick(instances, vmName, func(inst *cpi.Instance) bool { return CarriesTags(inst, tags) })
}

// FindUntagged returns the VM named vmName that carries none of the tag keys
// in tags, for a VM whose tagging failed after it was created. A VM that
// carries any of those keys, with any value, is treated as someone else's.
// It returns nil with no error when no VM matches, and ErrAmbiguous when VMs
// with different ids do.
func FindUntagged(ctx context.Context, lister Lister, vmName string, tags map[string]string) (*cpi.Instance, error) {
	instances, err := lister.ListInstances(ctx, map[string]string{"name": vmName})
	if err != nil {
		return nil, err
	}

	return pick(instances, vmName, func(inst *cpi.Instance) bool { return !HasAnyTagKey(inst, tags) })
}

func pick(instances []*cpi.Instance, vmName string, keep func(*cpi.Instance) bool) (*cpi.Instance, error) {
	var match *cpi.Instance

	for _, inst := range instances {
		if inst == nil || inst.Name != vmName || !keep(inst) {
			continue
		}

		if match != nil && match.ID != inst.ID {
			return nil, fmt.Errorf("%w %s", ErrAmbiguous, vmName)
		}

		match = inst
	}

	return match, nil
}

// CarriesTags reports whether inst carries every key=value in tags.
func CarriesTags(inst *cpi.Instance, tags map[string]string) bool {
	for key, value := range tags {
		if !hasTag(inst, key, value) {
			return false
		}
	}

	return true
}

// hasTag reports whether inst carries key=value in any form a provider
// returns tags in. AWS, Azure, and STACKIT keep the key and value as
// written. PVE turns the pair into one tag identifier with an empty value.
// GCP sanitizes both the key and the value of a label.
func hasTag(inst *cpi.Instance, key, value string) bool {
	if got, ok := inst.Tags[key]; ok && got == value {
		return true
	}

	if _, ok := inst.Tags[pve.FormatTag(key, value)]; ok {
		return true
	}

	got, ok := inst.Tags[gcp.SanitizeLabel(key)]

	return ok && got == gcp.SanitizeLabel(value)
}

// HasAnyTagKey reports whether inst carries any key of tags, with whatever
// value, in any form a provider returns tags in.
func HasAnyTagKey(inst *cpi.Instance, tags map[string]string) bool {
	for key := range tags {
		if _, ok := inst.Tags[key]; ok {
			return true
		}

		if _, ok := inst.Tags[gcp.SanitizeLabel(key)]; ok {
			return true
		}

		prefix := pve.FormatTag(key, "")

		for tag := range inst.Tags {
			if strings.HasPrefix(tag, prefix) {
				return true
			}
		}
	}

	return false
}
