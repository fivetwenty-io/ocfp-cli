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
