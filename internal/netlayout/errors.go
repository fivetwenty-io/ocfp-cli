package netlayout

import (
	"errors"
	"fmt"
)

// ErrNotImplemented is returned by stub Layout methods that have not yet
// been given a real implementation for the calling strategy. Its existence
// means an accidental early invocation — a misordered test, a future task
// that jumps the gun — fails loudly with a greppable error instead of a
// nil-dereference panic or a silently-wrong empty result.
var ErrNotImplemented = errors.New("netlayout: not implemented")

// ErrUnknownStrategy is the sentinel Lookup wraps when asked for a strategy
// name that is not registered. Callers match it with errors.Is. Every
// caller resolves it through a *Catalog (see Catalog.unknownError in
// catalog.go) — Lookup, Default, and the package-level registry are all
// thin wrappers over Builtins(), a *Catalog scoped to the three built-in
// strategies.
var ErrUnknownStrategy = errors.New("unknown network strategy")

// ErrSubnetTooSmall is the sentinel ValidateSubnet wraps when cidr's prefix
// is longer (fewer host addresses) than a strategy's MinPrefix requires.
// Callers match it with errors.Is.
var ErrSubnetTooSmall = errors.New("subnet too small for strategy")

// ErrUnknownRole is the sentinel LayerASlots wraps when asked for a role
// that is neither "infra" nor "ocfp". Callers match it with errors.Is.
var ErrUnknownRole = errors.New("unknown netlayout role")

// unknownRoleError wraps ErrUnknownRole with the offending role name and
// the two roles LayerASlots recognizes, so the message is enough on its own
// to explain the rejection.
func unknownRoleError(role string) error {
	return fmt.Errorf("%w %q: known roles are %q, %q", ErrUnknownRole, role, slotRoleInfra, slotRoleOCFP)
}

// ErrNoMgmtTier is the sentinel LayerASlots wraps when the "ocfp" role is
// asked of a strategy whose definition declares no mgmt tier: the ocfp
// role's Layer A layout is derived entirely from that tier, so there is
// nothing to report. Callers match it with errors.Is.
var ErrNoMgmtTier = errors.New("netlayout: strategy defines no mgmt tier")

// noMgmtTierError wraps ErrNoMgmtTier with the offending strategy name.
func noMgmtTierError(strategy string) error {
	return fmt.Errorf("%w: strategy %q cannot answer the %q role", ErrNoMgmtTier, strategy, slotRoleOCFP)
}

// subnetTooSmallError wraps ErrSubnetTooSmall with the offending strategy
// name, cidr, its prefix, the strategy's minimum prefix, and its highest
// fixed offset, so the message is enough on its own to explain the
// rejection without cross-referencing the strategy's source.
func subnetTooSmallError(strategy, cidr string, prefix, minPrefix, highestOffset int) error {
	return fmt.Errorf("%w: strategy %q cidr %q is /%d, requires minimum /%d (highest fixed offset %d)",
		ErrSubnetTooSmall, strategy, cidr, prefix, minPrefix, highestOffset)
}

// Sentinel errors for ValidateBand's available-band override checks. Callers
// match with errors.Is. These port the historical
// internal/bootstrap.applyAvailableBandOverride sentinels of the same shape
// (ErrBandOverridePartial/StartTooLow/EndNotAfterStart/EndBeyondSubnet) into
// this package, since ValidateBand now owns that validation.
var (
	// ErrBandOverridePartial is returned when only one of a band override's
	// start/end is set (0). Both must be set together, or neither (the
	// caller skips ValidateBand entirely and uses the strategy's default
	// layout).
	ErrBandOverridePartial = errors.New("netlayout: band override start and end must both be set, or neither")
	// ErrBandOverrideStartTooLow is returned when start would collide with
	// the fixed named-IP slots below the historical available-band floor.
	ErrBandOverrideStartTooLow = errors.New("netlayout: band override start collides with reserved named-IP slots")
	// ErrBandOverrideEndNotAfterStart is returned when end does not fall
	// strictly after start.
	ErrBandOverrideEndNotAfterStart = errors.New("netlayout: band override end must be greater than start")
	// ErrBandOverrideEndBeyondSubnet is returned when end falls outside
	// cidr's usable address range.
	ErrBandOverrideEndBeyondSubnet = errors.New("netlayout: band override end is beyond the subnet's usable address range")
	// ErrInvalidCIDR is returned when ValidateBand cannot parse cidr as an
	// IPv4 CIDR.
	ErrInvalidCIDR = errors.New("netlayout: invalid CIDR")
	// ErrBandOverrideCollidesStatic is returned when a mgmt/ocf band
	// override's [start,end] range contains a named static's offset from
	// either tier, on any subnet index. Named statics from both tiers
	// share one physical address space per workload subnet, so an
	// override handed to one tier's dynamic allocator must not reach a
	// fixed IP the other tier's kit already reads unconditionally.
	ErrBandOverrideCollidesStatic = errors.New("netlayout: band override collides with a named static")
	// ErrBandOverrideCrossTier is returned when a mgmt/ocf band
	// override's [start,end] range intersects the OTHER tier's own
	// available band (open bands closed at the subnet's last usable
	// offset) on any subnet index, so the two directors' cloud-config
	// allocators would never race for the same dynamic IP.
	ErrBandOverrideCrossTier = errors.New("netlayout: band override intersects the other tier's available band")
)

// Sentinel errors for validateDefinition's semantic checks (offset/pinning,
// collisions, band coverage and overlap, the haproxy/CF-kit coupling, and
// min_prefix fit). Callers match with errors.Is; the wrapped message names
// def.Source, def.Name, and the offending values.
var (
	ErrOffsetCollision = errors.New("netlayout: static offset collision")
	ErrBandOverlap     = errors.New("netlayout: band overlap or coverage error")
	ErrHaproxyCoupling = errors.New("netlayout: haproxy must sit at ocf band start + 1 (the cf kit claims its static window from inside the available band)")
	ErrPrefixTooNarrow = errors.New("netlayout: min_prefix does not fit the definition's highest offset")
	ErrBadPinning      = errors.New("netlayout: invalid subnet pinning")
	// ErrDuplicateIPKey rejects two statics in one tier resolving to the
	// same output key (ip_key, or role+"_ip" when unset). The key is how
	// every downstream lookup — Layer A outputs, Layer B records,
	// PinnedWorkloadIndex — identifies a static; a duplicate would make
	// those lookups depend on map iteration order.
	ErrDuplicateIPKey = errors.New("netlayout: duplicate reserved-IP output key")
)

// bandOverrideStartTooLowError wraps ErrBandOverrideStartTooLow with the
// offending start and the floor it fell below.
func bandOverrideStartTooLowError(start int) error {
	return fmt.Errorf("%w: got %d, must be >= %d", ErrBandOverrideStartTooLow, start, bandOverrideFloor)
}

// bandOverrideEndNotAfterStartError wraps ErrBandOverrideEndNotAfterStart
// with the offending start/end pair.
func bandOverrideEndNotAfterStartError(start, end int) error {
	return fmt.Errorf("%w: start=%d end=%d", ErrBandOverrideEndNotAfterStart, start, end)
}

// bandOverrideEndBeyondSubnetError wraps ErrBandOverrideEndBeyondSubnet with
// the offending end, the subnet's last usable offset, and cidr.
func bandOverrideEndBeyondSubnetError(end, lastUsableOffset int, cidr string) error {
	return fmt.Errorf("%w: end=%d last-usable-offset=%d subnet=%s",
		ErrBandOverrideEndBeyondSubnet, end, lastUsableOffset, cidr)
}

// ErrTooFewSubnets is the sentinel ValidateSubnetSet wraps when fewer cidrs
// are given than a strategy's MinSubnets requires. Callers match it with
// errors.Is.
var ErrTooFewSubnets = errors.New("netlayout: too few workload subnets for strategy")

// tooFewSubnetsError wraps ErrTooFewSubnets with the strategy name, its
// MinSubnets, and the number of cidrs actually given.
func tooFewSubnetsError(strategy string, minSubnets, got int) error {
	return fmt.Errorf("%w: strategy %q requires at least %d workload subnets, got %d",
		ErrTooFewSubnets, strategy, minSubnets, got)
}

// bandOverrideCollidesStaticError wraps ErrBandOverrideCollidesStatic with
// the offending override range and the colliding static's tier, role, and
// offset, so the "either tier" behavior is explained without the caller
// cross-referencing the strategy's definition.
func bandOverrideCollidesStaticError(start, end int, tier Tier, role string, offset int) error {
	return fmt.Errorf("%w: [%d,%d] collides with tier %q static %q at offset %d",
		ErrBandOverrideCollidesStatic, start, end, tier, role, offset)
}

// bandOverrideCrossTierError wraps ErrBandOverrideCrossTier with the
// offending override range, the tier it was requested for, and the other
// tier's own band range it intersects.
func bandOverrideCrossTierError(start, end int, tier, otherTier Tier, otherStart, otherEnd int) error {
	return fmt.Errorf("%w: tier %q override [%d,%d] intersects tier %q band [%d,%d]",
		ErrBandOverrideCrossTier, tier, start, end, otherTier, otherStart, otherEnd)
}

// ErrStrategyShadowed is the sentinel Catalog.add wraps when a BYO strategy
// definition's name collides with an already-registered strategy — a
// built-in, or an earlier BYO file loaded into the same catalog. Callers
// match it with errors.Is.
var ErrStrategyShadowed = errors.New("netlayout: strategy name conflicts with an already-registered strategy")

// strategyShadowedError wraps ErrStrategyShadowed with the offending name
// and the source (file path, or "built-in:...") it was loaded from.
func strategyShadowedError(name, source string) error {
	return fmt.Errorf("%w: strategy %q from %s conflicts with an already-registered strategy",
		ErrStrategyShadowed, name, source)
}

// ErrSchemeCollision is the sentinel Catalog.add wraps when a BYO strategy
// definition's scheme_version collides with an already-registered
// strategy's — a built-in's, or an earlier BYO file loaded into the same
// catalog. Two strategies sharing a scheme_version would make the stamped
// guard value ambiguous about which strategy actually produced a bloc's
// addresses. Callers match it with errors.Is.
var ErrSchemeCollision = errors.New("netlayout: scheme_version conflicts with an already-registered strategy")

// schemeCollisionError wraps ErrSchemeCollision with the offending
// scheme_version and the two strategy names that both claim it.
func schemeCollisionError(schemeVersion, name, existingName string) error {
	return fmt.Errorf("%w: strategy %q's scheme_version %q already claimed by strategy %q",
		ErrSchemeCollision, name, schemeVersion, existingName)
}
