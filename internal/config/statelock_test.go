package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAcquireStateLock_TimesOutRatherThanHanging is the anti-wedge guarantee:
// a stuck or crashed lock holder must produce one clean error, never six
// agents blocked forever.
func TestAcquireStateLock_TimesOutRatherThanHanging(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.yml.lock")

	release, err := acquireStateLock(path, time.Second)
	require.NoError(t, err)

	defer release()

	start := time.Now()
	_, err = acquireStateLock(path, 150*time.Millisecond)
	elapsed := time.Since(start)

	require.ErrorIs(t, err, ErrStateLockTimeout)
	assert.Less(t, elapsed, 2*time.Second, "acquisition must be bounded, not an unbounded block")
}

// The lock must be reusable once released, or the first bootstrap of the day
// would lock out every command that follows it.
func TestAcquireStateLock_ReacquirableAfterRelease(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.yml.lock")

	release, err := acquireStateLock(path, time.Second)
	require.NoError(t, err)
	release()

	second, err := acquireStateLock(path, time.Second)
	require.NoError(t, err)
	second()
}

// withStateLock must still run its critical section when there is no OCFP home
// to place a lock file in, rather than failing the caller.
func TestWithStateLock_NoOcfpHomeStillRuns(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	ran := false

	err := withStateLock(func() error {
		ran = true

		return nil
	})

	require.NoError(t, err)
	assert.True(t, ran)
}

// A second run for the same lock waits and then gives up with a clear error,
// and the critical section it guards never runs.
func TestWithFileLock_SamePathWaitsThenTimesOut(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "bloc", "inception-vault.lock")

	err := WithFileLock(path, time.Second, func() error {
		ran := false
		start := time.Now()

		inner := WithFileLock(path, 150*time.Millisecond, func() error {
			ran = true

			return nil
		})

		require.ErrorIs(t, inner, ErrFileLockTimeout)
		assert.Contains(t, inner.Error(), path)
		assert.GreaterOrEqual(t, time.Since(start), 150*time.Millisecond, "the second run must wait before it gives up")
		assert.False(t, ran)

		return nil
	})
	require.NoError(t, err)
}

// Different blocs never wait for each other.
func TestWithFileLock_DifferentPathsDoNotWait(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	err := WithFileLock(filepath.Join(dir, "a", "inception-vault.lock"), time.Second, func() error {
		start := time.Now()
		ran := false

		inner := WithFileLock(filepath.Join(dir, "b", "inception-vault.lock"), time.Second, func() error {
			ran = true

			return nil
		})

		require.NoError(t, inner)
		assert.True(t, ran)
		assert.Less(t, time.Since(start), 500*time.Millisecond)

		return nil
	})
	require.NoError(t, err)
}

// The lock is released whether the critical section succeeds or fails, and
// the section's own error comes back unchanged.
func TestWithFileLock_ReleasesAndReturnsTheSectionsError(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "inception-vault.lock")
	boom := errors.New("boom")

	require.ErrorIs(t, WithFileLock(path, time.Second, func() error { return boom }), boom)
	require.NoError(t, WithFileLock(path, 100*time.Millisecond, func() error { return nil }))

	info, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}
