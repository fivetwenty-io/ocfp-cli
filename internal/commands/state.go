package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// NewStateCmd creates the state command with subcommands for state management.
func NewStateCmd() *cobra.Command {
	var (
		displayFlags ResourceDisplayFlags
		outputFormat string
		filterBy     []string
		sortBy       string
		search       string
	)

	cmd := &cobra.Command{
		Use:          "state",
		Short:        "Manage infrastructure state",
		SilenceUsage: true,
		Long:         getStateLongDescription(),
		Example:      getStateExamples(),
		RunE:         runStateDisplay,
	}

	addStateFlags(cmd, &displayFlags, &outputFormat, &filterBy, &sortBy, &search)
	bindStateViperFlags(cmd)

	// Add subcommands
	cmd.AddCommand(newStateSyncCmd())
	cmd.AddCommand(newStateExportCmd())

	return cmd
}

// getStateLongDescription returns the long description for the state command.
func getStateLongDescription() string {
	return `Manage infrastructure state files for OCFP blocs.

The state command provides operations for viewing and synchronizing infrastructure
state. State files track all resources managed by OCFP for a given bloc.

When run without a subcommand, displays the current state in table format.

Resource Display Flags:
By default, all resources are displayed. Use flags to filter specific resource types:

  --all                     Show all resources (default)
  --servers, --instances    Show compute instances
  --volumes                 Show block volumes
  --buckets                 Show object storage buckets
  --load-balancers, --lbs   Show load balancers
  --public-ips              Show public IP addresses
  --keys, --key-pairs       Show SSH key pairs
  --networks, --nets        Show networks/VPCs
  --subnets                 Show subnets
  --security-groups, --sgs  Show security groups
  --routers                 Show routers
  --snapshots               Show volume snapshots

Flags can be combined to show multiple resource types.

Available subcommands:
- sync: Reconcile cloud infrastructure into state file
- export: Export state to file with filtering and formatting options`
}

// getStateExamples returns the examples for the state command.
func getStateExamples() string {
	return `  # Display all resources (default table format)
  ocfp state --bloc dev

  # Display as JSON
  ocfp state --bloc dev -o json

  # Display only compute instances
  ocfp state --bloc dev --servers

  # Display servers and volumes
  ocfp state --bloc dev --servers --volumes

  # Filter by property
  ocfp state --bloc dev --filter-by "state=running"
  ocfp state --bloc dev --filter-by "name=web-*"
  ocfp state --bloc dev --filter-by "tags.env=prod"

  # Search resources
  ocfp state --bloc dev --search "web"

  # Sort resources
  ocfp state --bloc dev --sort-by name
  ocfp state --bloc dev --sort-by date:desc

  # Combine filters, search, and sort
  ocfp state --bloc dev --filter-by "state=running" --search web --sort-by name -o json

  # Export to file
  ocfp state export --bloc dev state.json -o json
  ocfp state export --bloc dev --filter-by "state=running" servers.yaml -o yaml

  # Sync state from cloud provider
  ocfp state sync --bloc dev

  # Preview sync changes without applying
  ocfp state sync --bloc dev --dry-run`
}

// addStateFlags adds all command flags to the state command.
func addStateFlags(cmd *cobra.Command, displayFlags *ResourceDisplayFlags, outputFormat *string, filterBy *[]string, sortBy, search *string) {
	AddResourceDisplayFlags(cmd, displayFlags)
	cmd.Flags().StringVarP(outputFormat, "output", "o", OutputFormatTable, "output format: table|json|yaml")
	cmd.Flags().StringArrayVar(filterBy, "filter-by", []string{}, "filter by property (e.g., name=web-*, state=running, tags.env=prod)")
	cmd.Flags().StringVar(sortBy, "sort-by", "", "sort by field (name, date, state, type) with optional :asc or :desc (e.g., name:desc)")
	cmd.Flags().StringVar(search, "search", "", "search resources by keyword (searches name, ID, type, state, properties, and tags)")
}

// bindStateViperFlags binds all state flags to viper.
func bindStateViperFlags(cmd *cobra.Command) {
	_ = viper.BindPFlag("state.output", cmd.Flags().Lookup("output"))
	_ = viper.BindPFlag("state.filter_by", cmd.Flags().Lookup("filter-by"))
	_ = viper.BindPFlag("state.sort_by", cmd.Flags().Lookup("sort-by"))
	_ = viper.BindPFlag("state.search", cmd.Flags().Lookup("search"))
}

// stateDisplayContext holds configuration for displaying state.
type stateDisplayContext struct {
	blocName        string
	outputFormat    string
	displayFlags    ResourceDisplayFlags
	propertyFilters *PropertyFilterSet
	searchOpts      *SearchOptions
	sortOpts        *SortOptions
}

// loadAndValidateState loads and validates state for the given bloc.
func loadAndValidateState(blocName string) (*state.State, error) {
	// Get standard state directory for this bloc
	stateDir, err := state.GetStateDir(blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to determine state directory: %w", err)
	}

	// Load state
	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create state manager: %w", err)
	}

	currentState, err := stateManager.Load(blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to load state for bloc %q: %w\n\nTo create state, run:\n  ocfp state sync --bloc %s", blocName, err, blocName)
	}

	// Check if state is empty
	if len(currentState.Resources) == 0 {
		_, _ = fmt.Fprintf(os.Stdout, "No resources found in state for bloc %q\n\nTo sync state from cloud provider:\n  ocfp state sync --bloc %s\n", blocName, blocName)

		return nil, nil
	}

	return currentState, nil
}

// parseDisplayContext extracts and validates all display options from the command.
func parseDisplayContext(cmd *cobra.Command, blocName string) (*stateDisplayContext, error) {
	outputFormat := viper.GetString("state.output")

	err := ValidateOutputFormat(outputFormat)
	if err != nil {
		return nil, err
	}

	// Parse display flags
	displayFlags := ParseDisplayFlagsFromCmd(cmd)

	// Parse property filters
	filterByQueries := viper.GetStringSlice("state.filter_by")

	var propertyFilters *PropertyFilterSet

	if len(filterByQueries) > 0 {
		var err error

		propertyFilters, err = NewPropertyFilterSet(filterByQueries)
		if err != nil {
			return nil, fmt.Errorf("invalid filter query: %w", err)
		}
	}

	// Parse search options
	searchQuery := viper.GetString("state.search")

	var searchOpts *SearchOptions

	if searchQuery != "" {
		var err error

		searchOpts, err = NewSearchOptions(searchQuery, false) // case-insensitive by default
		if err != nil {
			return nil, fmt.Errorf("invalid search query: %w", err)
		}
	}

	// Parse sort options
	sortByStr := viper.GetString("state.sort_by")

	var sortOpts *SortOptions

	if sortByStr != "" {
		var err error

		sortOpts, err = ParseSortBy(sortByStr)
		if err != nil {
			return nil, fmt.Errorf("invalid sort-by: %w", err)
		}
	}

	return &stateDisplayContext{
		blocName:        blocName,
		outputFormat:    outputFormat,
		displayFlags:    displayFlags,
		propertyFilters: propertyFilters,
		searchOpts:      searchOpts,
		sortOpts:        sortOpts,
	}, nil
}

// applyFiltersAndSorting applies filtering, searching, and sorting to state resources.
func applyFiltersAndSorting(currentState *state.State, ctx *stateDisplayContext) *state.State {
	// Create resource filter with both type and property filters
	var filter *ResourceFilter
	if ctx.propertyFilters != nil {
		filter = NewResourceFilterWithProperties(ctx.displayFlags, ctx.propertyFilters)
	} else {
		filter = NewResourceFilter(ctx.displayFlags)
	}

	// Apply filtering (resource type + property filters)
	filteredResources := filter.FilterResources(currentState.Resources)

	// Apply search (after filtering)
	if ctx.searchOpts != nil {
		filteredResources = SearchResources(filteredResources, ctx.searchOpts)
	}

	// Apply sorting if requested
	if ctx.sortOpts != nil {
		// Convert to sorted slice for display
		sortedList := SortResourceMap(filteredResources, ctx.sortOpts)

		// Create temporary state with sorted resources for display
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

		// Rebuild resources map in sorted order (for JSON/YAML)
		for _, resource := range sortedList {
			sortedState.Resources[resource.ID] = resource
		}

		return sortedState
	}

	// Just update state with filtered resources
	currentState.Resources = filteredResources

	return currentState
}

// renderStateOutput formats and displays state based on output format.
func renderStateOutput(currentState *state.State, outputFormat string) error {
	switch outputFormat {
	case OutputFormatJSON:
		jsonOutput, err := FormatStateJSON(currentState, nil) // Already filtered
		if err != nil {
			return fmt.Errorf("failed to format state as JSON: %w", err)
		}

		_, _ = fmt.Fprintln(os.Stdout, jsonOutput)

		return nil

	case OutputFormatYAML:
		yamlOutput, err := FormatStateYAML(currentState, nil) // Already filtered
		if err != nil {
			return fmt.Errorf("failed to format state as YAML: %w", err)
		}

		_, _ = fmt.Fprint(os.Stdout, yamlOutput)

		return nil

	case OutputFormatTable:
		fallthrough
	default:
		// Create a filter that shows all (since already filtered)
		allFilter := NewResourceFilter(ResourceDisplayFlags{All: true})
		table := FormatStateOutputFiltered(currentState, allFilter)

		err := table.Render()
		if err != nil {
			return fmt.Errorf("failed to render table: %w", err)
		}

		return nil
	}
}

// runStateDisplay is the default handler for 'state' command without subcommands.
func runStateDisplay(cmd *cobra.Command, _args []string) error {
	blocName := viper.GetString("bloc")
	if blocName == "" {
		return ErrBlocRequired
	}

	// Parse and validate display context
	ctx, err := parseDisplayContext(cmd, blocName)
	if err != nil {
		return err
	}

	// Load and validate state
	currentState, err := loadAndValidateState(blocName)
	if err != nil {
		return err
	}

	// Handle empty state
	if currentState == nil {
		return nil
	}

	// Apply filters, search, and sorting
	currentState = applyFiltersAndSorting(currentState, ctx)

	// Render output in requested format
	return renderStateOutput(currentState, ctx.outputFormat)
}

// newStateSyncCmd creates the 'state sync' subcommand.
func newStateSyncCmd() *cobra.Command {
	var (
		dryRun       bool
		force        bool
		output       string
		strategy     string
		displayFlags ResourceDisplayFlags
	)

	cmd := &cobra.Command{
		Use:          "sync",
		Short:        "Reconcile infrastructure state from cloud provider",
		SilenceUsage: true,
		Long:         getStateSyncLongDescription(),
		Example:      getStateSyncExamples(),
		RunE:         runStateSyncCommand,
	}

	addStateSyncFlags(cmd, &displayFlags, &dryRun, &force, &output, &strategy)
	bindStateSyncViperFlags(cmd)

	return cmd
}

// getStateSyncLongDescription returns the long description for the state sync command.
func getStateSyncLongDescription() string {
	return `Reconcile discovers all existing infrastructure resources from the cloud
provider and synchronizes them into the local state file for the specified bloc.

This command:
1. Authenticates with the cloud provider
2. Discovers all resources (networks, instances, volumes, buckets, etc.)
3. Compares discovered resources with current state
4. Merges changes according to the selected strategy
5. Updates the state file with reconciled information

The state file is automatically backed up before any modifications. If the sync
fails, the original state can be restored from the backup.

Merge Strategies:
- add-only: Only add newly discovered resources
- update: Add new resources and update existing ones
- full: Add, update, and remove resources no longer in provider (default)

Resource Display Flags:
By default, all resources are displayed. Use flags to filter which resources to sync and display:

  --all                     Sync/show all resources (default)
  --servers, --instances    Sync/show compute instances
  --volumes                 Sync/show block volumes
  --buckets                 Sync/show object storage buckets
  --load-balancers, --lbs   Sync/show load balancers
  --public-ips              Sync/show public IP addresses
  --keys, --key-pairs       Sync/show SSH key pairs
  --networks, --nets        Sync/show networks/VPCs
  --subnets                 Sync/show subnets
  --security-groups, --sgs  Sync/show security groups
  --routers                 Sync/show routers
  --snapshots               Sync/show volume snapshots`
}

// getStateSyncExamples returns the examples for the state sync command.
func getStateSyncExamples() string {
	return `  # Sync all resources for development environment
  ocfp state sync --bloc dev

  # Preview changes without modifying state
  ocfp state sync --bloc dev --dry-run

  # Sync only servers and volumes
  ocfp state sync --bloc dev --servers --volumes

  # Sync with full merge strategy (includes deletions)
  ocfp state sync --bloc dev --strategy full

  # Force sync without confirmation prompts
  ocfp state sync --bloc dev --force

  # Output sync plan as JSON
  ocfp state sync --bloc dev --dry-run --output json`
}

// addStateSyncFlags adds all command flags to the state sync command.
func addStateSyncFlags(cmd *cobra.Command, displayFlags *ResourceDisplayFlags, dryRun, force *bool, output, strategy *string) {
	cmd.Flags().BoolVar(dryRun, "dry-run", false, "preview sync changes without modifying state")
	cmd.Flags().BoolVar(force, "force", false, "skip confirmation prompts")
	cmd.Flags().StringVar(output, "output", OutputTable, "output format: table|json|yaml")
	cmd.Flags().StringVar(strategy, "strategy", "full", "merge strategy: add-only|update|full")
	AddResourceDisplayFlags(cmd, displayFlags)
}

// bindStateSyncViperFlags binds all state sync flags to viper.
func bindStateSyncViperFlags(cmd *cobra.Command) {
	_ = viper.BindPFlag("state.sync.dry_run", cmd.Flags().Lookup("dry-run"))
	_ = viper.BindPFlag("state.sync.force", cmd.Flags().Lookup("force"))
	_ = viper.BindPFlag("state.sync.output", cmd.Flags().Lookup("output"))
	_ = viper.BindPFlag("state.sync.strategy", cmd.Flags().Lookup("strategy"))
}

// stateSyncParams holds parameters for state sync operation.
type stateSyncParams struct {
	blocName    string
	configFile  string
	dryRun      bool
	output      string
	strategyStr string
	strategy    state.MergeStrategy
}

// validateAndParseStateSyncParams validates and parses state sync parameters.
func validateAndParseStateSyncParams() (*stateSyncParams, error) {
	params := &stateSyncParams{
		blocName:    viper.GetString("bloc"),
		configFile:  viper.GetString("config"),
		dryRun:      viper.GetBool("state.sync.dry_run"),
		output:      viper.GetString("state.sync.output"),
		strategyStr: viper.GetString("state.sync.strategy"),
	}

	if params.blocName == "" {
		return nil, ErrBlocRequired
	}

	// Validate merge strategy
	strategy, err := state.ParseMergeStrategy(params.strategyStr)
	if err != nil {
		return nil, fmt.Errorf("%w %q: must be add-only, update, or full", ErrInvalidMergeStrategy, params.strategyStr)
	}

	params.strategy = strategy

	// Validate output format
	validOutputs := map[string]bool{
		OutputTable: true,
		OutputJSON:  true,
		"yaml":      true,
	}
	if !validOutputs[params.output] {
		return nil, fmt.Errorf("%w %q: must be table, json, or yaml", ErrInvalidOutputFormat, params.output)
	}

	return params, nil
}

// displayStateSyncConfig displays sync configuration to stdout.
func displayStateSyncConfig(params *stateSyncParams) {
	_, _ = fmt.Fprintf(os.Stdout, "Syncing state for bloc: %s\n", params.blocName)
	_, _ = fmt.Fprintf(os.Stdout, "Merge strategy: %s\n", params.strategyStr)

	if params.dryRun {
		_, _ = fmt.Fprintln(os.Stdout, "Mode: DRY RUN (no changes will be made)")
	}

	_, _ = fmt.Fprintln(os.Stdout)
}

// initializeStateSyncReconciler creates and initializes the reconciler.
func initializeStateSyncReconciler(ctx context.Context, params *stateSyncParams) (*state.Reconciler, func(), error) {
	// Load bloc configuration
	cfg, err := config.LoadWithParams(params.configFile, params.blocName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load configuration for bloc %s: %w", params.blocName, err)
	}

	// Determine provider and region
	iaas, region, err := determineProviderFromConfig(cfg)
	if err != nil {
		return nil, nil, err
	}

	logger.Infof("Using provider: %s (region: %s)", iaas, region)

	// Create and initialize provider
	provider, err := cpi.GetProvider(iaas)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get provider: %w", err)
	}

	err = provider.Initialize(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize provider: %w", err)
	}

	cleanup := func() { _ = provider.Cleanup(ctx) }

	// Create state manager
	stateDir, err := state.GetStateDir(params.blocName)
	if err != nil {
		return nil, cleanup, fmt.Errorf("failed to determine state directory: %w", err)
	}

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		return nil, cleanup, fmt.Errorf("failed to create state manager: %w", err)
	}

	// Create reconciler
	reconciler, err := state.NewReconciler(provider, stateManager, params.blocName)
	if err != nil {
		return nil, cleanup, fmt.Errorf("failed to create reconciler: %w", err)
	}

	return reconciler, cleanup, nil
}

// runStateSyncCommand is the execution handler for 'state sync' command.
func runStateSyncCommand(cmd *cobra.Command, _args []string) error {
	params, err := validateAndParseStateSyncParams()
	if err != nil {
		return err
	}

	// Initialize logger for this bloc
	err = initializeStateLogger(params.blocName, "sync")
	if err != nil {
		return err
	}

	defer func() { _ = logger.Sync() }()

	displayStateSyncConfig(params)

	ctx := context.Background()

	reconciler, cleanup, err := initializeStateSyncReconciler(ctx, params)
	if err != nil {
		return err
	}

	defer cleanup()

	// Validate provider credentials
	err = reconciler.ValidateProvider(ctx)
	if err != nil {
		return fmt.Errorf("provider validation failed: %w", err)
	}

	// Run reconciliation
	opts := state.ReconcileOptions{
		DryRun:   params.dryRun,
		Strategy: params.strategy,
		Force:    viper.GetBool("state.sync.force"),
	}

	result, err := reconciler.Reconcile(ctx, opts)
	if err != nil {
		return fmt.Errorf("reconciliation failed: %w", err)
	}

	// Parse display flags and create filter
	displayFlags := ParseDisplayFlagsFromCmd(cmd)
	filter := NewResourceFilter(displayFlags)

	// Display results using formatter with filtering
	formattedOutput, err := FormatSyncOutputFiltered(result, result.DiffSet, params.blocName, params.strategyStr, params.dryRun, params.output, filter)
	if err != nil {
		return fmt.Errorf("failed to format output: %w", err)
	}

	_, _ = fmt.Fprint(os.Stdout, formattedOutput)

	return nil
}

// initializeStateLogger initializes the logger for state operations.
func initializeStateLogger(blocName string, subcommand string) error {
	// Use new path structure: ~/.ocfp (not ~/.ocfp/logs)
	logDir := filepath.Join(os.Getenv("HOME"), ".ocfp")

	err := logger.Initialize(logger.Config{
		Level:      viper.GetString("log_level"),
		Debug:      viper.GetBool("debug"),
		Verbose:    viper.GetBool("verbose"),
		Trace:      viper.GetBool("trace"),
		NoLog:      viper.GetBool("no_log"),
		LogDir:     logDir,
		BlocName:   blocName,
		Command:    "state",
		Subcommand: subcommand, // e.g., "sync" or "export"
		RequestID:  os.Getenv("OCFP_REQUEST_ID"),
		DirectorID: "",
	})
	if err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}

	return nil
}

// determineProviderFromConfig extracts provider and region from configuration.
func determineProviderFromConfig(cfg *config.Config) (string, string, error) {
	iaas := cfg.Provider
	if iaas == "" {
		return "", "", ErrNoProviderConfigured
	}

	region := cfg.Region
	if region == "" {
		region = viper.GetString("region")
	}

	return iaas, region, nil
}
