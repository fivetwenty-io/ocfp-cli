package aws

import (
	"errors"
	"testing"
)

// ---- CircuitBreakerState.String ---------------------------------------------

func TestCircuitBreakerStateString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		state CircuitBreakerState
		want  string
	}{
		{StateClosed, "closed"},
		{StateOpen, "open"},
		{StateHalfOpen, "half-open"},
		{CircuitBreakerState(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			got := tt.state.String()
			if got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---- NewCircuitBreaker with nil config --------------------------------------

func TestNewCircuitBreaker_NilConfig(t *testing.T) {
	t.Parallel()

	cb := NewCircuitBreaker(nil)
	if cb == nil {
		t.Fatal("NewCircuitBreaker(nil) returned nil")
	}
	if cb.config == nil {
		t.Errorf("NewCircuitBreaker(nil): config should default to non-nil")
	}
	if cb.config.MaxFailures != DefaultCircuitBreakerMaxFailures {
		t.Errorf("MaxFailures = %d, want %d", cb.config.MaxFailures, DefaultCircuitBreakerMaxFailures)
	}
}

// ---- wrapError --------------------------------------------------------------

func TestWrapError_NilReturnsNil(t *testing.T) {
	t.Parallel()

	got := wrapError(nil, "op")
	if got != nil {
		t.Errorf("wrapError(nil, ...) = %v, want nil", got)
	}
}

func TestWrapError_NonNilWraps(t *testing.T) {
	t.Parallel()

	orig := errors.New("something went wrong")
	got := wrapError(orig, "DescribeInstances")
	if got == nil {
		t.Errorf("wrapError(non-nil, ...) returned nil")
	}
	if got == orig {
		t.Errorf("wrapError should wrap the error, not return the original")
	}
}
