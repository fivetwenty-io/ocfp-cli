package vault

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"go.uber.org/zap"
)

const (
	// DefaultPasswordLength is the default number of characters for generated passwords.
	DefaultPasswordLength = 32
	// SimplePasswordLength is the number of characters for simplified password generation.
	SimplePasswordLength = 24

	// DefaultRSAKeySize is the default RSA key size in bits for SSH key generation.
	DefaultRSAKeySize = 2048
	// UUIDByteLength is the number of random bytes used for UUID generation.
	UUIDByteLength = 16
	// DefaultKeyLength is the default encryption key length in bytes.
	DefaultKeyLength = 32
	// JWTKeyLength is the key length in bytes for JWT signing secrets.
	JWTKeyLength = 64

	// UUIDVersion4 is the version 4 bit pattern for UUID generation.
	UUIDVersion4 = 0x40
	// UUIDVariant10 is the variant 10 bit pattern for UUID generation.
	UUIDVariant10 = 0x80
	// UUIDVersionMask masks the lower nibble for setting UUID version bits.
	UUIDVersionMask = 0x0f
	// UUIDVariantMask masks the lower 6 bits for setting UUID variant bits.
	UUIDVariantMask = 0x3f

	// BlobstoreKeyLength is the encryption key length in bytes for blobstore secrets.
	BlobstoreKeyLength = 32
	// DBKeyLength is the encryption key length in bytes for database secrets.
	DBKeyLength = 32
	// Encryption512Bit is the byte length for a 512-bit encryption key.
	Encryption512Bit = 64

	// DefaultInternalIP is the default internal IP address for BOSH director deployments.
	DefaultInternalIP = "10.0.0.6"
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
		Length:           DefaultPasswordLength,
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
		return "", ErrPasswordLengthMustBePositive
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
		return "", ErrNoCharacterTypesSelectedForPassword
	}

	// Generate password
	password := make([]byte, opts.Length)
	charsetLen := big.NewInt(int64(len(charset)))

	for index := range opts.Length {
		randomIndex, err := rand.Int(rand.Reader, charsetLen)
		if err != nil {
			return "", fmt.Errorf("failed to generate random number: %w", err)
		}

		password[index] = charset[randomIndex.Int64()]
	}

	sg.logger.Debugw("Generated password", "length", opts.Length)

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
func (sg *SecretGenerator) GenerateSSHKeyPair(opts *KeyPairOptions) (string, string, error) {
	if opts == nil {
		opts = &KeyPairOptions{
			KeyType: "rsa",
			KeySize: DefaultRSAKeySize,
			Comment: fmt.Sprintf("ocfp-generated-%d", time.Now().Unix()),
		}
	}

	switch strings.ToLower(opts.KeyType) {
	case "rsa":
		return sg.generateRSAKeyPair(opts)
	case "ed25519":
		return "", "", ErrEd25519KeyGenerationNotImplemented
	default:
		return "", "", ErrUnsupportedKeyType(opts.KeyType)
	}
}

// GenerateUUID generates a UUID-like string for identifiers.
func (sg *SecretGenerator) GenerateUUID() (string, error) {
	// Generate 16 random bytes
	bytes := make([]byte, UUIDByteLength)

	_, err := rand.Read(bytes)
	if err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Set version (4) and variant bits
	bytes[6] = (bytes[6] & UUIDVersionMask) | UUIDVersion4  // Version 4
	bytes[8] = (bytes[8] & UUIDVariantMask) | UUIDVariant10 // Variant 10

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]), nil
}

// GenerateEncryptionKey generates a random encryption key.
func (sg *SecretGenerator) GenerateEncryptionKey(length int) (string, error) {
	if length <= 0 {
		length = 32 // Default to 256-bit key
	}

	bytes := make([]byte, length)

	_, err := rand.Read(bytes)
	if err != nil {
		return "", fmt.Errorf("failed to generate encryption key: %w", err)
	}

	// Return as hex string
	return hex.EncodeToString(bytes), nil
}

// GenerateJWTSecret generates a secret suitable for JWT signing.
func (sg *SecretGenerator) GenerateJWTSecret() (string, error) {
	return sg.GenerateEncryptionKey(Encryption512Bit) // 512-bit secret
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
	sg.logger.Infow("Generating inception secrets", "deployment", deploymentName)

	secrets := sg.createInceptionSecretsTemplate(deploymentName)

	err := sg.generateCorePasswords(secrets)
	if err != nil {
		return nil, err
	}

	err = sg.generateDatabasePasswords(secrets)
	if err != nil {
		return nil, err
	}

	err = sg.generateServicePasswords(secrets)
	if err != nil {
		return nil, err
	}

	err = sg.generateEncryptionKeys(secrets)
	if err != nil {
		return nil, err
	}

	sg.logger.Infow("Successfully generated inception secrets", "deployment", deploymentName)

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
	sg.logger.Infow("Generating default secrets", "deployment", deploymentName)

	secrets := &DefaultSecrets{
		AdminPassword:            "",
		UAAAdminClientSecret:     "",
		CredhubAdminClientSecret: "",
		NatsPassword:             "",
		PostgresPassword:         "",
		BlobstoreSecret:          "",
		DeploymentName:           deploymentName,
		DirectorName:             deploymentName + "-bosh",
		InternalIP:               "10.0.0.6", // Default from Perl implementation
	}

	var err error

	secrets.AdminPassword, err = sg.GenerateSimplePassword(DefaultPasswordLength)
	if err != nil {
		return nil, fmt.Errorf("failed to generate admin password: %w", err)
	}

	secrets.UAAAdminClientSecret, err = sg.GenerateSimplePassword(DefaultPasswordLength)
	if err != nil {
		return nil, fmt.Errorf("failed to generate UAA admin client secret: %w", err)
	}

	secrets.CredhubAdminClientSecret, err = sg.GenerateSimplePassword(DefaultPasswordLength)
	if err != nil {
		return nil, fmt.Errorf("failed to generate Credhub admin client secret: %w", err)
	}

	secrets.NatsPassword, err = sg.GenerateSimplePassword(SimplePasswordLength)
	if err != nil {
		return nil, fmt.Errorf("failed to generate NATS password: %w", err)
	}

	secrets.PostgresPassword, err = sg.GenerateSimplePassword(SimplePasswordLength)
	if err != nil {
		return nil, fmt.Errorf("failed to generate Postgres password: %w", err)
	}

	secrets.BlobstoreSecret, err = sg.GenerateSimplePassword(DefaultPasswordLength)
	if err != nil {
		return nil, fmt.Errorf("failed to generate blobstore secret: %w", err)
	}

	sg.logger.Infow("Successfully generated default secrets", "deployment", deploymentName)

	return secrets, nil
}

func (sg *SecretGenerator) createInceptionSecretsTemplate(deploymentName string) *InceptionSecrets {
	return &InceptionSecrets{
		AdminPassword:          "",
		DirectorPassword:       "",
		PostgresPassword:       "",
		MySQLPassword:          "",
		NatsPassword:           "",
		RedisPassword:          "",
		RegistryPassword:       "",
		HealthMonitorPassword:  "",
		BlobstoreEncryptionKey: "",
		DBEncryptionKey:        "",
		DeploymentName:         deploymentName,
		InceptionDate:          time.Now().Format(time.RFC3339),
	}
}

func (sg *SecretGenerator) generateCorePasswords(secrets *InceptionSecrets) error {
	var err error

	secrets.AdminPassword, err = sg.GenerateSimplePassword(DefaultPasswordLength)
	if err != nil {
		return fmt.Errorf("failed to generate admin password: %w", err)
	}

	secrets.DirectorPassword, err = sg.GenerateSimplePassword(DefaultPasswordLength)
	if err != nil {
		return fmt.Errorf("failed to generate director password: %w", err)
	}

	return nil
}

func (sg *SecretGenerator) generateDatabasePasswords(secrets *InceptionSecrets) error {
	var err error

	secrets.PostgresPassword, err = sg.GenerateSimplePassword(SimplePasswordLength)
	if err != nil {
		return fmt.Errorf("failed to generate postgres password: %w", err)
	}

	secrets.MySQLPassword, err = sg.GenerateSimplePassword(SimplePasswordLength)
	if err != nil {
		return fmt.Errorf("failed to generate mysql password: %w", err)
	}

	return nil
}

func (sg *SecretGenerator) generateServicePasswords(secrets *InceptionSecrets) error {
	var err error

	secrets.NatsPassword, err = sg.GenerateSimplePassword(SimplePasswordLength)
	if err != nil {
		return fmt.Errorf("failed to generate nats password: %w", err)
	}

	secrets.RedisPassword, err = sg.GenerateSimplePassword(SimplePasswordLength)
	if err != nil {
		return fmt.Errorf("failed to generate redis password: %w", err)
	}

	secrets.RegistryPassword, err = sg.GenerateSimplePassword(SimplePasswordLength)
	if err != nil {
		return fmt.Errorf("failed to generate registry password: %w", err)
	}

	secrets.HealthMonitorPassword, err = sg.GenerateSimplePassword(SimplePasswordLength)
	if err != nil {
		return fmt.Errorf("failed to generate health monitor password: %w", err)
	}

	return nil
}

func (sg *SecretGenerator) generateEncryptionKeys(secrets *InceptionSecrets) error {
	var err error

	secrets.BlobstoreEncryptionKey, err = sg.GenerateEncryptionKey(BlobstoreKeyLength)
	if err != nil {
		return fmt.Errorf("failed to generate blobstore encryption key: %w", err)
	}

	secrets.DBEncryptionKey, err = sg.GenerateEncryptionKey(BlobstoreKeyLength)
	if err != nil {
		return fmt.Errorf("failed to generate db encryption key: %w", err)
	}

	return nil
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

// generateRSAKeyPair generates an RSA SSH key pair.
func (sg *SecretGenerator) generateRSAKeyPair(opts *KeyPairOptions) (string, string, error) {
	keySize := opts.KeySize
	if keySize == 0 {
		keySize = DefaultRSAKeySize
	}

	// Generate private key
	privateKey, err := rsa.GenerateKey(rand.Reader, keySize)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate RSA key: %w", err)
	}

	// Encode private key to PEM
	privateKeyPEM := &pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: nil,
		Bytes:   x509.MarshalPKCS1PrivateKey(privateKey),
	}
	privateKeyStr := string(pem.EncodeToMemory(privateKeyPEM))

	// Generate public key
	publicKeyRaw, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal public key: %w", err)
	}

	publicKeyPEM := &pem.Block{
		Type:    "PUBLIC KEY",
		Headers: nil,
		Bytes:   publicKeyRaw,
	}
	publicKeyStr := string(pem.EncodeToMemory(publicKeyPEM))

	sg.logger.Debugw("Generated RSA key pair", "size", keySize, "comment", opts.Comment)

	return publicKeyStr, privateKeyStr, nil
}
