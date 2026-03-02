package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	// Maximum number of backups to keep per bloc.
	maxBackups = 5

	// Backup file suffix pattern.
	backupSuffix = ".backup"

	// Temp file suffix for atomic writes.
	tempSuffix = ".tmp"

	// backupFilenameParts is the expected number of parts when splitting a backup filename.
	backupFilenameParts = 2
)

// BackupInfo contains information about a state backup.
type BackupInfo struct {
	Path      string
	Timestamp time.Time
	Size      int64
}

// createBackup creates a timestamped backup of the current state file.
// Returns the backup path if successful, or an error if backup fails.
func (m *Manager) createBackup(blocName string) (string, error) {
	statePath := m.getStatePath(blocName)

	// Check if state file exists
	info, err := os.Stat(statePath)
	if os.IsNotExist(err) {
		// No state file to backup
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("failed to stat state file: %w", err)
	}

	// Create timestamped backup path
	timestamp := time.Now().Format("20060102-150405")
	backupPath := fmt.Sprintf("%s%s.%s", statePath, backupSuffix, timestamp)

	// Copy state file to backup
	data, err := os.ReadFile(statePath) // #nosec G304 - statePath is validated
	if err != nil {
		return "", fmt.Errorf("failed to read state file: %w", err)
	}

	err = os.WriteFile(backupPath, data, stateFileMode)
	if err != nil {
		return "", fmt.Errorf("failed to write backup file: %w", err)
	}

	logger.Infof("Created state backup: %s (size: %d bytes)", backupPath, info.Size())

	return backupPath, nil
}

// cleanupOldBackups removes old backup files, keeping only the most recent maxBackups.
func (m *Manager) cleanupOldBackups(blocName string) error {
	backups, err := m.listBackups(blocName)
	if err != nil {
		return fmt.Errorf("failed to list backups: %w", err)
	}

	// If we have fewer backups than the max, nothing to do
	if len(backups) <= maxBackups {
		return nil
	}

	// Sort backups by timestamp (oldest first)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Timestamp.Before(backups[j].Timestamp)
	})

	// Delete oldest backups beyond maxBackups
	toDelete := len(backups) - maxBackups
	for backupIndex := range toDelete {
		err := os.Remove(backups[backupIndex].Path)
		if err != nil {
			logger.Warnf("Failed to remove old backup %s: %v", backups[backupIndex].Path, err)

			continue
		}

		logger.Debugf("Removed old backup: %s", backups[backupIndex].Path)
	}

	logger.Infof("Cleaned up %d old backups, keeping %d most recent", toDelete, maxBackups)

	return nil
}

// listBackups returns a list of all backup files for a given bloc.
func (m *Manager) listBackups(blocName string) ([]BackupInfo, error) {
	statePath := m.getStatePath(blocName)
	backupPrefix := statePath + backupSuffix

	entries, err := os.ReadDir(m.stateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read state directory: %w", err)
	}

	backups := make([]BackupInfo, 0)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		fullPath := filepath.Join(m.stateDir, entry.Name())

		// Check if this is a backup file for our bloc
		if !strings.HasPrefix(fullPath, backupPrefix) {
			continue
		}

		// Extract timestamp from backup filename
		// Format: <statePath>.backup.<timestamp>
		parts := strings.Split(entry.Name(), backupSuffix+".")
		if len(parts) != backupFilenameParts {
			logger.Warnf("Skipping malformed backup file: %s", entry.Name())

			continue
		}

		timestampStr := parts[1]

		timestamp, err := time.Parse("20060102-150405", timestampStr)
		if err != nil {
			logger.Warnf("Skipping backup with invalid timestamp: %s", entry.Name())

			continue
		}

		info, err := os.Stat(fullPath)
		if err != nil {
			logger.Warnf("Failed to stat backup file %s: %v", fullPath, err)

			continue
		}

		backups = append(backups, BackupInfo{
			Path:      fullPath,
			Timestamp: timestamp,
			Size:      info.Size(),
		})
	}

	return backups, nil
}

// getLatestBackup returns the most recent backup for a bloc, or nil if none exist.
func (m *Manager) getLatestBackup(blocName string) (*BackupInfo, error) {
	backups, err := m.listBackups(blocName)
	if err != nil {
		return nil, err
	}

	if len(backups) == 0 {
		return nil, nil
	}

	// Sort by timestamp (newest first)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Timestamp.After(backups[j].Timestamp)
	})

	return &backups[0], nil
}

// restoreFromBackup restores state from the latest backup.
// This is used for rollback on save failures.
func (m *Manager) restoreFromBackup(blocName string) error {
	backup, err := m.getLatestBackup(blocName)
	if err != nil {
		return fmt.Errorf("failed to get latest backup: %w", err)
	}

	if backup == nil {
		return ErrNoBackupsAvailable
	}

	statePath := m.getStatePath(blocName)

	// Copy backup to state file
	data, err := os.ReadFile(backup.Path) // #nosec G304 - backup.Path from listBackups
	if err != nil {
		return fmt.Errorf("failed to read backup file: %w", err)
	}

	err = os.WriteFile(statePath, data, stateFileMode)
	if err != nil {
		return fmt.Errorf("failed to restore state file: %w", err)
	}

	logger.Infof("Restored state from backup: %s", backup.Path)

	// Reload the restored state into memory
	var state State

	err = json.Unmarshal(data, &state)
	if err != nil {
		return fmt.Errorf("failed to parse restored state: %w", err)
	}

	m.mu.Lock()
	m.current = &state
	m.mu.Unlock()

	return nil
}

// atomicWrite performs an atomic write operation using a temporary file.
// This ensures the state file is never left in a corrupted or partial state.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	// Create temp file in same directory
	dir := filepath.Dir(path)

	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+tempSuffix+".*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	tmpPath := tmpFile.Name()

	// Ensure temp file is cleaned up on error
	defer func() {
		if tmpFile != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	// Write data to temp file
	_, err = tmpFile.Write(data)
	if err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// Sync to ensure data is written to disk
	err = tmpFile.Sync()
	if err != nil {
		return fmt.Errorf("failed to sync temp file: %w", err)
	}

	// Close temp file
	err = tmpFile.Close()
	if err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	tmpFile = nil // Prevent deferred cleanup

	// Set permissions
	err = os.Chmod(tmpPath, perm)
	if err != nil {
		_ = os.Remove(tmpPath)

		return fmt.Errorf("failed to set file permissions: %w", err)
	}

	// Atomically replace old file with new file
	err = os.Rename(tmpPath, path)
	if err != nil {
		_ = os.Remove(tmpPath)

		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// SaveWithBackup saves the current state with automatic backup and rollback on failure.
func (m *Manager) SaveWithBackup() error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.current == nil {
		return ErrNoStateLoaded
	}

	blocName := m.current.BlocName
	statePath := m.getStatePath(blocName)

	// Step 1: Create backup of existing state
	backupPath, err := m.createBackup(blocName)
	if err != nil {
		logger.Warnf("Failed to create backup: %v", err)
		// Continue anyway - backup failure shouldn't prevent save
	}

	// Step 2: Update timestamp
	m.current.UpdatedAt = time.Now()

	// Step 3: Marshal state to JSON
	data, err := json.MarshalIndent(m.current, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	// Step 4: Atomically write state file
	err = atomicWrite(statePath, data, stateFileMode)
	if err != nil {
		// Attempt rollback if backup exists
		if backupPath != "" {
			logger.Warnf("State save failed, attempting rollback...")

			rollbackErr := m.restoreFromBackup(blocName)
			if rollbackErr != nil {
				logger.Errorf("Rollback failed: %v", rollbackErr)

				return fmt.Errorf("failed to write state: %w (rollback also failed: %w)", err, rollbackErr)
			}

			logger.Info("Successfully rolled back to previous state")
		}

		return fmt.Errorf("failed to write state: %w", err)
	}

	logger.Debugf("Saved state for bloc %s with %d resources", blocName, len(m.current.Resources))

	// Step 5: Cleanup old backups
	err = m.cleanupOldBackups(blocName)
	if err != nil {
		logger.Warnf("Failed to cleanup old backups: %v", err)
		// Don't fail the save operation due to cleanup failure
	}

	return nil
}
