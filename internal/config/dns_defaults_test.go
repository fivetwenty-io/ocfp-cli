package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplyDefaultsConvergesDNSWithoutYAML covers the programmatic path: a
// Config assembled in code (no UnmarshalYAML hook) still converges its
// resolver fields and inherits the bloc-level dns list when applyDefaults
// runs, for every provider including ones with no public-resolver fallback.
func TestApplyDefaultsConvergesDNSWithoutYAML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		provider    string
		network     NetworkConfig
		blocDNS     []string
		wantNetwork []string
		wantBloc    []string
	}{
		{
			name:        "pve DNS only",
			provider:    "pve",
			network:     NetworkConfig{DNS: []string{"10.0.0.53"}},
			wantNetwork: []string{"10.0.0.53"},
			wantBloc:    []string{"10.0.0.53"},
		},
		{
			name:        "pve DNSServers only",
			provider:    "pve",
			network:     NetworkConfig{DNSServers: []string{"10.0.0.54"}},
			wantNetwork: []string{"10.0.0.54"},
			wantBloc:    []string{"10.0.0.54"},
		},
		{
			name:        "pve DNSServers wins over DNS",
			provider:    "pve",
			network:     NetworkConfig{DNS: []string{"9.9.9.9"}, DNSServers: []string{"10.0.0.55"}},
			wantNetwork: []string{"10.0.0.55"},
			wantBloc:    []string{"10.0.0.55"},
		},
		{
			name:        "pve explicit bloc DNS kept",
			provider:    "pve",
			network:     NetworkConfig{DNSServers: []string{"10.0.0.55"}},
			blocDNS:     []string{"10.0.0.2"},
			wantNetwork: []string{"10.0.0.55"},
			wantBloc:    []string{"10.0.0.2"},
		},
		{
			name:        "stackit inherits network resolvers before public fallback",
			provider:    "stackit",
			network:     NetworkConfig{DNS: []string{"10.4.0.53"}},
			wantNetwork: []string{"10.4.0.53"},
			wantBloc:    []string{"10.4.0.53"},
		},
		{
			name:        "aws inherits network resolvers before public fallback",
			provider:    "aws",
			network:     NetworkConfig{DNSServers: []string{"10.5.0.53"}},
			wantNetwork: []string{"10.5.0.53"},
			wantBloc:    []string{"10.5.0.53"},
		},
		{
			name:        "stackit with nothing set still gets public fallback",
			provider:    "stackit",
			wantNetwork: []string{dnsCloudflare, "8.8.8.8"},
			wantBloc:    []string{dnsCloudflare, "8.8.8.8"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &Config{Provider: tt.provider, Network: tt.network, DNS: tt.blocDNS}

			require.NoError(t, applyDefaults(cfg, tt.provider))

			assert.Equal(t, tt.wantNetwork, cfg.Network.DNS, "Network.DNS")
			assert.Equal(t, tt.wantNetwork, cfg.Network.DNSServers, "Network.DNSServers")
			assert.Equal(t, tt.wantBloc, cfg.DNS, "bloc-level DNS")
		})
	}
}

// TestApplyDefaultsPVEBastionFlavor checks the PVE provider case of
// applyDefaults fills the bastion flavor preset and leaves an explicit one
// alone, matching the other providers' behaviour.
func TestApplyDefaultsPVEBastionFlavor(t *testing.T) {
	t.Parallel()

	empty := &Config{Provider: "pve"}
	require.NoError(t, applyDefaults(empty, "pve"))
	assert.Equal(t, "bastion", empty.Bastion.Flavor)

	explicit := &Config{Provider: "pve", Bastion: Bastion{Flavor: "xlarge"}}
	require.NoError(t, applyDefaults(explicit, "pve"))
	assert.Equal(t, "xlarge", explicit.Bastion.Flavor)
}
