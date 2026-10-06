package bootstrap_test

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

func TestRulesMatchPVEReadBack(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current *cpi.SecurityRule
		desired *cpi.SecurityRule
		want    bool
	}{
		{
			name:    "pve in matches ingress",
			current: &cpi.SecurityRule{Direction: "in", Protocol: "tcp", PortRangeMin: 22, PortRangeMax: 22, RemoteIPCIDR: "10.4.0.0/20"},
			desired: &cpi.SecurityRule{Direction: "ingress", Protocol: "tcp", PortRangeMin: 22, PortRangeMax: 22, RemoteIPCIDR: "10.4.0.0/20"},
			want:    true,
		},
		{
			name:    "pve out with no source matches egress all 0.0.0.0/0",
			current: &cpi.SecurityRule{Direction: "out"},
			desired: &cpi.SecurityRule{Direction: "egress", Protocol: "all", RemoteIPCIDR: "0.0.0.0/0"},
			want:    true,
		},
		{
			name:    "direction case is ignored",
			current: &cpi.SecurityRule{Direction: "IN", Protocol: "TCP", PortRangeMin: 443, PortRangeMax: 443},
			desired: &cpi.SecurityRule{Direction: "Ingress", Protocol: "tcp", PortRangeMin: 443},
			want:    true,
		},
		{
			name:    "bare IP matches host CIDR",
			current: &cpi.SecurityRule{Direction: "in", Protocol: "tcp", PortRangeMin: 22, PortRangeMax: 22, RemoteIPCIDR: "10.0.0.5"},
			desired: &cpi.SecurityRule{Direction: "ingress", Protocol: "tcp", PortRangeMin: 22, PortRangeMax: 22, RemoteIPCIDR: "10.0.0.5/32"},
			want:    true,
		},
		{
			name:    "opposite directions differ",
			current: &cpi.SecurityRule{Direction: "out", Protocol: "tcp", PortRangeMin: 22, PortRangeMax: 22},
			desired: &cpi.SecurityRule{Direction: "ingress", Protocol: "tcp", PortRangeMin: 22, PortRangeMax: 22},
			want:    false,
		},
		{
			name:    "different port differs",
			current: &cpi.SecurityRule{Direction: "in", Protocol: "tcp", PortRangeMin: 80, PortRangeMax: 80},
			desired: &cpi.SecurityRule{Direction: "ingress", Protocol: "tcp", PortRangeMin: 443, PortRangeMax: 443},
			want:    false,
		},
		{
			name:    "different source differs",
			current: &cpi.SecurityRule{Direction: "in", Protocol: "tcp", PortRangeMin: 22, PortRangeMax: 22, RemoteIPCIDR: "10.0.0.0/8"},
			desired: &cpi.SecurityRule{Direction: "ingress", Protocol: "tcp", PortRangeMin: 22, PortRangeMax: 22, RemoteIPCIDR: "192.168.0.0/16"},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := bootstrap.RulesMatch(tt.current, tt.desired); got != tt.want {
				t.Errorf("RulesMatch = %v, want %v", got, tt.want)
			}

			if got := bootstrap.RulesMatch(tt.desired, tt.current); got != tt.want {
				t.Errorf("RulesMatch (swapped) = %v, want %v", got, tt.want)
			}
		})
	}
}

// Every default rule, once read back the way PVE reports it, must match
// itself so that reconcile adds nothing.
func TestDefaultRulesMatchTheirPVEReadBack(t *testing.T) {
	t.Parallel()

	for group, rules := range bootstrap.DefaultSecurityGroupRules(&config.Config{Name: "prod", Region: "eu01", Network: config.NetworkConfig{NetworkCIDR: "10.4.0.0/20"}}) {
		for _, rule := range rules {
			readBack := *rule
			if rule.Direction == "egress" {
				readBack.Direction = "out"
			} else {
				readBack.Direction = "in"
			}

			if rule.RemoteIPCIDR == "0.0.0.0/0" {
				readBack.RemoteIPCIDR = ""
			}

			if !bootstrap.RulesMatch(&readBack, rule) {
				t.Errorf("group %s rule %q does not match its PVE read-back", group, rule.Description)
			}
		}
	}
}

// A current rule satisfies a desired rule only when it is a plain ACCEPT rule.
// Rules that drop, are disabled, or narrow the match by destination, source
// port, macro, or interface must not hide a missing allow rule.
func TestRulesMatchRequiresPlainAcceptRule(t *testing.T) {
	t.Parallel()

	ssh := &cpi.SecurityRule{Direction: "ingress", Protocol: "tcp", PortRangeMin: 22, PortRangeMax: 22, RemoteIPCIDR: "10.4.0.0/20"}
	allowAll := &cpi.SecurityRule{Direction: "egress", Protocol: "all", RemoteIPCIDR: "0.0.0.0/0", Description: "Allow all outbound"}

	sshWith := func(attrs map[string]string) *cpi.SecurityRule {
		return &cpi.SecurityRule{Direction: "in", Protocol: "tcp", PortRangeMin: 22, PortRangeMax: 22, RemoteIPCIDR: "10.4.0.0/20", Attributes: attrs}
	}

	tests := []struct {
		name    string
		current *cpi.SecurityRule
		desired *cpi.SecurityRule
		want    bool
	}{
		{"no attributes", sshWith(nil), ssh, true},
		{"accept and enabled", sshWith(map[string]string{"action": "ACCEPT", "enable": "1"}), ssh, true},
		{"lower-case accept", sshWith(map[string]string{"action": "accept"}), ssh, true},
		{"neutral attributes", sshWith(map[string]string{"action": "ACCEPT", "enable": "1", "log": "nolog", "ipversion": "4", "comment": "x", "digest": "abc"}), ssh, true},
		{"numeric dport is not an attribute", sshWith(map[string]string{"dport": "22"}), ssh, true},
		{"drop", sshWith(map[string]string{"action": "DROP", "enable": "1"}), ssh, false},
		{"reject", sshWith(map[string]string{"action": "REJECT"}), ssh, false},
		{"disabled", sshWith(map[string]string{"action": "ACCEPT", "enable": "0"}), ssh, false},
		{"destination scoped", sshWith(map[string]string{"action": "ACCEPT", "dest": "10.0.0.5"}), ssh, false},
		{"source port scoped", sshWith(map[string]string{"sport": "1024"}), ssh, false},
		{"interface scoped", sshWith(map[string]string{"iface": "net0"}), ssh, false},
		{"port list", sshWith(map[string]string{"dport": "22,2222"}), ssh, false},
		{"macro on a port rule", sshWith(map[string]string{"macro": "SSH"}), ssh, false},
		{
			name:    "macro-only egress does not satisfy allow-all",
			current: &cpi.SecurityRule{Direction: "out", Protocol: "all", Attributes: map[string]string{"action": "ACCEPT", "enable": "1", "macro": "DNS"}},
			desired: allowAll,
			want:    false,
		},
		{
			name:    "plain egress satisfies allow-all",
			current: &cpi.SecurityRule{Direction: "out", Protocol: "all", Attributes: map[string]string{"action": "ACCEPT", "enable": "1"}},
			desired: allowAll,
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := bootstrap.RulesMatch(tt.current, tt.desired); got != tt.want {
				t.Errorf("RulesMatch = %v, want %v", got, tt.want)
			}
		})
	}
}
