package netlayout

import (
	"fmt"
	"sort"

	"github.com/ocfp/ocfp-cli-go/internal/reservedip"
)

// slotRoleInfra and slotRoleOCFP are the two role values LayerASlots
// accepts — any other role is rejected with unknownRoleError.
const (
	slotRoleInfra = "infra"
	slotRoleOCFP  = "ocfp"
)

// The infra role's named-slot offsets and available band, carried over
// unchanged from internal/bootstrap.defaultReservedIPLayout. They are fixed
// for every strategy: Layer A's infra subnet is carved once per bloc and is
// not a workload subnet whose layout varies by strategy, so a strategy's own
// mgmt-tier offsets never apply here.
const (
	infraBastionOffset    = 3
	infraBoshOffset       = 4
	infraShieldOffset     = 9
	infraBlacksmithOffset = 10
	infraAvailableStart   = 12
	infraAvailableEnd     = 29
	infraReservedBOffset  = 10
	infraReservedCOffset  = 30
)

// tierEnvType maps a Tier to the engine's envType key used as the second
// level of a reservedip.AssignmentTable entry (the historical "mgmt"/"ocf"
// keys every strategy's Layer B table is written in terms of).
var tierEnvType = map[Tier]string{
	TierMgmt: "mgmt",
	TierOCF:  "ocf",
}

// compiledLayout is a validated Definition plus its precomputed Layer B
// assignment table and highest static offset.
type compiledLayout struct {
	def           Definition
	table         reservedip.AssignmentTable
	highestOffset int
}

// Compile validates def and builds its workload table once. The returned
// *compiledLayout answers WorkloadTable from that precomputed table rather
// than rebuilding it per call.
func Compile(def Definition) (*compiledLayout, error) {
	if err := validateDefinition(def); err != nil {
		return nil, err
	}

	return &compiledLayout{
		def:           def,
		table:         buildWorkloadTable(def),
		highestOffset: highestStaticOffset(def),
	}, nil
}

// highestStaticOffset returns the highest static offset across both tiers
// of def, used by ValidateSubnet to name the offset a too-small cidr fails
// to fit.
func highestStaticOffset(def Definition) int {
	highest := 0

	for _, t := range tierOrder {
		tier, ok := def.Tiers[t]
		if !ok {
			continue
		}

		for _, s := range tier.Statics {
			if s.Offset > highest {
				highest = s.Offset
			}
		}
	}

	return highest
}

// Name returns the compiled definition's strategy name.
func (c *compiledLayout) Name() string { return c.def.Name }

// SchemeVersion returns the compiled definition's guard-stamped scheme
// identity.
func (c *compiledLayout) SchemeVersion() string { return c.def.SchemeVersion }

// Placement returns the compiled definition's role-distribution mode.
func (c *compiledLayout) Placement() Placement { return c.def.Placement }

// MinPrefix returns the compiled definition's minimum CIDR prefix length.
func (c *compiledLayout) MinPrefix() int { return c.def.MinPrefix }

// MinSubnets returns the compiled definition's minimum workload subnet
// count.
func (c *compiledLayout) MinSubnets() int { return c.def.MinSubnets }

// PinnedWorkloadIndex reports the single workload-subnet index the
// mgmt-tier static whose Layer A output key (ip_key, or role+"_ip" when
// unset) equals ipKey is pinned to. Unpinned statics (Subnets nil — placed
// on every index), multi-pinned statics, unknown keys, and definitions
// without a mgmt tier all report ok false: only an unambiguous single-index
// pin is a preference a lookup can act on.
func (c *compiledLayout) PinnedWorkloadIndex(ipKey string) (int, bool) {
	tier, ok := c.def.Tiers[TierMgmt]
	if !ok {
		return 0, false
	}

	for role, s := range tier.Statics {
		key := s.IPKey
		if key == "" {
			key = role + "_ip"
		}

		if key != ipKey {
			continue
		}

		if len(s.Subnets) == 1 {
			return s.Subnets[0], true
		}

		return 0, false
	}

	return 0, false
}

// LayerASlots returns the Layer A named-slot set for role on cidr at subnet
// index idx. The infra role's table is fixed regardless of strategy, cidr,
// or idx (see infraLayerASlots); the ocfp role's table is derived from this
// definition's mgmt tier (see ocfpLayerASlots). A negative idx means the
// caller has no workload-subnet position at all, and yields only the
// placements that apply to every index (see placedOn). Any other role is
// rejected with unknownRoleError.
func (c *compiledLayout) LayerASlots(role, cidr string, idx int) (LayerASlots, error) {
	switch role {
	case slotRoleInfra:
		return infraLayerASlots(), nil
	case slotRoleOCFP:
		return c.ocfpLayerASlots(cidr, idx)
	default:
		return LayerASlots{}, unknownRoleError(role)
	}
}

// infraLayerASlots returns the infra role's fixed LayerASlots: the four
// named statics the Layer A bootstrap subnet reads before BOSH exists
// (bastion, bosh, shield, blacksmith), plus the infra role's available band
// and its derived reserved offsets — the same historical infra-subnet
// layout every strategy shares (see the infraXxxOffset constants above).
func infraLayerASlots() LayerASlots {
	return LayerASlots{
		Named: []NamedSlot{
			{Key: "bastion_ip", Offset: infraBastionOffset},
			{Key: "bosh_ip", Offset: infraBoshOffset},
			{Key: "shield_ip", Offset: infraShieldOffset},
			{Key: "blacksmith_ip", Offset: infraBlacksmithOffset},
		},
		AvailableA: infraAvailableStart,
		AvailableB: infraAvailableEnd,
		ReservedB:  infraReservedBOffset,
		ReservedC:  infraReservedCOffset,
	}
}

// ocfpLayerASlots returns the ocfp role's LayerASlots for idx. A definition
// with no mgmt tier at all (LoadDefinition accepts an ocf-only strategy) has
// no Layer A workload layout to report and is rejected with
// noMgmtTierError rather than silently reporting a zero-valued band. For
// every other definition: every mgmt-tier static whose pinning includes idx,
// keyed by its ip_key (or
// role+"_ip" when unset) and sorted by offset then key for determinism,
// plus the mgmt band covering idx. AvailableB closes an open-ended band at
// cidr's last usable offset so Layer A and Layer B never disagree about
// where the band ends.
func (c *compiledLayout) ocfpLayerASlots(cidr string, idx int) (LayerASlots, error) {
	tier, ok := c.def.Tiers[TierMgmt]
	if !ok {
		return LayerASlots{}, noMgmtTierError(c.def.Name)
	}

	band, err := bandFor(tier.Available, idx)
	if err != nil {
		return LayerASlots{}, err
	}

	named := make([]NamedSlot, 0, len(tier.Statics))

	for role, s := range tier.Statics {
		if !placedOn(s.Subnets, idx) {
			continue
		}

		key := s.IPKey
		if key == "" {
			key = role + "_ip"
		}

		named = append(named, NamedSlot{Key: key, Offset: s.Offset})
	}

	sort.Slice(named, func(i, j int) bool {
		if named[i].Offset != named[j].Offset {
			return named[i].Offset < named[j].Offset
		}

		return named[i].Key < named[j].Key
	})

	availableB := band.End
	if availableB == 0 {
		lastUsable, ok := subnetLastUsableOffset(cidr)
		if !ok {
			return LayerASlots{}, ErrInvalidCIDR
		}

		availableB = lastUsable
	}

	return LayerASlots{
		Named:      named,
		AvailableA: band.Start,
		AvailableB: availableB,
		ReservedB:  band.Start - 1,
		ReservedC:  availableB + 1,
	}, nil
}

// ValidateSubnet rejects cidr if its prefix is longer (fewer host
// addresses) than the compiled definition's MinPrefix, wrapping
// ErrSubnetTooSmall with the strategy name, the offending cidr, its prefix,
// MinPrefix, and the definition's highest static offset (precomputed at
// Compile time).
func (c *compiledLayout) ValidateSubnet(cidr string) error {
	_, prefix, err := reservedip.ParseCIDR(cidr)
	if err != nil {
		return err
	}

	if prefix > c.def.MinPrefix {
		return subnetTooSmallError(c.def.Name, cidr, prefix, c.def.MinPrefix, c.highestOffset)
	}

	return nil
}

// ValidateSubnetSet rejects cidrs if it has fewer entries than the compiled
// definition's MinSubnets, wrapping ErrTooFewSubnets; otherwise it validates
// every cidr with ValidateSubnet, returning the first failure.
func (c *compiledLayout) ValidateSubnetSet(cidrs []string) error {
	if len(cidrs) < c.def.MinSubnets {
		return tooFewSubnetsError(c.def.Name, c.def.MinSubnets, len(cidrs))
	}

	for _, cidr := range cidrs {
		if err := c.ValidateSubnet(cidr); err != nil {
			return err
		}
	}

	return nil
}

// otherTier returns the mgmt/ocf tier that isn't t — ValidateBand's
// cross-tier check for a mgmt override looks at the ocf tier's band, and
// vice versa.
func otherTier(t Tier) Tier {
	if t == TierOCF {
		return TierMgmt
	}

	return TierOCF
}

// ValidateBand validates [start,end] as a reserved-IP available-band
// override for tier on cidr. TierInfra delegates to the shared validateBand
// (band.go), unchanged. TierMgmt/TierOCF share this generic path: after the
// shape checks (both start/end set, start < end, end within cidr's usable
// range), [start,end] must not collide with a named static of EITHER tier
// on any subnet index (ErrBandOverrideCollidesStatic — checked first, since
// a specific named-IP collision is the more actionable diagnostic), and
// must not intersect the OTHER tier's own available band on any subnet
// index (ErrBandOverrideCrossTier — an open band closed at cidr's last
// usable offset), so the two directors' cloud-config allocators never claim
// the same dynamic IP.
func (c *compiledLayout) ValidateBand(tier Tier, cidr string, start, end int) error {
	if tier == TierInfra {
		return validateBand(tier, cidr, start, end)
	}

	if start == 0 || end == 0 {
		return ErrBandOverridePartial
	}

	if end <= start {
		return bandOverrideEndNotAfterStartError(start, end)
	}

	lastUsable, ok := subnetLastUsableOffset(cidr)
	if !ok {
		return ErrInvalidCIDR
	}

	if end > lastUsable {
		return bandOverrideEndBeyondSubnetError(end, lastUsable, cidr)
	}

	if err := c.validateBandCollidesStatic(start, end); err != nil {
		return err
	}

	return c.validateBandCrossTier(tier, start, end, lastUsable)
}

// validateBandCollidesStatic rejects [start,end] if it contains a static
// offset of either tier, on any subnet index. When more than one static
// collides, it reports the lowest offending offset (tied-broken by role
// name) so the error is deterministic regardless of map iteration order.
func (c *compiledLayout) validateBandCollidesStatic(start, end int) error {
	var (
		found     bool
		hitTier   Tier
		hitRole   string
		hitOffset int
	)

	for idx := range c.def.MinSubnets {
		for _, t := range tierOrder {
			tier, ok := c.def.Tiers[t]
			if !ok {
				continue
			}

			for role, s := range tier.Statics {
				if !placedOn(s.Subnets, idx) || s.Offset < start || s.Offset > end {
					continue
				}

				if !found || s.Offset < hitOffset || (s.Offset == hitOffset && role < hitRole) {
					found, hitTier, hitRole, hitOffset = true, t, role, s.Offset
				}
			}
		}
	}

	if found {
		return bandOverrideCollidesStaticError(start, end, hitTier, hitRole, hitOffset)
	}

	return nil
}

// validateBandCrossTier rejects [start,end] (an override for tier) if it
// intersects the other tier's own available band on any subnet index, that
// band's open end closed at lastUsable.
func (c *compiledLayout) validateBandCrossTier(tier Tier, start, end, lastUsable int) error {
	other := otherTier(tier)

	otherDef, ok := c.def.Tiers[other]
	if !ok {
		return nil
	}

	for idx := range c.def.MinSubnets {
		band, err := bandFor(otherDef.Available, idx)
		if err != nil {
			return err
		}

		otherEnd := band.End
		if otherEnd == 0 {
			otherEnd = lastUsable
		}

		if start <= otherEnd && band.Start <= end {
			return bandOverrideCrossTierError(start, end, tier, other, band.Start, otherEnd)
		}
	}

	return nil
}

// WorkloadTable returns the compiled Layer B assignment table. cidr is
// accepted for Layout interface conformance but unused: the table is fixed
// at Compile time and does not vary by subnet size, preserving the contract
// the hand-written wide/compact tables documented — every workload subnet
// independently gets the same role set, computed from its own base address.
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
