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

// rename(2) would quietly replace an empty directory or overwrite a file at
// the destination, so an aside path that is already taken must be refused
// and both entries left exactly as they were.
func TestMoveAsideIfPresent_RefusesAnExistingDestination(t *testing.T) {
	for name, makeAside := range map[string]func(t *testing.T, aside string){
		"file": func(t *testing.T, aside string) {
			t.Helper()
			require.NoError(t, os.WriteFile(aside, []byte("older-key\n"), 0o600))
		},
		"empty directory": func(t *testing.T, aside string) {
			t.Helper()
			require.NoError(t, os.Mkdir(aside, 0o700))
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "vault.key")
			aside := path + VaultArchiveSuffix + "20261006-120000"

			require.NoError(t, os.WriteFile(path, []byte("current-key\n"), 0o600))
			makeAside(t, aside)

			err := moveAsideIfPresent(path, aside, zap.NewNop().Sugar())
			require.ErrorIs(t, err, ErrVaultArchiveExists)

			current, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			assert.Equal(t, "current-key\n", string(current))

			if name == "file" {
				older, readErr := os.ReadFile(aside)
				require.NoError(t, readErr)
				assert.Equal(t, "older-key\n", string(older))
			} else {
				assert.DirExists(t, aside)
			}
		})
	}
}

// When the data moved aside but a key file then cannot, the archive that was
// made stays where it is and the key stays in place. Nothing is undone by
// deleting, and nothing is lost.
func TestArchiveAndForgetVault_KeyMoveFailureKeepsEverything(t *testing.T) {
	home := t.TempDir()
	paths := map[string]string{
		"vaultDir":       filepath.Join(home, ".vault"),
		"rootKeyFile":    filepath.Join(home, "vault.key"),
		"unsealKeysFile": filepath.Join(home, "vault.key"),
	}

	require.NoError(t, os.MkdirAll(filepath.Join(paths["vaultDir"], "core"), 0o700))
	require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte("legacy-key\n"), 0o600))
	require.NoError(t, os.WriteFile(paths["rootKeyFile"]+VaultArchiveSuffix+"20261006-120000", []byte("taken\n"), 0o600))

	archived, err := archiveAndForgetVault(paths, "20261006-120000", zap.NewNop().Sugar())
	require.ErrorIs(t, err, ErrVaultArchiveExists)

	assert.Equal(t, paths["vaultDir"]+VaultArchiveSuffix+"20261006-120000", archived)
	assert.DirExists(t, filepath.Join(archived, "core"))
	assert.FileExists(t, paths["rootKeyFile"])
}

// noBlocVaultLayout points the home directory at a temporary directory and
// lays out a stopped vault without a bloc: ~/.vault with both key files
// loose beside it.
func noBlocVaultLayout(t *testing.T) (string, map[string]string) {
	t.Helper()

	home := t.TempDir()

	original := homeDirFn
	homeDirFn = func() (string, error) { return home, nil }

	t.Cleanup(func() { homeDirFn = original })

	paths := mustInceptionPaths(t, "", false)

	require.NoError(t, os.MkdirAll(filepath.Join(paths["vaultDir"], "raft"), 0o700))
	require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte("root\n"), 0o600))
	require.NoError(t, os.WriteFile(paths["unsealKeysFile"], []byte("unseal\n"), 0o600))

	return home, paths
}

// The copies of a root token kept beside the root key file belong to the
// vault being archived. Without a bloc they sit loose in the home directory,
// so the archive renames each one under the archive's suffix, where the next
// vault never mistakes it for a copy of its own token.
func TestArchiveAndForgetVault_NoBlocMovesTheKeptTokensWithTheArchive(t *testing.T) {
	home, paths := noBlocVaultLayout(t)
	root := paths["rootKeyFile"]
	suffix := "20261006-120000"

	kept := map[string]string{
		".saferc-20261006-110000":   "s.SAFE-ONE\n",
		".saferc-20261006-110000-2": "s.SAFE-TWO\n",
		".rejected-20261006-110500": "s.REFUSED\n",
	}
	for rest, value := range kept {
		require.NoError(t, os.WriteFile(root+rest, []byte(value), 0o600))
	}

	otherLayout := filepath.Join(home, "test-vault.root.key.saferc-20261006-110000")
	require.NoError(t, os.WriteFile(otherLayout, []byte("s.TEST-MODE\n"), 0o600))

	archived, err := archiveAndForgetVault(paths, suffix, zap.NewNop().Sugar())
	require.NoError(t, err)
	assert.Equal(t, paths["vaultDir"]+VaultArchiveSuffix+suffix, archived)

	for rest, value := range kept {
		assert.NoFileExists(t, root+rest, "the copy must not stay beside the next vault's root key file")

		moved, readErr := os.ReadFile(root + VaultArchiveSuffix + suffix + rest) // #nosec G304 -- the test's own file
		require.NoError(t, readErr, "the copy must travel with the archive")
		assert.Equal(t, value, string(moved))
	}

	left, err := filepath.Glob(root + ".saferc-*")
	require.NoError(t, err)
	assert.Empty(t, left)

	assert.FileExists(t, otherLayout, "another layout's copies are not this vault's")
}

// A kept copy whose archive name is already taken stays where it is, and the
// file at that name is not replaced.
func TestArchiveAndForgetVault_KeptTokenMoveRefusesAnExistingName(t *testing.T) {
	_, paths := noBlocVaultLayout(t)
	root := paths["rootKeyFile"]
	suffix := "20261006-120000"
	copyPath := root + ".saferc-20261006-110000"
	taken := root + VaultArchiveSuffix + suffix + ".saferc-20261006-110000"

	require.NoError(t, os.WriteFile(copyPath, []byte("s.SAFE\n"), 0o600))
	require.NoError(t, os.WriteFile(taken, []byte("s.OLDER\n"), 0o600))

	_, err := archiveAndForgetVault(paths, suffix, zap.NewNop().Sugar())
	require.ErrorIs(t, err, ErrVaultArchiveExists)

	current, err := os.ReadFile(copyPath) // #nosec G304 -- the test's own file
	require.NoError(t, err)
	assert.Equal(t, "s.SAFE\n", string(current))

	older, err := os.ReadFile(taken) // #nosec G304 -- the test's own file
	require.NoError(t, err)
	assert.Equal(t, "s.OLDER\n", string(older))
}
