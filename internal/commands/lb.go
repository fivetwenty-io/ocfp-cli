package commands

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	// DefaultHTTPPort is the default HTTP port for load balancers.
	DefaultHTTPPort = 80

	// DefaultHTTPSPort is the default HTTPS port for load balancers.
	DefaultHTTPSPort = 443

	// HealthCheckPath is the default URL path for health check probes.
	HealthCheckPath = "/health"
	// HealthCheckIntervalSeconds is the interval between health check probes.
	HealthCheckIntervalSeconds = 30
	// HealthCheckTimeoutSeconds is the timeout for each health check probe.
	HealthCheckTimeoutSeconds = 5
	// HealthCheckThreshold is the number of consecutive failures before marking unhealthy.
	HealthCheckThreshold = 3

	// DefaultServiceWeight is the default weight assigned to backend members.
	DefaultServiceWeight = 1
	// DefaultHealthTimeout is the default timeout in seconds for health checks.
	DefaultHealthTimeout = 5

	// ExactArgsTwo is the expected argument count for commands requiring exactly two arguments.
	ExactArgsTwo = 2

	// FilterParts is the expected number of parts when splitting a filter expression.
	FilterParts = 2

	// StatusTableRows is the initial allocation for status table rows.
	StatusTableRows = 3
)

// NewLBCmd creates the load balancer command.
func NewLBCmd() *cobra.Command {
	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "lb",
		Short: "Manage operational load balancers",
		Long: `Manage load balancers for Cloud Foundry deployments.

The lb command provides functionality to create, delete, and manage
load balancers including adding/removing services and checking status.`,
		PersistentPreRunE: func(_cmd *cobra.Command, _args []string) error {
			// Initialize per-command file logger; keep stdout for UX
			blocName := viper.GetString("bloc")
			logDir := config.GetLogDir()

			return logger.Initialize(logger.Config{
				Level:      viper.GetString("log_level"),
				Debug:      viper.GetBool("debug"),
				Verbose:    viper.GetBool("verbose"),
				Trace:      viper.GetBool("trace"),
				NoLog:      viper.GetBool("no_log"),
				LogDir:     logDir,
				BlocName:   blocName,
				Command:    "lb",
				Subcommand: "", // Will be set by subcommand prerun if applicable
				RequestID:  os.Getenv("OCFP_REQUEST_ID"),
			})
		},
	}

	// Add subcommands
	cmd.AddCommand(newLBCreateCmd())
	cmd.AddCommand(newLBDeleteCmd())
	cmd.AddCommand(newLBListCmd())
	cmd.AddCommand(newLBStatusCmd())
	cmd.AddCommand(newLBAddServiceCmd())
	cmd.AddCommand(newLBRemoveServiceCmd())
	cmd.AddCommand(newLBUpdateCmd())

	// Typed LB management commands (STACKIT-oriented but provider-agnostic wrappers)
	cmd.AddCommand(newLBOpsCmd())
	cmd.AddCommand(newLBRoutersCmd())
	cmd.AddCommand(newLBTCPRoutersCmd())
	cmd.AddCommand(newLBCFSSHCmd())
	cmd.AddCommand(newLBSyncCmd())

	return cmd
}

// lbCreateOptions holds the options for the lb create command.
type lbCreateOptions struct {
	name        string
	lbType      string
	algorithm   string
	port        int
	targetPort  int
	protocol    string
	healthCheck bool
	tags        []string
	dryRun      bool
	output      string
}

// newLBCreateCmd creates the lb create subcommand.
func newLBCreateCmd() *cobra.Command {
	opts := &lbCreateOptions{}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new load balancer",
		Long: `Create a new load balancer for Cloud Foundry services.

This command creates a load balancer with the specified configuration
and associates it with the appropriate network resources.`,
		Example: `  # Create a basic HTTP load balancer
  ocfp lb create --name cf-router --type external --port 80

  # Create HTTPS load balancer with health checks
  ocfp lb create --name cf-router --type external --port 443 --protocol https --health-check

  # Create internal load balancer for databases
  ocfp lb create --name postgres-lb --type internal --port 5432 --target-port 5432`,
		RunE: func(_cmd *cobra.Command, _args []string) error {
			return runLBCreate(opts)
		},
	}

	cmd.Flags().StringVar(&opts.name, "name", "", "load balancer name")
	cmd.Flags().StringVar(&opts.lbType, "type", "external", "load balancer type (external|internal)")
	cmd.Flags().StringVar(&opts.algorithm, "algorithm", "round-robin", "load balancing algorithm")
	cmd.Flags().IntVar(&opts.port, "port", DefaultHTTPPort, "load balancer port")
	cmd.Flags().IntVar(&opts.targetPort, "target-port", 0, "backend target port (defaults to lb port)")
	cmd.Flags().StringVar(&opts.protocol, "protocol", "tcp", "protocol (tcp|http|https)")
	cmd.Flags().BoolVar(&opts.healthCheck, "health-check", false, "enable health checks")
	cmd.Flags().StringSliceVar(&opts.tags, "tags", nil, "tags to apply to load balancer")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "preview creation without making changes")
	cmd.Flags().StringVar(&opts.output, "output", OutputTable, "output format: table|json|yaml (for dry-run plan)")

	return cmd
}

// runLBCreate handles the actual load balancer creation logic.
func runLBCreate(opts *lbCreateOptions) error {
	ctx := context.Background()

	if opts.name == "" {
		return ErrLoadBalancerNameRequired
	}

	// Load configuration
	configFile := viper.GetString("config")
	blocName := viper.GetString("bloc")

	cfg, err := config.LoadWithParams(configFile, blocName)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if opts.dryRun {
		return renderLBCreateDryRun(opts)
	}

	return createLoadBalancer(ctx, cfg, opts)
}

// renderLBCreateDryRun renders the dry run output for LB creation.
func renderLBCreateDryRun(opts *lbCreateOptions) error {
	table := &ui.Table{
		Title:   "DRY RUN — LB Create Plan",
		Summary: fmt.Sprintf("Create load balancer '%s'", opts.name),
	}

	table.Sections = append(table.Sections, ui.Section{
		Title:   "Load Balancer",
		Headers: []string{"NAME", "TYPE", "PROTOCOL", "ALGORITHM", "PORT", "TARGET_PORT", "TAGS"},
		Rows: [][]string{{
			opts.name, opts.lbType, opts.protocol, opts.algorithm,
			strconv.Itoa(opts.port), strconv.Itoa(opts.targetPort), strings.Join(opts.tags, ","),
		}},
	})
	if opts.healthCheck {
		table.Sections = append(table.Sections, ui.Section{
			Title:   "Health Check",
			Headers: []string{"PATH", "INTERVAL", "TIMEOUT", "THRESHOLDS"},
			Rows:    [][]string{{HealthCheckPath, strconv.Itoa(HealthCheckIntervalSeconds), strconv.Itoa(HealthCheckTimeoutSeconds), strconv.Itoa(HealthCheckThreshold) + "/" + strconv.Itoa(HealthCheckThreshold)}},
		})
	}

	output := opts.output
	if output == "" {
		output = "table"
	}

	err := ui.Render(table, strings.ToLower(output))
	if err != nil {
		return fmt.Errorf("failed to render table: %w", err)
	}

	return nil
}

// createLoadBalancer creates the actual load balancer.
func createLoadBalancer(ctx context.Context, cfg *config.Config, opts *lbCreateOptions) error {
	log := logger.Get()

	// Get provider
	provider, err := cpi.GetProvider(cfg.Provider)
	if err != nil {
		return fmt.Errorf("failed to get provider: %w", err)
	}

	// Initialize provider
	err = provider.Initialize(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize provider: %w", err)
	}

	defer func() { _ = provider.Cleanup(ctx) }()

	network := provider.NetworkManager()
	if network == nil {
		return ErrProviderDoesNotSupportNetworkMgmt
	}

	log.Infow("Creating load balancer", "name", opts.name, "type", opts.lbType)

	// Create load balancer configuration
	lbConfig := &cpi.LoadBalancer{
		Name:       opts.name,
		Type:       opts.lbType,
		Algorithm:  opts.algorithm,
		Port:       opts.port,
		TargetPort: opts.targetPort,
		Protocol:   opts.protocol,
		Tags:       opts.tags,
	}

	// Create the load balancer
	loadBalancer, err := network.CreateLoadBalancer(ctx, lbConfig)
	if err != nil {
		return fmt.Errorf("failed to create load balancer: %w", err)
	}

	log.Info("Load balancer created successfully",
		"id", loadBalancer.ID,
		"name", loadBalancer.Name,
		"ip", loadBalancer.IPAddress,
		"status", loadBalancer.Status)

	// Configure health check if requested
	if opts.healthCheck {
		err = configureLoadBalancerHealthCheck(ctx, network, loadBalancer.ID)
		if err != nil {
			log.Warnw("Failed to configure health check", "error", err)
		} else {
			log.Info("Health check configured")
		}
	}

	_, err = fmt.Fprintf(os.Stdout, "Load balancer created: %s (%s)\n", loadBalancer.Name, loadBalancer.IPAddress)
	if err != nil {
		return fmt.Errorf("failed to write load balancer info: %w", err)
	}

	return nil
}

// configureLoadBalancerHealthCheck configures health check for a load balancer.
func configureLoadBalancerHealthCheck(ctx context.Context, network cpi.NetworkManager, lbID string) error {
	healthConfig := &cpi.HealthCheck{
		Path:               HealthCheckPath,
		Interval:           HealthCheckIntervalSeconds,
		Timeout:            HealthCheckTimeoutSeconds,
		HealthyThreshold:   HealthCheckThreshold,
		UnhealthyThreshold: HealthCheckThreshold,
	}

	err := network.ConfigureHealthCheck(ctx, lbID, healthConfig)
	if err != nil {
		return fmt.Errorf("failed to configure health check: %w", err)
	}

	return nil
}

// newLBDeleteCmd creates the lb delete subcommand.
func newLBDeleteCmd() *cobra.Command {
	var (
		force  bool
		all    bool
		dryRun bool
		output string
	)

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a load balancer",
		Long:  `Delete a load balancer and its associated resources.`,
		Example: `  # Delete a specific load balancer
  ocfp lb delete cf-router

  # Force delete without confirmation
  ocfp lb delete cf-router --force

  # Delete all load balancers
  ocfp lb delete --all --force`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()

			err := validateLBDeleteArgs(all, args)
			if err != nil {
				return err
			}

			network, err := setupLBProvider(ctx)
			if err != nil {
				return err
			}

			lbsToDelete, err := getLBsToDelete(ctx, network, all, args, log)
			if err != nil {
				return err
			}

			if dryRun {
				return showLBDeletePlan(ctx, network, all, args, output)
			}

			if !force {
				if !confirmLBDeletion(len(lbsToDelete), log) {
					return nil
				}
			}

			return executeLBDeletion(ctx, network, lbsToDelete, log)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation prompt")
	cmd.Flags().BoolVar(&all, "all", false, "delete all load balancers")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview deletions without making changes")
	cmd.Flags().StringVar(&output, "output", OutputTable, "output format: table|json|yaml (for dry-run plan)")

	return cmd
}

func validateLBDeleteArgs(all bool, args []string) error {
	if !all && len(args) == 0 {
		return ErrLoadBalancerNameRequiredOrUseAll
	}

	return nil
}

//nolint:ireturn
func setupLBProvider(ctx context.Context) (cpi.NetworkManager, error) {
	configFile := viper.GetString("config")
	blocName := viper.GetString("bloc")

	cfg, err := config.LoadWithParams(configFile, blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	provider, err := cpi.GetProvider(cfg.Provider)
	if err != nil {
		return nil, fmt.Errorf("failed to get provider: %w", err)
	}

	err = provider.Initialize(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize provider: %w", err)
	}

	network := provider.NetworkManager()
	if network == nil {
		return nil, ErrProviderDoesNotSupportNetworkMgmt
	}

	return network, nil
}

func getLBsToDelete(ctx context.Context, network cpi.NetworkManager, all bool, args []string, log logger.Logger) ([]string, error) {
	if all {
		lbs, err := network.ListLoadBalancers(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to list load balancers: %w", err)
		}

		var lbIDs []string
		for _, lb := range lbs {
			lbIDs = append(lbIDs, lb.ID)
		}

		log.Infow("Found load balancers to delete", "count", len(lbIDs))

		return lbIDs, nil
	}

	return []string{args[0]}, nil
}

func showLBDeletePlan(ctx context.Context, network cpi.NetworkManager, all bool, args []string, output string) error {
	deleteTable := &ui.Table{
		Title:    "DRY RUN — LB Delete Plan",
		Summary:  "",
		Sections: nil,
	}
	rows := make([][]string, 0)

	if all {
		lbs, err := network.ListLoadBalancers(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to list load balancers: %w", err)
		}

		rows = make([][]string, 0, len(lbs))
		for _, lb := range lbs {
			rows = append(rows, []string{lb.Name, lb.ID, lb.IPAddress, strconv.Itoa(lb.Port), lb.Type})
		}
	} else {
		lb, err := network.GetLoadBalancer(ctx, args[0])
		if err == nil {
			rows = append(rows, []string{lb.Name, lb.ID, lb.IPAddress, strconv.Itoa(lb.Port), lb.Type})
		} else {
			rows = append(rows, []string{args[0], "(not found)", "", "", ""})
		}
	}

	deleteTable.Summary = fmt.Sprintf("Delete %d load balancer(s)", len(rows))
	deleteTable.Sections = append(deleteTable.Sections, ui.Section{Title: "Load Balancers", Headers: []string{"NAME", "ID", "IP", "PORT", "TYPE"}, Rows: rows})

	if output == "" {
		output = OutputTable
	}

	return fmt.Errorf("failed to render delete table: %w", ui.Render(deleteTable, strings.ToLower(output)))
}

func confirmLBDeletion(count int, log logger.Logger) bool {
	_, err := fmt.Fprintf(os.Stdout, "This will delete %d load balancer(s). Continue? [y/N]: ", count)
	if err != nil {
		log.Errorw("failed to write confirmation prompt", "error", err)

		return false
	}

	var response string

	_, _ = fmt.Scanln(&response)

	if !strings.HasPrefix(strings.ToLower(response), "y") {
		log.Info("Deletion cancelled by user")

		return false
	}

	return true
}

func executeLBDeletion(ctx context.Context, network cpi.NetworkManager, lbsToDelete []string, log logger.Logger) error {
	for _, lbID := range lbsToDelete {
		log.Infow("Deleting load balancer", "id", lbID)

		err := network.DeleteLoadBalancer(ctx, lbID)
		if err != nil {
			log.Errorw("Failed to delete load balancer", "id", lbID, "error", err)
		} else {
			log.Infow("Load balancer deleted", "id", lbID)
		}
	}

	return nil
}

// newLBListCmd creates the lb list subcommand.
func newLBListCmd() *cobra.Command {
	var (
		output string
		filter string
	)

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List load balancers",
		Long:  `List all load balancers in the current deployment.`,
		Example: `  # List all load balancers
  ocfp lb list

  # List with custom format
  ocfp lb list --format json

  # Filter by type
  ocfp lb list --filter type=external`,
		RunE: func(_cmd *cobra.Command, _args []string) error {
			return runLBList(output, filter)
		},
	}

	cmd.Flags().StringVar(&output, "output", OutputTable, "output format (table|json|yaml)")
	cmd.Flags().StringVar(&filter, "filter", "", "filter results (key=value)")

	return cmd
}

func runLBList(output, filter string) error {
	ctx := context.Background()

	network, err := setupLBProvider(ctx)
	if err != nil {
		return err
	}

	filters := parseLBFilters(filter)

	lbs, err := network.ListLoadBalancers(ctx, filters)
	if err != nil {
		return fmt.Errorf("failed to list load balancers: %w", err)
	}

	if len(lbs) == 0 {
		_, _ = fmt.Fprint(os.Stdout, "No load balancers found\n")

		return nil
	}

	return renderLBListTable(lbs, output)
}

func parseLBFilters(filter string) map[string]string {
	filters := make(map[string]string)

	if filter != "" {
		parts := strings.Split(filter, "=")
		if len(parts) == FilterParts {
			filters[parts[0]] = parts[1]
		}
	}

	return filters
}

func renderLBListTable(lbs []*cpi.LoadBalancer, output string) error {
	table := &ui.Table{
		Title:    "Load Balancers",
		Summary:  "",
		Sections: nil,
	}

	rows := make([][]string, 0, len(lbs))
	for _, lb := range lbs {
		created := lb.CreatedAt.Format(time.RFC3339)
		rows = append(rows, []string{lb.Name, lb.Type, lb.IPAddress, strconv.Itoa(lb.Port), lb.Status, created})
	}

	table.Sections = append(table.Sections, ui.Section{
		Title:   fmt.Sprintf("%d items", len(rows)),
		Headers: []string{"NAME", "TYPE", "IP", "PORT", "STATUS", "CREATED"},
		Rows:    rows,
	})

	if output == "" {
		output = OutputTable
	}

	err := ui.Render(table, strings.ToLower(output))
	if err != nil {
		return fmt.Errorf("failed to render table: %w", err)
	}

	return nil
}

// newLBStatusCmd creates the lb status subcommand.
func newLBStatusCmd() *cobra.Command {
	var output string

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "status <name>",
		Short: "Get load balancer status",
		Long:  `Get detailed status information for a load balancer.`,
		Example: `  # Get status of a load balancer
  ocfp lb status cf-router`,
		Args: cobra.ExactArgs(1),
		RunE: func(_cmd *cobra.Command, args []string) error {
			return runLBStatus(args[0], output)
		},
	}
	cmd.Flags().StringVar(&output, "output", OutputTable, "output format (table|json|yaml)")

	return cmd
}

func runLBStatus(lbName, output string) error {
	ctx := context.Background()

	network, err := setupLBProvider(ctx)
	if err != nil {
		return err
	}

	loadBalancer, err := network.GetLoadBalancer(ctx, lbName)
	if err != nil {
		return fmt.Errorf("failed to get load balancer: %w", err)
	}

	return renderLBStatusTable(ctx, network, loadBalancer, output)
}

func renderLBStatusTable(ctx context.Context, network cpi.NetworkManager, loadBalancer *cpi.LoadBalancer, output string) error {
	lbTable := &ui.Table{
		Title:    "Load Balancer: " + loadBalancer.Name,
		Summary:  "",
		Sections: nil,
	}

	addLBDetailsSection(lbTable, loadBalancer)
	addBackendPoolsSection(ctx, network, lbTable, loadBalancer.ID)
	addHealthStatusSection(ctx, network, lbTable, loadBalancer.ID)

	if output == "" {
		output = OutputTable
	}

	err := ui.Render(lbTable, strings.ToLower(output))
	if err != nil {
		return fmt.Errorf("failed to render table: %w", err)
	}

	return nil
}

func addLBDetailsSection(table *ui.Table, loadBalancer *cpi.LoadBalancer) {
	detailsRow := []string{
		loadBalancer.ID,
		loadBalancer.Type,
		loadBalancer.Status,
		loadBalancer.IPAddress,
		strconv.Itoa(loadBalancer.Port),
		loadBalancer.Algorithm,
		loadBalancer.CreatedAt.Format(time.RFC3339),
	}
	table.Sections = append(table.Sections, ui.Section{
		Title:   "Details",
		Headers: []string{"ID", "TYPE", "STATUS", "IP", "PORT", "ALGORITHM", "CREATED"},
		Rows:    [][]string{detailsRow},
	})
}

func addBackendPoolsSection(ctx context.Context, network cpi.NetworkManager, table *ui.Table, lbID string) {
	pools, err := network.GetBackendPools(ctx, lbID)
	if err == nil && len(pools) > 0 {
		rows := make([][]string, 0, len(pools))
		for _, pool := range pools {
			rows = append(rows, []string{pool.Name, strconv.Itoa(len(pool.Members))})
		}

		table.Sections = append(table.Sections, ui.Section{
			Title:   "Backend Pools",
			Headers: []string{"NAME", "MEMBERS"},
			Rows:    rows,
		})
	}
}

func addHealthStatusSection(ctx context.Context, network cpi.NetworkManager, table *ui.Table, lbID string) {
	health, err := network.GetLoadBalancerHealth(ctx, lbID)
	if err == nil {
		healthRow := []string{
			strconv.Itoa(health.Healthy),
			strconv.Itoa(health.Unhealthy),
			strconv.Itoa(health.Total),
		}
		table.Sections = append(table.Sections, ui.Section{
			Title:   "Health Status",
			Headers: []string{"HEALTHY", "UNHEALTHY", "TOTAL"},
			Rows:    [][]string{healthRow},
		})
	}
}

// newLBAddServiceCmd creates the lb add-service subcommand.
func newLBAddServiceCmd() *cobra.Command {
	var (
		port       int
		targetPort int
		weight     int
		dryRun     bool
		output     string
	)

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "add-service <lb-name> <service-ip|reserved:key[:index]>",
		Short: "Add a service to load balancer",
		Long: `Add a backend service to a load balancer pool.

You can reference reserved IPs computed by bootstrap (STACKIT) using the form:
  reserved:<key>[:index]
Examples:
  reserved:vault_ip        # uses ocfp-0 by default
  reserved:doomsday_ip:1   # uses ocfp-1
`,
		Example: `  # Add a service to load balancer
  ocfp lb add-service cf-router 10.0.1.10

  # Add with custom port mapping
  ocfp lb add-service cf-router 10.0.1.10 --port 8080 --target-port 80

  # Add with weight for weighted load balancing
  ocfp lb add-service cf-router 10.0.1.10 --weight 2

  # Use reserved IPs (STACKIT)
  ocfp lb add-service ops-https reserved:vault_ip
  ocfp lb add-service doomsday-mgmt reserved:doomsday_ip:1`,
		Args: cobra.ExactArgs(ExactArgsTwo),
		RunE: func(_cmd *cobra.Command, args []string) error {
			return runLBAddServiceCmd(args[0], args[1], port, targetPort, weight, dryRun, output)
		},
	}

	cmd.Flags().IntVar(&port, "port", DefaultHTTPPort, "service port")
	cmd.Flags().IntVar(&targetPort, "target-port", 0, "target port (defaults to service port)")
	cmd.Flags().IntVar(&weight, "weight", DefaultServiceWeight, "service weight for weighted load balancing")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview member addition without making changes")
	cmd.Flags().StringVar(&output, "output", OutputTable, "output format: table|json|yaml (for dry-run plan)")

	return cmd
}

// runLBAddServiceCmd executes the lb add-service command logic.
func runLBAddServiceCmd(lbName, serviceIP string, port, targetPort, weight int, dryRun bool, output string) error {
	ctx := context.Background()
	log := logger.Get()

	configFile := viper.GetString("config")
	blocName := viper.GetString("bloc")

	cfg, err := config.LoadWithParams(configFile, blocName)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	provider, err := cpi.GetProvider(cfg.Provider)
	if err != nil {
		return fmt.Errorf("failed to get provider: %w", err)
	}

	err = provider.Initialize(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize provider: %w", err)
	}

	defer func() { _ = provider.Cleanup(ctx) }()

	network := provider.NetworkManager()
	if network == nil {
		return ErrProviderDoesNotSupportNetworkMgmt
	}

	loadBalancer, err := network.GetLoadBalancer(ctx, lbName)
	if err != nil {
		return fmt.Errorf("failed to get load balancer: %w", err)
	}

	resolvedIP, err := resolveServiceIP(serviceIP)
	if err != nil {
		return err
	}

	member := createBackendMember(resolvedIP, port, targetPort, weight)

	log.Info("Adding service to load balancer",
		"lb", lbName,
		"service", resolvedIP,
		"port", port)

	if dryRun {
		return handleLBAddServiceDryRun(ctx, network, loadBalancer, lbName, member, output)
	}

	return addServiceToLoadBalancer(ctx, network, loadBalancer, member, resolvedIP, lbName)
}

// resolveServiceIP resolves token references or returns the IP as-is.
func resolveServiceIP(serviceIP string) (string, error) {
	if !isToken(serviceIP) {
		return serviceIP, nil
	}

	blocName := viper.GetString("bloc")

	resolvedIP, err := ResolveTargetIP(blocName, serviceIP)
	if err != nil {
		return "", fmt.Errorf("failed to resolve %s: %w", serviceIP, err)
	}

	return resolvedIP, nil
}

// createBackendMember creates a backend member configuration.
func createBackendMember(ipAddress string, port, targetPort, weight int) *cpi.BackendMember {
	return &cpi.BackendMember{
		ID:         "",
		IPAddress:  ipAddress,
		Port:       port,
		TargetPort: targetPort,
		Weight:     weight,
		Status:     "",
	}
}

// handleLBAddServiceDryRun handles the dry-run logic and output.
func handleLBAddServiceDryRun(ctx context.Context, network cpi.NetworkManager, loadBalancer *cpi.LoadBalancer, lbName string, member *cpi.BackendMember, output string) error {
	addServiceTable := &ui.Table{
		Title:    "DRY RUN — LB Add Service Plan",
		Summary:  "",
		Sections: nil,
	}
	addServiceTable.Summary = "Add backend to " + lbName

	pools, err := network.GetBackendPools(ctx, loadBalancer.ID)
	if err == nil && len(pools) > 0 {
		rows := make([][]string, 0, len(pools[0].Members))
		for _, m := range pools[0].Members {
			rows = append(rows, []string{m.IPAddress, strconv.Itoa(m.Port)})
		}

		addServiceTable.Sections = append(addServiceTable.Sections, ui.Section{
			Title:   "Current Members",
			Headers: []string{"IP", "PORT"},
			Rows:    rows,
		})
	}

	addServiceTable.Sections = append(addServiceTable.Sections, ui.Section{
		Title:   "Add",
		Headers: []string{"IP", "PORT", "TARGET_PORT", "WEIGHT"},
		Rows: [][]string{{
			member.IPAddress,
			strconv.Itoa(member.Port),
			strconv.Itoa(member.TargetPort),
			strconv.Itoa(member.Weight),
		}},
	})

	if output == "" {
		output = OutputTable
	}

	err = ui.Render(addServiceTable, strings.ToLower(output))
	if err != nil {
		return fmt.Errorf("failed to render add service plan: %w", err)
	}

	return nil
}

// addServiceToLoadBalancer adds the service to the load balancer and reports success.
func addServiceToLoadBalancer(ctx context.Context, network cpi.NetworkManager, loadBalancer *cpi.LoadBalancer, member *cpi.BackendMember, serviceIP, lbName string) error {
	err := network.AddBackendMember(ctx, loadBalancer.ID, member)
	if err != nil {
		return fmt.Errorf("failed to add backend member: %w", err)
	}

	logger.Get().Info("Service added to load balancer successfully")

	_, err = fmt.Fprintf(os.Stdout, "Added %s to load balancer %s\n", serviceIP, lbName)
	if err != nil {
		return fmt.Errorf("failed to write service add confirmation: %w", err)
	}

	return nil
}

// ResolveReservedIP resolves a reserved IP output from state based on
// the token form reserved:<key>[:index]. Default index is 0. The subnet
// recorded at that index in bootstrap's subnet_<name>_index outputs is
// consulted first — it holds under any subnet naming — with the legacy
// "reserved_<bloc>-ocfp-<index>_<key>" shape as the fallback for states
// predating the index outputs.
func ResolveReservedIP(blocName string, token string) (string, error) {
	// token format: reserved:key or reserved:key:index
	parts := strings.Split(strings.TrimPrefix(token, "reserved:"), ":")
	if len(parts) == 0 || parts[0] == "" {
		return "", ErrInvalidReservedFormat
	}

	key := parts[0]

	index := "0"
	if len(parts) > 1 && parts[1] != "" {
		index = parts[1]
	}

	stateManager, err := initStateManager(blocName)
	if err != nil {
		return "", err
	}

	if st, loadErr := stateManager.Load(blocName); loadErr == nil && st != nil {
		if idx, convErr := strconv.Atoi(index); convErr == nil {
			if name, ok := workloadSubnetNameForIndex(st.Outputs, idx); ok {
				if value, isString := st.Outputs["reserved_"+name+"_"+key].(string); isString && value != "" {
					return value, nil
				}
			}
		}
	}

	// Legacy output key: reserved_<bloc>-ocfp-<index>_<key>
	stateKey := fmt.Sprintf("reserved_%s-ocfp-%s_%s", blocName, index, key)

	val, err := stateManager.GetOutput(stateKey)
	if err != nil {
		return "", ErrOutputNotFound(stateKey)
	}

	ipAddress, ok := val.(string)
	if !ok || ipAddress == "" {
		return "", ErrOutputEmptyOrNotString(stateKey)
	}

	return ipAddress, nil
}

// isToken returns true if arg begins with a supported token prefix.
func isToken(s string) bool {
	return strings.HasPrefix(s, "reserved:") || strings.HasPrefix(s, "public-ip:")
}

// ResolveTargetIP resolves tokens of the form:
//   - reserved:<key>[:index]
//   - public-ip:<job>[:index]
func ResolveTargetIP(blocName string, token string) (string, error) {
	if strings.HasPrefix(token, "reserved:") {
		return ResolveReservedIP(blocName, token)
	}

	if strings.HasPrefix(token, "public-ip:") {
		rest := strings.TrimPrefix(token, "public-ip:")

		parts := strings.Split(rest, ":")
		if len(parts) == 0 || parts[0] == "" {
			return "", ErrInvalidPublicIPToken
		}

		job := parts[0]

		index := ""
		if len(parts) > 1 {
			index = parts[1]
		}

		ipAddress, err := findPublicIPByJob(blocName, job, index)
		if err != nil {
			return "", err
		}

		if ipAddress == "" {
			return "", ErrNoMatchingPublicIPForJob(job, index)
		}

		return ipAddress, nil
	}

	return token, nil
}

// findPublicIPByJob reads state for public_ip resources with matching job and optional index.
func findPublicIPByJob(blocName, job, index string) (string, error) {
	stateManager, err := initStateManager(blocName)
	if err != nil {
		return "", err
	}

	res, err := stateManager.ListResources("public_ip")
	if err != nil {
		return "", fmt.Errorf("failed to list public IP resources: %w", err)
	}

	for _, resource := range res {
		if !matchesJobAndIndex(*resource, job, index) {
			continue
		}

		if addr, ok := resource.Properties["address"].(string); ok && addr != "" {
			return addr, nil
		}
	}

	return "", nil
}

func initStateManager(blocName string) (*state.Manager, error) {
	// Check for environment variable override first
	stateDir := os.Getenv("OCFP_STATE_DIR")
	if stateDir == "" {
		// Use standard state directory for this bloc
		var err error

		stateDir, err = state.GetStateDir(blocName)
		if err != nil {
			return nil, fmt.Errorf("failed to determine state directory: %w", err)
		}
	}

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		return nil, fmt.Errorf("state manager: %w", err)
	}

	_, err = stateManager.Load(blocName)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}

	return stateManager, nil
}

func matchesJobAndIndex(resource state.Resource, targetJob, targetIndex string) bool {
	resourceJob := extractResourceValue(resource, "job")
	if resourceJob != targetJob {
		return false
	}

	if targetIndex != "" {
		resourceIndex := extractResourceValue(resource, "index")
		if resourceIndex != targetIndex {
			return false
		}
	}

	return true
}

func extractResourceValue(resource state.Resource, key string) string {
	if resource.Tags != nil && resource.Tags[key] != "" {
		return resource.Tags[key]
	}

	if v, ok := resource.Properties[key].(string); ok {
		return v
	}

	return ""
}

// confirmServiceRemoval prompts the user to confirm service removal if force is false.
// Returns true if confirmed, false if cancelled, and error on I/O failure.
func confirmServiceRemoval(force bool, serviceIP, lbName string, log logger.Logger) (bool, error) {
	if force {
		return true, nil
	}

	_, err := fmt.Fprintf(os.Stdout, "Remove %s from load balancer %s? [y/N]: ", serviceIP, lbName)
	if err != nil {
		return false, fmt.Errorf("failed to write removal prompt: %w", err)
	}

	var response string

	_, _ = fmt.Scanln(&response)
	if !strings.HasPrefix(strings.ToLower(response), "y") {
		log.Info("Removal cancelled by user")

		return false, nil
	}

	return true, nil
}

// showRemoveServicePlan displays the dry-run plan for service removal.
func showRemoveServicePlan(ctx context.Context, network cpi.NetworkManager, loadBalancer *cpi.LoadBalancer, serviceIP, output string) error {
	removeServiceTable := &ui.Table{
		Title:    "DRY RUN — LB Remove Service Plan",
		Summary:  "",
		Sections: nil,
	}
	removeServiceTable.Summary = "Remove backend from " + loadBalancer.Name

	pools, err := network.GetBackendPools(ctx, loadBalancer.ID)
	if err == nil && len(pools) > 0 {
		rows := make([][]string, 0, len(pools[0].Members))
		for _, m := range pools[0].Members {
			rows = append(rows, []string{m.IPAddress, strconv.Itoa(m.Port)})
		}

		removeServiceTable.Sections = append(removeServiceTable.Sections, ui.Section{
			Title:   "Current Members",
			Headers: []string{"IP", "PORT"},
			Rows:    rows,
		})
	}

	removeServiceTable.Sections = append(removeServiceTable.Sections, ui.Section{
		Title:   "Remove",
		Headers: []string{"IP"},
		Rows:    [][]string{{serviceIP}},
	})

	if output == "" {
		output = OutputTable
	}

	err = ui.Render(removeServiceTable, strings.ToLower(output))
	if err != nil {
		return fmt.Errorf("failed to render remove service plan: %w", err)
	}

	return nil
}

// executeServiceRemoval removes the service from the load balancer and reports success.
func executeServiceRemoval(ctx context.Context, network cpi.NetworkManager, loadBalancer *cpi.LoadBalancer, serviceIP string, log logger.Logger) error {
	err := network.RemoveBackendMember(ctx, loadBalancer.ID, serviceIP)
	if err != nil {
		return fmt.Errorf("failed to remove backend member: %w", err)
	}

	log.Info("Service removed from load balancer successfully")

	_, err = fmt.Fprintf(os.Stdout, "Removed %s from load balancer %s\n", serviceIP, loadBalancer.Name)
	if err != nil {
		return fmt.Errorf("failed to write removal confirmation: %w", err)
	}

	return nil
}

// newLBRemoveServiceCmd creates the lb remove-service subcommand.
func newLBRemoveServiceCmd() *cobra.Command {
	var (
		force  bool
		dryRun bool
		output string
	)

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "remove-service <lb-name> <service-ip>",
		Short: "Remove a service from load balancer",
		Long:  `Remove a backend service from a load balancer pool.`,
		Example: `  # Remove a service from load balancer
  ocfp lb remove-service cf-router 10.0.1.10

  # Force removal without confirmation
  ocfp lb remove-service cf-router 10.0.1.10 --force`,
		Args: cobra.ExactArgs(ExactArgsTwo),
		RunE: func(_cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()

			lbName := args[0]
			serviceIP := args[1]

			// Confirm removal if not forced
			confirmed, err := confirmServiceRemoval(force, serviceIP, lbName, log)
			if err != nil {
				return err
			}

			if !confirmed {
				return nil
			}

			// Setup provider and network manager
			network, err := setupLBProvider(ctx)
			if err != nil {
				return err
			}

			log.Infow("Removing service from load balancer", "lb", lbName, "service", serviceIP)

			// Get load balancer
			loadBalancer, err := network.GetLoadBalancer(ctx, lbName)
			if err != nil {
				return fmt.Errorf("failed to get load balancer: %w", err)
			}

			// Handle dry run
			if dryRun {
				return showRemoveServicePlan(ctx, network, loadBalancer, serviceIP, output)
			}

			// Execute service removal
			return executeServiceRemoval(ctx, network, loadBalancer, serviceIP, log)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation prompt")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview member removal without making changes")
	cmd.Flags().StringVar(&output, "output", OutputTable, "output format: table|json|yaml (for dry-run plan)")

	return cmd
}

// newLBUpdateCmd creates the lb update subcommand.
func newLBUpdateCmd() *cobra.Command {
	var (
		algorithm   string
		healthCheck string
		timeout     int
		dryRun      bool
		output      string
	)

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Update load balancer configuration",
		Long:  `Update configuration settings for an existing load balancer.`,
		Example: `  # Update load balancing algorithm
  ocfp lb update cf-router --algorithm least-connections

  # Update health check settings
  ocfp lb update cf-router --health-check /health --timeout 10`,
		Args: cobra.ExactArgs(1),
		RunE: func(_cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()
			lbName := args[0]

			// Validate before contacting the provider so bad input fails
			// fast and costs nothing.
			err := validateHealthCheckTimeout(timeout)
			if err != nil {
				return err
			}

			network, err := setupLBProvider(ctx)
			if err != nil {
				return err
			}

			log.Infow("Updating load balancer", "name", lbName)

			loadBalancer, err := network.GetLoadBalancer(ctx, lbName)
			if err != nil {
				return fmt.Errorf("failed to get load balancer: %w", err)
			}

			if dryRun {
				return showLBUpdatePlan(loadBalancer, algorithm, healthCheck, timeout, output)
			}

			return applyLBUpdates(ctx, network, loadBalancer, algorithm, healthCheck, timeout, log)
		},
	}

	cmd.Flags().StringVar(&algorithm, "algorithm", "", "load balancing algorithm (round-robin|least-connections|ip-hash)")
	cmd.Flags().StringVar(&healthCheck, "health-check", "", "health check path")
	cmd.Flags().IntVar(&timeout, "timeout", DefaultHealthTimeout, "health check timeout in seconds")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview updates without making changes")
	cmd.Flags().StringVar(&output, "output", "table", "output format: table|json|yaml (for dry-run plan)")

	return cmd
}

// validateHealthCheckTimeout rejects health check timeouts that are not a
// positive number of seconds representable as an int32.
//
// The upper bound exists because the provider load balancer managers narrow
// this value to an int32 (cpi/gcp/loadbalancer.go sets TimeoutSec from it),
// where anything above math.MaxInt32 wraps silently — 1<<32 seconds becomes a
// zero timeout rather than an error. Providers apply their own, far tighter
// limits and report those themselves; this bound only guarantees the value
// reaches them intact.
func validateHealthCheckTimeout(timeout int) error {
	if timeout < 1 || timeout > math.MaxInt32 {
		return fmt.Errorf("%w %d: must be between 1 and %d seconds",
			ErrInvalidHealthCheckTimeout, timeout, math.MaxInt32)
	}

	return nil
}

func showLBUpdatePlan(loadBalancer *cpi.LoadBalancer, algorithm, healthCheck string, timeout int, output string) error {
	table := &ui.Table{
		Title:    "DRY RUN — LB Update Plan",
		Summary:  "",
		Sections: nil,
	}
	table.Summary = "Update load balancer " + loadBalancer.Name

	rows := make([][]string, 0, StatusTableRows)
	if algorithm != "" && algorithm != loadBalancer.Algorithm {
		rows = append(rows, []string{"algorithm", loadBalancer.Algorithm, algorithm})
	}

	if healthCheck != "" {
		rows = append(rows, []string{"health_check.path", "/", healthCheck})
		rows = append(rows, []string{"health_check.timeout", strconv.Itoa(DefaultHealthTimeout), strconv.Itoa(timeout)})
	}

	if len(rows) == 0 {
		rows = append(rows, []string{"no-op", "", ""})
	}

	table.Sections = append(table.Sections, ui.Section{Title: "Changes", Headers: []string{"FIELD", "CURRENT", "NEW"}, Rows: rows})

	if output == "" {
		output = OutputTable
	}

	return fmt.Errorf("failed to render table: %w", ui.Render(table, strings.ToLower(output)))
}

func applyLBUpdates(ctx context.Context, network cpi.NetworkManager, loadBalancer *cpi.LoadBalancer, algorithm, healthCheck string, timeout int, log logger.Logger) error {
	updateNeeded := false

	if algorithm != "" && algorithm != loadBalancer.Algorithm {
		loadBalancer.Algorithm = algorithm
		updateNeeded = true
	}

	if updateNeeded {
		err := network.UpdateLoadBalancer(ctx, loadBalancer)
		if err != nil {
			return fmt.Errorf("failed to update load balancer: %w", err)
		}

		log.Info("Load balancer configuration updated")
	}

	if healthCheck != "" {
		healthConfig := &cpi.HealthCheck{
			Protocol:           "http",
			Port:               0,
			Path:               healthCheck,
			Interval:           HealthCheckIntervalSeconds,
			Timeout:            timeout,
			HealthyThreshold:   HealthCheckThreshold,
			UnhealthyThreshold: HealthCheckThreshold,
		}

		err := network.ConfigureHealthCheck(ctx, loadBalancer.ID, healthConfig)
		if err != nil {
			return fmt.Errorf("failed to update health check: %w", err)
		}

		log.Info("Health check configuration updated")
	}

	_, err := fmt.Fprintf(os.Stdout, "Load balancer %s updated successfully\n", loadBalancer.Name)
	if err != nil {
		return fmt.Errorf("failed to write update confirmation: %w", err)
	}

	return nil
}
