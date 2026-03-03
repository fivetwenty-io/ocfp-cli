package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	// Command arguments.
	scaleTwoArgs = 2

	// HTTPPort is the standard HTTP port used for scaling health checks.
	HTTPPort = 80
)

var (
	// ErrNoBackendPoolsFound indicates no backend pools were found for the load balancer.
	ErrNoBackendPoolsFound = errors.New("no backend pools found")
)

// scaleOptions holds the scale command options.
type scaleOptions struct {
	dryRun bool
	force  bool
	wait   bool
	output string
}

// NewScaleCmd creates the scale command.
func NewScaleCmd() *cobra.Command {
	opts := &scaleOptions{
		dryRun: false,
		force:  false,
		wait:   false,
		output: "",
	}

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "scale <resource> <count>",
		Short: "Scale OCFP resources",
		Long: `Scale resources like routers, instances, and other Cloud Foundry components.

The scale command allows you to increase or decrease the number of instances
for various resources in your Cloud Foundry deployment. It supports scaling
routers, cells, and other component instances.`,
		Example: `  # Scale routers to 3 instances
  ocfp scale routers 3

  # Scale diego cells to 5 instances
  ocfp scale cells 5

  # Scale with dry run to preview changes
  ocfp scale routers 3 --dry-run

  # Scale and wait for completion
  ocfp scale cells 10 --wait`,
		Args: cobra.ExactArgs(scaleTwoArgs),
		RunE: func(_cmd *cobra.Command, args []string) error {
			return runScaleCommand(args, opts)
		},
	}

	addScaleFlags(cmd, opts)

	return cmd
}

// addScaleFlags adds all scale command flags.
func addScaleFlags(cmd *cobra.Command, opts *scaleOptions) {
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "preview scaling without making changes")
	cmd.Flags().BoolVar(&opts.force, "force", false, "skip confirmation prompt")
	cmd.Flags().BoolVar(&opts.wait, "wait", false, "wait for scaling to complete")
	cmd.Flags().StringVar(&opts.output, "output", OutputTable, "output format: table|json|yaml (for dry-run plan)")

	// Bind flags to viper
	_ = viper.BindPFlag("scale.dry_run", cmd.Flags().Lookup("dry-run"))
	_ = viper.BindPFlag("scale.force", cmd.Flags().Lookup("force"))
	_ = viper.BindPFlag("scale.wait", cmd.Flags().Lookup("wait"))
	_ = viper.BindPFlag("scale.output", cmd.Flags().Lookup("output"))
}

// runScaleCommand executes the scale command logic.
func runScaleCommand(args []string, opts *scaleOptions) error {
	ctx := context.Background()

	resource, count, err := parseScaleArgs(args)
	if err != nil {
		return err
	}

	log, err := initializeScaleLogger()
	if err != nil {
		return err
	}

	defer func() { _ = logger.Sync() }()

	cfg, err := loadScaleConfig()
	if err != nil {
		return err
	}

	log.Infow("Scaling resource", "resource", resource, "count", count, "dry-run", opts.dryRun)

	confirmScaling(resource, count, opts.force, opts.dryRun, log)

	provider, err := initializeProvider(ctx, cfg)
	if err != nil {
		return err
	}

	defer func() { _ = provider.Cleanup(ctx) }()

	return executeScaling(ctx, resource, count, opts, provider, cfg, log)
}

// parseScaleArgs parses and validates scale command arguments.
func parseScaleArgs(args []string) (string, int, error) {
	resource := args[0]
	countStr := args[1]

	count, err := strconv.Atoi(countStr)
	if err != nil {
		return "", 0, ErrInvalidCount(countStr)
	}

	if count < 0 {
		return "", 0, ErrCountMustBeNonNegative
	}

	return resource, count, nil
}

// initializeScaleLogger initializes logging for the scale command.
func initializeScaleLogger() (logger.Logger, error) {
	blocName := viper.GetString("bloc")
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
		Command:    "scale",
		Subcommand: "", // Scale has no subcommands
		RequestID:  os.Getenv("OCFP_REQUEST_ID"),
		DirectorID: "",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize logger: %w", err)
	}

	return logger.Get(), nil
}

// loadScaleConfig loads configuration for scaling operations.
func loadScaleConfig() (*config.Config, error) {
	configFile := viper.GetString("config")
	blocName := viper.GetString("bloc")

	cfg, err := config.LoadWithParams(configFile, blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	return cfg, nil
}

// confirmScaling handles user confirmation for scaling operations.
func confirmScaling(resource string, count int, force, dryRun bool, log logger.Logger) {
	if force || dryRun {
		return
	}

	_, err := fmt.Fprintf(os.Stdout, "Scale %s to %d instances? [y/N]: ", resource, count)
	if err != nil {
		logger.Get().Error(fmt.Sprintf("Failed to write prompt: %v", err))

		return
	}

	var response string

	_, _ = fmt.Scanln(&response)

	if !strings.HasPrefix(strings.ToLower(response), "y") {
		log.Info("Scaling cancelled by user")

		return
	}
}

// executeScaling performs the actual scaling operation.
func executeScaling(ctx context.Context, resource string, count int, opts *scaleOptions, provider cpi.Provider, cfg *config.Config, log logger.Logger) error {
	if opts.dryRun {
		return handleDryRunScaling(ctx, resource, count, provider, cfg)
	}

	err := performResourceScaling(ctx, resource, count, opts, provider, cfg)
	if err != nil {
		return fmt.Errorf("scaling failed: %w", err)
	}

	log.Info("Scaling completed successfully")

	return nil
}

// handleDryRunScaling handles dry run scaling operations.
func handleDryRunScaling(ctx context.Context, resource string, count int, provider cpi.Provider, cfg *config.Config) error {
	err := renderScalePlan(ctx, resource, count, viper.GetString("scale.output"), provider, cfg)
	if err != nil {
		return fmt.Errorf("failed to render scale plan: %w", err)
	}

	return nil
}

// performResourceScaling performs scaling based on resource type.
func performResourceScaling(ctx context.Context, resource string, count int, opts *scaleOptions, provider cpi.Provider, cfg *config.Config) error {
	switch strings.ToLower(resource) {
	case ResourceRouters, ResourceRouter:
		return scaleRouters(ctx, provider, cfg, count, opts.dryRun, opts.wait)
	case "cells", "cell", "diego-cells":
		return scaleCells(ctx, provider, cfg, count, opts.dryRun, opts.wait)
	case ResourceInstances, ResourceInstance:
		return scaleInstances(count, opts.dryRun)
	case "load-balancer", "lb":
		return scaleLoadBalancer(ctx, provider, count, opts.dryRun, opts.wait)
	case "database", "db", "postgres":
		return scaleDatabase(count, opts.dryRun)
	default:
		return ErrUnknownResourceType(resource)
	}
}

// scaleRouters scales router instances.
func scaleRouters(ctx context.Context, provider cpi.Provider, cfg *config.Config, count int, dryRun bool, wait bool) error {
	log := logger.Get()
	log.Infow("Scaling routers", "target_count", count)

	if dryRun {
		return nil
	}

	compute := provider.Compute()
	if compute == nil {
		return ErrProviderDoesNotSupportComputeMgmt
	}

	instances, err := compute.ListInstances(ctx, map[string]string{"role": "router"})
	if err != nil {
		return fmt.Errorf("failed to list router instances: %w", err)
	}

	currentCount := len(instances)
	log.Infow("Current router count", "count", currentCount)

	if currentCount == count {
		log.Info("Already at desired count")

		return nil
	}

	err = scaleRouterInstances(ctx, compute, cfg, instances, currentCount, count, log)
	if err != nil {
		return err
	}

	if wait {
		log.Info("Waiting for scaling to complete...")
	}

	return nil
}

func scaleRouterInstances(ctx context.Context, compute cpi.ComputeManager, cfg *config.Config, instances []*cpi.Instance, currentCount, targetCount int, log logger.Logger) error {
	if currentCount < targetCount {
		return scaleUpRouters(ctx, compute, cfg, currentCount, targetCount, log)
	}

	return scaleDownRouters(ctx, compute, instances, currentCount, targetCount, log)
}

func scaleUpRouters(ctx context.Context, compute cpi.ComputeManager, cfg *config.Config, currentCount, targetCount int, log logger.Logger) error {
	toCreate := targetCount - currentCount
	log.Infow("Scaling up routers", "creating", toCreate)

	for i := range toCreate {
		req := createRouterInstanceRequest(cfg, currentCount+i+1)

		instance, err := compute.CreateInstance(ctx, req)
		if err != nil {
			log.Errorw("Failed to create router instance", "error", err)

			continue
		}

		log.Infow("Created router instance", "id", instance.ID, "name", instance.Name)
	}

	return nil
}

func scaleDownRouters(ctx context.Context, compute cpi.ComputeManager, instances []*cpi.Instance, currentCount, targetCount int, log logger.Logger) error {
	toRemove := currentCount - targetCount
	log.Infow("Scaling down routers", "removing", toRemove)

	for i := 0; i < toRemove && i < len(instances); i++ {
		instance := instances[len(instances)-1-i]
		log.Infow("Removing router instance", "id", instance.ID, "name", instance.Name)

		err := compute.DeleteInstance(ctx, instance.ID)
		if err != nil {
			log.Errorw("Failed to delete router instance", "id", instance.ID, "error", err)

			continue
		}
	}

	return nil
}

func createRouterInstanceRequest(cfg *config.Config, instanceNum int) *cpi.InstanceRequest {
	return &cpi.InstanceRequest{
		Name:             fmt.Sprintf("%s-router-%d", cfg.Name, instanceNum),
		Flavor:           cfg.Routers.Flavor,
		Image:            cfg.Routers.Image,
		NetworkID:        cfg.Network.ID,
		SubnetID:         cfg.Network.SubnetID,
		SecurityGroups:   []string{"ocf-sg"},
		KeyPair:          cfg.Bastion.SSHKeyName,
		AvailabilityZone: "",
		Tags: map[string]string{
			"role":       "router",
			"deployment": cfg.Name,
		},
	}
}

// scaleCells scales Diego cell instances.
func scaleCells(ctx context.Context, provider cpi.Provider, cfg *config.Config, count int, dryRun bool, wait bool) error {
	log := logger.Get()
	log.Infow("Scaling Diego cells", "target_count", count)

	if dryRun {
		return nil
	}

	compute := provider.Compute()
	if compute == nil {
		return ErrProviderDoesNotSupportComputeMgmt
	}

	instances, err := compute.ListInstances(ctx, map[string]string{"role": "diego-cell"})
	if err != nil {
		return fmt.Errorf("failed to list Diego cell instances: %w", err)
	}

	currentCount := len(instances)
	log.Infow("Current Diego cell count", "count", currentCount)

	if currentCount == count {
		log.Info("Already at desired count")

		return nil
	}

	err = scaleCellInstances(ctx, compute, cfg, instances, currentCount, count, log)
	if err != nil {
		return err
	}

	if wait {
		log.Info("Waiting for scaling to complete...")
	}

	return nil
}

func scaleCellInstances(ctx context.Context, compute cpi.ComputeManager, cfg *config.Config, instances []*cpi.Instance, currentCount, targetCount int, log logger.Logger) error {
	if currentCount < targetCount {
		return scaleUpCells(ctx, compute, cfg, currentCount, targetCount, log)
	}

	return scaleDownCells(ctx, compute, instances, currentCount, targetCount, log)
}

func scaleUpCells(ctx context.Context, compute cpi.ComputeManager, cfg *config.Config, currentCount, targetCount int, log logger.Logger) error {
	toCreate := targetCount - currentCount
	log.Infow("Scaling up Diego cells", "creating", toCreate)

	for i := range toCreate {
		req := createCellInstanceRequest(cfg, currentCount+i+1)

		instance, err := compute.CreateInstance(ctx, req)
		if err != nil {
			log.Errorw("Failed to create Diego cell instance", "error", err)

			continue
		}

		log.Infow("Created Diego cell instance", "id", instance.ID, "name", instance.Name)
	}

	return nil
}

func scaleDownCells(ctx context.Context, compute cpi.ComputeManager, instances []*cpi.Instance, currentCount, targetCount int, log logger.Logger) error {
	toRemove := currentCount - targetCount
	log.Infow("Scaling down Diego cells", "removing", toRemove)

	for i := 0; i < toRemove && i < len(instances); i++ {
		instance := instances[len(instances)-1-i]
		log.Infow("Removing Diego cell instance", "id", instance.ID, "name", instance.Name)
		log.Infow("Draining apps from cell", "id", instance.ID)

		err := compute.DeleteInstance(ctx, instance.ID)
		if err != nil {
			log.Errorw("Failed to delete Diego cell instance", "id", instance.ID, "error", err)

			continue
		}
	}

	return nil
}

func createCellInstanceRequest(cfg *config.Config, instanceNum int) *cpi.InstanceRequest {
	return &cpi.InstanceRequest{
		Name:             fmt.Sprintf("%s-diego-cell-%d", cfg.Name, instanceNum),
		Flavor:           cfg.Cells.Flavor,
		Image:            cfg.Cells.Image,
		NetworkID:        cfg.Network.ID,
		SubnetID:         cfg.Network.SubnetID,
		SecurityGroups:   []string{"ocf-sg"},
		KeyPair:          cfg.Bastion.SSHKeyName,
		AvailabilityZone: "",
		Tags: map[string]string{
			"role":       "diego-cell",
			"deployment": cfg.Name,
		},
	}
}

// scaleInstances scales generic instances.
func scaleInstances(count int, dryRun bool) error {
	log := logger.Get()
	log.Infow("Scaling instances", "target_count", count)

	if dryRun {
		log.Infow("[DRY RUN] Would scale instances", "count", count)

		return nil
	}

	// Pending: implement generic instance scaling
	// This would require additional parameters to specify instance type

	return ErrGenericInstanceScalingNotImplemented
}

// scaleLoadBalancer scales load balancer backend members.
func scaleLoadBalancer(ctx context.Context, provider cpi.Provider, count int, dryRun bool, wait bool) error {
	log := logger.Get()
	log.Infow("Scaling load balancer backends", "target_count", count)

	if dryRun {
		return nil
	}

	network := provider.Network()
	if network == nil {
		return ErrProviderDoesNotSupportNetworkMgmt
	}

	loadBalancer, pool, err := getFirstLoadBalancerPool(ctx, network, log)
	if err != nil {
		return err
	}

	currentCount := len(pool.Members)
	log.Infow("Current backend count", "count", currentCount)

	if currentCount == count {
		log.Info("Already at desired count")

		return nil
	}

	err = scaleBackendMembers(ctx, network, loadBalancer.ID, pool, currentCount, count, log)
	if err != nil {
		return err
	}

	if wait {
		log.Info("Waiting for scaling to complete...")
	}

	return nil
}

func getFirstLoadBalancerPool(ctx context.Context, network cpi.NetworkManager, log logger.Logger) (*cpi.LoadBalancer, *cpi.BackendPool, error) {
	lbs, err := network.ListLoadBalancers(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list load balancers: %w", err)
	}

	if len(lbs) == 0 {
		return nil, nil, ErrNoLoadBalancersFound
	}

	loadBalancer := lbs[0]
	log.Infow("Scaling backends for load balancer", "name", loadBalancer.Name)

	pools, err := network.GetBackendPools(ctx, loadBalancer.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get backend pools: %w", err)
	}

	if len(pools) == 0 {
		log.Info("No backend pools found")

		return nil, nil, ErrNoBackendPoolsFound
	}

	return loadBalancer, pools[0], nil
}

func scaleBackendMembers(ctx context.Context, network cpi.NetworkManager, lbID string, pool *cpi.BackendPool, currentCount, targetCount int, log logger.Logger) error {
	if currentCount < targetCount {
		return scaleUpBackendMembers(ctx, network, lbID, currentCount, targetCount, log)
	}

	return scaleDownBackendMembers(ctx, network, lbID, pool, currentCount, targetCount, log)
}

func scaleUpBackendMembers(ctx context.Context, network cpi.NetworkManager, lbID string, currentCount, targetCount int, log logger.Logger) error {
	toAdd := targetCount - currentCount
	log.Infow("Adding backend members", "count", toAdd)

	for i := range toAdd {
		member := &cpi.BackendMember{
			IPAddress: fmt.Sprintf("10.0.1.%d", 100+currentCount+i),
			Port:      HTTPPort,
			Weight:    1,
		}

		err := network.AddBackendMember(ctx, lbID, member)
		if err != nil {
			log.Errorw("Failed to add backend member", "error", err)

			continue
		}

		log.Infow("Added backend member", "ip", member.IPAddress)
	}

	return nil
}

func scaleDownBackendMembers(ctx context.Context, network cpi.NetworkManager, lbID string, pool *cpi.BackendPool, currentCount, targetCount int, log logger.Logger) error {
	toRemove := currentCount - targetCount
	log.Infow("Removing backend members", "count", toRemove)

	for i := 0; i < toRemove && i < len(pool.Members); i++ {
		member := pool.Members[len(pool.Members)-1-i]

		err := network.RemoveBackendMember(ctx, lbID, member.IPAddress)
		if err != nil {
			log.Errorw("Failed to remove backend member", "error", err)

			continue
		}

		log.Infow("Removed backend member", "ip", member.IPAddress)
	}

	return nil
}

// scaleDatabase scales database instances.
func scaleDatabase(count int, dryRun bool) error {
	log := logger.Get()
	log.Infow("Scaling database instances", "target_count", count)

	if dryRun {
		// No-op here; dry-run handled at command layer with a plan
		return nil
	}

	if count > 1 {
		log.Infow("Setting up PostgreSQL replication", "replicas", count-1)
		// Pending: implement PostgreSQL replication setup
		// This would involve:
		// 1. Creating replica instances
		// 2. Configuring streaming replication
		// 3. Setting up connection pooling
		// 4. Configuring automatic failover
	}

	return ErrDatabaseScalingNotImplemented
}

// renderScalePlan builds and renders a dry-run plan for the scale command.
func renderScalePlan(ctx context.Context, resource string, target int, format string, provider cpi.Provider, cfg *config.Config) error {
	title := fmt.Sprintf("DRY RUN — Scale Plan (resource=%s, target=%d)", strings.ToLower(resource), target)
	planTable := &ui.Table{
		Title:    title,
		Summary:  "",
		Sections: nil,
	}

	var err error

	switch strings.ToLower(resource) {
	case ResourceRouters, ResourceRouter:
		err = buildRouterScalePlan(ctx, planTable, provider, cfg, target)
	case "cells", "cell", "diego-cells":
		err = buildCellScalePlan(ctx, planTable, provider, cfg, target)
	case "load-balancer", "lb":
		err = buildLBScalePlan(ctx, planTable, provider, target)
	case ResourceInstances, ResourceInstance:
		planTable.Summary = "Generic instance scaling not yet implemented"
	case "database", "db", "postgres":
		planTable.Summary = "Database scaling not yet implemented"
	default:
		return ErrUnknownResourceType(resource)
	}

	if err != nil {
		return err
	}

	if format == "" {
		format = OutputTable
	}

	err = ui.Render(planTable, format)
	if err != nil {
		return fmt.Errorf("failed to render scaling plan: %w", err)
	}

	return nil
}

func buildRouterScalePlan(ctx context.Context, planTable *ui.Table, provider cpi.Provider, cfg *config.Config, target int) error {
	compute := provider.Compute()
	if compute == nil {
		return ErrProviderDoesNotSupportComputeMgmt
	}

	instances, err := compute.ListInstances(ctx, map[string]string{"role": "router"})
	if err != nil {
		return fmt.Errorf("failed to list router instances: %w", err)
	}

	current := len(instances)
	delta := target - current
	planTable.Summary = fmt.Sprintf("Routers: current=%d target=%d delta=%+d", current, target, delta)

	// Add current instances section
	addCurrentInstancesSection(planTable, instances, "Current Routers")

	// Add planned changes section
	addInstancePlanChanges(planTable, instances, delta, cfg.Name, "router", current)

	return nil
}

func buildCellScalePlan(ctx context.Context, planTable *ui.Table, provider cpi.Provider, cfg *config.Config, target int) error {
	compute := provider.Compute()
	if compute == nil {
		return ErrProviderDoesNotSupportComputeMgmt
	}

	instances, err := compute.ListInstances(ctx, map[string]string{"role": "diego-cell"})
	if err != nil {
		return fmt.Errorf("failed to list Diego cell instances: %w", err)
	}

	current := len(instances)
	delta := target - current
	planTable.Summary = fmt.Sprintf("Diego Cells: current=%d target=%d delta=%+d", current, target, delta)

	// Add current instances section
	addCurrentInstancesSection(planTable, instances, "Current Cells")

	// Add planned changes section
	addInstancePlanChanges(planTable, instances, delta, cfg.Name, "diego-cell", current)

	return nil
}

func buildLBScalePlan(ctx context.Context, planTable *ui.Table, provider cpi.Provider, target int) error {
	network := provider.Network()
	if network == nil {
		return ErrProviderDoesNotSupportNetworkMgmt
	}

	lbs, err := network.ListLoadBalancers(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to list load balancers: %w", err)
	}

	if len(lbs) == 0 {
		return ErrNoLoadBalancersFound
	}

	loadBalancer := lbs[0]

	pools, err := network.GetBackendPools(ctx, loadBalancer.ID)
	if err != nil {
		return fmt.Errorf("failed to get backend pools: %w", err)
	}

	if len(pools) == 0 {
		planTable.Summary = fmt.Sprintf("LB: %s — no pools found", loadBalancer.Name)

		return nil
	}

	pool := pools[0]
	current := len(pool.Members)
	delta := target - current
	planTable.Summary = fmt.Sprintf("LB %s pool %s: current=%d target=%d delta=%+d", loadBalancer.Name, pool.Name, current, target, delta)

	// Current members section
	rows := make([][]string, 0, len(pool.Members))
	for _, m := range pool.Members {
		rows = append(rows, []string{m.IPAddress, strconv.Itoa(m.Port)})
	}

	planTable.Sections = append(planTable.Sections, ui.Section{Title: "Current Members", Headers: []string{"IP", "PORT"}, Rows: rows})

	// Planned changes section
	if delta > 0 {
		planTable.Sections = append(planTable.Sections, ui.Section{Title: "Add", Headers: []string{"COUNT"}, Rows: [][]string{{strconv.Itoa(delta)}}})
	} else if delta < 0 {
		planTable.Sections = append(planTable.Sections, ui.Section{Title: "Remove", Headers: []string{"COUNT"}, Rows: [][]string{{strconv.Itoa(-delta)}}})
	}

	return nil
}

func addCurrentInstancesSection(planTable *ui.Table, instances []*cpi.Instance, title string) {
	rows := make([][]string, 0, len(instances))
	for _, inst := range instances {
		rows = append(rows, []string{inst.Name, inst.ID})
	}

	planTable.Sections = append(planTable.Sections, ui.Section{Title: title, Headers: []string{"NAME", "ID"}, Rows: rows})
}

func addInstancePlanChanges(planTable *ui.Table, instances []*cpi.Instance, delta int, cfgName, roleType string, current int) {
	if delta > 0 {
		planned := make([][]string, 0, delta)
		for i := range delta {
			planned = append(planned, []string{fmt.Sprintf("%s-%s-%d", cfgName, roleType, current+i+1)})
		}

		planTable.Sections = append(planTable.Sections, ui.Section{Title: "Create", Headers: []string{"NAME"}, Rows: planned})
	} else if delta < 0 {
		removeN := -delta

		planned := make([][]string, 0, removeN)
		for i := 0; i < removeN && i < len(instances); i++ {
			inst := instances[len(instances)-1-i]
			planned = append(planned, []string{inst.Name, inst.ID})
		}

		planTable.Sections = append(planTable.Sections, ui.Section{Title: "Remove", Headers: []string{"NAME", "ID"}, Rows: planned})
	}
}
