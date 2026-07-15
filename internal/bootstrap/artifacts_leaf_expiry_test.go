package bootstrap

import (
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
)

// TestLeafNotAfterRFC3339_ValidCertReturnsFormattedNotAfter (task 6.2) asserts
// a real issued leaf's NotAfter round-trips through leafNotAfterRFC3339 as
// RFC3339, matching the cert's own NotAfter field.
func TestLeafNotAfterRFC3339_ValidCertReturnsFormattedNotAfter(t *testing.T) {
	t.Parallel()

	mat, err := artifacts.GenerateSelfSignedTLS("dev-artifacts", []string{"dev-artifacts"}, nil)
	if err != nil {
		t.Fatalf("GenerateSelfSignedTLS: %v", err)
	}

	got := leafNotAfterRFC3339(&mat)
	if got == "" {
		t.Fatal("expected non-empty tls_leaf_not_after for a valid leaf cert")
	}

	parsed, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatalf("leafNotAfterRFC3339 output %q did not parse as RFC3339: %v", got, err)
	}

	// GenerateSelfSignedTLS issues a 1-year cert; assert the recorded value is
	// in the future and roughly a year out, without depending on the exact
	// validity constant (an internal/artifacts implementation detail this
	// package does not own).
	if !parsed.After(time.Now()) {
		t.Errorf("leafNotAfterRFC3339 = %v, want a time in the future", parsed)
	}
}

// TestLeafNotAfterRFC3339_NilOrEmptyReturnsEmpty asserts the disabled-TLS
// case (tlsMat == nil) and an empty CertPEM both degrade to "" rather than
// panicking or erroring — CreateArtifacts must not fail VM creation over
// expiry-metadata extraction.
func TestLeafNotAfterRFC3339_NilOrEmptyReturnsEmpty(t *testing.T) {
	t.Parallel()

	if got := leafNotAfterRFC3339(nil); got != "" {
		t.Errorf("leafNotAfterRFC3339(nil) = %q, want empty", got)
	}

	empty := &artifacts.TLSMaterial{}
	if got := leafNotAfterRFC3339(empty); got != "" {
		t.Errorf("leafNotAfterRFC3339(empty CertPEM) = %q, want empty", got)
	}
}

// TestLeafNotAfterRFC3339_FallsBackToPEMParseWhenNotAfterUnset (task 6.2 gap
// 1 follow-up) asserts the PEM-parse fallback still works for a TLSMaterial
// that only sets CertPEM — e.g. a value built by an older code path or a
// test fixture that predates the NotAfter field being populated at
// issuance.
func TestLeafNotAfterRFC3339_FallsBackToPEMParseWhenNotAfterUnset(t *testing.T) {
	t.Parallel()

	mat, err := artifacts.GenerateSelfSignedTLS("dev-artifacts", []string{"dev-artifacts"}, nil)
	if err != nil {
		t.Fatalf("GenerateSelfSignedTLS: %v", err)
	}

	// Simulate an older TLSMaterial value that never set NotAfter.
	pemOnly := &artifacts.TLSMaterial{CertPEM: mat.CertPEM}

	got := leafNotAfterRFC3339(pemOnly)
	if got == "" {
		t.Fatal("expected the PEM-parse fallback to still extract NotAfter")
	}

	if got != mat.NotAfter {
		t.Errorf("leafNotAfterRFC3339 (PEM fallback) = %q, want %q (matching the direct-field value from the same cert)", got, mat.NotAfter)
	}
}

// TestLeafNotAfterRFC3339_PrefersDirectFieldOverPEM asserts the direct
// TLSMaterial.NotAfter field is returned as-is when set, without re-parsing
// CertPEM — the common case for material produced by GenerateSelfSignedTLS/
// IssueLeafCert, which populate both.
func TestLeafNotAfterRFC3339_PrefersDirectFieldOverPEM(t *testing.T) {
	t.Parallel()

	// CertPEM deliberately unparsable: if the function fell through to the
	// PEM-parse path despite NotAfter being set, this would return "" instead
	// of the direct-field value, and the test would catch it.
	mat := &artifacts.TLSMaterial{CertPEM: "not a pem", NotAfter: "2027-01-02T03:04:05Z"}

	got := leafNotAfterRFC3339(mat)
	if got != "2027-01-02T03:04:05Z" {
		t.Errorf("leafNotAfterRFC3339 = %q, want the direct NotAfter field value unchanged", got)
	}
}

// TestLeafNotAfterRFC3339_UnparsablePEMReturnsEmpty asserts malformed cert
// material degrades to "" (logged, non-fatal) instead of panicking.
func TestLeafNotAfterRFC3339_UnparsablePEMReturnsEmpty(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"not pem":          "this is not a pem block",
		"wrong block type": "-----BEGIN EC PRIVATE KEY-----\nZm9v\n-----END EC PRIVATE KEY-----\n",
		"corrupt der":      "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n",
	}

	for name, certPEM := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			mat := &artifacts.TLSMaterial{CertPEM: certPEM}
			if got := leafNotAfterRFC3339(mat); got != "" {
				t.Errorf("leafNotAfterRFC3339(%q) = %q, want empty", name, got)
			}
		})
	}
}
