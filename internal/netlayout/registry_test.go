package netlayout_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
)

func TestRegistry(t *testing.T) {
	t.Parallel()

	t.Run("DefaultIsWide", func(t *testing.T) {
		t.Parallel()

		if got := netlayout.Default().Name(); got != "wide" {
			t.Fatalf("Default().Name() = %q, want %q", got, "wide")
		}
	})

	t.Run("LookupEmptyReturnsDefault", func(t *testing.T) {
		t.Parallel()

		layout, err := netlayout.Lookup("")
		if err != nil {
			t.Fatalf("Lookup(\"\") returned unexpected error: %v", err)
		}

		if got, want := layout.Name(), netlayout.Default().Name(); got != want {
			t.Fatalf("Lookup(\"\").Name() = %q, want %q", got, want)
		}
	})

	t.Run("LookupUnknownWrapsName", func(t *testing.T) {
		t.Parallel()

		_, err := netlayout.Lookup("bogus")
		if !errors.Is(err, netlayout.ErrUnknownStrategy) {
			t.Fatalf("Lookup(\"bogus\") error = %v, want wrapping ErrUnknownStrategy", err)
		}

		if !strings.Contains(err.Error(), "bogus") {
			t.Fatalf("Lookup(\"bogus\") error %q does not mention the offending name", err.Error())
		}
	})

	t.Run("NamesSortedBothRegistered", func(t *testing.T) {
		t.Parallel()

		want := []string{"compact", "wide"}
		got := netlayout.Names()

		if len(got) != len(want) {
			t.Fatalf("Names() = %v, want %v", got, want)
		}

		for i, name := range want {
			if got[i] != name {
				t.Fatalf("Names() = %v, want %v", got, want)
			}
		}
	})
}

// TestCompactStub proves the stub-safety claim in the package doc: an
// accidental early call into compact's not-yet-implemented methods fails
// loudly with netlayout.ErrNotImplemented rather than panicking or
// returning a silently-wrong zero value.
func TestCompactStub(t *testing.T) {
	t.Parallel()

	compact, err := netlayout.Lookup("compact")
	if err != nil {
		t.Fatalf("Lookup(\"compact\") returned unexpected error: %v", err)
	}

	t.Run("WorkloadTableReturnsErrNotImplemented", func(t *testing.T) {
		t.Parallel()

		table, err := compact.WorkloadTable("10.0.0.0/26")
		if !errors.Is(err, netlayout.ErrNotImplemented) {
			t.Fatalf("WorkloadTable() error = %v, want ErrNotImplemented", err)
		}

		if table != nil {
			t.Fatalf("WorkloadTable() table = %v, want nil on stub error", table)
		}
	})

	t.Run("SlotsReturnsErrNotImplemented", func(t *testing.T) {
		t.Parallel()

		slots, err := compact.Slots("ocfp", "10.0.0.0/26")
		if !errors.Is(err, netlayout.ErrNotImplemented) {
			t.Fatalf("Slots() error = %v, want ErrNotImplemented", err)
		}

		if slots != (netlayout.InfraSlots{}) {
			t.Fatalf("Slots() slots = %+v, want zero value on stub error", slots)
		}
	})

	t.Run("ValidateSubnetReturnsErrNotImplemented", func(t *testing.T) {
		t.Parallel()

		if err := compact.ValidateSubnet("10.0.0.0/26"); !errors.Is(err, netlayout.ErrNotImplemented) {
			t.Fatalf("ValidateSubnet() error = %v, want ErrNotImplemented", err)
		}
	})
}
