package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// appDirName is the per-application subdirectory ocfp creates inside each
// XDG base directory (e.g. $XDG_CONFIG_HOME/ocfp).
const appDirName = "ocfp"

// legacyWarnOnce gates the dual-read legacy-fallback warning so it
// prints at most once per process, regardless of how many config/state/data
// lookups fall back to the pre-XDG ~/.ocfp layout.
var legacyWarnOnce sync.Once

// configHome returns ocfp's XDG config-class base directory: $XDG_CONFIG_HOME/ocfp
// if XDG_CONFIG_HOME is set (used verbatim as the base, joined with "ocfp"),
// else the literal default ~/.config/ocfp.
//
// Literal path on ALL platforms, including darwin. Do NOT use
// os.UserConfigDir() here — it resolves to ~/Library/Application Support on
// darwin, not the XDG-style layout the user asked for.
func configHome() string {
	return xdgPath("XDG_CONFIG_HOME", ".config")
}

// stateHome returns ocfp's XDG state-class base directory: $XDG_STATE_HOME/ocfp
// if XDG_STATE_HOME is set, else the literal default ~/.local/state/ocfp.
//
// Literal path on ALL platforms; no os.UserCacheDir()/GOOS branching.
func stateHome() string {
	return xdgPath("XDG_STATE_HOME", ".local", "state")
}

// dataHome returns ocfp's XDG data-class base directory: $XDG_DATA_HOME/ocfp
// if XDG_DATA_HOME is set, else the literal default ~/.local/share/ocfp.
//
// Literal path on ALL platforms; no os.UserCacheDir()/GOOS branching.
func dataHome() string {
	return xdgPath("XDG_DATA_HOME", ".local", "share")
}

// xdgPath resolves an XDG base-directory class to ocfp's per-class directory.
// If envVar is set, its value is used verbatim as the base (joined with
// appDirName). Otherwise the literal defaultParts under the user's home
// directory are used (joined with appDirName). defaultParts are relative
// path segments, e.g. ".config" or ".local", "state".
//
// If the home directory cannot be determined (os.UserHomeDir error), an
// empty string is returned; callers treat that the same as any other
// unresolved path.
func xdgPath(envVar string, defaultParts ...string) string {
	if v := os.Getenv(envVar); v != "" {
		return filepath.Join(v, appDirName)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	parts := make([]string, 0, len(defaultParts)+2)
	parts = append(parts, home)
	parts = append(parts, defaultParts...)
	parts = append(parts, appDirName)

	return filepath.Join(parts...)
}

// resolveExisting implements the dual-read migration path: callers
// pass the new XDG-class path and the equivalent legacy ~/.ocfp-rooted path
// for the same file or directory.
//
//   - newPath exists: returns (newPath, false).
//   - only legacyPath exists: returns (legacyPath, true) and prints a
//     one-time-per-process deprecation warning to stderr (gated by
//     legacyWarnOnce), regardless of how many distinct path pairs fall back
//     during the run.
//   - neither exists: returns (newPath, false) — the non-existent new
//     location, so callers writing a fresh file/dir land on the new layout.
//
// Stat errors other than "not exist" (e.g. permission denied) are treated
// the same as "does not exist": resolveExisting cannot safely assume
// existence it could not verify.
func resolveExisting(newPath, legacyPath string) (path string, usedLegacy bool) {
	if _, err := os.Stat(newPath); err == nil {
		return newPath, false
	}

	if _, err := os.Stat(legacyPath); err == nil {
		legacyWarnOnce.Do(func() {
			fmt.Fprintf(os.Stderr,
				"ocfp: using legacy path %s; new location is %s (run `ocfp` again after migrating to silence this warning)\n",
				legacyPath, newPath)
		})

		return legacyPath, true
	}

	return newPath, false
}
