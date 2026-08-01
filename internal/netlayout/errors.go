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

// subnetTooSmallError wraps ErrSubnetTooSmall with the offending strategy
// name, cidr, its prefix, the strategy's minimum prefix, and its highest
// fixed offset, so the message is enough on its own to explain the
// rejection without cross-referencing the strategy's source.
func subnetTooSmallError(strategy, cidr string, prefix, minPrefix, highestOffset int) error {
	return fmt.Errorf("%w: strategy %q cidr %q is /%d, requires minimum /%d (highest fixed offset %d)",
		ErrSubnetTooSmall, strategy, cidr, prefix, minPrefix, highestOffset)
}
