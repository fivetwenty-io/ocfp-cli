package config_test

import (
	"strings"
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

// TestNetworkConfigAvailableBandStart_HardError proves the removed
// single-band override keys (network.availableBandStart/End, in every
// supported alias form) fail config load with a hard error naming both
// replacement keys, rather than being silently ignored. A config that sets
// neither the old keys nor the new network.bands keys, and one that sets
// only the new network.bands keys, must still load cleanly.
func TestNetworkConfigAvailableBandStart_HardError(t *testing.T) {
	t.Parallel()

	errorCases := []struct {
		name string
		yaml string
	}{
		{
			name: "camelCase keys",
			yaml: "availableBandStart: 12\navailableBandEnd: 509",
		},
		{
			name: "snake_case keys",
			yaml: "available_band_start: 12\navailable_band_end: 509",
		},
		{
			name: "camelCase and snake_case both set",
			yaml: "availableBandStart: 20\navailableBandEnd: 400\navailable_band_start: 99\navailable_band_end: 999",
		},
		{
			name: "only start key set",
			yaml: "availableBandStart: 12",
		},
		{
			name: "only end key set",
			yaml: "availableBandEnd: 509",
		},
		{
			name: "only snake_case start key set",
			yaml: "available_band_start: 12",
		},
		{
			name: "only snake_case end key set",
			yaml: "available_band_end: 509",
		},
	}

	for _, tt := range errorCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var nc config.NetworkConfig

			err := yaml.Unmarshal([]byte(tt.yaml), &nc)
			if err == nil {
				t.Fatalf("unmarshal: want error, got nil")
			}

			if !strings.Contains(err.Error(), "network.bands.infra") {
				t.Errorf("error %q missing network.bands.infra", err.Error())
			}

			if !strings.Contains(err.Error(), "network.bands.mgmt") {
				t.Errorf("error %q missing network.bands.mgmt", err.Error())
			}
		})
	}

	okCases := []struct {
		name string
		yaml string
	}{
		{
			name: "neither old nor new keys set",
			yaml: "cidr: 10.0.0.0/16",
		},
		{
			name: "new bands keys set",
			yaml: "cidr: 10.0.0.0/16\nbands:\n  infra:\n    start: 12\n    end: 199\n  mgmt:\n    start: 200\n    end: 249",
		},
	}

	for _, tt := range okCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var nc config.NetworkConfig

			err := yaml.Unmarshal([]byte(tt.yaml), &nc)
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
		})
	}
}
