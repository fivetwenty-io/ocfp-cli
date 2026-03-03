package azure

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// RetryConfig configures retry behavior.
type RetryConfig struct {
	MaxRetries     int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	BackoffFactor  float64
	Jitter         float64 // 0.0 to 1.0
}

// DefaultRetryConfig returns the default retry configuration.
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxRetries:     3, //nolint:mnd
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     30 * time.Second, //nolint:mnd
		BackoffFactor:  2.0, //nolint:mnd
		Jitter:         0.2, //nolint:mnd
	}
}

// RetryableFunc is a function that can be retried.
type RetryableFunc func() error

// IsRetryableFunc determines if an error is retryable.
type IsRetryableFunc func(error) bool

// RetryWithBackoff retries a function with exponential backoff.
func RetryWithBackoff(ctx context.Context, config *RetryConfig, isRetryable IsRetryableFunc, fn RetryableFunc) error { //nolint:varnamelen // fn is clear in context
	if config == nil {
		config = DefaultRetryConfig()
	}

	var lastErr error

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		// Check context before attempting
		if ctx.Err() != nil {
			return fmt.Errorf("retry cancelled before attempt %d: %w", attempt, ctx.Err())
		}

		// Execute the function
		lastErr = fn()
		if lastErr == nil {
			return nil
		}

		// Check if we should retry
		if !isRetryable(lastErr) {
			return lastErr
		}

		// Don't sleep after the last attempt
		if attempt == config.MaxRetries {
			break
		}

		// Calculate backoff with jitter
		backoff := calculateBackoff(config, attempt)

		logger.Debugw("Retrying after error",
			"attempt", attempt+1,
			"maxRetries", config.MaxRetries,
			"backoff", backoff,
			"error", lastErr)

		// Sleep with context cancellation support
		select {
		case <-ctx.Done():
			return fmt.Errorf("retry cancelled during backoff at attempt %d: %w", attempt, ctx.Err())
		case <-time.After(backoff):
			// Continue to next attempt
		}
	}

	return lastErr
}

// calculateBackoff calculates the backoff duration for a given attempt.
func calculateBackoff(config *RetryConfig, attempt int) time.Duration {
	// Exponential backoff
	backoff := float64(config.InitialBackoff) * math.Pow(config.BackoffFactor, float64(attempt))

	// Cap at max backoff
	if backoff > float64(config.MaxBackoff) {
		backoff = float64(config.MaxBackoff)
	}

	// Add jitter
	if config.Jitter > 0 {
		jitterRange := backoff * config.Jitter
		jitter := (rand.Float64() * 2 * jitterRange) - jitterRange //nolint:gosec // weak rand is fine for jitter
		backoff += jitter
	}

	return time.Duration(backoff)
}

// RetryOperation retries an operation using the standard Azure retry logic.
func RetryOperation(ctx context.Context, maxRetries int, fn RetryableFunc) error { //nolint:varnamelen // fn is clear in context
	config := &RetryConfig{
		MaxRetries:     maxRetries,
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     30 * time.Second, //nolint:mnd
		BackoffFactor:  2.0, //nolint:mnd
		Jitter:         0.2, //nolint:mnd
	}

	return RetryWithBackoff(ctx, config, IsRetryable, fn)
}

// CircuitBreaker implements a simple circuit breaker pattern.
type CircuitBreaker struct {
	failures     int
	threshold    int
	resetTimeout time.Duration
	lastFailure  time.Time
	state        CircuitState
}

// CircuitState represents the state of the circuit breaker.
type CircuitState int

const (
	// CircuitClosed means the circuit is operating normally.
	CircuitClosed CircuitState = iota
	// CircuitOpen means the circuit is tripped and requests will fail fast.
	CircuitOpen
	// CircuitHalfOpen means the circuit is testing if the service is recovered.
	CircuitHalfOpen
)

// NewCircuitBreaker creates a new circuit breaker.
func NewCircuitBreaker(threshold int, resetTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		threshold:    threshold,
		resetTimeout: resetTimeout,
		state:        CircuitClosed,
	}
}

// Allow checks if the request should be allowed.
func (cb *CircuitBreaker) Allow() bool {
	switch cb.state {
	case CircuitClosed:
		return true
	case CircuitOpen:
		// Check if we should transition to half-open
		if time.Since(cb.lastFailure) > cb.resetTimeout {
			cb.state = CircuitHalfOpen

			return true
		}

		return false
	case CircuitHalfOpen:
		return true
	default:
		return true
	}
}

// RecordSuccess records a successful operation.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.failures = 0
	cb.state = CircuitClosed
}

// RecordFailure records a failed operation.
func (cb *CircuitBreaker) RecordFailure() {
	cb.failures++
	cb.lastFailure = time.Now()

	if cb.failures >= cb.threshold {
		cb.state = CircuitOpen
	}
}

// State returns the current circuit state.
func (cb *CircuitBreaker) State() CircuitState {
	return cb.state
}

// Reset resets the circuit breaker to closed state.
func (cb *CircuitBreaker) Reset() {
	cb.failures = 0
	cb.state = CircuitClosed
}
