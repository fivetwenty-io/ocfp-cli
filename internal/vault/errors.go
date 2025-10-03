package vault

import (
	"errors"
	"fmt"
	"strings"
)

// Vault authentication errors.
var (
	ErrTokenAuthRequiresVaultToken            = errors.New("token authentication requires VAULT_TOKEN to be set")
	ErrUsernameAndPasswordRequiredForUserpass = errors.New("username and password required for userpass auth")
	ErrNoAuthInfoReturned                     = errors.New("no auth info returned")
	ErrRoleIDAndSecretIDRequiredForApprole    = errors.New("role_id and secret_id required for approle auth")
	ErrInvalidTokenResponse                   = errors.New("invalid token response")
	ErrNoMountInformationReturned             = errors.New("no mount information returned")
	ErrEnvironmentNameRequired                = errors.New("environment name is required")
	ErrAtLeastOneSecretsProviderRequired      = errors.New("at least one secrets provider is required")
	ErrSecretsProviderNameRequired            = errors.New("secrets provider name is required")
	ErrSecretsProviderTypeRequired            = errors.New("secrets provider type is required")
	ErrVaultSecretsProviderRequiresURL        = errors.New("vault secrets provider requires URL")
	ErrVaultSecretsProviderRequiresAuthType   = errors.New("vault secrets provider requires auth type")
	ErrVaultSecretsProviderRequiresPath       = errors.New("vault secrets provider requires path")
	ErrPathCannotBeEmpty                      = errors.New("path cannot be empty")
	ErrNoTokenDataReturned                    = errors.New("no token data returned")
	ErrNoPoliciesFoundInToken                 = errors.New("no policies found in token")
	ErrInvalidPoliciesFormatInToken           = errors.New("invalid policies format in token")
	ErrNoAuthInformationInTokenResponse       = errors.New("no auth information in token response")
	ErrPasswordLengthMustBePositive           = errors.New("password length must be positive")
	ErrNoCharacterTypesSelectedForPassword    = errors.New("no character types selected for password generation")
	ErrEd25519KeyGenerationNotImplemented     = errors.New("ed25519 key generation not yet implemented")
	ErrNoUnsealKeysFoundInEnvVars             = errors.New("no unseal keys found in environment variables")
	ErrTimeoutWaitingForVaultReady            = errors.New("timeout waiting for vault to be ready")
	ErrBlocNameRequiredForVaultMigrate        = errors.New("bloc is required for vault migrate operation")
	ErrConnectionTimeout                      = errors.New("connection timeout")
	ErrAccessDenied                           = errors.New("access denied")
	ErrPathNotFound                           = errors.New("path not found")
	ErrNotAString                             = errors.New("not a string")
	ErrNotImplementedInMock                   = errors.New("not implemented in mock")
)

// Dynamic error constructors.
func ErrUnsupportedAuthType(authType string) error {
	return fmt.Errorf("unsupported auth type: %s", authType) //nolint:err113 // dynamic error with context
}

func ErrGenesisDirectoryNotFound(blocName string) error {
	return fmt.Errorf("genesis directory not found for bloc %s", blocName) //nolint:err113 // dynamic error with context
}

func ErrUnsupportedSecretsProviderType(providerType string) error {
	return fmt.Errorf("unsupported secrets provider type: %s", providerType) //nolint:err113 // dynamic error with context
}

func ErrNoGenesisDirectoryFor(blocName string) error {
	return fmt.Errorf("no genesis directory for bloc %s", blocName) //nolint:err113 // dynamic error with context
}

func ErrInvalidVaultPathFormat(path string) error {
	return fmt.Errorf("invalid vault path format: %s", path) //nolint:err113 // dynamic error with context
}

func ErrInvalidConfigPathFormat(path string) error {
	return fmt.Errorf("invalid config path format: %s", path) //nolint:err113 // dynamic error with context
}

func ErrTokenMissingRequiredPolicies(missing []string) error {
	return fmt.Errorf("token missing required policies: %s", strings.Join(missing, ", ")) //nolint:err113 // dynamic error with context
}

func ErrNoSecretFoundAtPath(path string) error {
	return fmt.Errorf("no secret found at path %s", path) //nolint:err113 // dynamic error with context
}

func ErrKeyNotFoundAtPath(key, path string) error {
	return fmt.Errorf("key '%s' not found at path %s", key, path) //nolint:err113 // dynamic error with context
}

func ErrUnexpectedDataTypeAtPath(path string) error {
	return fmt.Errorf("unexpected data type at path %s", path) //nolint:err113 // dynamic error with context
}

func ErrValueNotStringAtPath(path, key string) error {
	return fmt.Errorf("value at %s:%s is not a string", path, key) //nolint:err113 // dynamic error with context
}

func ErrUnsupportedKeyType(keyType string) error {
	return fmt.Errorf("unsupported key type: %s", keyType) //nolint:err113 // dynamic error with context
}

func ErrInvalidCIDRFormat(cidr string) error {
	return fmt.Errorf("invalid CIDR format: %s", cidr) //nolint:err113 // dynamic error with context
}

func ErrInvalidNetworkAddress(network string) error {
	return fmt.Errorf("invalid network address: %s", network) //nolint:err113 // dynamic error with context
}

func ErrInvalidFQDNsConfigType(envType string, envFQDNs interface{}) error {
	return fmt.Errorf("invalid FQDNs config type for %s: %T", envType, envFQDNs) //nolint:err113 // dynamic error with context
}

func ErrRollbackPartiallyFailed(errors []string) error {
	return fmt.Errorf("rollback partially failed: %s", strings.Join(errors, "; ")) //nolint:err113 // dynamic error with context
}

func ErrDynamicTestMessage(errMsg string) error {
	return fmt.Errorf("%s", errMsg) //nolint:err113 // dynamic error for testing
}

func ErrVaultStillSealedAfterKeys(providedKeys, needed, total int) error {
	return fmt.Errorf("vault still sealed after providing %d keys (needed %d out of %d)", providedKeys, needed, total) //nolint:err113 // dynamic error with context
}

func ErrUnknownSubcommand(subcommand string) error {
	return fmt.Errorf("unknown subcommand: %s", subcommand) //nolint:err113 // dynamic error with context
}

func ErrValidationFailedWithErrors(errorCount int) error {
	return fmt.Errorf("validation failed with %d errors", errorCount) //nolint:err113 // dynamic error with context
}

func ErrMigrationValidationFailedChecksumMismatch(inception, production string) error {
	return fmt.Errorf("migration validation failed: checksums do not match (inception: %s, production: %s)", inception, production) //nolint:err113 // dynamic error with context
}

func ErrUnsupportedProvider(provider string) error {
	return fmt.Errorf("unsupported provider: %s", provider) //nolint:err113 // dynamic error with context
}
