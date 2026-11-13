package providers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/stateguard"
)

// TestMain fails the run if a test changes the developer's own ocfp
// directories. The providers read and create bloc state, so a test that
// leaves HOME and the XDG variables alone writes under the real
// ~/.local/state/ocfp.
func TestMain(m *testing.M) {
	os.Exit(stateguard.Run(m))
}

// isolateOcfpHome points HOME, the XDG base directories, and OCFP_HOME at a
// temporary directory, so a test that reads or creates bloc state, keys, or
// config does so there and never under the developer's real directories.
// It sets environment variables, so a test that calls it cannot be
// parallel.
func isolateOcfpHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("OCFP_HOME", "")

	return home
}
