package commands

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateXDGHome points HOME at a fresh temp directory and clears every
// XDG_*_HOME/OCFP_HOME override, so ConfigHome() and OcfpHome() resolve to
// distinct, disjoint directories under the temp home rather than the
// developer's real home directory or a shared OCFP_HOME override (which
// would collapse both to the same path and defeat dual-read assertions).
func isolateXDGHome(t *testing.T) string {
	t.Helper()

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	return tmpHome
}

// writeEnvConfig writes a minimal valid bloc config file (name + provider,
// the two fields findEnvironments requires to accept a match) at dir/name.
func writeEnvConfig(t *testing.T, dir, filename, blocName, provider string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(dir, 0o750))

	content := "name: " + blocName + "\nprovider: " + provider + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o600))
}

// TestFindEnvironments_DualRead asserts findEnvironments scans both the new
// XDG config-class configs/ directory and the legacy ~/.ocfp/configs
// directory, surfacing bloc configs written to either location.
func TestFindEnvironments_DualRead(t *testing.T) {
	isolateXDGHome(t)

	newConfigsDir := filepath.Join(config.ConfigHome(), "configs")
	legacyConfigsDir := filepath.Join(config.OcfpHome(), "configs")
	require.NotEqual(t, newConfigsDir, legacyConfigsDir, "test setup must isolate ConfigHome from OcfpHome")

	writeEnvConfig(t, newConfigsDir, "new-bloc.yml", "new-bloc", "aws")
	writeEnvConfig(t, legacyConfigsDir, "legacy-bloc.yml", "legacy-bloc", "pve")

	envs := findEnvironments()

	names := make([]string, 0, len(envs))
	for _, e := range envs {
		names = append(names, e.Name)
	}

	sort.Strings(names)

	assert.Contains(t, names, "new-bloc")
	assert.Contains(t, names, "legacy-bloc")
}

// TestFindEnvironments_LegacyFlatLayout asserts a config file placed
// directly under the legacy ~/.ocfp root (the pre-configs/-subdirectory
// flat layout) is still found.
func TestFindEnvironments_LegacyFlatLayout(t *testing.T) {
	isolateXDGHome(t)

	writeEnvConfig(t, config.OcfpHome(), "flat-bloc.yml", "flat-bloc", "stackit")

	envs := findEnvironments()

	found := false

	for _, e := range envs {
		if e.Name == "flat-bloc" {
			found = true
		}
	}

	assert.True(t, found, "expected flat-bloc to be found under the legacy ~/.ocfp root")
}

// TestConfigSearchPaths_DedupesUnderOCFPHomeOverride asserts that when
// OCFP_HOME is set (collapsing ConfigHome() and OcfpHome() to the same
// directory), configSearchPaths does not return duplicate entries -- each
// candidate directory is scanned exactly once.
func TestConfigSearchPaths_DedupesUnderOCFPHomeOverride(t *testing.T) {
	override := t.TempDir()
	t.Setenv("OCFP_HOME", override)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	paths := configSearchPaths()

	seen := make(map[string]int, len(paths))
	for _, p := range paths {
		seen[p]++
	}

	for p, count := range seen {
		assert.Equal(t, 1, count, "path %q appeared %d times, want 1", p, count)
	}

	assert.Contains(t, paths, filepath.Join(override, "configs"))
	assert.Contains(t, paths, override)
}
