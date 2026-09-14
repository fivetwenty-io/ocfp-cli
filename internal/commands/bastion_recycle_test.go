package commands

import (
	"errors"
	"strings"
	"testing"
)

// TestRecycleConfirmationRequired asserts a recycle cannot destroy anything
// without an explicit go-ahead.
//
// The operation passes a point of no return: once the original VM is
// destroyed, its boot disk and its snapshots go with it and the only route
// back is restoring an archive. That is not something to do on a mistyped
// command.
func TestRecycleConfirmationRequired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yes     bool
		abort   bool
		wantAsk bool
	}{
		{"a plain run asks", false, false, true},
		{"--yes proceeds", true, false, false},
		{"--abort never asks; it undoes rather than destroys", false, true, false},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := recycleNeedsConfirmation(tc.yes, tc.abort); got != tc.wantAsk {
				t.Errorf("recycleNeedsConfirmation(yes=%v, abort=%v) = %v, want %v",
					tc.yes, tc.abort, got, tc.wantAsk)
			}
		})
	}
}

// TestRecycleWarningNamesThePointOfNoReturn asserts the operator is told, in
// the confirmation, exactly what cannot be undone. A warning that does not say
// which step is irreversible is not a warning.
func TestRecycleWarningNamesThePointOfNoReturn(t *testing.T) {
	t.Parallel()

	warning := recycleWarning("my-bloc")

	for _, want := range []string{"my-bloc", "destroy", "snapshot", "cannot be undone"} {
		if !strings.Contains(strings.ToLower(warning), strings.ToLower(want)) {
			t.Errorf("the confirmation does not mention %q:\n%s", want, warning)
		}
	}
}

// TestRecycleRejectsUnknownAction keeps the dispatcher honest.
func TestRecycleRejectsUnknownAction(t *testing.T) {
	t.Parallel()

	err := ErrUnknownBastionAction("recycel")
	if err == nil {
		t.Fatal("expected an error for a misspelled action")
	}

	if !strings.Contains(err.Error(), "recycel") {
		t.Errorf("the error does not name what was typed: %v", err)
	}
}

// TestErrRecycleNeedsArchiveStorage asserts --archive without a storage name
// fails before anything is stopped, rather than halfway through.
func TestErrRecycleNeedsArchiveStorage(t *testing.T) {
	t.Parallel()

	err := validateRecycleFlags(true, "")
	if err == nil {
		t.Fatal("expected an error when --archive is given with no storage")
	}

	if !errors.Is(err, ErrArchiveStorageRequired) {
		t.Errorf("error = %v, want ErrArchiveStorageRequired", err)
	}

	if err := validateRecycleFlags(true, "nfs-backup"); err != nil {
		t.Errorf("unexpected error with a storage named: %v", err)
	}

	if err := validateRecycleFlags(false, ""); err != nil {
		t.Errorf("unexpected error without --archive: %v", err)
	}
}
