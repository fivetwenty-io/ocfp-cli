package commands

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// sharedLogDir gives paths a log directory other users can read, holding
// logs they can read too, a file that is not a log, and a link named like a
// log that points outside the directory. It returns the link's target and
// the file that is not a log.
func sharedLogDir(t *testing.T, paths map[string]string) (string, string) {
	t.Helper()

	logDir := paths["logDir"]
	require.NoError(t, os.MkdirAll(logDir, 0o700))
	require.NoError(t, os.Chmod(logDir, 0o755)) // #nosec G302 -- the test shares the directory on purpose

	for _, logFile := range sharedLogs(paths) {
		writeFileWithMode(t, logFile, 0o644)
	}

	writeFileWithMode(t, paths["logFile"]+".previous-20261006-100000", 0o640)

	other := filepath.Join(logDir, "notes.txt")
	writeFileWithMode(t, other, 0o644)

	target := filepath.Join(t.TempDir(), "elsewhere.log")
	writeFileWithMode(t, target, 0o644)
	require.NoError(t, os.Symlink(target, paths["logFile"]+".previous-20261006-090000"))

	return target, other
}

func sharedLogs(paths map[string]string) []string {
	logFile := paths["logFile"]

	return []string{logFile, logFile + previousVaultLogSuffix, logFile + ".previous-20261006-110000-2"}
}

func writeFileWithMode(t *testing.T, path string, mode fs.FileMode) {
	t.Helper()

	require.NoError(t, os.WriteFile(path, []byte("log line\n"), 0o600))
	require.NoError(t, os.Chmod(path, mode))
}

func permOf(t *testing.T, path string) fs.FileMode {
	t.Helper()

	info, err := os.Lstat(path)
	require.NoError(t, err)

	return info.Mode().Perm()
}

// assertLogsPrivate checks that the log directory and every log in it lost
// their group and other access, and that nothing else changed.
func assertLogsPrivate(t *testing.T, paths map[string]string, target, other string) {
	t.Helper()

	assert.Equal(t, fs.FileMode(0o700), permOf(t, paths["logDir"]))

	for _, logFile := range sharedLogs(paths) {
		assert.Equal(t, fs.FileMode(0o600), permOf(t, logFile), logFile)
	}

	assert.Equal(t, fs.FileMode(0o600), permOf(t, paths["logFile"]+".previous-20261006-100000"))
	assert.Equal(t, fs.FileMode(0o644), permOf(t, target), "a link's target outside the directory is left alone")
	assert.Equal(t, fs.FileMode(0o644), permOf(t, other), "a file that is not a vault log is left alone")
}

// A log can hold the only copy of an unseal key, so the log directory is
// created private to the user.
func TestPrepareVaultDirectories_CreatesAPrivateLogDirectory(t *testing.T) {
	paths := reconcilePaths(t)

	require.NoError(t, prepareVaultDirectories(paths, zap.NewNop().Sugar()))

	assert.Equal(t, fs.FileMode(0o700), permOf(t, paths["logDir"]))
}

// Logs left readable by an older release lose their group and other access
// before the vault starts again.
func TestPrepareVaultDirectories_TightensAnExistingLogDirectory(t *testing.T) {
	paths := reconcilePaths(t)
	target, other := sharedLogDir(t, paths)

	require.NoError(t, prepareVaultDirectories(paths, zap.NewNop().Sugar()))

	assertLogsPrivate(t, paths, target, other)
}

// Every run tightens the logs, even one that stops before it starts a vault.
func TestEnsureInceptionVaultLocked_TightensTheLogsOnEveryRun(t *testing.T) {
	paths := reconcilePaths(t)
	target, other := sharedLogDir(t, paths)
	paths["port"], paths["clusterPort"] = "not-a-port", ""

	err := ensureInceptionVaultLocked("ocfp-lab-drgao", paths)
	require.ErrorContains(t, err, "invalid inception vault port")

	assertLogsPrivate(t, paths, target, other)
}

// A run that stops early leaves a missing log directory missing.
func TestEnsureInceptionVaultLocked_LeavesAMissingLogDirectoryMissing(t *testing.T) {
	paths := reconcilePaths(t)
	paths["port"], paths["clusterPort"] = "not-a-port", ""

	require.ErrorContains(t, ensureInceptionVaultLocked("ocfp-lab-drgao", paths), "invalid inception vault port")

	assert.NoDirExists(t, paths["logDir"])
}

// A migration writes its own log beside the vault's, so it tightens the
// directory the same way.
func TestPrepareMigration_TightensTheLogDirectory(t *testing.T) {
	paths := migrationPaths(t)
	target, other := sharedLogDir(t, paths)
	layout := newMigrationLayout(paths, time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))

	require.NoError(t, prepareMigration(paths, layout, "storage \"raft\" {}\n", zap.NewNop().Sugar()))

	assertLogsPrivate(t, paths, target, other)
}

// A log whose mode cannot be changed is reported, not fatal, and the warning
// names the file without anything from inside it.
func TestTightenVaultLogs_WarnsWhenAModeCannotBeChanged(t *testing.T) {
	paths := reconcilePaths(t)
	require.NoError(t, os.MkdirAll(paths["logDir"], 0o700))
	writeFileWithMode(t, paths["logFile"], 0o644)

	original := chmodVaultLog
	chmodVaultLog = func(string, fs.FileMode) error { return errors.New("operation not permitted") }

	t.Cleanup(func() { chmodVaultLog = original })

	core, logs := observer.New(zapcore.WarnLevel)
	tightenVaultLogs(paths, zap.New(core).Sugar())

	assert.Equal(t, fs.FileMode(0o644), permOf(t, paths["logFile"]))

	warnings := logs.FilterField(zap.String("path", paths["logFile"])).All()
	require.Len(t, warnings, 1)
	assert.NotContains(t, warnings[0].Message, "log line")
}
