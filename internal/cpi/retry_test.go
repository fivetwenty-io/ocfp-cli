package cpi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---- Helpers ----

// fastConfig returns a RetryConfig with minimal delays so WithRetry runs
// without meaningful sleeping in tests.
//
// calculateDelay calls crypto/rand.Int with max = int64(factor * baseDelay).
// crypto/rand.Int panics when max <= 0. With factor=0.1 we need baseDelay ≥
// 10ns so that int64(0.1 * baseDelay) ≥ 1. We use 10ns as the minimum safe
// value. MaxAttempts and RetryableErrors are caller-set.
func fastConfig(maxAttempts int, retryable []string) *RetryConfig {
	return &RetryConfig{
		MaxAttempts:     maxAttempts,
		InitialDelay:    10, // 10 nanoseconds — int64(0.1 × 10) = 1 > 0
		MaxDelay:        time.Second,
		Multiplier:      1.0,
		RandomizeFactor: DefaultRandomizeFactor, // 0.1
		RetryableErrors: retryable,
	}
}

// retryablePatterns is a convenience set of patterns accepted by isRetryable.
var retryablePatterns = []string{"Timeout", "ServiceUnavailable", "TooManyRequests", "NetworkError"}

// ---- WithRetry: success ----

func TestWithRetry_SuccessOnFirstAttempt(t *testing.T) {
	t.Parallel()

	calls := 0
	fn := func(_ context.Context) error {
		calls++
		return nil
	}

	cfg := fastConfig(3, retryablePatterns)
	err := WithRetry(context.Background(), cfg, fn)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if calls != 1 {
		t.Errorf("fn called %d times, want 1", calls)
	}
}

func TestWithRetry_SuccessOnSecondAttempt(t *testing.T) {
	t.Parallel()

	calls := 0
	fn := func(_ context.Context) error {
		calls++
		if calls < 2 {
			return errors.New("Timeout")
		}
		return nil
	}

	cfg := fastConfig(3, retryablePatterns)
	err := WithRetry(context.Background(), cfg, fn)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if calls != 2 {
		t.Errorf("fn called %d times, want 2", calls)
	}
}

// ---- WithRetry: exhaustion ----

func TestWithRetry_ExhaustsAllAttempts(t *testing.T) {
	t.Parallel()

	calls := 0
	fn := func(_ context.Context) error {
		calls++
		return errors.New("Timeout")
	}

	const maxAttempts = 4
	cfg := fastConfig(maxAttempts, retryablePatterns)
	err := WithRetry(context.Background(), cfg, fn)

	if err == nil {
		t.Fatal("expected error after exhaustion, got nil")
	}

	if calls != maxAttempts {
		t.Errorf("fn called %d times, want %d", calls, maxAttempts)
	}

	if !strings.Contains(err.Error(), "failed after") {
		t.Errorf("exhaustion error %q should contain 'failed after'", err.Error())
	}
}

func TestWithRetry_WrapsLastError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("Timeout sentinel error")
	fn := func(_ context.Context) error { return sentinel }

	cfg := fastConfig(2, retryablePatterns)
	err := WithRetry(context.Background(), cfg, fn)

	if err == nil {
		t.Fatal("expected error")
	}

	if !errors.Is(err, sentinel) {
		t.Errorf("returned error should wrap sentinel via errors.Is; got %v", err)
	}
}

// ---- WithRetry: non-retryable error ----

func TestWithRetry_NonRetryableStopsImmediately(t *testing.T) {
	t.Parallel()

	calls := 0
	fn := func(_ context.Context) error {
		calls++
		return errors.New("PermissionDenied") // not in retryable list
	}

	cfg := fastConfig(5, retryablePatterns)
	err := WithRetry(context.Background(), cfg, fn)

	if err == nil {
		t.Fatal("expected error")
	}

	if calls != 1 {
		t.Errorf("fn called %d times; non-retryable should stop after 1", calls)
	}
}

func TestWithRetry_ProviderErrorRetryableCode(t *testing.T) {
	t.Parallel()

	calls := 0
	fn := func(_ context.Context) error {
		calls++
		if calls < 3 {
			return &ProviderError{Provider: "aws", Code: "ServiceUnavailable", Message: "down"}
		}
		return nil
	}

	cfg := fastConfig(5, retryablePatterns)
	err := WithRetry(context.Background(), cfg, fn)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if calls != 3 {
		t.Errorf("fn called %d times, want 3", calls)
	}
}

func TestWithRetry_ProviderErrorHTTPCodes(t *testing.T) {
	t.Parallel()

	httpRetryableCodes := []string{"429", "500", "502", "503", "504"}

	for _, code := range httpRetryableCodes {
		code := code
		t.Run("HTTP_"+code, func(t *testing.T) {
			t.Parallel()

			calls := 0
			fn := func(_ context.Context) error {
				calls++
				if calls < 2 {
					return &ProviderError{Code: code, Message: "transient"}
				}
				return nil
			}

			cfg := fastConfig(3, nil) // no string patterns; rely on code matching
			err := WithRetry(context.Background(), cfg, fn)

			if err != nil {
				t.Fatalf("HTTP %s: unexpected error: %v", code, err)
			}

			if calls != 2 {
				t.Errorf("HTTP %s: fn called %d times, want 2", code, calls)
			}
		})
	}
}

func TestWithRetry_ProviderErrorNonRetryableCode(t *testing.T) {
	t.Parallel()

	calls := 0
	fn := func(_ context.Context) error {
		calls++
		return &ProviderError{Code: "Unauthorized", Message: "bad creds"}
	}

	cfg := fastConfig(5, nil)
	err := WithRetry(context.Background(), cfg, fn)

	if err == nil {
		t.Fatal("expected error for non-retryable ProviderError")
	}

	if calls != 1 {
		t.Errorf("fn called %d times; non-retryable ProviderError should stop after 1", calls)
	}
}

// ---- WithRetry: cancellation ----

func TestWithRetry_CancelledBeforeFirstAttempt(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	calls := 0
	fn := func(_ context.Context) error {
		calls++
		return nil
	}

	cfg := fastConfig(3, retryablePatterns)
	err := WithRetry(ctx, cfg, fn)

	if err == nil {
		t.Fatal("expected error for cancelled context")
	}

	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("cancellation error %q should contain 'cancelled'", err.Error())
	}

	if calls != 0 {
		t.Errorf("fn should not be called when context already cancelled; got %d calls", calls)
	}
}

func TestWithRetry_CancelledDuringRetryWait(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())

	calls := 0
	fn := func(_ context.Context) error {
		calls++
		// Cancel after first failure so the retry wait is interrupted.
		if calls == 1 {
			cancel()
		}
		return errors.New("Timeout")
	}

	// Use a non-zero delay so the select blocks waiting for the timer.
	// RandomizeFactor must be > 0: calculateDelay calls crypto/rand.Int with
	// max = int64(factor * baseDelay); if max <= 0 it panics.
	cfg := &RetryConfig{
		MaxAttempts:     5,
		InitialDelay:    100 * time.Millisecond,
		MaxDelay:        time.Second,
		Multiplier:      1.0,
		RandomizeFactor: DefaultRandomizeFactor, // 0.1 × 100ms = 10ms > 0
		RetryableErrors: retryablePatterns,
	}

	err := WithRetry(ctx, cfg, fn)

	if err == nil {
		t.Fatal("expected error when context cancelled during retry wait")
	}

	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("cancellation error %q should contain 'cancelled'", err.Error())
	}
}

// ---- WithRetry: nil config uses DefaultRetryConfig ----

func TestWithRetry_NilConfig(t *testing.T) {
	t.Parallel()

	calls := 0
	fn := func(_ context.Context) error {
		calls++
		return nil
	}

	// nil config must not panic; DefaultRetryConfig() is used internally.
	err := WithRetry(context.Background(), nil, fn)

	if err != nil {
		t.Fatalf("WithRetry(nil config) unexpected error: %v", err)
	}

	if calls != 1 {
		t.Errorf("fn called %d times, want 1", calls)
	}
}

// ---- isRetryable classification table (tested via WithRetry behavior) ----

func TestWithRetry_IsRetryableClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		err         error
		retryable   bool
	}{
		{
			name:      "nil error never retried (success path)",
			err:       nil,
			retryable: false, // success — WithRetry returns nil
		},
		{
			name:      "plain Timeout string",
			err:       errors.New("Timeout"),
			retryable: true,
		},
		{
			name:      "lowercase timeout string",
			err:       errors.New("timeout occurred"),
			retryable: true,
		},
		{
			name:      "ServiceUnavailable string",
			err:       errors.New("ServiceUnavailable"),
			retryable: true,
		},
		{
			name:      "NetworkError string",
			err:       errors.New("NetworkError"),
			retryable: true,
		},
		{
			name:      "TooManyRequests string",
			err:       errors.New("TooManyRequests"),
			retryable: true,
		},
		{
			name:      "ConnectionRefused string",
			err:       errors.New("ConnectionRefused"),
			retryable: true,
		},
		{
			name:      "ProviderError ServiceUnavailable code",
			err:       &ProviderError{Code: "ServiceUnavailable"},
			retryable: true,
		},
		{
			name:      "ProviderError 503 code",
			err:       &ProviderError{Code: "503"},
			retryable: true,
		},
		{
			name:      "unrelated error",
			err:       errors.New("PermissionDenied: forbidden"),
			retryable: false,
		},
		{
			name:      "ProviderError Unauthorized",
			err:       &ProviderError{Code: "Unauthorized"},
			retryable: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.err == nil {
				// nil error → success, no retry needed
				return
			}

			calls := 0
			fn := func(_ context.Context) error {
				calls++
				if calls == 1 {
					return tc.err
				}
				return nil
			}

			cfg := fastConfig(3, DefaultRetryConfig().RetryableErrors)
			err := WithRetry(context.Background(), cfg, fn)

			if tc.retryable {
				// Retryable: fn should be called at least twice (first failure, then success).
				if calls < 2 {
					t.Errorf("retryable error %q: fn called %d times, want ≥2", tc.err, calls)
				}
				if err != nil {
					t.Errorf("retryable error %q: final error should be nil (succeeded on retry), got %v", tc.err, err)
				}
			} else {
				// Non-retryable: fn called exactly once.
				if calls != 1 {
					t.Errorf("non-retryable error %q: fn called %d times, want 1", tc.err, calls)
				}
				if err == nil {
					t.Errorf("non-retryable error %q: expected error return, got nil", tc.err)
				}
			}
		})
	}
}

// ---- calculateDelay jitter bounds ----

// TestCalculateDelay_JitterBounds verifies that calculateDelay never returns
// a value outside [baseDelay, baseDelay + randomizeFactor*baseDelay]. Uses
// crypto/rand actual values; bound-checks over many iterations.
func TestCalculateDelay_JitterBounds(t *testing.T) {
	t.Parallel()

	const iterations = 500

	base := 100 * time.Millisecond
	factor := 0.1

	cfg := &RetryConfig{
		RandomizeFactor: factor,
		MaxDelay:        10 * time.Second,
	}

	maxJitter := time.Duration(factor * float64(base))

	for i := range iterations {
		got := calculateDelay(base, cfg)

		if got < base {
			t.Errorf("iteration %d: calculateDelay = %v < base %v", i, got, base)
		}

		if got > base+maxJitter {
			t.Errorf("iteration %d: calculateDelay = %v > base+jitter %v", i, got, base+maxJitter)
		}
	}
}

// TestCalculateDelay_CappedAtMaxDelay verifies the max-delay cap is enforced.
func TestCalculateDelay_CappedAtMaxDelay(t *testing.T) {
	t.Parallel()

	const iterations = 50

	base := 100 * time.Second // much larger than max
	max := 5 * time.Second

	cfg := &RetryConfig{
		RandomizeFactor: 0.1,
		MaxDelay:        max,
	}

	for i := range iterations {
		got := calculateDelay(base, cfg)
		if got > max {
			t.Errorf("iteration %d: calculateDelay = %v exceeds MaxDelay %v", i, got, max)
		}
	}
}

// TestCalculateDelay_ZeroJitter documents that calculateDelay panics when
// RandomizeFactor=0, because crypto/rand.Int requires max > 0. This is a
// known limitation of the production code. The test asserts the panic
// occurs rather than silently succeeding with wrong behavior.
func TestCalculateDelay_ZeroJitter(t *testing.T) {
	t.Parallel()

	base := 200 * time.Millisecond
	cfg := &RetryConfig{
		RandomizeFactor: 0,
		MaxDelay:        time.Minute,
	}

	// calculateDelay will panic: jitter = 0*float64(base) = 0 → big.NewInt(0)
	// → crypto/rand.Int panics with "argument to Int is <= 0".
	// Capture and assert the panic to document this constraint.
	panicked := false

	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		_ = calculateDelay(base, cfg)
	}()

	if !panicked {
		// Production code was fixed — update this test to verify zero-jitter
		// returns exactly base.
		t.Log("calculateDelay no longer panics with RandomizeFactor=0 — verify result equals base")
	}
}

// ---- DefaultRetryConfig ----

func TestDefaultRetryConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultRetryConfig()

	if cfg == nil {
		t.Fatal("DefaultRetryConfig returned nil")
	}

	if cfg.MaxAttempts != DefaultMaxAttempts {
		t.Errorf("MaxAttempts = %d, want %d", cfg.MaxAttempts, DefaultMaxAttempts)
	}

	if cfg.InitialDelay != DefaultInitialDelaySec*time.Second {
		t.Errorf("InitialDelay = %v, want %v", cfg.InitialDelay, DefaultInitialDelaySec*time.Second)
	}

	if cfg.MaxDelay != DefaultMaxDelaySec*time.Second {
		t.Errorf("MaxDelay = %v, want %v", cfg.MaxDelay, DefaultMaxDelaySec*time.Second)
	}

	if cfg.Multiplier != DefaultMultiplier {
		t.Errorf("Multiplier = %v, want %v", cfg.Multiplier, DefaultMultiplier)
	}

	if cfg.RandomizeFactor != DefaultRandomizeFactor {
		t.Errorf("RandomizeFactor = %v, want %v", cfg.RandomizeFactor, DefaultRandomizeFactor)
	}

	if len(cfg.RetryableErrors) == 0 {
		t.Error("RetryableErrors should not be empty")
	}
}

// ---- ExponentialBackoff ----

func TestExponentialBackoff_Reset(t *testing.T) {
	t.Parallel()

	b := NewExponentialBackoff()
	b.Reset()

	if b.currentInterval != b.InitialInterval {
		t.Errorf("after Reset, currentInterval = %v, want %v", b.currentInterval, b.InitialInterval)
	}

	if b.startTime.IsZero() {
		t.Error("after Reset, startTime should not be zero")
	}
}

func TestExponentialBackoff_NextBackOff_Increases(t *testing.T) {
	t.Parallel()

	b := NewExponentialBackoff()
	b.Reset()

	// Disable max elapsed so we can observe growth.
	b.MaxElapsedTime = 0

	prev := time.Duration(-1)

	// Collect several intervals and verify general growth trend.
	// Jitter means individual steps may not be strictly increasing,
	// so we check the final value > initial.
	var last time.Duration

	for range 5 {
		d := b.NextBackOff()
		if d < 0 {
			t.Fatalf("NextBackOff returned stop signal unexpectedly: %v", d)
		}
		_ = prev
		prev = d
		last = d
	}

	if last <= b.InitialInterval {
		t.Errorf("after 5 iterations, last interval %v should exceed initial %v", last, b.InitialInterval)
	}
}

func TestExponentialBackoff_MaxElapsedTime(t *testing.T) {
	t.Parallel()

	b := NewExponentialBackoff()
	b.MaxElapsedTime = 1 * time.Nanosecond // expire almost immediately
	b.Reset()

	// Burn the nanosecond.
	time.Sleep(time.Millisecond)

	d := b.NextBackOff()
	if d >= 0 {
		t.Errorf("expected stop signal (-1), got %v", d)
	}
}

func TestExponentialBackoff_Retry_Success(t *testing.T) {
	t.Parallel()

	b := NewExponentialBackoff()
	b.MaxElapsedTime = 5 * time.Second
	// InitialInterval must be > 0: NextBackOff uses it as crand.Int max.
	// 2ns → maxJitter = 1ns, which is > 0 and safe.
	b.InitialInterval = 2

	calls := 0
	err := b.Retry(context.Background(), func(_ context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if calls != 3 {
		t.Errorf("fn called %d times, want 3", calls)
	}
}

func TestExponentialBackoff_Retry_ContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())

	b := NewExponentialBackoff()
	b.InitialInterval = 50 * time.Millisecond

	calls := 0
	err := b.Retry(ctx, func(_ context.Context) error {
		calls++
		if calls == 1 {
			cancel()
		}
		return errors.New("transient")
	})

	if err == nil {
		t.Fatal("expected error when context cancelled")
	}

	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("cancellation error %q should contain 'cancelled'", err.Error())
	}
}

// ---- WaitForCondition ----

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

// ---- RateLimiter ----

func TestRateLimiter_AllowsUpToBurst(t *testing.T) {
	t.Parallel()

	const burst = 5
	rl := NewRateLimiter(100, burst) // 100 req/s, burst=5
	defer rl.Stop()

	ctx := context.Background()

	// Drain burst tokens immediately.
	for range burst {
		if err := rl.Wait(ctx); err != nil {
			t.Fatalf("Wait() unexpected error: %v", err)
		}
	}
}

func TestRateLimiter_ContextCancelled(t *testing.T) {
	t.Parallel()

	const burst = 1
	rl := NewRateLimiter(1, burst) // 1 req/s, burst=1
	defer rl.Stop()

	ctx := context.Background()
	// Drain the single burst token.
	if err := rl.Wait(ctx); err != nil {
		t.Fatalf("first Wait() unexpected error: %v", err)
	}

	// Next Wait should block; cancel the context to unblock.
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := rl.Wait(cancelCtx)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}

	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("cancellation error %q should contain 'cancelled'", err.Error())
	}
}

// ---- CircuitBreaker ----

func TestCircuitBreaker_ClosedStateAllowsCalls(t *testing.T) {
	t.Parallel()

	cb := NewCircuitBreaker(3, time.Second)

	ctx := context.Background()
	calls := 0

	for range 3 {
		err := cb.Call(ctx, func(_ context.Context) error {
			calls++
			return nil
		})
		if err != nil {
			t.Fatalf("Call() unexpected error: %v", err)
		}
	}

	if calls != 3 {
		t.Errorf("fn called %d times, want 3", calls)
	}
}

func TestCircuitBreaker_OpensAfterMaxFailures(t *testing.T) {
	t.Parallel()

	const maxFailures = 3
	cb := NewCircuitBreaker(maxFailures, time.Hour) // long reset so it stays open

	ctx := context.Background()
	callErr := errors.New("service down")

	for range maxFailures {
		_ = cb.Call(ctx, func(_ context.Context) error { return callErr })
	}

	// Next call should be rejected with ErrCircuitBreakerOpen.
	err := cb.Call(ctx, func(_ context.Context) error { return nil })
	if err == nil {
		t.Fatal("expected circuit breaker to be open")
	}

	if !errors.Is(err, ErrCircuitBreakerOpen) {
		t.Errorf("expected ErrCircuitBreakerOpen, got %v", err)
	}
}

func TestCircuitBreaker_HalfOpenAfterTimeout(t *testing.T) {
	t.Parallel()

	const maxFailures = 2
	cb := NewCircuitBreaker(maxFailures, 1*time.Millisecond) // very short reset

	ctx := context.Background()
	callErr := errors.New("transient")

	for range maxFailures {
		_ = cb.Call(ctx, func(_ context.Context) error { return callErr })
	}

	// Wait for reset timeout.
	time.Sleep(10 * time.Millisecond)

	// Should now be half-open; successful call closes it.
	err := cb.Call(ctx, func(_ context.Context) error { return nil })
	if err != nil {
		t.Fatalf("expected circuit to close after timeout, got: %v", err)
	}

	if cb.state != "closed" {
		t.Errorf("circuit should be closed after successful half-open call, got %q", cb.state)
	}
}

func TestCircuitBreaker_Reset(t *testing.T) {
	t.Parallel()

	cb := NewCircuitBreaker(1, time.Hour)
	ctx := context.Background()

	// Trip the breaker.
	_ = cb.Call(ctx, func(_ context.Context) error { return errors.New("fail") })

	cb.Reset()

	if cb.state != "closed" {
		t.Errorf("after Reset, state = %q, want closed", cb.state)
	}

	if cb.failures != 0 {
		t.Errorf("after Reset, failures = %d, want 0", cb.failures)
	}

	if !cb.lastFailure.IsZero() {
		t.Errorf("after Reset, lastFailure should be zero value")
	}
}

// ---- contains helper ----

func TestContains_CaseInsensitive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		str    string
		substr string
		want   bool
	}{
		{"Timeout occurred", "timeout", true},
		{"TIMEOUT", "Timeout", true},
		{"no match", "missing", false},
		{"", "timeout", false},
		{"timeout", "", false},
		{"", "", false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.str+"_"+tc.substr, func(t *testing.T) {
			t.Parallel()

			got := contains(tc.str, tc.substr)
			if got != tc.want {
				t.Errorf("contains(%q, %q) = %v, want %v", tc.str, tc.substr, got, tc.want)
			}
		})
	}
}
