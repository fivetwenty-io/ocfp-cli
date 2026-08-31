package commands

import (
	"reflect"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// TestAddPVEProviderConfig_TemplateSeedFields covers the template_seed_*
// forwarding addPVEProviderConfig adds after the existing template_bridge
// block: all four provider-config keys are forwarded (with the DNS fallback
// resolved from Network.DNSServers) when TemplateSeedIP is set, none of
// them are forwarded when it is empty (the zero-value / DHCP case, so every
// bloc without these fields configured is unaffected), and — matching the
// guard every other key in this function already applies to itself — an
// unset optional field (gateway, dns, searchdomain) is simply absent from
// the map rather than forwarded as an empty string.
func TestAddPVEProviderConfig_TemplateSeedFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *config.Config
		wantKey map[string]interface{}
		absent  []string
	}{
		{
			name: "static fields present forward all four keys with explicit dns",
			cfg: &config.Config{
				TemplateBridge:           "vlan54",
				TemplateSeedIP:           "10.61.148.2/24",
				TemplateSeedGateway:      "10.61.148.1",
				TemplateSeedDNS:          []string{"10.97.160.160", "10.97.160.161"},
				TemplateSeedSearchDomain: "ldschurch.org",
			},
			wantKey: map[string]interface{}{
				"template_seed_ip":           "10.61.148.2/24",
				"template_seed_gateway":      "10.61.148.1",
				"template_seed_dns":          []string{"10.97.160.160", "10.97.160.161"},
				"template_seed_searchdomain": "ldschurch.org",
			},
		},
		{
			name: "static fields present, dns fallback resolved from Network.DNSServers, no searchdomain configured",
			cfg: &config.Config{
				TemplateBridge:      "vlan54",
				TemplateSeedIP:      "10.61.148.2/24",
				TemplateSeedGateway: "10.61.148.1",
				Network: config.NetworkConfig{
					DNSServers: []string{"10.97.160.160"},
				},
			},
			wantKey: map[string]interface{}{
				"template_seed_ip":      "10.61.148.2/24",
				"template_seed_gateway": "10.61.148.1",
				"template_seed_dns":     []string{"10.97.160.160"},
			},
			absent: []string{"template_seed_searchdomain"},
		},
		{
			name: "empty TemplateSeedIP forwards none of the four keys",
			cfg: &config.Config{
				TemplateBridge:      "vlan54",
				TemplateSeedGateway: "10.61.148.1", // ignored: gateway alone never forwarded
			},
			absent: []string{
				"template_seed_ip",
				"template_seed_gateway",
				"template_seed_dns",
				"template_seed_searchdomain",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			providerConfig := map[string]interface{}{}
			addPVEProviderConfig(providerConfig, tt.cfg)

			for k, want := range tt.wantKey {
				got, ok := providerConfig[k]
				if !ok {
					t.Errorf("missing key %q in %v", k, providerConfig)

					continue
				}

				if !reflect.DeepEqual(got, want) {
					t.Errorf("key %q = %v, want %v", k, got, want)
				}
			}

			for _, k := range tt.absent {
				if _, present := providerConfig[k]; present {
					t.Errorf("key %q must be absent, not forwarded as an empty string; got %v", k, providerConfig[k])
				}
			}
		})
	}
}

// TestResolveTemplateSeedDNS covers the three-tier fallback: explicit
// TemplateSeedDNS wins, Network.DNSServers is the fallback when unset, and
// nil is returned when neither is set (the pve provider then applies
// defaultPVECloudInitDNS).
func TestResolveTemplateSeedDNS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *config.Config
		want []string
	}{
		{
			name: "explicit TemplateSeedDNS wins",
			cfg: &config.Config{
				TemplateSeedDNS: []string{"10.97.160.160", "10.97.160.161"},
				Network: config.NetworkConfig{
					DNSServers: []string{"1.1.1.1"},
				},
			},
			want: []string{"10.97.160.160", "10.97.160.161"},
		},
		{
			name: "falls back to Network.DNSServers when TemplateSeedDNS empty",
			cfg: &config.Config{
				Network: config.NetworkConfig{
					DNSServers: []string{"10.97.160.160"},
				},
			},
			want: []string{"10.97.160.160"},
		},
		{
			name: "nil when neither is set",
			cfg:  &config.Config{},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := resolveTemplateSeedDNS(tt.cfg)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveTemplateSeedDNS() = %v, want %v", got, tt.want)
			}
		})
	}
}
