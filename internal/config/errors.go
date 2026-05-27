package config

import (
	"errors"
	"fmt"
)

// Config errors.
var (
	ErrProviderOrIaasRequired = errors.New("provider or iaas must be specified")
	ErrNoConfigPath           = errors.New("no config file path available")

	// ErrNoConfigFile is returned when no configuration file is found at the
	// default locations and no explicit path was provided.
	ErrNoConfigFile = errors.New("no configuration file found; use ~/.ocfp/config.yml or specify -f configfile.yml")

	// ErrNoBlocName is returned when LoadWithParams is called without a bloc name.
	ErrNoBlocName = errors.New("no bloc name provided")

	// ErrPVEAuthRequired is returned when a PVE bloc has neither API token auth
	// (auth_token + token_secret) nor user/password auth (username + password)
	// configured. At least one complete auth mode is required.
	ErrPVEAuthRequired = errors.New("pve config: at least one auth mode required: set (auth_token + token_secret) or (username + password)")

	// ErrPVEVMIDRangeInvalid is returned when the configured vmid_range_start
	// and vmid_range_end values are inconsistent: end must be greater than start,
	// both must be positive, and end must not exceed the PVE maximum (999999999).
	ErrPVEVMIDRangeInvalid = errors.New("pve config: vmid_range_end must be > vmid_range_start > 0 and <= 999999999")
)

// ErrBlocNotFound returns an error indicating the specified bloc was not found in the configuration file.
func ErrBlocNotFound(blocName, configPath string) error {
	return fmt.Errorf("bloc '%s' not found in configuration file %s", blocName, configPath) //nolint:err113 // dynamic error with context
}

// ErrInvalidProvider returns an error for an unrecognized or unsupported cloud provider.
func ErrInvalidProvider(provider string) error {
	return fmt.Errorf("invalid provider: %s", provider) //nolint:err113 // dynamic error with context
}
