package commands

import (
	"errors"
	"fmt"
)

// ExitError carries the process exit code for a command that has already
// written its own message to stderr, so the caller exits with Code and does
// not print Err again.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit code %d", e.Code)
	}

	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode returns the process exit code for err: 0 for nil, the code of the
// first *ExitError in its chain, and 1 for any other error. An *ExitError
// whose code is not positive gives 1, so a failure never exits 0.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}

	var ee *ExitError
	if errors.As(err, &ee) && ee.Code > 0 {
		return ee.Code
	}

	return 1
}
