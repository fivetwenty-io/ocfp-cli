package commands

import (
	"strings"
	"testing"
)

// vault import has no fqdns phase, so its --force help must not mention one.
func TestVaultImportForceHelpHasNoFQDNsWording(t *testing.T) {
	t.Parallel()

	flag := newVaultImportCmd().Flags().Lookup("force")
	if flag == nil {
		t.Fatal("vault import has no --force flag")
	}

	if flag.Usage != "overwrite existing secrets" {
		t.Errorf("--force usage = %q, want %q", flag.Usage, "overwrite existing secrets")
	}

	if strings.Contains(strings.ToLower(flag.Usage), "fqdn") {
		t.Errorf("--force usage mentions fqdns: %q", flag.Usage)
	}
}
