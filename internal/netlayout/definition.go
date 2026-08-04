package netlayout

import (
	"errors"
	"fmt"

	"github.com/goccy/go-yaml"
)

// Placement identifies how a strategy distributes roles across a bloc's
// workload subnets: colocated replicates the full table on every subnet;
// spanning pins roles to specific subnet indices.
type Placement string

const (
	// PlacementColocated replicates the full table per subnet.
	PlacementColocated Placement = "colocated"
	// PlacementSpanning distributes roles across the subnet set.
	PlacementSpanning Placement = "spanning"
)

// ErrInvalidDefinition is the sentinel every structural load failure wraps.
// Callers match with errors.Is; the wrapped message names the source file,
// the strategy (when known), and the failed rule.
var ErrInvalidDefinition = errors.New("netlayout: invalid strategy definition")

// StaticPlacement places one named role at a fixed offset. Subnets nil
// means "every subnet index"; IPKey overrides the default "{role}_ip"
// output key (see reservedip.Assignment.IPKey).
type StaticPlacement struct {
	Offset  int
	Subnets []int
	IPKey   string
}

// UnmarshalYAML accepts either a bare integer offset or the mapping form
// {offset, subnets, ip_key}.
func (s *StaticPlacement) UnmarshalYAML(data []byte) error {
	var offset int
	if err := yaml.Unmarshal(data, &offset); err == nil {
		*s = StaticPlacement{Offset: offset}

		return nil
	}

	var raw struct {
		Offset  int    `yaml:"offset"`
		Subnets []int  `yaml:"subnets"`
		IPKey   string `yaml:"ip_key"`
	}

	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("static placement: %w", err)
	}

	*s = StaticPlacement{Offset: raw.Offset, Subnets: raw.Subnets, IPKey: raw.IPKey}

	return nil
}

// BandPlacement is one available-band entry. End 0 means open-ended (to
// the subnet's last usable host); Subnets nil means every subnet index.
type BandPlacement struct {
	Start   int   `yaml:"start"`
	End     int   `yaml:"end"`
	Subnets []int `yaml:"subnets"`
}

// bandList accepts either a single band mapping or a list of them.
type bandList []BandPlacement

func (b *bandList) UnmarshalYAML(data []byte) error {
	var one BandPlacement
	// Unmarshaling a mapping into a single BandPlacement succeeds (err==nil);
	// unmarshaling a list into a single struct fails ("sequence was used where
	// mapping is expected"). Thus err==nil alone distinguishes single vs. list.
	if err := yaml.Unmarshal(data, &one); err == nil {
		*b = bandList{one}

		return nil
	}

	var many []BandPlacement
	if err := yaml.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("available band: %w", err)
	}

	*b = bandList(many)

	return nil
}

// TierDef is one tier's placements within a Definition.
type TierDef struct {
	Statics   map[string]StaticPlacement
	Available []BandPlacement
}

// Definition is the declarative form of one reserved-IP strategy — the
// single representation shared by built-ins (embedded YAML) and operator
// BYO files. Reserved complements are never part of a Definition; the
// compiler derives them (see buildWorkloadTable).
type Definition struct {
	Name          string
	Description   string
	SchemeVersion string
	Placement     Placement
	MinPrefix     int
	MinSubnets    int
	Tiers         map[Tier]TierDef
	// Source records where the definition came from (file path or
	// "built-in") for error messages.
	Source string
}

// invalidDef wraps ErrInvalidDefinition with source and rule context.
func invalidDef(source, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalidDefinition, source, fmt.Sprintf(format, args...))
}

// LoadDefinition parses one YAML strategy definition and applies the
// structural rules (fields present, placement recognized, min_subnets
// consistent with placement). Semantic rules (collisions, band overlaps,
// min_prefix fit) belong to Compile.
func LoadDefinition(data []byte, source string) (Definition, error) {
	var raw struct {
		Name          string `yaml:"name"`
		Description   string `yaml:"description"`
		SchemeVersion string `yaml:"scheme_version"`
		Placement     string `yaml:"placement"`
		MinPrefix     int    `yaml:"min_prefix"`
		MinSubnets    int    `yaml:"min_subnets"`
		Tiers         struct {
			Mgmt struct {
				Statics   map[string]StaticPlacement `yaml:"statics"`
				Available bandList                   `yaml:"available"`
			} `yaml:"mgmt"`
			OCF struct {
				Statics   map[string]StaticPlacement `yaml:"statics"`
				Available bandList                   `yaml:"available"`
			} `yaml:"ocf"`
		} `yaml:"tiers"`
	}

	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Definition{}, invalidDef(source, "parse: %v", err)
	}

	if raw.Name == "" {
		return Definition{}, invalidDef(source, "name is required")
	}

	if raw.SchemeVersion == "" {
		return Definition{}, invalidDef(source, "strategy %q: scheme_version is required", raw.Name)
	}

	if raw.MinPrefix == 0 {
		return Definition{}, invalidDef(source, "strategy %q: min_prefix is required", raw.Name)
	}

	placement := Placement(raw.Placement)

	switch placement {
	case PlacementColocated:
		if raw.MinSubnets != 0 {
			return Definition{}, invalidDef(source, "strategy %q: min_subnets is forbidden for colocated placement", raw.Name)
		}

		raw.MinSubnets = 1
	case PlacementSpanning:
		if raw.MinSubnets < 2 { //nolint:mnd // spanning needs at least two subnets to span
			return Definition{}, invalidDef(source, "strategy %q: spanning placement requires min_subnets >= 2", raw.Name)
		}
	default:
		return Definition{}, invalidDef(source, "strategy %q: placement must be %q or %q, got %q",
			raw.Name, PlacementColocated, PlacementSpanning, raw.Placement)
	}

	tiers := map[Tier]TierDef{}
	if len(raw.Tiers.Mgmt.Statics) > 0 || len(raw.Tiers.Mgmt.Available) > 0 {
		tiers[TierMgmt] = TierDef{Statics: raw.Tiers.Mgmt.Statics, Available: raw.Tiers.Mgmt.Available}
	}

	if len(raw.Tiers.OCF.Statics) > 0 || len(raw.Tiers.OCF.Available) > 0 {
		tiers[TierOCF] = TierDef{Statics: raw.Tiers.OCF.Statics, Available: raw.Tiers.OCF.Available}
	}

	if len(tiers) == 0 {
		return Definition{}, invalidDef(source, "strategy %q: at least one tier is required", raw.Name)
	}

	return Definition{
		Name:          raw.Name,
		Description:   raw.Description,
		SchemeVersion: raw.SchemeVersion,
		Placement:     placement,
		MinPrefix:     raw.MinPrefix,
		MinSubnets:    raw.MinSubnets,
		Tiers:         tiers,
		Source:        source,
	}, nil
}
