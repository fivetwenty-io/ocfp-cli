package commands

import "os"

// homeDirFn is the function used to resolve the current user's home directory.
// Tests replace this to avoid depending on the real home directory.
var homeDirFn = os.UserHomeDir //nolint:gochecknoglobals // intentional seam for testing

// homeDir returns the current user's home directory.
// Uses os.UserHomeDir (not os.Getenv("HOME")) which is correct cross-platform
// and honours the XDG_HOME / per-user config on all supported OSes.
// Failure modes: returns error when the OS cannot determine the home directory
// (e.g., no /etc/passwd entry for the current UID on Linux).
func homeDir() (string, error) {
	return homeDirFn()
}
