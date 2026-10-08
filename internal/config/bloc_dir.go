package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// ErrBlocVaultInBothDirs reports a bloc that has an inception vault in both
// its XDG data directory and its legacy ~/.ocfp directory. Only a person can
// tell which one holds the secrets the bloc needs, so ocfp uses neither.
var ErrBlocVaultInBothDirs = errors.New("the bloc has an inception vault in two directories")

// blocVaultKeyFiles are the key files whose presence alone makes a bloc
// directory hold a vault, even when its data directory is gone.
var blocVaultKeyFiles = []string{"root.key", "unseal.keys"} //nolint:gochecknoglobals // fixed list

// mixedBlocDirWarned remembers the blocs whose split layout has already been
// reported, so a run that resolves one bloc's directory many times warns
// about it once.
var mixedBlocDirWarned sync.Map //nolint:gochecknoglobals // per-process warning gate

// resolveBlocDir chooses between a bloc's XDG data directory (newPath) and
// its legacy ~/.ocfp directory (legacyPath). The rules exist so that a
// directory without a vault never hides one that has a vault, because a
// reconcile that sees no vault starts a new, empty one.
//
//   - Both paths lead to one directory, or to one vault directory, through a
//     link: newPath, since there is only one vault to use.
//   - Both hold a vault: an error wrapping ErrBlocVaultInBothDirs that names
//     both directories.
//   - Only newPath holds a vault: newPath.
//   - Only legacyPath holds a vault: legacyPath, even when newPath holds
//     other bloc files, with a one-time warning when it does.
//   - Neither holds a vault: newPath when it holds bloc content, else
//     legacyPath when that does, else newPath as the place to create one.
//
// See blocDirHoldsVault and blocDirHoldsContent for what those terms mean.
// A directory that cannot be inspected is an error, since guessing could
// hide a vault.
func resolveBlocDir(blocName, newPath, legacyPath string) (string, error) {
	if newPath == legacyPath || sameBlocVaultDir(newPath, legacyPath) {
		return newPath, nil
	}

	newVault, err := blocDirHoldsVault(newPath)
	if err != nil {
		return "", err
	}

	legacyVault, err := blocDirHoldsVault(legacyPath)
	if err != nil {
		return "", err
	}

	switch {
	case newVault && legacyVault:
		return "", blocVaultInBothDirsError(blocName, newPath, legacyPath)
	case newVault:
		return newPath, nil
	case legacyVault:
		return useLegacyBlocVault(blocName, newPath, legacyPath)
	}

	newContent, err := blocDirHoldsContent(newPath)
	if err != nil {
		return "", err
	}

	if newContent {
		return newPath, nil
	}

	legacyContent, err := blocDirHoldsContent(legacyPath)
	if err != nil {
		return "", err
	}

	if legacyContent {
		warnLegacyPath(legacyPath, newPath)

		return legacyPath, nil
	}

	return newPath, nil
}

// sameBlocVaultDir reports whether newPath and legacyPath lead to one
// directory, or their vault directories do, because a link joins them. One
// vault seen through two paths is not two vaults, and treating it as two
// would refuse it and advise moving the only copy aside. A path that cannot
// be followed proves nothing here, and the checks after this one judge it.
func sameBlocVaultDir(newPath, legacyPath string) bool {
	for _, sub := range []string{"", "vault"} {
		newInfo, newErr := os.Stat(filepath.Join(newPath, sub))
		legacyInfo, legacyErr := os.Stat(filepath.Join(legacyPath, sub))

		if newErr == nil && legacyErr == nil && os.SameFile(newInfo, legacyInfo) {
			return true
		}
	}

	return false
}

// useLegacyBlocVault returns legacyPath for a bloc whose only vault lives
// there. When newPath also holds other files, the bloc's files are split
// across two directories, and the operator is told once how to join them.
func useLegacyBlocVault(blocName, newPath, legacyPath string) (string, error) {
	newContent, err := blocDirHoldsContent(newPath)
	if err != nil {
		return "", err
	}

	if !newContent {
		warnLegacyPath(legacyPath, newPath)

		return legacyPath, nil
	}

	_, warned := mixedBlocDirWarned.LoadOrStore(blocName, true)
	if !warned {
		_, _ = fmt.Fprintf(os.Stderr,
			"ocfp: bloc %s keeps its inception vault in %s, so ocfp uses that directory even though %s also "+
				"holds files for the bloc; stop the vault with 'tmux kill-session -t %s' and run "+
				"'ocfp config migrate' to bring them together\n",
			blocName, legacyPath, newPath, blocInceptionTmuxSession(blocName))
	}

	return legacyPath, nil
}

// warnLegacyPath prints the shared one-time notice that a legacy path is in
// use.
func warnLegacyPath(legacyPath, newPath string) {
	legacyWarnOnce.Do(func() {
		_, _ = fmt.Fprintf(os.Stderr,
			"ocfp: using legacy path %s; new location is %s (run `ocfp` again after migrating to silence this warning)\n",
			legacyPath, newPath)
	})
}

// blocDirHoldsVault reports whether a bloc directory holds an inception
// vault. It does when its vault directory has a root.key or an unseal.keys,
// of any size, or a data directory with anything in it. A data entry that is
// not a directory counts too, because it is not ocfp's to explain away. An
// empty data directory holds nothing to lose, and ocfp creates one before it
// starts a vault, so it does not count.
func blocDirHoldsVault(blocDir string) (bool, error) {
	vaultDir := filepath.Join(blocDir, "vault")

	for _, name := range blocVaultKeyFiles {
		exists, err := blocPathExists(filepath.Join(vaultDir, name))
		if err != nil || exists {
			return exists, err
		}
	}

	return blocDirHoldsContent(filepath.Join(vaultDir, "data"))
}

// blocDirHoldsContent reports whether path exists and is something other
// than an empty directory. A bloc directory that holds content, such as ssh
// keys, deployments, or a vault, is the bloc's directory; an empty one is
// only a name.
func blocDirHoldsContent(path string) (bool, error) {
	info, err := os.Lstat(path)
	if isMissingBlocPath(err) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("cannot inspect %s to find the bloc's directory: %w", path, err)
	}

	if !info.IsDir() {
		return true, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return false, fmt.Errorf("cannot list %s to find the bloc's directory: %w", path, err)
	}

	return len(entries) > 0, nil
}

// blocPathExists reports whether path exists, without following a symlink.
// A parent that is not a directory means the path cannot exist.
func blocPathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if isMissingBlocPath(err) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("cannot inspect %s to find the bloc's directory: %w", path, err)
	}

	return true, nil
}

// blocInceptionTmuxSession names the tmux session a bloc's inception vault
// runs in.
func blocInceptionTmuxSession(blocName string) string {
	return blocName + "-inception-vault"
}

// blocVaultInBothDirsError explains a bloc with a vault in both directories,
// and how to keep one without losing the other. A link deeper inside either
// vault directory can still make the two share files, so the advice says to
// look for links before anything is moved.
func blocVaultInBothDirsError(blocName, newPath, legacyPath string) error {
	session := blocInceptionTmuxSession(blocName)
	newVault, legacyVault := filepath.Join(newPath, "vault"), filepath.Join(legacyPath, "vault")

	return fmt.Errorf("%w: bloc %s has a vault in %s and another in %s. ocfp will not guess which one holds "+
		"the secrets the bloc needs, so it has left both as they are and will use neither until one is set aside. "+
		"First make sure they really are two vaults, because a link can make one vault show up in both places. "+
		"Run 'ls -l %s %s' and 'realpath %s %s'. If either listing shows a link, such as a data directory or a "+
		"key file that points into the other vault directory, there may be only one vault behind both paths, "+
		"so move nothing and sort out the link by hand, because moving a directory that a link points into "+
		"strands the vault behind it. When there are no links, find the vault in use. Run "+
		"'lsof -nP -iTCP:%d -sTCP:LISTEN' to see what serves the bloc's API port, run 'tmux ls' to see whether "+
		"session %s exists, and compare the files and dates under each vault directory, including root.key and "+
		"unseal.keys. If the vault to set aside is the running one, stop it first with 'tmux kill-session -t %s'. "+
		"Then rename its vault directory rather than deleting it, with 'mv %s %s.set-aside' or "+
		"'mv %s %s.set-aside', so that both vaults stay on disk. The next ocfp command uses the vault that remains",
		ErrBlocVaultInBothDirs, blocName, newPath, legacyPath, newVault, legacyVault, newVault, legacyVault,
		InceptionVaultPort(blocName), session, session, newVault, newVault, legacyVault, legacyVault)
}

// isMissingBlocPath reports whether a stat error means the path is not there,
// either because it does not exist or because a parent is not a directory.
func isMissingBlocPath(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}
