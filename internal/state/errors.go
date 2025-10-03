package state

import (
	"errors"
	"fmt"
)

// State management errors.
var (
	ErrNoStateLoaded        = errors.New("no state loaded")
	ErrResourceIDRequired   = errors.New("resource ID is required")
	ErrStateIsLocked        = errors.New("state is locked")
	ErrNoBackupsAvailable   = errors.New("no backups available for rollback")
	ErrCurrentStateNil      = errors.New("current state cannot be nil")
	ErrDiffSetNil           = errors.New("diff set cannot be nil")
	ErrUnknownMergeStrategy = errors.New("unknown merge strategy")
	ErrProviderNil          = errors.New("provider cannot be nil")
	ErrStateManagerNil      = errors.New("state manager cannot be nil")
	ErrBlocNameEmpty        = errors.New("bloc name cannot be empty")
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
