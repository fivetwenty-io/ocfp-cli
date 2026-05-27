// Package cleanup_test contains unit tests for the ResourceTracker.
// No build tag — runs in the standard unit test lane.
package cleanup_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ocfp/ocfp-cli-go/tests/integration/cleanup"
)

// T75 TestTrack_RecordsResource verifies that Track stores resources in the
// order they are added, and that the CID and Kind are preserved exactly.
func TestTrack_RecordsResource(t *testing.T) {
	tr := cleanup.New()

	r := cleanup.Resource{
		Kind: cleanup.KindVM,
		CID:  "vm-42",
		Meta: map[string]string{"deployment": "test"},
	}
	tr.Track(r)

	// Verify via a Cleanup pass that exactly one callback is invoked with the
	// correct resource.
	var called []cleanup.Resource
	err := tr.Cleanup(context.Background(), map[cleanup.ResourceKind]func(context.Context, cleanup.Resource) error{
		cleanup.KindVM: func(_ context.Context, r cleanup.Resource) error {
			called = append(called, r)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Cleanup returned unexpected error: %v", err)
	}
	if len(called) != 1 {
		t.Fatalf("expected 1 callback invocation, got %d", len(called))
	}
	if called[0].CID != "vm-42" {
		t.Errorf("callback received CID %q, want %q", called[0].CID, "vm-42")
	}
	if called[0].Kind != cleanup.KindVM {
		t.Errorf("callback received Kind %v, want KindVM", called[0].Kind)
	}
	if called[0].Meta["deployment"] != "test" {
		t.Errorf("Meta[deployment] = %q, want %q", called[0].Meta["deployment"], "test")
	}
}

// T76 TestCleanup_ReverseOrder verifies that Cleanup invokes callbacks in the
// reverse of the order Track was called (LIFO). Track order: A, B, C;
// expected cleanup order: C, B, A.
func TestCleanup_ReverseOrder(t *testing.T) {
	tr := cleanup.New()

	tr.Track(cleanup.Resource{Kind: cleanup.KindStemcell, CID: "A"})
	tr.Track(cleanup.Resource{Kind: cleanup.KindVM, CID: "B"})
	tr.Track(cleanup.Resource{Kind: cleanup.KindDisk, CID: "C"})

	var order []string
	cb := func(_ context.Context, r cleanup.Resource) error {
		order = append(order, r.CID)
		return nil
	}

	err := tr.Cleanup(context.Background(), map[cleanup.ResourceKind]func(context.Context, cleanup.Resource) error{
		cleanup.KindStemcell: cb,
		cleanup.KindVM:       cb,
		cleanup.KindDisk:     cb,
	})
	if err != nil {
		t.Fatalf("Cleanup returned unexpected error: %v", err)
	}

	want := []string{"C", "B", "A"}
	if len(order) != len(want) {
		t.Fatalf("callback invoked %d times, want %d; order=%v", len(order), len(want), order)
	}
	for i, cid := range want {
		if order[i] != cid {
			t.Errorf("cleanup order[%d] = %q, want %q", i, order[i], cid)
		}
	}
}

// T77 TestCleanup_BestEffort verifies that when one callback returns an error,
// subsequent (earlier-tracked) resources are still cleaned up and the combined
// error is returned.
func TestCleanup_BestEffort(t *testing.T) {
	tr := cleanup.New()

	tr.Track(cleanup.Resource{Kind: cleanup.KindStemcell, CID: "stemcell-1"})
	tr.Track(cleanup.Resource{Kind: cleanup.KindVM, CID: "vm-1"})
	tr.Track(cleanup.Resource{Kind: cleanup.KindDisk, CID: "disk-1"})

	// disk-1 cleanup succeeds, vm-1 fails, stemcell-1 succeeds.
	errVM := errors.New("delete_vm: not found")

	var cleanedCIDs []string
	err := tr.Cleanup(context.Background(), map[cleanup.ResourceKind]func(context.Context, cleanup.Resource) error{
		cleanup.KindDisk: func(_ context.Context, r cleanup.Resource) error {
			cleanedCIDs = append(cleanedCIDs, r.CID)
			return nil
		},
		cleanup.KindVM: func(_ context.Context, r cleanup.Resource) error {
			cleanedCIDs = append(cleanedCIDs, r.CID)
			return errVM
		},
		cleanup.KindStemcell: func(_ context.Context, r cleanup.Resource) error {
			cleanedCIDs = append(cleanedCIDs, r.CID)
			return nil
		},
	})

	// All three callbacks must have been called.
	if len(cleanedCIDs) != 3 {
		t.Fatalf("expected 3 callback invocations, got %d: %v", len(cleanedCIDs), cleanedCIDs)
	}

	// Cleanup must return a non-nil combined error.
	if err == nil {
		t.Fatal("Cleanup returned nil error, expected combined error from vm-1 failure")
	}
	if !errors.Is(err, errVM) {
		t.Errorf("combined error does not wrap errVM; got: %v", err)
	}
}

// T78 TestCleanup_MissingCallback_SkipsResource verifies that a resource
// whose Kind has no entry in the callbacks map is silently skipped — no panic,
// no error, and the remaining resources with callbacks are still cleaned.
func TestCleanup_MissingCallback_SkipsResource(t *testing.T) {
	tr := cleanup.New()

	tr.Track(cleanup.Resource{Kind: cleanup.KindVM, CID: "vm-1"})
	// KindZone has no callback registered below.
	tr.Track(cleanup.Resource{Kind: cleanup.KindZone, CID: "zone-1"})
	tr.Track(cleanup.Resource{Kind: cleanup.KindDisk, CID: "disk-1"})

	var called []string
	err := tr.Cleanup(context.Background(), map[cleanup.ResourceKind]func(context.Context, cleanup.Resource) error{
		cleanup.KindVM: func(_ context.Context, r cleanup.Resource) error {
			called = append(called, r.CID)
			return nil
		},
		cleanup.KindDisk: func(_ context.Context, r cleanup.Resource) error {
			called = append(called, r.CID)
			return nil
		},
		// KindZone intentionally absent.
	})

	if err != nil {
		t.Fatalf("Cleanup returned unexpected error: %v", err)
	}

	// Only vm-1 and disk-1 should be in called (in reverse order: disk-1, vm-1).
	if len(called) != 2 {
		t.Fatalf("expected 2 callbacks (skipping zone), got %d: %v", len(called), called)
	}
	if called[0] != "disk-1" || called[1] != "vm-1" {
		t.Errorf("unexpected cleanup order: %v (want [disk-1 vm-1])", called)
	}
}

// TestCleanup_EmptyTracker_NoOp verifies that a Tracker with no tracked
// resources calls no callbacks and returns nil.
func TestCleanup_EmptyTracker_NoOp(t *testing.T) {
	tr := cleanup.New()

	called := false
	err := tr.Cleanup(context.Background(), map[cleanup.ResourceKind]func(context.Context, cleanup.Resource) error{
		cleanup.KindVM: func(_ context.Context, _ cleanup.Resource) error {
			called = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("empty Tracker Cleanup returned error: %v", err)
	}
	if called {
		t.Fatal("callback was invoked on empty Tracker")
	}
}

// TestCleanup_EmptyCID_Skipped verifies that resources with an empty CID are
// silently skipped (no callback invocation) without error.
func TestCleanup_EmptyCID_Skipped(t *testing.T) {
	tr := cleanup.New()

	tr.Track(cleanup.Resource{Kind: cleanup.KindVM, CID: ""}) // empty CID
	tr.Track(cleanup.Resource{Kind: cleanup.KindDisk, CID: "disk-1"})

	var called []string
	err := tr.Cleanup(context.Background(), map[cleanup.ResourceKind]func(context.Context, cleanup.Resource) error{
		cleanup.KindVM: func(_ context.Context, r cleanup.Resource) error {
			called = append(called, r.CID)
			return nil
		},
		cleanup.KindDisk: func(_ context.Context, r cleanup.Resource) error {
			called = append(called, r.CID)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Cleanup returned unexpected error: %v", err)
	}
	if len(called) != 1 || called[0] != "disk-1" {
		t.Errorf("expected only disk-1 in callbacks, got %v", called)
	}
}

// TestCleanup_NilContext_ReturnsError verifies that passing a nil context
// returns an error immediately without invoking callbacks.
func TestCleanup_NilContext_ReturnsError(t *testing.T) {
	tr := cleanup.New()
	tr.Track(cleanup.Resource{Kind: cleanup.KindVM, CID: "vm-1"})

	called := false
	var ctx context.Context // intentionally nil to exercise nil-context rejection path
	err := tr.Cleanup(ctx, map[cleanup.ResourceKind]func(context.Context, cleanup.Resource) error{
		cleanup.KindVM: func(_ context.Context, _ cleanup.Resource) error {
			called = true
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected error for nil context, got nil")
	}
	if called {
		t.Fatal("callback was invoked despite nil context")
	}
}
