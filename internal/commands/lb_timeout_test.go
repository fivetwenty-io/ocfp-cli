package commands

import (
	"errors"
	"math"
	"strconv"
	"testing"
)

// TestValidateHealthCheckTimeout_Bounds verifies the health check timeout is
// constrained to the range that survives the int32 conversions performed by
// the provider load balancer managers. Without the upper bound, a value above
// math.MaxInt32 wraps silently rather than erroring.
func TestValidateHealthCheckTimeout_Bounds(t *testing.T) {
	valid := []int{1, 5, 120, 300, math.MaxInt32}
	for _, timeout := range valid {
		t.Run("valid/"+strconv.Itoa(timeout), func(t *testing.T) {
			if err := validateHealthCheckTimeout(timeout); err != nil {
				t.Fatalf("validateHealthCheckTimeout(%d) error = %v, want nil", timeout, err)
			}
		})
	}

	invalid := []int{math.MinInt32, -1, 0, math.MaxInt32 + 1, math.MaxInt64}
	for _, timeout := range invalid {
		t.Run("invalid/"+strconv.Itoa(timeout), func(t *testing.T) {
			err := validateHealthCheckTimeout(timeout)
			if err == nil {
				t.Fatalf("validateHealthCheckTimeout(%d) = nil, want error", timeout)
			}

			if !errors.Is(err, ErrInvalidHealthCheckTimeout) {
				t.Fatalf("validateHealthCheckTimeout(%d) error = %v, want wrapped ErrInvalidHealthCheckTimeout", timeout, err)
			}
		})
	}
}

// TestValidateHealthCheckTimeout_RejectsInt32Truncation pins the specific
// defect: 1<<32 truncates to 0 under int32 conversion, so an unvalidated
// value silently becomes a zero timeout instead of being rejected.
func TestValidateHealthCheckTimeout_RejectsInt32Truncation(t *testing.T) {
	wrapsToZero := 1 << 32

	// #nosec G115 -- the truncation is the defect under test; it must happen here.
	if truncated := int32(wrapsToZero); truncated != 0 {
		t.Fatalf("precondition failed: int32(%d) = %d, want 0", wrapsToZero, truncated)
	}

	if err := validateHealthCheckTimeout(wrapsToZero); err == nil {
		t.Fatalf("validateHealthCheckTimeout(%d) = nil, want error: value truncates to 0", wrapsToZero)
	}
}

// TestLBUpdate_RejectsOutOfRangeTimeoutBeforeProvider verifies the bound is
// enforced on the actual `ocfp lb update` code path, and that it is enforced
// early enough to fail before any cloud provider is contacted. Placing the
// check after setupLBProvider would surface a provider error here instead.
func TestLBUpdate_RejectsOutOfRangeTimeoutBeforeProvider(t *testing.T) {
	cmd := newLBUpdateCmd()
	cmd.SetArgs([]string{"cf-router", "--health-check", "/health", "--timeout", "4294967296"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	if err == nil {
		t.Fatal("lb update with --timeout 4294967296 returned nil, want error")
	}

	if !errors.Is(err, ErrInvalidHealthCheckTimeout) {
		t.Fatalf("lb update error = %v, want wrapped ErrInvalidHealthCheckTimeout (validation must run before provider setup)", err)
	}
}

// TestLBUpdate_AcceptsInRangeTimeout guards against the bound rejecting a
// value operators legitimately pass today. It asserts only that the failure
// is not the timeout validation; reaching provider setup is expected here.
func TestLBUpdate_AcceptsInRangeTimeout(t *testing.T) {
	cmd := newLBUpdateCmd()
	cmd.SetArgs([]string{"cf-router", "--health-check", "/health", "--timeout", "30"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	if errors.Is(err, ErrInvalidHealthCheckTimeout) {
		t.Fatalf("lb update with in-range --timeout 30 was rejected by timeout validation: %v", err)
	}
}
