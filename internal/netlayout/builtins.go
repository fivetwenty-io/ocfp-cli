package netlayout

import (
	"embed"
	"fmt"
	"path/filepath"
)

//go:embed strategies/*.yaml
var builtinFS embed.FS //nolint:unused // used by go:embed directive

// builtinDefinitions returns the parsed built-in strategy definitions,
// panicking if any embedded YAML is invalid (programmer error).
//nolint:unused // used by builtinLayouts
func builtinDefinitions() []Definition {
	entries, err := builtinFS.ReadDir("strategies")
	if err != nil {
		panic(fmt.Sprintf("builtins: read strategies dir: %v", err))
	}

	defs := make([]Definition, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		path := filepath.Join("strategies", entry.Name())

		data, err := builtinFS.ReadFile(path)
		if err != nil {
			panic(fmt.Sprintf("builtins: read %s: %v", entry.Name(), err))
		}

		source := "built-in:" + entry.Name()

		def, err := LoadDefinition(data, source)
		if err != nil {
			panic(fmt.Sprintf("builtins: %s: %v", entry.Name(), err))
		}

		defs = append(defs, def)
	}

	return defs
}

// builtinLayouts returns a map of strategy name to compiled layout for
// every built-in strategy. It panics if compilation fails (programmer error).
//nolint:unused // used by test
func builtinLayouts() map[string]*compiledLayout {
	layouts := make(map[string]*compiledLayout)

	for _, def := range builtinDefinitions() {
		compiled, err := Compile(def)
		if err != nil {
			panic(fmt.Sprintf("builtins: compile %q: %v", def.Name, err))
		}

		layouts[def.Name] = compiled
	}

	return layouts
}
