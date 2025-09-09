package cpi

import (
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	// Default retry configuration.
	DefaultMaxAttempts     = 3
	DefaultInitialDelaySec = 2
	DefaultMaxDelaySec     = 30
	DefaultMultiplier      = 2.0
	DefaultRandomizeFactor = 0.1

	// Exponential backoff configuration.
	DefaultInitialIntervalMS = 500
	DefaultMaxIntervalSec    = 60
	ExponentialMultiplier    = 1.5
	DefaultMaxElapsedMin     = 15

	// Jitter calculation.
	JitterDivisor = 2
	JitterQuarter = 4

	// Conflict retry configuration.
	ConflictInitialIntervalMS = 100
	ConflictMaxIntervalSec    = 3
	ConflictMultiplier        = 2.0
	ConflictMaxElapsedSec     = 30
)

// RetryConfig configures retry behavior.
type RetryConfig struct {
	MaxAttempts     int
	InitialDelay    time.Duration
	MaxDelay        time.Duration
	Multiplier      float64
	RandomizeFactor float64
	RetryableErrors []string
}

// DefaultRetryConfig returns the default retry configuration.
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxAttempts:     DefaultMaxAttempts,
		InitialDelay:    DefaultInitialDelaySec * time.Second,
		MaxDelay:        DefaultMaxDelaySec * time.Second,
		Multiplier:      DefaultMultiplier,
		RandomizeFactor: DefaultRandomizeFactor,
		RetryableErrors: []string{
			"Timeout",
			"ServiceUnavailable",
			"TooManyRequests",
			"NetworkError",
			"ConnectionRefused",
		},
	}
}

// RetryableFunc is a function that can be retried.
type RetryableFunc func(ctx context.Context) error

// WithRetry executes a function with retry logic.
func WithRetry(ctx context.Context, cfg *RetryConfig, retryableFunc RetryableFunc) error {
	if cfg == nil {
		cfg = DefaultRetryConfig()
	}

	var lastErr error

	delay := cfg.InitialDelay

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		// Check context before attempting
		select {
		case <-ctx.Done():
			return fmt.Errorf("operation cancelled: %w", ctx.Err())
		default:
		}

		// Execute the function
		err := retryableFunc(ctx)
		if err == nil {
			return nil
		}

		lastErr = err

		// Check if error is retryable
		if !isRetryable(err, cfg.RetryableErrors) {
			logger.Debugf("Error is not retryable: %v", err)

			return err
		}

		// Don't retry if we've exhausted attempts
		if attempt >= cfg.MaxAttempts {
			logger.Warnf("Max retry attempts (%d) reached", cfg.MaxAttempts)

			break
		}

		// Calculate next delay with jitter
		nextDelay := calculateDelay(delay, cfg)

		logger.Infof("Attempt %d/%d failed, retrying in %v: %v",
			attempt, cfg.MaxAttempts, nextDelay, err)

		// Wait before next attempt
		timer := time.NewTimer(nextDelay)
		select {
		case <-ctx.Done():
			timer.Stop()

			return fmt.Errorf("operation cancelled during retry: %w", ctx.Err())
		case <-timer.C:
		}

		// Update delay for next iteration
		delay = time.Duration(float64(delay) * cfg.Multiplier)
		if delay > cfg.MaxDelay {
			delay = cfg.MaxDelay
		}
	}

	return fmt.Errorf("operation failed after %d attempts: %w", cfg.MaxAttempts, lastErr)
}

// isRetryable checks if an error is retryable.
func isRetryable(err error, retryableErrors []string) bool {
	if err == nil {
		return false
	}

	// Check if it's a provider error
	perr := &ProviderError{
		Provider: "",
		Code:     "",
		Message:  "",
		Details:  nil,
	}
	if errors.As(err, &perr) {
		for _, code := range retryableErrors {
			if perr.Code == code {
				return true
			}
		}

		// Check common HTTP status codes
		switch perr.Code {
		case "429", "500", "502", "503", "504":
			return true
		}
	}

	// Check error message for common patterns
	errStr := err.Error()
	for _, pattern := range retryableErrors {
		if contains(errStr, pattern) {
			return true
		}
	}

	return false
}

// calculateDelay calculates the next delay with jitter.
func calculateDelay(baseDelay time.Duration, cfg *RetryConfig) time.Duration {
	// Add jitter to prevent thundering herd
	jitter := cfg.RandomizeFactor * float64(baseDelay)
	// Use crypto/rand for secure random number generation
	n, _ := crand.Int(crand.Reader, big.NewInt(int64(jitter)))
	jitterDuration := time.Duration(n.Int64())

	delay := baseDelay + jitterDuration

	// Cap at max delay
	if delay > cfg.MaxDelay {
		delay = cfg.MaxDelay
	}

	return delay
}

// contains checks if a string contains a substring (case-insensitive).
func contains(str, substr string) bool {
	if len(str) == 0 || len(substr) == 0 {
		return false
	}

	return strings.Contains(strings.ToLower(str), strings.ToLower(substr))
}

// ExponentialBackoff implements exponential backoff with context support.
type ExponentialBackoff struct {
	InitialInterval time.Duration
	MaxInterval     time.Duration
	Multiplier      float64
	MaxElapsedTime  time.Duration

	currentInterval time.Duration
	startTime       time.Time
}

// NewExponentialBackoff creates a new exponential backoff.
func NewExponentialBackoff() *ExponentialBackoff {
	return &ExponentialBackoff{
		InitialInterval: DefaultInitialIntervalMS * time.Millisecond,
		MaxInterval:     DefaultMaxIntervalSec * time.Second,
		Multiplier:      ExponentialMultiplier,
		MaxElapsedTime:  DefaultMaxElapsedMin * time.Minute,
		currentInterval: time.Duration(0),
		startTime:       time.Time{},
	}
}

// NextBackOff returns the next backoff duration.
func (b *ExponentialBackoff) NextBackOff() time.Duration {
	// Initialize on first use
	if b.startTime.IsZero() {
		b.Reset()
	}

	// Check if max elapsed time exceeded
	if b.MaxElapsedTime > 0 {
		elapsed := time.Since(b.startTime)
		if elapsed > b.MaxElapsedTime {
			return -1 // Stop retrying
		}
	}

	// Calculate next interval
	defer func() {
		b.currentInterval = time.Duration(float64(b.currentInterval) * b.Multiplier)
		if b.currentInterval > b.MaxInterval {
			b.currentInterval = b.MaxInterval
		}
	}()

	// Add jitter (±25%) using crypto/rand
	maxJitter := b.currentInterval / JitterDivisor
	n, _ := crand.Int(crand.Reader, big.NewInt(int64(maxJitter)))
	jitter := time.Duration(n.Int64()) - b.currentInterval/JitterQuarter

	return b.currentInterval + jitter
}

// Reset resets the backoff to initial state.
func (b *ExponentialBackoff) Reset() {
	b.currentInterval = b.InitialInterval
	b.startTime = time.Now()
}

// Retry executes a function with exponential backoff.
func (b *ExponentialBackoff) Retry(ctx context.Context, retryableFunc RetryableFunc) error {
	b.Reset()

	for {
		err := retryableFunc(ctx)
		if err == nil {
			return nil
		}

		// Get next backoff duration
		backoff := b.NextBackOff()
		if backoff < 0 {
			return fmt.Errorf("operation failed: max elapsed time exceeded: %w", err)
		}

		logger.Debugf("Operation failed, retrying in %v: %v", backoff, err)

		// Wait with context
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()

			return fmt.Errorf("context cancelled during backoff: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

// RetryOnConflict retries an operation when a conflict occurs.
func RetryOnConflict(ctx context.Context, retryableFunc RetryableFunc) error {
	backoff := &ExponentialBackoff{
		InitialInterval: ConflictInitialIntervalMS * time.Millisecond,
		MaxInterval:     ConflictMaxIntervalSec * time.Second,
		Multiplier:      DefaultMultiplier,
		MaxElapsedTime:  ConflictMaxElapsedSec * time.Second,
		currentInterval: time.Duration(0),
		startTime:       time.Time{},
	}

	return backoff.Retry(ctx, func(ctx context.Context) error {
		err := retryableFunc(ctx)
		if err != nil && IsAlreadyExists(err) {
			return err // Retry on conflict
		}

		return err
	})
}

// WaitForCondition waits for a condition to be true.
func WaitForCondition(ctx context.Context, interval time.Duration, timeout time.Duration,
	condition func() (bool, error)) error {
	deadline := time.Now().Add(timeout)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		// Check condition
		done, err := condition()
		if err != nil {
			return err
		}

		if done {
			return nil
		}

		// Check timeout
		if time.Now().After(deadline) {
			return ErrTimeoutWaitingForCondition(timeout.String())
		}

		// Wait for next check
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during wait condition: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// RateLimiter provides rate limiting functionality.
type RateLimiter struct {
	rate   int // requests per second
	burst  int // burst size
	tokens chan struct{}
	ticker *time.Ticker
	stopCh chan struct{}
}

// NewRateLimiter creates a new rate limiter.
func NewRateLimiter(rate int, burst int) *RateLimiter {
	rateLimiter := &RateLimiter{
		rate:   rate,
		burst:  burst,
		tokens: make(chan struct{}, burst),
		ticker: nil,
		stopCh: make(chan struct{}),
	}

	// Fill initial tokens
	for range burst {
		rateLimiter.tokens <- struct{}{}
	}

	// Start token refill goroutine
	rateLimiter.ticker = time.NewTicker(time.Second / time.Duration(rate))
	go rateLimiter.refill()

	return rateLimiter
}

// Stop stops the rate limiter.
func (rateLimiter *RateLimiter) Stop() {
	close(rateLimiter.stopCh)
	rateLimiter.ticker.Stop()
}

// Wait blocks until a token is available.
func (rateLimiter *RateLimiter) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("context cancelled during rate limiter wait: %w", ctx.Err())
	case <-rateLimiter.tokens:
		return nil
	}
}

// refill adds tokens at the configured rate.
func (rateLimiter *RateLimiter) refill() {
	for {
		select {
		case <-rateLimiter.stopCh:
			return
		case <-rateLimiter.ticker.C:
			select {
			case rateLimiter.tokens <- struct{}{}:
			default:
				// Channel full, skip
			}
		}
	}
}

// CircuitBreaker implements the circuit breaker pattern.
type CircuitBreaker struct {
	maxFailures      int
	resetTimeout     time.Duration
	halfOpenRequests int

	failures    int
	lastFailure time.Time
	state       string // closed, open, half-open
}

// NewCircuitBreaker creates a new circuit breaker.
func NewCircuitBreaker(maxFailures int, resetTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		maxFailures:      maxFailures,
		resetTimeout:     resetTimeout,
		halfOpenRequests: 1,
		failures:         0,
		lastFailure:      time.Time{},
		state:            "closed",
	}
}

// Call executes a function with circuit breaker protection.
func (cb *CircuitBreaker) Call(ctx context.Context, retryableFunc RetryableFunc) error {
	// Check circuit state
	if cb.state == "open" {
		if time.Since(cb.lastFailure) > cb.resetTimeout {
			cb.state = "half-open"
			cb.failures = 0
		} else {
			return ErrCircuitBreakerOpen
		}
	}

	// Execute function
	err := retryableFunc(ctx)

	// Update circuit state based on result
	if err != nil {
		cb.failures++
		cb.lastFailure = time.Now()

		if cb.failures >= cb.maxFailures {
			cb.state = "open"
			logger.Warnf("Circuit breaker opened after %d failures", cb.failures)
		}

		return err
	}

	// Success - reset if needed
	if cb.state == "half-open" {
		cb.state = "closed"
		cb.failures = 0

		logger.Info("Circuit breaker closed")
	}

	return nil
}

// Reset resets the circuit breaker.
func (cb *CircuitBreaker) Reset() {
	cb.state = "closed"
	cb.failures = 0
	cb.lastFailure = time.Time{}
}
