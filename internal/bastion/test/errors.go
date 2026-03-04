// Package test provides test helpers and mock implementations for bastion tests.
package test

import (
	"fmt"
)

// ErrDynamicTestError creates a dynamic error for testing purposes.
func ErrDynamicTestError(msg string) error {
	return fmt.Errorf("%s", msg) //nolint:err113 // dynamic error needed for testing
}
