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
// the sorted list of registered strategy names, matching the error text
// convention set by P1 §3.2 ("unknown network strategy %q: known
// strategies are ...").
func unknownStrategyError(name string) error {
	return fmt.Errorf("%w %q: known strategies are %s", ErrUnknownStrategy, name, strings.Join(Names(), ", "))
}
