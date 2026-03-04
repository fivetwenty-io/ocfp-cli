package bootstrap

import (
	"errors"
	"fmt"
)

// Bootstrap errors.
var (
	ErrNoSuitableSubnetFoundForBastion = errors.New("no suitable subnet found for bastion; ensure subnets phase created at least one subnet")
	errImageNotFound                   = errors.New("image not found")
	errNoSuitableFlavor                = errors.New("no suitable flavor found")
	errNoFlavorWithDisk                = errors.New("no flavor found with sufficient disk space")
	errBastionPublicIPNotFound         = errors.New("bastion public IP not found in state")
	errNetworkManagerNotAvailable      = errors.New("network manager not available")
	errEmptyPrivateKey                 = errors.New("cannot save empty private key")
)

// ErrInvalidNetworkIDInState returns an error for an invalid network_id value found in state.
func ErrInvalidNetworkIDInState(networkID interface{}) error {
	return fmt.Errorf("invalid network_id in state: %v", networkID) //nolint:err113 // dynamic error with context
}

// ErrInvalidSGBastionIDInState returns an error for an invalid sg_bastion_id value found in state.
func ErrInvalidSGBastionIDInState(sgID interface{}) error {
	return fmt.Errorf("invalid sg_bastion_id in state: %v", sgID) //nolint:err113 // dynamic error with context
}

// ErrBucketCreationErrors returns an error wrapping multiple bucket creation failures.
func ErrBucketCreationErrors(errs []error) error {
	return fmt.Errorf("bucket creation errors: %v", errs) //nolint:err113 // dynamic error with context
}

// ErrInvalidNetworkID returns an error for an invalid network identifier.
func ErrInvalidNetworkID(networkID interface{}) error {
	return fmt.Errorf("invalid network ID: %v", networkID) //nolint:err113 // dynamic error with context
}

// ErrBastionSubnetNotFound returns an error when the specified bastion subnet cannot be found.
func ErrBastionSubnetNotFound(bastionSubnet string) error {
	return fmt.Errorf("bastion subnet %s not found", bastionSubnet) //nolint:err113 // dynamic error with context
}

// ErrBastionSecurityGroupNotFound returns an error when the bastion security group cannot be found.
func ErrBastionSecurityGroupNotFound(sgName string) error {
	return fmt.Errorf("bastion security group %s not found", sgName) //nolint:err113 // dynamic error with context
}

// ErrInvalidCIDRTypeForSubnet returns an error for an invalid CIDR type on a subnet.
func ErrInvalidCIDRTypeForSubnet(subnetName string) error {
	return fmt.Errorf("invalid CIDR type for subnet %s", subnetName) //nolint:err113 // dynamic error with context
}

// ErrInvalidCIDRTypeForResource returns an error for an invalid CIDR type on a resource.
func ErrInvalidCIDRTypeForResource(resourceName string) error {
	return fmt.Errorf("invalid CIDR type for resource %s", resourceName) //nolint:err113 // dynamic error with context
}
