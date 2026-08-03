package netlayout

import (
	"reflect"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/reservedip"
)

// wideGoldenTable is the Layer B assignment table the hand-written
// wideLayout emitted before the compiled built-ins replaced it, transcribed
// verbatim from its WorkloadTable (wide.go, deleted in this commit; see
// c72be26:internal/netlayout/wide.go for the original). It is the byte-compat
// contract for every bloc already deployed under scheme "2": the vault-layer
// reserved-ips a redeploy computes must not move, so a change here is a
// change to live infrastructure, never a test fixup.
func wideGoldenTable() reservedip.AssignmentTable {
	return reservedip.AssignmentTable{
		"bastion":      {"mgmt": {Offset: 3}},
		"bosh":         {"mgmt": {Offset: 4}, "ocf": {Offset: 64}},
		"vault":        {"mgmt": {Offset: 5}, "ocf": {Offset: 65}},
		"jumpbox":      {"mgmt": {Offset: 6}, "ocf": {Offset: 66}},
		"concourse":    {"mgmt": {Offset: 7}},
		"prometheus":   {"mgmt": {Offset: 8}},
		"shield":       {"mgmt": {Offset: 9}},
		"blacksmith":   {"mgmt": {Offset: 10}, "ocf": {Offset: 67}},
		"artifacts":    {"mgmt": {Offset: 11}},
		"wireguard":    {"mgmt": {Offset: 12}},
		"ovpn":         {"mgmt": {Offset: 13}},
		"rustfs":       {"mgmt": {Offset: 14}},
		"rustfs_smoke": {"mgmt": {Offset: 21, IPKey: "rustfs_ip_smoke"}},
		"proxycache":   {"mgmt": {Offset: 15}},
		"nfs":          {"mgmt": {Offset: 16}},
		"ocfp_ui":      {"mgmt": {Offset: 17}},
		"doomsday":     {"mgmt": {Offset: 18}},
		"shout":        {"mgmt": {Offset: 19}},
		"garage":       {"mgmt": {Offset: 20}},
		"garage_smoke": {"mgmt": {Offset: 22, IPKey: "garage_ip_smoke"}},
		"haproxy":      {"ocf": {Offset: 97}},
		"available": {
			"mgmt": {RangeSpec: "32-63"},
			"ocf":  {RangeSpec: "96->"},
		},
		"reserved": {
			"mgmt": {RangeSpec: "0-31,64->"},
			"ocf":  {RangeSpec: "0-95"},
		},
	}
}

// compactGoldenTable is the same byte-compat contract for scheme
// "3-compact", transcribed verbatim from the deleted compactLayout's
// WorkloadTable: wide's mgmt tier unchanged, ocf's four cross-tier statics
// compressed from 64-67 down to 23-26, and both available bands shrunk to
// fit a /26.
func compactGoldenTable() reservedip.AssignmentTable {
	return reservedip.AssignmentTable{
		"bastion":      {"mgmt": {Offset: 3}},
		"bosh":         {"mgmt": {Offset: 4}, "ocf": {Offset: 23}},
		"vault":        {"mgmt": {Offset: 5}, "ocf": {Offset: 24}},
		"jumpbox":      {"mgmt": {Offset: 6}, "ocf": {Offset: 25}},
		"concourse":    {"mgmt": {Offset: 7}},
		"prometheus":   {"mgmt": {Offset: 8}},
		"shield":       {"mgmt": {Offset: 9}},
		"blacksmith":   {"mgmt": {Offset: 10}, "ocf": {Offset: 26}},
		"artifacts":    {"mgmt": {Offset: 11}},
		"wireguard":    {"mgmt": {Offset: 12}},
		"ovpn":         {"mgmt": {Offset: 13}},
		"rustfs":       {"mgmt": {Offset: 14}},
		"rustfs_smoke": {"mgmt": {Offset: 21, IPKey: "rustfs_ip_smoke"}},
		"proxycache":   {"mgmt": {Offset: 15}},
		"nfs":          {"mgmt": {Offset: 16}},
		"ocfp_ui":      {"mgmt": {Offset: 17}},
		"doomsday":     {"mgmt": {Offset: 18}},
		"shout":        {"mgmt": {Offset: 19}},
		"garage":       {"mgmt": {Offset: 20}},
		"garage_smoke": {"mgmt": {Offset: 22, IPKey: "garage_ip_smoke"}},
		"haproxy":      {"ocf": {Offset: 37}},
		"available": {
			"mgmt": {RangeSpec: "28-35"},
			"ocf":  {RangeSpec: "36->"},
		},
		"reserved": {
			"mgmt": {RangeSpec: "0-27,36->"},
			"ocf":  {RangeSpec: "0-35"},
		},
	}
}

// TestBuiltinTablesMatchGolden proves the embedded YAML definitions compile
// to exactly the tables the deleted hand-written wideLayout/compactLayout
// emitted, and carry their scheme identities unchanged. This is Layer B's
// byte-compat proof: it is what lets the strategy definitions move from Go
// code to YAML without any deployed bloc's reserved-ips shifting.
func TestBuiltinTablesMatchGolden(t *testing.T) {
	cases := []struct {
		name          string
		want          reservedip.AssignmentTable
		schemeVersion string
		minPrefix     int
	}{
		{name: "wide", want: wideGoldenTable(), schemeVersion: "2", minPrefix: 25},
		{name: "compact", want: compactGoldenTable(), schemeVersion: "3-compact", minPrefix: 26},
	}

	layouts := builtinLayouts()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			compiled, ok := layouts[tc.name]
			if !ok {
				t.Fatalf("builtinLayouts() has no %q strategy", tc.name)
			}

			got, err := compiled.WorkloadTable("10.0.0.0/22")
			if err != nil {
				t.Fatalf("WorkloadTable: %v", err)
			}

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s: compiled table diverges from golden\ngot:  %#v\nwant: %#v", tc.name, got, tc.want)
			}

			if got := compiled.SchemeVersion(); got != tc.schemeVersion {
				t.Errorf("%s: SchemeVersion() = %q, want %q", tc.name, got, tc.schemeVersion)
			}

			if got := compiled.MinPrefix(); got != tc.minPrefix {
				t.Errorf("%s: MinPrefix() = %d, want %d", tc.name, got, tc.minPrefix)
			}
		})
	}
}
