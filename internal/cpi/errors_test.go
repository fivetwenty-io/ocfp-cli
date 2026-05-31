package cpi

import (
	"errors"
	"strings"
	"testing"
)

func TestProviderError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		pe       *ProviderError
		wantSubs []string
	}{
		{
			name:     "all fields set",
			pe:       &ProviderError{Provider: "aws", Code: "404", Message: "not found"},
			wantSubs: []string{"aws", "404", "not found"},
		},
		{
			name:     "empty provider",
			pe:       &ProviderError{Provider: "", Code: "500", Message: "server error"},
			wantSubs: []string{"500", "server error"},
		},
		{
			name:     "empty code",
			pe:       &ProviderError{Provider: "gcp", Code: "", Message: "oops"},
			wantSubs: []string{"gcp", "oops"},
		},
		{
			name:     "all empty",
			pe:       &ProviderError{},
			wantSubs: []string{"[]"},
		},
		{
			name:     "with details",
			pe:       &ProviderError{Provider: "azure", Code: "429", Message: "rate limited", Details: map[string]interface{}{"retry-after": "60"}},
			wantSubs: []string{"azure", "429", "rate limited"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := tc.pe.Error()
			for _, sub := range tc.wantSubs {
				if !strings.Contains(got, sub) {
					t.Errorf("Error() = %q, want substring %q", got, sub)
				}
			}
		})
	}
}

func TestProviderError_ErrorsAs(t *testing.T) {
	t.Parallel()

	orig := &ProviderError{Provider: "pve", Code: "NotFound", Message: "vm missing"}

	var target *ProviderError
	if !errors.As(orig, &target) {
		t.Fatal("errors.As should match *ProviderError")
	}

	if target.Provider != "pve" {
		t.Errorf("Provider = %q, want %q", target.Provider, "pve")
	}

	if target.Code != "NotFound" {
		t.Errorf("Code = %q, want %q", target.Code, "NotFound")
	}
}

func TestProviderError_ErrorsIs(t *testing.T) {
	t.Parallel()

	// ProviderError has no Unwrap and no custom Is — errors.Is checks pointer
	// equality only. Two distinct instances with identical fields are NOT equal.
	a := &ProviderError{Provider: "aws", Code: "404", Message: "not found"}
	b := &ProviderError{Provider: "aws", Code: "404", Message: "not found"}

	if errors.Is(a, b) {
		t.Error("errors.Is(a, b) should be false for distinct *ProviderError instances")
	}

	if !errors.Is(a, a) {
		t.Error("errors.Is(a, a) should be true (same pointer)")
	}
}

func TestErrProviderAlreadyRegistered(t *testing.T) {
	t.Parallel()

	err := ErrProviderAlreadyRegistered("aws")
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	if !strings.Contains(err.Error(), "aws") {
		t.Errorf("error %q should contain provider name", err.Error())
	}

	if !strings.Contains(err.Error(), "already registered") {
		t.Errorf("error %q should indicate already-registered", err.Error())
	}

	// Each call returns a new error value — not the same pointer.
	err2 := ErrProviderAlreadyRegistered("aws")
	if errors.Is(err, err2) {
		t.Error("two calls to ErrProviderAlreadyRegistered should return distinct errors")
	}
}

func TestErrProviderNotFound(t *testing.T) {
	t.Parallel()

	err := ErrProviderNotFound("stackit")
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	if !strings.Contains(err.Error(), "stackit") {
		t.Errorf("error %q should contain provider name", err.Error())
	}

	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q should indicate not-found", err.Error())
	}
}

func TestErrTimeoutWaitingForCondition(t *testing.T) {
	t.Parallel()

	err := ErrTimeoutWaitingForCondition("30s")
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	if !strings.Contains(err.Error(), "30s") {
		t.Errorf("error %q should contain timeout value", err.Error())
	}

	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error %q should contain 'timeout'", err.Error())
	}
}

func TestErrCircuitBreakerOpen(t *testing.T) {
	t.Parallel()

	if ErrCircuitBreakerOpen == nil {
		t.Fatal("ErrCircuitBreakerOpen must be non-nil sentinel")
	}

	if !errors.Is(ErrCircuitBreakerOpen, ErrCircuitBreakerOpen) {
		t.Error("errors.Is(ErrCircuitBreakerOpen, ErrCircuitBreakerOpen) must be true")
	}
}

func TestIsNotFound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"NotFound code", &ProviderError{Code: "NotFound"}, true},
		{"404 code", &ProviderError{Code: "404"}, true},
		{"other code", &ProviderError{Code: "500"}, false},
		{"nil error", nil, false},
		{"plain error", errors.New("not found"), false},
		{"empty code", &ProviderError{Code: ""}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := IsNotFound(tc.err)
			if got != tc.want {
				t.Errorf("IsNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsAlreadyExists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"AlreadyExists code", &ProviderError{Code: "AlreadyExists"}, true},
		{"409 code", &ProviderError{Code: "409"}, true},
		{"other code", &ProviderError{Code: "404"}, false},
		{"nil error", nil, false},
		{"plain error", errors.New("already exists"), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := IsAlreadyExists(tc.err)
			if got != tc.want {
				t.Errorf("IsAlreadyExists(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsUnauthorized(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"Unauthorized code", &ProviderError{Code: "Unauthorized"}, true},
		{"401 code", &ProviderError{Code: "401"}, true},
		{"other code", &ProviderError{Code: "403"}, false},
		{"nil error", nil, false},
		{"plain error", errors.New("unauthorized"), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := IsUnauthorized(tc.err)
			if got != tc.want {
				t.Errorf("IsUnauthorized(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
