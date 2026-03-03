package aws

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetryWithBackoff_Success(t *testing.T) {
	ctx := context.Background()
	config := DefaultRetryConfig()
	config.MaxRetries = 3
	config.BaseDelay = 10 * time.Millisecond

	callCount := 0
	err := RetryWithBackoff(ctx, config, "test-op", func() error {
		callCount++
		if callCount < 3 {
			return &Error{Code: ErrCodeThrottling}
		}

		return nil
	})

	if err != nil {
		t.Errorf("Expected success, got error: %v", err)
	}

	if callCount != 3 {
		t.Errorf("Expected 3 calls, got %d", callCount)
	}
}

func TestRetryWithBackoff_MaxRetriesExceeded(t *testing.T) {
	ctx := context.Background()
	config := DefaultRetryConfig()
	config.MaxRetries = 2
	config.BaseDelay = 10 * time.Millisecond

	callCount := 0
	err := RetryWithBackoff(ctx, config, "test-op", func() error {
		callCount++

		return &Error{Code: ErrCodeThrottling}
	})

	if err == nil {
		t.Error("Expected error, got nil")
	}

	if callCount != 3 { // Initial + 2 retries
		t.Errorf("Expected 3 calls, got %d", callCount)
	}
}

func TestRetryWithBackoff_NonRetryableError(t *testing.T) {
	ctx := context.Background()
	config := DefaultRetryConfig()
	config.MaxRetries = 3
	config.BaseDelay = 10 * time.Millisecond

	callCount := 0
	testErr := &Error{Code: ErrCodeNotFound}
	err := RetryWithBackoff(ctx, config, "test-op", func() error {
		callCount++

		return testErr
	})

	if !errors.Is(err, testErr) {
		t.Errorf("Expected original error, got %v", err)
	}

	if callCount != 1 {
		t.Errorf("Expected 1 call (no retries), got %d", callCount)
	}
}

func TestRetryWithBackoff_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	config := DefaultRetryConfig()
	config.MaxRetries = 5
	config.BaseDelay = 100 * time.Millisecond

	callCount := 0
	errChan := make(chan error, 1)

	go func() {
		err := RetryWithBackoff(ctx, config, "test-op", func() error {
			callCount++

			return &Error{Code: ErrCodeThrottling}
		})
		errChan <- err
	}()

	// Cancel context after a short delay
	time.Sleep(50 * time.Millisecond)
	cancel()

	err := <-errChan
	if err == nil {
		t.Error("Expected context cancellation error, got nil")
	}

	if callCount == 0 {
		t.Error("Expected at least one call")
	}
}

func TestCalculateBackoff(t *testing.T) {
	config := DefaultRetryConfig()
	config.BaseDelay = 100 * time.Millisecond
	config.MaxDelay = 1 * time.Second
	config.JitterFactor = 0.0 // No jitter for predictable testing

	tests := []struct {
		attempt     int
		minExpected time.Duration
		maxExpected time.Duration
	}{
		{0, 100 * time.Millisecond, 100 * time.Millisecond},
		{1, 200 * time.Millisecond, 200 * time.Millisecond},
		{2, 400 * time.Millisecond, 400 * time.Millisecond},
		{3, 800 * time.Millisecond, 800 * time.Millisecond},
		{4, 1 * time.Second, 1 * time.Second}, // Capped at MaxDelay
		{5, 1 * time.Second, 1 * time.Second}, // Capped at MaxDelay
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			delay := calculateBackoff(tt.attempt, config, nil)
			if delay < tt.minExpected || delay > tt.maxExpected {
				t.Errorf("Attempt %d: expected delay between %v and %v, got %v",
					tt.attempt, tt.minExpected, tt.maxExpected, delay)
			}
		})
	}
}

func TestCircuitBreaker_Closed(t *testing.T) {
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig())

	// Should allow requests in closed state
	callCount := 0
	err := cb.Execute(context.Background(), "test-op", func() error {
		callCount++

		return nil
	})

	if err != nil {
		t.Errorf("Expected success, got error: %v", err)
	}

	if callCount != 1 {
		t.Errorf("Expected 1 call, got %d", callCount)
	}

	if cb.State() != StateClosed {
		t.Errorf("Expected closed state, got %s", cb.State())
	}
}

func TestCircuitBreaker_OpenAfterFailures(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.MaxFailures = 3
	cb := NewCircuitBreaker(config)

	testErr := errors.New("test error")

	// Trigger failures to open circuit
	for i := 0; i < 3; i++ {
		_ = cb.Execute(context.Background(), "test-op", func() error {
			return testErr
		})
	}

	if cb.State() != StateOpen {
		t.Errorf("Expected open state after %d failures, got %s", config.MaxFailures, cb.State())
	}

	// Next request should be blocked
	err := cb.Execute(context.Background(), "test-op", func() error {
		t.Error("Function should not be called when circuit is open")

		return nil
	})

	if !IsCircuitBreakerOpen(err) {
		t.Errorf("Expected circuit breaker error, got %v", err)
	}
}

func TestCircuitBreaker_HalfOpen(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.MaxFailures = 2
	config.Timeout = 100 * time.Millisecond
	config.HalfOpenMaxRequests = 2
	cb := NewCircuitBreaker(config)

	testErr := errors.New("test error")

	// Open the circuit
	for i := 0; i < 2; i++ {
		_ = cb.Execute(context.Background(), "test-op", func() error {
			return testErr
		})
	}

	if cb.State() != StateOpen {
		t.Fatalf("Expected open state, got %s", cb.State())
	}

	// Wait for timeout
	time.Sleep(150 * time.Millisecond)

	// Next request should transition to half-open
	callCount := 0
	_ = cb.Execute(context.Background(), "test-op", func() error {
		callCount++

		return nil
	})

	if callCount != 1 {
		t.Errorf("Expected 1 call in half-open, got %d", callCount)
	}
}

func TestCircuitBreaker_RecoveryToClosedState(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.MaxFailures = 2
	config.Timeout = 50 * time.Millisecond
	config.HalfOpenMaxRequests = 3
	cb := NewCircuitBreaker(config)

	// Open the circuit
	for i := 0; i < 2; i++ {
		_ = cb.Execute(context.Background(), "test-op", func() error {
			return errors.New("test error")
		})
	}

	// Wait for timeout
	time.Sleep(100 * time.Millisecond)

	// Execute successful requests in half-open state
	for i := 0; i < 3; i++ {
		err := cb.Execute(context.Background(), "test-op", func() error {
			return nil
		})
		if err != nil {
			t.Errorf("Request %d failed: %v", i, err)
		}
	}

	// Circuit should be closed now
	if cb.State() != StateClosed {
		t.Errorf("Expected closed state after successful half-open tests, got %s", cb.State())
	}
}

func TestCircuitBreaker_Reset(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.MaxFailures = 2
	cb := NewCircuitBreaker(config)

	// Open the circuit
	for i := 0; i < 2; i++ {
		_ = cb.Execute(context.Background(), "test-op", func() error {
			return errors.New("test error")
		})
	}

	if cb.State() != StateOpen {
		t.Fatalf("Expected open state, got %s", cb.State())
	}

	// Reset the circuit breaker
	cb.Reset()

	if cb.State() != StateClosed {
		t.Errorf("Expected closed state after reset, got %s", cb.State())
	}

	// Should allow requests after reset
	callCount := 0
	err := cb.Execute(context.Background(), "test-op", func() error {
		callCount++

		return nil
	})

	if err != nil {
		t.Errorf("Expected success after reset, got error: %v", err)
	}

	if callCount != 1 {
		t.Errorf("Expected 1 call after reset, got %d", callCount)
	}
}

func TestCircuitBreakerError_Error(t *testing.T) {
	err := &CircuitBreakerError{
		State:     StateOpen,
		Operation: "test-operation",
	}

	expected := "circuit breaker is open for operation: test-operation"
	if err.Error() != expected {
		t.Errorf("Expected error message %q, got %q", expected, err.Error())
	}
}

func TestIsCircuitBreakerOpen(t *testing.T) {
	cbErr := &CircuitBreakerError{State: StateOpen, Operation: "test"}
	if !IsCircuitBreakerOpen(cbErr) {
		t.Error("Expected IsCircuitBreakerOpen to return true for CircuitBreakerError")
	}

	otherErr := errors.New("other error")
	if IsCircuitBreakerOpen(otherErr) {
		t.Error("Expected IsCircuitBreakerOpen to return false for non-CircuitBreakerError")
	}
}
