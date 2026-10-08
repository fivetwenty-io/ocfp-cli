package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// sshKeyNames are the private key files ocfp looks for in a bloc's ssh
// directory.
var sshKeyNames = []string{"id_ed25519", "id_rsa"}

// sshKeysInBothDirsOnce gates the warning that both ssh directories hold
// different keys, so a command that resolves the directory many times says it
// once.
var sshKeysInBothDirsOnce sync.Once

// resolveSSHKeyDir picks the ssh directory for a bloc from the XDG and legacy
// candidates. It keeps its own rules, apart from resolveBlocDir, because a
// split bloc can keep its ssh keys in one directory and its vault in the
// other.
//
//   - Both paths lead to one directory: newPath.
//   - newPath holds id_ed25519, which is the only key bootstrap reuses:
//     newPath.
//   - Otherwise legacyPath holds id_ed25519: legacyPath.
//   - Otherwise a directory with any key wins, newPath first, so an id_rsa
//     does not hide an id_ed25519 on the other side.
//   - Neither holds keys: newPath if it holds anything, else legacyPath if it
//     does, else newPath, which is where new keys go.
//   - A one-time warning names both directories whenever both hold keys and
//     the keys differ. The one-time legacy notice prints when legacyPath is
//     used and newPath holds no keys.
//   - A directory that cannot be inspected is chosen, so a permission problem
//     surfaces as a read error and never as a newly generated key. That holds
//     for legacyPath unless newPath holds id_ed25519, since an id_rsa in
//     newPath does not stop bootstrap from generating an id_ed25519.
//
// A key counts only when it resolves to a regular file, so a dangling link is
// not a key.
func resolveSSHKeyDir(newPath, legacyPath string) string {
	if newPath == legacyPath || sameDir(newPath, legacyPath) {
		return newPath
	}

	newKeys, newErr := sshDirHoldsKeys(newPath)
	if newErr != nil {
		return newPath
	}

	legacyKeys, legacyErr := sshDirHoldsKeys(legacyPath)
	if legacyErr != nil {
		newEd, _ := sshFileExists(filepath.Join(newPath, "id_ed25519"))
		if !newEd {
			return legacyPath
		}
	}

	if newKeys || legacyKeys {
		chosen := chooseKeyDir(newPath, legacyPath, newKeys)

		switch {
		case newKeys && legacyKeys:
			if !sshKeysMatch(newPath, legacyPath) {
				warnSSHKeysInBothDirs(newPath, legacyPath, chosen)
			}
		case chosen == legacyPath:
			warnLegacyPath(legacyPath, newPath)
		}

		return chosen
	}

	newContent, newErr := blocDirHoldsContent(newPath)
	if newErr != nil || newContent {
		return newPath
	}

	legacyContent, legacyErr := blocDirHoldsContent(legacyPath)
	if legacyErr != nil {
		return legacyPath
	}

	if legacyContent {
		warnLegacyPath(legacyPath, newPath)

		return legacyPath
	}

	return newPath
}

// chooseKeyDir ranks two directories that hold keys. A directory with
// id_ed25519 beats one without, newPath first; otherwise newPath wins when it
// holds any key.
func chooseKeyDir(newPath, legacyPath string, newKeys bool) string {
	newEd, _ := sshFileExists(filepath.Join(newPath, "id_ed25519"))
	if newEd {
		return newPath
	}

	legacyEd, _ := sshFileExists(filepath.Join(legacyPath, "id_ed25519"))
	if legacyEd || !newKeys {
		return legacyPath
	}

	return newPath
}

// sameDir reports whether two paths lead to one directory.
func sameDir(a, b string) bool {
	aInfo, aErr := os.Stat(a)
	bInfo, bErr := os.Stat(b)

	return aErr == nil && bErr == nil && os.SameFile(aInfo, bInfo)
}

// sshFileExists reports whether path resolves to a regular file. A missing
// path, including a dangling link, is not a file; any other failure to look
// is an error.
func sshFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if isMissingBlocPath(err) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("cannot inspect %s to find the bloc's ssh keys: %w", path, err)
	}

	return info.Mode().IsRegular(), nil
}

// sshDirHoldsKeys reports whether dir has an id_ed25519 or an id_rsa. A
// missing directory holds none; any other failure to look is an error.
func sshDirHoldsKeys(dir string) (bool, error) {
	for _, name := range sshKeyNames {
		exists, err := sshFileExists(filepath.Join(dir, name))
		if err != nil || exists {
			return exists, err
		}
	}

	return false, nil
}

// sshKeysMatch reports whether both directories hold the same set of key
// names with identical contents. A file that cannot be read counts as
// different, so the warning errs toward speaking.
func sshKeysMatch(a, b string) bool {
	for _, name := range sshKeyNames {
		aExists, aErr := sshFileExists(filepath.Join(a, name))
		bExists, bErr := sshFileExists(filepath.Join(b, name))

		if aErr != nil || bErr != nil || aExists != bExists {
			return false
		}

		if !aExists {
			continue
		}

		aData, aErr := os.ReadFile(filepath.Join(a, name)) // #nosec G304 -- fixed names under ocfp's own directories
		bData, bErr := os.ReadFile(filepath.Join(b, name)) // #nosec G304 -- fixed names under ocfp's own directories

		if aErr != nil || bErr != nil || !bytes.Equal(aData, bData) {
			return false
		}
	}

	return true
}

// warnSSHKeysInBothDirs prints the one-time notice that two ssh directories
// hold different keys and which one is in use.
func warnSSHKeysInBothDirs(newPath, legacyPath, chosen string) {
	sshKeysInBothDirsOnce.Do(func() {
		_, _ = fmt.Fprintf(os.Stderr,
			"ocfp: ssh keys exist in both %s and %s and they differ; using %s\n",
			newPath, legacyPath, chosen)
	})
}
