package commands

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// vaultDataState is what a bloc's inception vault data directory holds,
// judged by what the storage backends write rather than by whether the
// directory exists.
type vaultDataState int

const (
	// vaultDataAbsent means no storage backend has written here: the
	// directory is missing, or it holds neither a raft nor a file marker.
	vaultDataAbsent vaultDataState = iota
	// vaultDataFile means the file backend wrote here. It keeps its keyring
	// and seal configuration under core/.
	vaultDataFile
	// vaultDataRaft means integrated raft storage wrote here: vault.db or a
	// raft/ directory, and no core/.
	vaultDataRaft
	// vaultDataMixed means both backends' markers are present, or an entry
	// could not be examined, or the data path is a link to nothing or not a
	// directory. Nothing may touch such a directory until a person has
	// looked at it.
	vaultDataMixed
)

// String names the state for logs and error messages.
func (s vaultDataState) String() string {
	switch s {
	case vaultDataAbsent:
		return "absent"
	case vaultDataFile:
		return "file"
	case vaultDataRaft:
		return "raft"
	case vaultDataMixed:
		return "mixed"
	}

	return "unknown"
}

// classifyVaultData reports which storage backend owns dir.
//
// The raft markers are the ones safe's localDataInitialized checks, so ocfp
// and safe agree on when a raft directory is initialized. Any lookup that
// fails for a reason other than the entry not existing counts as the marker
// being there: a directory we cannot read is not proof that it is empty, and
// treating it as empty would start a fresh vault over real data. For the same
// reason, dir is absent only when nothing at all is there. A link to nothing,
// such as one into a disk that is not mounted, and an entry that is not a
// directory both count as data that cannot be examined.
func classifyVaultData(dir string) vaultDataState {
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !entryExists(dir) {
			return vaultDataAbsent
		}

		return vaultDataMixed
	}

	if !info.IsDir() {
		return vaultDataMixed
	}

	raft := entryMayExist(filepath.Join(dir, "vault.db")) || entryMayExist(filepath.Join(dir, "raft"))
	file := dirMayExist(filepath.Join(dir, "core"))

	switch {
	case raft && file:
		return vaultDataMixed
	case raft:
		return vaultDataRaft
	case file:
		return vaultDataFile
	default:
		return vaultDataAbsent
	}
}

// entryExists reports whether anything, a dangling link included, is at
// path, counting a lookup that fails for any reason other than absence as
// something being there.
func entryExists(path string) bool {
	_, err := os.Lstat(path)

	return err == nil || !errors.Is(err, fs.ErrNotExist)
}

// entryMayExist reports whether path exists, counting a lookup that fails for
// any reason other than absence as existing.
func entryMayExist(path string) bool {
	_, err := os.Stat(path)

	return err == nil || !errors.Is(err, fs.ErrNotExist)
}

// dirMayExist reports whether path is a directory, counting a lookup that
// fails for any reason other than absence as one.
func dirMayExist(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return !errors.Is(err, fs.ErrNotExist)
	}

	return info.IsDir()
}
