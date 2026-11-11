package ssh

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// KnownHostsFile returns the path of the user's known_hosts file, the
// same file OpenSSH reads by default.
func KnownHostsFile() (string, error) {
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

// mismatchFromKeyError turns a knownhosts error into a HostKeyMismatchError
// when the host has keys on record and none of them matched. It returns nil
// for any other error, including a host with no entry at all.
func mismatchFromKeyError(err error, knownHostsPath, hostname string, key ssh.PublicKey) *HostKeyMismatchError {
	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) || len(keyErr.Want) == 0 {
		return nil
	}

	recorded := make([]string, 0, len(keyErr.Want))
	for _, known := range keyErr.Want {
		recorded = append(recorded, fmt.Sprintf("%s (%s line %d)", ssh.FingerprintSHA256(known.Key), filepath.Base(known.Filename), known.Line))
	}

	return &HostKeyMismatchError{
		Host:           knownhosts.Normalize(hostname),
		KnownHostsPath: knownHostsPath,
		Presented:      ssh.FingerprintSHA256(key),
		Recorded:       recorded,
		Err:            err,
	}
}

// isUnknownHost reports whether err means known_hosts has no entry for the
// host, which is the only case where a key may be added without asking.
func isUnknownHost(err error) bool {
	var keyErr *knownhosts.KeyError

	return errors.As(err, &keyErr) && len(keyErr.Want) == 0
}

// sshConfigPath quotes path for an ssh -o option when it holds whitespace,
// which ssh would otherwise read as a list of several files.
func sshConfigPath(path string) string {
	if !strings.ContainsAny(path, " \t") {
		return path
	}

	return `"` + path + `"`
}
