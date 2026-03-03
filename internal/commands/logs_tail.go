package commands

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Log tail configuration constants.
const (
	tailTickerMilliseconds = 100 // Milliseconds between file checks when following logs
)

// Static errors for log tailer.
var (
	ErrNoLogFiles      = errors.New("no log files specified")
	ErrLogFileRemoved  = errors.New("log file was removed")
	ErrNoLogsCouldOpen = errors.New("no log files could be opened")
)

// logEntry represents a parsed JSON log entry from zap logger.
type logEntry struct {
	Level        string    `json:"level"`
	TimestampStr string    `json:"timestamp"`
	Caller       string    `json:"caller"`
	Message      string    `json:"msg"`
	_parsedTime  time.Time //nolint:unused // Reserved for future use
}

// LogTailer handles tailing log files.
type LogTailer struct{}

// NewLogTailer creates a new log tailer.
func NewLogTailer() *LogTailer {
	return &LogTailer{}
}

// TailLogs tails one or more log files.
func (t *LogTailer) TailLogs(paths []string, follow bool, lineCount int, outputFormat string) error {
	if len(paths) == 0 {
		return ErrNoLogFiles
	}

	// If single file, tail it directly
	if len(paths) == 1 {
		return t.tailSingleFile(paths[0], follow, lineCount, outputFormat)
	}

	// Multiple files: interleave output
	return t.tailMultipleFiles(paths, follow, lineCount, outputFormat)
}

// formatLogLine formats a log line based on output format.
func formatLogLine(line string, outputFormat string) string {
	// If JSON output requested, return raw line
	if outputFormat == "json" {
		return line
	}

	// Try to parse as JSON log entry
	var entry logEntry
	//nolint:noinlineerr // Preserving existing error handling pattern for no functional changes
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		// Not valid JSON, return as-is
		return line
	}

	// Parse timestamp - zap uses RFC3339-like format with milliseconds
	// Format: 2025-10-23T06:13:50.993-0400
	timestamp, err := time.Parse("2006-01-02T15:04:05.000-0700", entry.TimestampStr)
	if err != nil {
		// Try without milliseconds
		timestamp, err = time.Parse("2006-01-02T15:04:05-0700", entry.TimestampStr)
		if err != nil {
			// If timestamp parsing fails, use raw string
			return fmt.Sprintf("[%s] %s: %s (caller=%s)", entry.TimestampStr, entry.Level, entry.Message, entry.Caller)
		}
	}

	// Format as human-readable text
	timestampStr := timestamp.Format("2006-01-02 15:04:05")

	return fmt.Sprintf("[%s] %s: %s (caller=%s)", timestampStr, entry.Level, entry.Message, entry.Caller)
}

// tailSingleFile tails a single log file.
func (t *LogTailer) tailSingleFile(path string, follow bool, lineCount int, outputFormat string) error {
	// Clean path to prevent path traversal
	cleanPath := filepath.Clean(path)

	file, err := os.Open(cleanPath)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	defer func() {
		_ = file.Close()
	}()

	// Read last N lines
	err = t.readLastLines(file, lineCount, outputFormat)
	if err != nil {
		return fmt.Errorf("failed to read log file: %w", err)
	}

	// Follow mode
	if follow {
		return t.followFile(file, path, outputFormat)
	}

	return nil
}

// tailMultipleFiles tails multiple log files with headers.
func (t *LogTailer) tailMultipleFiles(paths []string, follow bool, lineCount int, outputFormat string) error {
	for i, path := range paths {
		if i > 0 {
			_, _ = fmt.Fprintf(os.Stdout, "\n") // Add blank line between files
		}

		// Print header
		_, _ = fmt.Fprintf(os.Stdout, "==> %s <==\n", filepath.Base(path))

		// Tail the file (clean path to prevent path traversal)
		cleanPath := filepath.Clean(path)

		file, err := os.Open(cleanPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening %s: %v\n", path, err)

			continue
		}

		err = t.readLastLines(file, lineCount, outputFormat)
		_ = file.Close()

		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", path, err)

			continue
		}
	}

	// Follow mode for multiple files
	if follow {
		return t.followMultipleFiles(paths, outputFormat)
	}

	return nil
}

// readLastLines reads the last N lines from a file.
func (t *LogTailer) readLastLines(file *os.File, lineCount int, outputFormat string) error {
	fileSize, err := getFileSize(file)
	if err != nil {
		return err
	}

	if fileSize == 0 {
		return nil // Empty file
	}

	lines, err := extractLastLines(file, fileSize, lineCount)
	if err != nil {
		return err
	}

	printFormattedLines(lines, outputFormat)

	return nil
}

// getFileSize retrieves the file size.
func getFileSize(file *os.File) (int64, error) {
	stat, err := file.Stat()
	if err != nil {
		return 0, fmt.Errorf("failed to stat file: %w", err)
	}

	return stat.Size(), nil
}

// extractLastLines reads file backwards to extract last N lines.
func extractLastLines(file *os.File, fileSize int64, lineCount int) ([]string, error) {
	const chunkSize = 4096

	var (
		buffer []byte
		offset int64
	)

	for {
		readSize := calculateReadSize(chunkSize, offset, fileSize)
		readOffset := fileSize - offset - int64(readSize)

		chunk, err := readChunkAt(file, readSize, readOffset)
		if err != nil {
			return nil, err
		}

		buffer = prependToBuffer(chunk, buffer)
		currentLines := splitLines(buffer)

		if lines := checkLineCount(currentLines, lineCount, readOffset); lines != nil {
			return lines, nil
		}

		offset += int64(readSize)
		if offset >= fileSize {
			return currentLines, nil
		}
	}
}

// calculateReadSize determines how many bytes to read from file.
func calculateReadSize(chunkSize int, offset, fileSize int64) int {
	if offset+int64(chunkSize) > fileSize {
		return int(fileSize - offset)
	}

	return chunkSize
}

// readChunkAt reads a chunk from the file at the specified offset.
func readChunkAt(file *os.File, size int, offset int64) ([]byte, error) {
	chunk := make([]byte, size)

	_, err := file.ReadAt(chunk, offset)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return chunk, nil
}

// prependToBuffer prepends chunk to existing buffer.
func prependToBuffer(chunk, buffer []byte) []byte {
	newBuffer := make([]byte, 0, len(chunk)+len(buffer))
	newBuffer = append(newBuffer, chunk...)
	newBuffer = append(newBuffer, buffer...)

	return newBuffer
}

// checkLineCount checks if we have enough lines and returns them if so.
func checkLineCount(lines []string, targetCount int, readOffset int64) []string {
	if len(lines) >= targetCount+1 || readOffset == 0 {
		if len(lines) > targetCount {
			return lines[len(lines)-targetCount:]
		}

		return lines
	}

	return nil
}

// printFormattedLines outputs lines with formatting.
func printFormattedLines(lines []string, outputFormat string) {
	for _, line := range lines {
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", formatLogLine(line, outputFormat)) //nolint:gosec // output to stdout, not web context
	}
}

// followFile follows a single file for new content.
func (t *LogTailer) followFile(file *os.File, path string, outputFormat string) error {
	_, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("failed to seek to end of file: %w", err)
	}

	reader := bufio.NewReader(file)

	ticker := time.NewTicker(tailTickerMilliseconds * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		//nolint:noinlineerr // Preserving existing error handling pattern for no functional changes
		if err := checkFileExists(path); err != nil {
			return err
		}

		//nolint:noinlineerr // Preserving existing error handling pattern for no functional changes
		if err := readAndPrintLines(reader, outputFormat); err != nil {
			return err
		}
	}

	return nil
}

// checkFileExists verifies the file still exists.
func checkFileExists(path string) error {
	_, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrLogFileRemoved
		}

		return fmt.Errorf("failed to stat file: %w", err)
	}

	return nil
}

// readAndPrintLines reads new lines from reader and prints them.
func readAndPrintLines(reader *bufio.Reader, outputFormat string) error {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				if len(line) > 0 {
					printTrimmedLine(line, outputFormat)
				}

				break
			}

			return fmt.Errorf("error reading file: %w", err)
		}

		printTrimmedLine(line, outputFormat)
	}

	return nil
}

// printTrimmedLine trims and prints a line.
func printTrimmedLine(line, outputFormat string) {
	trimmedLine := line
	if len(trimmedLine) > 0 && trimmedLine[len(trimmedLine)-1] == '\n' {
		trimmedLine = trimmedLine[:len(trimmedLine)-1]
	}

	_, _ = fmt.Fprintf(os.Stdout, "%s\n", formatLogLine(trimmedLine, outputFormat)) //nolint:gosec // output to stdout, not web context
}

// followMultipleFiles follows multiple files for new content.
func (t *LogTailer) followMultipleFiles(paths []string, outputFormat string) error {
	files, err := openFilesForFollow(paths)
	if err != nil {
		return err
	}

	ticker := time.NewTicker(tailTickerMilliseconds * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		processFileUpdates(files, outputFormat)
	}

	return nil
}

// fileInfo holds information for following a file.
type fileInfo struct {
	path   string
	file   *os.File
	reader *bufio.Reader
}

// openFilesForFollow opens and seeks to end of all files for following.
func openFilesForFollow(paths []string) ([]*fileInfo, error) {
	files := make([]*fileInfo, 0, len(paths))

	for _, path := range paths {
		cleanPath := filepath.Clean(path)

		file, err := os.Open(cleanPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening %s: %v\n", path, err)

			continue
		}

		defer func() {
			_ = file.Close()
		}()

		_, _ = file.Seek(0, io.SeekEnd)

		files = append(files, &fileInfo{
			path:   path,
			file:   file,
			reader: bufio.NewReader(file),
		})
	}

	if len(files) == 0 {
		return nil, ErrNoLogsCouldOpen
	}

	return files, nil
}

// processFileUpdates checks all files for new content and prints it.
func processFileUpdates(files []*fileInfo, outputFormat string) {
	for _, fileData := range files {
		//nolint:noinlineerr // Preserving existing error handling pattern for no functional changes
		if _, err := os.Stat(fileData.path); err != nil {
			continue
		}

		newLines := readNewLines(fileData.reader, outputFormat)
		if len(newLines) > 0 {
			printFileHeader(fileData.path)
			printLines(newLines)
		}
	}
}

// readNewLines reads all new lines from the reader.
func readNewLines(reader *bufio.Reader, outputFormat string) []string {
	var lines []string

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}

			continue
		}

		trimmedLine := trimLineEnding(line)
		lines = append(lines, formatLogLine(trimmedLine, outputFormat))
	}

	return lines
}

// trimLineEnding removes trailing newline from a line.
func trimLineEnding(line string) string {
	if len(line) > 0 && line[len(line)-1] == '\n' {
		return line[:len(line)-1]
	}

	return line
}

// printFileHeader prints the header for a file's output.
func printFileHeader(path string) {
	_, _ = fmt.Fprintf(os.Stdout, "\n==> %s <==\n", filepath.Base(path))
}

// printLines prints multiple lines to stdout.
func printLines(lines []string) {
	for _, line := range lines {
		_, _ = fmt.Fprintf(os.Stdout, "%s\n", line) //nolint:gosec // output to stdout, not web context
	}
}

// splitLines splits buffer into lines.
func splitLines(data []byte) []string {
	var (
		lines []string
		line  []byte
	)

	for _, b := range data {
		if b == '\n' {
			lines = append(lines, string(line))
			line = nil
		} else {
			line = append(line, b)
		}
	}

	// Add last line if not empty
	if len(line) > 0 {
		lines = append(lines, string(line))
	}

	return lines
}
