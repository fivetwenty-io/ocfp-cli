package exec

import "errors"

// ErrNilContext is returned when a nil context is passed to RunWithEnv.
var ErrNilContext = errors.New("runwithenv: context must not be nil")

// ErrEmptyName is returned when an empty executable name is passed to RunWithEnv.
var ErrEmptyName = errors.New("runwithenv: executable name must not be empty")
