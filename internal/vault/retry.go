package vault

import (
	"fmt"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// RetryConfig holds retry configuration
type RetryConfig struct {
	MaxAttempts   int
	BaseDelay     time.Duration
	MaxDelay      time.Duration
	BackoffFactor float64
}

// DefaultRetryConfig returns sensible retry defaults
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxAttempts:   3,
		BaseDelay:     1 * time.Second,
		MaxDelay:      30 * time.Second,
		BackoffFactor: 2.0,
	}
}

// RetryableError represents an error that can be retried
type RetryableError struct {
	Err       error
	Retryable bool
}

func (re *RetryableError) Error() string {
	return re.Err.Error()
}

// IsRetryable checks if an error should trigger a retry
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}

	// Check for specific error patterns that indicate transient failures
	errMsg := err.Error()

	// Network-related errors
	if containsAny(errMsg, []string{
		"connection refused",
		"connection reset",
		"connection timeout",
		"network unreachable",
		"temporary failure",
		"server temporarily unavailable",
		"too many requests",
		"rate limit",
		"deadline exceeded",
		"context deadline exceeded",
	}) {
		return true
	}

	// HTTP status codes that suggest retrying
	if containsAny(errMsg, []string{
		"502 Bad Gateway",
		"503 Service Unavailable",
		"504 Gateway Timeout",
		"429 Too Many Requests",
		"500 Internal Server Error",
	}) {
		return true
	}

	return false
}

// containsAny checks if a string contains any of the provided substrings
func containsAny(s string, substrings []string) bool {
	for _, sub := range substrings {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

// WithRetry executes a function with exponential backoff retry logic
func WithRetry(operation func() error, config *RetryConfig) error {
	if config == nil {
		config = DefaultRetryConfig()
	}

	log := logger.Get()
	var lastErr error

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		if attempt > 1 {
			delay := calculateDelay(attempt-1, config)
			log.Debug("Retrying operation", "attempt", attempt, "delay", delay)
			time.Sleep(delay)
		}

		err := operation()
		if err == nil {
			if attempt > 1 {
				log.Info("Operation succeeded after retry", "attempts", attempt)
			}
			return nil
		}

		lastErr = err

		// Check if we should retry
		if !IsRetryable(err) {
			log.Debug("Error not retryable, giving up", "error", err, "attempt", attempt)
			return &VaultError{
				Operation: "retry",
				Err:       err,
				Retryable: false,
			}
		}

		if attempt == config.MaxAttempts {
			log.Warn("Operation failed after all retry attempts", "attempts", attempt, "error", err)
		} else {
			log.Debug("Operation failed, will retry", "attempt", attempt, "error", err)
		}
	}

	return &VaultError{
		Operation: "retry",
		Err:       fmt.Errorf("operation failed after %d attempts: %w", config.MaxAttempts, lastErr),
		Retryable: false,
	}
}

// calculateDelay calculates the delay for a given attempt using exponential backoff
func calculateDelay(attempt int, config *RetryConfig) time.Duration {
	delay := config.BaseDelay
	for i := 0; i < attempt; i++ {
		delay = time.Duration(float64(delay) * config.BackoffFactor)
	}

	if delay > config.MaxDelay {
		delay = config.MaxDelay
	}

	return delay
}

// VaultError represents a vault-specific error with context
type VaultError struct {
	Operation string
	Path      string
	Key       string
	Err       error
	Retryable bool
}

func (ve *VaultError) Error() string {
	if ve.Path != "" && ve.Key != "" {
		return fmt.Sprintf("vault %s failed at %s:%s: %v", ve.Operation, ve.Path, ve.Key, ve.Err)
	} else if ve.Path != "" {
		return fmt.Sprintf("vault %s failed at %s: %v", ve.Operation, ve.Path, ve.Err)
	}
	return fmt.Sprintf("vault %s failed: %v", ve.Operation, ve.Err)
}

func (ve *VaultError) Unwrap() error {
	return ve.Err
}

func (ve *VaultError) IsRetryable() bool {
	return ve.Retryable
}

// NewVaultError creates a new VaultError
func NewVaultError(operation, path, key string, err error) *VaultError {
	return &VaultError{
		Operation: operation,
		Path:      path,
		Key:       key,
		Err:       err,
		Retryable: IsRetryable(err),
	}
}

// RetryableVaultOperation wraps a vault operation with retry logic
func RetryableVaultOperation(operation, path, key string, fn func() error) error {
	return WithRetry(func() error {
		err := fn()
		if err != nil {
			return NewVaultError(operation, path, key, err)
		}
		return nil
	}, DefaultRetryConfig())
}
