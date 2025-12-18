package gcp

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"google.golang.org/api/googleapi"
)

var (
	// ErrInvalidConfigType indicates the config type is not supported.
	ErrInvalidConfigType = errors.New("invalid config type for GCP provider")
	// ErrProjectIDRequired indicates that project ID is missing.
	ErrProjectIDRequired = errors.New("project ID is required for GCP provider")
	// ErrServiceAccountRequired indicates that service account is missing.
	ErrServiceAccountRequired = errors.New("service account JSON is required for GCP provider")
	// ErrRegionRequired indicates that region is missing.
	ErrRegionRequired = errors.New("region is required for GCP provider")
	// ErrZoneRequired indicates that zone is missing for zonal resources.
	ErrZoneRequired = errors.New("zone is required for zonal resources")
	// ErrResourceNotFound indicates resource was not found.
	ErrResourceNotFound = errors.New("resource not found")
	// ErrResourceAlreadyExists indicates resource already exists.
	ErrResourceAlreadyExists = errors.New("resource already exists")
	// ErrOperationTimeout indicates timeout waiting for operation.
	ErrOperationTimeout = errors.New("operation timed out")
	// ErrInvalidRequest indicates an invalid request parameter.
	ErrInvalidRequest = errors.New("invalid request")
	// ErrNotImplemented indicates functionality not yet implemented.
	ErrNotImplemented = errors.New("not implemented")
	// ErrSharedVPCNotConfigured indicates Shared VPC is not properly configured.
	ErrSharedVPCNotConfigured = errors.New("shared VPC not configured")
	// ErrFirewallRuleNotFound indicates firewall rule was not found.
	ErrFirewallRuleNotFound = errors.New("firewall rule not found")
	// ErrVolumeWaitTimeout indicates timeout waiting for volume state.
	ErrVolumeWaitTimeout = errors.New("timeout waiting for volume to reach desired state")
	// ErrVolumeErrorState indicates volume entered error state.
	ErrVolumeErrorState = errors.New("volume entered error state")
	// ErrSnapshotWaitTimeout indicates timeout waiting for snapshot state.
	ErrSnapshotWaitTimeout = errors.New("timeout waiting for snapshot to reach desired state")
	// ErrSnapshotErrorState indicates snapshot entered error state.
	ErrSnapshotErrorState = errors.New("snapshot entered error state")
)

// ErrorCode represents GCP-specific error codes.
type ErrorCode string

const (
	// ErrCodeRateLimitExceeded indicates rate limiting.
	ErrCodeRateLimitExceeded ErrorCode = "rateLimitExceeded"
	// ErrCodeQuotaExceeded indicates quota exceeded.
	ErrCodeQuotaExceeded ErrorCode = "quotaExceeded"
	// ErrCodeResourceNotFound indicates resource not found.
	ErrCodeResourceNotFound ErrorCode = "notFound"
	// ErrCodeResourceAlreadyExists indicates resource already exists.
	ErrCodeResourceAlreadyExists ErrorCode = "alreadyExists"
	// ErrCodePermissionDenied indicates permission denied.
	ErrCodePermissionDenied ErrorCode = "forbidden"
	// ErrCodeUnauthorized indicates authentication failure.
	ErrCodeUnauthorized ErrorCode = "unauthorized"
	// ErrCodeInternalError indicates internal GCP error.
	ErrCodeInternalError ErrorCode = "internalError"
	// ErrCodeBadRequest indicates invalid request.
	ErrCodeBadRequest ErrorCode = "badRequest"
	// ErrCodeServiceUnavailable indicates service is temporarily unavailable.
	ErrCodeServiceUnavailable ErrorCode = "serviceUnavailable"
	// ErrCodeOperationInProgress indicates an operation is already in progress.
	ErrCodeOperationInProgress ErrorCode = "operationInProgress"
	// ErrCodeResourceInUse indicates resource is in use.
	ErrCodeResourceInUse ErrorCode = "resourceInUse"
	// ErrCodeConditionNotMet indicates precondition not met.
	ErrCodeConditionNotMet ErrorCode = "conditionNotMet"
)

// GCPError represents a GCP-specific error.
type GCPError struct {
	Code       ErrorCode
	Message    string
	StatusCode int
	Operation  string
	Details    map[string]interface{}
	Err        error
}

func (e *GCPError) Error() string {
	if e.Operation != "" {
		return fmt.Sprintf("[GCP:%s] %s: %s", e.Operation, e.Code, e.Message)
	}
	return fmt.Sprintf("[GCP] %s: %s", e.Code, e.Message)
}

// Unwrap returns the underlying error.
func (e *GCPError) Unwrap() error {
	return e.Err
}

// IsRetryable returns true if the error is retryable.
func (e *GCPError) IsRetryable() bool {
	switch e.Code {
	case ErrCodeRateLimitExceeded, ErrCodeServiceUnavailable, ErrCodeInternalError:
		return true
	}

	// Check HTTP status codes
	switch e.StatusCode {
	case http.StatusTooManyRequests,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
		http.StatusBadGateway,
		http.StatusInternalServerError:
		return true
	}

	return false
}

// IsNotFound returns true if the error indicates resource not found.
func (e *GCPError) IsNotFound() bool {
	return e.Code == ErrCodeResourceNotFound || e.StatusCode == http.StatusNotFound
}

// IsAlreadyExists returns true if the error indicates resource already exists.
func (e *GCPError) IsAlreadyExists() bool {
	return e.Code == ErrCodeResourceAlreadyExists || e.StatusCode == http.StatusConflict
}

// WrapGCPError wraps a GCP API error into a structured error.
func WrapGCPError(err error, operation string) error {
	if err == nil {
		return nil
	}

	gcpErr := &GCPError{
		Code:      ErrCodeInternalError,
		Message:   err.Error(),
		Operation: operation,
		Details:   make(map[string]interface{}),
		Err:       err,
	}

	// Extract Google API error information
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		gcpErr.StatusCode = apiErr.Code
		gcpErr.Message = apiErr.Message
		gcpErr.Code = mapHTTPStatusToErrorCode(apiErr.Code)

		// Extract additional details from error body
		if len(apiErr.Errors) > 0 {
			gcpErr.Details["errors"] = apiErr.Errors
			// Use the first error's reason as the code if available
			if apiErr.Errors[0].Reason != "" {
				gcpErr.Code = ErrorCode(apiErr.Errors[0].Reason)
			}
		}
	}

	// Log error with full context
	logError(gcpErr)

	// Map GCP error codes to provider error codes
	provErr := mapToProviderError(gcpErr)
	if provErr != nil {
		return provErr
	}

	return gcpErr
}

// mapHTTPStatusToErrorCode maps HTTP status codes to GCP error codes.
func mapHTTPStatusToErrorCode(statusCode int) ErrorCode {
	switch statusCode {
	case http.StatusNotFound:
		return ErrCodeResourceNotFound
	case http.StatusConflict:
		return ErrCodeResourceAlreadyExists
	case http.StatusForbidden:
		return ErrCodePermissionDenied
	case http.StatusUnauthorized:
		return ErrCodeUnauthorized
	case http.StatusTooManyRequests:
		return ErrCodeRateLimitExceeded
	case http.StatusBadRequest:
		return ErrCodeBadRequest
	case http.StatusServiceUnavailable:
		return ErrCodeServiceUnavailable
	case http.StatusInternalServerError:
		return ErrCodeInternalError
	default:
		return ErrCodeInternalError
	}
}

// logError logs a GCP error with full context.
func logError(gcpErr *GCPError) {
	args := buildLogArgs(gcpErr)

	if gcpErr.IsRetryable() {
		logger.Debug(args...)
	} else {
		logger.Error(args...)
	}
}

// buildLogArgs builds logging arguments based on available error data.
func buildLogArgs(gcpErr *GCPError) []interface{} {
	args := []interface{}{
		"GCP error occurred",
		"code", gcpErr.Code,
		"message", gcpErr.Message,
		"operation", gcpErr.Operation,
	}

	if gcpErr.StatusCode > 0 {
		args = append(args, "status_code", gcpErr.StatusCode)
	}

	if gcpErr.IsRetryable() {
		args = append(args, "retryable", true)
	}

	return args
}

// mapToProviderError maps GCP errors to provider-specific errors.
func mapToProviderError(gcpErr *GCPError) error {
	// Check for GCP-specific NotFound error codes
	errorCode := string(gcpErr.Code)
	if gcpErr.Code == ErrCodeResourceNotFound || strings.Contains(errorCode, "notFound") {
		return &cpi.ProviderError{
			Provider: "gcp",
			Code:     "NotFound",
			Message:  gcpErr.Message,
			Details: map[string]interface{}{
				"operation": gcpErr.Operation,
			},
		}
	}

	switch gcpErr.Code {
	case ErrCodeResourceAlreadyExists:
		return &cpi.ProviderError{
			Provider: "gcp",
			Code:     "AlreadyExists",
			Message:  gcpErr.Message,
			Details: map[string]interface{}{
				"operation": gcpErr.Operation,
			},
		}

	case ErrCodeUnauthorized, ErrCodePermissionDenied:
		return &cpi.ProviderError{
			Provider: "gcp",
			Code:     "Unauthorized",
			Message:  gcpErr.Message,
			Details: map[string]interface{}{
				"operation": gcpErr.Operation,
			},
		}

	case ErrCodeBadRequest:
		return &cpi.ProviderError{
			Provider: "gcp",
			Code:     "InvalidParameter",
			Message:  gcpErr.Message,
			Details: map[string]interface{}{
				"operation": gcpErr.Operation,
			},
		}

	case ErrCodeQuotaExceeded:
		return &cpi.ProviderError{
			Provider: "gcp",
			Code:     "QuotaExceeded",
			Message:  gcpErr.Message,
			Details: map[string]interface{}{
				"operation": gcpErr.Operation,
			},
		}

	case ErrCodeResourceInUse:
		return &cpi.ProviderError{
			Provider: "gcp",
			Code:     "DependencyViolation",
			Message:  gcpErr.Message,
			Details: map[string]interface{}{
				"operation": gcpErr.Operation,
			},
		}
	}

	return nil
}

// IsNotFound checks if the error is a not found error.
func IsNotFound(err error) bool {
	var gcpErr *GCPError
	if errors.As(err, &gcpErr) {
		return gcpErr.IsNotFound()
	}
	return cpi.IsNotFound(err)
}

// IsAlreadyExists checks if the error indicates the resource already exists.
func IsAlreadyExists(err error) bool {
	var gcpErr *GCPError
	if errors.As(err, &gcpErr) {
		return gcpErr.IsAlreadyExists()
	}
	return cpi.IsAlreadyExists(err)
}

// IsRetryable checks if the error is retryable.
func IsRetryable(err error) bool {
	var gcpErr *GCPError
	if errors.As(err, &gcpErr) {
		return gcpErr.IsRetryable()
	}
	return false
}

// IsQuotaExceeded checks if the error is due to quota being exceeded.
func IsQuotaExceeded(err error) bool {
	var gcpErr *GCPError
	if errors.As(err, &gcpErr) {
		return gcpErr.Code == ErrCodeQuotaExceeded
	}
	return false
}

// IsResourceInUse checks if the error is due to resource being in use.
func IsResourceInUse(err error) bool {
	var gcpErr *GCPError
	if errors.As(err, &gcpErr) {
		return gcpErr.Code == ErrCodeResourceInUse
	}
	return false
}
