package gcp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	// DefaultMaxAttempts is the default maximum number of retry attempts.
	DefaultMaxAttempts = 3
	// DefaultInitialDelay is the default initial delay between retries.
	DefaultInitialDelay = 2 * time.Second
	// DefaultMaxDelay is the default maximum delay between retries.
	DefaultMaxDelay = 30 * time.Second
	// DefaultMultiplier is the default backoff multiplier.
	DefaultMultiplier = 2.0
	// DefaultJitterFactor is the default jitter factor (0-1).
	DefaultJitterFactor = 0.1
)

// RetryConfig holds retry configuration.
type RetryConfig struct {
	MaxAttempts  int
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Multiplier   float64
	JitterFactor float64
}

// DefaultRetryConfig returns a default retry configuration.
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxAttempts:  DefaultMaxAttempts,
		InitialDelay: DefaultInitialDelay,
		MaxDelay:     DefaultMaxDelay,
		Multiplier:   DefaultMultiplier,
		JitterFactor: DefaultJitterFactor,
	}
}

// RetryableFunc is a function that can be retried.
type RetryableFunc func() error

// WithRetry executes a function with retry logic.
func WithRetry(ctx context.Context, operation string, fn RetryableFunc) error {
	return WithRetryConfig(ctx, operation, DefaultRetryConfig(), fn)
}

// WithRetryConfig executes a function with custom retry configuration.
//
//nolint:funlen // retry logic with context, backoff, and jitter must remain together
func WithRetryConfig(ctx context.Context, operation string, config *RetryConfig, fn RetryableFunc) error { //nolint:varnamelen
	if config == nil {
		config = DefaultRetryConfig()
	}

	var lastErr error

	delay := config.InitialDelay

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		// Check context before each attempt
		select {
		case <-ctx.Done():
			return fmt.Errorf("operation %s cancelled before attempt %d: %w", operation, attempt, ctx.Err())
		default:
		}

		// Execute the function
		err := fn()
		if err == nil {
			if attempt > 1 {
				logger.Debugw("Operation succeeded after retry",
					"operation", operation,
					"attempt", attempt)
			}

			return nil
		}

		lastErr = err

		// Check if error is retryable
		if !IsRetryable(err) {
			logger.Debugw("Non-retryable error, not retrying",
				"operation", operation,
				"error", err.Error())

			return err
		}

		// Don't retry if this was the last attempt
		if attempt >= config.MaxAttempts {
			break
		}

		// Log retry attempt
		logger.Debugw("Retrying operation",
			"operation", operation,
			"attempt", attempt,
			"maxAttempts", config.MaxAttempts,
			"delay", delay.String(),
			"error", err.Error())

		// Wait with exponential backoff and jitter
		jitteredDelay := addJitter(delay, config.JitterFactor)
		select {
		case <-ctx.Done():
			return fmt.Errorf("operation %s cancelled during backoff: %w", operation, ctx.Err())
		case <-time.After(jitteredDelay):
		}

		// Calculate next delay with exponential backoff
		delay = time.Duration(float64(delay) * config.Multiplier)
		if delay > config.MaxDelay {
			delay = config.MaxDelay
		}
	}

	logger.Debugw("Operation failed after max retries",
		"operation", operation,
		"maxAttempts", config.MaxAttempts,
		"error", lastErr.Error())

	return lastErr
}

// addJitter adds random jitter to a delay.
func addJitter(delay time.Duration, jitterFactor float64) time.Duration {
	if jitterFactor <= 0 {
		return delay
	}

	// Add random jitter between -jitterFactor and +jitterFactor
	jitter := (rand.Float64()*2 - 1) * jitterFactor * float64(delay) // #nosec G404 -- jitter does not need crypto-grade randomness

	return delay + time.Duration(jitter)
}

// OperationWaiter waits for a GCP operation to complete with polling.
type OperationWaiter struct {
	PollInterval time.Duration
	Timeout      time.Duration
}

// DefaultOperationWaiter returns a default operation waiter.
func DefaultOperationWaiter() *OperationWaiter {
	return &OperationWaiter{
		PollInterval: 5 * time.Second,  //nolint:mnd
		Timeout:      10 * time.Minute, //nolint:mnd
	}
}

// OperationStatus represents the status of an operation.
type OperationStatus struct {
	Done   bool
	Error  error
	Status string
}

// OperationChecker is a function that checks operation status.
type OperationChecker func(ctx context.Context) (*OperationStatus, error)

// Wait waits for an operation to complete.
func (w *OperationWaiter) Wait(ctx context.Context, operation string, check OperationChecker) error {
	timeout := w.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute //nolint:mnd
	}

	pollInterval := w.PollInterval
	if pollInterval == 0 {
		pollInterval = 5 * time.Second //nolint:mnd
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return ErrOperationTimeout
			}

			return fmt.Errorf("operation %s cancelled: %w", operation, ctx.Err())

		case <-ticker.C:
			status, err := check(ctx)
			if err != nil {
				logger.Debugw("Error checking operation status",
					"operation", operation,
					"error", err.Error())
				// Continue polling unless it's a fatal error
				if !IsRetryable(err) {
					return err
				}

				continue
			}

			if status.Done {
				if status.Error != nil {
					return status.Error
				}

				logger.Debugw("Operation completed",
					"operation", operation,
					"status", status.Status)

				return nil
			}

			logger.Debugw("Waiting for operation",
				"operation", operation,
				"status", status.Status)
		}
	}
}

// ResourceWaiter waits for a resource to reach a desired state.
type ResourceWaiter struct {
	PollInterval time.Duration
	Timeout      time.Duration
}

// DefaultResourceWaiter returns a default resource waiter.
func DefaultResourceWaiter() *ResourceWaiter {
	return &ResourceWaiter{
		PollInterval: 5 * time.Second, //nolint:mnd
		Timeout:      5 * time.Minute, //nolint:mnd
	}
}

// ResourceStateChecker is a function that checks resource state.
type ResourceStateChecker func(ctx context.Context) (string, error)

// WaitForState waits for a resource to reach one of the desired states.
func (w *ResourceWaiter) WaitForState(ctx context.Context, resourceName string, desiredStates []string, check ResourceStateChecker) error {
	timeout := w.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute //nolint:mnd
	}

	pollInterval := w.PollInterval
	if pollInterval == 0 {
		pollInterval = 5 * time.Second //nolint:mnd
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	desiredStateSet := make(map[string]bool)
	for _, state := range desiredStates {
		desiredStateSet[state] = true
	}

	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return ErrOperationTimeout
			}

			return fmt.Errorf("waiting for resource %s cancelled: %w", resourceName, ctx.Err())

		case <-ticker.C:
			state, err := check(ctx)
			if err != nil {
				// If resource not found, keep waiting (might still be creating)
				if IsNotFound(err) {
					continue
				}

				return err
			}

			if desiredStateSet[state] {
				logger.Debugw("Resource reached desired state",
					"resource", resourceName,
					"state", state)

				return nil
			}

			// Check for error states
			if state == OperationStateFailed || state == "ERROR" {
				return ErrVolumeErrorState
			}

			logger.Debugw("Waiting for resource state",
				"resource", resourceName,
				"currentState", state,
				"desiredStates", desiredStates)
		}
	}
}

// ExponentialBackoff calculates exponential backoff delay.
func ExponentialBackoff(attempt int, baseDelay, maxDelay time.Duration, multiplier float64) time.Duration {
	if attempt <= 0 {
		return baseDelay
	}

	delay := time.Duration(float64(baseDelay) * math.Pow(multiplier, float64(attempt-1)))
	if delay > maxDelay {
		delay = maxDelay
	}

	return delay
}
