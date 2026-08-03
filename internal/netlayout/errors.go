package netlayout

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNotImplemented is returned by stub Layout methods that have not yet
// been given a real implementation for the calling strategy. Its existence
// means an accidental early invocation — a misordered test, a future task
// that jumps the gun — fails loudly with a greppable error instead of a
// nil-dereference panic or a silently-wrong empty result.
var ErrNotImplemented = errors.New("netlayout: not implemented")

// ErrUnknownStrategy is the sentinel Lookup wraps when asked for a strategy
// name that is not registered. Callers match it with errors.Is.
var ErrUnknownStrategy = errors.New("unknown network strategy")

// unknownStrategyError wraps ErrUnknownStrategy with the offending name and
// the sorted list of registered strategy names ("unknown network strategy
// %q: known strategies are ...").
func unknownStrategyError(name string) error {
	return fmt.Errorf("%w %q: known strategies are %s", ErrUnknownStrategy, name, strings.Join(Names(), ", "))
}

// ErrSubnetTooSmall is the sentinel ValidateSubnet wraps when cidr's prefix
// is longer (fewer host addresses) than a strategy's MinPrefix requires.
// Callers match it with errors.Is.
var ErrSubnetTooSmall = errors.New("subnet too small for strategy")

// ErrUnknownRole is the sentinel Slots wraps when asked for a role that is
// neither "infra" nor "ocfp". Callers match it with errors.Is.
var ErrUnknownRole = errors.New("unknown netlayout role")

// unknownRoleError wraps ErrUnknownRole with the offending role name and
// the two roles Slots recognizes, so the message is enough on its own to
// explain the rejection.
func unknownRoleError(role string) error {
	return fmt.Errorf("%w %q: known roles are %q, %q", ErrUnknownRole, role, slotRoleInfra, slotRoleOCFP)
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
