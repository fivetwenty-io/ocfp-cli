package config_test

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/goccy/go-yaml"
)

// TestNetworkConfigUnmarshalAliases proves the YAML decoder accepts the
// snake_case network_cidr key in addition to the documented cidr and
// networkCidr forms. The snake_case form is what PVE configs in the wild
// use, and yaml.v3 alone would silently drop it.
func TestNetworkConfigUnmarshalAliases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		yaml            string
		wantCIDR        string
		wantNetworkCIDR string
	}{
		{
			name:            "cidr key",
			yaml:            "cidr: 192.168.1.0/24",
			wantCIDR:        "192.168.1.0/24",
			wantNetworkCIDR: "192.168.1.0/24",
		},
		{
			name:            "networkCidr camelCase key",
			yaml:            "networkCidr: 10.0.0.0/20",
			wantCIDR:        "10.0.0.0/20",
			wantNetworkCIDR: "10.0.0.0/20",
		},
		{
			name:            "network_cidr snake_case key",
			yaml:            "network_cidr: 192.168.1.0/24",
			wantCIDR:        "192.168.1.0/24",
			wantNetworkCIDR: "192.168.1.0/24",
		},
		{
			name:            "cidr wins over networkCidr",
			yaml:            "cidr: 10.0.0.0/16\nnetworkCidr: 172.16.0.0/16",
			wantCIDR:        "10.0.0.0/16",
			wantNetworkCIDR: "172.16.0.0/16",
		},
		{
			name:            "networkCidr wins over network_cidr",
			yaml:            "networkCidr: 172.16.0.0/16\nnetwork_cidr: 192.168.0.0/16",
			wantCIDR:        "172.16.0.0/16",
			wantNetworkCIDR: "172.16.0.0/16",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var nc config.NetworkConfig

			err := yaml.Unmarshal([]byte(tt.yaml), &nc)
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if nc.CIDR != tt.wantCIDR {
				t.Errorf("CIDR = %q, want %q", nc.CIDR, tt.wantCIDR)
			}

			if nc.NetworkCIDR != tt.wantNetworkCIDR {
				t.Errorf("NetworkCIDR = %q, want %q", nc.NetworkCIDR, tt.wantNetworkCIDR)
			}
		})
	}
}

// TestNetworkConfigUnmarshalScalarFields confirms the alias hook still
// reads non-CIDR fields (name, subnetId/subnet_id, dns servers) so adding
// the hook didn't regress unrelated network keys.
func TestNetworkConfigUnmarshalScalarFields(t *testing.T) {
	t.Parallel()

	const input = `
name: vmbr0
cidr: 192.168.1.0/24
subnet_id: subnet-123
dns_servers: [1.1.1.1, 8.8.8.8]
`

	var nc config.NetworkConfig

	err := yaml.Unmarshal([]byte(input), &nc)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if nc.Name != "vmbr0" {
		t.Errorf("Name = %q, want vmbr0", nc.Name)
	}

	if nc.SubnetID != "subnet-123" {
		t.Errorf("SubnetID = %q, want subnet-123", nc.SubnetID)
	}

	if len(nc.DNSServers) != 2 || nc.DNSServers[0] != "1.1.1.1" || nc.DNSServers[1] != "8.8.8.8" {
		t.Errorf("DNSServers = %v, want [1.1.1.1 8.8.8.8]", nc.DNSServers)
	}
}
