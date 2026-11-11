package ssh

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1" // #nosec G505 -- OpenSSH hashes known_hosts names with HMAC-SHA1; matching them needs it
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ErrEmptyKnownHost reports a request to remove entries for an empty host.
var ErrEmptyKnownHost = errors.New("host must not be empty")

// RemovedHostKey describes one known_hosts entry RemoveKnownHost deleted.
type RemovedHostKey struct {
	// Line is the entry's line number in the file before removal.
	Line int
	// Fingerprint is the SHA256 fingerprint of the entry's key, or empty
	// when the key could not be parsed.
	Fingerprint string
}

// RemoveKnownHost deletes the known_hosts entries for host from the file at
// path, for use when a host is rebuilt on purpose and its key is expected to
// change. It returns the entries it removed so the caller can log the old
// fingerprints.
//
// An entry is removed when one of its names is host itself, "[host]:port"
// for any port, or a hashed name for host or "[host]:port" on the given
// port. A line that lists several names goes as a whole, as with ssh-keygen
// -R. Wildcard patterns, lines whose negated names cover host,
// @cert-authority and @revoked lines, comments, and every other line stay
// byte for byte. When path is a symlink the file it points at is rewritten
// and the link stays, and the file keeps its mode and, where the platform
// allows, its owner. A missing file is not an error, and a file with nothing
// to remove is not rewritten.
func RemoveKnownHost(path, host string, port int) ([]RemovedHostKey, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, ErrEmptyKnownHost
	}

	path, err := resolveKnownHostsPath(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path) // #nosec G304 -- caller-chosen known_hosts path
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read known_hosts: %w", err)
	}

	hashedNames := []string{host}
	if port != DefaultSSHPort {
		hashedNames = append(hashedNames, "["+host+"]:"+strconv.Itoa(port))
	}

	var (
		kept    bytes.Buffer
		removed []RemovedHostKey
	)

	for index, line := range bytes.SplitAfter(data, []byte("\n")) {
		names, key, ok := knownHostsEntry(string(line))
		if ok && entryNamesHost(names, host, hashedNames) {
			removed = append(removed, RemovedHostKey{Line: index + 1, Fingerprint: key})

			continue
		}

		kept.Write(line)
	}

	if len(removed) == 0 {
		return nil, nil
	}

	err = replaceFile(path, kept.Bytes())
	if err != nil {
		return nil, err
	}

	return removed, nil
}

// knownHostsEntry splits a known_hosts line into its name field and the
// SHA256 fingerprint of its key. It reports false for blank lines, comments,
// and marker lines, which RemoveKnownHost never removes.
func knownHostsEntry(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "@") {
		return "", "", false
	}

	split := strings.IndexAny(trimmed, " \t")
	if split < 0 {
		return "", "", false
	}

	names, rest := trimmed[:split], trimmed[split+1:]

	return names, keyFingerprint(rest), true
}

// keyFingerprint returns the SHA256 fingerprint of the "type base64" key at
// the start of fields, or an empty string when it does not parse.
func keyFingerprint(fields string) string {
	parts := strings.Fields(fields)
	if len(parts) < 2 { //nolint:mnd // key type and key data
		return ""
	}

	blob, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}

	key, err := ssh.ParsePublicKey(blob)
	if err != nil {
		return ""
	}

	return ssh.FingerprintSHA256(key)
}

// entryNamesHost reports whether a known_hosts name field names host.
func entryNamesHost(names, host string, hashedNames []string) bool {
	if strings.HasPrefix(names, "|1|") {
		return hashedNameMatches(names, hashedNames)
	}

	matched := false

	for pattern := range strings.SplitSeq(names, ",") {
		if strings.HasPrefix(pattern, "!") {
			// A negated name that covers host means the line does not
			// apply to it, so it is not ours to remove. A negation is
			// read as OpenSSH reads it, wildcards included, so a line
			// is never removed for a host it excludes.
			if patternCoversHost(pattern[1:], host) {
				return false
			}

			continue
		}

		if plainNameIsHost(pattern, host) {
			matched = true
		}
	}

	return matched
}

// plainNameIsHost reports whether a literal known_hosts name is host or
// "[host]:port" for some port. Names with wildcards never match.
func plainNameIsHost(name, host string) bool {
	if strings.ContainsAny(name, "*?") {
		return false
	}

	if strings.EqualFold(name, host) {
		return true
	}

	inner, port, found := strings.Cut(name, "]:")
	if !found || !strings.HasPrefix(inner, "[") {
		return false
	}

	_, err := strconv.Atoi(port)

	return err == nil && strings.EqualFold(inner[1:], host)
}

// patternCoversHost reports whether a known_hosts pattern, which may hold
// the wildcards "*" and "?", matches host or "[host]:port" for some port.
// Matching ignores case, as OpenSSH does for host names.
func patternCoversHost(pattern, host string) bool {
	pattern, host = strings.ToLower(pattern), strings.ToLower(host)
	if wildcardMatch(pattern, host) {
		return true
	}

	inner, port, found := strings.Cut(pattern, "]:")
	if !found || !strings.HasPrefix(inner, "[") {
		return false
	}

	_, err := strconv.Atoi(port)

	return err == nil && wildcardMatch(inner[1:], host)
}

// wildcardMatch matches s against an OpenSSH pattern, where "*" stands for
// any run of characters and "?" for exactly one.
func wildcardMatch(pattern, s string) bool {
	for pattern != "" {
		switch pattern[0] {
		case '*':
			rest := strings.TrimLeft(pattern, "*")
			if rest == "" {
				return true
			}

			for i := range len(s) + 1 {
				if wildcardMatch(rest, s[i:]) {
					return true
				}
			}

			return false
		case '?':
			if s == "" {
				return false
			}
		default:
			if s == "" || s[0] != pattern[0] {
				return false
			}
		}

		pattern, s = pattern[1:], s[1:]
	}

	return s == ""
}

// hashedNameMatches reports whether a hashed known_hosts name,
// "|1|base64(salt)|base64(HMAC-SHA1(salt, name))", hashes one of names.
func hashedNameMatches(hashed string, names []string) bool {
	parts := strings.Split(hashed, "|")
	if len(parts) != 4 { //nolint:mnd // "", "1", salt, hash
		return false
	}

	salt, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}

	want, err := base64.StdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}

	for _, name := range names {
		for _, candidate := range []string{name, strings.ToLower(name)} {
			mac := hmac.New(sha1.New, salt)
			mac.Write([]byte(candidate))

			if hmac.Equal(mac.Sum(nil), want) {
				return true
			}
		}
	}

	return false
}

// resolveKnownHostsPath follows symlinks to the file that holds the
// entries, so a rewrite replaces that file rather than the link to it.
func resolveKnownHostsPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve known_hosts: %w", err)
	}

	return resolved, nil
}

// replaceFile writes data to path through a temporary file in the same
// directory, so a reader never sees a half-written file, and keeps the
// original file's mode and, where it can, its owner. path must not be a
// symlink; the caller resolves it first.
func replaceFile(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat known_hosts: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".known_hosts-*")
	if err != nil {
		return fmt.Errorf("create temporary known_hosts: %w", err)
	}

	tmpPath := tmp.Name()

	defer func() { _ = os.Remove(tmpPath) }()

	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(info.Mode().Perm())
	}

	if err == nil {
		keepOwner(tmp, info)
	}

	if err == nil {
		err = tmp.Sync()
	}

	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}

	if err != nil {
		return fmt.Errorf("write temporary known_hosts: %w", err)
	}

	err = os.Rename(tmpPath, path)
	if err != nil {
		return fmt.Errorf("replace known_hosts: %w", err)
	}

	return nil
}
