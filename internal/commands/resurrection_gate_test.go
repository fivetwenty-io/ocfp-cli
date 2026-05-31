package commands

import (
	"context"
	"errors"
	"testing"
)

// boshCall records a single runBosh invocation.
type boshCall struct {
	args []string
}

// fakeBosh returns a runBosh function that appends each call to calls and
// returns the error mapped by the first arg string in errMap (nil if absent).
func fakeBosh(calls *[]boshCall, errMap map[string]error) func(args ...string) error {
	return func(args ...string) error {
		*calls = append(*calls, boshCall{args: args})

		if len(args) >= 2 {
			if err, ok := errMap[args[1]]; ok {
				return err
			}
		}

		return nil
	}
}

// TestWithResurrectionGate_OffBeforeDeploy_OnAfterSuccess verifies that
// update-resurrection off is called before deployFn and on is called after
// a successful deploy, in that order.
func TestWithResurrectionGate_OffBeforeDeploy_OnAfterSuccess(t *testing.T) {
	t.Parallel()

	var calls []boshCall
	deployCalled := false

	rb := fakeBosh(&calls, nil)

	err := WithResurrectionGate(context.Background(), rb, func() error {
		// Assert: off must have been called before deployFn runs.
		if len(calls) != 1 {
			t.Errorf("expected 1 bosh call before deployFn, got %d", len(calls))
		} else if calls[0].args[1] != "off" {
			t.Errorf("expected first call to be 'off', got %q", calls[0].args[1])
		}

		deployCalled = true

		return nil
	})

	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if !deployCalled {
		t.Fatal("deployFn was not called")
	}

	// After gate: calls should be [off, on].
	if len(calls) != 2 {
		t.Fatalf("expected 2 bosh calls total, got %d", len(calls))
	}

	if calls[0].args[1] != "off" {
		t.Errorf("first bosh call: want 'off', got %q", calls[0].args[1])
	}

	if calls[1].args[1] != "on" {
		t.Errorf("second bosh call: want 'on', got %q", calls[1].args[1])
	}
}

// TestWithResurrectionGate_OnAfterDeployFailure verifies that the deferred
// update-resurrection on fires even when deployFn returns an error, and that
// the deployFn error is returned to the caller.
func TestWithResurrectionGate_OnAfterDeployFailure(t *testing.T) {
	t.Parallel()

	var calls []boshCall
	deployErr := errors.New("deploy exploded")

	rb := fakeBosh(&calls, nil)

	err := WithResurrectionGate(context.Background(), rb, func() error {
		return deployErr
	})

	if err == nil {
		t.Fatal("expected non-nil error when deployFn fails")
	}

	if !errors.Is(err, deployErr) {
		t.Errorf("expected wrapped deployErr, got: %v", err)
	}

	// Deferred on must still have been called.
	if len(calls) != 2 {
		t.Fatalf("expected 2 bosh calls (off + deferred on), got %d", len(calls))
	}

	if calls[1].args[1] != "on" {
		t.Errorf("second bosh call after failure: want 'on', got %q", calls[1].args[1])
	}
}

// TestWithResurrectionGate_OffToggleFailDoesNotAbort verifies that when
// update-resurrection off returns an error, deployFn is still called (warn,
// continue), and the gate returns the deployFn result, not the toggle error.
func TestWithResurrectionGate_OffToggleFailDoesNotAbort(t *testing.T) {
	t.Parallel()

	var calls []boshCall
	offErr := errors.New("bosh cli not found")

	// "off" fails; "on" succeeds.
	rb := fakeBosh(&calls, map[string]error{"off": offErr})

	deployCalled := false

	err := WithResurrectionGate(context.Background(), rb, func() error {
		deployCalled = true

		return nil
	})

	// off failure must not abort — deployFn should have run.
	if !deployCalled {
		t.Fatal("deployFn not called when update-resurrection off failed")
	}

	// Gate returns nil because deployFn succeeded.
	if err != nil {
		t.Fatalf("expected nil error when deployFn succeeds despite off failure, got: %v", err)
	}

	// Toggle error must not be returned — only deployFn result matters.
	if errors.Is(err, offErr) {
		t.Error("toggle error must not propagate to caller")
	}

	// Both off and on should have been attempted (off failed, on still called).
	if len(calls) != 2 {
		t.Fatalf("expected 2 bosh calls, got %d", len(calls))
	}
}
