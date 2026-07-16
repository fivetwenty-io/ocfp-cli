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
	ErrSafercMustBeInHomeDirectory            = errors.New("invalid path: .saferc must be in home directory")
)

// ErrUnsupportedAuthType returns an error for an unrecognized vault authentication type.
func ErrUnsupportedAuthType(authType string) error {
	return fmt.Errorf("unsupported auth type: %s", authType) //nolint:err113 // dynamic error with context
}

// ErrUnsupportedSecretsProviderType returns an error for an unrecognized secrets provider type.
func ErrUnsupportedSecretsProviderType(providerType string) error {
	return fmt.Errorf("unsupported secrets provider type: %s", providerType) //nolint:err113 // dynamic error with context
}

// ErrNoGenesisDirectoryFor returns an error when no Genesis directory exists for a bloc.
func ErrNoGenesisDirectoryFor(blocName string) error {
	return fmt.Errorf("no genesis directory for bloc %s", blocName) //nolint:err113 // dynamic error with context
}

// ErrInvalidVaultPathFormat returns an error for a malformed vault path.
func ErrInvalidVaultPathFormat(path string) error {
	return fmt.Errorf("invalid vault path format: %s", path) //nolint:err113 // dynamic error with context
}

// ErrInvalidConfigPathFormat returns an error for a malformed configuration path.
func ErrInvalidConfigPathFormat(path string) error {
	return fmt.Errorf("invalid config path format: %s", path) //nolint:err113 // dynamic error with context
}

// ErrTokenMissingRequiredPolicies returns an error listing the vault policies absent from the token.
func ErrTokenMissingRequiredPolicies(missing []string) error {
	return fmt.Errorf("token missing required policies: %s", strings.Join(missing, ", ")) //nolint:err113 // dynamic error with context
}

// ErrNoSecretFoundAtPath returns an error when no secret exists at the specified vault path.
func ErrNoSecretFoundAtPath(path string) error {
	return fmt.Errorf("no secret found at path %s", path) //nolint:err113 // dynamic error with context
}

// ErrKeyNotFoundAtPath returns an error when the requested key is missing at a vault path.
func ErrKeyNotFoundAtPath(key, path string) error {
	return fmt.Errorf("key '%s' not found at path %s", key, path) //nolint:err113 // dynamic error with context
}

// ErrUnexpectedDataTypeAtPath returns an error when the data at a vault path has an unexpected type.
func ErrUnexpectedDataTypeAtPath(path string) error {
	return fmt.Errorf("unexpected data type at path %s", path) //nolint:err113 // dynamic error with context
}

// ErrValueNotStringAtPath returns an error when a vault value is not a string as expected.
func ErrValueNotStringAtPath(path, key string) error {
	return fmt.Errorf("value at %s:%s is not a string", path, key) //nolint:err113 // dynamic error with context
}

// ErrUnsupportedKeyType returns an error for an unrecognized SSH key type.
func ErrUnsupportedKeyType(keyType string) error {
	return fmt.Errorf("unsupported key type: %s", keyType) //nolint:err113 // dynamic error with context
}

// ErrInvalidCIDRFormat returns an error for a malformed CIDR notation string.
func ErrInvalidCIDRFormat(cidr string) error {
	return fmt.Errorf("invalid CIDR format: %s", cidr) //nolint:err113 // dynamic error with context
}

// ErrInvalidNetworkAddress returns an error for a malformed network address.
func ErrInvalidNetworkAddress(network string) error {
	return fmt.Errorf("invalid network address: %s", network) //nolint:err113 // dynamic error with context
}

// ErrInvalidFQDNsConfigType returns an error when the FQDNs configuration has an unexpected type.
func ErrInvalidFQDNsConfigType(envType string, envFQDNs interface{}) error {
	return fmt.Errorf("invalid FQDNs config type for %s: %T", envType, envFQDNs) //nolint:err113 // dynamic error with context
}

// ErrRollbackPartiallyFailed returns an error listing the failures encountered during rollback.
func ErrRollbackPartiallyFailed(errors []string) error {
	return fmt.Errorf("rollback partially failed: %s", strings.Join(errors, "; ")) //nolint:err113 // dynamic error with context
}

// ErrDynamicTestMessage returns an error with the given message, used for testing dynamic error paths.
func ErrDynamicTestMessage(errMsg string) error {
	return fmt.Errorf("%s", errMsg) //nolint:err113 // dynamic error for testing
}

// ErrVaultStillSealedAfterKeys returns an error when the vault remains sealed after providing unseal keys.
func ErrVaultStillSealedAfterKeys(providedKeys, needed, total int) error {
	return fmt.Errorf("vault still sealed after providing %d keys (needed %d out of %d)", providedKeys, needed, total) //nolint:err113 // dynamic error with context
}

// ErrUnknownSubcommand returns an error for an unrecognized CLI subcommand.
func ErrUnknownSubcommand(subcommand string) error {
	return fmt.Errorf("unknown subcommand: %s", subcommand) //nolint:err113 // dynamic error with context
}

// ErrValidationFailedWithErrors returns an error indicating the number of validation failures.
func ErrValidationFailedWithErrors(errorCount int) error {
	return fmt.Errorf("validation failed with %d errors", errorCount) //nolint:err113 // dynamic error with context
}

// ErrMigrationValidationFailedChecksumMismatch returns an error when inception and production checksums differ.
func ErrMigrationValidationFailedChecksumMismatch(inception, production string) error {
	return fmt.Errorf("migration validation failed: checksums do not match (inception: %s, production: %s)", inception, production) //nolint:err113 // dynamic error with context
}

// ErrUnsupportedProvider returns an error for an unrecognized cloud provider name.
func ErrUnsupportedProvider(provider string) error {
	return fmt.Errorf("unsupported provider: %s", provider) //nolint:err113 // dynamic error with context
}

// ErrNoCurrentVaultSet returns an error when no vault target is configured in ~/.saferc.
func ErrNoCurrentVaultSet() error {
	return errors.New("no current vault set in ~/.saferc") //nolint:err113 // dynamic error with context
}

// ErrVaultNotFoundInSaferc returns an error when the named vault is not present in ~/.saferc.
func ErrVaultNotFoundInSaferc(vaultName string) error {
	return fmt.Errorf("vault %q not found in ~/.saferc", vaultName) //nolint:err113 // dynamic error with context
}

// ErrNoTokenFoundForVault returns an error when no authentication token exists for the named vault.
func ErrNoTokenFoundForVault(vaultName string) error {
	return fmt.Errorf("no token found for vault %q", vaultName) //nolint:err113 // dynamic error with context
}
