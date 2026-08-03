package netlayout

import "net"

// bandOverrideFloor is the lowest offset an available-band override may
// start at: offsets 0-11 are the fixed named-IP slots (bastion/bosh/vault/
// .../artifacts) on the infra role's layout (see infraLayerASlots), so any
// override must start at or after the first free offset to avoid colliding
// with them. This floor is NOT strategy-specific — the infra subnet's layout
// is the same for every strategy — so it reuses infraAvailableStart
// (compiled.go) rather than restating the value.
const bandOverrideFloor = infraAvailableStart

// ipv4Bits and bandOverrideBroadcastOffset support subnetLastUsableOffset's
// total-address and last-usable-offset arithmetic, mirroring the historical
// internal/bootstrap.subnetTotalSize/applyAvailableBandOverride computation
// this method replaces.
const (
	ipv4Bits                    = 32
	bandOverrideBroadcastOffset = 2
)

// validateBand is the TierInfra body compiledLayout.ValidateBand delegates
// to for every strategy alike. It is a faithful port of the historical
// internal/bootstrap.applyAvailableBandOverride hand-rolled validation
// branches: reject a partial pair (one of start/end
// zero), start >= end, start below the historical floor, and a band that
// exceeds cidr's usable host range. tier is accepted for interface
// conformance and future per-tier bands but is not (yet) branched on: none
// of these four checks were ever tier- or strategy-specific historically.
func validateBand(_ Tier, cidr string, start, end int) error {
	if start == 0 || end == 0 {
		return ErrBandOverridePartial
	}

	if start < bandOverrideFloor {
		return bandOverrideStartTooLowError(start)
	}

	if end <= start {
		return bandOverrideEndNotAfterStartError(start, end)
	}

	lastUsableOffset, ok := subnetLastUsableOffset(cidr)
	if !ok {
		return ErrInvalidCIDR
	}

	if end > lastUsableOffset {
		return bandOverrideEndBeyondSubnetError(end, lastUsableOffset, cidr)
	}

	return nil
}

// subnetLastUsableOffset returns the highest offset (relative to cidr's base
// address) that is still a usable host address — excluding the network and
// broadcast addresses — and whether cidr parsed as an IPv4 CIDR.
func subnetLastUsableOffset(cidr string) (int, bool) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil || ipnet == nil {
		return 0, false
	}

	prefixLen, bits := ipnet.Mask.Size()
	if bits != ipv4Bits || prefixLen < 0 || prefixLen > ipv4Bits {
		return 0, false
	}

	total := uint32(1) << (ipv4Bits - prefixLen)

	return int(total) - bandOverrideBroadcastOffset, true
}
