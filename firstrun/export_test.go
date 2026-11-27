package firstrun

// TrackOwned sets the hook that sees every secret buffer the package
// allocates or takes over, and returns the function that removes it. A
// test that calls it must not run in parallel, because the hook is one
// package variable.
func TrackOwned(f func(label string, b []byte)) func() {
	ownedHook = f

	return func() { ownedHook = nil }
}
