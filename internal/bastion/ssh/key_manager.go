package ssh

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/security"
	"golang.org/x/crypto/ssh"
)

// KeyManager handles SSH key discovery and authentication.
type KeyManager struct {
	log logger.Logger
}

// NewKeyManager creates a new key manager.
func NewKeyManager() *KeyManager {
	return &KeyManager{
		log: logger.Get(),
	}
}

// FindPrivateKey discovers SSH private keys using the search order from Perl implementation.
func (km *KeyManager) FindPrivateKey(blocName string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	// Search paths in priority order
	searchPaths := []string{
		// Provider-specific key (bastion key from bootstrap)
		filepath.Join(homeDir, ".ssh", blocName+"-bastion"),
		// Common SSH keys
		filepath.Join(homeDir, ".ssh", "id_rsa"),
		filepath.Join(homeDir, ".ssh", "id_ecdsa"),
		filepath.Join(homeDir, ".ssh", "id_ed25519"),
		filepath.Join(homeDir, ".ssh", "id_dsa"),
	}

	km.log.Debug("Searching for SSH private keys")

	for _, path := range searchPaths {
		if _, err := os.Stat(path); err == nil {
			km.log.Debug("Found SSH key", "path", path)

			// Verify the key is valid
			if km.isValidPrivateKey(path) {
				km.log.Info("Using SSH private key", "path", path)

				return path, nil
			} else {
				km.log.Warn("Invalid SSH key", "path", path)
			}
		}
	}

	return "", errors.New("no valid SSH private key found")
}

// CreatePublicKeyAuth creates an SSH public key authentication method.
//nolint:ireturn // returning ssh.AuthMethod interface is intentional (crypto/ssh API)
func (km *KeyManager) CreatePublicKeyAuth(keyPath, passphrase string) (ssh.AuthMethod, error) {
	if err := security.ValidateSSHKeyPath(keyPath); err != nil {
		return nil, fmt.Errorf("invalid key path: %w", err)
	}

	keyData, err := os.ReadFile(keyPath) // #nosec G304 - path validated above
	if err != nil {
		return nil, fmt.Errorf("failed to read key file: %w", err)
	}

	var signer ssh.Signer

	if km.isKeyPasswordProtected(keyData) {
		if passphrase == "" {
			return nil, errors.New("private key is encrypted but no passphrase provided")
		}

		signer, err = ssh.ParsePrivateKeyWithPassphrase(keyData, []byte(passphrase))
		if err != nil {
			return nil, fmt.Errorf("failed to parse encrypted private key: %w", err)
		}
	} else {
		signer, err = ssh.ParsePrivateKey(keyData)
		if err != nil {
			return nil, fmt.Errorf("failed to parse private key: %w", err)
		}
	}

	return ssh.PublicKeys(signer), nil
}

// IsKeyPasswordProtected checks if an SSH key is password protected.
func (km *KeyManager) IsKeyPasswordProtected(keyPath string) (bool, error) {
	if err := security.ValidateSSHKeyPath(keyPath); err != nil {
		return false, fmt.Errorf("invalid key path: %w", err)
	}

	keyData, err := os.ReadFile(keyPath) // #nosec G304 - path validated above
	if err != nil {
		return false, fmt.Errorf("failed to read key file: %w", err)
	}

	return km.isKeyPasswordProtected(keyData), nil
}

// GetKeyFingerprint returns the fingerprint of an SSH key.
func (km *KeyManager) GetKeyFingerprint(keyPath string) (string, error) {
	if err := security.ValidateSSHKeyPath(keyPath); err != nil {
		return "", fmt.Errorf("invalid key path: %w", err)
	}

	keyData, err := os.ReadFile(keyPath) // #nosec G304 - path validated above
	if err != nil {
		return "", fmt.Errorf("failed to read key file: %w", err)
	}

	// Parse the private key to get the public key
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return "", fmt.Errorf("failed to parse private key: %w", err)
	}

	publicKey := signer.PublicKey()
	fingerprint := ssh.FingerprintSHA256(publicKey)

	return fingerprint, nil
}

// ValidateKeyPair verifies that a private key matches its public key.
func (km *KeyManager) ValidateKeyPair(privateKeyPath, publicKeyPath string) error {
	// Read and parse private key
	if err := security.ValidateSSHKeyPath(privateKeyPath); err != nil {
		return fmt.Errorf("invalid private key path: %w", err)
	}

	privateKeyData, err := os.ReadFile(privateKeyPath) // #nosec G304 - path validated above
	if err != nil {
		return fmt.Errorf("failed to read private key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(privateKeyData)
	if err != nil {
		return fmt.Errorf("failed to parse private key: %w", err)
	}

	// Read and parse public key
	if err := security.ValidateSSHKeyPath(publicKeyPath); err != nil {
		return fmt.Errorf("invalid public key path: %w", err)
	}

	publicKeyData, err := os.ReadFile(publicKeyPath) // #nosec G304 - path validated above
	if err != nil {
		return fmt.Errorf("failed to read public key: %w", err)
	}

    publicKey, _, _, _, err := ssh.ParseAuthorizedKey(publicKeyData) //nolint:dogsled // only the publicKey and error are needed
	if err != nil {
		return fmt.Errorf("failed to parse public key: %w", err)
	}

	// Compare public keys
	if !keysEqual(signer.PublicKey(), publicKey) {
		return errors.New("private and public keys do not match")
	}

	return nil
}

// keysEqual compares two SSH public keys for equality.
func keysEqual(key1, key2 ssh.PublicKey) bool {
	if key1 == nil || key2 == nil {
		return key1 == key2
	}

	// Compare key types
	if key1.Type() != key2.Type() {
		return false
	}

	// Compare marshaled key data
	return bytes.Equal(key1.Marshal(), key2.Marshal())
}

// GenerateKeyPair generates a new SSH key pair.
func (km *KeyManager) GenerateKeyPair(keyPath, keyType string, keySize int) error {
	km.log.Info("Generating SSH key pair",
		"path", keyPath,
		"type", keyType,
		"size", keySize)

	// Ensure SSH directory exists
	sshDir := filepath.Dir(keyPath)
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("failed to create SSH directory: %w", err)
	}

	var (
		privateKey interface{}
		err        error
	)

	// Generate key based on type

	switch strings.ToLower(keyType) {
	case "ed25519":
		// Ed25519 keys don't use key size parameter
		_, privateKey, err = ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return fmt.Errorf("failed to generate Ed25519 key: %w", err)
		}

	case "rsa":
		if keySize == 0 {
			keySize = 4096 // Default RSA key size
		}

		if keySize < 2048 {
			return errors.New("RSA key size must be at least 2048 bits")
		}

		privateKey, err = rsa.GenerateKey(rand.Reader, keySize)
		if err != nil {
			return fmt.Errorf("failed to generate RSA key: %w", err)
		}

	default:
		return fmt.Errorf("unsupported key type: %s (supported: rsa, ed25519)", keyType)
	}

	// Convert to SSH format
	sshPrivateKey, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		return fmt.Errorf("failed to create SSH signer: %w", err)
	}

	// Get public key
	sshPublicKey := sshPrivateKey.PublicKey()

	// Write private key
	if err := km.writePrivateKey(keyPath, privateKey, keyType); err != nil {
		return fmt.Errorf("failed to write private key: %w", err)
	}

	// Write public key
	publicKeyPath := keyPath + ".pub"
	if err := km.writePublicKey(publicKeyPath, sshPublicKey); err != nil {
		return fmt.Errorf("failed to write public key: %w", err)
	}

	km.log.Info("SSH key pair generated successfully",
		"private_key", keyPath,
		"public_key", publicKeyPath,
		"fingerprint", ssh.FingerprintSHA256(sshPublicKey))

	return nil
}

// writePrivateKey writes a private key to file in the appropriate format.
func (km *KeyManager) writePrivateKey(keyPath string, privateKey interface{}, keyType string) error {
	var (
		keyBytes  []byte
		keyFormat string
	)

	switch keyType {
	case "ed25519":
		// Ed25519 private key
		ed25519Key, ok := privateKey.(ed25519.PrivateKey)
		if !ok {
			return errors.New("invalid Ed25519 private key type")
		}

		keyBytes, _ = x509.MarshalPKCS8PrivateKey(ed25519Key)
		keyFormat = "PRIVATE KEY"

	case "rsa":
		// RSA private key
		rsaKey, ok := privateKey.(*rsa.PrivateKey)
		if !ok {
			return errors.New("invalid RSA private key type")
		}

		keyBytes = x509.MarshalPKCS1PrivateKey(rsaKey)
		keyFormat = "RSA PRIVATE KEY"

	default:
		return fmt.Errorf("unsupported key type for writing: %s", keyType)
	}

	// Create PEM block
	keyBlock := &pem.Block{
		Type:  keyFormat,
		Bytes: keyBytes,
	}

	// Write to file
	if err := security.ValidateSSHKeyPath(keyPath); err != nil {
		return fmt.Errorf("invalid key path: %w", err)
	}

	keyFile, err := os.OpenFile(keyPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600) // #nosec G304 - path validated above
	if err != nil {
		return fmt.Errorf("failed to create private key file: %w", err)
	}

	defer func() {
		err := keyFile.Close()
		if err != nil {
			km.log.Warn("Failed to close private key file", "error", err.Error())
		}
	}()

	if err := pem.Encode(keyFile, keyBlock); err != nil {
		return fmt.Errorf("failed to encode private key: %w", err)
	}

	return nil
}

// writePublicKey writes a public key to file in OpenSSH format.
func (km *KeyManager) writePublicKey(keyPath string, publicKey ssh.PublicKey) error {
	// Format public key in OpenSSH format
	keyData := ssh.MarshalAuthorizedKey(publicKey)

	// Write to file
	err := os.WriteFile(keyPath, keyData, 0600)
	if err != nil {
		return fmt.Errorf("failed to write public key: %w", err)
	}

	return nil
}

// isValidPrivateKey checks if a file contains a valid SSH private key.
func (km *KeyManager) isValidPrivateKey(keyPath string) bool {
	if err := security.ValidateSSHKeyPath(keyPath); err != nil {
		return false
	}

	keyData, err := os.ReadFile(keyPath) // #nosec G304 - path validated above
	if err != nil {
		return false
	}

	// Try to parse as SSH private key
	_, err = ssh.ParsePrivateKey(keyData)

	return err == nil
}

// isKeyPasswordProtected checks if key data represents an encrypted key.
func (km *KeyManager) isKeyPasswordProtected(keyData []byte) bool {
	// Check for encrypted key markers
	keyStr := string(keyData)

	// Common encrypted key indicators
	encryptedMarkers := []string{
		"ENCRYPTED",
		"Proc-Type: 4,ENCRYPTED",
		"BEGIN ENCRYPTED PRIVATE KEY",
	}

	for _, marker := range encryptedMarkers {
		if strings.Contains(keyStr, marker) {
			return true
		}
	}

	// Parse PEM block to check for encryption
	block, _ := pem.Decode(keyData)
	if block == nil {
		return false
	}

	// Check PEM headers for encryption
	if procType, ok := block.Headers["Proc-Type"]; ok {
		if strings.Contains(procType, "ENCRYPTED") {
			return true
		}
	}

	// Check for PKCS#8 encrypted key
	if block.Type == "ENCRYPTED PRIVATE KEY" {
		return true
	}

	// Try to parse the key - if it fails with decryption error, it's encrypted
	_, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		// Check if error suggests encryption
		errStr := err.Error()
		if strings.Contains(errStr, "decrypt") ||
			strings.Contains(errStr, "password") ||
			strings.Contains(errStr, "encrypted") {
			return true
		}
	}

	return false
}

// LoadAuthorizedKeys loads authorized keys from a file.
func (km *KeyManager) LoadAuthorizedKeys(authorizedKeysPath string) ([]ssh.PublicKey, error) {
	if err := security.ValidateSSHKeyPath(authorizedKeysPath); err != nil {
		return nil, fmt.Errorf("invalid authorized keys path: %w", err)
	}

	file, err := os.Open(authorizedKeysPath) // #nosec G304 - authorizedKeysPath is validated above
	if err != nil {
		return nil, fmt.Errorf("failed to open authorized_keys file: %w", err)
	}

	defer func() {
		err := file.Close()
		if err != nil {
			km.log.Debug("Failed to close authorized_keys file", "error", err.Error())
		}
	}()

	var publicKeys []ssh.PublicKey

	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

        publicKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line)) //nolint:dogsled // only the publicKey and error are needed
		if err != nil {
			km.log.Warn("Failed to parse authorized key",
				"line", line,
				"error", err.Error())

			continue
		}

		publicKeys = append(publicKeys, publicKey)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading authorized_keys file: %w", err)
	}

	return publicKeys, nil
}

// FormatPublicKey formats a public key for authorized_keys file.
func (km *KeyManager) FormatPublicKey(publicKey ssh.PublicKey, comment string) string {
	keyData := ssh.MarshalAuthorizedKey(publicKey)
	keyStr := strings.TrimSpace(string(keyData))

	if comment != "" {
		keyStr += " " + comment
	}

	return keyStr
}
