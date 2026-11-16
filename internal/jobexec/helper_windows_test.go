//go:build windows

package jobexec

import (
	"context"
	"errors"
)

// runTreeVerb needs a lock that a child holds, which only Unix gives it.
func runTreeVerb(context.Context, Job, func() (string, error)) (int, error) {
	return 0, errors.New("the tree helper verb runs only on Unix")
}

// holdLock is the lock helper, which runs only on Unix.
func holdLock() int {
	return 3
}

// runFDsVerb has no descriptors to list on Windows.
func runFDsVerb(context.Context, Job, func() (string, error)) (int, error) {
	return 0, errors.New("the fds helper verb runs only on Unix")
}
