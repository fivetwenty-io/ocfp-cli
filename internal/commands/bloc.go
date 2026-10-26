package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/blocinv"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Errors returned by the bloc subcommands.
var (
	// ErrBlocNotFound is returned when show names a bloc with no config entry
	// and no state file.
	ErrBlocNotFound = errors.New("bloc not found in config or state")

	// ErrNoCurrentBloc is returned when show gets no argument and no current
	// bloc resolves.
	ErrNoCurrentBloc = errors.New("no bloc named and no current bloc set")
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
--bloc, then the only configured bloc.

ocfp bloc show prints a summary of one bloc. It never prints secrets: the
config summary is built from an allowlist of safe fields, and the state
summary reports counts and a few names instead of output values.`,
		Example: `  ocfp bloc list
  ocfp bloc ls --json
  ocfp bloc show ocfp-lab-wayneeseguin
  ocfp bloc show --json`,
	}

	cmd.AddCommand(newBlocListCmd(), newBlocShowCmd())

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

func newBlocShowCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "show [bloc]",
		Short: "Show a safe summary of one bloc",
		Long: `Show what ocfp knows about one bloc from local data. With no argument it
shows the current bloc. If no bloc resolves, the command fails with a hint to
run ocfp bloc list.

The Config section lists only an allowlist of safe fields: the provider, iaas,
region, project and org ids, the network CIDR, whether artifacts is enabled,
and the config file. Any other config field stays hidden, including every
token, key, and password. The State section reports the bastion id and
addresses, the network, resource counts by type, and how many outputs the
state holds. The reserved outputs appear as a count and a few names, and no
output value other than the bastion addresses is ever printed.

A bloc whose state file cannot be parsed is shown with the source broken
instead of failing the command. With --json, the same data is emitted as one
object.`,
		Example: `  ocfp bloc show
  ocfp bloc show ocfp-lab-wayneeseguin
  ocfp bloc show ocfp-lab-wayneeseguin --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true

			name := ""
			if len(args) > 0 {
				name = args[0]
			}

			return runBlocShow(cmd.OutOrStdout(), name, asJSON)
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit output as JSON")

	return cmd
}

// blocShowReport is the JSON shape of `bloc show`.
type blocShowReport struct {
	Name        string                 `json:"name"`
	Source      string                 `json:"source"`
	Current     bool                   `json:"current"`
	BrokenError string                 `json:"broken_error,omitempty"`
	Config      *blocinv.ConfigSummary `json:"config,omitempty"`
	State       *blocinv.StateSummary  `json:"state,omitempty"`
}

func runBlocShow(out io.Writer, name string, asJSON bool) error {
	opts := blocInventoryOptions()

	if name == "" {
		name = opts.Current
	}

	if name == "" {
		return fmt.Errorf("%w; run `ocfp bloc list` to see the available blocs", ErrNoCurrentBloc)
	}

	entries, err := blocinv.Collect(opts)
	if err != nil {
		return fmt.Errorf("collecting blocs: %w", err)
	}

	entry, found := blocinv.Find(entries, name)
	if !found {
		return fmt.Errorf("%w: %q; run `ocfp bloc list` to see the available blocs", ErrBlocNotFound, name)
	}

	summary, err := blocinv.Summarize(entry)
	if err != nil {
		return fmt.Errorf("summarizing bloc %s: %w", name, err)
	}

	report := blocShowReport{
		Name:        entry.Name,
		Source:      entry.Source(),
		Current:     entry.Current,
		BrokenError: entry.BrokenError,
		Config:      summary.Config,
		State:       summary.State,
	}

	if asJSON {
		return writeBlocJSON(out, report)
	}

	return writeBlocShowText(out, entry, report)
}

// writeBlocShowText prints the Bloc, Config, and State sections.
func writeBlocShowText(out io.Writer, entry blocinv.Entry, report blocShowReport) error {
	var text strings.Builder

	field := func(label, value string) {
		fmt.Fprintf(&text, "  %-20s %s\n", label+":", value)
	}

	text.WriteString("Bloc\n")
	field("Name", report.Name)
	field("Source", report.Source)
	field("Current", yesNo(report.Current))

	if report.BrokenError != "" {
		field("Problem", report.BrokenError)
		field("State file", entry.StatePath)
	}

	text.WriteString("\nConfig\n")

	if cfg := report.Config; cfg == nil {
		text.WriteString("  no config entry\n")
	} else {
		field("File", cfg.File)

		if len(entry.ConfigFiles) > 1 {
			field("Also defined in", strings.Join(entry.ConfigFiles[1:], ", "))
		}

		field("Provider", hyphenIfEmpty(cfg.Provider))
		field("IaaS", hyphenIfEmpty(cfg.IaaS))
		field("Region", hyphenIfEmpty(cfg.Region))
		field("Project ID", hyphenIfEmpty(cfg.ProjectID))
		field("Org ID", hyphenIfEmpty(cfg.OrgID))
		field("Network CIDR", hyphenIfEmpty(cfg.NetworkCIDR))
		field("Artifacts enabled", yesNo(cfg.ArtifactsEnabled))
	}

	text.WriteString("\nState\n")

	switch st := report.State; {
	case st != nil:
		writeBlocStateText(field, st)
	case entry.Broken:
		text.WriteString("  state file cannot be loaded\n")
	default:
		text.WriteString("  no state\n")
	}

	_, err := io.WriteString(out, text.String())
	if err != nil {
		return fmt.Errorf("writing bloc summary: %w", err)
	}

	return nil
}

// writeBlocStateText prints the fields of a state summary through field.
func writeBlocStateText(field func(label, value string), st *blocinv.StateSummary) {
	field("Provider", hyphenIfEmpty(st.Provider))
	field("Region", hyphenIfEmpty(st.Region))
	field("Created", formatUpdated(st.CreatedAt))
	field("Updated", formatUpdated(st.UpdatedAt))
	field("Bastion ID", hyphenIfEmpty(st.Bastion.ID))
	field("Bastion private IP", hyphenIfEmpty(st.Bastion.PrivateIP))
	field("Bastion public IP", hyphenIfEmpty(st.Bastion.PublicIP))
	field("Bastion SSH host", hyphenIfEmpty(st.Bastion.SSHHost))
	field("Network", hyphenIfEmpty(st.Network.Name))
	field("Network CIDR", hyphenIfEmpty(st.Network.CIDR))

	kinds := make([]string, 0, len(st.Resources))
	for kind := range st.Resources {
		kinds = append(kinds, kind)
	}

	sort.Strings(kinds)

	counts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		counts = append(counts, fmt.Sprintf("%s=%d", kind, st.Resources[kind]))
	}

	field("Resources", hyphenIfEmpty(strings.Join(counts, " ")))
	field("Outputs", strconv.Itoa(st.OutputCount))
	field("Reserved outputs", strconv.Itoa(st.ReservedOutputCount))

	if len(st.ReservedOutputSample) > 0 {
		field("Reserved sample", strings.Join(st.ReservedOutputSample, ", "))
	}
}

// yesNo renders a boolean for the text summary.
func yesNo(value bool) string {
	if value {
		return "yes"
	}

	return "no"
}
