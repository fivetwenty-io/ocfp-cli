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
	ErrUnknownMergeStrategy = errors.New("invalid merge strategy")
	ErrProviderNil          = errors.New("provider cannot be nil")
	ErrStateManagerNil      = errors.New("state manager cannot be nil")
	ErrBlocNameEmpty        = errors.New("bloc name cannot be empty")
)

// ErrResourceNotFound returns an error indicating the specified resource key was not found in state.
func ErrResourceNotFound(key string) error {
	return fmt.Errorf("resource %s not found", key) //nolint:err113 // dynamic error with context
}

// ErrOutputNotFound returns an error indicating the specified output key was not found in state.
func ErrOutputNotFound(key string) error {
	return fmt.Errorf("output %s not found", key) //nolint:err113 // dynamic error with context
}

// ErrStateIsLockedBy returns an error indicating the state is locked by the given owner since the given time.
func ErrStateIsLockedBy(owner, createdAt interface{}) error {
	return fmt.Errorf("state is locked by %v at %v", owner, createdAt) //nolint:err113 // dynamic error with context
}
