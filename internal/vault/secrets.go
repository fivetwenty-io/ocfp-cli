package vault

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"go.uber.org/zap"
)

// SecretGenerator provides utilities for generating various types of secrets.
type SecretGenerator struct {
	logger *zap.SugaredLogger
}

// NewSecretGenerator creates a new secret generator.
func NewSecretGenerator() *SecretGenerator {
	return &SecretGenerator{
		logger: logger.Get(),
	}
}

// PasswordOptions holds options for password generation.
type PasswordOptions struct {
	Length           int
	IncludeUpper     bool
	IncludeLower     bool
	IncludeNumbers   bool
	IncludeSymbols   bool
	ExcludeAmbiguous bool
}

// DefaultPasswordOptions returns sensible defaults for password generation.
func DefaultPasswordOptions() *PasswordOptions {
	return &PasswordOptions{
		Length:           32,
		IncludeUpper:     true,
		IncludeLower:     true,
		IncludeNumbers:   true,
		IncludeSymbols:   true,
		ExcludeAmbiguous: true,
	}
}

// GeneratePassword generates a random password with specified options.
func (sg *SecretGenerator) GeneratePassword(opts *PasswordOptions) (string, error) {
	if opts == nil {
		opts = DefaultPasswordOptions()
	}

	if opts.Length <= 0 {
		return "", errors.New("password length must be positive")
	}

	// Build character set
	var charset string

	if opts.IncludeLower {
		if opts.ExcludeAmbiguous {
			charset += "abcdefghijkmnopqrstuvwxyz" // exclude l
		} else {
			charset += "abcdefghijklmnopqrstuvwxyz"
		}
	}

	if opts.IncludeUpper {
		if opts.ExcludeAmbiguous {
			charset += "ABCDEFGHJKLMNPQRSTUVWXYZ" // exclude I, O
		} else {
			charset += "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		}
	}

	if opts.IncludeNumbers {
		if opts.ExcludeAmbiguous {
			charset += "23456789" // exclude 0, 1
		} else {
			charset += "0123456789"
		}
	}

	if opts.IncludeSymbols {
		if opts.ExcludeAmbiguous {
			charset += "!@#$%^&*+-=" // exclude quotes, backticks, similar looking symbols
		} else {
			charset += "!@#$%^&*()_+-=[]{}|;:,.<>?"
		}
	}

	if charset == "" {
		return "", errors.New("no character types selected for password generation")
	}

	// Generate password
	password := make([]byte, opts.Length)
	charsetLen := big.NewInt(int64(len(charset)))

	for i := range opts.Length {
		randomIndex, err := rand.Int(rand.Reader, charsetLen)
		if err != nil {
			return "", fmt.Errorf("failed to generate random number: %w", err)
		}

		password[i] = charset[randomIndex.Int64()]
	}

	sg.logger.Debug("Generated password", "length", opts.Length)

	return string(password), nil
}

// GenerateSimplePassword generates a password with default settings.
func (sg *SecretGenerator) GenerateSimplePassword(length int) (string, error) {
	opts := DefaultPasswordOptions()
	opts.Length = length

	return sg.GeneratePassword(opts)
}

// KeyPairOptions holds options for SSH key generation.
type KeyPairOptions struct {
	KeyType string // rsa, ed25519
	KeySize int    // for RSA keys
	Comment string
}

// GenerateSSHKeyPair generates an SSH key pair.
func (sg *SecretGenerator) GenerateSSHKeyPair(opts *KeyPairOptions) (publicKey, privateKey string, err error) {
	if opts == nil {
		opts = &KeyPairOptions{
			KeyType: "rsa",
			KeySize: 2048,
			Comment: fmt.Sprintf("ocfp-generated-%d", time.Now().Unix()),
		}
	}

	switch strings.ToLower(opts.KeyType) {
	case "rsa":
		return sg.generateRSAKeyPair(opts)
	case "ed25519":
		return "", "", errors.New("ed25519 key generation not yet implemented")
	default:
		return "", "", fmt.Errorf("unsupported key type: %s", opts.KeyType)
	}
}

// generateRSAKeyPair generates an RSA SSH key pair.
func (sg *SecretGenerator) generateRSAKeyPair(opts *KeyPairOptions) (string, string, error) {
	keySize := opts.KeySize
	if keySize == 0 {
		keySize = 2048
	}

	// Generate private key
	privateKey, err := rsa.GenerateKey(rand.Reader, keySize)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate RSA key: %w", err)
	}

	// Encode private key to PEM
	privateKeyPEM := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}
	privateKeyStr := string(pem.EncodeToMemory(privateKeyPEM))

	// Generate public key
	publicKeyRaw, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal public key: %w", err)
	}

	publicKeyPEM := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicKeyRaw,
	}
	publicKeyStr := string(pem.EncodeToMemory(publicKeyPEM))

	sg.logger.Debug("Generated RSA key pair", "size", keySize, "comment", opts.Comment)

	return publicKeyStr, privateKeyStr, nil
}

// GenerateUUID generates a UUID-like string for identifiers.
func (sg *SecretGenerator) GenerateUUID() (string, error) {
	// Generate 16 random bytes
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Set version (4) and variant bits
	bytes[6] = (bytes[6] & 0x0f) | 0x40 // Version 4
	bytes[8] = (bytes[8] & 0x3f) | 0x80 // Variant 10

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]), nil
}

// GenerateEncryptionKey generates a random encryption key.
func (sg *SecretGenerator) GenerateEncryptionKey(length int) (string, error) {
	if length <= 0 {
		length = 32 // Default to 256-bit key
	}

	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate encryption key: %w", err)
	}

	// Return as hex string
	return hex.EncodeToString(bytes), nil
}

// GenerateJWTSecret generates a secret suitable for JWT signing.
func (sg *SecretGenerator) GenerateJWTSecret() (string, error) {
	return sg.GenerateEncryptionKey(64) // 512-bit secret
}

// InceptionSecrets holds the secrets generated for inception.
type InceptionSecrets struct {
	AdminPassword          string
	DirectorPassword       string
	PostgresPassword       string
	MySQLPassword          string
	NatsPassword           string
	RedisPassword          string
	RegistryPassword       string
	HealthMonitorPassword  string
	BlobstoreEncryptionKey string
	DBEncryptionKey        string
	DeploymentName         string
	InceptionDate          string
}

// GenerateInceptionSecrets generates all secrets needed for inception
// This matches the functionality in Perl's generateInceptionSecrets.
func (sg *SecretGenerator) GenerateInceptionSecrets(deploymentName string) (*InceptionSecrets, error) {
	sg.logger.Info("Generating inception secrets", "deployment", deploymentName)

	secrets := &InceptionSecrets{
		DeploymentName: deploymentName,
		InceptionDate:  time.Now().Format(time.RFC3339),
	}

	var err error

	// Core passwords
	if secrets.AdminPassword, err = sg.GenerateSimplePassword(32); err != nil {
		return nil, fmt.Errorf("failed to generate admin password: %w", err)
	}

	if secrets.DirectorPassword, err = sg.GenerateSimplePassword(32); err != nil {
		return nil, fmt.Errorf("failed to generate director password: %w", err)
	}

	// Database passwords
	if secrets.PostgresPassword, err = sg.GenerateSimplePassword(24); err != nil {
		return nil, fmt.Errorf("failed to generate postgres password: %w", err)
	}

	if secrets.MySQLPassword, err = sg.GenerateSimplePassword(24); err != nil {
		return nil, fmt.Errorf("failed to generate mysql password: %w", err)
	}

	// Service passwords
	if secrets.NatsPassword, err = sg.GenerateSimplePassword(24); err != nil {
		return nil, fmt.Errorf("failed to generate nats password: %w", err)
	}

	if secrets.RedisPassword, err = sg.GenerateSimplePassword(24); err != nil {
		return nil, fmt.Errorf("failed to generate redis password: %w", err)
	}

	if secrets.RegistryPassword, err = sg.GenerateSimplePassword(24); err != nil {
		return nil, fmt.Errorf("failed to generate registry password: %w", err)
	}

	if secrets.HealthMonitorPassword, err = sg.GenerateSimplePassword(24); err != nil {
		return nil, fmt.Errorf("failed to generate health monitor password: %w", err)
	}

	// Encryption keys
	if secrets.BlobstoreEncryptionKey, err = sg.GenerateEncryptionKey(32); err != nil {
		return nil, fmt.Errorf("failed to generate blobstore encryption key: %w", err)
	}

	if secrets.DBEncryptionKey, err = sg.GenerateEncryptionKey(32); err != nil {
		return nil, fmt.Errorf("failed to generate db encryption key: %w", err)
	}

	sg.logger.Info("Successfully generated inception secrets", "deployment", deploymentName)

	return secrets, nil
}

// ToMap converts InceptionSecrets to a map for vault storage.
func (is *InceptionSecrets) ToMap() map[string]interface{} {
	return map[string]interface{}{
		"admin_password":           is.AdminPassword,
		"director_password":        is.DirectorPassword,
		"postgres_password":        is.PostgresPassword,
		"mysql_password":           is.MySQLPassword,
		"nats_password":            is.NatsPassword,
		"redis_password":           is.RedisPassword,
		"registry_password":        is.RegistryPassword,
		"health_monitor_password":  is.HealthMonitorPassword,
		"blobstore_encryption_key": is.BlobstoreEncryptionKey,
		"db_encryption_key":        is.DBEncryptionKey,
		"deployment_name":          is.DeploymentName,
		"inception_date":           is.InceptionDate,
	}
}

// DefaultSecrets holds the default secrets for a deployment.
type DefaultSecrets struct {
	AdminPassword            string
	UAAAdminClientSecret     string
	CredhubAdminClientSecret string
	NatsPassword             string
	PostgresPassword         string
	BlobstoreSecret          string
	DeploymentName           string
	DirectorName             string
	InternalIP               string
}

// GenerateDefaultSecrets generates default secrets for a deployment
// This matches the functionality in Perl's generateDefaultSecrets.
func (sg *SecretGenerator) GenerateDefaultSecrets(deploymentName string) (*DefaultSecrets, error) {
	sg.logger.Info("Generating default secrets", "deployment", deploymentName)

	secrets := &DefaultSecrets{
		DeploymentName: deploymentName,
		DirectorName:   deploymentName + "-bosh",
		InternalIP:     "10.0.0.6", // Default from Perl implementation
	}

	var err error
	if secrets.AdminPassword, err = sg.GenerateSimplePassword(32); err != nil {
		return nil, fmt.Errorf("failed to generate admin password: %w", err)
	}

	if secrets.UAAAdminClientSecret, err = sg.GenerateSimplePassword(32); err != nil {
		return nil, fmt.Errorf("failed to generate UAA admin client secret: %w", err)
	}

	if secrets.CredhubAdminClientSecret, err = sg.GenerateSimplePassword(32); err != nil {
		return nil, fmt.Errorf("failed to generate Credhub admin client secret: %w", err)
	}

	if secrets.NatsPassword, err = sg.GenerateSimplePassword(24); err != nil {
		return nil, fmt.Errorf("failed to generate NATS password: %w", err)
	}

	if secrets.PostgresPassword, err = sg.GenerateSimplePassword(24); err != nil {
		return nil, fmt.Errorf("failed to generate Postgres password: %w", err)
	}

	if secrets.BlobstoreSecret, err = sg.GenerateSimplePassword(32); err != nil {
		return nil, fmt.Errorf("failed to generate blobstore secret: %w", err)
	}

	sg.logger.Info("Successfully generated default secrets", "deployment", deploymentName)

	return secrets, nil
}

// ToMap converts DefaultSecrets to a map for vault storage.
func (ds *DefaultSecrets) ToMap() map[string]interface{} {
	return map[string]interface{}{
		"admin_password":              ds.AdminPassword,
		"uaa_admin_client_secret":     ds.UAAAdminClientSecret,
		"credhub_admin_client_secret": ds.CredhubAdminClientSecret,
		"nats_password":               ds.NatsPassword,
		"postgres_password":           ds.PostgresPassword,
		"blobstore_secret":            ds.BlobstoreSecret,
		"deployment_name":             ds.DeploymentName,
		"director_name":               ds.DirectorName,
		"internal_ip":                 ds.InternalIP,
	}
}
