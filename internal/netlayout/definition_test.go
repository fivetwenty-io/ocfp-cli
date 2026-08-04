package netlayout

import (
	"errors"
	"testing"
)

const miniYAML = `
name: mini
scheme_version: "x-mini"
placement: colocated
min_prefix: 26
tiers:
  mgmt:
    statics:
      bastion: 3
      rustfs_smoke: { offset: 21, ip_key: rustfs_ip_smoke }
    available: { start: 28, end: 35 }
  ocf:
    statics:
      bosh: { offset: 23, subnets: [0] }
      haproxy: { offset: 37, subnets: [0] }
    available: { start: 36 }
`

func TestLoadDefinitionParsesAllForms(t *testing.T) {
	def, err := LoadDefinition([]byte(miniYAML), "test.yml")
	if err != nil {
		t.Fatalf("LoadDefinition: %v", err)
	}

	if def.Name != "mini" || def.SchemeVersion != "x-mini" {
		t.Fatalf("identity: %+v", def)
	}

	if def.Placement != PlacementColocated || def.MinSubnets != 1 {
		t.Fatalf("placement: %q min_subnets=%d", def.Placement, def.MinSubnets)
	}

	mgmt := def.Tiers[TierMgmt]
	if got := mgmt.Statics["bastion"]; got.Offset != 3 || got.Subnets != nil {
		t.Fatalf("bare-int static: %+v", got)
	}

	if got := mgmt.Statics["rustfs_smoke"]; got.IPKey != "rustfs_ip_smoke" {
		t.Fatalf("ip_key: %+v", got)
	}

	ocf := def.Tiers[TierOCF]
	if got := ocf.Statics["bosh"]; got.Offset != 23 || len(got.Subnets) != 1 || got.Subnets[0] != 0 {
		t.Fatalf("pinned static: %+v", got)
	}

	if len(mgmt.Available) != 1 || mgmt.Available[0].Start != 28 || mgmt.Available[0].End != 35 {
		t.Fatalf("closed band: %+v", mgmt.Available)
	}

	if ocf.Available[0].End != 0 {
		t.Fatalf("open band End must be 0: %+v", ocf.Available)
	}
}

func TestLoadDefinitionBandListForm(t *testing.T) {
	y := `
name: bands
scheme_version: "x-bands"
placement: spanning
min_prefix: 26
min_subnets: 3
tiers:
  mgmt:
    statics: { bastion: { offset: 3, subnets: [0] } }
    available: { start: 11, end: 29 }
  ocf:
    statics: { bosh: { offset: 31, subnets: [0] } }
    available:
      - { subnets: [0], start: 38 }
      - { subnets: [1, 2], start: 37 }
`
	def, err := LoadDefinition([]byte(y), "bands.yml")
	if err != nil {
		t.Fatalf("LoadDefinition: %v", err)
	}

	ocf := def.Tiers[TierOCF]
	if len(ocf.Available) != 2 || ocf.Available[0].Subnets[0] != 0 || ocf.Available[1].Start != 37 {
		t.Fatalf("per-index bands: %+v", ocf.Available)
	}
}

func TestLoadDefinitionStructuralErrors(t *testing.T) {
	cases := []struct{ name, yaml string }{
		{"missing name", `{scheme_version: "x", placement: colocated, min_prefix: 26, tiers: {mgmt: {statics: {a: 3}, available: {start: 11}}}}`},
		{"missing scheme", `{name: a, placement: colocated, min_prefix: 26, tiers: {mgmt: {statics: {a: 3}, available: {start: 11}}}}`},
		{"bad placement", `{name: a, scheme_version: "x", placement: sideways, min_prefix: 26, tiers: {mgmt: {statics: {a: 3}, available: {start: 11}}}}`},
		{"min_subnets on colocated", `{name: a, scheme_version: "x", placement: colocated, min_subnets: 3, min_prefix: 26, tiers: {mgmt: {statics: {a: 3}, available: {start: 11}}}}`},
		{"spanning without min_subnets", `{name: a, scheme_version: "x", placement: spanning, min_prefix: 26, tiers: {mgmt: {statics: {a: 3}, available: {start: 11}}}}`},
		{"missing min_prefix", `{name: a, scheme_version: "x", placement: colocated, tiers: {mgmt: {statics: {a: 3}, available: {start: 11}}}}`},
	}
	for _, tc := range cases {
		if _, err := LoadDefinition([]byte(tc.yaml), "bad.yml"); err == nil {
			t.Errorf("%s: expected error", tc.name)
		} else if !errors.Is(err, ErrInvalidDefinition) {
			t.Errorf("%s: want ErrInvalidDefinition, got %v", tc.name, err)
		}
	}
}

func TestLoadDefinitionBandWithZeroStart(t *testing.T) {
	y := `
name: zero-start
scheme_version: "x-zero-start"
placement: colocated
min_prefix: 26
tiers:
  mgmt:
    statics: { a: 1 }
    available: { start: 0, end: 20 }
`
	def, err := LoadDefinition([]byte(y), "zero-start.yml")
	if err != nil {
		t.Fatalf("LoadDefinition: %v", err)
	}

	mgmt := def.Tiers[TierMgmt]
	if len(mgmt.Available) != 1 || mgmt.Available[0].Start != 0 || mgmt.Available[0].End != 20 {
		t.Fatalf("band with start=0: %+v", mgmt.Available)
	}
}
