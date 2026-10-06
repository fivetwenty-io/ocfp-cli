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

// TestArchiveVaultState_KeepsDataWithoutKeyMaterial covers a vault whose key
// files are both gone. The data alone cannot be opened, but a person with the
// keys from elsewhere still can, so the data must be archived, never deleted.
func TestArchiveVaultState_KeepsDataWithoutKeyMaterial(t *testing.T) {
	paths := blocVaultLayout(t)
	vaultRoot := filepath.Dir(paths["vaultDir"])

	archived, err := archiveVaultState(paths, "20260720-1200", zap.NewNop().Sugar())
	require.NoError(t, err)

	assert.Equal(t, vaultRoot+VaultArchiveSuffix+"20260720-1200", archived)
	assert.NoDirExists(t, vaultRoot, "the original location should be vacated")

	data, err := os.ReadFile(filepath.Join(archived, "data", "vault.db"))
	require.NoError(t, err, "the data directory must survive in the archive")
	assert.Equal(t, "secrets", string(data))
}

// TestArchiveVaultState_NothingToArchive asserts a bloc with no vault
// directory at all archives nothing and reports no error.
func TestArchiveVaultState_NothingToArchive(t *testing.T) {
	paths := blocVaultLayout(t)
	vaultRoot := filepath.Dir(paths["vaultDir"])

	require.NoError(t, os.RemoveAll(vaultRoot))

	archived, err := archiveVaultState(paths, "20260720-1200", zap.NewNop().Sugar())
	require.NoError(t, err)
	assert.Empty(t, archived)
	assert.NoDirExists(t, vaultRoot+VaultArchiveSuffix+"20260720-1200")
}

// TestArchiveVaultState_RefusesToOverwriteAnArchive guards two archives in
// the same second: the second must fail and leave both directories intact
// rather than replace the first.
func TestArchiveVaultState_RefusesToOverwriteAnArchive(t *testing.T) {
	paths := blocVaultLayout(t)
	vaultRoot := filepath.Dir(paths["vaultDir"])
	existing := vaultRoot + VaultArchiveSuffix + "20260720-1200"

	require.NoError(t, os.MkdirAll(existing, 0o700))

	_, err := archiveVaultState(paths, "20260720-1200", zap.NewNop().Sugar())
	require.Error(t, err)

	assert.FileExists(t, filepath.Join(paths["vaultDir"], "vault.db"), "the vault must stay where it was")
	assert.DirExists(t, existing)
}

// TestArchiveVaultState_RenamesDataInUnscopedLayout guards the legacy and
// test-mode shapes, where vaultDir is ~/.vault and its parent is the home
// directory. Archiving the parent there would move the user's home aside, so
// only the data directory is renamed, and the key files stay where they are.
func TestArchiveVaultState_RenamesDataInUnscopedLayout(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, ".vault")

	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "core"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "core", "_keyring"), []byte("keyring"), 0600))

	paths := map[string]string{
		"vaultDir":       dataDir,
		"rootKeyFile":    filepath.Join(home, "vault.key"),
		"unsealKeysFile": filepath.Join(home, "vault.key"),
	}

	require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte("k"), 0600))

	archived, err := archiveVaultState(paths, "20260720-1200", zap.NewNop().Sugar())
	require.NoError(t, err)

	assert.Equal(t, dataDir+VaultArchiveSuffix+"20260720-1200", archived)
	assert.DirExists(t, home, "the parent directory must be left alone")
	assert.NoDirExists(t, dataDir)

	keyring, err := os.ReadFile(filepath.Join(archived, "core", "_keyring"))
	require.NoError(t, err, "the data must survive in the archive")
	assert.Equal(t, "keyring", string(keyring))

	key, err := os.ReadFile(paths["rootKeyFile"])
	require.NoError(t, err, "key files must stay where they are")
	assert.Equal(t, "k", string(key))
}
