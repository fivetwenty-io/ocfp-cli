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

// sshConfigPath writes path as the value of an ssh -o file option such as
// UserKnownHostsFile, so that ssh reads back exactly path. ssh expands
// %-tokens in the value, so each % is doubled. It splits the value on
// whitespace and gives quotes and backslashes their own meaning, so a path
// holding any of those is double-quoted, with " and \ escaped inside.
//
// ssh also expands ${NAME} in these values and has no escape for it; a
// caller that cannot accept that checks the path with
// checkSSHConfigPath first.
//
// The doubled % relies on OpenSSH 8.4 or later, the first release to
// expand tokens in UserKnownHostsFile. An older ssh, such as 8.2 on
// Ubuntu 20.04 or 8.0 on RHEL 8, reads the value literally, so a home
// directory holding % would point it at a file that does not exist. We
// keep the doubling, because operator hosts and bastions run newer
// releases, and without it a newer ssh would expand the %.
func sshConfigPath(path string) string {
	path = strings.ReplaceAll(path, "%", "%%")

	if !strings.ContainsAny(path, " \t'\"\\") {
		return path
	}

	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(path) + `"`
}

// QuoteConfigPath writes path as the value of an ssh_config file option,
// such as IdentityFile, so that ssh reads back exactly path. It escapes %
// and quotes the path as sshConfigPath does, and has the same limit for
// "${".
func QuoteConfigPath(path string) string {
	return sshConfigPath(path)
}

// checkSSHConfigPath reports a path that sshConfigPath cannot pass to ssh
// intact: one holding "${", which ssh expands as an environment variable.
func checkSSHConfigPath(path string) error {
	if strings.Contains(path, "${") {
		return fmt.Errorf("%w: %q holds \"${\", which system ssh expands as an environment variable, "+
			"so it would check a different file than ocfp does; run ocfp with a HOME that does not hold \"${\"",
			ErrKnownHostsPathUnusable, path)
	}

	return nil
}
