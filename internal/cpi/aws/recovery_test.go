package aws

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestNewRecoveryManager(t *testing.T) {
	rm := NewRecoveryManager()
	if rm == nil {
		t.Fatal("Expected non-nil RecoveryManager")
	}

	if len(rm.strategies) == 0 {
		t.Error("Expected default strategies to be registered")
	}
}

func TestRecoveryManager_AddStrategy(t *testing.T) {
	rm := NewRecoveryManager()
	initialCount := len(rm.strategies)

	rm.AddStrategy(&ThrottlingRecovery{})
	if len(rm.strategies) != initialCount+1 {
		t.Errorf("Expected %d strategies, got %d", initialCount+1, len(rm.strategies))
	}
}

func TestThrottlingRecovery_CanRecover(t *testing.T) {
	recovery := &ThrottlingRecovery{}

	throttlingErr := &Error{Code: ErrCodeThrottling}
	if !recovery.CanRecover(throttlingErr) {
		t.Error("Expected ThrottlingRecovery to handle throttling errors")
	}

	otherErr := &Error{Code: ErrCodeNotFound}
	if recovery.CanRecover(otherErr) {
		t.Error("Expected ThrottlingRecovery not to handle non-throttling errors")
	}
}

func TestDependencyViolationRecovery_CanRecover(t *testing.T) {
	recovery := &DependencyViolationRecovery{}

	depErr := &Error{Code: ErrCodeDependencyViolation}
	if !recovery.CanRecover(depErr) {
		t.Error("Expected DependencyViolationRecovery to handle dependency violation errors")
	}

	otherErr := &Error{Code: ErrCodeNotFound}
	if recovery.CanRecover(otherErr) {
		t.Error("Expected DependencyViolationRecovery not to handle non-dependency errors")
	}
}

func TestInvalidStateRecovery_CanRecover(t *testing.T) {
	recovery := &InvalidStateRecovery{}

	stateErr := &Error{Code: ErrCodeInvalidState}
	if !recovery.CanRecover(stateErr) {
		t.Error("Expected InvalidStateRecovery to handle invalid state errors")
	}

	otherErr := &Error{Code: ErrCodeNotFound}
	if recovery.CanRecover(otherErr) {
		t.Error("Expected InvalidStateRecovery not to handle non-state errors")
	}
}

func TestExecuteWithRecovery_Success(t *testing.T) {
	rm := NewRecoveryManager()
	ctx := context.Background()

	callCount := 0
	err := ExecuteWithRecovery(ctx, rm, "test-op", func() error {
		callCount++

		return nil
	})

	if err != nil {
		t.Errorf("Expected success, got error: %v", err)
	}

	if callCount != 1 {
		t.Errorf("Expected 1 call, got %d", callCount)
	}
}

func TestExecuteWithRecovery_NonRecoverableError(t *testing.T) {
	rm := NewRecoveryManager()
	ctx := context.Background()

	testErr := &Error{Code: ErrCodeNotFound}
	err := ExecuteWithRecovery(ctx, rm, "test-op", func() error {
		return testErr
	})

	if err == nil {
		t.Error("Expected error, got nil")
	}
}

func TestWaitForResourceState_Success(t *testing.T) {
	ctx := context.Background()
	currentState := "pending"

	err := WaitForResourceState(
		ctx,
		"test-resource",
		func() (string, error) {
			if currentState == "pending" {
				currentState = "available"
			}

			return currentState, nil
		},
		[]string{"available"},
		[]string{"error"},
		2*time.Second,
		10*time.Millisecond,
	)

	if err != nil {
		t.Errorf("Expected success, got error: %v", err)
	}
}

func TestWaitForResourceState_ErrorState(t *testing.T) {
	ctx := context.Background()
	currentState := "pending"

	err := WaitForResourceState(
		ctx,
		"test-resource",
		func() (string, error) {
			if currentState == "pending" {
				currentState = "error"
			}

			return currentState, nil
		},
		[]string{"available"},
		[]string{"error"},
		2*time.Second,
		10*time.Millisecond,
	)

	if err == nil {
		t.Error("Expected error state error, got nil")
	}
}

func TestWaitForResourceState_Timeout(t *testing.T) {
	ctx := context.Background()

	err := WaitForResourceState(
		ctx,
		"test-resource",
		func() (string, error) {
			return "pending", nil
		},
		[]string{"available"},
		[]string{"error"},
		100*time.Millisecond,
		10*time.Millisecond,
	)

	if err == nil {
		t.Error("Expected timeout error, got nil")
	}
}

func TestWaitForResourceState_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// checkedCh closes after the first state check, signalling the goroutine
	// is active and blocked on a select — safe to cancel without a sleep.
	checkedCh := make(chan struct{})
	checkOnce := sync.OnceFunc(func() { close(checkedCh) })

	errChan := make(chan error, 1)
	go func() {
		err := WaitForResourceState(
			ctx,
			"test-resource",
			func() (string, error) {
				checkOnce()

				return "pending", nil
			},
			[]string{"available"},
			[]string{"error"},
			5*time.Second,
			50*time.Millisecond,
		)
		errChan <- err
	}()

	// Wait for the first check to complete, then cancel — no fixed sleep.
	<-checkedCh
	cancel()

	err := <-errChan
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Expected context.Canceled error, got %v", err)
	}
}

func TestWaitForResourceState_CheckStateError(t *testing.T) {
	ctx := context.Background()
	testErr := errors.New("check state error")

	err := WaitForResourceState(
		ctx,
		"test-resource",
		func() (string, error) {
			return "", testErr
		},
		[]string{"available"},
		[]string{"error"},
		1*time.Second,
		10*time.Millisecond,
	)

	if err == nil {
		t.Error("Expected error, got nil")
	}

	if !errors.Is(err, testErr) {
		t.Errorf("Expected wrapped test error, got %v", err)
	}
}

func TestRetryWithRecovery_Success(t *testing.T) {
	ctx := context.Background()
	retryConfig := DefaultRetryConfig()
	retryConfig.MaxRetries = 2
	retryConfig.BaseDelay = 10 * time.Millisecond
	rm := NewRecoveryManager()

	callCount := 0
	err := RetryWithRecovery(ctx, retryConfig, rm, "test-op", func() error {
		callCount++
		if callCount < 2 {
			return &Error{Code: ErrCodeThrottling}
		}

		return nil
	})

	if err != nil {
		t.Errorf("Expected success, got error: %v", err)
	}

	if callCount < 2 {
		t.Errorf("Expected at least 2 calls, got %d", callCount)
	}
}

func TestRetryWithRecovery_WithNilConfig(t *testing.T) {
	ctx := context.Background()
	rm := NewRecoveryManager()

	err := RetryWithRecovery(ctx, nil, rm, "test-op", func() error {
		return nil
	})

	if err != nil {
		t.Errorf("Expected success with nil config (should use defaults), got error: %v", err)
	}
}

func TestRetryWithRecovery_WithNilRecoveryManager(t *testing.T) {
	ctx := context.Background()
	retryConfig := DefaultRetryConfig()
	retryConfig.MaxRetries = 1
	retryConfig.BaseDelay = 10 * time.Millisecond

	err := RetryWithRecovery(ctx, retryConfig, nil, "test-op", func() error {
		return nil
	})

	if err != nil {
		t.Errorf("Expected success with nil recovery manager (should use default), got error: %v", err)
	}
}
