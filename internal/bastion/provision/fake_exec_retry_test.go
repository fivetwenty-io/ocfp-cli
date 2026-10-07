package provision

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRetryWhileTextFileBusy_SucceedsAfterBusyAttempts(t *testing.T) {
	t.Parallel()

	calls := 0
	err := retryWhileTextFileBusy(func() error {
		calls++
		if calls <= 2 {
			return fmt.Errorf("fork/exec fake: %w", syscall.ETXTBSY)
		}

		return nil
	}, time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("retryWhileTextFileBusy() = %v, want nil", err)
	}

	if calls != 3 {
		t.Fatalf("run called %d times, want 3 (two busy, one success)", calls)
	}
}

func TestRetryWhileTextFileBusy_GivesUpWhenAlwaysBusy(t *testing.T) {
	t.Parallel()

	calls := 0
	err := retryWhileTextFileBusy(func() error {
		calls++

		return fmt.Errorf("fork/exec fake: %w", syscall.ETXTBSY)
	}, 30*time.Millisecond, time.Millisecond)

	if !errors.Is(err, syscall.ETXTBSY) {
		t.Fatalf("retryWhileTextFileBusy() = %v, want an ETXTBSY error", err)
	}

	if calls < 2 {
		t.Fatalf("run called %d times, want it retried before giving up", calls)
	}
}

func TestRetryWhileTextFileBusy_DoesNotRetryOtherErrors(t *testing.T) {
	t.Parallel()

	calls := 0
	err := retryWhileTextFileBusy(func() error {
		calls++

		return errors.New("exit status 1")
	}, time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("retryWhileTextFileBusy() = %v, want nil for a non-busy failure", err)
	}

	if calls != 1 {
		t.Fatalf("run called %d times, want 1", calls)
	}
}

// TestWriteFakeExecutableFile_ProbeSkipsBody proves the settle run returns
// before the fake's own body, so a body that records its calls never sees it.
func TestWriteFakeExecutableFile_ProbeSkipsBody(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	path := filepath.Join(dir, "fake")

	writeFakeExecutableFile(t, path, "#!/bin/sh\necho ran >> '"+record+"'\n")

	if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the probe ran the fake's body (stat err = %v)", err)
	}
}
