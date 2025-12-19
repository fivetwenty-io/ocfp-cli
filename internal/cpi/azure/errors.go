package azure

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

var (
	// ErrResourceGroupNotFound indicates that a resource group was not found.
	ErrResourceGroupNotFound = errors.New("resource group not found")
	// ErrVNetNotFound indicates that a virtual network was not found.
	ErrVNetNotFound = errors.New("virtual network not found")
	// ErrSubnetNotFound indicates that a subnet was not found.
	ErrSubnetNotFound = errors.New("subnet not found")
	// ErrVMNotFound indicates that a virtual machine was not found.
	ErrVMNotFound = errors.New("virtual machine not found")
	// ErrDiskNotFound indicates that a managed disk was not found.
	ErrDiskNotFound = errors.New("managed disk not found")
	// ErrNSGNotFound indicates that a network security group was not found.
	ErrNSGNotFound = errors.New("network security group not found")
	// ErrPublicIPNotFound indicates that a public IP was not found.
	ErrPublicIPNotFound = errors.New("public IP not found")
	// ErrLoadBalancerNotFound indicates that a load balancer was not found.
	ErrLoadBalancerNotFound = errors.New("load balancer not found")
	// ErrStorageAccountNotFound indicates that a storage account was not found.
	ErrStorageAccountNotFound = errors.New("storage account not found")
	// ErrSnapshotNotFound indicates that a snapshot was not found.
	ErrSnapshotNotFound = errors.New("snapshot not found")
	// ErrSSHKeyNotFound indicates that an SSH key was not found.
	ErrSSHKeyNotFound = errors.New("SSH public key not found")
	// ErrImageNotFound indicates that an image was not found.
	ErrImageNotFound = errors.New("image not found")

	// ErrVolumeWaitTimeout indicates timeout waiting for disk state.
	ErrVolumeWaitTimeout = errors.New("timeout waiting for disk to reach desired state")
	// ErrVolumeErrorState indicates disk entered error state.
	ErrVolumeErrorState = errors.New("disk entered error state")
	// ErrSnapshotWaitTimeout indicates timeout waiting for snapshot state.
	ErrSnapshotWaitTimeout = errors.New("timeout waiting for snapshot to reach desired state")
	// ErrSnapshotErrorState indicates snapshot entered error state.
	ErrSnapshotErrorState = errors.New("snapshot entered error state")
	// ErrVMWaitTimeout indicates timeout waiting for VM state.
	ErrVMWaitTimeout = errors.New("timeout waiting for VM to reach desired state")
	// ErrVMErrorState indicates VM entered error state.
	ErrVMErrorState = errors.New("VM entered error state")

	// ErrInvalidRequest indicates an invalid request parameter.
	ErrInvalidRequest = errors.New("invalid request")
	// ErrNotFound indicates resource was not found.
	ErrNotFound = errors.New("resource not found")
	// ErrNotImplemented indicates functionality not yet implemented.
	ErrNotImplemented = errors.New("not implemented")
)

// ErrorCode represents Azure-specific error codes.
type ErrorCode string

const (
	// ErrCodeResourceNotFound indicates resource not found.
	ErrCodeResourceNotFound ErrorCode = "ResourceNotFound"
	// ErrCodeResourceGroupNotFound indicates resource group not found.
	ErrCodeResourceGroupNotFound ErrorCode = "ResourceGroupNotFound"
	// ErrCodeSubscriptionNotFound indicates subscription not found.
	ErrCodeSubscriptionNotFound ErrorCode = "SubscriptionNotFound"
	// ErrCodeAuthenticationFailed indicates authentication failure.
	ErrCodeAuthenticationFailed ErrorCode = "AuthenticationFailed"
	// ErrCodeAuthorizationFailed indicates authorization failure.
	ErrCodeAuthorizationFailed ErrorCode = "AuthorizationFailed"
	// ErrCodeQuotaExceeded indicates quota exceeded.
	ErrCodeQuotaExceeded ErrorCode = "QuotaExceeded"
	// ErrCodeConflict indicates resource conflict/already exists.
	ErrCodeConflict ErrorCode = "Conflict"
	// ErrCodeBadRequest indicates bad request.
	ErrCodeBadRequest ErrorCode = "BadRequest"
	// ErrCodeTooManyRequests indicates rate limiting.
	ErrCodeTooManyRequests ErrorCode = "TooManyRequests"
	// ErrCodeInternalServerError indicates internal Azure error.
	ErrCodeInternalServerError ErrorCode = "InternalServerError"
	// ErrCodeServiceUnavailable indicates service is temporarily unavailable.
	ErrCodeServiceUnavailable ErrorCode = "ServiceUnavailable"
	// ErrCodeOperationNotAllowed indicates operation not allowed.
	ErrCodeOperationNotAllowed ErrorCode = "OperationNotAllowed"
	// ErrCodeResourceInUse indicates resource has dependencies.
	ErrCodeResourceInUse ErrorCode = "ResourceInUse"
	// ErrCodeInvalidParameter indicates invalid parameter.
	ErrCodeInvalidParameter ErrorCode = "InvalidParameter"
)

// AzureError represents an Azure-specific error.
type AzureError struct {
	Code       ErrorCode
	Message    string
	StatusCode int
	RequestID  string
	Operation  string
	Details    map[string]interface{}
	Err        error
}

func (e *AzureError) Error() string {
	if e.Operation != "" {
		return fmt.Sprintf("[Azure:%s] %s: %s (request: %s)", e.Operation, e.Code, e.Message, e.RequestID)
	}

	return fmt.Sprintf("[Azure] %s: %s (request: %s)", e.Code, e.Message, e.RequestID)
}

// Unwrap returns the underlying error.
func (e *AzureError) Unwrap() error {
	return e.Err
}

// IsRetryable returns true if the error is retryable.
//
//nolint:exhaustive // Only listing retryable error codes, others are non-retryable by default
func (e *AzureError) IsRetryable() bool {
	switch e.Code {
	case ErrCodeTooManyRequests, ErrCodeServiceUnavailable, ErrCodeInternalServerError:
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

// IsNotFound returns true if the error indicates resource not found.
func (e *AzureError) IsNotFound() bool {
	return e.Code == ErrCodeResourceNotFound ||
		e.Code == ErrCodeResourceGroupNotFound ||
		e.Code == ErrCodeSubscriptionNotFound ||
		e.StatusCode == http.StatusNotFound
}

// IsAlreadyExists returns true if the error indicates resource already exists.
func (e *AzureError) IsAlreadyExists() bool {
	return e.Code == ErrCodeConflict || e.StatusCode == http.StatusConflict
}

// IsQuotaExceeded returns true if the error indicates quota exceeded.
func (e *AzureError) IsQuotaExceeded() bool {
	return e.Code == ErrCodeQuotaExceeded
}

// IsResourceInUse returns true if the error indicates resource is in use.
func (e *AzureError) IsResourceInUse() bool {
	return e.Code == ErrCodeResourceInUse
}

// WrapAzureError wraps an Azure SDK error into a structured error with request ID tracking.
func WrapAzureError(err error, operation string) error {
	if err == nil {
		return nil
	}

	azureErr := &AzureError{
		Code:      ErrCodeInternalServerError,
		Message:   err.Error(),
		Operation: operation,
		Details:   make(map[string]interface{}),
		Err:       err,
	}

	// Extract Azure-specific error information from ResponseError
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		azureErr.StatusCode = respErr.StatusCode
		azureErr.Code = ErrorCode(respErr.ErrorCode)
		azureErr.Message = respErr.Error()

		// Try to extract request ID from response headers
		if respErr.RawResponse != nil {
			azureErr.RequestID = respErr.RawResponse.Header.Get("x-ms-request-id")
		}
	}

	// Map HTTP status codes to error codes if not already set
	if azureErr.Code == ErrCodeInternalServerError {
		azureErr.Code = mapStatusCodeToErrorCode(azureErr.StatusCode)
	}

	// Log error with full context
	logError(azureErr)

	// Map Azure error codes to provider error codes
	provErr := mapToProviderError(azureErr)
	if provErr != nil {
		return provErr
	}

	return azureErr
}

// mapStatusCodeToErrorCode maps HTTP status codes to Azure error codes.
func mapStatusCodeToErrorCode(statusCode int) ErrorCode {
	switch statusCode {
	case http.StatusNotFound:
		return ErrCodeResourceNotFound
	case http.StatusConflict:
		return ErrCodeConflict
	case http.StatusUnauthorized:
		return ErrCodeAuthenticationFailed
	case http.StatusForbidden:
		return ErrCodeAuthorizationFailed
	case http.StatusBadRequest:
		return ErrCodeBadRequest
	case http.StatusTooManyRequests:
		return ErrCodeTooManyRequests
	case http.StatusServiceUnavailable:
		return ErrCodeServiceUnavailable
	default:
		return ErrCodeInternalServerError
	}
}

// logError logs an Azure error with full context.
func logError(azureErr *AzureError) {
	args := buildLogArgs(azureErr)

	if azureErr.IsRetryable() {
		logger.Debug(args...)
	} else {
		logger.Error(args...)
	}
}

// buildLogArgs builds logging arguments based on available error data.
func buildLogArgs(azureErr *AzureError) []interface{} {
	args := []interface{}{
		"Azure error occurred",
		"code", azureErr.Code,
		"message", azureErr.Message,
		"operation", azureErr.Operation,
	}

	hasRequestID := azureErr.RequestID != ""
	hasStatusCode := azureErr.StatusCode > 0

	switch {
	case hasRequestID && hasStatusCode:
		args = append(args, "request_id", azureErr.RequestID, "status_code", azureErr.StatusCode)
	case hasRequestID:
		args = append(args, "request_id", azureErr.RequestID)
	case hasStatusCode:
		args = append(args, "status_code", azureErr.StatusCode)
	}

	if azureErr.IsRetryable() {
		args = append(args, "retryable", true)
	}

	return args
}

// mapToProviderError maps Azure errors to provider-specific errors.
//
//nolint:exhaustive,funlen // Only mapping specific error codes; error mapping requires multiple cases
func mapToProviderError(azureErr *AzureError) error {
	// Check for Azure-specific NotFound error codes
	errorCode := string(azureErr.Code)
	if azureErr.IsNotFound() || strings.Contains(errorCode, "NotFound") {
		return &cpi.ProviderError{
			Provider: "azure",
			Code:     "NotFound",
			Message:  azureErr.Message,
			Details: map[string]interface{}{
				"operation":  azureErr.Operation,
				"request_id": azureErr.RequestID,
			},
		}
	}

	switch azureErr.Code {
	case ErrCodeConflict:
		return &cpi.ProviderError{
			Provider: "azure",
			Code:     "AlreadyExists",
			Message:  azureErr.Message,
			Details: map[string]interface{}{
				"operation":  azureErr.Operation,
				"request_id": azureErr.RequestID,
			},
		}

	case ErrCodeAuthenticationFailed, ErrCodeAuthorizationFailed:
		return &cpi.ProviderError{
			Provider: "azure",
			Code:     "Unauthorized",
			Message:  azureErr.Message,
			Details: map[string]interface{}{
				"operation":  azureErr.Operation,
				"request_id": azureErr.RequestID,
			},
		}

	case ErrCodeBadRequest, ErrCodeInvalidParameter:
		return &cpi.ProviderError{
			Provider: "azure",
			Code:     "InvalidParameter",
			Message:  azureErr.Message,
			Details: map[string]interface{}{
				"operation":  azureErr.Operation,
				"request_id": azureErr.RequestID,
			},
		}

	case ErrCodeQuotaExceeded:
		return &cpi.ProviderError{
			Provider: "azure",
			Code:     "QuotaExceeded",
			Message:  azureErr.Message,
			Details: map[string]interface{}{
				"operation":  azureErr.Operation,
				"request_id": azureErr.RequestID,
			},
		}

	case ErrCodeResourceInUse:
		return &cpi.ProviderError{
			Provider: "azure",
			Code:     "DependencyViolation",
			Message:  azureErr.Message,
			Details: map[string]interface{}{
				"operation":  azureErr.Operation,
				"request_id": azureErr.RequestID,
			},
		}
	}

	return nil
}

// IsNotFound checks if the error is a not found error.
func IsNotFound(err error) bool {
	var azureErr *AzureError
	if errors.As(err, &azureErr) {
		return azureErr.IsNotFound()
	}

	return cpi.IsNotFound(err)
}

// IsAlreadyExists checks if the error indicates the resource already exists.
func IsAlreadyExists(err error) bool {
	var azureErr *AzureError
	if errors.As(err, &azureErr) {
		return azureErr.IsAlreadyExists()
	}

	return cpi.IsAlreadyExists(err)
}

// IsRetryable checks if the error is retryable.
func IsRetryable(err error) bool {
	var azureErr *AzureError
	if errors.As(err, &azureErr) {
		return azureErr.IsRetryable()
	}

	return false
}

// IsThrottling checks if the error is due to rate limiting.
func IsThrottling(err error) bool {
	var azureErr *AzureError
	if errors.As(err, &azureErr) {
		return azureErr.Code == ErrCodeTooManyRequests
	}

	return false
}

// IsResourceInUse checks if the error indicates the resource is in use.
func IsResourceInUse(err error) bool {
	var azureErr *AzureError
	if errors.As(err, &azureErr) {
		return azureErr.IsResourceInUse()
	}

	return false
}
