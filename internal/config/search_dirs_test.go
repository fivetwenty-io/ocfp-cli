package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateSearchHome points every home the search depends on at a temporary
// directory and returns it.
func isolateSearchHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(ConfigPathEnv, "")

	return home
}

func TestConfigSearchDirs_DefaultsToTheConfigHome(t *testing.T) {
	home := isolateSearchHome(t)

	assert.Equal(t,
		[]string{filepath.Join(home, ".config", "ocfp"), "config", "."},
		ConfigSearchDirs())
}

func TestConfigSearchDirs_ConfigPathComesFirst(t *testing.T) {
	isolateSearchHome(t)

	dir := t.TempDir()
	t.Setenv(ConfigPathEnv, dir)

	assert.Equal(t, []string{dir, "config", "."}, ConfigSearchDirs())
}

func TestConfigSearchDirs_UsesTheLegacyHomeWhenOnlyItHasAConfig(t *testing.T) {
	home := isolateSearchHome(t)

	legacy := filepath.Join(home, ".ocfp")
	require.NoError(t, os.MkdirAll(legacy, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "config.yml"), []byte("blocs: {}\n"), 0o600))

	assert.Equal(t, []string{legacy, "config", "."}, ConfigSearchDirs())
}
