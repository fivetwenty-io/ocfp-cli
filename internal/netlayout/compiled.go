package netlayout

import (
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/reservedip"
)

// tierEnvType maps a Tier to the engine's envType key used as the second
// level of a reservedip.AssignmentTable entry (the "mgmt"/"ocf" keys
// wideLayout's hand-written table already uses).
var tierEnvType = map[Tier]string{
	TierMgmt: "mgmt",
	TierOCF:  "ocf",
}

// compiledLayout is a validated Definition plus its precomputed Layer B
// assignment table. Later tasks add more methods (Slots, ValidateSubnet,
// ValidateBand, ...).
type compiledLayout struct {
	def   Definition
	table reservedip.AssignmentTable
}

// Compile validates def and builds its workload table once. The returned
// *compiledLayout answers WorkloadTable from that precomputed table rather
// than rebuilding it per call.
func Compile(def Definition) (*compiledLayout, error) {
	if err := validateDefinition(def); err != nil {
		return nil, err
	}

	return &compiledLayout{def: def, table: buildWorkloadTable(def)}, nil
}

// WorkloadTable returns the compiled Layer B assignment table. cidr is
// accepted for Layout interface conformance but unused: the table is fixed
// at Compile time and does not vary by subnet size, matching wideLayout's
// documented contract.
func (c *compiledLayout) WorkloadTable(_ string) (reservedip.AssignmentTable, error) {
	return c.table, nil
}

// buildWorkloadTable compiles def's statics and bands into a Layer B
// assignment table: one entry per named role from buildStatics, plus the
// "available" and derived "reserved" pseudo-roles per tier.
func buildWorkloadTable(def Definition) reservedip.AssignmentTable {
	table := reservedip.AssignmentTable{}
	buildStatics(def, table)

	table["available"] = map[string]*reservedip.Assignment{}
	table["reserved"] = map[string]*reservedip.Assignment{}

	for _, t := range tierOrder {
		tier, ok := def.Tiers[t]
		if !ok {
			continue
		}

		envType := tierEnvType[t]
		table["available"][envType] = buildAvailable(tier.Available)
		table["reserved"][envType] = buildReserved(tier.Available)
	}

	return table
}

// buildStatics compiles every tier's named statics into table in place. An
// unpinned static (Subnets == nil) becomes an Offset assignment (carrying
// IPKey when set); a pinned static becomes a SubnetMapping assignment —
// validateDefinition already rejects a pinned static with IPKey set, since
// the engine's SubnetMapping path never reads Assignment.IPKey.
func buildStatics(def Definition, table reservedip.AssignmentTable) {
	for _, t := range tierOrder {
		tier, ok := def.Tiers[t]
		if !ok {
			continue
		}

		envType := tierEnvType[t]

		for role, s := range tier.Statics {
			if table[role] == nil {
				table[role] = map[string]*reservedip.Assignment{}
			}

			if s.Subnets == nil {
				table[role][envType] = &reservedip.Assignment{Offset: s.Offset, IPKey: s.IPKey}
			} else {
				table[role][envType] = &reservedip.Assignment{SubnetMapping: map[int][]int{s.Offset: s.Subnets}}
			}
		}
	}
}

// buildAvailable compiles one tier's available bands into an Assignment: a
// single unpinned band (Subnets == nil) becomes a RangeSpec; anything else
// (multiple bands, or a pinned single band) becomes SubnetRanges, one entry
// per band keyed by its range spec.
func buildAvailable(bands []BandPlacement) *reservedip.Assignment {
	if len(bands) == 1 && bands[0].Subnets == nil {
		return &reservedip.Assignment{RangeSpec: availableRangeSpec(bands[0])}
	}

	ranges := make(map[string][]int, len(bands))
	for _, b := range bands {
		ranges[availableRangeSpec(b)] = b.Subnets
	}

	return &reservedip.Assignment{SubnetRanges: ranges}
}

// buildReserved compiles one tier's available bands into their DERIVED
// reserved complement, one entry per band, using the same
// RangeSpec-vs-SubnetRanges shape decision as buildAvailable.
func buildReserved(bands []BandPlacement) *reservedip.Assignment {
	if len(bands) == 1 && bands[0].Subnets == nil {
		return &reservedip.Assignment{RangeSpec: reservedRangeSpec(bands[0])}
	}

	ranges := make(map[string][]int, len(bands))
	for _, b := range bands {
		ranges[reservedRangeSpec(b)] = b.Subnets
	}

	return &reservedip.Assignment{SubnetRanges: ranges}
}

// availableRangeSpec returns b's own range spec: "start-end" when closed,
// "start->" when open (End == 0).
func availableRangeSpec(b BandPlacement) string {
	if b.End > 0 {
		return fmt.Sprintf("%d-%d", b.Start, b.End)
	}

	return fmt.Sprintf("%d->", b.Start)
}

// reservedRangeSpec returns b's derived reserved complement: a closed band
// [s,e] reserves everything below and above it ("0-(s-1),(e+1)->"); an open
// band [s,) reserves everything below it ("0-(s-1)").
func reservedRangeSpec(b BandPlacement) string {
	if b.End > 0 {
		return fmt.Sprintf("0-%d,%d->", b.Start-1, b.End+1)
	}

	return fmt.Sprintf("0-%d", b.Start-1)
}
