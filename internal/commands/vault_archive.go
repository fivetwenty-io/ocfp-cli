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

// ErrVaultDirIsLink reports a vault directory that an archive would rename
// but that is a symbolic link. Renaming the link would leave the vault
// where it is, so a later run could find the old vault again.
var ErrVaultDirIsLink = errors.New("refusing to archive an inception vault directory that is a link")

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
// archiveAndForgetVault moves the loose key files aside on its own.
//
// A vault directory that is a link is never renamed, because the rename
// would move the link and leave the vault where it was. It is refused with
// ErrVaultDirIsLink instead, before anything moves.
func archiveVaultState(paths map[string]string, suffix string, log *zap.SugaredLogger) (string, error) {
	target := vaultArchiveTarget(paths)

	info, err := os.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("failed to inspect vault state at %s: %w", target, err)
	}

	if info.Mode()&fs.ModeSymlink != 0 {
		return "", linkedVaultDirError(target)
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

// vaultArchiveTarget returns the directory an archive renames: the bloc's
// vault directory in the bloc layout, and the data directory in the legacy
// and test layouts.
func vaultArchiveTarget(paths map[string]string) string {
	vaultDir := paths["vaultDir"]

	vaultRoot := filepath.Dir(vaultDir)
	if vaultRootContains(vaultDir, vaultRoot, paths["rootKeyFile"], paths["unsealKeysFile"]) {
		return vaultRoot
	}

	return vaultDir
}

// refuseLinkedVaultDir returns an error wrapping ErrVaultDirIsLink when the
// directory an archive would rename is a link, and nil when that directory
// does not exist, since the archive itself reports that. A directory that
// cannot be examined is an error naming the path, because the archive would
// fail on it too. A command that stops the vault before it archives it calls
// this first, so a vault it would refuse to archive is never stopped.
func refuseLinkedVaultDir(paths map[string]string) error {
	target := vaultArchiveTarget(paths)

	info, err := os.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("cannot examine the vault directory %s: %w", target, err)
	}

	if info.Mode()&fs.ModeSymlink != 0 {
		return linkedVaultDirError(target)
	}

	return nil
}

// linkedVaultDirError names the link at link and where it leads, and tells
// the operator to archive the directory it leads to, or to remove the link,
// by hand.
func linkedVaultDirError(link string) error {
	dest, err := os.Readlink(link)
	if err != nil {
		return fmt.Errorf("%w: %s is a link, and where it leads cannot be read: %w. Archive the directory it "+
			"leads to yourself, or remove the link, and then run the command again", ErrVaultDirIsLink, link, err)
	}

	if !filepath.IsAbs(dest) {
		dest = filepath.Join(filepath.Dir(link), dest)
	}

	return fmt.Errorf("%w: %s is a link to %s. An archive renames the vault directory, and renaming the link "+
		"would leave the vault where it is, so nothing was moved. Archive %s yourself, or remove the link, and "+
		"then run the command again", ErrVaultDirIsLink, link, dest, dest)
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
// them instead, as this path once did, would leave the archived data with no
// way back in. The copies of a root token kept beside the root key file move
// with them, so every token of the archived vault stays with its archive.
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

	err = moveKeptTokensAside(paths["rootKeyFile"], suffix, log)
	if err != nil {
		return archived, err
	}

	return archived, nil
}

// moveKeptTokensAside renames each copy of a root token still kept beside
// rootKeyFile, root.key.saferc-<timestamp> and root.key.rejected-<timestamp>,
// to root.key.superseded-<suffix>.saferc-<timestamp> and so on. The new name
// no longer starts with the prefix a kept copy is found by, so the next vault
// never takes the archived vault's token for its own. In the bloc layout the
// copies already moved with <bloc>/vault and there is nothing left to do.
// It renames only, and refuses to replace anything.
func moveKeptTokensAside(rootKeyFile, suffix string, log *zap.SugaredLogger) error {
	if rootKeyFile == "" {
		return nil
	}

	dir := filepath.Dir(rootKeyFile)

	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to list the root token copies in %s: %w", dir, err)
	}

	base := filepath.Base(rootKeyFile)

	for _, entry := range entries {
		rest, ok := strings.CutPrefix(entry.Name(), base)
		if !ok || !entry.Type().IsRegular() {
			continue
		}

		if !strings.HasPrefix(rest, keyfile.TokenCopyInfix) && !strings.HasPrefix(rest, rejectedTokenInfix) {
			continue
		}

		err = moveAsideIfPresent(filepath.Join(dir, entry.Name()), rootKeyFile+VaultArchiveSuffix+suffix+rest, log)
		if err != nil {
			return err
		}
	}

	return nil
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
