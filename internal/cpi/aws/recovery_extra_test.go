package aws

import (
	"context"
	"errors"
	"testing"
)

// ---- RecoveryManager.Recover ------------------------------------------------

func TestRecoveryManager_Recover_NilError(t *testing.T) {
	t.Parallel()

	rm := NewRecoveryManager()
	if err := rm.Recover(context.Background(), nil); err != nil {
		t.Errorf("Recover(nil) = %v, want nil", err)
	}
}

func TestRecoveryManager_Recover_NoStrategyCanRecover(t *testing.T) {
	t.Parallel()

	rm := &RecoveryManager{strategies: []RecoveryStrategy{}} // no strategies
	orig := errors.New("unrecoverable")
	err := rm.Recover(context.Background(), orig)
	if err != orig {
		t.Errorf("Recover with no strategies = %v, want original error", err)
	}
}

func TestRecoveryManager_Recover_StrategySucceeds(t *testing.T) {
	t.Parallel()

	rm := &RecoveryManager{
		strategies: []RecoveryStrategy{&successRecovery{}},
	}
	err := rm.Recover(context.Background(), errors.New("some-error"))
	if err != nil {
		t.Errorf("Recover with succeeding strategy = %v, want nil", err)
	}
}

func TestRecoveryManager_Recover_StrategyFails(t *testing.T) {
	t.Parallel()

	orig := errors.New("original")
	rm := &RecoveryManager{
		strategies: []RecoveryStrategy{&failingRecovery{err: errors.New("recovery failed")}},
	}
	err := rm.Recover(context.Background(), orig)
	// Strategy CanRecover=true but Recover returns error — returns the ORIGINAL error
	if err != orig {
		t.Errorf("Recover with failing strategy = %v, want original error", err)
	}
}

// ---- ThrottlingRecovery / DependencyViolationRecovery / InvalidStateRecovery ----

func TestThrottlingRecovery_Recover(t *testing.T) {
	t.Parallel()

	r := &ThrottlingRecovery{}
	orig := &Error{Code: ErrCodeThrottling, Message: "throttled"}
	err := r.Recover(context.Background(), orig)
	// Returns the original error — retry is caller's responsibility
	if err == nil {
		t.Errorf("ThrottlingRecovery.Recover: expected non-nil error (caller handles retry)")
	}
}

func TestDependencyViolationRecovery_Recover_ContextCancelled(t *testing.T) {
	t.Parallel()

	r := &DependencyViolationRecovery{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := r.Recover(ctx, &Error{Code: ErrCodeDependencyViolation})
	if err == nil {
		t.Errorf("DependencyViolationRecovery.Recover: expected error on cancelled context")
	}
}

func TestExecuteWithRecovery_RecoverySucceedsButOpKeepsFailing(t *testing.T) {
	t.Parallel()

	// Use a manager with a strategy that always succeeds at recovery but the
	// underlying op always fails — hits the "retry after recovery" path and
	// eventually exhausts all attempts.
	rm := &RecoveryManager{
		strategies: []RecoveryStrategy{&successRecovery{}},
	}

	callCount := 0
	err := ExecuteWithRecovery(context.Background(), rm, "test-op", func() error {
		callCount++
		return errors.New("persistent failure")
	})

	if err == nil {
		t.Errorf("expected error after exhausting recovery attempts, got nil")
	}
	if callCount < 3 {
		t.Errorf("expected at least 3 calls, got %d", callCount)
	}
}

func TestInvalidStateRecovery_Recover_ContextCancelled(t *testing.T) {
	t.Parallel()

	r := &InvalidStateRecovery{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately to avoid 3s wait

	err := r.Recover(ctx, &Error{Code: ErrCodeInvalidState})
	if err == nil {
		t.Errorf("InvalidStateRecovery.Recover: expected error on cancelled context")
	}
}

// helpers

type successRecovery struct{}

func (s *successRecovery) CanRecover(_ error) bool                         { return true }
func (s *successRecovery) Recover(_ context.Context, _ error) error        { return nil }

type failingRecovery struct{ err error }

func (f *failingRecovery) CanRecover(_ error) bool                         { return true }
func (f *failingRecovery) Recover(_ context.Context, _ error) error        { return f.err }
