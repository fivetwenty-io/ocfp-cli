//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFlock answers each LOCK_EX call with the next error in errs, and nil
// once they run out. It counts those calls, and lets LOCK_UN through.
type fakeFlock struct {
	errs  []error
	calls int
}

func (f *fakeFlock) flock(_ int, how int) error {
	if how&syscall.LOCK_UN != 0 {
		return nil
	}

	f.calls++

	if len(f.errs) == 0 {
		return nil
	}

	err := f.errs[0]
	if len(f.errs) > 1 {
		f.errs = f.errs[1:]
	}

	return err
}

func openLockFile(t *testing.T) *os.File {
	t.Helper()

	file, err := os.OpenFile(filepath.Join(t.TempDir(), "state.yml.lock"), os.O_CREATE|os.O_RDWR, stateLockFileMode)
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })

	return file
}

// Only another holder is worth waiting for. An error such as ENOLCK, from a
// filesystem that can't lock at all, never clears, so it is reported as
// itself at once rather than as a timeout that blames a holder that isn't
// there.
func TestLockFile_ReportsAnErrorOtherThanBusyAtOnce(t *testing.T) {
	t.Parallel()

	fake := &fakeFlock{errs: []error{syscall.ENOLCK}}

	start := time.Now()
	_, err := lockFile(openLockFile(t), "state.yml.lock", time.Second, fake.flock)

	require.ErrorIs(t, err, syscall.ENOLCK)
	require.NotErrorIs(t, err, ErrStateLockTimeout)
	assert.Equal(t, 1, fake.calls)
	assert.Less(t, time.Since(start), 500*time.Millisecond)
}

func TestLockFile_RetriesAnInterruptedCall(t *testing.T) {
	t.Parallel()

	fake := &fakeFlock{errs: []error{syscall.EINTR, nil}}

	release, err := lockFile(openLockFile(t), "state.yml.lock", time.Second, fake.flock)
	require.NoError(t, err)

	release()
	assert.Equal(t, 2, fake.calls)
}

func TestLockFile_WaitsOutABusyLockThenTimesOut(t *testing.T) {
	t.Parallel()

	fake := &fakeFlock{errs: []error{syscall.EWOULDBLOCK}}

	_, err := lockFile(openLockFile(t), "state.yml.lock", 100*time.Millisecond, fake.flock)

	require.ErrorIs(t, err, ErrStateLockTimeout)
	assert.Greater(t, fake.calls, 1)
}
