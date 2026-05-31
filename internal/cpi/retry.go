package cpi

import (
	"context"
	"fmt"
	"time"
)

// WaitForCondition polls condition on the given interval until it returns true,
// the timeout elapses, or the context is cancelled.
func WaitForCondition(ctx context.Context, interval time.Duration, timeout time.Duration,
	condition func() (bool, error)) error {
	deadline := time.Now().Add(timeout)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		done, err := condition()
		if err != nil {
			return err
		}

		if done {
			return nil
		}

		if time.Now().After(deadline) {
			return ErrTimeoutWaitingForCondition(timeout.String())
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during wait condition: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
