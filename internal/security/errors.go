package security

import (
	"errors"
	"fmt"
)

// Path validation errors.
var (
	ErrEmptyPath = errors.New("empty path")
)

// Dynamic error constructors.
func ErrPathContainsDangerousPattern(path string) error {
	return fmt.Errorf("path contains dangerous pattern: %s", path) //nolint:err113 // dynamic error with context
}

func ErrPathContainsShellMetacharacters(path string) error {
	return fmt.Errorf("path contains shell metacharacters: %s", path) //nolint:err113 // dynamic error with context
}

func ErrPathDoesNotMatchPattern(path string) error {
	return fmt.Errorf("path does not match required pattern: %s", path) //nolint:err113 // dynamic error with context
}

func ErrInvalidInput(input string) error {
	return fmt.Errorf("invalid input: %s", input) //nolint:err113 // dynamic error with context
}

func ErrInputContainsShellMetacharacters(input string) error {
	return fmt.Errorf("input contains shell metacharacters: %s", input) //nolint:err113 // dynamic error with context
}
