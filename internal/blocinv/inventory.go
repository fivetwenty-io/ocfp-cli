// Package blocinv builds the inventory of blocs the CLI knows about. It
// merges the bloc definitions in every config file the CLI searches with the
// per-bloc state directories into one entry per bloc. It reads local files
// only: it makes no provider calls and prints nothing.
package blocinv

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
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
	HasState    bool
	Current     bool      // the bloc the CLI resolves as current
	Broken      bool      // the state file exists but cannot be loaded
	BrokenError string    // why it cannot be loaded, never the file's contents
	ConfigFile  string    // the first file that defines the bloc
	ConfigFiles []string  // every file that defines the bloc, in search order
	StatePath   string    // the state file, empty when the bloc has none
	UpdatedAt   time.Time // zero when there is no state

	// cfg is the first definition of the bloc as written in config. Summarize
	// reads it through an allowlist and nothing else exposes it.
	cfg *config.Config
}

// Options tells Collect where to look.
type Options struct {
	// PrimaryConfig is the config file the CLI itself loads: the --config
	// file, else config.yml in the config home. It is read before every
	// other file, so it supplies the source of any bloc it defines.
	PrimaryConfig string
	// ConfigFiles are explicit config files, read after PrimaryConfig and
	// before ConfigDirs.
	ConfigFiles []string
	// ConfigDirs are directories scanned for *.yml and *.yaml files, in
	// priority order.
	ConfigDirs []string
	// StateHomes are the directories that hold <bloc>/state/<bloc>.json, in
	// priority order. When several hold the same bloc, the first wins.
	StateHomes []string
	// Current is the name of the current bloc, resolved by the caller. The
	// inventory cannot resolve it itself because the resolution order lives
	// in a package that imports this one. Empty marks no entry.
	Current string
}

// Collect merges every source into one entry per bloc, sorted by name.
func Collect(opts Options) ([]Entry, error) {
	byName := make(map[string]*Entry)

	for _, path := range configFilePaths(opts) {
		mergeConfigFile(byName, path)
	}

	err := mergeStateHomes(byName, opts.StateHomes)
	if err != nil {
		return nil, err
	}

	entries := make([]Entry, 0, len(byName))
	for _, entry := range byName {
		entry.Current = opts.Current != "" && entry.Name == opts.Current
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

// configFilePaths lists the config files to read: the primary config first,
// then the explicit files, then each directory's *.yml and *.yaml matches. A
// file reachable more than once, by two names or through a symlink, is listed
// once under the first name it was reached by, which is why the primary comes
// first: a config.yml that links to config.pve.yml stays config.yml.
func configFilePaths(opts Options) []string {
	seen := make(map[string]bool)
	paths := make([]string, 0, len(opts.ConfigFiles)+1)

	add := func(path string) {
		if path == "" {
			return
		}

		key := fileIdentity(path)
		if seen[key] {
			return
		}

		seen[key] = true

		paths = append(paths, path)
	}

	add(opts.PrimaryConfig)

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

// fileIdentity names the file a path leads to, with symlinks resolved, so two
// paths to one file compare equal. A path that cannot be resolved, such as one
// that does not exist, is identified by its absolute form.
func fileIdentity(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs
	}

	return resolved
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
		logger.Debugf("bloc inventory: skipping %s: %s", path, SafeMessage(err, path))

		return
	}

	var file config.ConfigFile

	err = yaml.Unmarshal(data, &file)
	if err != nil {
		logger.Debugf("bloc inventory: skipping blocs map in %s: %s", path, SafeMessage(err, path))
	}

	for name, cfg := range file.Blocs {
		mergeBloc(byName, name, cfg, path)
	}

	var flat config.Config

	err = yaml.Unmarshal(data, &flat)
	if err != nil {
		logger.Debugf("bloc inventory: skipping flat config in %s: %s", path, SafeMessage(err, path))

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

	if entry.cfg == nil {
		entry.cfg = cfg
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

// mergeStateHomes adds the blocs found under each state home. A directory
// counts as a bloc only when <dir>/state/<dir>.json exists, which leaves out
// the checkpoints, logs, and state entries and the CLI's own state files.
func mergeStateHomes(byName map[string]*Entry, homes []string) error {
	seen := make(map[string]bool, len(homes))

	for _, home := range homes {
		if home == "" || seen[filepath.Clean(home)] {
			continue
		}

		seen[filepath.Clean(home)] = true

		dirEntries, err := os.ReadDir(home)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}

			return fmt.Errorf("reading state home %s: %w", home, err)
		}

		for _, dirEntry := range dirEntries {
			name := dirEntry.Name()
			statePath := filepath.Join(home, name, "state", name+".json")

			info, statErr := os.Stat(statePath)
			if statErr != nil || !info.Mode().IsRegular() {
				continue
			}

			mergeState(byName, name, statePath)
		}
	}

	return nil
}

// mergeState records the state file of one bloc. A bloc that an earlier state
// home already supplied keeps that state.
func mergeState(byName map[string]*Entry, name, statePath string) {
	entry, ok := byName[name]
	if !ok {
		entry = &Entry{Name: name}
		byName[name] = entry
	}

	if entry.HasState {
		return
	}

	entry.HasState = true
	entry.StatePath = statePath

	st, err := loadState(name, statePath)
	if err != nil {
		entry.Broken = true
		entry.BrokenError = describeStateError(err)

		return
	}

	entry.UpdatedAt = st.UpdatedAt

	if entry.Provider == "" {
		entry.Provider = st.Provider
	}

	if entry.Region == "" {
		entry.Region = st.Region
	}
}

// Source says where the entry's data came from: config, state, both, or
// broken when the state file cannot be loaded.
func (e Entry) Source() string {
	switch {
	case e.Broken:
		return "broken"
	case e.HasConfig && e.HasState:
		return "both"
	case e.HasState:
		return "state"
	default:
		return "config"
	}
}

// loadState reads one bloc's state file through the state manager so the
// inventory parses state exactly as every other command does.
func loadState(name, statePath string) (*state.State, error) {
	// Manager.Load answers a missing file with a fresh empty state, which
	// would hide a file that vanished after the inventory found it.
	_, err := os.Stat(statePath)
	if err != nil {
		return nil, fmt.Errorf("checking state file: %w", err)
	}

	manager, err := state.NewManager(filepath.Dir(statePath))
	if err != nil {
		return nil, fmt.Errorf("opening state directory: %w", err)
	}

	st, err := manager.Load(name)
	if err != nil {
		logger.Debugf("bloc inventory: loading %s: %s", statePath, SafeMessage(err, statePath))

		return nil, fmt.Errorf("loading state: %w", err)
	}

	return st, nil
}

// describeStateError turns a state load failure into a fixed phrase. The
// parser's own message can quote the offending line of the file, and a state
// file can hold secrets, so the inventory never repeats it.
func describeStateError(err error) string {
	var pathErr *fs.PathError

	if errors.As(err, &pathErr) {
		return "state file cannot be read"
	}

	return "state file does not parse as JSON or YAML"
}
