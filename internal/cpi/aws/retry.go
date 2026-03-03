package aws

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	// DefaultMaxRetries is the default maximum number of retry attempts.
	DefaultMaxRetries = 3
	// DefaultBaseDelay is the default base delay for exponential backoff.
	DefaultBaseDelay = 100 * time.Millisecond
	// DefaultMaxDelay is the default maximum delay between retries.
	DefaultMaxDelay = 30 * time.Second
	// DefaultJitterFactor is the default jitter factor (0.0 to 1.0).
	DefaultJitterFactor = 0.3
	// DefaultCircuitBreakerMaxFailures is the default max failures before opening circuit.
	DefaultCircuitBreakerMaxFailures = 5
	// DefaultCircuitBreakerTimeout is the default timeout before half-open state.
	DefaultCircuitBreakerTimeout = 60 * time.Second
	// DefaultCircuitBreakerHalfOpenRequests is the default max requests in half-open state.
	DefaultCircuitBreakerHalfOpenRequests = 3
	// ExponentialBackoffBase is the base for exponential backoff calculation.
	ExponentialBackoffBase = 2
)

// RetryConfig configures retry behavior.
type RetryConfig struct {
	// MaxRetries is the maximum number of retry attempts.
	MaxRetries int
	// BaseDelay is the base delay for exponential backoff.
	BaseDelay time.Duration
	// MaxDelay is the maximum delay between retries.
	MaxDelay time.Duration
	// JitterFactor controls randomization (0.0 to 1.0).
	JitterFactor float64
	// RetryableChecker determines if an error is retryable.
	RetryableChecker func(error) bool
}

// DefaultRetryConfig returns the default retry configuration.
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxRetries:       DefaultMaxRetries,
		BaseDelay:        DefaultBaseDelay,
		MaxDelay:         DefaultMaxDelay,
		JitterFactor:     DefaultJitterFactor,
		RetryableChecker: IsRetryable,
	}
}

// RetryWithBackoff executes a function with exponential backoff retry logic.
func RetryWithBackoff(ctx context.Context, config *RetryConfig, operation string, opFunc func() error) error {
	if config == nil {
		config = DefaultRetryConfig()
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano())) //nolint:gosec // not cryptographic

	return executeRetryLoop(ctx, config, operation, opFunc, rng)
}

// executeRetryLoop performs the actual retry loop.
func executeRetryLoop(
	ctx context.Context,
	config *RetryConfig,
	operation string,
	opFunc func() error,
	rng *rand.Rand,
) error {
	var lastErr error

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		err := opFunc()
		if err == nil {
			logRetrySuccess(operation, attempt, config.MaxRetries)

			return nil
		}

		lastErr = err

		if !config.RetryableChecker(err) {
			logger.Debug("Error is not retryable",
				"operation", operation,
				"error", err.Error())

			return err
		}

		if attempt >= config.MaxRetries {
			logger.Debug("Max retries exhausted",
				"operation", operation,
				"attempts", attempt+1,
				"error", err.Error())

			break
		}

		waitErr := performRetryWait(ctx, config, operation, attempt, err, rng)
		if waitErr != nil {
			return waitErr
		}
	}

	return fmt.Errorf("operation failed after %d attempts: %w", config.MaxRetries+1, lastErr)
}

// logRetrySuccess logs successful retry.
func logRetrySuccess(operation string, attempt, maxRetries int) {
	if attempt > 0 {
		logger.Debug("Retry succeeded",
			"operation", operation,
			"attempt", attempt+1,
			"total_attempts", maxRetries+1)
	}
}

// performRetryWait waits before retrying an operation.
func performRetryWait(
	ctx context.Context,
	config *RetryConfig,
	operation string,
	attempt int,
	err error,
	rng *rand.Rand,
) error {
	delay := calculateBackoff(attempt, config, rng)

	select {
	case <-ctx.Done():
		return fmt.Errorf("operation cancelled during retry: %w", ctx.Err())
	default:
	}

	logger.Debug("Retrying operation",
		"operation", operation,
		"attempt", attempt+1,
		"max_attempts", config.MaxRetries+1,
		"delay", delay,
		"error", err.Error())

	select {
	case <-ctx.Done():
		return fmt.Errorf("operation cancelled during backoff: %w", ctx.Err())
	case <-time.After(delay):
	}

	return nil
}

// calculateBackoff calculates the delay for a given retry attempt.
func calculateBackoff(attempt int, config *RetryConfig, rng *rand.Rand) time.Duration {
	// Exponential backoff: baseDelay * 2^attempt
	exponentialDelay := float64(config.BaseDelay) * math.Pow(ExponentialBackoffBase, float64(attempt))

	// Cap at max delay
	if exponentialDelay > float64(config.MaxDelay) {
		exponentialDelay = float64(config.MaxDelay)
	}

	// Add jitter: randomize ± jitterFactor
	jitter := 1.0
	if rng != nil && config.JitterFactor > 0 {
		jitter = 1.0 + (rng.Float64()*2-1)*config.JitterFactor
	}

	delay := time.Duration(exponentialDelay * jitter)

	// Ensure delay is at least base delay
	if delay < config.BaseDelay {
		delay = config.BaseDelay
	}

	return delay
}

// CircuitBreakerState represents the state of a circuit breaker.
type CircuitBreakerState int

const (
	// StateClosed means requests are allowed.
	StateClosed CircuitBreakerState = iota
	// StateOpen means requests are blocked.
	StateOpen
	// StateHalfOpen means limited requests are allowed for testing.
	StateHalfOpen
)

func (s CircuitBreakerState) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreakerConfig configures a circuit breaker.
type CircuitBreakerConfig struct {
	// MaxFailures is the number of consecutive failures before opening.
	MaxFailures int
	// Timeout is how long to wait before transitioning from open to half-open.
	Timeout time.Duration
	// HalfOpenMaxRequests is the max requests allowed in half-open state.
	HalfOpenMaxRequests int
}

// DefaultCircuitBreakerConfig returns the default circuit breaker configuration.
func DefaultCircuitBreakerConfig() *CircuitBreakerConfig {
	return &CircuitBreakerConfig{
		MaxFailures:         DefaultCircuitBreakerMaxFailures,
		Timeout:             DefaultCircuitBreakerTimeout,
		HalfOpenMaxRequests: DefaultCircuitBreakerHalfOpenRequests,
	}
}

// CircuitBreaker implements the circuit breaker pattern.
type CircuitBreaker struct {
	config *CircuitBreakerConfig
	mu     sync.RWMutex

	state              CircuitBreakerState
	failures           int
	consecutiveSuccess int
	lastFailureTime    time.Time
	halfOpenRequests   int
}

// NewCircuitBreaker creates a new circuit breaker.
func NewCircuitBreaker(config *CircuitBreakerConfig) *CircuitBreaker {
	if config == nil {
		config = DefaultCircuitBreakerConfig()
	}

	return &CircuitBreaker{
		config:             config,
		state:              StateClosed,
		failures:           0,
		consecutiveSuccess: 0,
		lastFailureTime:    time.Time{},
		halfOpenRequests:   0,
	}
}

// Execute runs a function through the circuit breaker.
func (cb *CircuitBreaker) Execute(_ctx context.Context, operation string, opFunc func() error) error {
	// Check if circuit breaker allows the request
	if !cb.canAttempt() {
		return &CircuitBreakerError{
			State:     cb.state,
			Operation: operation,
		}
	}

	// Execute the operation
	err := opFunc()

	// Record the result
	cb.recordResult(err, operation)

	return err
}

// State returns the current state of the circuit breaker.
func (cb *CircuitBreaker) State() CircuitBreakerState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	return cb.state
}

// Reset resets the circuit breaker to closed state.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.state = StateClosed
	cb.failures = 0
	cb.consecutiveSuccess = 0
	cb.halfOpenRequests = 0

	logger.Debug("Circuit breaker reset")
}

// canAttempt checks if a request can be attempted.
func (cb *CircuitBreaker) canAttempt() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case StateClosed:
		return true

	case StateOpen:
		// Check if timeout has elapsed
		if time.Since(cb.lastFailureTime) > cb.config.Timeout {
			cb.state = StateHalfOpen
			cb.halfOpenRequests = 0

			logger.Debug("Circuit breaker transitioning to half-open")

			return true
		}

		return false

	case StateHalfOpen:
		// Allow limited requests in half-open state
		if cb.halfOpenRequests < cb.config.HalfOpenMaxRequests {
			cb.halfOpenRequests++

			return true
		}

		return false

	default:
		return false
	}
}

// recordResult records the result of an operation.
func (cb *CircuitBreaker) recordResult(err error, operation string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err == nil {
		cb.onSuccess(operation)
	} else {
		cb.onFailure(operation)
	}
}

// onSuccess handles a successful operation.
func (cb *CircuitBreaker) onSuccess(operation string) {
	cb.consecutiveSuccess++

	switch cb.state {
	case StateClosed:
		// Reset failure count
		cb.failures = 0

	case StateOpen:
		// Should not happen as requests are blocked in open state
		logger.Warnw("Success recorded in open state", "operation", operation)

	case StateHalfOpen:
		// If enough successes in half-open, close the circuit
		if cb.consecutiveSuccess >= cb.config.HalfOpenMaxRequests {
			cb.state = StateClosed
			cb.failures = 0
			cb.consecutiveSuccess = 0

			logger.Info("Circuit breaker closed after successful half-open tests",
				"operation", operation)
		}
	}
}

// onFailure handles a failed operation.
func (cb *CircuitBreaker) onFailure(operation string) {
	cb.failures++
	cb.consecutiveSuccess = 0
	cb.lastFailureTime = time.Now()

	switch cb.state {
	case StateClosed:
		// Open if max failures reached
		if cb.failures >= cb.config.MaxFailures {
			cb.state = StateOpen
			logger.Warn("Circuit breaker opened due to consecutive failures",
				"operation", operation,
				"failures", cb.failures)
		}

	case StateOpen:
		// Already open, just update failure count
		logger.Debugw("Failure recorded in open state", "operation", operation)

	case StateHalfOpen:
		// Return to open state on failure
		cb.state = StateOpen

		logger.Warn("Circuit breaker reopened after half-open failure",
			"operation", operation)
	}
}

// CircuitBreakerError represents an error when the circuit breaker is open.
type CircuitBreakerError struct {
	State     CircuitBreakerState
	Operation string
}

func (e *CircuitBreakerError) Error() string {
	return fmt.Sprintf("circuit breaker is %s for operation: %s", e.State, e.Operation)
}

// IsCircuitBreakerOpen checks if an error is due to an open circuit breaker.
func IsCircuitBreakerOpen(err error) bool {
	var cbErr *CircuitBreakerError

	return errors.As(err, &cbErr)
}
