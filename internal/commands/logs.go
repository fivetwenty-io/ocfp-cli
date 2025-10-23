package commands

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Default configuration values for logs command.
const (
	defaultTailLines = 10 // Default number of lines to show when tailing logs
)

// Static errors for logs command.
var (
	ErrNoActiveCommands     = errors.New("no active commands running")
	ErrNoLogsFound          = errors.New("no logs found")
	ErrNoLogsFoundCommand   = errors.New("no logs found for commands")
	ErrLogFileDoesNotExist  = errors.New("log file does not exist")
)

// LogsFlags holds flags for the logs command.
type LogsFlags struct {
	active       bool
	recent       bool
	all          bool
	outputFormat string
	follow       bool
	lines        int
}

// NewLogsCmd creates the logs command for viewing CLI invocation logs.
func NewLogsCmd() *cobra.Command {
	flags := &LogsFlags{}

	cmd := &cobra.Command{
		Use:          "logs [commands...]",
		Short:        "View and manage OCFP CLI invocation logs",
		SilenceUsage: true,
		Long:         getLogsLongDescription(),
		Example:      getLogsExamples(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogsList(cmd, args, flags)
		},
	}

	// Add flags
	cmd.Flags().BoolVar(&flags.active, "active", false, "show only currently running commands")
	cmd.Flags().BoolVar(&flags.recent, "recent", false, "show most recent log per command")
	cmd.Flags().BoolVar(&flags.all, "all", false, "show all log files")
	cmd.Flags().StringVarP(&flags.outputFormat, "output", "o", "table", "output format: table, json, yaml")

	// Bind flags to viper
	_ = viper.BindPFlag("logs.active", cmd.Flags().Lookup("active"))
	_ = viper.BindPFlag("logs.recent", cmd.Flags().Lookup("recent"))
	_ = viper.BindPFlag("logs.all", cmd.Flags().Lookup("all"))
	_ = viper.BindPFlag("logs.output", cmd.Flags().Lookup("output"))

	// Add tail subcommand
	cmd.AddCommand(newLogsTailCmd(flags))

	return cmd
}

// newLogsTailCmd creates the logs tail subcommand.
func newLogsTailCmd(flags *LogsFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "tail [commands...] or tail [file-path]",
		Aliases: []string{"t"},
		Short:   "Tail log files",
		Long: `Tail one or more log files with optional follow mode.

You can specify:
- Command names (e.g., init, vault) to tail most recent logs for those commands
- Direct file path to tail a specific log file
- No arguments to tail the most recent log file overall
- --active flag to tail all currently running command logs`,
		Example: `  # Tail the most recent log
  ocfp logs tail

  # Tail init command logs
  ocfp logs tail init

  # Tail multiple commands
  ocfp logs tail init,vault

  # Tail with follow mode
  ocfp logs tail -f init

  # Tail a specific log file
  ocfp logs tail -f ~/.ocfp/520-aws-wayne/logs/init/20251023-070445.log

  # Tail last 100 lines and follow
  ocfp logs tail -f -n 100 init

  # Tail all active commands
  ocfp logs tail --active -f

  # Tail with raw JSON output
  ocfp logs tail init -o json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogsTail(cmd, args, flags)
		},
	}

	// Tail-specific flags
	cmd.Flags().BoolVarP(&flags.follow, "follow", "f", false, "follow log output (like tail -f)")
	cmd.Flags().IntVarP(&flags.lines, "lines", "n", defaultTailLines, "number of lines to show")
	cmd.Flags().BoolVar(&flags.active, "active", false, "tail all active command logs")
	cmd.Flags().StringVarP(&flags.outputFormat, "output", "o", "text", "output format: text, json")

	return cmd
}

// getLogsLongDescription returns the long description for the logs command.
func getLogsLongDescription() string {
	return `View and manage OCFP CLI invocation logs.

Log files are organized under ~/.ocfp/{bloc}/logs/{command}/[{subcommand}/]
with timestamps and optional request IDs.

Display Modes:
  Default:  Group logs by command with counts and latest timestamps
  --recent: Show most recent log per command (human-readable time/size)
  --active: Show currently running commands
  --all:    Show all log files

The --bloc flag can be used to filter logs for a specific bloc.

Commands can be filtered by providing comma-separated command names as arguments.`
}

// getLogsExamples returns usage examples for the logs command.
func getLogsExamples() string {
	return `  # Show logs grouped by command (default)
  ocfp logs

  # Show most recent log per command
  ocfp logs --recent

  # Show only currently running commands
  ocfp logs --active

  # Show all logs for specific commands
  ocfp logs init,vault --all

  # Filter by bloc
  ocfp --bloc 520-aws-wayne logs --recent

  # JSON output
  ocfp logs --recent -o json

  # YAML output
  ocfp logs --all -o yaml

  # Tail logs
  ocfp logs tail init
  ocfp logs tail -f -n 50 init
  ocfp logs tail --active -f`
}

// runLogsList handles the main logs list/display command.
func runLogsList(_ *cobra.Command, args []string, flags *LogsFlags) error {
	// Get bloc filter from viper
	blocFilter := viper.GetString("bloc")

	// Parse command filters from args
	var commandFilters []string
	if len(args) > 0 {
		commandFilters = parseCommandList(args[0])
	}

	// Get base directory
	baseDir, err := getLogsBaseDir()
	if err != nil {
		return fmt.Errorf("failed to determine logs directory: %w", err)
	}

	// Create scanner
	scanner := NewLogScanner(baseDir, blocFilter)

	// Scan logs
	entries, err := scanner.ScanLogs(commandFilters)
	if err != nil {
		return fmt.Errorf("failed to scan logs: %w", err)
	}

	if len(entries) == 0 {
		if blocFilter != "" {
			_, _ = fmt.Fprintf(os.Stdout, "No logs found for bloc: %s\n", blocFilter)
		} else {
			_, _ = fmt.Fprintf(os.Stdout, "No logs found\n")
		}

		return nil
	}

	// Filter active if requested
	if flags.active {
		tracker := NewCommandTracker(baseDir)

		entries, err = tracker.FilterActive(entries)
		if err != nil {
			return fmt.Errorf("failed to filter active logs: %w", err)
		}

		if len(entries) == 0 {
			_, _ = fmt.Fprintf(os.Stdout, "No active commands running\n")

			return nil
		}
	}

	// Display logs based on mode and format
	displayer := NewLogDisplayer(flags.outputFormat)

	switch {
	case flags.recent:
		return displayer.DisplayRecent(entries)
	case flags.all:
		return displayer.DisplayAll(entries)
	case flags.active:
		return displayer.DisplayActive(entries)
	default:
		// Default: grouped display
		return displayer.DisplayGrouped(entries)
	}
}

// runLogsTail handles the logs tail subcommand.
func runLogsTail(_ *cobra.Command, args []string, flags *LogsFlags) error {
	// Check if argument is a direct file path
	if len(args) > 0 && isFilePath(args[0]) {
		// Validate file exists
		_, err := os.Stat(args[0])
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("%w: %s", ErrLogFileDoesNotExist, args[0])
			}

			return fmt.Errorf("failed to access log file: %w", err)
		}

		// Tail the specified file directly
		tailer := NewLogTailer()

		return tailer.TailLogs([]string{args[0]}, flags.follow, flags.lines, flags.outputFormat)
	}

	blocFilter := viper.GetString("bloc")
	commandFilters := parseCommandFilters(args)

	baseDir, err := getLogsBaseDir()
	if err != nil {
		return fmt.Errorf("failed to determine logs directory: %w", err)
	}

	scanner := NewLogScanner(baseDir, blocFilter)

	logPaths, err := determineLogPaths(scanner, baseDir, commandFilters, flags.active)
	if err != nil {
		return err
	}

	tailer := NewLogTailer()

	return tailer.TailLogs(logPaths, flags.follow, flags.lines, flags.outputFormat)
}

// parseCommandFilters extracts command filters from arguments.
func parseCommandFilters(args []string) []string {
	if len(args) > 0 {
		return parseCommandList(args[0])
	}

	return nil
}

// determineLogPaths determines which log paths to tail based on flags and filters.
func determineLogPaths(scanner *LogScanner, baseDir string, commandFilters []string, activeOnly bool) ([]string, error) {
	switch {
	case activeOnly:
		return getActiveLogPaths(baseDir)
	case len(commandFilters) > 0:
		return getFilteredLogPaths(scanner, commandFilters)
	default:
		return getMostRecentLogPath(scanner)
	}
}

// getActiveLogPaths retrieves log paths for currently active commands.
func getActiveLogPaths(baseDir string) ([]string, error) {
	tracker := NewCommandTracker(baseDir)

	activeCommands, err := tracker.GetActiveCommands()
	if err != nil {
		return nil, fmt.Errorf("failed to get active commands: %w", err)
	}

	if len(activeCommands) == 0 {
		return nil, ErrNoActiveCommands
	}

	logPaths := make([]string, 0, len(activeCommands))
	for _, ac := range activeCommands {
		logPaths = append(logPaths, ac.LogPath)
	}

	return logPaths, nil
}

// getFilteredLogPaths retrieves most recent log for each specified command.
func getFilteredLogPaths(scanner *LogScanner, commandFilters []string) ([]string, error) {
	entries, err := scanner.ScanLogs(commandFilters)
	if err != nil {
		return nil, fmt.Errorf("failed to scan logs: %w", err)
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: %v", ErrNoLogsFoundCommand, commandFilters)
	}

	grouped := GroupByCommand(entries)

	logPaths := make([]string, 0, len(grouped))
	for _, logs := range grouped {
		if len(logs) > 0 {
			logPaths = append(logPaths, logs[0].Path)
		}
	}

	return logPaths, nil
}

// getMostRecentLogPath retrieves the most recent log overall.
func getMostRecentLogPath(scanner *LogScanner) ([]string, error) {
	entries, err := scanner.ScanLogs(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to scan logs: %w", err)
	}

	if len(entries) == 0 {
		return nil, ErrNoLogsFound
	}

	return []string{entries[0].Path}, nil
}

// isFilePath checks if a string looks like a file path rather than a command name.
func isFilePath(arg string) bool {
	// Check for absolute paths
	if len(arg) > 0 && arg[0] == '/' {
		return true
	}

	// Check for relative paths
	if len(arg) >= 2 && arg[:2] == "./" {
		return true
	}

	if len(arg) >= 3 && arg[:3] == "../" {
		return true
	}

	// Check for home directory paths
	if len(arg) > 0 && arg[0] == '~' {
		return true
	}

	return false
}
