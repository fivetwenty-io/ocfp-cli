package aws

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aws/smithy-go"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

var (
	// ErrInternetGatewayNotFound indicates that an internet gateway was not found for a VPC.
	ErrInternetGatewayNotFound = errors.New("internet gateway not found for VPC")
	// ErrInvalidNetworkCIDR indicates that a CIDR is not a valid network address.
	ErrInvalidNetworkCIDR = errors.New("CIDR is not a valid network address")
	// ErrVolumeWaitTimeout indicates timeout waiting for volume state.
	ErrVolumeWaitTimeout = errors.New("timeout waiting for volume to reach desired state")
	// ErrVolumeErrorState indicates volume entered error state.
	ErrVolumeErrorState = errors.New("volume entered error state")
	// ErrSnapshotWaitTimeout indicates timeout waiting for snapshot state.
	ErrSnapshotWaitTimeout = errors.New("timeout waiting for snapshot to reach desired state")
	// ErrSnapshotErrorState indicates snapshot entered error state.
	ErrSnapshotErrorState = errors.New("snapshot entered error state")
	// ErrInvalidRequest indicates an invalid request parameter.
	ErrInvalidRequest = errors.New("invalid request")
	// ErrSecurityGroupNotFound indicates security group was not found.
	ErrSecurityGroupNotFound = errors.New("security group not found")
	// ErrNotFound indicates resource was not found.
	ErrNotFound = errors.New("resource not found")
	// ErrNotImplemented indicates functionality not yet implemented.
	ErrNotImplemented = errors.New("not implemented")
)

// ErrorCode represents AWS-specific error codes.
type ErrorCode string

const (
	// ErrCodeThrottling indicates rate limiting.
	ErrCodeThrottling ErrorCode = "Throttling"
	// ErrCodeRequestLimitExceeded indicates rate limiting.
	ErrCodeRequestLimitExceeded ErrorCode = "RequestLimitExceeded"
	// ErrCodeServiceUnavailable indicates service is temporarily unavailable.
	ErrCodeServiceUnavailable ErrorCode = "ServiceUnavailable"
	// ErrCodeInternalError indicates internal AWS error.
	ErrCodeInternalError ErrorCode = "InternalError"
	// ErrCodeInvalidParameter indicates invalid parameter.
	ErrCodeInvalidParameter ErrorCode = "InvalidParameterValue"
	// ErrCodeNotFound indicates resource not found.
	ErrCodeNotFound ErrorCode = "NotFound"
	// ErrCodeAlreadyExists indicates resource already exists.
	ErrCodeAlreadyExists ErrorCode = "AlreadyExists"
	// ErrCodeUnauthorized indicates authentication failure.
	ErrCodeUnauthorized ErrorCode = "UnauthorizedOperation"
	// ErrCodeAccessDenied indicates permission denied.
	ErrCodeAccessDenied ErrorCode = "AccessDenied"
	// ErrCodeQuotaExceeded indicates quota exceeded.
	ErrCodeQuotaExceeded ErrorCode = "QuotaExceeded"
	// ErrCodeDependencyViolation indicates resource has dependencies.
	ErrCodeDependencyViolation ErrorCode = "DependencyViolation"
	// ErrCodeInvalidState indicates invalid resource state.
	ErrCodeInvalidState ErrorCode = "InvalidState"
	// ErrCodeOperationNotPermitted indicates the operation is not allowed (e.g., termination protection).
	ErrCodeOperationNotPermitted ErrorCode = "OperationNotPermitted"
)

// Error represents an AWS-specific error.
type Error struct {
	Code       ErrorCode
	Message    string
	StatusCode int
	RequestID  string
	Operation  string
	Details    map[string]interface{}
	Err        error
}

func (e *Error) Error() string {
	if e.Operation != "" {
		return fmt.Sprintf("[AWS:%s] %s: %s (request: %s)", e.Operation, e.Code, e.Message, e.RequestID)
	}

	return fmt.Sprintf("[AWS] %s: %s (request: %s)", e.Code, e.Message, e.RequestID)
}

// Unwrap returns the underlying error.
func (e *Error) Unwrap() error {
	return e.Err
}

// IsRetryable returns true if the error is retryable.
//
//nolint:exhaustive // Only listing retryable error codes, others are non-retryable by default
func (e *Error) IsRetryable() bool {
	switch e.Code {
	case ErrCodeThrottling, ErrCodeRequestLimitExceeded,
		ErrCodeServiceUnavailable, ErrCodeInternalError:
		return true
	}

	// Check HTTP status codes
	switch e.StatusCode {
	case http.StatusTooManyRequests,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
		http.StatusBadGateway:
		return true
	}

	return false
}

// WrapAWSError wraps an AWS SDK error into a structured error with request ID tracking.
func WrapAWSError(err error, operation string) error {
	if err == nil {
		return nil
	}

	awsErr := &Error{
		Code:      ErrCodeInternalError,
		Message:   err.Error(),
		Operation: operation,
		Details:   make(map[string]interface{}),
		Err:       err,
	}

	// Extract AWS-specific error information
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		awsErr.Code = ErrorCode(apiErr.ErrorCode())
		awsErr.Message = apiErr.ErrorMessage()
	}

	// Extract request ID from metadata
	awsErr.RequestID = extractRequestID(err)

	// Extract HTTP status code
	awsErr.StatusCode = extractStatusCode(err)

	// Log error with full context
	logError(awsErr)

	// Map AWS error codes to provider error codes
	provErr := mapToProviderError(awsErr)
	if provErr != nil {
		return provErr
	}

	return awsErr
}

// extractRequestID attempts to extract the AWS request ID from an error.
func extractRequestID(err error) string {
	// Try to get request ID from ResponseError
	type responseError interface {
		ServiceRequestID() string
	}

	var respErr responseError
	if errors.As(err, &respErr) {
		return respErr.ServiceRequestID()
	}

	// Fallback: try to extract from error metadata
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		// Some AWS errors include request ID in error fault
		if fault := apiErr.ErrorFault(); fault.String() != "" {
			// Request ID might be in the fault string
			return ""
		}
	}

	return ""
}

// extractStatusCode attempts to extract the HTTP status code from an error.
func extractStatusCode(err error) int {
	// Try to get status code from HTTPResponse
	type httpResponse interface {
		HTTPStatusCode() int
	}

	var httpErr httpResponse
	if errors.As(err, &httpErr) {
		return httpErr.HTTPStatusCode()
	}

	return 0
}

// logError logs an AWS error with full context.
func logError(awsErr *Error) {
	args := buildLogArgs(awsErr)

	if awsErr.IsRetryable() {
		logger.Debug(args...)
	} else {
		logger.Error(args...)
	}
}

// buildLogArgs builds logging arguments based on available error data.
func buildLogArgs(awsErr *Error) []interface{} {
	args := []interface{}{
		"AWS error occurred",
		"code", awsErr.Code,
		"message", awsErr.Message,
		"operation", awsErr.Operation,
	}

	hasRequestID := awsErr.RequestID != ""
	hasStatusCode := awsErr.StatusCode > 0

	switch {
	case hasRequestID && hasStatusCode:
		args = append(args, "request_id", awsErr.RequestID, "status_code", awsErr.StatusCode)
	case hasRequestID:
		args = append(args, "request_id", awsErr.RequestID)
	case hasStatusCode:
		args = append(args, "status_code", awsErr.StatusCode)
	}

	if awsErr.IsRetryable() {
		args = append(args, "retryable", true)
	}

	return args
}

// mapToProviderError maps AWS errors to provider-specific errors.
//
//nolint:exhaustive,funlen // Only mapping specific error codes; error mapping requires multiple cases
func mapToProviderError(awsErr *Error) error {
	// Check for AWS-specific NotFound error codes
	// AWS uses patterns like: InvalidGroup.NotFound, InvalidSubnetID.NotFound, etc.
	errorCode := string(awsErr.Code)
	if awsErr.Code == ErrCodeNotFound || strings.HasSuffix(errorCode, ".NotFound") {
		return &cpi.ProviderError{
			Provider: "aws",
			Code:     "NotFound",
			Message:  awsErr.Message,
			Details: map[string]interface{}{
				"operation":  awsErr.Operation,
				"request_id": awsErr.RequestID,
			},
		}
	}

	// Check for AWS-specific Duplicate error codes
	// AWS uses patterns like: InvalidGroup.Duplicate for existing resources
	if strings.HasSuffix(errorCode, ".Duplicate") {
		return &cpi.ProviderError{
			Provider: "aws",
			Code:     "AlreadyExists",
			Message:  awsErr.Message,
			Details: map[string]interface{}{
				"operation":  awsErr.Operation,
				"request_id": awsErr.RequestID,
			},
		}
	}

	switch awsErr.Code {
	case ErrCodeAlreadyExists:
		return &cpi.ProviderError{
			Provider: "aws",
			Code:     "AlreadyExists",
			Message:  awsErr.Message,
			Details: map[string]interface{}{
				"operation":  awsErr.Operation,
				"request_id": awsErr.RequestID,
			},
		}

	case ErrCodeUnauthorized, ErrCodeAccessDenied:
		return &cpi.ProviderError{
			Provider: "aws",
			Code:     "Unauthorized",
			Message:  awsErr.Message,
			Details: map[string]interface{}{
				"operation":  awsErr.Operation,
				"request_id": awsErr.RequestID,
			},
		}

	case ErrCodeInvalidParameter, ErrCodeInvalidState:
		return &cpi.ProviderError{
			Provider: "aws",
			Code:     "InvalidParameter",
			Message:  awsErr.Message,
			Details: map[string]interface{}{
				"operation":  awsErr.Operation,
				"request_id": awsErr.RequestID,
			},
		}

	case ErrCodeQuotaExceeded:
		return &cpi.ProviderError{
			Provider: "aws",
			Code:     "QuotaExceeded",
			Message:  awsErr.Message,
			Details: map[string]interface{}{
				"operation":  awsErr.Operation,
				"request_id": awsErr.RequestID,
			},
		}

	case ErrCodeDependencyViolation:
		return &cpi.ProviderError{
			Provider: "aws",
			Code:     "DependencyViolation",
			Message:  awsErr.Message,
			Details: map[string]interface{}{
				"operation":  awsErr.Operation,
				"request_id": awsErr.RequestID,
			},
		}
	}

	return nil
}

// IsNotFound checks if the error is a not found error.
func IsNotFound(err error) bool {
	var awsErr *Error
	if errors.As(err, &awsErr) {
		return awsErr.Code == ErrCodeNotFound
	}

	return cpi.IsNotFound(err)
}

// IsAlreadyExists checks if the error indicates the resource already exists.
func IsAlreadyExists(err error) bool {
	var awsErr *Error
	if errors.As(err, &awsErr) {
		return awsErr.Code == ErrCodeAlreadyExists
	}

	return cpi.IsAlreadyExists(err)
}

// IsRetryable checks if the error is retryable.
func IsRetryable(err error) bool {
	var awsErr *Error
	if errors.As(err, &awsErr) {
		return awsErr.IsRetryable()
	}

	return false
}

// IsThrottling checks if the error is due to rate limiting.
func IsThrottling(err error) bool {
	var awsErr *Error
	if errors.As(err, &awsErr) {
		return awsErr.Code == ErrCodeThrottling || awsErr.Code == ErrCodeRequestLimitExceeded
	}

	return false
}

// IsDependencyViolation checks if the error is a dependency violation.
func IsDependencyViolation(err error) bool {
	var awsErr *Error
	if errors.As(err, &awsErr) {
		return awsErr.Code == ErrCodeDependencyViolation
	}

	return false
}

// IsTerminationProtected checks if the error indicates an instance has
// termination protection (DisableApiTermination) enabled. AWS returns
// OperationNotPermitted with a message referencing DisableApiTermination.
func IsTerminationProtected(err error) bool {
	var awsErr *Error
	if errors.As(err, &awsErr) {
		if awsErr.Code == ErrCodeOperationNotPermitted &&
			strings.Contains(awsErr.Message, "DisableApiTermination") {
			return true
		}
	}

	var provErr *cpi.ProviderError
	if errors.As(err, &provErr) {
		if provErr.Code == "OperationNotPermitted" &&
			strings.Contains(provErr.Message, "DisableApiTermination") {
			return true
		}
	}

	// Also check the raw error message for cases where the error
	// hasn't been wrapped into a structured type yet
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		if apiErr.ErrorCode() == "OperationNotPermitted" &&
			strings.Contains(apiErr.ErrorMessage(), "DisableApiTermination") {
			return true
		}
	}

	return false
}
