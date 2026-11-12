package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
)

// An operator who types the bastion's address or host name instead of
// "bastion" still reaches the bastion, so its host key is checked.
func TestSSHAddressIsBastion(t *testing.T) {
	t.Parallel()

	resolved := func(context.Context) (string, error) { return "10.61.148.10", nil }
	unresolved := func(context.Context) (string, error) { return "", errors.New("no provider") } //nolint:err113 // test error

	named := &config.Config{ //nolint:exhaustruct // only the bastion fields matter
		Name:      "b",
		BastionIP: "100.109.226.53",
		Tailscale: &config.TailscaleConfig{Hostname: "Lab-Jump"}, //nolint:exhaustruct // only the host name matters
	}
	unnamed := &config.Config{Name: "b"} //nolint:exhaustruct // only the bloc name matters

	tests := []struct {
		name    string
		address string
		cfg     *config.Config
		lookup  func(context.Context) (string, error)
		want    bool
	}{
		{"ConfiguredBastionIP", "100.109.226.53", named, unresolved, true},
		{"ResolvedBastionIP", "10.61.148.10", unnamed, resolved, true},
		{"ResolvedBastionIPv6", "FD00::0:10", unnamed, func(context.Context) (string, error) { return "fd00::10", nil }, true},
		{"TailscaleHostName", "lab-jump", named, unresolved, true},
		{"TailscaleHostNameAnyCase", "LAB-JUMP", named, unresolved, true},
		{"MagicDNSName", "lab-jump.tail1234.ts.net.", named, unresolved, true},
		{"DefaultHostName", "B-Bastion.tail1234.ts.net", unnamed, unresolved, true},
		{"OtherAddress", "10.64.64.20", named, resolved, false},
		{"OtherName", "lab-jump-2.tail1234.ts.net", named, resolved, false},
		{"NothingResolves", "10.61.148.10", unnamed, unresolved, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sshAddressIsBastion(context.Background(), tt.address, tt.cfg, tt.lookup)
			if got != tt.want {
				t.Errorf("sshAddressIsBastion(%q) = %v, want %v", tt.address, got, tt.want)
			}
		})
	}
}

// The configured address and host name answer without a provider lookup,
// which can take seconds.
func TestSSHAddressIsBastionPrefersTheConfig(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "b", BastionIP: "100.109.226.53"} //nolint:exhaustruct // only the bastion fields matter

	lookup := func(context.Context) (string, error) {
		t.Fatal("the configured bastion address needs no lookup")

		return "", nil
	}

	if !sshAddressIsBastion(context.Background(), "100.109.226.53", cfg, lookup) {
		t.Error("the configured bastion address was not recognised")
	}
}
