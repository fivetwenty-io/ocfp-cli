package ssh

import (
	"fmt"
	"os"
	"path/filepath"
)

// homeKnownHostsFile returns the path of the user's known_hosts file, the
// same file OpenSSH reads by default.
func homeKnownHostsFile() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	return filepath.Join(homeDir, ".ssh", "known_hosts"), nil
}

// knownHostKeyAlgorithms returns the host key algorithms the client should
// offer when it dials address, or nil to keep the library default.
//
// A server usually holds several host keys, and known_hosts often records
// only the one an earlier client chose. Go's client prefers ECDSA over
// ed25519, so without this it can negotiate a key type the file has no entry
// for and see a correct host as a changed one. The order comes from
// preferredHostKeyAlgorithms, which Stream uses too, so both ask a host for
// a key the file can vouch for. A file that cannot be read leaves the
// default, and the host key check then reports the problem.
func knownHostKeyAlgorithms(knownHostsPath, address string) []string {
	file, err := loadKnownHosts(knownHostsPath)
	if err != nil {
		return nil
	}

	return preferredHostKeyAlgorithms(file.entriesFor(address))
}
