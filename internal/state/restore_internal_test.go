package state

// Internal white-box tests for restoreFromBackupLocked and getLatestBackup.
// Uses package state (not state_test) so unexported methods are accessible.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetLatestBackup_NoBackups verifies nil return when no backups exist.
func TestGetLatestBackup_NoBackups(t *testing.T) {
	tmpDir := t.TempDir()
	m, err := NewManager(tmpDir)
	require.NoError(t, err)

	backup, err := m.getLatestBackup("no-backups-bloc")
	require.NoError(t, err)
	assert.Nil(t, backup, "should return nil when no backups exist")
}

// TestGetLatestBackup_ReturnsNewest seeds multiple backup files and asserts the
// returned entry has the most recent timestamp.
func TestGetLatestBackup_ReturnsNewest(t *testing.T) {
	tmpDir := t.TempDir()
	m, err := NewManager(tmpDir)
	require.NoError(t, err)

	blocName := "backup-bloc"
	baseFile := blocName + ".json"
	statePath := filepath.Join(tmpDir, baseFile)

	// Write a stub primary state file so createBackup has something to copy.
	payload := `{"version":"1.0","blocName":"backup-bloc","resources":{}}`
	require.NoError(t, os.WriteFile(statePath, []byte(payload), 0600))

	// Manufacture three backup files with distinct second-precision timestamps.
	timestamps := []string{"20230101-100001", "20230101-100002", "20230101-100003"}
	for _, ts := range timestamps {
		backupPath := filepath.Join(tmpDir, baseFile+backupSuffix+"."+ts)
		require.NoError(t, os.WriteFile(backupPath, []byte(payload), 0600))
	}

	latest, err := m.getLatestBackup(blocName)
	require.NoError(t, err)
	require.NotNil(t, latest)

	expected, err := time.Parse("20060102-150405", "20230101-100003")
	require.NoError(t, err)
	assert.Equal(t, expected, latest.Timestamp, "latest backup should be the newest timestamp")
}

// TestRestoreFromBackupLocked_RestoresContent creates a Manager, saves a
// known state with a backup, then calls restoreFromBackupLocked and asserts
// the returned *State matches the backup content.
func TestRestoreFromBackupLocked_RestoresContent(t *testing.T) {
	tmpDir := t.TempDir()
	m, err := NewManager(tmpDir)
	require.NoError(t, err)

	blocName := "restore-bloc"

	// Build and persist an initial state so there is a file to back up.
	initial := &State{
		Version:      "1.0",
		BlocName:     blocName,
		Provider:     "test-provider",
		Region:       "test-region",
		Resources:    map[string]*Resource{"r-001": {ID: "r-001", Type: "network", Name: "net-1", Properties: map[string]interface{}{}, Tags: map[string]string{}}},
		Outputs:      map[string]interface{}{},
		Dependencies: map[string][]string{},
	}

	m.current = initial
	require.NoError(t, m.SaveWithBackup())

	// Verify at least one backup exists before we overwrite the primary.
	backups, err := m.listBackups(blocName)
	require.NoError(t, err)

	if len(backups) == 0 {
		// SaveWithBackup on first call creates the primary file; no backup is
		// written because there was no pre-existing primary to copy. Trigger a
		// second save to create a backup.
		initial.Resources["r-002"] = &Resource{ID: "r-002", Type: "compute_instance", Name: "vm-1", Properties: map[string]interface{}{}, Tags: map[string]string{}}
		require.NoError(t, m.SaveWithBackup())

		backups, err = m.listBackups(blocName)
		require.NoError(t, err)
	}

	require.NotEmpty(t, backups, "at least one backup must exist before testing restore")

	// Corrupt the primary state file to ensure we are restoring from backup.
	statePath := filepath.Join(tmpDir, blocName+".json")
	require.NoError(t, os.WriteFile(statePath, []byte("corrupted"), 0600))

	// restoreFromBackupLocked should recover the last good backup.
	restored, err := m.restoreFromBackupLocked(blocName)
	require.NoError(t, err)
	require.NotNil(t, restored)

	assert.Equal(t, blocName, restored.BlocName)
	assert.Equal(t, "test-provider", restored.Provider)

	// Primary state file should now contain valid JSON again.
	data, err := os.ReadFile(statePath)
	require.NoError(t, err)

	var reloaded State
	require.NoError(t, json.Unmarshal(data, &reloaded))
	assert.Equal(t, blocName, reloaded.BlocName)
}

// TestRestoreFromBackupLocked_NoBackup asserts ErrNoBackupsAvailable is returned
// when restoreFromBackupLocked is called with no backups present.
func TestRestoreFromBackupLocked_NoBackup(t *testing.T) {
	tmpDir := t.TempDir()
	m, err := NewManager(tmpDir)
	require.NoError(t, err)

	_, err = m.restoreFromBackupLocked("empty-bloc")
	assert.ErrorIs(t, err, ErrNoBackupsAvailable)
}
