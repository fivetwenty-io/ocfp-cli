package bootstrap_test

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// makeProviderManager builds the minimal Manager needed to exercise the
// provider-dispatch helpers — they only read m.options.Provider and
// m.config.Network.SubnetStrategy, so a nil state manager/provider is fine
// and avoids the heavier setupComputeTest scaffolding for what are pure
// boolean predicates.
func makeProviderManager(t *testing.T, provider, subnetStrategy string) *bootstrap.Manager {
	t.Helper()

	cfg := &config.Config{
		Network: config.NetworkConfig{
			SubnetStrategy: subnetStrategy,
		},
	}

	return bootstrap.NewManager(cfg, nil, nil, &bootstrap.Options{
		BlocName: "test",
		Provider: provider,
	})
}

// TestUseVirtualSubnets verifies the dispatcher that routes "no native
// subnet" providers through the logical-subnet (state-only) path instead of
// calling CreateSubnet on the wire. The triple-subnet strategy override
// must work for any provider.
func TestUseVirtualSubnets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		provider       string
		subnetStrategy string
		want           bool
	}{
		{name: "stackit (no native subnets)", provider: "stackit", want: true},
		{name: "pve (bridge mode flat L2)", provider: "pve", want: true},
		{name: "PVE uppercase", provider: "PVE", want: true},
		{name: "Stackit mixed case", provider: "Stackit", want: true},
		{name: "aws uses native subnets", provider: "aws", want: false},
		{name: "gcp uses native subnets", provider: "gcp", want: false},
		{name: "empty provider falls through", provider: "", want: false},
		{name: "unknown provider falls through", provider: "vsphere", want: false},
		{
			name:           "ocfp-triple strategy forces virtual for aws too",
			provider:       "aws",
			subnetStrategy: "ocfp-triple",
			want:           true,
		},
		{
			name:           "ocfp-triple strategy on stackit still virtual",
			provider:       "stackit",
			subnetStrategy: "ocfp-triple",
			want:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := makeProviderManager(t, tt.provider, tt.subnetStrategy)

			got := m.UseVirtualSubnets()
			if got != tt.want {
				t.Errorf("UseVirtualSubnets(provider=%q, strategy=%q) = %v, want %v",
					tt.provider, tt.subnetStrategy, got, tt.want)
			}
		})
	}
}

// TestProviderUsesLocalKeypairs verifies the dispatcher that routes providers
// without a server-side CreateKeyPair primitive through the local-gen +
// import-public-key path.
func TestProviderUsesLocalKeypairs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		want     bool
	}{
		{name: "stackit needs local keys", provider: "stackit", want: true},
		{name: "pve needs local keys (cloud-init)", provider: "pve", want: true},
		{name: "PVE uppercase", provider: "PVE", want: true},
		{name: "aws has native CreateKeyPair", provider: "aws", want: false},
		{name: "gcp has native CreateKeyPair", provider: "gcp", want: false},
		{name: "empty provider falls through", provider: "", want: false},
		{name: "unknown provider falls through", provider: "vsphere", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := makeProviderManager(t, tt.provider, "")

			got := m.ProviderUsesLocalKeypairs()
			if got != tt.want {
				t.Errorf("ProviderUsesLocalKeypairs(provider=%q) = %v, want %v",
					tt.provider, got, tt.want)
			}
		})
	}
}

// TestProviderDisplayName covers the human-facing label used in log/console
// strings since the rename away from STACKIT-specific names.
func TestProviderDisplayName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		want     string
	}{
		{name: "pve capitalized", provider: "pve", want: "PVE"},
		{name: "PVE already uppercase", provider: "PVE", want: "PVE"},
		{name: "aws", provider: "aws", want: "AWS"},
		{name: "gcp", provider: "gcp", want: "GCP"},
		{name: "stackit", provider: "stackit", want: "STACKIT"},
		{name: "Stackit mixed", provider: "Stackit", want: "STACKIT"},
		{name: "unknown provider passthrough", provider: "vsphere", want: "vsphere"},
		{name: "empty defaults to provider", provider: "", want: "provider"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := makeProviderManager(t, tt.provider, "")

			got := m.ProviderDisplayName()
			if got != tt.want {
				t.Errorf("ProviderDisplayName(provider=%q) = %q, want %q",
					tt.provider, got, tt.want)
			}
		})
	}
}

// TestAdjustSubnetForProvider covers the helper that converts the
// "virtual:<name>" state-only subnet ID into the form the provider's
// CreateInstance expects — empty for providers without native subnets, or
// the ID unchanged for everything else.
func TestAdjustSubnetForProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		subnetID string
		want     string
	}{
		{
			name:     "pve strips virtual prefix",
			provider: "pve",
			subnetID: "virtual:520-pve-wayne-ocfp-0",
			want:     "",
		},
		{
			name:     "stackit strips virtual prefix",
			provider: "stackit",
			subnetID: "virtual:prod-ocfp-0",
			want:     "",
		},
		{
			name:     "PVE uppercase strips virtual",
			provider: "PVE",
			subnetID: "virtual:foo",
			want:     "",
		},
		{
			name:     "aws virtual prefix kept (unexpected but not stripped)",
			provider: "aws",
			subnetID: "virtual:foo",
			want:     "virtual:foo",
		},
		{
			name:     "non-virtual id passes through unchanged",
			provider: "pve",
			subnetID: "subnet-abc123",
			want:     "subnet-abc123",
		},
		{
			name:     "empty subnet id passes through",
			provider: "pve",
			subnetID: "",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := makeProviderManager(t, tt.provider, "")

			got := m.AdjustSubnetForProvider(tt.subnetID)
			if got != tt.want {
				t.Errorf("AdjustSubnetForProvider(provider=%q, id=%q) = %q, want %q",
					tt.provider, tt.subnetID, got, tt.want)
			}
		})
	}
}
