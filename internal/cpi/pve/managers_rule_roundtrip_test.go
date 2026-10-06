package pve_test

import (
	"encoding/json"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/pve"
)

// pveReadBack sends the request body ocfp posts for a rule through JSON, as
// PVE would store and report it, and parses the result like a rule listing.
// PVE returns numbers such as enable as JSON numbers, which decode as float64.
func pveReadBack(t *testing.T, rule *cpi.SecurityRule) *cpi.SecurityRule {
	t.Helper()

	raw, err := json.Marshal(pve.BuildPVERuleParams(rule))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var reported map[string]interface{}
	if err := json.Unmarshal(raw, &reported); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	reported["pos"] = float64(4)
	reported["digest"] = "0123456789abcdef"

	return pve.ParsePVERule(reported, 0)
}

// A rule ocfp wrote must still satisfy its desired rule after PVE reports it
// back, or every configure run would add it again.
func TestWrittenRulesMatchAfterReadBack(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "prod", Region: "eu01", Network: config.NetworkConfig{NetworkCIDR: "10.4.0.0/20"}}

	for group, rules := range bootstrap.DefaultSecurityGroupRules(cfg) {
		for _, rule := range rules {
			readBack := pveReadBack(t, rule)

			if readBack.Attributes["action"] != "ACCEPT" || readBack.Attributes["enable"] != "1" {
				t.Errorf("group %s rule %q: read-back attributes = %v", group, rule.Description, readBack.Attributes)
			}

			if !bootstrap.RulesMatch(readBack, rule) {
				t.Errorf("group %s rule %q does not match its read-back %+v", group, rule.Description, readBack)
			}
		}
	}
}

func TestWrittenRulesMatchAfterReadBackBothDirections(t *testing.T) {
	t.Parallel()

	for _, direction := range []string{"ingress", "egress"} {
		for _, source := range []string{"", "0.0.0.0/0", "10.4.0.0/20"} {
			rule := &cpi.SecurityRule{Direction: direction, Protocol: "tcp", PortRangeMin: 22, PortRangeMax: 22, RemoteIPCIDR: source}
			if readBack := pveReadBack(t, rule); !bootstrap.RulesMatch(readBack, rule) {
				t.Errorf("%s from %q: no match after read-back %+v", direction, source, readBack)
			}
		}
	}
}
