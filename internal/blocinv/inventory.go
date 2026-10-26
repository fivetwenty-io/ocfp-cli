// Package blocinv builds the inventory of blocs the CLI knows about. It
// merges the bloc definitions in every config file the CLI searches into one
// entry per bloc. It reads local files only: it makes no provider calls and
// prints nothing.
package blocinv

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/goccy/go-yaml"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// cliStateFileNames are the CLI-owned files that live beside config files
// when OCFP_HOME collapses the config and state homes into one directory.
// They carry a blocs: map of machine-managed keys, not bloc definitions.
var cliStateFileNames = map[string]bool{
	"state.yml":  true,
	"state.yaml": true,
}

// Entry is one bloc the CLI knows about.
type Entry struct {
	Name        string
	Provider    string
	Region      string
	HasConfig   bool
	ConfigFile  string   // the first file that defines the bloc
	ConfigFiles []string // every file that defines the bloc, in search order
}

// Options tells Collect where to look.
type Options struct {
	// ConfigFiles are explicit config files, read before ConfigDirs.
	ConfigFiles []string
	// ConfigDirs are directories scanned for *.yml and *.yaml files, in
	// priority order.
	ConfigDirs []string
}

// Collect merges every source into one entry per bloc, sorted by name.
func Collect(opts Options) ([]Entry, error) {
	byName := make(map[string]*Entry)

	for _, path := range configFilePaths(opts) {
		mergeConfigFile(byName, path)
	}

	entries := make([]Entry, 0, len(byName))
	for _, entry := range byName {
		entries = append(entries, *entry)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	return entries, nil
}

// Find returns the entry called name.
func Find(entries []Entry, name string) (Entry, bool) {
	if name == "" {
		return Entry{}, false
	}

	for _, entry := range entries {
		if entry.Name == name {
			return entry, true
		}
	}

	return Entry{}, false
}

// configFilePaths lists the config files to read, explicit files first, then
// each directory's *.yml and *.yaml matches. A file reachable more than once
// is listed once, at its first position.
func configFilePaths(opts Options) []string {
	seen := make(map[string]bool)
	paths := make([]string, 0, len(opts.ConfigFiles))

	add := func(path string) {
		if path == "" {
			return
		}

		key := path
		if abs, err := filepath.Abs(path); err == nil {
			key = abs
		}

		if seen[key] {
			return
		}

		seen[key] = true

		paths = append(paths, path)
	}

	for _, path := range opts.ConfigFiles {
		add(path)
	}

	for _, dir := range opts.ConfigDirs {
		for _, pattern := range []string{"*.yml", "*.yaml"} {
			matches, _ := filepath.Glob(filepath.Join(dir, pattern))
			for _, match := range matches {
				add(match)
			}
		}
	}

	return paths
}

// mergeConfigFile adds the blocs one config file defines. The file may hold a
// blocs: map, a flat bloc config with a top-level name and provider, or both.
// A file that cannot be read or parsed is skipped with a debug log.
func mergeConfigFile(byName map[string]*Entry, path string) {
	if cliStateFileNames[filepath.Base(path)] {
		return
	}

	data, err := os.ReadFile(path) // #nosec G304 -- path comes from the trusted config search paths
	if err != nil {
		logger.Debugf("bloc inventory: skipping %s: %v", path, err)

		return
	}

	var file config.ConfigFile

	err = yaml.Unmarshal(data, &file)
	if err != nil {
		logger.Debugf("bloc inventory: skipping blocs map in %s: %v", path, err)
	}

	for name, cfg := range file.Blocs {
		mergeBloc(byName, name, cfg, path)
	}

	var flat config.Config

	err = yaml.Unmarshal(data, &flat)
	if err != nil {
		logger.Debugf("bloc inventory: skipping flat config in %s: %v", path, err)

		return
	}

	if flat.Name != "" && flat.Provider != "" {
		mergeBloc(byName, flat.Name, &flat, path)
	}
}

// mergeBloc records that path defines the named bloc. The first definition
// supplies the provider and region, and later ones only fill gaps.
func mergeBloc(byName map[string]*Entry, name string, cfg *config.Config, path string) {
	if name == "" {
		return
	}

	entry, ok := byName[name]
	if !ok {
		entry = &Entry{Name: name, HasConfig: true, ConfigFile: path}
		byName[name] = entry
	}

	for _, existing := range entry.ConfigFiles {
		if existing == path {
			return
		}
	}

	entry.ConfigFiles = append(entry.ConfigFiles, path)

	if cfg == nil {
		return
	}

	if entry.Provider == "" {
		entry.Provider = providerOf(cfg)
	}

	if entry.Region == "" {
		entry.Region = cfg.Region
	}
}

// providerOf returns the provider a config names, falling back to its iaas.
func providerOf(cfg *config.Config) string {
	if cfg.Provider != "" {
		return cfg.Provider
	}

	return cfg.IaaS
}
