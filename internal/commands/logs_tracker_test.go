package commands

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogsTracker_ActiveDirResolvesUnderStateHomeBaseDir asserts that a
// CommandTracker built from getLogsBaseDir's result writes its lock files
// under StateHome()/.active rather than the pre-migration flat
// OcfpHome()/.active directory, proving the tracker's ".active" lock
// directory correctly inherits the corrected baseDir producer instead of
// resolving to a hardcoded legacy location.
func TestLogsTracker_ActiveDirResolvesUnderStateHomeBaseDir(t *testing.T) {
	isolateXDGHome(t)

	xdgStateBase := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdgStateBase)

	baseDir, err := getLogsBaseDir()
	require.NoError(t, err)
	assert.Equal(t, config.StateHome(), baseDir)

	tracker := NewCommandTracker(baseDir)

	info := ActiveCommand{
		Timestamp:  time.Date(2025, 10, 22, 15, 36, 3, 0, time.UTC),
		PID:        os.Getpid(),
		Bloc:       "test-bloc",
		Command:    "deploy",
		Subcommand: "",
		LogPath:    filepath.Join(baseDir, "test-bloc", "logs", "deploy", "20251022-153603.log"),
	}

	require.NoError(t, tracker.CreateLockFile(info))

	wantLockPath := filepath.Join(config.StateHome(), ".active", "20251022-153603-"+
		strconv.Itoa(os.Getpid())+".lock")
	_, statErr := os.Stat(wantLockPath)
	require.NoError(t, statErr, "expected lock file under StateHome()/.active, not legacy OcfpHome()/.active")

	legacyActiveDir := filepath.Join(config.OcfpHome(), ".active")
	_, legacyStatErr := os.Stat(legacyActiveDir)
	assert.True(t, os.IsNotExist(legacyStatErr), "legacy OcfpHome()/.active must not be written to")
}

// TestLogsTracker_GetActiveCommands_RoundTripsThroughStateHomeBaseDir
// exercises the full write/read cycle (CreateLockFile then
// GetActiveCommands) against a tracker built from getLogsBaseDir's result,
// confirming active-command lookups operate against the new XDG
// state-class directory end to end.
func TestLogsTracker_GetActiveCommands_RoundTripsThroughStateHomeBaseDir(t *testing.T) {
	isolateXDGHome(t)

	xdgStateBase := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdgStateBase)

	baseDir, err := getLogsBaseDir()
	require.NoError(t, err)

	tracker := NewCommandTracker(baseDir)

	info := ActiveCommand{
		Timestamp:  time.Now(),
		PID:        os.Getpid(),
		Bloc:       "test-bloc",
		Command:    "scale",
		Subcommand: "up",
		LogPath:    filepath.Join(baseDir, "test-bloc", "logs", "scale", "up", "20260101-000000.log"),
	}

	require.NoError(t, tracker.CreateLockFile(info))

	active, activeErr := tracker.GetActiveCommands()
	require.NoError(t, activeErr)
	require.Len(t, active, 1)
	assert.Equal(t, "test-bloc", active[0].Bloc)
	assert.Equal(t, "scale", active[0].Command)
	assert.Equal(t, "up", active[0].Subcommand)

	// The tracker's own activeDir is a subdirectory of the StateHome-based
	// baseDir it was constructed with.
	assert.Equal(t, filepath.Join(config.StateHome(), ".active"), tracker.activeDir)
}
