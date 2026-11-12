//go:build unix

package safedir

// ResolveAsWithHooks is ResolveAs with afterWalk called once the walk has
// checked the parents, and afterEval called once they have been resolved
// again.
func ResolveAsWithHooks(path string, euid int, afterWalk, afterEval func()) (string, error) {
	return resolveAs(path, euid, resolveHooks{afterWalk: afterWalk, afterEval: afterEval})
}
