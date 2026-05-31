package bastion

import (
	"context"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// brewSkipped must be false for every provider: Linuxbrew is the primary tool
// source everywhere, including PVE. The former PVE skip existed because the
// default kvm64 VM CPU lacks the SSSE3 instructions Linuxbrew bottles need, but
// OCFP now provisions PVE VMs with cpu=host so bottles run.
func TestBrewSkippedByProvider(t *testing.T) {
	cases := map[string]bool{
		"pve":     false,
		"PVE":     false,
		"aws":     false,
		"stackit": false,
		"":        false,
	}

	for provider, want := range cases {
		mgr := NewManager(context.Background(), &config.Config{Provider: provider}, &ProvisioningOptions{})
		if got := mgr.brewSkipped(); got != want {
			t.Errorf("brewSkipped(provider=%q) = %v, want %v", provider, got, want)
		}
	}
}
