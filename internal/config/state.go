package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

// StateFile represents CLI-managed state stored separately from user config.
// This file is owned by the CLI and should not be hand-edited.
type StateFile struct {
	CurrentBloc string                `yaml:"current_bloc,omitempty"`
	ConfigFile  string                `yaml:"config_file,omitempty"`
	Blocs       map[string]*BlocState `yaml:"blocs,omitempty"`
}

// BlocState stores per-bloc machine-managed state such as SSH keys.
type BlocState struct {
	Keys map[string]string `yaml:"keys,omitempty"`
}

// stateFileMode is the file permission for state.yml (owner read/write only).
const stateFileMode os.FileMode = 0o600

// stateDirMode is the file permission for the state directory.
const stateDirMode os.FileMode = 0o750

// StateFilePath returns the path to the CLI state file. Resolves under
// StateHome(), with a dual-read fallback to the pre-migration
// ~/.ocfp/state.yml when only that exists.
func StateFilePath() string {
	stateHome := StateHome()
	if stateHome == "" {
		return ""
	}

	newPath := filepath.Join(stateHome, "state.yml")
	legacyPath := filepath.Join(OcfpHome(), "state.yml")

	path, _ := ResolveExisting(newPath, legacyPath)

	return path
}

// LoadState reads and unmarshals the state file.
// Returns an empty StateFile if the file does not exist.
// On first load, migrates legacy keys from config.yml if state.yml is missing.
func LoadState() (*StateFile, error) {
	statePath := StateFilePath()
	if statePath == "" {
		return &StateFile{}, nil
	}

	data, err := os.ReadFile(statePath) // #nosec G304 -- path is from trusted config
	if err != nil {
		if os.IsNotExist(err) {
			return migrateStateFromConfig()
		}

		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	state := &StateFile{}

	err = yaml.Unmarshal(data, state)
	if err != nil {
		return nil, fmt.Errorf("failed to parse state file: %w", err)
	}

	return state, nil
}

// SaveState marshals and writes the state file with 2-space indentation.
func SaveState(state *StateFile) error {
	testSafetyGuard("SaveState")

	statePath := StateFilePath()
	if statePath == "" {
		return fmt.Errorf("cannot determine state file path: %w", ErrOcfpHomeNotFound)
	}

	// Ensure directory exists
	dir := filepath.Dir(statePath)

	err := os.MkdirAll(dir, stateDirMode) // #nosec -- path is from trusted config
	if err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	// Marshal with 2-space indentation
	data, err := marshalYAMLWithIndent(state, 2) //nolint:mnd // 2-space indent is intentional
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	err = os.WriteFile(statePath, data, stateFileMode) // #nosec -- path is from trusted config
	if err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	return nil
}

// marshalYAMLWithIndent marshals a value to YAML with a specified indentation.
func marshalYAMLWithIndent(v interface{}, indent int) ([]byte, error) {
	data, err := yaml.MarshalWithOptions(v, yaml.Indent(indent))
	if err != nil {
		return nil, fmt.Errorf("yaml marshal: %w", err)
	}

	return data, nil
}

// GetCurrentBloc returns the current bloc name from the state file.
func GetCurrentBloc() (string, error) {
	state, err := LoadState()
	if err != nil {
		return "", err
	}

	return state.CurrentBloc, nil
}

// SetCurrentBloc updates the current bloc and config file path in state.
//
// The load-modify-save runs under the state lock so a bloc's entry is not lost
// to a concurrently bootstrapping sibling.
func SetCurrentBloc(blocName, configFile string) error {
	return withStateLock(func() error {
		state, err := LoadState()
		if err != nil {
			return fmt.Errorf("failed to load state: %w", err)
		}

		state.CurrentBloc = blocName
		state.ConfigFile = configFile

		return SaveState(state)
	})
}

// SaveBlocKeys persists SSH keys for a bloc in the state file.
//
// state.yml is shared by every bloc, so the load-modify-save runs under the
// state lock. Without it, two blocs generating SSH keys at the same moment
// interleave and one bloc's keys are silently dropped.
func SaveBlocKeys(blocName string, keys map[string]string) error {
	return withStateLock(func() error {
		state, err := LoadState()
		if err != nil {
			return fmt.Errorf("failed to load state: %w", err)
		}

		if state.Blocs == nil {
			state.Blocs = make(map[string]*BlocState)
		}

		blocState, ok := state.Blocs[blocName]
		if !ok {
			blocState = &BlocState{} //nolint:exhaustruct // Keys is assigned immediately below
			state.Blocs[blocName] = blocState
		}

		blocState.Keys = keys

		return SaveState(state)
	})
}

// GetBlocKeys returns the SSH keys for a bloc from the state file.
func GetBlocKeys(blocName string) (map[string]string, error) {
	state, err := LoadState()
	if err != nil {
		return nil, err
	}

	if state.Blocs == nil {
		return nil, nil
	}

	blocState, ok := state.Blocs[blocName]
	if !ok || blocState.Keys == nil {
		return nil, nil
	}

	return blocState.Keys, nil
}

// migrateStateFromConfig performs a one-time migration of CLI state
// from config.yml (old format) to state.yml.
func migrateStateFromConfig() (*StateFile, error) {
	state := &StateFile{}

	configPath := defaultConfigPath()

	data, err := os.ReadFile(configPath) // #nosec G304 -- path is from trusted config
	if err != nil {
		// No config.yml either -- return empty state
		return state, nil //nolint:nilerr // intentionally ignoring missing config file
	}

	// Parse as generic map to find legacy keys
	var raw map[string]interface{}

	err = yaml.Unmarshal(data, &raw)
	if err != nil {
		// Can't parse config.yml -- return empty state
		return state, nil //nolint:nilerr // intentionally ignoring unparseable config file
	}

	migrated := false

	if bloc, ok := raw["bloc"].(string); ok && bloc != "" {
		state.CurrentBloc = bloc
		migrated = true
	} else if env, ok := raw["current_environment"].(string); ok && env != "" {
		state.CurrentBloc = env
		migrated = true
	}

	if cf, ok := raw["config_file"].(string); ok && cf != "" {
		state.ConfigFile = cf
		migrated = true
	}

	// Write state.yml if we found anything to migrate
	if migrated {
		// Best-effort save; if it fails, we still return the in-memory state
		_ = SaveState(state)
	}

	return state, nil
}
