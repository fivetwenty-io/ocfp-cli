package config_test

import (
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/ocfp/ocfp-cli-go/internal/config"
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

// TestNetworkConfigUnmarshalAvailableBandAliases proves the reserved-IP
// available-band override fields parse from both the documented camelCase
// keys and the snake_case aliases, with camelCase taking precedence when both
// are present (matching every other aliased field on NetworkConfig).
func TestNetworkConfigUnmarshalAvailableBandAliases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		yaml      string
		wantStart int
		wantEnd   int
	}{
		{
			name:      "camelCase keys",
			yaml:      "availableBandStart: 12\navailableBandEnd: 509",
			wantStart: 12,
			wantEnd:   509,
		},
		{
			name:      "snake_case keys",
			yaml:      "available_band_start: 12\navailable_band_end: 509",
			wantStart: 12,
			wantEnd:   509,
		},
		{
			name:      "camelCase wins over snake_case",
			yaml:      "availableBandStart: 20\navailableBandEnd: 400\navailable_band_start: 99\navailable_band_end: 999",
			wantStart: 20,
			wantEnd:   400,
		},
		{
			name:      "unset fields default to zero (no override)",
			yaml:      "cidr: 10.0.0.0/16",
			wantStart: 0,
			wantEnd:   0,
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

			if nc.AvailableBandStart != tt.wantStart {
				t.Errorf("AvailableBandStart = %d, want %d", nc.AvailableBandStart, tt.wantStart)
			}

			if nc.AvailableBandEnd != tt.wantEnd {
				t.Errorf("AvailableBandEnd = %d, want %d", nc.AvailableBandEnd, tt.wantEnd)
			}
		})
	}
}
