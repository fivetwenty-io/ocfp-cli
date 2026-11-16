//go:build windows

package jobexec

import (
	"context"
	"errors"
)

// runFDsVerb has no descriptors to list on Windows.
func runFDsVerb(context.Context, Job, func() (string, error)) (int, error) {
	return 0, errors.New("the fds helper verb runs only on Unix")
}
