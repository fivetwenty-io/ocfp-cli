package netlayout

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// subnetStrategyTriple mirrors internal/bootstrap's canonical "ocfp-triple"
// subnet-strategy value. It cannot be imported from that package (bootstrap
// already imports netlayout), so the literal is duplicated here; both sides
// document the coupling.
const subnetStrategyTriple = "ocfp-triple"

// Catalog is a name-keyed set of Layouts available for selection: the
// built-in strategies (wide, compact, spanning), optionally extended with
// operator-supplied BYO definitions loaded from network.strategyPaths.
// Callers obtain one via Builtins or BuildCatalog and never construct a
// Catalog directly.
type Catalog struct {
	layouts map[string]Layout
	// schemes maps scheme_version to the name of the strategy that claimed
	// it, so add can reject a second strategy (built-in or BYO) reusing an
	// already-claimed scheme_version.
	schemes map[string]string
}

// Builtins returns a fresh Catalog containing only the embedded built-in
// strategies (wide, compact, spanning). Each call builds a new Catalog from
// builtinLayouts, so callers can never observe or cause mutation of a
// shared instance.
func Builtins() *Catalog {
	cat := &Catalog{
		layouts: make(map[string]Layout),
		schemes: make(map[string]string),
	}

	for name, layout := range builtinLayouts() {
		cat.layouts[name] = layout
		cat.schemes[layout.SchemeVersion()] = name
	}

	return cat
}

// BuildCatalog returns a Catalog containing the built-in strategies plus
// every strategy definition loaded from paths. A relative entry in paths is
// resolved against baseDir (the directory containing the config file that
// declared network.strategyPaths). Each path may name a single strategy
// file or a directory, in which case every "*.yml"/"*.yaml" file inside it
// (sorted) is loaded. A BYO strategy whose name or scheme_version collides
// with an already-registered strategy (built-in or earlier BYO file) fails
// the whole load with ErrStrategyShadowed or ErrSchemeCollision — a silent
// shadow would let one operator-facing strategy name resolve to different
// definitions depending on load order.
func BuildCatalog(paths []string, baseDir string) (*Catalog, error) {
	cat := Builtins()

	for _, p := range paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(baseDir, p)
		}

		files, err := strategyFiles(p)
		if err != nil {
			return nil, fmt.Errorf("netlayout: strategy path %q: %w", p, err)
		}

		for _, f := range files {
			data, err := os.ReadFile(f) //nolint:gosec // f is an operator-configured strategy path, not user input
			if err != nil {
				return nil, fmt.Errorf("netlayout: read strategy file %q: %w", f, err)
			}

			def, err := LoadDefinition(data, f)
			if err != nil {
				return nil, err
			}

			layout, err := Compile(def)
			if err != nil {
				return nil, err
			}

			if err := cat.add(layout); err != nil {
				return nil, err
			}
		}
	}

	return cat, nil
}

// strategyFiles resolves p to the list of strategy definition files it
// names: p itself when it is a file, or every "*.yml"/"*.yaml" entry inside
// it (sorted, non-recursive) when it is a directory.
func strategyFiles(p string) ([]string, error) {
	info, err := os.Stat(p)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() {
		return []string{p}, nil
	}

	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}

	files := make([]string, 0, len(entries))

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		ext := filepath.Ext(entry.Name())
		if ext != ".yml" && ext != ".yaml" {
			continue
		}

		files = append(files, filepath.Join(p, entry.Name()))
	}

	sort.Strings(files)

	return files, nil
}

// add registers layout under its own name, failing if that name or its
// scheme_version is already claimed by a layout already in c.
func (c *Catalog) add(layout Layout) error {
	name := layout.Name()

	if _, exists := c.layouts[name]; exists {
		return strategyShadowedError(name, layoutSource(layout))
	}

	scheme := layout.SchemeVersion()

	if existingName, exists := c.schemes[scheme]; exists {
		return schemeCollisionError(scheme, name, existingName)
	}

	c.layouts[name] = layout
	c.schemes[scheme] = name

	return nil
}

// layoutSource returns the definition source (file path, or "built-in:...")
// recorded for layout, for use in error messages. Every real Layout this
// package produces is a *compiledLayout; a defensive "" covers any future
// implementation that is not.
func layoutSource(layout Layout) string {
	compiled, ok := layout.(*compiledLayout)
	if !ok {
		return ""
	}

	return compiled.def.Source
}

// Lookup resolves name to a Layout registered in this catalog. Unlike the
// package-level Lookup, an empty name is an error: callers resolve the
// provider/subnet-strategy default (see DefaultNameFor) to a concrete name
// before calling Lookup, so an empty name reaching here is a caller bug.
func (c *Catalog) Lookup(name string) (Layout, error) {
	layout, ok := c.layouts[name]
	if !ok {
		return nil, c.unknownError(name)
	}

	return layout, nil
}

// unknownError wraps ErrUnknownStrategy with name and this catalog's own
// registered strategy names (built-ins plus any BYO additions), so a
// catalog extended with operator strategies reports them alongside the
// built-ins rather than the package-level built-in-only list.
func (c *Catalog) unknownError(name string) error {
	return fmt.Errorf("%w %q: known strategies are %s", ErrUnknownStrategy, name, strings.Join(c.Names(), ", "))
}

// Names returns every strategy name registered in this catalog, sorted.
func (c *Catalog) Names() []string {
	names := make([]string, 0, len(c.layouts))
	for name := range c.layouts {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// DefaultNameFor returns the strategy name an empty network.strategy
// resolves to for the given provider and subnet strategy. AWS always
// spans (its VPC subnets are separate address spaces, like PVE's
// per-workload-subnet /22s, but AWS has no wide/compact-sized single
// subnet to colocate onto). STACKIT shares one address space across its
// workload subnets under the triple layout, so only its triple subnet
// strategy needs spanning; the single-subnet layout colocates like PVE's
// default. Every other provider (including PVE and the empty/unset
// default) colocates onto "wide".
func DefaultNameFor(provider, subnetStrategy string) string {
	switch provider {
	case "aws":
		return "spanning"
	case "stackit":
		if subnetStrategy == subnetStrategyTriple {
			return "spanning"
		}

		return defaultStrategyName
	default:
		return defaultStrategyName
	}
}
