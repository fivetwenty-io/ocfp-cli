package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/ui"
	"github.com/goccy/go-yaml"
)

// Output format constants for logs display.
const (
	formatJSON = "json"
	formatYAML = "yaml"
)

// LogDisplayer handles displaying log entries in various formats.
type LogDisplayer struct {
	format string
}

// NewLogDisplayer creates a new log displayer.
func NewLogDisplayer(format string) *LogDisplayer {
	return &LogDisplayer{
		format: format,
	}
}

// DisplayGrouped displays logs grouped by command (default mode).
func (d *LogDisplayer) DisplayGrouped(entries []LogEntry) error {
	grouped := GroupByCommand(entries)

	switch d.format {
	case formatJSON:
		return d.displayGroupedJSON(grouped)
	case formatYAML:
		return d.displayGroupedYAML(grouped)
	default:
		// Table format
		return d.displayGroupedTable(grouped)
	}
}

// DisplayRecent displays the most recent log per command.
func (d *LogDisplayer) DisplayRecent(entries []LogEntry) error {
	// Get most recent log per command
	grouped := GroupByCommand(entries)

	var recent []LogEntry

	for _, logs := range grouped {
		if len(logs) > 0 {
			recent = append(recent, logs[0])
		}
	}

	// Sort by timestamp (most recent first)
	sort.Slice(recent, func(i, j int) bool {
		return recent[i].Timestamp.After(recent[j].Timestamp)
	})

	switch d.format {
	case formatJSON:
		return d.displayEntriesJSON(recent)
	case formatYAML:
		return d.displayEntriesYAML(recent)
	default:
		// Table format
		return d.displayRecentTable(recent)
	}
}

// DisplayActive displays currently running commands.
func (d *LogDisplayer) DisplayActive(entries []LogEntry) error {
	switch d.format {
	case formatJSON:
		return d.displayEntriesJSON(entries)
	case formatYAML:
		return d.displayEntriesYAML(entries)
	default:
		// Table format
		return d.displayActiveTable(entries)
	}
}

// DisplayAll displays all log entries.
func (d *LogDisplayer) DisplayAll(entries []LogEntry) error {
	switch d.format {
	case formatJSON:
		return d.displayEntriesJSON(entries)
	case formatYAML:
		return d.displayEntriesYAML(entries)
	default:
		// Table format
		return d.displayAllTable(entries)
	}
}

// displayGroupedTable displays grouped logs in table format.
func (d *LogDisplayer) displayGroupedTable(grouped map[string][]LogEntry) error {
	table := &ui.Table{
		Title:   "OCFP CLI Logs",
		Summary: fmt.Sprintf("Logs grouped by command (%d commands)", len(grouped)),
	}

	rows := d.buildGroupedTableRows(grouped)

	table.Sections = []ui.Section{
		{
			Title:   "Commands",
			Headers: []string{"COMMAND", "COUNT", "LATEST", "SIZE", "STATUS"},
			Rows:    rows,
		},
	}

	return d.renderTable(table)
}

// buildGroupedTableRows builds table rows from grouped log entries.
func (d *LogDisplayer) buildGroupedTableRows(grouped map[string][]LogEntry) [][]string {
	// Create a sorted list of command names
	commands := make([]string, 0, len(grouped))
	for cmd := range grouped {
		commands = append(commands, cmd)
	}

	sort.Strings(commands)

	// Build rows
	rows := make([][]string, 0, len(commands))

	for _, cmd := range commands {
		logs := grouped[cmd]
		if len(logs) == 0 {
			continue
		}

		// Count active logs
		activeCount := 0

		for _, log := range logs {
			if log.IsActive {
				activeCount++
			}
		}

		// Get most recent log
		latest := logs[0]
		timestamp := latest.Timestamp.Format(time.RFC3339)
		size := humanizeSize(latest.Size)

		activeStr := ""
		if activeCount > 0 {
			activeStr = fmt.Sprintf(" [%d active]", activeCount)
		}

		row := []string{
			cmd,
			strconv.Itoa(len(logs)),
			timestamp,
			size,
			activeStr,
		}
		rows = append(rows, row)
	}

	return rows
}

// renderTable renders a table using the UI renderer.
func (d *LogDisplayer) renderTable(table *ui.Table) error {
	err := ui.Render(table, "table")
	if err != nil {
		return fmt.Errorf("failed to render table: %w", err)
	}

	return nil
}

// displayRecentTable displays recent logs in table format.
func (d *LogDisplayer) displayRecentTable(entries []LogEntry) error {
	table := &ui.Table{
		Title:   "Recent OCFP CLI Logs",
		Summary: fmt.Sprintf("Most recent log per command (%d commands)", len(entries)),
	}

	rows := make([][]string, 0, len(entries))

	for _, entry := range entries {
		cmd := entry.Command
		if entry.Subcommand != "" {
			cmd = entry.Command + "." + entry.Subcommand
		}

		timestamp := entry.Timestamp.Format(time.RFC3339)
		size := humanizeSize(entry.Size)

		row := []string{
			cmd,
			timestamp,
			size,
			entry.Path,
		}
		rows = append(rows, row)
	}

	table.Sections = []ui.Section{
		{
			Headers: []string{"COMMAND", "TIME", "SIZE", "PATH"},
			Rows:    rows,
		},
	}

	err := ui.Render(table, "table")
	if err != nil {
		return fmt.Errorf("failed to render table: %w", err)
	}

	return nil
}

// displayActiveTable displays active logs in table format.
func (d *LogDisplayer) displayActiveTable(entries []LogEntry) error {
	table := &ui.Table{
		Title:   "Active OCFP Commands",
		Summary: fmt.Sprintf("%d commands currently running", len(entries)),
	}

	rows := make([][]string, 0, len(entries))

	for _, entry := range entries {
		cmd := entry.Command
		if entry.Subcommand != "" {
			cmd = entry.Command + "." + entry.Subcommand
		}

		started := entry.Timestamp.Format(time.RFC3339)

		row := []string{
			cmd,
			strconv.Itoa(entry.PID),
			started,
			entry.Path,
		}
		rows = append(rows, row)
	}

	table.Sections = []ui.Section{
		{
			Headers: []string{"COMMAND", "PID", "STARTED", "LOG PATH"},
			Rows:    rows,
		},
	}

	err := ui.Render(table, "table")
	if err != nil {
		return fmt.Errorf("failed to render table: %w", err)
	}

	return nil
}

// displayAllTable displays all logs in table format.
func (d *LogDisplayer) displayAllTable(entries []LogEntry) error {
	table := &ui.Table{
		Title:   "All OCFP CLI Logs",
		Summary: fmt.Sprintf("%d log files", len(entries)),
	}

	rows := make([][]string, 0, len(entries))

	for _, entry := range entries {
		cmd := entry.Command
		if entry.Subcommand != "" {
			cmd = entry.Command + "." + entry.Subcommand
		}

		timestamp := entry.Timestamp.Format(time.RFC3339)
		size := humanizeSize(entry.Size)

		bloc := entry.Bloc
		if bloc == "" {
			bloc = "-"
		}

		row := []string{
			cmd,
			bloc,
			timestamp,
			size,
			entry.Path,
		}
		rows = append(rows, row)
	}

	table.Sections = []ui.Section{
		{
			Headers: []string{"COMMAND", "BLOC", "TIME", "SIZE", "PATH"},
			Rows:    rows,
		},
	}

	err := ui.Render(table, "table")
	if err != nil {
		return fmt.Errorf("failed to render table: %w", err)
	}

	return nil
}

// displayGroupedJSON displays grouped logs in JSON format.
func (d *LogDisplayer) displayGroupedJSON(grouped map[string][]LogEntry) error {
	type commandGroup struct {
		Command string     `json:"command"`
		Count   int        `json:"count"`
		Active  int        `json:"active"`
		Logs    []LogEntry `json:"logs"`
	}

	groups := make([]commandGroup, 0, len(grouped))

	for cmd, logs := range grouped {
		activeCount := 0

		for _, log := range logs {
			if log.IsActive {
				activeCount++
			}
		}

		groups = append(groups, commandGroup{
			Command: cmd,
			Count:   len(logs),
			Active:  activeCount,
			Logs:    logs,
		})
	}

	// Sort by command name
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Command < groups[j].Command
	})

	return outputJSON(groups)
}

// displayGroupedYAML displays grouped logs in YAML format.
func (d *LogDisplayer) displayGroupedYAML(grouped map[string][]LogEntry) error {
	type commandGroup struct {
		Command string     `yaml:"command"`
		Count   int        `yaml:"count"`
		Active  int        `yaml:"active"`
		Logs    []LogEntry `yaml:"logs"`
	}

	groups := make([]commandGroup, 0, len(grouped))

	for cmd, logs := range grouped {
		activeCount := 0

		for _, log := range logs {
			if log.IsActive {
				activeCount++
			}
		}

		groups = append(groups, commandGroup{
			Command: cmd,
			Count:   len(logs),
			Active:  activeCount,
			Logs:    logs,
		})
	}

	// Sort by command name
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Command < groups[j].Command
	})

	return outputYAML(groups)
}

// displayEntriesJSON displays log entries in JSON format.
func (d *LogDisplayer) displayEntriesJSON(entries []LogEntry) error {
	return outputJSON(entries)
}

// displayEntriesYAML displays log entries in YAML format.
func (d *LogDisplayer) displayEntriesYAML(entries []LogEntry) error {
	return outputYAML(entries)
}

// humanizeSize converts bytes to human-readable format.
func humanizeSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	units := []string{"KB", "MB", "GB", "TB"}

	return fmt.Sprintf("%.1f %s", float64(bytes)/float64(div), units[exp])
}

// outputJSON outputs data as JSON to stdout.
func outputJSON(v interface{}) error {
	encoder := os.Stdout

	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	_, err = encoder.Write(data)
	if err != nil {
		return fmt.Errorf("failed to write JSON: %w", err)
	}

	_, _ = encoder.WriteString("\n")

	return nil
}

// outputYAML outputs data as YAML to stdout.
func outputYAML(v interface{}) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %w", err)
	}

	_, err = os.Stdout.Write(data)
	if err != nil {
		return fmt.Errorf("failed to write YAML: %w", err)
	}

	return nil
}
