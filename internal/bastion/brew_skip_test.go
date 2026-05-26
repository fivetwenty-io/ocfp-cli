package bastion

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// brewSkipped must be true for PVE (tools come from the provision script and
// the lab CPU cannot run Linuxbrew) and false for cloud providers that rely on
// brew for tool delivery.
func TestBrewSkippedByProvider(t *testing.T) {
	cases := map[string]bool{
		"pve":     true,
		"PVE":     true,
		"aws":     false,
		"stackit": false,
		"":        false,
	}

	for provider, want := range cases {
		mgr := NewManager(&config.Config{Provider: provider}, &ProvisioningOptions{})
		if got := mgr.brewSkipped(); got != want {
			t.Errorf("brewSkipped(provider=%q) = %v, want %v", provider, got, want)
		}
	}
}
