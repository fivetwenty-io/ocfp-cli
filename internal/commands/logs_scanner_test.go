package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogsScanner_GetLogsBaseDir_ResolvesUnderStateHome asserts
// getLogsBaseDir returns config.StateHome() rather than the pre-migration
// flat config.OcfpHome() directory, when only the new XDG state directory
// is relevant (no legacy ~/.ocfp directory present on disk).
func TestLogsScanner_GetLogsBaseDir_ResolvesUnderStateHome(t *testing.T) {
	isolateXDGHome(t)

	xdgStateBase := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdgStateBase)

	want := config.StateHome()
	require.NotEmpty(t, want)

	got, err := getLogsBaseDir()
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.NotEqual(t, config.OcfpHome(), got)
}

// TestLogsScanner_GetLogsBaseDir_DualReadFallsBackToLegacyOcfpHome asserts
// getLogsBaseDir falls back to the legacy ~/.ocfp directory when only that
// exists on disk (the new XDG state directory has never been created), and
// prefers it over an unresolvable/nonexistent new path.
func TestLogsScanner_GetLogsBaseDir_DualReadFallsBackToLegacyOcfpHome(t *testing.T) {
	isolateXDGHome(t)

	legacyDir := config.OcfpHome()
	require.NoError(t, os.MkdirAll(legacyDir, 0o750))

	got, err := getLogsBaseDir()
	require.NoError(t, err)
	assert.Equal(t, legacyDir, got)
}

// TestLogsScanner_GetLogsBaseDir_NewPathPreferredWhenBothExist asserts that
// once both the new XDG state directory and the legacy ~/.ocfp directory
// exist on disk, getLogsBaseDir prefers the new path.
func TestLogsScanner_GetLogsBaseDir_NewPathPreferredWhenBothExist(t *testing.T) {
	isolateXDGHome(t)

	legacyDir := config.OcfpHome()
	require.NoError(t, os.MkdirAll(legacyDir, 0o750))

	newDir := config.StateHome()
	require.NoError(t, os.MkdirAll(newDir, 0o750))

	got, err := getLogsBaseDir()
	require.NoError(t, err)
	assert.Equal(t, newDir, got)
}

// TestLogsScanner_ScanLogs_DiscoversEntriesUnderStateHomeBaseDir is an
// end-to-end check that a LogScanner built from getLogsBaseDir's result
// discovers log entries written under the new XDG state-class directory
// tree, exercising the same baseDir -> {bloc}/logs/{command}/{file}.log
// layout used by the real `ocfp logs` command.
func TestLogsScanner_ScanLogs_DiscoversEntriesUnderStateHomeBaseDir(t *testing.T) {
	isolateXDGHome(t)

	xdgStateBase := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdgStateBase)

	baseDir, err := getLogsBaseDir()
	require.NoError(t, err)
	assert.Equal(t, config.StateHome(), baseDir)

	logDir := filepath.Join(baseDir, "test-bloc", "logs", "deploy")
	require.NoError(t, os.MkdirAll(logDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(logDir, "20251022-153603.log"), []byte("log body"), 0o600))

	scanner := NewLogScanner(baseDir, "")

	entries, scanErr := scanner.ScanLogs(nil)
	require.NoError(t, scanErr)
	require.Len(t, entries, 1)
	assert.Equal(t, "test-bloc", entries[0].Bloc)
	assert.Equal(t, "deploy", entries[0].Command)
}
