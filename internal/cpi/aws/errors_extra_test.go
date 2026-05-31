package aws

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// ---- buildLogArgs -----------------------------------------------------------

func TestBuildLogArgs_AllFields(t *testing.T) {
	t.Parallel()

	e := &Error{
		Code:       ErrCodeThrottling,
		Message:    "too many requests",
		Operation:  "RunInstances",
		RequestID:  "req-abc",
		StatusCode: 429,
	}

	args := buildLogArgs(e)

	if len(args) == 0 {
		t.Errorf("buildLogArgs returned empty slice")
	}
	// Verify retryable appended (Throttling is retryable)
	found := false
	for _, a := range args {
		if a == "retryable" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("buildLogArgs: expected 'retryable' in args for throttling error")
	}
}

func TestBuildLogArgs_NoRequestIDNoStatusCode(t *testing.T) {
	t.Parallel()

	e := &Error{Code: ErrCodeNotFound, Message: "missing", Operation: "GetVolume"}
	args := buildLogArgs(e)

	for _, a := range args {
		if a == "request_id" || a == "status_code" {
			t.Errorf("buildLogArgs: should not include %q when RequestID and StatusCode are zero", a)
		}
	}
}

func TestBuildLogArgs_RequestIDOnly(t *testing.T) {
	t.Parallel()

	e := &Error{Code: ErrCodeNotFound, Message: "missing", RequestID: "req-only"}
	args := buildLogArgs(e)

	found := false
	for _, a := range args {
		if a == "request_id" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("buildLogArgs: expected 'request_id' in args")
	}
}

func TestBuildLogArgs_StatusCodeOnly(t *testing.T) {
	t.Parallel()

	e := &Error{Code: ErrCodeServiceUnavailable, Message: "down", StatusCode: 503}
	args := buildLogArgs(e)

	found := false
	for _, a := range args {
		if a == "status_code" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("buildLogArgs: expected 'status_code' in args")
	}
}

// ---- mapToProviderError -----------------------------------------------------

func TestMapToProviderError_NotFound(t *testing.T) {
	t.Parallel()

	e := &Error{Code: ErrCodeNotFound, Message: "not found", Operation: "op"}
	result := mapToProviderError(e)
	if result == nil {
		t.Fatal("expected ProviderError, got nil")
	}
}

func TestMapToProviderError_DotNotFound(t *testing.T) {
	t.Parallel()

	e := &Error{Code: ErrorCode("InvalidGroup.NotFound"), Message: "sg not found"}
	result := mapToProviderError(e)
	if result == nil {
		t.Fatal("expected ProviderError for .NotFound suffix, got nil")
	}
}

func TestMapToProviderError_DotDuplicate(t *testing.T) {
	t.Parallel()

	e := &Error{Code: ErrorCode("InvalidGroup.Duplicate"), Message: "duplicate"}
	result := mapToProviderError(e)
	if result == nil {
		t.Fatal("expected ProviderError for .Duplicate suffix, got nil")
	}
}

func TestMapToProviderError_AlreadyExists(t *testing.T) {
	t.Parallel()

	e := &Error{Code: ErrCodeAlreadyExists, Message: "exists"}
	result := mapToProviderError(e)
	if result == nil {
		t.Fatal("expected ProviderError")
	}
}

func TestMapToProviderError_Unauthorized(t *testing.T) {
	t.Parallel()

	for _, code := range []ErrorCode{ErrCodeUnauthorized, ErrCodeAccessDenied} {
		e := &Error{Code: code, Message: "denied"}
		result := mapToProviderError(e)
		if result == nil {
			t.Errorf("code %q: expected ProviderError, got nil", code)
		}
	}
}

func TestMapToProviderError_InvalidParameter(t *testing.T) {
	t.Parallel()

	for _, code := range []ErrorCode{ErrCodeInvalidParameter, ErrCodeInvalidState} {
		e := &Error{Code: code, Message: "bad param"}
		result := mapToProviderError(e)
		if result == nil {
			t.Errorf("code %q: expected ProviderError, got nil", code)
		}
	}
}

func TestMapToProviderError_QuotaExceeded(t *testing.T) {
	t.Parallel()

	e := &Error{Code: ErrCodeQuotaExceeded, Message: "quota"}
	result := mapToProviderError(e)
	if result == nil {
		t.Fatal("expected ProviderError")
	}
}

func TestMapToProviderError_DependencyViolation(t *testing.T) {
	t.Parallel()

	e := &Error{Code: ErrCodeDependencyViolation, Message: "has deps"}
	result := mapToProviderError(e)
	if result == nil {
		t.Fatal("expected ProviderError")
	}
}

// ---- IsTerminationProtected via ProviderError -------------------------------

func TestIsTerminationProtected_ProviderError(t *testing.T) {
	t.Parallel()

	err := &cpi.ProviderError{
		Provider: "aws",
		Code:     "OperationNotPermitted",
		Message:  "DisableApiTermination is enabled on this instance",
	}
	if !IsTerminationProtected(err) {
		t.Errorf("IsTerminationProtected: expected true for ProviderError with DisableApiTermination")
	}

	err2 := &cpi.ProviderError{
		Provider: "aws",
		Code:     "OperationNotPermitted",
		Message:  "some other operation not permitted",
	}
	if IsTerminationProtected(err2) {
		t.Errorf("IsTerminationProtected: expected false for ProviderError without DisableApiTermination")
	}
}

func TestMapToProviderError_Unknown(t *testing.T) {
	t.Parallel()

	e := &Error{Code: ErrCodeInternalError, Message: "internal"}
	result := mapToProviderError(e)
	if result != nil {
		t.Errorf("mapToProviderError(InternalError) = %v, want nil (fall-through)", result)
	}
}
