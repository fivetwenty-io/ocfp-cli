package commands

import (
	"fmt"
	"os"

	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	// exportFilePermissions defines the file mode for exported state files.
	exportFilePermissions = 0644
)

// newStateExportCmd creates the 'state export' subcommand.
func newStateExportCmd() *cobra.Command {
	var (
		displayFlags ResourceDisplayFlags
		outputFormat string
		filterBy     []string
		sortBy       string
		search       string
		outputFile   string
		overwrite    bool
	)

	cmd := &cobra.Command{
		Use:          "export [file]",
		Short:        "Export state to file",
		SilenceUsage: true,
		Long: `Export state to a file in JSON, YAML, or table format.

Supports all filtering, sorting, and search options from the main state command.
Output file can be specified as an argument or via --output-file flag.
Use "-" as filename to write to stdout.`,
		Example: `  # Export all resources to JSON
  ocfp state export --bloc dev state.json -o json

  # Export filtered resources to YAML
  ocfp state export --bloc dev --filter-by "state=running" state.yaml -o yaml

  # Export sorted and searched resources
  ocfp state export --bloc dev --search web --sort-by name servers.json -o json

  # Export to stdout
  ocfp state export --bloc dev - -o json`,
		Args: cobra.MaximumNArgs(1),
		RunE: runStateExport,
	}

	// Add resource display flags
	AddResourceDisplayFlags(cmd, &displayFlags)

	// Add output format flag
	cmd.Flags().StringVarP(&outputFormat, "output", "o", OutputFormatJSON, "output format: table|json|yaml")

	// Add property filter flag
	cmd.Flags().StringArrayVar(&filterBy, "filter-by", []string{}, "filter by property (e.g., name=web-*, state=running, tags.env=prod)")

	// Add sort flag
	cmd.Flags().StringVar(&sortBy, "sort-by", "", "sort by field (name, date, state, type) with optional :asc or :desc (e.g., name:desc)")

	// Add search flag
	cmd.Flags().StringVar(&search, "search", "", "search resources by keyword")

	// Add export-specific flags
	cmd.Flags().StringVar(&outputFile, "output-file", "", "output file path (alternative to positional argument)")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "overwrite existing file without confirmation")

	// Bind flags to viper
	_ = viper.BindPFlag("state.export.output", cmd.Flags().Lookup("output"))
	_ = viper.BindPFlag("state.export.filter_by", cmd.Flags().Lookup("filter-by"))
	_ = viper.BindPFlag("state.export.sort_by", cmd.Flags().Lookup("sort-by"))
	_ = viper.BindPFlag("state.export.search", cmd.Flags().Lookup("search"))
	_ = viper.BindPFlag("state.export.output_file", cmd.Flags().Lookup("output-file"))
	_ = viper.BindPFlag("state.export.overwrite", cmd.Flags().Lookup("overwrite"))

	return cmd
}

// determineOutputFile determines the output file path from args or flags.
func determineOutputFile(args []string) (string, error) {
	outputFile := viper.GetString("state.export.output_file")
	if len(args) > 0 {
		outputFile = args[0]
	}

	if outputFile == "" {
		return "", ErrOutputFileRequired
	}

	return outputFile, nil
}

// checkFileOverwrite checks if file exists and handles overwrite permission.
func checkFileOverwrite(outputFile string) error {
	if outputFile == "-" {
		return nil
	}

	_, statErr := os.Stat(outputFile)
	if statErr == nil {
		overwrite := viper.GetBool("state.export.overwrite")
		if !overwrite {
			return fmt.Errorf("%w %q, use --overwrite to replace", ErrFileAlreadyExists, outputFile)
		}
	}

	return nil
}

// loadStateForExport loads the state for the given bloc.
func loadStateForExport(blocName string) (*state.State, error) {
	stateDir, err := state.GetStateDir(blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to determine state directory: %w", err)
	}

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create state manager: %w", err)
	}

	currentState, err := stateManager.Load(blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to load state for bloc %q: %w", blocName, err)
	}

	return currentState, nil
}

// createResourceFilter creates a resource filter from command flags and queries.
func createResourceFilter(cmd *cobra.Command) (*ResourceFilter, error) {
	displayFlags := ParseDisplayFlagsFromCmd(cmd)
	filterByQueries := viper.GetStringSlice("state.export.filter_by")

	if len(filterByQueries) == 0 {
		return NewResourceFilter(displayFlags), nil
	}

	propertyFilters, err := NewPropertyFilterSet(filterByQueries)
	if err != nil {
		return nil, fmt.Errorf("invalid filter query: %w", err)
	}

	return NewResourceFilterWithProperties(displayFlags, propertyFilters), nil
}

// createSearchOptions creates search options from viper config.
func createSearchOptions() (*SearchOptions, error) {
	searchQuery := viper.GetString("state.export.search")
	if searchQuery == "" {
		return nil, nil
	}

	searchOpts, err := NewSearchOptions(searchQuery, false)
	if err != nil {
		return nil, fmt.Errorf("invalid search query: %w", err)
	}

	return searchOpts, nil
}

// createSortOptions creates sort options from viper config.
func createSortOptions() (*SortOptions, error) {
	sortByStr := viper.GetString("state.export.sort_by")
	if sortByStr == "" {
		return nil, nil
	}

	sortOpts, err := ParseSortBy(sortByStr)
	if err != nil {
		return nil, fmt.Errorf("invalid sort-by: %w", err)
	}

	return sortOpts, nil
}

// applySortToState applies sorting to the state resources.
func applySortToState(currentState *state.State, sortOpts *SortOptions, resources map[string]*state.Resource) *state.State {
	sortedList := SortResourceMap(resources, sortOpts)
	sortedState := &state.State{
		Version:      currentState.Version,
		BlocName:     currentState.BlocName,
		Provider:     currentState.Provider,
		Region:       currentState.Region,
		CreatedAt:    currentState.CreatedAt,
		UpdatedAt:    currentState.UpdatedAt,
		Outputs:      currentState.Outputs,
		Dependencies: currentState.Dependencies,
		Resources:    make(map[string]*state.Resource),
	}

	for _, resource := range sortedList {
		sortedState.Resources[resource.ID] = resource
	}

	return sortedState
}

// applyExportFiltersAndSorting applies all filtering, searching, and sorting to the state.
func applyExportFiltersAndSorting(cmd *cobra.Command, currentState *state.State) (*state.State, error) {
	filter, err := createResourceFilter(cmd)
	if err != nil {
		return nil, err
	}

	searchOpts, err := createSearchOptions()
	if err != nil {
		return nil, err
	}

	sortOpts, err := createSortOptions()
	if err != nil {
		return nil, err
	}

	filteredResources := filter.FilterResources(currentState.Resources)

	if searchOpts != nil {
		filteredResources = SearchResources(filteredResources, searchOpts)
	}

	if sortOpts != nil {
		return applySortToState(currentState, sortOpts, filteredResources), nil
	}

	currentState.Resources = filteredResources

	return currentState, nil
}

// formatStateForExport formats state according to output format.
func formatStateForExport(currentState *state.State, outputFormat string) (string, error) {
	var (
		output string
		err    error
	)

	switch outputFormat {
	case OutputFormatJSON:
		output, err = FormatStateJSON(currentState, nil)
		if err != nil {
			return "", fmt.Errorf("failed to format state as JSON: %w", err)
		}

	case OutputFormatYAML:
		output, err = FormatStateYAML(currentState, nil)
		if err != nil {
			return "", fmt.Errorf("failed to format state as YAML: %w", err)
		}

	case OutputFormatTable:
		allFilter := NewResourceFilter(ResourceDisplayFlags{All: true})
		table := FormatStateOutputFiltered(currentState, allFilter)
		output = fmt.Sprintf("%s", table)

	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedOutputFormat, outputFormat)
	}

	return output, nil
}

// writeExportOutput writes formatted output to file or stdout.
func writeExportOutput(output, outputFile, outputFormat string) error {
	if outputFile == "-" {
		_, _ = fmt.Fprint(os.Stdout, output)
	} else {
		err := os.WriteFile(outputFile, []byte(output), exportFilePermissions)
		if err != nil {
			return fmt.Errorf("failed to write to file %q: %w", outputFile, err)
		}

		_, _ = fmt.Fprintf(os.Stdout, "State exported to %q (%s format)\n", outputFile, outputFormat)
	}

	return nil
}

// runStateExport is the execution handler for 'state export' command.
func runStateExport(cmd *cobra.Command, args []string) error {
	blocName := viper.GetString("bloc")
	if blocName == "" {
		return ErrBlocRequired
	}

	outputFile, err := determineOutputFile(args)
	if err != nil {
		return err
	}

	outputFormat := viper.GetString("state.export.output")

	err = ValidateOutputFormat(outputFormat)
	if err != nil {
		return err
	}

	err = checkFileOverwrite(outputFile)
	if err != nil {
		return err
	}

	currentState, err := loadStateForExport(blocName)
	if err != nil {
		return err
	}

	if len(currentState.Resources) == 0 {
		_, _ = fmt.Fprintf(os.Stdout, "No resources found in state for bloc %q\n", blocName)

		return nil
	}

	currentState, err = applyExportFiltersAndSorting(cmd, currentState)
	if err != nil {
		return err
	}

	output, err := formatStateForExport(currentState, outputFormat)
	if err != nil {
		return err
	}

	return writeExportOutput(output, outputFile, outputFormat)
}
