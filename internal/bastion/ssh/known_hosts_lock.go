package ssh

import (
	"errors"
	"fmt"
	"os"
)

// lockKnownHosts takes an exclusive advisory lock that every read-modify-write
// of the known_hosts file at path holds, so a removal that reads the file and
// renames a rewrite over it cannot drop an entry another writer appended in
// between. The lock lives on a sibling file, path+".lock", because the rename
// replaces path itself. It blocks until the lock is free and returns the
// function that releases it. path should already be resolved through any
// symlink, so every writer locks the same file.
func lockKnownHosts(path string) (func(), error) {
	lockPath := path + ".lock"

	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, privateKeyMode) // #nosec G304 -- sibling of the caller's known_hosts path
	if err != nil {
		return nil, fmt.Errorf("open known_hosts lock: %w", err)
	}

	err = lockFile(file)
	if err != nil {
		_ = file.Close()

		return nil, fmt.Errorf("lock known_hosts: %w", err)
	}

	return func() {
		_ = unlockFile(file)
		_ = file.Close()
	}, nil
}

// knownHostsWritePath resolves path through any symlink to the file that
// holds the entries. A path that does not exist yet is returned as it is,
// since there is nothing to resolve until the first entry creates it.
func knownHostsWritePath(path string) (string, error) {
	resolved, err := resolveKnownHostsPath(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, nil
	}

	return resolved, err
}
