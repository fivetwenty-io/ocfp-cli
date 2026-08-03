package netlayout

import "sort"

// defaultStrategyName is the registry key Default resolves to, and the key
// Lookup falls back to for an empty name.
const defaultStrategyName = "wide"

// registry holds every Layout available for selection: one compiled layout
// per built-in strategy definition (see builtins.go and strategies/*.yaml).
var registry = builtinRegistry()

// builtinRegistry compiles the embedded strategy definitions into the
// name-keyed Layout map registry is built from. Compilation failure is a
// programmer error in an embedded YAML file and panics at package
// initialization rather than at first Lookup.
func builtinRegistry() map[string]Layout {
	compiled := builtinLayouts()

	layouts := make(map[string]Layout, len(compiled))
	for name, layout := range compiled {
		layouts[name] = layout
	}

	return layouts
}

// Lookup resolves name to a registered Layout. An empty name resolves to
// Default. An unrecognized name returns ErrUnknownStrategy wrapping name.
func Lookup(name string) (Layout, error) {
	if name == "" {
		return Default(), nil
	}

	layout, ok := registry[name]
	if !ok {
		return nil, unknownStrategyError(name)
	}

	return layout, nil
}

// Default returns the default Layout ("wide").
func Default() Layout {
	return registry[defaultStrategyName]
}

// Names returns every registered strategy name, sorted.
func Names() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}
