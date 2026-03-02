package providers

import (
	"errors"
	"fmt"
)

// Provider errors.
var (
	ErrAWSAccessKeyRequired       = errors.New("AWS access key ID is required")
	ErrAWSSecretKeyRequired       = errors.New("AWS secret access key is required")
	ErrAWSRegionRequired          = errors.New("AWS region is required")
	ErrCouldNotDetermineBastionIP = errors.New("could not determine bastion IP address")
	ErrRestoredPathEmpty          = errors.New("restored path is empty")
)

// Dynamic error constructors.
func ErrNoKeyFoundInConfig(keypairName string) error {
	return fmt.Errorf("no key found in config for %s", keypairName) //nolint:err113 // dynamic error with context
}
