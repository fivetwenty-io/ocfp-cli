package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderRaftMigrationConfig_Golden(t *testing.T) {
	cfg, err := renderRaftMigrationConfig(
		"/home/me/.local/share/ocfp/ocfp-lab-drgao/vault/data",
		"/home/me/.local/share/ocfp/ocfp-lab-drgao/vault/data.raft-migrating",
		"19534",
	)
	require.NoError(t, err)

	assert.Equal(t, `storage_source "file" {
  path = "/home/me/.local/share/ocfp/ocfp-lab-drgao/vault/data"
}

storage_destination "raft" {
  path    = "/home/me/.local/share/ocfp/ocfp-lab-drgao/vault/data.raft-migrating"
  node_id = "safe-local"
}

cluster_addr = "https://127.0.0.1:19534"
`, cfg)
}

func TestRenderRaftMigrationConfig_QuotesPaths(t *testing.T) {
	cfg, err := renderRaftMigrationConfig(`/tmp/it's a "bloc"\x/data`, `/tmp/it's a "bloc"\x/data.raft-migrating`, "19534")
	require.NoError(t, err)

	assert.Contains(t, cfg, `path = "/tmp/it's a \"bloc\"\\x/data"`)
	assert.Contains(t, cfg, `path    = "/tmp/it's a \"bloc\"\\x/data.raft-migrating"`)
}

// HCL reads ${ and %{ inside a string as a template, and a control character
// has no safe spelling in every engine's parser, so such a path is refused
// rather than rendered into a config that might point somewhere else.
func TestRenderRaftMigrationConfig_RefusesTemplatesAndControlCharacters(t *testing.T) {
	for _, path := range []string{"/tmp/${HOME}/data", "/tmp/%{x}/data", "/tmp/a\nb/data", "/tmp/a\tb/data"} {
		_, err := renderRaftMigrationConfig(path, "/tmp/staging", "19534")
		require.ErrorIs(t, err, ErrMigrationPathUnsafe, path)

		_, err = renderRaftMigrationConfig("/tmp/data", path, "19534")
		require.ErrorIs(t, err, ErrMigrationPathUnsafe, path)
	}
}

// The node ID is a contract with safe: a migrated raft store records it, and
// safe starts the node under its own constant, so the two must match or the
// single node will not find itself in its own configuration.
func TestSafeLocalRaftNodeIDMatchesSafe(t *testing.T) {
	assert.Equal(t, "safe-local", safeLocalRaftNodeID)
}
