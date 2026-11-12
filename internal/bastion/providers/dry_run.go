package providers

import (
	"fmt"

	"github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
	"github.com/fivetwenty-io/ocfp-cli/internal/security"
	"github.com/fivetwenty-io/ocfp-cli/internal/state"
)

// A provider's connection lookup has two forms. GetConnectionDetails may
// write, because it creates the bloc's state directory when it opens a
// missing one and restores the bloc's SSH key from the config when no key
// file is found. PreviewConnectionDetails is the form a dry run uses. It
// makes the same lookups in the same order and fails where they fail, but
// it writes nothing. It opens the state without creating its directory,
// and it logs the key restore instead of making it. The helpers below hold
// the two places where the forms differ.

// openState opens the state in stateDir. The real lookup creates the
// directory when it is missing. A dry run opens it without creating it and
// fails where the real lookup would fail to create it, with the same error.
func openState(stateDir string, dryRun bool) (*state.Manager, error) {
	if dryRun {
		return state.PreviewManager(stateDir) //nolint:wrapcheck // callers wrap it as they wrap NewManager's
	}

	return state.NewManager(stateDir) //nolint:wrapcheck // callers wrap it
}

// bastionKey is the private key a lookup settles on.
type bastionKey struct {
	// path is the key file, or the file that a dry run would have
	// restored the key to.
	path string

	// pendingPEM is the config's copy of a key that a dry run would have
	// restored. The dry run reads it in place of the file it didn't write.
	// It is empty for a key that has a file.
	pendingPEM string
}

// restoreKey restores the bloc's key from privateKeyPEM, the config's copy,
// and returns it. A dry run makes the checks the restore makes, writes
// nothing, logs the restore, and returns the key that the restore would
// have written. The path is empty when privateKeyPEM is, as it is from
// the restore.
func restoreKey(keyManager *ssh.KeyManager, blocName, privateKeyPEM string, dryRun bool) (bastionKey, error) {
	if !dryRun {
		path, err := keyManager.RestoreKeyFromConfig(blocName, privateKeyPEM)

		return bastionKey{path: path, pendingPEM: ""}, err //nolint:wrapcheck // callers wrap it
	}

	path, err := keyManager.PreviewRestoreKeyFromConfig(blocName, privateKeyPEM)
	if err != nil || path == "" {
		return bastionKey{path: path, pendingPEM: ""}, err //nolint:wrapcheck // callers wrap it
	}

	return bastionKey{path: path, pendingPEM: privateKeyPEM}, nil
}

// encrypted reports whether the key is passphrase-protected. It reads the
// key file, or the config's copy of a key that a dry run didn't restore.
// For the config's copy it first validates the path the key would have,
// as reading the file does, and fails with the same error.
func (k bastionKey) encrypted(keyManager *ssh.KeyManager) (bool, error) {
	if k.pendingPEM != "" {
		err := security.ValidateSSHKeyPath(k.path)
		if err != nil {
			return false, fmt.Errorf("invalid key path: %w", err)
		}

		return keyManager.IsKeyDataPasswordProtected([]byte(k.pendingPEM)), nil
	}

	return keyManager.IsKeyPasswordProtected(k.path) //nolint:wrapcheck // callers log it as they always have
}
