package netlayout

import (
	"errors"
	"strings"
	"testing"
)

func validSpanningDef() Definition {
	return Definition{
		Name: "t", SchemeVersion: "x-t", Placement: PlacementSpanning,
		MinPrefix: 25, MinSubnets: 3, Source: "test",
		Tiers: map[Tier]TierDef{
			TierMgmt: {
				Statics: map[string]StaticPlacement{
					"bastion": {Offset: 3, Subnets: []int{0}},
					"vault":   {Offset: 5},
				},
				Available: []BandPlacement{{Start: 32, End: 63}},
			},
			TierOCF: {
				Statics: map[string]StaticPlacement{
					"bosh":    {Offset: 64, Subnets: []int{0}},
					"haproxy": {Offset: 97, Subnets: []int{0}},
				},
				Available: []BandPlacement{{Start: 96}},
			},
		},
	}
}

func TestValidateDefinitionAcceptsValid(t *testing.T) {
	if err := validateDefinition(validSpanningDef()); err != nil {
		t.Fatalf("valid def rejected: %v", err)
	}
}

func TestValidateDefinitionRules(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Definition)
		wantErr error
	}{
		{"offset below 3", func(d *Definition) {
			tier := d.Tiers[TierMgmt]
			tier.Statics["bad"] = StaticPlacement{Offset: 2}
			d.Tiers[TierMgmt] = tier
		}, ErrBadPinning},
		{"pin index out of range", func(d *Definition) {
			tier := d.Tiers[TierMgmt]
			tier.Statics["bad"] = StaticPlacement{Offset: 20, Subnets: []int{3}}
			d.Tiers[TierMgmt] = tier
		}, ErrBadPinning},
		{"same offset same index", func(d *Definition) {
			tier := d.Tiers[TierOCF]
			tier.Statics["clash"] = StaticPlacement{Offset: 5} // mgmt vault is 5 on all
			d.Tiers[TierOCF] = tier
		}, ErrOffsetCollision},
		{"static inside band", func(d *Definition) {
			tier := d.Tiers[TierMgmt]
			tier.Statics["bad"] = StaticPlacement{Offset: 40}
			d.Tiers[TierMgmt] = tier
		}, ErrBandOverlap},
		{"haproxy off coupling", func(d *Definition) {
			tier := d.Tiers[TierOCF]
			tier.Statics["haproxy"] = StaticPlacement{Offset: 98, Subnets: []int{0}}
			d.Tiers[TierOCF] = tier
		}, ErrHaproxyCoupling},
		{"band end beyond prefix", func(d *Definition) {
			tier := d.Tiers[TierMgmt]
			tier.Available = []BandPlacement{{Start: 32, End: 130}} // /25 last host is 126
			d.Tiers[TierMgmt] = tier
		}, ErrPrefixTooNarrow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := validSpanningDef()
			tc.mutate(&def)
			err := validateDefinition(def)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidateDefinitionSameOffsetDifferentIndexOK(t *testing.T) {
	def := validSpanningDef()
	tier := def.Tiers[TierMgmt]
	tier.Statics["doomsday"] = StaticPlacement{Offset: 3, Subnets: []int{1}} // bastion is 3 on idx 0 only
	def.Tiers[TierMgmt] = tier
	if err := validateDefinition(def); err != nil {
		t.Fatalf("cross-index reuse rejected: %v", err)
	}
}

// validColocatedDef is a minimal valid colocated definition (MinSubnets
// forced to 1, as LoadDefinition would produce) used to exercise the
// colocated-forbids-pinning rule, which validSpanningDef cannot cover since
// it is a spanning definition.
func validColocatedDef() Definition {
	return Definition{
		Name: "c", SchemeVersion: "x-c", Placement: PlacementColocated,
		MinPrefix: 26, MinSubnets: 1, Source: "test",
		Tiers: map[Tier]TierDef{
			TierMgmt: {
				Statics:   map[string]StaticPlacement{"bastion": {Offset: 3}},
				Available: []BandPlacement{{Start: 11, End: 20}},
			},
			TierOCF: {
				Statics:   map[string]StaticPlacement{"bosh": {Offset: 22}},
				Available: []BandPlacement{{Start: 25}},
			},
		},
	}
}

func TestValidateDefinitionAcceptsValidColocated(t *testing.T) {
	if err := validateDefinition(validColocatedDef()); err != nil {
		t.Fatalf("valid colocated def rejected: %v", err)
	}
}

// TestValidateDefinitionMoreRules covers the remaining rules from the task
// brief that validSpanningDef's single mutation table cannot reach cleanly:
// colocated pinning, band well-formedness, band coverage, and the two
// open-band constraints (at most one across tiers; must be the topmost
// zone). Several of these mutations also happen to trip the cross-tier
// overlap check first — that is expected, since an open band failing to be
// "topmost" is, by construction, also a band that overlaps whatever sits
// above it; both paths wrap ErrBandOverlap.
func TestValidateDefinitionMoreRules(t *testing.T) {
	cases := []struct {
		name           string
		mutate         func(*Definition)
		wantErr        error
		wantMsgContain string // when set, err.Error() must also contain this — pins the case to the intended rule branch
	}{
		{"pinning under colocated", func(d *Definition) {
			tier := d.Tiers[TierMgmt]
			tier.Statics["bad"] = StaticPlacement{Offset: 5, Subnets: []int{0}}
			d.Tiers[TierMgmt] = tier
		}, ErrBadPinning, ""},
		{"pinned static with ip_key", func(d *Definition) {
			tier := d.Tiers[TierMgmt]
			tier.Statics["bad"] = StaticPlacement{Offset: 20, Subnets: []int{0}, IPKey: "bad_ip_custom"}
			d.Tiers[TierMgmt] = tier
		}, ErrBadPinning, "ip_key is only supported on unpinned statics"},
		{"band start not less than end", func(d *Definition) {
			tier := d.Tiers[TierMgmt]
			tier.Available = []BandPlacement{{Start: 63, End: 32}}
			d.Tiers[TierMgmt] = tier
		}, ErrBandOverlap, ""},
		{"band pin index out of range", func(d *Definition) {
			tier := d.Tiers[TierMgmt]
			tier.Available = []BandPlacement{{Start: 32, End: 63, Subnets: []int{5}}} // MinSubnets is 3
			d.Tiers[TierMgmt] = tier
		}, ErrBadPinning, ""},
		{"missing band coverage for an index", func(d *Definition) {
			tier := d.Tiers[TierOCF]
			tier.Available = []BandPlacement{{Start: 96, Subnets: []int{0, 1}}} // idx 2 uncovered
			d.Tiers[TierOCF] = tier
		}, ErrBandOverlap, ""},
		{"duplicate band coverage in one tier", func(d *Definition) {
			tier := d.Tiers[TierMgmt]
			tier.Available = []BandPlacement{{Start: 32, End: 63}, {Start: 70, End: 80}} // both cover every index
			d.Tiers[TierMgmt] = tier
		}, ErrBandOverlap, ""},
		{"static offset beyond prefix", func(d *Definition) {
			tier := d.Tiers[TierMgmt]
			tier.Statics["vault"] = StaticPlacement{Offset: 200} // /25 last host is 126
			d.Tiers[TierMgmt] = tier
		}, ErrPrefixTooNarrow, ""},
		{"tier bands overlap on shared index", func(d *Definition) {
			tier := d.Tiers[TierOCF]
			tier.Available = []BandPlacement{{Start: 40}} // mgmt band is [32,63]
			d.Tiers[TierOCF] = tier
		}, ErrBandOverlap, ""},
		{"two open bands on one index", func(d *Definition) {
			// Start: 200 keeps mgmt's open band from numerically overlapping
			// ocf's open band [96,126] (bandEnd caps at lastHost=126 for
			// MinPrefix 25), so validateCrossTierOverlap does NOT fire —
			// nothing upstream bounds an open band's Start against
			// lastHost. This isolates the openCount > 1 branch in
			// validateOpenBandTopmost as the one and only rule that can
			// reject it.
			tier := d.Tiers[TierMgmt]
			tier.Available = []BandPlacement{{Start: 200}}
			d.Tiers[TierMgmt] = tier
		}, ErrBandOverlap, "open-ended"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := validColocatedDefOrSpanning(tc.name)
			tc.mutate(&def)
			err := validateDefinition(def)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}

			if tc.wantMsgContain != "" && !strings.Contains(err.Error(), tc.wantMsgContain) {
				t.Fatalf("want error containing %q, got %v", tc.wantMsgContain, err)
			}
		})
	}
}

// validColocatedDefOrSpanning picks the base fixture: the colocated-pinning
// case needs a colocated definition to exercise that rule at all, every
// other case builds on the spanning fixture.
func validColocatedDefOrSpanning(name string) Definition {
	if name == "pinning under colocated" {
		return validColocatedDef()
	}

	return validSpanningDef()
}
