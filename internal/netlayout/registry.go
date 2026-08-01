package netlayout

import "sort"

// defaultStrategyName is the registry key Default resolves to, and the key
// Lookup falls back to for an empty name.
const defaultStrategyName = "wide"

// registry holds every Layout implementation available for selection. Both
// entries have every Layout method real (see wide.go and compact.go).
var registry = map[string]Layout{
	"wide":    wideLayout{},
	"compact": compactLayout{},
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

// wideLayout is declared in wide.go; compactLayout is declared in
// compact.go. Both have every Layout method real.
