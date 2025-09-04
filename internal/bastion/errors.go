package bastion

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// ErrorType represents different categories of errors
type ErrorType string

const (
	ErrorTypeNetwork       ErrorType = "network"
	ErrorTypePermission    ErrorType = "permission"
	ErrorTypeConfiguration ErrorType = "configuration"
	ErrorTypeDependency    ErrorType = "dependency"
	ErrorTypeTimeout       ErrorType = "timeout"
	ErrorTypeRetryable     ErrorType = "retryable"
	ErrorTypeUnknown       ErrorType = "unknown"
)

// BastionError represents an error with context and retry information
type BastionError struct {
	Type         ErrorType
	Phase        string
	Command      string
	Message      string
	Cause        error
	Retryable    bool
	Suggestions  []string
	AttemptCount int
}

// Error implements the error interface
func (be *BastionError) Error() string {
	return fmt.Sprintf("[%s] %s: %s", be.Type, be.Phase, be.Message)
}

// Unwrap returns the underlying error
func (be *BastionError) Unwrap() error {
	return be.Cause
}

// ErrorHandler handles error classification and retry logic
type ErrorHandler struct {
	log        logger.Logger
	maxRetries int
	baseDelay  time.Duration
}

// NewErrorHandler creates a new error handler
func NewErrorHandler() *ErrorHandler {
	return &ErrorHandler{
		log:        logger.Get(),
		maxRetries: 3,
		baseDelay:  2 * time.Second,
	}
}

// ClassifyError analyzes an error and returns a BastionError with context
func (eh *ErrorHandler) ClassifyError(err error, phase, command string) *BastionError {
	if err == nil {
		return nil
	}

	errMsg := strings.ToLower(err.Error())

	bastionErr := &BastionError{
		Phase:   phase,
		Command: command,
		Message: err.Error(),
		Cause:   err,
	}

	// Network-related errors
	if eh.containsAny(errMsg, []string{
		"connection refused", "network unreachable", "timeout",
		"dial tcp", "no route to host", "connection timed out",
		"temporary failure", "dns resolution",
	}) {
		bastionErr.Type = ErrorTypeNetwork
		bastionErr.Retryable = true
		bastionErr.Suggestions = []string{
			"Check network connectivity",
			"Verify bastion host is running",
			"Check security group rules",
			"Verify DNS resolution",
		}
		return bastionErr
	}

	// Permission errors
	if eh.containsAny(errMsg, []string{
		"permission denied", "access denied", "operation not permitted",
		"sudo", "not allowed", "unauthorized", "authentication failed",
	}) {
		bastionErr.Type = ErrorTypePermission
		bastionErr.Retryable = false
		bastionErr.Suggestions = []string{
			"Check SSH key permissions (600 for private key)",
			"Verify SSH user has sudo access",
			"Check file/directory permissions",
			"Ensure SSH key matches bastion configuration",
		}
		return bastionErr
	}

	// Configuration errors
	if eh.containsAny(errMsg, []string{
		"config", "configuration", "not found", "missing",
		"invalid", "parse", "yaml", "json",
	}) {
		bastionErr.Type = ErrorTypeConfiguration
		bastionErr.Retryable = false
		bastionErr.Suggestions = []string{
			"Check OCFP configuration file exists",
			"Verify configuration file syntax",
			"Ensure all required fields are present",
			"Check file paths and permissions",
		}
		return bastionErr
	}

	// Dependency errors
	if eh.containsAny(errMsg, []string{
		"command not found", "no such file", "package", "dependency",
		"module", "library", "install", "apt-get", "snap",
	}) {
		bastionErr.Type = ErrorTypeDependency
		bastionErr.Retryable = true
		bastionErr.Suggestions = []string{
			"Update package cache: sudo apt-get update",
			"Check if package repositories are accessible",
			"Verify sufficient disk space for installations",
			"Check if conflicting packages are installed",
		}
		return bastionErr
	}

	// Timeout errors
	if eh.containsAny(errMsg, []string{
		"timeout", "deadline exceeded", "context canceled",
		"operation timed out",
	}) {
		bastionErr.Type = ErrorTypeTimeout
		bastionErr.Retryable = true
		bastionErr.Suggestions = []string{
			"Increase timeout values",
			"Check network latency",
			"Verify system load",
			"Consider running during off-peak hours",
		}
		return bastionErr
	}

	// Default to retryable for unknown errors
	bastionErr.Type = ErrorTypeUnknown
	bastionErr.Retryable = true
	bastionErr.Suggestions = []string{
		"Check system logs for more details",
		"Verify system resources (CPU, memory, disk)",
		"Review recent system changes",
		"Contact support if issue persists",
	}

	return bastionErr
}

// ExecuteWithRetry executes a function with retry logic
func (eh *ErrorHandler) ExecuteWithRetry(ctx context.Context, phase string, fn func() error) error {
	var lastErr *BastionError

	for attempt := 1; attempt <= eh.maxRetries; attempt++ {
		err := fn()
		if err == nil {
			return nil // Success
		}

		// Classify the error
		bastionErr := eh.ClassifyError(err, phase, "")
		bastionErr.AttemptCount = attempt

		// Log the error with context
		eh.log.Warn("Phase execution failed",
			"phase", phase,
			"attempt", attempt,
			"max_attempts", eh.maxRetries,
			"error_type", string(bastionErr.Type),
			"retryable", bastionErr.Retryable,
			"error", err.Error())

		// Don't retry if error is not retryable
		if !bastionErr.Retryable {
			eh.log.Error("Non-retryable error encountered",
				"phase", phase,
				"error_type", string(bastionErr.Type))
			eh.logSuggestions(bastionErr)
			return bastionErr
		}

		lastErr = bastionErr

		// Don't sleep after the last attempt
		if attempt == eh.maxRetries {
			break
		}

		// Calculate delay with exponential backoff
		delay := eh.calculateDelay(attempt)
		eh.log.Info("Retrying after delay",
			"phase", phase,
			"attempt", attempt+1,
			"delay", delay.String())

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
			// Continue to next attempt
		}
	}

	// All attempts failed
	eh.log.Error("All retry attempts failed",
		"phase", phase,
		"attempts", eh.maxRetries)
	eh.logSuggestions(lastErr)

	return fmt.Errorf("phase %s failed after %d attempts: %w", phase, eh.maxRetries, lastErr)
}

// calculateDelay calculates delay with exponential backoff
func (eh *ErrorHandler) calculateDelay(attempt int) time.Duration {
	// Exponential backoff: 2s, 4s, 8s, 16s, ...
	multiplier := 1 << (attempt - 1) // 2^(attempt-1)
	delay := time.Duration(multiplier) * eh.baseDelay

	// Cap at 30 seconds
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}

	return delay
}

// logSuggestions logs helpful suggestions for error recovery
func (eh *ErrorHandler) logSuggestions(bastionErr *BastionError) {
	if len(bastionErr.Suggestions) == 0 {
		return
	}

	eh.log.Info("Suggested recovery actions:")
	for i, suggestion := range bastionErr.Suggestions {
		eh.log.Info(fmt.Sprintf("  %d. %s", i+1, suggestion))
	}
}

// containsAny checks if a string contains any of the given substrings
func (eh *ErrorHandler) containsAny(text string, substrings []string) bool {
	for _, substr := range substrings {
		if strings.Contains(text, substr) {
			return true
		}
	}
	return false
}

// RecoverableErrorWrapper wraps operations to provide error context and recovery
type RecoverableErrorWrapper struct {
	handler *ErrorHandler
	phase   string
	log     logger.Logger
}

// NewRecoverableErrorWrapper creates a new error wrapper
func NewRecoverableErrorWrapper(phase string) *RecoverableErrorWrapper {
	return &RecoverableErrorWrapper{
		handler: NewErrorHandler(),
		phase:   phase,
		log:     logger.Get(),
	}
}

// Execute wraps function execution with error handling
func (rew *RecoverableErrorWrapper) Execute(ctx context.Context, operation func() error) error {
	return rew.handler.ExecuteWithRetry(ctx, rew.phase, operation)
}

// ExecuteCommand wraps SSH command execution with error handling
func (rew *RecoverableErrorWrapper) ExecuteCommand(ctx context.Context, sshClient SSHClient, cmd string) (*ssh.CommandResult, error) {
	var result *ssh.CommandResult
	var err error

	operation := func() error {
		result, err = sshClient.ExecuteCommand(ctx, cmd)
		return err
	}

	if execErr := rew.Execute(ctx, operation); execErr != nil {
		return result, execErr
	}

	return result, nil
}

// ExecuteTransfer wraps file transfer with error handling
func (rew *RecoverableErrorWrapper) ExecuteTransfer(ctx context.Context, sshClient SSHClient, local, remote string, opts ssh.TransferOptions) error {
	operation := func() error {
		return sshClient.TransferFile(ctx, local, remote, opts)
	}

	return rew.Execute(ctx, operation)
}
