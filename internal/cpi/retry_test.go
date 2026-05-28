package cpi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWaitForCondition_ImmediateTrue(t *testing.T) {
	t.Parallel()

	err := WaitForCondition(context.Background(), time.Millisecond, time.Second, func() (bool, error) {
		return true, nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForCondition_Timeout(t *testing.T) {
	t.Parallel()

	err := WaitForCondition(
		context.Background(),
		1*time.Millisecond,
		5*time.Millisecond,
		func() (bool, error) { return false, nil },
	)

	if err == nil {
		t.Fatal("expected timeout error")
	}

	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("timeout error %q should contain 'timeout'", err.Error())
	}
}

func TestWaitForCondition_ConditionError(t *testing.T) {
	t.Parallel()

	condErr := errors.New("condition check failed")

	err := WaitForCondition(
		context.Background(),
		time.Millisecond,
		time.Second,
		func() (bool, error) { return false, condErr },
	)

	if err == nil {
		t.Fatal("expected error from condition func")
	}

	if !errors.Is(err, condErr) {
		t.Errorf("returned error should be condErr; got %v", err)
	}
}

func TestWaitForCondition_ContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := WaitForCondition(ctx, time.Millisecond, time.Second, func() (bool, error) {
		return false, nil
	})

	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestWaitForCondition_TrueAfterSeveralChecks(t *testing.T) {
	t.Parallel()

	calls := 0

	err := WaitForCondition(
		context.Background(),
		1*time.Millisecond,
		500*time.Millisecond,
		func() (bool, error) {
			calls++
			return calls >= 3, nil
		},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if calls < 3 {
		t.Errorf("condition checked %d times, want ≥3", calls)
	}
}
