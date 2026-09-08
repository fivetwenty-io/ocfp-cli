package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNetworkConfigDNSKeysConverge proves that whichever resolver key a bloc
// writes under network (dns, dnsServers, or dns_servers), both
// NetworkConfig.DNS and NetworkConfig.DNSServers hold the same list after
// decoding. Bootstrap reads DNSServers while env reads DNS, so a divergence
// silently drops the operator's resolvers on one of those paths.
func TestNetworkConfigDNSKeysConverge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{
			name: "dns only",
			yaml: "dns: [10.0.0.53, 10.0.0.54]",
			want: []string{"10.0.0.53", "10.0.0.54"},
		},
		{
			name: "dnsServers only",
			yaml: "dnsServers: [10.1.0.53]",
			want: []string{"10.1.0.53"},
		},
		{
			name: "dns_servers only",
			yaml: "dns_servers: [10.2.0.53, 10.2.0.54]",
			want: []string{"10.2.0.53", "10.2.0.54"},
		},
		{
			name: "dnsServers wins over dns",
			yaml: "dns: [9.9.9.9]\ndnsServers: [10.3.0.53]",
			want: []string{"10.3.0.53"},
		},
		{
			name: "dnsServers wins over dns_servers and dns",
			yaml: "dns: [9.9.9.9]\ndns_servers: [8.8.4.4]\ndnsServers: [10.4.0.53]",
			want: []string{"10.4.0.53"},
		},
		{
			name: "dns_servers wins over dns",
			yaml: "dns: [9.9.9.9]\ndns_servers: [10.5.0.53]",
			want: []string{"10.5.0.53"},
		},
		{
			name: "none set leaves both empty",
			yaml: "cidr: 10.0.0.0/24",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var nc config.NetworkConfig

			require.NoError(t, yaml.Unmarshal([]byte(tt.yaml), &nc))

			assert.Equal(t, tt.want, nc.DNS, "NetworkConfig.DNS")
			assert.Equal(t, tt.want, nc.DNSServers, "NetworkConfig.DNSServers")
		})
	}
}

// writePVEBlocConfig writes a config.yml holding a single PVE bloc named
// "lab" whose body is the supplied YAML (indented under the bloc), and
// returns the path. A fresh directory per call keeps the load cache cold.
func writePVEBlocConfig(t *testing.T, blocBody string) string {
	t.Helper()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")

	body := "blocs:\n  lab:\n    provider: pve\n    api_endpoint: https://pve.example:8006\n    auth_token: \"root@pam!ocfp\"\n    token_secret: secret\n"

	for line := range strings.SplitSeq(blocBody, "\n") {
		body += "    " + line + "\n"
	}

	require.NoError(t, os.WriteFile(cfgPath, []byte(body), 0o600))

	return cfgPath
}

// TestLoadConvergesDNSAcrossBlocAndNetwork exercises the full
// LoadWithParams path that `ocfp bootstrap --bloc X` uses and checks that
// resolvers set under any network key reach every consumer: both
// NetworkConfig fields and the bloc-level dns list the vault populate
// writes into network and subnet records.
func TestLoadConvergesDNSAcrossBlocAndNetwork(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		blocBody    string
		wantNetwork []string
		wantBloc    []string
	}{
		{
			name:        "network.dns only feeds DNSServers and bloc dns",
			blocBody:    "network:\n  cidr: 10.61.148.0/24\n  dns: [10.61.148.53]",
			wantNetwork: []string{"10.61.148.53"},
			wantBloc:    []string{"10.61.148.53"},
		},
		{
			name:        "network.dnsServers only feeds DNS and bloc dns",
			blocBody:    "network:\n  cidr: 10.61.148.0/24\n  dnsServers: [10.61.148.53, 10.61.148.54]",
			wantNetwork: []string{"10.61.148.53", "10.61.148.54"},
			wantBloc:    []string{"10.61.148.53", "10.61.148.54"},
		},
		{
			name:        "network.dns_servers only feeds DNS and bloc dns",
			blocBody:    "network:\n  cidr: 10.61.148.0/24\n  dns_servers: [10.61.148.55]",
			wantNetwork: []string{"10.61.148.55"},
			wantBloc:    []string{"10.61.148.55"},
		},
		{
			name:        "dnsServers wins over dns and bloc dns follows",
			blocBody:    "network:\n  cidr: 10.61.148.0/24\n  dns: [9.9.9.9]\n  dnsServers: [10.61.148.53]",
			wantNetwork: []string{"10.61.148.53"},
			wantBloc:    []string{"10.61.148.53"},
		},
		{
			name:        "explicit bloc dns is kept over network resolvers",
			blocBody:    "dns: [10.61.148.2]\nnetwork:\n  cidr: 10.61.148.0/24\n  dnsServers: [10.61.148.53]",
			wantNetwork: []string{"10.61.148.53"},
			wantBloc:    []string{"10.61.148.2"},
		},
		{
			name:        "pve bloc with no resolvers stays empty",
			blocBody:    "network:\n  cidr: 10.61.148.0/24",
			wantNetwork: nil,
			wantBloc:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfgPath := writePVEBlocConfig(t, tt.blocBody)

			cfg, err := config.LoadWithParams(cfgPath, "lab")
			require.NoError(t, err)

			assert.Equal(t, tt.wantNetwork, cfg.Network.DNS, "Network.DNS")
			assert.Equal(t, tt.wantNetwork, cfg.Network.DNSServers, "Network.DNSServers")
			assert.Equal(t, tt.wantBloc, cfg.DNS, "bloc-level DNS")
		})
	}
}

// TestLoadPVEDefaultsBastionFlavor proves a PVE bloc loads with the
// "bastion" flavor preset when bastion.flavor is omitted, keeps an explicit
// flavor untouched, and therefore accepts artifacts.enabled without the
// operator hand-setting the flavor to satisfy the bastion-enabled check.
func TestLoadPVEDefaultsBastionFlavor(t *testing.T) {
	t.Parallel()

	t.Run("omitted flavor defaults to bastion preset", func(t *testing.T) {
		t.Parallel()

		cfgPath := writePVEBlocConfig(t, "bastion:\n  image: ubuntu-noble-template")

		cfg, err := config.LoadWithParams(cfgPath, "lab")
		require.NoError(t, err)

		assert.Equal(t, "bastion", cfg.Bastion.Flavor)
	})

	t.Run("explicit flavor is preserved", func(t *testing.T) {
		t.Parallel()

		cfgPath := writePVEBlocConfig(t, "bastion:\n  flavor: large")

		cfg, err := config.LoadWithParams(cfgPath, "lab")
		require.NoError(t, err)

		assert.Equal(t, "large", cfg.Bastion.Flavor)
	})

	t.Run("artifacts enabled loads without a hand-set flavor", func(t *testing.T) {
		t.Parallel()

		cfgPath := writePVEBlocConfig(t, "artifacts:\n  enabled: true")

		cfg, err := config.LoadWithParams(cfgPath, "lab")
		require.NoError(t, err)

		assert.True(t, cfg.Artifacts.Enabled)
		assert.Equal(t, "bastion", cfg.Bastion.Flavor)
	})
}
