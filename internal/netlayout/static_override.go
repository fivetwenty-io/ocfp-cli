package netlayout

import (
	"maps"
	"sort"

	"github.com/fivetwenty-io/ocfp-cli/internal/reservedip"
)

// ControlPlaneRole is the mgmt-tier static that gives the single
// control-plane core VM its fixed address. The wide and spanning strategies
// place it at offset 23 and keep 24 and 25 free, so a later list of
// control-plane addresses can grow into 23-25 without moving the first
// one. The compact strategy has no free mgmt offset and does not place it.
const ControlPlaneRole = "control_plane"

// gatewayOffset is the offset of every workload subnet's gateway.
const gatewayOffset = 1

// BandRange is an effective available band for one tier, as offsets from a
// subnet's base address. End 0 means the band runs to the subnet's last
// usable address.
type BandRange struct {
	Start int
	End   int
}

// WithStaticOverride returns a copy of c with the tier static named role
// moved to offsets[0], checked for the workload subnet at index idx whose
// CIDR is cidr. The checks run in this order:
//
//   - tier is mgmt or ocf, and the strategy places role in it;
//   - offsets holds exactly one entry.
//
// Those two hold on every subnet. The rest run only when the strategy
// places role on idx, because a subnet that does not carry the static never
// writes its address, and its size or statics say nothing about it:
//
//   - the offset is inside cidr, and is not the network address, the
//     broadcast address, the gateway (offset 1), or below the static floor;
//   - the offset is outside the available band of both tiers on idx
//     (bands[t] when given, else the strategy's own band for idx);
//   - no other static of either tier holds the offset on idx.
//
// Every writer calls this once per subnet, so each placing subnet is
// checked against its own CIDR. The derived layout is built without
// re-running validateDefinition, because that would check the moved static
// against the strategy's default bands even when a band override has moved
// a band clear of it. The checks above cover every rule validateDefinition
// applies to a single static, and the caller's later ValidateBand call
// checks the band override against the moved static.
func (c *compiledLayout) WithStaticOverride(
	tier Tier, role, cidr string, idx int, offsets []int, bands map[Tier]BandRange,
) (Layout, error) {
	tierDef, ok := c.def.Tiers[tier]
	if tier == TierInfra || !ok {
		return nil, staticOverrideUnknownRoleError(c.def.Name, tier, role)
	}

	static, ok := tierDef.Statics[role]
	if !ok {
		return nil, staticOverrideUnknownRoleError(c.def.Name, tier, role)
	}

	if len(offsets) != 1 {
		return nil, staticOverrideCountError(role, len(offsets))
	}

	offset := offsets[0]

	if placedOn(static.Subnets, idx) {
		if err := c.checkStaticOverrideOnSubnet(tier, role, cidr, idx, offset, bands); err != nil {
			return nil, err
		}
	}

	derived := c.def
	derived.Tiers = maps.Clone(c.def.Tiers)

	moved := tierDef
	moved.Statics = maps.Clone(tierDef.Statics)
	static.Offset = offset
	moved.Statics[role] = static
	derived.Tiers[tier] = moved

	return &compiledLayout{
		def:           derived,
		table:         buildWorkloadTable(derived),
		highestOffset: highestStaticOffset(derived),
	}, nil
}

// checkStaticOverrideOnSubnet runs the address, band, and static checks
// for an override of role to offset on the subnet at index idx.
func (c *compiledLayout) checkStaticOverrideOnSubnet(
	tier Tier, role, cidr string, idx, offset int, bands map[Tier]BandRange,
) error {
	lastUsable, ok := subnetLastUsableOffset(cidr)
	if !ok {
		return ErrInvalidCIDR
	}

	if err := checkStaticOverrideAddress(role, cidr, offset, lastUsable); err != nil {
		return err
	}

	if err := c.checkStaticOverrideBands(role, offset, idx, lastUsable, bands); err != nil {
		return err
	}

	return c.checkStaticOverrideStatics(tier, role, offset, idx)
}

// checkStaticOverrideAddress rejects an offset that is outside cidr, or
// that lands on the network, broadcast, or gateway address, or below the
// static floor.
func checkStaticOverrideAddress(role, cidr string, offset, lastUsable int) error {
	broadcast := lastUsable + 1

	if offset < 0 || offset > broadcast {
		return staticOverrideOutsideSubnetError(role, offset, cidr, broadcast)
	}

	base, _, err := reservedip.ParseCIDR(cidr)
	if err != nil {
		return ErrInvalidCIDR
	}

	ip := reservedip.AddOffsetToIP(base, offset)

	switch {
	case offset == 0:
		return staticOverrideAddressError(ErrStaticOverrideNetworkAddress, role, offset, ip, cidr)
	case offset == broadcast:
		return staticOverrideAddressError(ErrStaticOverrideBroadcastAddress, role, offset, ip, cidr)
	case offset == gatewayOffset:
		return staticOverrideAddressError(ErrStaticOverrideGateway, role, offset, ip, cidr)
	case offset < minStaticOffset:
		return staticOverrideBelowFloorError(role, offset)
	}

	return nil
}

// checkStaticOverrideBands rejects an offset that falls inside either
// tier's effective available band on subnet index idx.
func (c *compiledLayout) checkStaticOverrideBands(
	role string, offset, idx, lastUsable int, bands map[Tier]BandRange,
) error {
	for _, t := range tierOrder {
		tierDef, ok := c.def.Tiers[t]
		if !ok {
			continue
		}

		start, end, err := effectiveBand(tierDef, t, idx, bands)
		if err != nil {
			return err
		}

		if end == 0 {
			end = lastUsable
		}

		if offset >= start && offset <= end {
			return staticOverrideInBandError(role, offset, idx, t, start, end)
		}
	}

	return nil
}

// effectiveBand returns tier t's available band on subnet index idx: the
// override in bands when one is given for t, else the strategy's own band.
func effectiveBand(tierDef TierDef, t Tier, idx int, bands map[Tier]BandRange) (int, int, error) {
	if override, ok := bands[t]; ok {
		return override.Start, override.End, nil
	}

	band, err := bandFor(tierDef.Available, idx)
	if err != nil {
		return 0, 0, err
	}

	return band.Start, band.End, nil
}

// checkStaticOverrideStatics rejects an offset another static of either
// tier already holds on subnet index idx. Roles are visited in sorted order
// so the reported collision is deterministic.
func (c *compiledLayout) checkStaticOverrideStatics(tier Tier, role string, offset, idx int) error {
	for _, t := range tierOrder {
		tierDef, ok := c.def.Tiers[t]
		if !ok {
			continue
		}

		roles := make([]string, 0, len(tierDef.Statics))
		for r := range tierDef.Statics {
			roles = append(roles, r)
		}

		sort.Strings(roles)

		for _, other := range roles {
			if t == tier && other == role {
				continue
			}

			s := tierDef.Statics[other]
			if s.Offset == offset && placedOn(s.Subnets, idx) {
				return staticOverrideCollidesStaticError(role, offset, idx, t, other)
			}
		}
	}

	return nil
}
