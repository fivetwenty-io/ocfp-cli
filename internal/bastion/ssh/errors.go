package ssh

import (
	"errors"
	"fmt"
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
