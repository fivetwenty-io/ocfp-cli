package netlayout

import (
	"sort"

	"github.com/ocfp/ocfp-cli-go/internal/reservedip"
)

// defaultStrategyName is the registry key Default resolves to, and the key
// Lookup falls back to for an empty name.
const defaultStrategyName = "wide"

// registry holds every Layout implementation available for selection. Both
// entries are stub-safe (see Layout's doc comment): compact has only Name
// real, wide additionally has WorkloadTable, SchemeVersion, MinPrefix, and
// ValidateSubnet real (its Slots remains a stub until its owning task
// lands).
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

// wideLayout is declared in wide.go — its WorkloadTable, SchemeVersion,
// MinPrefix, and ValidateSubnet are real; Slots remains a stub pending its
// implementation.
//
// compactLayout is declared here rather than in a dedicated compact.go
// file until its real implementation lands. Every method but Name is a
// stub: stubs that can return an
// error return ErrNotImplemented; SchemeVersion and MinPrefix cannot
// (Layout declares them without an error return) and instead return their
// type's documented zero value below — never a panic.

type compactLayout struct{}

func (compactLayout) Name() string { return "compact" }

// SchemeVersion is a stub: returns "" until the real implementation
// lands ("3-compact").
func (compactLayout) SchemeVersion() string { return "" }

func (compactLayout) WorkloadTable(_ string) (reservedip.AssignmentTable, error) {
	return nil, ErrNotImplemented
}

func (compactLayout) Slots(_, _ string) (InfraSlots, error) {
	return InfraSlots{}, ErrNotImplemented
}

// MinPrefix is a stub: returns 0 until the real implementation lands (26).
func (compactLayout) MinPrefix() int { return 0 }

func (compactLayout) ValidateSubnet(_ string) error {
	return ErrNotImplemented
}

func (compactLayout) ValidateBand(_ Tier, _ string, _, _ int) error {
	return ErrNotImplemented
}
