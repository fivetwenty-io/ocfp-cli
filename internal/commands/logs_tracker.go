package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// File permission and lock file constants.
const (
	filePermUserRWXGroupRX = 0750 // Directory permissions: user rwx, group rx
	filePermUserRW         = 0600 // File permissions: user rw only
	minLockFilenameParts   = 3    // Minimum parts in lock filename: YYYYMMDD-HHMMSS-PID
)

// Static errors for command tracker.
var (
	ErrInvalidLockFilename = errors.New("invalid lock filename format")
)

// ActiveCommand represents a currently running command.
type ActiveCommand struct {
	Timestamp  time.Time `json:"timestamp"`
	PID        int       `json:"pid"`
	Bloc       string    `json:"bloc"`
	Command    string    `json:"command"`
	Subcommand string    `json:"subcommand"`
	LogPath    string    `json:"log_path"`
}

// CommandTracker manages active command tracking via lock files.
type CommandTracker struct {
	activeDir string
}

// NewCommandTracker creates a new command tracker.
func NewCommandTracker(baseDir string) *CommandTracker {
	return &CommandTracker{
		activeDir: filepath.Join(baseDir, ".active"),
	}
}

// CreateLockFile creates a lock file for an active command.
func (t *CommandTracker) CreateLockFile(info ActiveCommand) error {
	// Ensure .active directory exists
	err := os.MkdirAll(t.activeDir, filePermUserRWXGroupRX)
	if err != nil {
		return fmt.Errorf("failed to create active directory: %w", err)
	}

	// Generate lock filename: {timestamp}-{pid}.lock
	timestampStr := info.Timestamp.Format("20060102-150405")
	lockFilename := fmt.Sprintf("%s-%d.lock", timestampStr, info.PID)
	lockPath := filepath.Join(t.activeDir, lockFilename)

	// Marshal command info to JSON
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal command info: %w", err)
	}

	// Write lock file
	err = os.WriteFile(lockPath, data, filePermUserRW)
	if err != nil {
		return fmt.Errorf("failed to write lock file: %w", err)
	}

	// Debug: Verify file was written
	_, err = os.Stat(lockPath)
	if err != nil {
		return fmt.Errorf("lock file verification failed - file does not exist after write: %w", err)
	}

	return nil
}

// RemoveLockFile removes the lock file for a command.
func (t *CommandTracker) RemoveLockFile(timestamp time.Time, pid int) error {
	timestampStr := timestamp.Format("20060102-150405")
	lockFilename := fmt.Sprintf("%s-%d.lock", timestampStr, pid)
	lockPath := filepath.Join(t.activeDir, lockFilename)

	err := os.Remove(lockPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove lock file: %w", err)
	}

	return nil
}

// GetActiveCommands returns all currently active commands.
func (t *CommandTracker) GetActiveCommands() ([]ActiveCommand, error) {
	// Ensure .active directory exists
	//nolint:noinlineerr // Preserving existing error handling pattern for no functional changes
	if _, err := os.Stat(t.activeDir); os.IsNotExist(err) {
		return []ActiveCommand{}, nil
	}

	// Read all lock files
	entries, err := os.ReadDir(t.activeDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read active directory: %w", err)
	}

	activeCommands := make([]ActiveCommand, 0, len(entries))

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}

		lockPath := filepath.Join(t.activeDir, entry.Name())

		// Read and parse lock file
		data, err := os.ReadFile(filepath.Clean(lockPath))
		if err != nil {
			// Skip unreadable files
			continue
		}

		var cmd ActiveCommand

		err = json.Unmarshal(data, &cmd)
		if err != nil {
			// Skip unparseable files
			continue
		}

		// Check if process is still running
		if !t.IsProcessRunning(cmd.PID) {
			// Process is dead, remove stale lock
			_ = os.Remove(lockPath)

			continue
		}

		activeCommands = append(activeCommands, cmd)
	}

	return activeCommands, nil
}

// IsProcessRunning checks if a process with the given PID is running.
func (t *CommandTracker) IsProcessRunning(pid int) bool {
	// On Unix-like systems, we can check if a process exists by sending signal 0
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// On Unix, FindProcess always succeeds, so we need to send a signal
	// Signal 0 doesn't actually send a signal but checks if we can
	err = process.Signal(syscall.Signal(0))

	return err == nil
}

// CleanStaleLocks removes lock files for processes that are no longer running.
func (t *CommandTracker) CleanStaleLocks() error {
	// Ensure .active directory exists
	//nolint:noinlineerr // Preserving existing error handling pattern for no functional changes
	if _, err := os.Stat(t.activeDir); os.IsNotExist(err) {
		return nil
	}

	// Read all lock files
	entries, err := os.ReadDir(t.activeDir)
	if err != nil {
		return fmt.Errorf("failed to read active directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}

		lockPath := filepath.Join(t.activeDir, entry.Name())

		// Read and parse lock file
		data, err := os.ReadFile(filepath.Clean(lockPath))
		if err != nil {
			continue
		}

		var cmd ActiveCommand

		err = json.Unmarshal(data, &cmd)
		if err != nil {
			// Remove invalid lock files
			_ = os.Remove(lockPath)

			continue
		}

		// Check if process is still running
		if !t.IsProcessRunning(cmd.PID) {
			// Remove stale lock
			_ = os.Remove(lockPath)
		}
	}

	return nil
}

// MatchLogToLock matches a log entry to an active command lock.
func (t *CommandTracker) MatchLogToLock(entry LogEntry) (*ActiveCommand, bool) {
	// Get all active commands
	activeCommands, err := t.GetActiveCommands()
	if err != nil {
		return nil, false
	}

	// Try to match by log path or timestamp
	for _, cmd := range activeCommands {
		if cmd.LogPath == entry.Path {
			return &cmd, true
		}
		// Also try matching by timestamp
		if cmd.Timestamp.Equal(entry.Timestamp) &&
			cmd.Command == entry.Command &&
			cmd.Subcommand == entry.Subcommand &&
			cmd.Bloc == entry.Bloc {
			return &cmd, true
		}
	}

	return nil, false
}

// FilterActive filters log entries to only include active ones.
func (t *CommandTracker) FilterActive(entries []LogEntry) ([]LogEntry, error) {
	// Get all active commands
	activeCommands, err := t.GetActiveCommands()
	if err != nil {
		return nil, fmt.Errorf("failed to get active commands: %w", err)
	}

	// Create a map for quick lookup
	activeMap := make(map[string]bool)
	for _, cmd := range activeCommands {
		activeMap[cmd.LogPath] = true
	}

	// Filter entries
	var filtered []LogEntry

	for _, entry := range entries {
		if activeMap[entry.Path] {
			// Mark as active and set PID
			entry.IsActive = true
			// Find the matching command to get PID
			for _, cmd := range activeCommands {
				if cmd.LogPath == entry.Path {
					entry.PID = cmd.PID

					break
				}
			}

			filtered = append(filtered, entry)
		}
	}

	return filtered, nil
}

// ParseLockFilename parses a lock filename to extract timestamp and PID.
func ParseLockFilename(filename string) (time.Time, int, error) {
	// Remove .lock extension
	filename = strings.TrimSuffix(filename, ".lock")

	// Split on '-' to get parts
	// Format: YYYYMMDD-HHMMSS-PID
	parts := strings.Split(filename, "-")
	if len(parts) < minLockFilenameParts {
		return time.Time{}, 0, fmt.Errorf("%w: %s", ErrInvalidLockFilename, filename)
	}

	// Reconstruct timestamp (YYYYMMDD-HHMMSS)
	timestampStr := parts[0] + "-" + parts[1]

	timestamp, err := time.Parse("20060102-150405", timestampStr)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("failed to parse timestamp: %w", err)
	}

	// Parse PID (last part)
	pidStr := parts[len(parts)-1]

	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("failed to parse PID: %w", err)
	}

	return timestamp, pid, nil
}
