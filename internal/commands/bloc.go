package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/blocinv"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// NewBlocCmd builds the `ocfp bloc` command group, which inspects the blocs
// the CLI knows about without changing anything.
func NewBlocCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bloc",
		Short: "List the blocs ocfp knows about",
		Long: `Inspect the blocs ocfp knows about. Every bloc command is read-only: it
changes no config, no state, and no infrastructure.

A bloc can be known from several places, and ocfp bloc list merges them into
one row per bloc. The sources are the blocs: map in every config file the CLI
searches, flat config files with a top-level name and provider, and the
per-bloc state directories under both the XDG state home and the legacy
~/.ocfp home. The SOURCE column says which of these a bloc has: config,
state, or both. When the XDG and legacy homes both hold a bloc, the XDG copy
wins, and when several config files define the same bloc, the first one in
search order is the one shown.

A state file that exists but cannot be parsed shows up with the SOURCE
broken, so a damaged bloc stays visible instead of vanishing from the list.
ocfp never deletes or repairs it.

The CURRENT column marks the bloc ocfp resolves as current, using the same
order as every other command: OCFP_BLOC, then the saved current bloc, then
--bloc, then the only configured bloc.`,
		Example: `  ocfp bloc list
  ocfp bloc ls --json`,
	}

	cmd.AddCommand(newBlocListCmd())

	return cmd
}

func newBlocListCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List every bloc found in config and state",
		Long: `List every bloc ocfp knows about, one row per bloc, merged from the config
files and state directories. See ocfp bloc --help for how the sources merge.

With --json, the same data is emitted as an array of objects with snake_case
fields, and a bloc with no state has a null updated_at.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBlocList(cmd.OutOrStdout(), asJSON)
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit output as JSON")

	return cmd
}

// blocInventoryOptions builds the inventory options for this process. It
// searches the explicit --config file, the XDG config home (which holds
// config.yml), and the directories findEnvironments has always scanned, and it
// reads state from the XDG state home and then the legacy home.
func blocInventoryOptions() blocinv.Options {
	var files []string
	if explicit := viper.GetString("config"); explicit != "" {
		files = append(files, explicit)
	}

	return blocinv.Options{
		ConfigFiles: files,
		ConfigDirs:  append([]string{config.ConfigHome()}, configSearchPaths()...),
		StateHomes:  []string{config.StateHome(), config.OcfpHome()},
		Current:     currentBlocName(),
	}
}

// currentBlocName resolves the current bloc in the order resolveBlocName in
// internal/cli uses: OCFP_BLOC, the saved current bloc, the --bloc flag, and
// then the only bloc the config defines. That function lives in a package
// that imports this one, so the order is repeated here and the two must stay
// in step.
func currentBlocName() string {
	if fromEnv := os.Getenv("OCFP_BLOC"); fromEnv != "" {
		return fromEnv
	}

	saved, err := config.GetCurrentBloc()
	if err == nil && saved != "" {
		return saved
	}

	if fromFlag := viper.GetString("bloc"); fromFlag != "" {
		return fromFlag
	}

	names, err := config.ListBlocNames(viper.GetString("config"))
	if err == nil && len(names) == 1 {
		return names[0]
	}

	return ""
}

// blocListRow is the JSON shape of one inventory entry.
type blocListRow struct {
	Name        string     `json:"name"`
	Provider    string     `json:"provider"`
	Region      string     `json:"region"`
	Source      string     `json:"source"`
	HasConfig   bool       `json:"has_config"`
	HasState    bool       `json:"has_state"`
	Broken      bool       `json:"broken"`
	BrokenError string     `json:"broken_error"`
	ConfigFile  string     `json:"config_file"`
	ConfigFiles []string   `json:"config_files"`
	StatePath   string     `json:"state_path"`
	Current     bool       `json:"current"`
	UpdatedAt   *time.Time `json:"updated_at"`
}

func newBlocListRow(entry blocinv.Entry) blocListRow {
	row := blocListRow{
		Name:        entry.Name,
		Provider:    entry.Provider,
		Region:      entry.Region,
		Source:      entry.Source(),
		HasConfig:   entry.HasConfig,
		HasState:    entry.HasState,
		Broken:      entry.Broken,
		BrokenError: entry.BrokenError,
		ConfigFile:  entry.ConfigFile,
		ConfigFiles: entry.ConfigFiles,
		StatePath:   entry.StatePath,
		Current:     entry.Current,
		UpdatedAt:   nil,
	}

	if row.ConfigFiles == nil {
		row.ConfigFiles = []string{}
	}

	if !entry.UpdatedAt.IsZero() {
		updated := entry.UpdatedAt.UTC()
		row.UpdatedAt = &updated
	}

	return row
}

func runBlocList(out io.Writer, asJSON bool) error {
	entries, err := blocinv.Collect(blocInventoryOptions())
	if err != nil {
		return fmt.Errorf("collecting blocs: %w", err)
	}

	if asJSON {
		rows := make([]blocListRow, 0, len(entries))
		for _, entry := range entries {
			rows = append(rows, newBlocListRow(entry))
		}

		return writeBlocJSON(out, rows)
	}

	if len(entries) == 0 {
		_, _ = fmt.Fprint(out, "No blocs found\n")

		return nil
	}

	tableWriter := tabwriter.NewWriter(out, 0, 0, TabwriterPadding, ' ', 0)
	_, _ = fmt.Fprintln(tableWriter, "NAME\tPROVIDER\tREGION\tSOURCE\tCONFIG\tUPDATED\tCURRENT")

	for _, entry := range entries {
		marker := ""
		if entry.Current {
			marker = "*"
		}

		_, _ = fmt.Fprintf(tableWriter, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			entry.Name,
			hyphenIfEmpty(entry.Provider),
			hyphenIfEmpty(entry.Region),
			entry.Source(),
			hyphenIfEmpty(entry.ConfigFile),
			formatUpdated(entry.UpdatedAt),
			marker)
	}

	return tableWriter.Flush()
}

// writeBlocJSON writes value as indented JSON followed by a newline.
func writeBlocJSON(out io.Writer, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding JSON: %w", err)
	}

	_, err = fmt.Fprintln(out, string(data))
	if err != nil {
		return fmt.Errorf("writing JSON: %w", err)
	}

	return nil
}

// hyphenIfEmpty renders an empty table cell as an ASCII hyphen. The plain
// hyphen keeps the table scriptable, unlike the em dash dashIfEmpty uses.
func hyphenIfEmpty(value string) string {
	if value == "" {
		return "-"
	}

	return value
}

// formatUpdated renders a state update time for the table, or a dash when the
// bloc has no state.
func formatUpdated(updated time.Time) string {
	if updated.IsZero() {
		return "-"
	}

	return updated.UTC().Format(time.RFC3339)
}
