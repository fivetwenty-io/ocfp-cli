package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// Log path structure constants.
const (
	pathDepthSimpleCommand  = 2  // Simple command path depth: {command}/{filename}
	pathDepthWithSubcommand = 3  // With subcommand depth: {command}/{subcommand}/{filename}
	minTimestampLength      = 15 // Minimum filename length for YYYYMMDD-HHMMSS format
)

// Static errors for log scanner.
var (
	ErrInvalidLogPath      = errors.New("invalid log path")
	ErrInvalidFilename     = errors.New("invalid filename format")
	ErrUnexpectedPathDepth = errors.New("unexpected path depth")
)

// LogEntry represents a single log file entry.
type LogEntry struct {
	Path       string
	Bloc       string
	Command    string
	Subcommand string
	Timestamp  time.Time
	RequestID  string
	Size       int64
	IsActive   bool
	PID        int // Process ID if active
}

// LogScanner scans log directories and discovers log files.
type LogScanner struct {
	baseDir    string
	blocFilter string
}

// NewLogScanner creates a new log scanner.
func NewLogScanner(baseDir, blocFilter string) *LogScanner {
	return &LogScanner{
		baseDir:    baseDir,
		blocFilter: blocFilter,
	}
}

// ScanLogs discovers and returns all log files matching the criteria.
func (s *LogScanner) ScanLogs(commandFilters []string) ([]LogEntry, error) {
	var entries []LogEntry

	// Get list of blocs to scan
	blocs, err := s.getBlocs()
	if err != nil {
		return nil, fmt.Errorf("failed to get blocs: %w", err)
	}

	// Scan each bloc
	for _, bloc := range blocs {
		blocPath := filepath.Join(s.baseDir, bloc, "logs")
		//nolint:noinlineerr // Preserving existing error handling pattern for no functional changes
		if _, statErr := os.Stat(blocPath); os.IsNotExist(statErr) {
			continue
		}

		// Walk the logs directory
		walkErr := filepath.Walk(blocPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				// Skip files/dirs we can't access - intentionally ignoring error
				return nil //nolint:nilerr // Skip inaccessible files
			}

			// Skip directories
			if info.IsDir() {
				return nil
			}

			// Only process .log files
			if !strings.HasSuffix(info.Name(), ".log") {
				return nil
			}

			// Parse log entry
			entry, parseErr := s.parseLogEntry(path, bloc, info)
			if parseErr != nil {
				// Skip unparseable logs - intentionally ignoring error
				return nil //nolint:nilerr // Skip unparseable logs
			}

			// Apply command filter if specified
			if len(commandFilters) > 0 && !s.matchesCommandFilter(entry, commandFilters) {
				return nil
			}

			entries = append(entries, *entry)

			return nil
		})
		if walkErr != nil {
			return nil, fmt.Errorf("failed to walk logs directory %s: %w", blocPath, walkErr)
		}
	}

	// Sort by timestamp (most recent first)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})

	return entries, nil
}

// getBlocs returns the list of blocs to scan.
func (s *LogScanner) getBlocs() ([]string, error) {
	// If bloc filter is specified, only scan that bloc
	if s.blocFilter != "" {
		return []string{s.blocFilter}, nil
	}

	// Otherwise, scan all bloc directories
	var blocs []string

	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		// If base dir doesn't exist, return empty
		if os.IsNotExist(err) {
			return []string{}, nil
		}

		return nil, fmt.Errorf("failed to read base directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Check if this directory has a logs subdirectory
		logsPath := filepath.Join(s.baseDir, entry.Name(), "logs")
		//nolint:noinlineerr // Preserving existing error handling pattern for no functional changes
		if _, err := os.Stat(logsPath); err == nil {
			blocs = append(blocs, entry.Name())
		}
	}

	return blocs, nil
}

// parseLogEntry parses a log file path and creates a LogEntry.
func (s *LogScanner) parseLogEntry(path string, bloc string, info os.FileInfo) (*LogEntry, error) {
	// Extract relative path from bloc logs directory
	blocLogsPath := filepath.Join(s.baseDir, bloc, "logs")

	relPath, err := filepath.Rel(blocLogsPath, path)
	if err != nil {
		return nil, fmt.Errorf("failed to get relative path: %w", err)
	}

	// Parse path components: {command}/[{subcommand}/]{filename}
	parts := strings.Split(relPath, string(filepath.Separator))
	if len(parts) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrInvalidLogPath, path)
	}

	entry := &LogEntry{
		Path: path,
		Bloc: bloc,
		Size: info.Size(),
	}

	// Determine command and subcommand
	switch len(parts) {
	case pathDepthSimpleCommand:
		// Simple command: {command}/{filename}
		entry.Command = parts[0]
	case pathDepthWithSubcommand:
		// With subcommand: {command}/{subcommand}/{filename}
		entry.Command = parts[0]
		entry.Subcommand = parts[1]
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnexpectedPathDepth, relPath)
	}

	// Parse filename: {timestamp}[-{requestID}].log
	filename := parts[len(parts)-1]
	filename = strings.TrimSuffix(filename, ".log")

	// Split on '-' to separate timestamp and optional request ID
	// Timestamp format: 20251022-153603 (14 chars)
	if len(filename) < minTimestampLength {
		return nil, fmt.Errorf("%w: %s", ErrInvalidFilename, filename)
	}

	timestampStr := filename[:minTimestampLength] // YYYYMMDD-HHMMSS

	entry.Timestamp, err = time.Parse("20060102-150405", timestampStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse timestamp: %w", err)
	}

	// Check for request ID
	if len(filename) > minTimestampLength && filename[minTimestampLength] == '-' {
		entry.RequestID = filename[minTimestampLength+1:]
	}

	return entry, nil
}

// matchesCommandFilter checks if an entry matches the command filter.
func (s *LogScanner) matchesCommandFilter(entry *LogEntry, filters []string) bool {
	for _, filter := range filters {
		if entry.Command == filter {
			return true
		}
		// Also check command.subcommand format
		if entry.Subcommand != "" {
			fullCommand := entry.Command + "." + entry.Subcommand
			if fullCommand == filter {
				return true
			}
		}
	}

	return false
}

// GroupByCommand groups log entries by command.
func GroupByCommand(entries []LogEntry) map[string][]LogEntry {
	grouped := make(map[string][]LogEntry)

	for _, entry := range entries {
		key := entry.Command
		if entry.Subcommand != "" {
			key = entry.Command + "." + entry.Subcommand
		}

		grouped[key] = append(grouped[key], entry)
	}

	// Sort each group by timestamp (most recent first)
	for key := range grouped {
		logs := grouped[key]
		sort.Slice(logs, func(i, j int) bool {
			return logs[i].Timestamp.After(logs[j].Timestamp)
		})
		grouped[key] = logs
	}

	return grouped
}

// parseCommandList parses a comma-separated list of commands.
func parseCommandList(commands string) []string {
	if commands == "" {
		return nil
	}

	parts := strings.Split(commands, ",")

	var result []string

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}

	return result
}

// getLogsBaseDir returns the base directory for logs.
func getLogsBaseDir() (string, error) {
	baseDir := config.OcfpHome()
	if baseDir == "" {
		return "", config.ErrOcfpHomeNotFound
	}

	return baseDir, nil
}
