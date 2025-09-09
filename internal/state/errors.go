package state

import (
	"errors"
	"fmt"
)

// State management errors.
var (
	ErrNoStateLoaded      = errors.New("no state loaded")
	ErrResourceIDRequired = errors.New("resource ID is required")
	ErrStateIsLocked      = errors.New("state is locked")
)

// Dynamic error constructors.
func ErrResourceNotFound(key string) error {
	return fmt.Errorf("resource %s not found", key) //nolint:err113 // dynamic error with context
}

func ErrOutputNotFound(key string) error {
	return fmt.Errorf("output %s not found", key) //nolint:err113 // dynamic error with context
}

func ErrStateIsLockedBy(owner, createdAt interface{}) error {
	return fmt.Errorf("state is locked by %v at %v", owner, createdAt) //nolint:err113 // dynamic error with context
}
