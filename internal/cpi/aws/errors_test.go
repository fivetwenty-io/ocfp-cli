package aws

import (
	"errors"
	"net/http"
	"testing"

	"github.com/aws/smithy-go"
)

func TestError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *Error
		expected string
	}{
		{
			name: "with operation",
			err: &Error{
				Code:      ErrCodeNotFound,
				Message:   "resource not found",
				RequestID: "req-123",
				Operation: "GetInstance",
			},
			expected: "[AWS:GetInstance] NotFound: resource not found (request: req-123)",
		},
		{
			name: "without operation",
			err: &Error{
				Code:      ErrCodeThrottling,
				Message:   "rate limit exceeded",
				RequestID: "req-456",
			},
			expected: "[AWS] Throttling: rate limit exceeded (request: req-456)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Error() != tt.expected {
				t.Errorf("expected '%s', got '%s'", tt.expected, tt.err.Error())
			}
		})
	}
}

func TestError_IsRetryable(t *testing.T) {
	tests := []struct {
		name      string
		err       *Error
		retryable bool
	}{
		{
			name: "throttling error",
			err: &Error{
				Code: ErrCodeThrottling,
			},
			retryable: true,
		},
		{
			name: "request limit exceeded",
			err: &Error{
				Code: ErrCodeRequestLimitExceeded,
			},
			retryable: true,
		},
		{
			name: "service unavailable",
			err: &Error{
				Code: ErrCodeServiceUnavailable,
			},
			retryable: true,
		},
		{
			name: "internal error",
			err: &Error{
				Code: ErrCodeInternalError,
			},
			retryable: true,
		},
		{
			name: "http 429",
			err: &Error{
				StatusCode: http.StatusTooManyRequests,
			},
			retryable: true,
		},
		{
			name: "http 503",
			err: &Error{
				StatusCode: http.StatusServiceUnavailable,
			},
			retryable: true,
		},
		{
			name: "not found error",
			err: &Error{
				Code: ErrCodeNotFound,
			},
			retryable: false,
		},
		{
			name: "invalid parameter",
			err: &Error{
				Code: ErrCodeInvalidParameter,
			},
			retryable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.IsRetryable() != tt.retryable {
				t.Errorf("expected IsRetryable() = %v, got %v", tt.retryable, tt.err.IsRetryable())
			}
		})
	}
}

func TestWrapAWSError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		operation string
		wantNil   bool
	}{
		{
			name:      "nil error",
			err:       nil,
			operation: "test",
			wantNil:   true,
		},
		{
			name:      "generic error",
			err:       errors.New("generic error"),
			operation: "TestOperation",
			wantNil:   false,
		},
		{
			name: "smithy API error",
			err: &smithy.GenericAPIError{
				Code:    "NotFound",
				Message: "resource not found",
			},
			operation: "GetInstance",
			wantNil:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := WrapAWSError(tt.err, tt.operation)

			if tt.wantNil {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
				return
			}

			if result == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestIsNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		isNF bool
	}{
		{
			name: "not found error",
			err: &Error{
				Code: ErrCodeNotFound,
			},
			isNF: true,
		},
		{
			name: "other error",
			err: &Error{
				Code: ErrCodeThrottling,
			},
			isNF: false,
		},
		{
			name: "generic error",
			err:  errors.New("generic error"),
			isNF: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if IsNotFound(tt.err) != tt.isNF {
				t.Errorf("expected IsNotFound() = %v, got %v", tt.isNF, IsNotFound(tt.err))
			}
		})
	}
}

func TestIsAlreadyExists(t *testing.T) {
	tests := []struct {
		name string
		err  error
		isAE bool
	}{
		{
			name: "already exists error",
			err: &Error{
				Code: ErrCodeAlreadyExists,
			},
			isAE: true,
		},
		{
			name: "other error",
			err: &Error{
				Code: ErrCodeNotFound,
			},
			isAE: false,
		},
		{
			name: "generic error",
			err:  errors.New("generic error"),
			isAE: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if IsAlreadyExists(tt.err) != tt.isAE {
				t.Errorf("expected IsAlreadyExists() = %v, got %v", tt.isAE, IsAlreadyExists(tt.err))
			}
		})
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{
			name: "retryable error",
			err: &Error{
				Code: ErrCodeThrottling,
			},
			retryable: true,
		},
		{
			name: "non-retryable error",
			err: &Error{
				Code: ErrCodeNotFound,
			},
			retryable: false,
		},
		{
			name:      "generic error",
			err:       errors.New("generic error"),
			retryable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if IsRetryable(tt.err) != tt.retryable {
				t.Errorf("expected IsRetryable() = %v, got %v", tt.retryable, IsRetryable(tt.err))
			}
		})
	}
}

func TestIsThrottling(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		throttling bool
	}{
		{
			name: "throttling error",
			err: &Error{
				Code: ErrCodeThrottling,
			},
			throttling: true,
		},
		{
			name: "request limit exceeded",
			err: &Error{
				Code: ErrCodeRequestLimitExceeded,
			},
			throttling: true,
		},
		{
			name: "other error",
			err: &Error{
				Code: ErrCodeNotFound,
			},
			throttling: false,
		},
		{
			name:       "generic error",
			err:        errors.New("generic error"),
			throttling: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if IsThrottling(tt.err) != tt.throttling {
				t.Errorf("expected IsThrottling() = %v, got %v", tt.throttling, IsThrottling(tt.err))
			}
		})
	}
}

func TestIsDependencyViolation(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		dependency bool
	}{
		{
			name: "dependency violation",
			err: &Error{
				Code: ErrCodeDependencyViolation,
			},
			dependency: true,
		},
		{
			name: "other error",
			err: &Error{
				Code: ErrCodeNotFound,
			},
			dependency: false,
		},
		{
			name:       "generic error",
			err:        errors.New("generic error"),
			dependency: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if IsDependencyViolation(tt.err) != tt.dependency {
				t.Errorf("expected IsDependencyViolation() = %v, got %v", tt.dependency, IsDependencyViolation(tt.err))
			}
		})
	}
}
