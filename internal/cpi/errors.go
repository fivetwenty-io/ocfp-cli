// Package cpi provides the cloud provider interface abstraction layer.
package cpi

import (
	"errors"
	"fmt"
)

// CPI errors.
var (
	ErrCircuitBreakerOpen = errors.New("circuit breaker is open")
)

// ErrProviderAlreadyRegistered returns an error indicating the given provider is already registered.
func ErrProviderAlreadyRegistered(name string) error {
	return fmt.Errorf("provider %s already registered", name) //nolint:err113 // dynamic error with context
}

// ErrProviderNotFound returns an error indicating the given provider was not found.
func ErrProviderNotFound(name string) error {
	return fmt.Errorf("provider %s not found", name) //nolint:err113 // dynamic error with context
}

// ErrTimeoutWaitingForCondition returns an error indicating the operation timed out waiting for a condition.
func ErrTimeoutWaitingForCondition(timeout string) error {
	return fmt.Errorf("timeout waiting for condition after %v", timeout) //nolint:err113 // dynamic error with context
}
