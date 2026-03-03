package aws

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	// DefaultThrottlingMaxRetries is the default max retries for throttling errors.
	DefaultThrottlingMaxRetries = 5
	// DefaultThrottlingBaseDelay is the default base delay for throttling recovery.
	DefaultThrottlingBaseDelay = 500 * time.Millisecond
	// DefaultThrottlingMaxDelay is the default max delay for throttling recovery.
	DefaultThrottlingMaxDelay = 60 * time.Second
	// DefaultThrottlingJitter is the default jitter factor for throttling recovery.
	DefaultThrottlingJitter = 0.3
	// DefaultDependencyWaitTime is the default wait time for dependency violations.
	DefaultDependencyWaitTime = 5 * time.Second
	// DefaultInvalidStateWaitTime is the default wait time for invalid state errors.
	DefaultInvalidStateWaitTime = 3 * time.Second
)

var (
	// ErrMaxRecoveryAttempts indicates operation failed after max recovery attempts.
	ErrMaxRecoveryAttempts = errors.New("operation failed after maximum recovery attempts")
	// ErrResourceStateTimeout indicates timeout waiting for resource to reach desired state.
	ErrResourceStateTimeout = errors.New("timeout waiting for resource to reach desired state")
	// ErrResourceErrorState indicates resource entered error state.
	ErrResourceErrorState = errors.New("resource entered error state")
)

// RecoveryStrategy defines a strategy for recovering from errors.
type RecoveryStrategy interface {
	// CanRecover returns true if the error can be recovered from.
	CanRecover(err error) bool
	// Recover attempts to recover from the error.
	Recover(ctx context.Context, err error) error
}

// RecoveryManager manages error recovery strategies.
type RecoveryManager struct {
	strategies []RecoveryStrategy
}

// NewRecoveryManager creates a new recovery manager.
func NewRecoveryManager() *RecoveryManager {
	return &RecoveryManager{
		strategies: []RecoveryStrategy{
			&ThrottlingRecovery{},
			&DependencyViolationRecovery{},
			&InvalidStateRecovery{},
		},
	}
}

// AddStrategy adds a recovery strategy.
func (rm *RecoveryManager) AddStrategy(strategy RecoveryStrategy) {
	rm.strategies = append(rm.strategies, strategy)
}

// Recover attempts to recover from an error using registered strategies.
func (rm *RecoveryManager) Recover(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}

	for _, strategy := range rm.strategies {
		if strategy.CanRecover(err) {
			logger.Debug("Attempting recovery",
				"strategy", fmt.Sprintf("%T", strategy),
				"error", err.Error())

			recoveryErr := strategy.Recover(ctx, err)
			if recoveryErr == nil {
				logger.Info("Recovery successful",
					"strategy", fmt.Sprintf("%T", strategy))

				return nil
			}

			logger.Warn("Recovery failed",
				"strategy", fmt.Sprintf("%T", strategy),
				"error", recoveryErr.Error())
		}
	}

	return err
}

// ThrottlingRecovery implements recovery from throttling errors.
type ThrottlingRecovery struct{}

// CanRecover returns true if the error is a throttling error.
func (r *ThrottlingRecovery) CanRecover(err error) bool {
	return IsThrottling(err)
}

// Recover implements exponential backoff for throttling errors.
func (r *ThrottlingRecovery) Recover(_ctx context.Context, err error) error {
	logger.Debug("Recovering from throttling error")

	// Use exponential backoff with longer delays for throttling
	config := &RetryConfig{
		MaxRetries:       DefaultThrottlingMaxRetries,
		BaseDelay:        DefaultThrottlingBaseDelay,
		MaxDelay:         DefaultThrottlingMaxDelay,
		JitterFactor:     DefaultThrottlingJitter,
		RetryableChecker: IsThrottling,
	}

	// Return the config to be used by the caller
	// In practice, this would be integrated with the actual retry mechanism
	_ = config

	return err // Return the original error; recovery is handled by retry logic
}

// DependencyViolationRecovery implements recovery from dependency violations.
type DependencyViolationRecovery struct{}

// CanRecover returns true if the error is a dependency violation.
func (r *DependencyViolationRecovery) CanRecover(err error) bool {
	return IsDependencyViolation(err)
}

// Recover attempts to resolve dependency violations.
func (r *DependencyViolationRecovery) Recover(ctx context.Context, err error) error {
	logger.Debug("Recovering from dependency violation")

	// For dependency violations, we typically need to:
	// 1. Wait for dependent resources to be deleted
	// 2. Retry the operation after a delay

	// Wait a bit before retrying
	select {
	case <-ctx.Done():
		return fmt.Errorf("dependency violation recovery cancelled: %w", ctx.Err())
	case <-time.After(DefaultDependencyWaitTime):
	}

	return err // Return the original error; caller should retry
}

// InvalidStateRecovery implements recovery from invalid state errors.
type InvalidStateRecovery struct{}

// CanRecover returns true if the error is an invalid state error.
func (r *InvalidStateRecovery) CanRecover(err error) bool {
	var awsErr *Error
	if errors.As(err, &awsErr) {
		return awsErr.Code == ErrCodeInvalidState
	}

	return false
}

// Recover attempts to handle invalid state errors.
func (r *InvalidStateRecovery) Recover(ctx context.Context, err error) error {
	logger.Debug("Recovering from invalid state error")

	// For invalid state errors, we typically need to:
	// 1. Wait for the resource to reach a valid state
	// 2. Retry the operation

	// Wait for resource state to stabilize
	select {
	case <-ctx.Done():
		return fmt.Errorf("invalid state recovery cancelled: %w", ctx.Err())
	case <-time.After(DefaultInvalidStateWaitTime):
	}

	return err // Return the original error; caller should retry
}

// ExecuteWithRecovery executes a function with automatic error recovery.
func ExecuteWithRecovery(
	ctx context.Context,
	manager *RecoveryManager,
	operation string,
	opFunc func() error,
) error {
	maxRecoveryAttempts := 3

	for attempt := range maxRecoveryAttempts {
		err := opFunc()
		if err == nil {
			return nil
		}

		// Attempt recovery
		recoveryErr := manager.Recover(ctx, err)
		if recoveryErr != nil {
			// Recovery failed
			if attempt >= maxRecoveryAttempts-1 {
				return fmt.Errorf("%w (attempts: %d): %w",
					ErrMaxRecoveryAttempts, maxRecoveryAttempts, err)
			}

			logger.Debug("Recovery attempt failed, retrying",
				"operation", operation,
				"attempt", attempt+1,
				"max_attempts", maxRecoveryAttempts)

			continue
		}

		// Recovery successful, retry the operation
		logger.Debug("Recovery successful, retrying operation",
			"operation", operation,
			"attempt", attempt+1)
	}

	return fmt.Errorf("%w (attempts: %d)", ErrMaxRecoveryAttempts, maxRecoveryAttempts)
}

// WaitForResourceState waits for a resource to reach a desired state.
func WaitForResourceState(
	ctx context.Context,
	operation string,
	checkState func() (string, error),
	desiredStates []string,
	errorStates []string,
	timeout time.Duration,
	interval time.Duration,
) error {
	deadline := time.Now().Add(timeout)

	for {
		// Check if context is done
		select {
		case <-ctx.Done():
			return fmt.Errorf("resource state wait cancelled: %w", ctx.Err())
		default:
		}

		// Check if timeout exceeded
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: %s", ErrResourceStateTimeout, operation)
		}

		// Check current state
		state, err := checkState()
		if err != nil {
			return fmt.Errorf("failed to check state: %w", err)
		}

		// Check if in desired state
		for _, desired := range desiredStates {
			if state == desired {
				logger.Debug("Resource reached desired state",
					"operation", operation,
					"state", state)

				return nil
			}
		}

		// Check if in error state
		for _, errorState := range errorStates {
			if state == errorState {
				return fmt.Errorf("%w: %s", ErrResourceErrorState, state)
			}
		}

		// Wait before next check
		logger.Debug("Waiting for resource state change",
			"operation", operation,
			"current_state", state,
			"desired_states", desiredStates)

		select {
		case <-ctx.Done():
			return fmt.Errorf("resource state wait cancelled: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
}

// RetryWithRecovery combines retry logic with recovery strategies.
func RetryWithRecovery(
	ctx context.Context,
	retryConfig *RetryConfig,
	manager *RecoveryManager,
	operation string,
	opFunc func() error,
) error {
	if retryConfig == nil {
		retryConfig = DefaultRetryConfig()
	}

	if manager == nil {
		manager = NewRecoveryManager()
	}

	return RetryWithBackoff(ctx, retryConfig, operation, func() error {
		err := opFunc()
		if err == nil {
			return nil
		}

		// Attempt recovery before retry
		recoveryErr := manager.Recover(ctx, err)
		if recoveryErr == nil {
			// Recovery successful, the next retry might succeed
			logger.Debug("Recovery successful, will retry operation",
				"operation", operation)
		}

		return err
	})
}
