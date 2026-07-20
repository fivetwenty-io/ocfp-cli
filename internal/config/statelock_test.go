package config

import (
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
