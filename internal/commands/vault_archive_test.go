package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// blocVaultLayout builds the on-disk shape getVaultInceptionPaths produces for
// a named bloc: <bloc>/vault/{data,root.key,unseal.keys}.
func blocVaultLayout(t *testing.T) map[string]string {
	t.Helper()

	vaultRoot := filepath.Join(t.TempDir(), "vault")
	dataDir := filepath.Join(vaultRoot, "data")

	require.NoError(t, os.MkdirAll(dataDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "vault.db"), []byte("secrets"), 0600))

	return map[string]string{
		"vaultDir":       dataDir,
		"rootKeyFile":    filepath.Join(vaultRoot, "root.key"),
		"unsealKeysFile": filepath.Join(vaultRoot, "unseal.keys"),
	}
}

// TestArchiveVaultState_PreservesKeyMaterial is the case that cost us a
// near-miss: a vault holding root.key and unseal.keys must be moved aside, not
// deleted. Losing those means the bloc's secrets are unrecoverable.
func TestArchiveVaultState_PreservesKeyMaterial(t *testing.T) {
	paths := blocVaultLayout(t)
	vaultRoot := filepath.Dir(paths["vaultDir"])

	require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte("root-token"), 0600))
	require.NoError(t, os.WriteFile(paths["unsealKeysFile"], []byte("unseal-key"), 0600))

	archived, err := archiveVaultState(paths, "20260720-1200", zap.NewNop().Sugar())
	require.NoError(t, err)
	require.NotEmpty(t, archived, "a vault holding key material must be archived, not deleted")

	assert.NoDirExists(t, vaultRoot, "the original location should be vacated")

	rootKey, err := os.ReadFile(filepath.Join(archived, "root.key"))
	require.NoError(t, err, "root.key must survive in the archive")
	assert.Equal(t, "root-token", string(rootKey))

	unsealKeys, err := os.ReadFile(filepath.Join(archived, "unseal.keys"))
	require.NoError(t, err, "unseal.keys must survive in the archive")
	assert.Equal(t, "unseal-key", string(unsealKeys))

	data, err := os.ReadFile(filepath.Join(archived, "data", "vault.db"))
	require.NoError(t, err, "the data directory must survive in the archive")
	assert.Equal(t, "secrets", string(data))
}

// TestArchiveVaultState_RemovesWhenNoKeyMaterial asserts the cheap path is
// preserved: a vault with nothing recoverable in it is just deleted, so we do
// not accumulate junk archives on every re-run.
func TestArchiveVaultState_RemovesWhenNoKeyMaterial(t *testing.T) {
	paths := blocVaultLayout(t)
	vaultRoot := filepath.Dir(paths["vaultDir"])

	archived, err := archiveVaultState(paths, "20260720-1200", zap.NewNop().Sugar())
	require.NoError(t, err)

	assert.Empty(t, archived, "nothing to preserve means nothing to archive")
	assert.NoDirExists(t, vaultRoot, "the vault directory should still be cleared")
}

// TestArchiveVaultState_RefusesUnscopedLayout guards the legacy and test-mode
// shapes, where vaultDir is ~/.vault and its parent is the home directory.
// Archiving there would move the user's home aside.
func TestArchiveVaultState_RefusesUnscopedLayout(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, ".vault")

	require.NoError(t, os.MkdirAll(dataDir, 0700))

	paths := map[string]string{
		"vaultDir":       dataDir,
		"rootKeyFile":    filepath.Join(home, "vault.key"),
		"unsealKeysFile": filepath.Join(home, "vault.key"),
	}

	require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte("k"), 0600))

	archived, err := archiveVaultState(paths, "20260720-1200", zap.NewNop().Sugar())
	require.NoError(t, err)

	assert.Empty(t, archived, "an unscoped layout must not be archived")
	assert.DirExists(t, home, "the parent directory must be left alone")
}
