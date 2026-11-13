package config

import (
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/testenv"
	"github.com/stretchr/testify/assert"
)

// stateInTemporaryDirs points the state file at a temporary XDG state
// directory, so the resolvers pass and only the write point's own check can
// fire once the test guards the file itself.
func stateInTemporaryDirs(t *testing.T) {
	t.Helper()

	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func TestSaveStatePanicsForAStateFileUnderTheRealDirectories(t *testing.T) {
	stateInTemporaryDirs(t)

	path := StateFilePath()
	testenv.GuardOnly(t, path)

	assert.Contains(t, testenv.PanicMessage(func() { _ = SaveState(&StateFile{CurrentBloc: "x"}) }), "config.SaveState")
	assert.NoFileExists(t, path)
}

func TestWithStateLockPanicsForALockFileUnderTheRealDirectories(t *testing.T) {
	stateInTemporaryDirs(t)

	path := stateLockPath()
	testenv.GuardOnly(t, path)

	called := false

	message := testenv.PanicMessage(func() {
		_ = withStateLock(func() error {
			called = true

			return nil
		})
	})

	assert.Contains(t, message, "config state lock")
	assert.False(t, called, "the guard must stop the lock before the work runs")
	assert.NoFileExists(t, path)
}
