package artifacts

import (
	"errors"
	"strings"
	"testing"
)

// TestInternalCAVaultError_NamesFixesAndAttemptedAddr asserts the actionable
// error text names, in order, the vault-inception fix, the env/`safe target`
// fallback, and the self-signed escape hatch — plus the bloc name and the
// underlying cause. Regression guard for the "which vault, what do I run"
// gap the incident surfaced.
func TestInternalCAVaultError_NamesFixesAndAttemptedAddr(t *testing.T) {
	t.Setenv("VAULT_ADDR", "")

	cause := errors.New("connection refused")

	err := InternalCAVaultError("ocfp-lab-wayne", cause)
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	msg := err.Error()

	for _, want := range []string{
		"ocfp-lab-wayne",
		"ocfp vault inception --bloc ocfp-lab-wayne",
		"VAULT_ADDR/VAULT_TOKEN",
		"safe target",
		"artifacts.tls.mode: self-signed",
		"connection refused",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q; got: %s", want, msg)
		}
	}

	if !errors.Is(err, cause) {
		t.Errorf("error does not wrap the original cause: %v", err)
	}
}

// TestDescribeVaultAddrAttempt_UsesVAULT_ADDRWhenSet asserts the attempted
// address text reflects an explicit VAULT_ADDR rather than the fallback
// description, so operators who did set it are not told it is unset.
func TestDescribeVaultAddrAttempt_UsesVAULT_ADDRWhenSet(t *testing.T) {
	t.Setenv("VAULT_ADDR", "https://vault.example.internal:8200")

	got := DescribeVaultAddrAttempt()
	if got != "https://vault.example.internal:8200" {
		t.Errorf("DescribeVaultAddrAttempt() = %q, want the VAULT_ADDR value", got)
	}
}

// TestDescribeVaultAddrAttempt_FallbackWhenUnset asserts the fallback
// description is returned (not empty, not a guess at an address) when
// VAULT_ADDR is unset.
func TestDescribeVaultAddrAttempt_FallbackWhenUnset(t *testing.T) {
	t.Setenv("VAULT_ADDR", "")

	got := DescribeVaultAddrAttempt()
	if !strings.Contains(got, "saferc") || !strings.Contains(got, "127.0.0.1:8200") {
		t.Errorf("DescribeVaultAddrAttempt() = %q, want it to describe the ~/.saferc + localhost fallback", got)
	}
}
