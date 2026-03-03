package stackit

import (
	"errors"
	"fmt"
)

// STACKIT provider errors.
var (
	ErrCreateKeyPairUnsupported                   = errors.New("stackit: CreateKeyPair unsupported; use ImportKeyPair with a public key")
	ErrEnableBackendNotImplemented                = errors.New("stackit: EnableBackend not yet implemented")
	ErrDisableBackendNotImplemented               = errors.New("stackit: DisableBackend not yet implemented")
	ErrGetHealthStatusNotImplemented              = errors.New("stackit: GetHealthStatus not yet implemented")
	ErrSubnetsNotSupported                        = errors.New("stackit: subnets are not supported; use networks and labels")
	ErrSubnetsNotSupportedUseNetworksAndLabels    = errors.New("stackit: subnets are not supported")
	ErrNotImplemented                             = errors.New("not implemented")
	ErrProjectIDRequiredForStackitProvider        = errors.New("project_id is required for STACKIT provider")
	ErrOrgIDRequiredForStackitProvider            = errors.New("org_id is required for STACKIT provider")
	ErrStackitAuthenticationRequired              = errors.New("STACKIT provider requires either 'service_account_json', 'service_account_token' or 'auth_token' to be set")
	ErrCouldNotDetermineCreatedCredentialsGroupID = errors.New("could not determine created credentials group id")
	ErrBucketInfoMissing                          = errors.New("bucket info missing")
	ErrConfigIsRequired                           = errors.New("config is required")
	ErrNICCreatedButNoID                          = errors.New("NIC created but no ID returned")
	ErrNoNetworksFound                            = errors.New("no networks found")
	ErrNetworkInterfaceNotFound                   = errors.New("network interface not found in any network")
)

// ErrCouldNotFindServerAssociatedWithPublicIP returns an error when no server is associated with the given public IP.
func ErrCouldNotFindServerAssociatedWithPublicIP(ipID string) error {
	return fmt.Errorf("could not find server associated with public IP %s", ipID) //nolint:err113 // dynamic error with context
}

// ErrInvalidConfigTypeForStackitProvider returns an error for an unsupported configuration type.
func ErrInvalidConfigTypeForStackitProvider(config interface{}) error {
	return fmt.Errorf("invalid config type for STACKIT provider: %T", config) //nolint:err113 // dynamic error with context
}

// ErrBucketMetadataMissingInResponse returns an error when bucket metadata is absent from the API response.
func ErrBucketMetadataMissingInResponse(name string) error {
	return fmt.Errorf("bucket metadata missing in response for %s", name) //nolint:err113 // dynamic error with context
}

// ErrCredentialsGroupNotFoundAfterCreation returns an error when a newly created credentials group cannot be found.
func ErrCredentialsGroupNotFoundAfterCreation(displayName string) error {
	return fmt.Errorf("credentials group %q not found after creation", displayName) //nolint:err113 // dynamic error with context
}
