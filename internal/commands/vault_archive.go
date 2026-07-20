package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// VaultArchiveSuffix prefixes the directory a superseded vault is moved to.
const VaultArchiveSuffix = ".superseded-"

// archiveVaultState clears a bloc's vault directory, preserving it if it holds
// anything unrecoverable.
//
// cleanupExistingVault runs whenever the liveness probe says no vault is
// serving the port. That probe has been wrong before — it shelled out to a CLI
// that could not start — and a wrong answer there used to mean os.RemoveAll on
// the data directory plus root.key and unseal.keys. Those keys are the only
// way back into the bloc's secrets, so a single false negative was
// unrecoverable data loss.
//
// Moving the directory aside costs a rename and makes that failure survivable.
// It returns the archive path, or an empty string when nothing needed keeping.
//
// The bloc layout is <bloc>/vault/{data,root.key,unseal.keys}, so archiving
// the parent captures keys and data together. Legacy and test-mode layouts put
// vaultDir at ~/.vault with keys loose in the home directory; there the parent
// is the user's home, so those are cleared the old way rather than archived.
func archiveVaultState(paths map[string]string, suffix string, log *zap.SugaredLogger) (string, error) {
	vaultDir := paths["vaultDir"]
	vaultRoot := filepath.Dir(vaultDir)

	if !vaultRootContains(vaultDir, vaultRoot, paths["rootKeyFile"], paths["unsealKeysFile"]) {
		return "", clearVaultDir(vaultDir, log)
	}

	if !anyFileExists(paths["rootKeyFile"], paths["unsealKeysFile"]) {
		return "", clearVaultDir(vaultRoot, log)
	}

	archivePath := vaultRoot + VaultArchiveSuffix + suffix

	err := os.Rename(vaultRoot, archivePath)
	if err != nil {
		return "", fmt.Errorf("failed to archive vault state to %s: %w", archivePath, err)
	}

	log.Warnw("Preserved existing vault state rather than deleting it",
		"archive", archivePath,
		"reason", "directory held root.key or unseal.keys")

	return archivePath, nil
}

// vaultRootContains reports whether the paths form the bloc-scoped layout,
// <bloc>/vault/{data,root.key,unseal.keys}, and are therefore safe to archive
// by renaming the parent.
//
// Checking only that the key files sit under the parent is not enough: the
// legacy layout is ~/.vault with keys loose in the home directory, and those
// are "under the parent" too. Requiring the exact directory names keeps a
// rename of the user's home out of reach.
func vaultRootContains(vaultDir, vaultRoot string, keyFiles ...string) bool {
	if filepath.Base(vaultRoot) != "vault" || filepath.Base(vaultDir) != "data" {
		return false
	}

	prefix := vaultRoot + string(os.PathSeparator)

	for _, keyFile := range keyFiles {
		if !strings.HasPrefix(keyFile, prefix) {
			return false
		}
	}

	return true
}

// anyFileExists reports whether at least one of the paths is present.
func anyFileExists(candidates ...string) bool {
	for _, candidate := range candidates {
		_, err := os.Stat(candidate)
		if err == nil {
			return true
		}
	}

	return false
}

// clearVaultDir removes a directory, treating an already-absent one as done.
func clearVaultDir(dir string, log *zap.SugaredLogger) error {
	err := os.RemoveAll(dir)
	if err != nil && !os.IsNotExist(err) {
		log.Warnw("Failed to remove vault directory", "dir", dir, "error", err)

		return fmt.Errorf("failed to remove vault directory %s: %w", dir, err)
	}

	return nil
}
