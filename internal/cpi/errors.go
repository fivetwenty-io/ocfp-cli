package cpi

import (
	"errors"
	"fmt"
)

// CPI errors.
var (
	ErrCircuitBreakerOpen = errors.New("circuit breaker is open")
)

// Dynamic error constructors.
func ErrProviderAlreadyRegistered(name string) error {
	return fmt.Errorf("provider %s already registered", name) //nolint:err113 // dynamic error with context
}

func ErrProviderNotFound(name string) error {
	return fmt.Errorf("provider %s not found", name) //nolint:err113 // dynamic error with context
}

func ErrTimeoutWaitingForCondition(timeout string) error {
	return fmt.Errorf("timeout waiting for condition after %v", timeout) //nolint:err113 // dynamic error with context
}
