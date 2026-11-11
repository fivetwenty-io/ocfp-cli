package ssh

import (
	"errors"
	"fmt"
	"strings"
)

// SSH client errors.
var (
	ErrEmptyCommand            = errors.New("empty command")
	ErrSSHClientNotConnected   = errors.New("SSH client not connected")
	ErrNoAuthMethodsAvailable  = errors.New("no authentication methods available")
	ErrExternalSSHNotAvailable = errors.New("external SSH command not available")
)

// ErrCommandNotAllowed returns an error indicating the given command is not permitted.
func ErrCommandNotAllowed(cmd string) error {
	return fmt.Errorf("command not allowed: %s", cmd) //nolint:err113 // dynamic error with context
}

// ErrShellMetacharacters returns an error when an argument contains unsafe shell metacharacters.
func ErrShellMetacharacters(arg string) error {
	return fmt.Errorf("argument contains shell metacharacters: %s", arg) //nolint:err113 // dynamic error with context
}

// ErrUnexpectedSSHOutput returns an error for unexpected output from an external SSH test.
func ErrUnexpectedSSHOutput(output string) error {
	return fmt.Errorf("external SSH test returned unexpected output: %s", output) //nolint:err113 // dynamic error with context
}

// SSH key manager errors.
var (
	ErrNoValidSSHKeyFound       = errors.New("no valid SSH private key found")
	ErrKeyEncryptedNoPassphrase = errors.New("private key is encrypted but no passphrase provided")
	ErrKeysMismatch             = errors.New("private and public keys do not match")
	ErrRSAKeySizeTooSmall       = errors.New("RSA key size must be at least 2048 bits")
	ErrInvalidEd25519KeyType    = errors.New("invalid Ed25519 private key type")
	ErrInvalidRSAKeyType        = errors.New("invalid RSA private key type")
)

// ErrUnsupportedKeyType returns an error for an unsupported SSH key type.
func ErrUnsupportedKeyType(keyType string) error {
	return fmt.Errorf("unsupported key type: %s (supported: rsa, ed25519)", keyType) //nolint:err113 // dynamic error with context
}

// ErrUnsupportedKeyTypeForWriting returns an error when the key type cannot be serialized.
func ErrUnsupportedKeyTypeForWriting(keyType string) error {
	return fmt.Errorf("unsupported key type for writing: %s", keyType) //nolint:err113 // dynamic error with context
}

// SSH transfer errors.
var (
	ErrNoBastionPrefixSpecified = errors.New("at least one path must specify bastion: prefix")
	ErrTarPipeNotImplemented    = errors.New("tar pipe not fully implemented")
	ErrFileTooLargeForBase64    = errors.New("file too large for base64 transfer")
	ErrExternalSCPNotAvailable  = errors.New("external SCP command not available")
)

// ErrFileIntegrityCheckFailed returns an error when local and remote file hashes do not match.
func ErrFileIntegrityCheckFailed(localHash, remoteHash string) error {
	return fmt.Errorf("file integrity check failed: local=%s remote=%s", localHash, remoteHash) //nolint:err113 // dynamic error with context
}

// HostKeyMismatchError describes a host key that differs from the one on
// record. errors.Is matches it against ErrHostKeyMismatch, the sentinel
// Stream uses for the same refusal. It is terminal: a client that sees it
// must not retry or fall back to another way of connecting.
type HostKeyMismatchError struct {
	// Host is the name known_hosts files the entry under: "host" on port
	// 22, otherwise "[host]:port".
	Host string
	// KnownHostsPath is the file holding the recorded key.
	KnownHostsPath string
	// Presented is the SHA256 fingerprint of the key the host offered.
	Presented string
	// Recorded lists the SHA256 fingerprints on record for the host.
	Recorded []string
	// Err is the underlying knownhosts error.
	Err error
}

// Error explains the mismatch and how to clear the entry after a rebuild.
func (e *HostKeyMismatchError) Error() string {
	return fmt.Sprintf(
		"host key verification failed: %s presented %s, but %s records %s for it. "+
			"Someone may be intercepting the connection. If the bastion was rebuilt on purpose, "+
			"remove the stale entry with `ssh-keygen -R %s -f %s` and connect again. "+
			"If the recorded line is a wildcard pattern such as `10.0.0.*`, which ssh-keygen -R "+
			"cannot remove, edit that line out of %s by hand",
		e.Host, e.Presented, e.KnownHostsPath, strings.Join(e.Recorded, ", "),
		shellWord(e.Host), shellWord(e.KnownHostsPath), e.KnownHostsPath)
}

// shellWord single-quotes s when a shell would otherwise split or glob it,
// such as the brackets in a "[host]:port" known_hosts name.
func shellWord(s string) string {
	if !strings.ContainsAny(s, " \t[]*?'\"$`\\") {
		return s
	}

	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Is reports whether target is ErrHostKeyMismatch.
func (e *HostKeyMismatchError) Is(target error) bool {
	return target == ErrHostKeyMismatch
}

// Unwrap returns the underlying knownhosts error.
func (e *HostKeyMismatchError) Unwrap() error {
	return e.Err
}
