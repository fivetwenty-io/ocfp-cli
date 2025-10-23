package config

import (
	"errors"
	"fmt"
)

// Config errors.
var (
	ErrProviderOrIaasRequired = errors.New("provider or iaas must be specified")
	ErrNoConfigPath           = errors.New("no config file path available")
)

// Dynamic error constructors.
func ErrBlocNotFound(blocName, configPath string) error {
	return fmt.Errorf("bloc '%s' not found in configuration file %s", blocName, configPath) //nolint:err113 // dynamic error with context
}

func ErrInvalidProvider(provider string) error {
	return fmt.Errorf("invalid provider: %s", provider) //nolint:err113 // dynamic error with context
}
