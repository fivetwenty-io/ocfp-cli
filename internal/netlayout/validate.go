package netlayout

import (
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/reservedip"
)

// haproxyRole is the ocf-tier static name the CF kit claims its static
// window from (see ErrHaproxyCoupling).
const haproxyRole = "haproxy"

// minStaticOffset is the lowest offset a static may claim; offsets below it
// collide with the fixed named-IP slots every strategy reserves.
const minStaticOffset = 3

// tierOrder fixes mgmt-before-ocf iteration so error messages and rule
// evaluation are deterministic regardless of map order.
var tierOrder = []Tier{TierMgmt, TierOCF}

// placedOn reports whether a placement pinned to subnets applies to idx
// (nil = all indices).
//
// A negative idx — a caller with no workload-subnet position, see
// Layout.LayerASlots — matches no pinned placement, and needs no special
// case to do so: validateSubnetPin rejects a negative pinned index at
// Compile time, and the membership test below is an equality scan, so no
// pinned entry can hold a value a negative idx would match.
func placedOn(subnets []int, idx int) bool {
	if subnets == nil {
		return true
	}

	return reservedip.ContainsInt(subnets, idx)
}

// bandFor returns the single band entry of bands that covers idx, and an
// error wrapping ErrBandOverlap if zero or more than one covers it.
func bandFor(bands []BandPlacement, idx int) (BandPlacement, error) {
	var (
		found BandPlacement
		count int
	)

	for _, b := range bands {
		if placedOn(b.Subnets, idx) {
			found = b
			count++
		}
	}

	if count != 1 {
		return BandPlacement{}, fmt.Errorf("%w: subnet index %d covered by %d bands, want exactly 1",
			ErrBandOverlap, idx, count)
	}

	return found, nil
}

// bandEnd returns b's effective end offset for overlap/fit arithmetic: its
// own End when closed, or the subnet's last usable host offset (for a
// /minPrefix subnet) when open (End == 0).
func bandEnd(b BandPlacement, minPrefix int) int {
	if b.End > 0 {
		return b.End
	}

	return reservedip.CalculateLastHostOffset(minPrefix)
}

// insideBand reports whether offset falls within b's range, inclusive of
// both ends.
func insideBand(offset int, b BandPlacement, minPrefix int) bool {
	return offset >= b.Start && offset <= bandEnd(b, minPrefix)
}

// bandsOverlap reports whether a and b's ranges (using effective ends)
// intersect.
func bandsOverlap(a, b BandPlacement, minPrefix int) bool {
	return a.Start <= bandEnd(b, minPrefix) && b.Start <= bandEnd(a, minPrefix)
}

// validateDefinition applies every semantic rule Task 1's structural
// LoadDefinition does not: offset floors and subnet pinning, per-index
// static collisions, band coverage and overlap (including the haproxy/CF
// kit coupling), and min_prefix fit. Task 3's Compile calls it before table
// building.
func validateDefinition(def Definition) error {
	if err := validateStatics(def); err != nil {
		return err
	}

	if err := validateBands(def); err != nil {
		return err
	}

	if err := validatePrefixFit(def); err != nil {
		return err
	}

	return validateIndices(def)
}

// validateStatics applies the per-static rules that don't depend on a
// specific subnet index: the offset floor, and subnet-pin range/placement
// checks.
func validateStatics(def Definition) error {
	for _, t := range tierOrder {
		tier, ok := def.Tiers[t]
		if !ok {
			continue
		}

		for role, s := range tier.Statics {
			if s.Offset < minStaticOffset {
				return fmt.Errorf("%w: %s: strategy %q tier %q static %q: offset %d below %d",
					ErrBadPinning, def.Source, def.Name, t, role, s.Offset, minStaticOffset)
			}

			// A pinned static's compiled Assignment uses SubnetMapping, whose
			// engine path (processSubnetMappingAssignment) never reads
			// Assignment.IPKey — so ip_key on a pinned static could never
			// reach its consumer. Reject at validation time rather than
			// silently dropping it at compile time.
			if s.Subnets != nil && s.IPKey != "" {
				return fmt.Errorf("%w: %s: strategy %q tier %q static %q: ip_key is only supported on unpinned statics",
					ErrBadPinning, def.Source, def.Name, t, role)
			}

			what := fmt.Sprintf("static %q", role)
			if err := validateSubnetPin(def, t, what, s.Subnets); err != nil {
				return err
			}
		}
	}

	return nil
}

// validateSubnetPin applies the shared pinning rule to a static's or band's
// Subnets field: pinning is forbidden under colocated placement, and every
// pinned index must fall within [0, def.MinSubnets).
func validateSubnetPin(def Definition, t Tier, what string, subnets []int) error {
	if subnets == nil {
		return nil
	}

	if def.Placement == PlacementColocated {
		return fmt.Errorf("%w: %s: strategy %q tier %q %s: subnet pinning forbidden under colocated placement",
			ErrBadPinning, def.Source, def.Name, t, what)
	}

	for _, idx := range subnets {
		if idx < 0 || idx >= def.MinSubnets {
			return fmt.Errorf("%w: %s: strategy %q tier %q %s: subnet index %d out of range [0,%d)",
				ErrBadPinning, def.Source, def.Name, t, what, idx, def.MinSubnets)
		}
	}

	return nil
}

// validateBands applies the per-band rules that don't depend on a specific
// subnet index: subnet-pin range/placement (shared with statics), and
// well-formedness (Start < End for a closed band).
func validateBands(def Definition) error {
	for _, t := range tierOrder {
		tier, ok := def.Tiers[t]
		if !ok {
			continue
		}

		for i, b := range tier.Available {
			what := fmt.Sprintf("band[%d]", i)

			if err := validateSubnetPin(def, t, what, b.Subnets); err != nil {
				return err
			}

			if b.End > 0 && b.Start >= b.End {
				return fmt.Errorf("%w: %s: strategy %q tier %q %s: start %d must be less than end %d",
					ErrBandOverlap, def.Source, def.Name, t, what, b.Start, b.End)
			}
		}
	}

	return nil
}

// validatePrefixFit checks that every static offset and every closed band
// end fits within def.MinPrefix's last usable host offset.
func validatePrefixFit(def Definition) error {
	lastHost := reservedip.CalculateLastHostOffset(def.MinPrefix)

	for _, t := range tierOrder {
		tier, ok := def.Tiers[t]
		if !ok {
			continue
		}

		for role, s := range tier.Statics {
			if s.Offset > lastHost {
				return fmt.Errorf("%w: %s: strategy %q min_prefix /%d allows offsets up to %d, tier %q static %q is at %d",
					ErrPrefixTooNarrow, def.Source, def.Name, def.MinPrefix, lastHost, t, role, s.Offset)
			}
		}

		for i, b := range tier.Available {
			if b.End > 0 && b.End > lastHost {
				return fmt.Errorf("%w: %s: strategy %q min_prefix /%d allows offsets up to %d, tier %q band[%d] ends at %d",
					ErrPrefixTooNarrow, def.Source, def.Name, def.MinPrefix, lastHost, t, i, b.End)
			}
		}
	}

	return nil
}

// validateIndices applies every per-subnet-index rule: static offset
// collisions, exactly-one band coverage per tier, cross-tier band overlap,
// statics falling inside a band, the haproxy/ocf-band coupling, and the
// open-band-must-be-topmost constraint.
func validateIndices(def Definition) error {
	for idx := range def.MinSubnets {
		if err := validateIndexCollisions(def, idx); err != nil {
			return err
		}

		bands := map[Tier]BandPlacement{}

		for _, t := range tierOrder {
			tier, ok := def.Tiers[t]
			if !ok {
				continue
			}

			b, err := bandFor(tier.Available, idx)
			if err != nil {
				return fmt.Errorf("%s: strategy %q tier %q index %d: %w", def.Source, def.Name, t, idx, err)
			}

			bands[t] = b
		}

		if err := validateCrossTierOverlap(def, idx, bands); err != nil {
			return err
		}

		if err := validateStaticsAgainstBands(def, idx, bands); err != nil {
			return err
		}

		if err := validateHaproxyCoupling(def, idx, bands); err != nil {
			return err
		}

		if err := validateOpenBandTopmost(def, idx, bands); err != nil {
			return err
		}
	}

	return nil
}

// validateIndexCollisions rejects two statics (in either tier) resolving to
// the same offset on idx.
func validateIndexCollisions(def Definition, idx int) error {
	seen := map[int]string{}

	for _, t := range tierOrder {
		tier, ok := def.Tiers[t]
		if !ok {
			continue
		}

		for role, s := range tier.Statics {
			if !placedOn(s.Subnets, idx) {
				continue
			}

			if prev, exists := seen[s.Offset]; exists {
				return fmt.Errorf("%w: %s: strategy %q index %d: offset %d claimed by both %s and %s/%s",
					ErrOffsetCollision, def.Source, def.Name, idx, s.Offset, prev, t, role)
			}

			seen[s.Offset] = fmt.Sprintf("%s/%s", t, role)
		}
	}

	return nil
}

// validateCrossTierOverlap rejects mgmt's and ocf's resolved bands on idx
// intersecting each other.
func validateCrossTierOverlap(def Definition, idx int, bands map[Tier]BandPlacement) error {
	mgmtBand, hasMgmt := bands[TierMgmt]

	ocfBand, hasOCF := bands[TierOCF]
	if !hasMgmt || !hasOCF {
		return nil
	}

	if bandsOverlap(mgmtBand, ocfBand, def.MinPrefix) {
		return fmt.Errorf("%w: %s: strategy %q index %d: mgmt band [%d,%d] overlaps ocf band [%d,%d]",
			ErrBandOverlap, def.Source, def.Name, idx,
			mgmtBand.Start, bandEnd(mgmtBand, def.MinPrefix),
			ocfBand.Start, bandEnd(ocfBand, def.MinPrefix))
	}

	return nil
}

// validateStaticsAgainstBands rejects any static (either tier) falling
// inside either tier's resolved band on idx, except the ocf-tier haproxy
// static — its placement inside the ocf band is checked separately by
// validateHaproxyCoupling, since a wrong offset there is a more specific
// (and more actionable) error than a generic band-overlap.
func validateStaticsAgainstBands(def Definition, idx int, bands map[Tier]BandPlacement) error {
	for _, t := range tierOrder {
		tier, ok := def.Tiers[t]
		if !ok {
			continue
		}

		for role, s := range tier.Statics {
			if t == TierOCF && role == haproxyRole {
				continue
			}

			if !placedOn(s.Subnets, idx) {
				continue
			}

			for _, bt := range tierOrder {
				band, ok := bands[bt]
				if !ok {
					continue
				}

				if insideBand(s.Offset, band, def.MinPrefix) {
					return fmt.Errorf("%w: %s: strategy %q index %d: tier %q static %q at %d falls inside tier %q band [%d,%d]",
						ErrBandOverlap, def.Source, def.Name, idx, t, role, s.Offset, bt, band.Start, bandEnd(band, def.MinPrefix))
				}
			}
		}
	}

	return nil
}

// validateHaproxyCoupling rejects an ocf-tier haproxy static placed on idx
// that does not sit at exactly the ocf band's start + 1 — the offset the CF
// kit assumes when it claims its own static window from inside the
// available band.
func validateHaproxyCoupling(def Definition, idx int, bands map[Tier]BandPlacement) error {
	tier, ok := def.Tiers[TierOCF]
	if !ok {
		return nil
	}

	s, ok := tier.Statics[haproxyRole]
	if !ok || !placedOn(s.Subnets, idx) {
		return nil
	}

	band, ok := bands[TierOCF]
	if !ok {
		return nil
	}

	want := band.Start + 1
	if s.Offset != want {
		return fmt.Errorf("%w: %s: strategy %q index %d: haproxy at %d, ocf band starts at %d so the cf kit needs it at %d",
			ErrHaproxyCoupling, def.Source, def.Name, idx, s.Offset, band.Start, want)
	}

	return nil
}

// validateOpenBandTopmost rejects more than one open-ended band resolving
// to idx across both tiers, and rejects the one open band that does exist
// from sitting at or below any closed band end or non-haproxy static on idx.
func validateOpenBandTopmost(def Definition, idx int, bands map[Tier]BandPlacement) error {
	openTier, openBand, openCount := findOpenBand(bands)
	if openCount > 1 {
		return fmt.Errorf("%w: %s: strategy %q index %d: %d open-ended bands, want at most 1",
			ErrBandOverlap, def.Source, def.Name, idx, openCount)
	}

	if openCount == 0 {
		return nil
	}

	for _, t := range tierOrder {
		b, ok := bands[t]
		if !ok || t == openTier || b.End == 0 {
			continue
		}

		if b.End >= openBand.Start {
			return fmt.Errorf("%w: %s: strategy %q index %d: open band must be the topmost zone (start %d not above tier %q band end %d)",
				ErrBandOverlap, def.Source, def.Name, idx, openBand.Start, t, b.End)
		}
	}

	for _, t := range tierOrder {
		tier, ok := def.Tiers[t]
		if !ok {
			continue
		}

		for role, s := range tier.Statics {
			if t == TierOCF && role == haproxyRole {
				continue
			}

			if !placedOn(s.Subnets, idx) || s.Offset < openBand.Start {
				continue
			}

			return fmt.Errorf("%w: %s: strategy %q index %d: open band must be the topmost zone (start %d not above tier %q static %q at %d)",
				ErrBandOverlap, def.Source, def.Name, idx, openBand.Start, t, role, s.Offset)
		}
	}

	return nil
}

// findOpenBand returns the tier and value of the one open-ended (End == 0)
// band resolved in bands, and how many such bands were found.
func findOpenBand(bands map[Tier]BandPlacement) (Tier, BandPlacement, int) {
	var (
		openTier  Tier
		openBand  BandPlacement
		openCount int
	)

	for _, t := range tierOrder {
		b, ok := bands[t]
		if !ok || b.End != 0 {
			continue
		}

		openTier = t
		openBand = b
		openCount++
	}

	return openTier, openBand, openCount
}
