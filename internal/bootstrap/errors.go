package bootstrap

import (
	"errors"
	"fmt"
)

// Bootstrap errors.
var (
	ErrNoSuitableSubnetFoundForBastion = errors.New("no suitable subnet found for bastion; ensure subnets phase created at least one subnet")
)

// Dynamic error constructors.
func ErrInvalidNetworkIDInState(networkID interface{}) error {
	return fmt.Errorf("invalid network_id in state: %v", networkID) //nolint:err113 // dynamic error with context
}

func ErrInvalidSGBastionIDInState(sgID interface{}) error {
	return fmt.Errorf("invalid sg_bastion_id in state: %v", sgID) //nolint:err113 // dynamic error with context
}

func ErrBucketCreationErrors(errs []error) error {
	return fmt.Errorf("bucket creation errors: %v", errs) //nolint:err113 // dynamic error with context
}

func ErrInvalidNetworkID(networkID interface{}) error {
	return fmt.Errorf("invalid network ID: %v", networkID) //nolint:err113 // dynamic error with context
}

func ErrBastionSubnetNotFound(bastionSubnet string) error {
	return fmt.Errorf("bastion subnet %s not found", bastionSubnet) //nolint:err113 // dynamic error with context
}

func ErrBastionSecurityGroupNotFound(sgName string) error {
	return fmt.Errorf("bastion security group %s not found", sgName) //nolint:err113 // dynamic error with context
}

func ErrInvalidCIDRTypeForSubnet(subnetName string) error {
	return fmt.Errorf("invalid CIDR type for subnet %s", subnetName) //nolint:err113 // dynamic error with context
}

func ErrInvalidCIDRTypeForResource(resourceName string) error {
	return fmt.Errorf("invalid CIDR type for resource %s", resourceName) //nolint:err113 // dynamic error with context
}
