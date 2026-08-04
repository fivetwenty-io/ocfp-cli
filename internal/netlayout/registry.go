package netlayout

// defaultStrategyName is the registry key Default resolves to, and the key
// Lookup falls back to for an empty name.
const defaultStrategyName = "wide"

// Lookup resolves name to a registered built-in Layout (see Builtins). An
// empty name resolves to Default. An unrecognized name returns
// ErrUnknownStrategy wrapping name. Lookup only ever sees the three
// built-in strategies: operator BYO strategies loaded from
// network.strategyPaths are resolved through a *Catalog instead (see
// BuildCatalog and Config.ResolveReservedIPLayout), never through this
// package-level built-ins-only registry.
func Lookup(name string) (Layout, error) {
	if name == "" {
		return Default(), nil
	}

	return Builtins().Lookup(name)
}

// Default returns the default built-in Layout ("wide").
func Default() Layout {
	layout, err := Builtins().Lookup(defaultStrategyName)
	if err != nil {
		// Programmer error: the wide built-in failed to load/compile, which
		// would already have panicked inside builtinLayouts().
		panic(err)
	}

	return layout
}

// Names returns every built-in strategy name, sorted.
func Names() []string {
	return Builtins().Names()
}
