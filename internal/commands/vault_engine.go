package commands

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// safeEngineEnvVar is the variable safe reads for its default engine.
const safeEngineEnvVar = "SAFE_ENGINE"

// inceptionEngineNames lists the engines safe can run, in the order safe
// prefers them when nothing pins one.
//
//nolint:gochecknoglobals // fixed list, read-only
var inceptionEngineNames = []string{"vault", "bao"}

var (
	// ErrVaultEngineNotFound reports that no usable vault engine binary exists.
	ErrVaultEngineNotFound = errors.New("vault engine not found")

	// ErrUnknownVaultEngine reports a SAFE_ENGINE value safe does not support.
	ErrUnknownVaultEngine = errors.New("unknown vault engine")
)

// inceptionEngine is the server binary safe runs for the inception vault.
type inceptionEngine struct {
	// name is the engine as safe's --engine option spells it.
	name string
	// path is the absolute path of the engine binary.
	path string
}

// inceptionTools are the binaries the inception vault runs on, as
// checkVaultInceptionPrerequisites validated them.
type inceptionTools struct {
	// safe is the absolute path of the safe whose version was checked. The
	// tmux pane runs this exact binary, never whatever safe PATH finds.
	safe string
	// engine is the server binary safe runs.
	engine inceptionEngine
}

// resolveInceptionEngine picks the engine the way safe's selectEngine does:
// SAFE_ENGINE when it is set, otherwise vault before bao. A pinned engine
// that is missing is an error, never a fallback to the other one.
//
// ocfp needs the same answer safe will reach, because it runs the engine
// itself to migrate file storage and then hands safe the result to start.
// Two different engines there would mean migrating with one and opening
// with another. ocfp also searches the fallback directories safe does not;
// the start command prefixes PATH with the engine's directory so safe finds
// the same binary.
func resolveInceptionEngine() (inceptionEngine, error) {
	preference := strings.ToLower(strings.TrimSpace(os.Getenv(safeEngineEnvVar)))

	order := inceptionEngineNames

	if preference != "" {
		if !knownInceptionEngine(preference) {
			return inceptionEngine{}, fmt.Errorf("%w %q from %s (supported: %s)",
				ErrUnknownVaultEngine, preference, safeEngineEnvVar, strings.Join(inceptionEngineNames, ", "))
		}

		order = []string{preference}
	}

	for _, name := range order {
		path, found := findInceptionCommand(name)
		if found {
			return inceptionEngine{name: name, path: path}, nil
		}
	}

	if preference != "" {
		return inceptionEngine{}, fmt.Errorf("%w: %s=%s, but %s is not installed or on PATH",
			ErrVaultEngineNotFound, safeEngineEnvVar, preference, preference)
	}

	return inceptionEngine{}, fmt.Errorf("%w: neither %s is installed or on PATH - please install one",
		ErrVaultEngineNotFound, strings.Join(inceptionEngineNames, " nor "))
}

// knownInceptionEngine reports whether safe supports the named engine.
func knownInceptionEngine(name string) bool {
	for _, known := range inceptionEngineNames {
		if name == known {
			return true
		}
	}

	return false
}
