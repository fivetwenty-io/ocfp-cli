// Package security provides path validation, input sanitization, and shell metacharacter detection.
package security

import (
	"errors"
	"fmt"
)

// Path validation errors.
var (
	ErrEmptyPath = errors.New("empty path")
)

// ErrPathContainsDangerousPattern returns an error for a path containing dangerous traversal or injection patterns.
func ErrPathContainsDangerousPattern(path string) error {
	return fmt.Errorf("path contains dangerous pattern: %s", path) //nolint:err113 // dynamic error with context
}

// ErrPathContainsShellMetacharacters returns an error for a path containing shell metacharacters.
func ErrPathContainsShellMetacharacters(path string) error {
	return fmt.Errorf("path contains shell metacharacters: %s", path) //nolint:err113 // dynamic error with context
}

// ErrPathDoesNotMatchPattern returns an error for a path that does not match the required validation pattern.
func ErrPathDoesNotMatchPattern(path string) error {
	return fmt.Errorf("path does not match required pattern: %s", path) //nolint:err113 // dynamic error with context
}

// ErrInvalidInput returns an error for input that fails validation checks.
func ErrInvalidInput(input string) error {
	return fmt.Errorf("invalid input: %s", input) //nolint:err113 // dynamic error with context
}

// ErrInputContainsShellMetacharacters returns an error for input containing shell metacharacters.
func ErrInputContainsShellMetacharacters(input string) error {
	return fmt.Errorf("input contains shell metacharacters: %s", input) //nolint:err113 // dynamic error with context
}
