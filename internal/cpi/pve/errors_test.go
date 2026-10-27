package pve

import (
	"errors"
	"testing"
)

func TestErrVMIDNotFound_IsClassifiedAsVMNotFound(t *testing.T) {
	t.Parallel()

	err := ErrVMIDNotFound(100)

	if !errors.Is(err, ErrVMNotFound) {
		t.Fatalf("errors.Is(%v, ErrVMNotFound) = false, want true", err)
	}

	if got, want := err.Error(), "pve: VM with ID 100 not found"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}

	if errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("errors.Is(%v, ErrNodeNotFound) = true, want false", err)
	}
}

func TestFormatTag_MatchesTheTagSetVMTagsWrites(t *testing.T) {
	t.Parallel()

	if got, want := FormatTag("ocfp:bloc", "Lab"), "ocfp_bloc--lab"; got != want {
		t.Fatalf("FormatTag = %q, want %q", got, want)
	}
}

func TestMatchesLabelFilters_IgnoresKeysWithoutTheLabelPrefix(t *testing.T) {
	t.Parallel()

	vmTags := []string{FormatTag("bloc", "other")}

	if !MatchesLabelFilters(vmTags, map[string]string{"bloc": "lab"}) {
		t.Fatal("an unprefixed key is not a label filter, so it must not exclude the VM")
	}

	if MatchesLabelFilters(vmTags, map[string]string{"label.bloc": "lab"}) {
		t.Fatal("label.bloc=lab must not match a VM tagged for another bloc")
	}

	if !MatchesLabelFilters(vmTags, map[string]string{"label.bloc": "other"}) {
		t.Fatal("label.bloc=other must match the VM's own tag")
	}
}
