package commands

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/keyfile"
	"go.uber.org/zap"
)

// VaultArchiveSuffix prefixes the directory a superseded vault is moved to.
const VaultArchiveSuffix = ".superseded-"

// ErrVaultArchiveExists reports an archive path that is already taken.
var ErrVaultArchiveExists = keyfile.ErrExists

// archiveVaultState moves a bloc's vault aside so a fresh one can start in
// its place, and never deletes anything.
//
// cleanupExistingVault runs whenever the liveness probe says no vault is
// serving the port. That probe has been wrong before — it shelled out to a CLI
// that could not start — and a wrong answer there used to mean os.RemoveAll on
// the data directory plus root.key and unseal.keys. Those keys are the only
// way back into the bloc's secrets, so a single false negative was
// unrecoverable data loss. Data without its keys is still worth keeping, too:
// the keys may turn up in a backup or another operator's copy.
//
// Moving the directory aside costs a rename and makes every such mistake
// survivable. It returns the archive path, or an empty string when there was
// nothing to move.
//
// The bloc layout is <bloc>/vault/{data,root.key,unseal.keys}, so archiving
// the parent captures keys and data together. Legacy and test-mode layouts put
// vaultDir at ~/.vault with keys loose in the home directory; there the parent
// is the user's home, so only the data directory is renamed here and
// archiveAndForgetVault moves the loose key file aside on its own.
func archiveVaultState(paths map[string]string, suffix string, log *zap.SugaredLogger) (string, error) {
	vaultDir := paths["vaultDir"]
	target := vaultDir

	vaultRoot := filepath.Dir(vaultDir)
	if vaultRootContains(vaultDir, vaultRoot, paths["rootKeyFile"], paths["unsealKeysFile"]) {
		target = vaultRoot
	}

	_, err := os.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("failed to inspect vault state at %s: %w", target, err)
	}

	archivePath := target + VaultArchiveSuffix + suffix

	// rename(2) quietly replaces an empty directory, and an archive from the
	// same second must never be replaced, so refuse any existing entry.
	_, err = os.Lstat(archivePath)
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%w: %s", ErrVaultArchiveExists, archivePath)
	}

	err = os.Rename(target, archivePath)
	if err != nil {
		return "", fmt.Errorf("failed to archive vault state to %s: %w", archivePath, err)
	}

	log.Warnw("Preserved existing vault state rather than deleting it", "archive", archivePath)

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

// archiveAndForgetVault moves a stopped vault's data and keys aside so a
// fresh vault can start in their place. It never deletes anything, and the
// caller must have stopped the vault first.
//
// In the bloc layout archiveVaultState renames <bloc>/vault, which carries
// the keys along with the data. In the legacy and test layouts the key files
// sit loose in the home directory, where the next fresh vault would write
// its own keys over them, so each is renamed aside under the same suffix. Deleting
// it instead, as this path once did, would leave the archived data with no
// way back in.
func archiveAndForgetVault(paths map[string]string, suffix string, log *zap.SugaredLogger) (string, error) {
	archived, err := archiveVaultState(paths, suffix, log)
	if err != nil {
		return "", err
	}

	seen := map[string]bool{}

	for _, keyFile := range []string{paths["rootKeyFile"], paths["unsealKeysFile"]} {
		if keyFile == "" || seen[keyFile] {
			continue
		}

		seen[keyFile] = true

		err = moveAsideIfPresent(keyFile, keyFile+VaultArchiveSuffix+suffix, log)
		if err != nil {
			return archived, err
		}
	}

	return archived, nil
}

// moveAsideIfPresent renames path to aside when path exists, refusing to
// replace anything already at aside.
func moveAsideIfPresent(path, aside string, log *zap.SugaredLogger) error {
	_, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to inspect %s: %w", path, err)
	}

	_, err = os.Lstat(aside)
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrVaultArchiveExists, aside)
	}

	err = os.Rename(path, aside)
	if err != nil {
		return fmt.Errorf("failed to move %s aside: %w", path, err)
	}

	log.Warnw("Preserved superseded vault key file", "archive", aside)

	return nil
}
