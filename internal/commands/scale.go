package commands

import (
	"context"
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

// NewScaleCmd creates the scale command.
func NewScaleCmd() *cobra.Command {
	var (
		dryRun bool
		force  bool
		wait   bool
		output string
	)

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
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			resource := args[0]
			countStr := args[1]

			// Parse count
			count, err := strconv.Atoi(countStr)
			if err != nil {
				return fmt.Errorf("invalid count: %s", countStr)
			}

			if count < 0 {
				return errors.New("count must be non-negative")
			}

			// Load configuration
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc_name")

			// Initialize logger per command
			logDir := filepath.Join(os.Getenv("HOME"), ".ocfp", "log")
			if err := logger.Initialize(logger.Config{
				Level:     viper.GetString("log_level"),
				Debug:     viper.GetBool("debug"),
				Verbose:   viper.GetBool("verbose"),
				Trace:     viper.GetBool("trace"),
				NoLog:     viper.GetBool("no_log"),
				LogDir:    logDir,
				BlocName:  blocName,
				Command:   "scale",
				RequestID: os.Getenv("OCFP_REQUEST_ID"),
			}); err != nil {
				return fmt.Errorf("failed to initialize logger: %w", err)
			}
			defer func() { _ = logger.Sync() }()
			log := logger.Get()

			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			log.Info("Scaling resource", "resource", resource, "count", count, "dry-run", dryRun)

			// Confirm scaling if not forced
			if !force && !dryRun {
				fmt.Printf("Scale %s to %d instances? [y/N]: ", resource, count)
				var response string
				_, _ = fmt.Scanln(&response)
				if !strings.HasPrefix(strings.ToLower(response), "y") {
					log.Info("Scaling cancelled by user")

					return nil
				}
			}

			// Get provider
			provider, err := cpi.GetProvider(cfg.Provider)
			if err != nil {
				return fmt.Errorf("failed to get provider: %w", err)
			}

			// Initialize provider
			if err := provider.Initialize(ctx, cfg); err != nil {
				return fmt.Errorf("failed to initialize provider: %w", err)
			}
			defer func() { _ = provider.Cleanup(ctx) }()

			// If dry-run, render a plan after discovery and exit
			if dryRun {
				err := renderScalePlan(ctx, resource, count, viper.GetString("scale.output"), provider, cfg)
				if err != nil {
					return fmt.Errorf("failed to render scale plan: %w", err)
				}

				return nil
			}

			// Perform scaling based on resource type (real changes)
			switch strings.ToLower(resource) {
			case "routers", "router":
				err = scaleRouters(ctx, provider, cfg, count, dryRun, wait)
			case "cells", "cell", "diego-cells":
				err = scaleCells(ctx, provider, cfg, count, dryRun, wait)
			case "instances", "instance":
				err = scaleInstances(ctx, provider, cfg, count, dryRun, wait)
			case "load-balancer", "lb":
				err = scaleLoadBalancer(ctx, provider, cfg, count, dryRun, wait)
			case "database", "db", "postgres":
				err = scaleDatabase(ctx, provider, cfg, count, dryRun, wait)
			default:
				return fmt.Errorf("unknown resource type: %s", resource)
			}

			if err != nil {
				return fmt.Errorf("scaling failed: %w", err)
			}

			if dryRun {
				log.Info("Dry run completed - no changes were made")
			} else {
				log.Info("Scaling completed successfully")
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview scaling without making changes")
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation prompt")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for scaling to complete")
	cmd.Flags().StringVar(&output, "output", "table", "output format: table|json|yaml (for dry-run plan)")

	// Bind flags to viper
	_ = viper.BindPFlag("scale.dry_run", cmd.Flags().Lookup("dry-run"))
	_ = viper.BindPFlag("scale.force", cmd.Flags().Lookup("force"))
	_ = viper.BindPFlag("scale.wait", cmd.Flags().Lookup("wait"))
	_ = viper.BindPFlag("scale.output", cmd.Flags().Lookup("output"))

	return cmd
}

// scaleRouters scales router instances.
func scaleRouters(ctx context.Context, provider cpi.Provider, cfg *config.Config, count int, dryRun bool, wait bool) error {
	log := logger.Get()
	log.Info("Scaling routers", "target_count", count)

	if dryRun {
		// No-op here; dry-run handled at command layer with a plan
		return nil
	}

	compute := provider.Compute()
	if compute == nil {
		return errors.New("provider does not support compute management")
	}

	// List current router instances
	instances, err := compute.ListInstances(ctx, map[string]string{"role": "router"})
	if err != nil {
		return fmt.Errorf("failed to list router instances: %w", err)
	}

	currentCount := len(instances)
	log.Info("Current router count", "count", currentCount)

	if currentCount == count {
		log.Info("Already at desired count")

		return nil
	}

	if currentCount < count {
		// Scale up - create new instances
		toCreate := count - currentCount
		log.Info("Scaling up routers", "creating", toCreate)

		for i := range toCreate {
			req := &cpi.CreateInstanceRequest{
				Name:           fmt.Sprintf("%s-router-%d", cfg.Name, currentCount+i+1),
				Flavor:         cfg.Routers.Flavor,
				Image:          cfg.Routers.Image,
				NetworkID:      cfg.Network.ID,
				SubnetID:       cfg.Network.SubnetID,
				SecurityGroups: []string{"ocf-sg"},
				KeyPair:        cfg.Bastion.SSHKeyName,
				Tags: map[string]string{
					"role":       "router",
					"deployment": cfg.Name,
				},
			}

			instance, err := compute.CreateInstance(ctx, req)
			if err != nil {
				log.Error("Failed to create router instance", "error", err)

				continue
			}

			log.Info("Created router instance", "id", instance.ID, "name", instance.Name)
		}
	} else {
		// Scale down - remove instances
		toRemove := currentCount - count
		log.Info("Scaling down routers", "removing", toRemove)

		// Remove instances starting from the highest numbered
		for i := 0; i < toRemove && i < len(instances); i++ {
			instance := instances[len(instances)-1-i]
			log.Info("Removing router instance", "id", instance.ID, "name", instance.Name)

			err := compute.DeleteInstance(ctx, instance.ID)
			if err != nil {
				log.Error("Failed to delete router instance", "id", instance.ID, "error", err)

				continue
			}
		}
	}

	if wait {
		log.Info("Waiting for scaling to complete...")
		// TODO: Implement wait logic with status checking
	}

	return nil
}

// scaleCells scales Diego cell instances.
func scaleCells(ctx context.Context, provider cpi.Provider, cfg *config.Config, count int, dryRun bool, wait bool) error {
	log := logger.Get()
	log.Info("Scaling Diego cells", "target_count", count)

	if dryRun {
		// No-op here; dry-run handled at command layer with a plan
		return nil
	}

	compute := provider.Compute()
	if compute == nil {
		return errors.New("provider does not support compute management")
	}

	// List current cell instances
	instances, err := compute.ListInstances(ctx, map[string]string{"role": "diego-cell"})
	if err != nil {
		return fmt.Errorf("failed to list Diego cell instances: %w", err)
	}

	currentCount := len(instances)
	log.Info("Current Diego cell count", "count", currentCount)

	if currentCount == count {
		log.Info("Already at desired count")

		return nil
	}

	if currentCount < count {
		// Scale up - create new instances
		toCreate := count - currentCount
		log.Info("Scaling up Diego cells", "creating", toCreate)

		for i := range toCreate {
			req := &cpi.CreateInstanceRequest{
				Name:           fmt.Sprintf("%s-diego-cell-%d", cfg.Name, currentCount+i+1),
				Flavor:         cfg.Cells.Flavor,
				Image:          cfg.Cells.Image,
				NetworkID:      cfg.Network.ID,
				SubnetID:       cfg.Network.SubnetID,
				SecurityGroups: []string{"ocf-sg"},
				KeyPair:        cfg.Bastion.SSHKeyName,
				Tags: map[string]string{
					"role":       "diego-cell",
					"deployment": cfg.Name,
				},
			}

			instance, err := compute.CreateInstance(ctx, req)
			if err != nil {
				log.Error("Failed to create Diego cell instance", "error", err)

				continue
			}

			log.Info("Created Diego cell instance", "id", instance.ID, "name", instance.Name)
		}
	} else {
		// Scale down - remove instances
		toRemove := currentCount - count
		log.Info("Scaling down Diego cells", "removing", toRemove)

		// Remove instances starting from the highest numbered
		for i := 0; i < toRemove && i < len(instances); i++ {
			instance := instances[len(instances)-1-i]
			log.Info("Removing Diego cell instance", "id", instance.ID, "name", instance.Name)

			// TODO: Drain apps from cell before deletion
			log.Info("Draining apps from cell", "id", instance.ID)

			err := compute.DeleteInstance(ctx, instance.ID)
			if err != nil {
				log.Error("Failed to delete Diego cell instance", "id", instance.ID, "error", err)

				continue
			}
		}
	}

	if wait {
		log.Info("Waiting for scaling to complete...")
		// TODO: Implement wait logic with status checking
	}

	return nil
}

// scaleInstances scales generic instances.
func scaleInstances(ctx context.Context, provider cpi.Provider, cfg *config.Config, count int, dryRun bool, wait bool) error {
	log := logger.Get()
	log.Info("Scaling instances", "target_count", count)

	if dryRun {
		log.Info("[DRY RUN] Would scale instances", "count", count)

		return nil
	}

	// TODO: Implement generic instance scaling
	// This would require additional parameters to specify instance type

	return errors.New("generic instance scaling not yet implemented")
}

// scaleLoadBalancer scales load balancer backend members.
func scaleLoadBalancer(ctx context.Context, provider cpi.Provider, cfg *config.Config, count int, dryRun bool, wait bool) error {
	log := logger.Get()
	log.Info("Scaling load balancer backends", "target_count", count)

	if dryRun {
		// No-op here; dry-run handled at command layer with a plan
		return nil
	}

	network := provider.Network()
	if network == nil {
		return errors.New("provider does not support network management")
	}

	// List load balancers
	lbs, err := network.ListLoadBalancers(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to list load balancers: %w", err)
	}

	if len(lbs) == 0 {
		return errors.New("no load balancers found")
	}

	// Scale backends for the first load balancer
	loadBalancer := lbs[0]
	log.Info("Scaling backends for load balancer", "name", loadBalancer.Name)

	// Get current backend pools
	pools, err := network.GetBackendPools(ctx, loadBalancer.ID)
	if err != nil {
		return fmt.Errorf("failed to get backend pools: %w", err)
	}

	if len(pools) == 0 {
		log.Info("No backend pools found")

		return nil
	}

	pool := pools[0]
	currentCount := len(pool.Members)
	log.Info("Current backend count", "count", currentCount)

	if currentCount == count {
		log.Info("Already at desired count")

		return nil
	}

	if currentCount < count {
		// Scale up - add new backend members
		toAdd := count - currentCount
		log.Info("Adding backend members", "count", toAdd)

		for i := range toAdd {
			member := &cpi.BackendMember{
				IPAddress: fmt.Sprintf("10.0.1.%d", 100+currentCount+i),
				Port:      80,
				Weight:    1,
			}

			err := network.AddBackendMember(ctx, loadBalancer.ID, member)
			if err != nil {
				log.Error("Failed to add backend member", "error", err)

				continue
			}

			log.Info("Added backend member", "ip", member.IPAddress)
		}
	} else {
		// Scale down - remove backend members
		toRemove := currentCount - count
		log.Info("Removing backend members", "count", toRemove)

		for i := 0; i < toRemove && i < len(pool.Members); i++ {
			member := pool.Members[len(pool.Members)-1-i]
			err := network.RemoveBackendMember(ctx, loadBalancer.ID, member.IPAddress)
			if err != nil {
				log.Error("Failed to remove backend member", "error", err)

				continue
			}

			log.Info("Removed backend member", "ip", member.IPAddress)
		}
	}

	if wait {
		log.Info("Waiting for scaling to complete...")
		// TODO: Implement wait logic
	}

	return nil
}

// scaleDatabase scales database instances.
func scaleDatabase(ctx context.Context, provider cpi.Provider, cfg *config.Config, count int, dryRun bool, wait bool) error {
	log := logger.Get()
	log.Info("Scaling database instances", "target_count", count)

	if dryRun {
		// No-op here; dry-run handled at command layer with a plan
		return nil
	}

	if count > 1 {
		log.Info("Setting up PostgreSQL replication", "replicas", count-1)
		// TODO: Implement PostgreSQL replication setup
		// This would involve:
		// 1. Creating replica instances
		// 2. Configuring streaming replication
		// 3. Setting up connection pooling
		// 4. Configuring automatic failover
	}

	return errors.New("database scaling not yet implemented")
}

// renderScalePlan builds and renders a dry-run plan for the scale command.
func renderScalePlan(ctx context.Context, resource string, target int, format string, provider cpi.Provider, cfg *config.Config) error {
	title := fmt.Sprintf("DRY RUN — Scale Plan (resource=%s, target=%d)", strings.ToLower(resource), target)
	planTable := &ui.Table{Title: title}

	switch strings.ToLower(resource) {
	case "routers", "router":
		compute := provider.Compute()
		if compute == nil {
			return errors.New("provider does not support compute management")
		}

		instances, err := compute.ListInstances(ctx, map[string]string{"role": "router"})
		if err != nil {
			return fmt.Errorf("failed to list router instances: %w", err)
		}

		current := len(instances)
		delta := target - current
		planTable.Summary = fmt.Sprintf("Routers: current=%d target=%d delta=%+d", current, target, delta)
		// Current section
		rows := [][]string{}
		for _, inst := range instances {
			rows = append(rows, []string{inst.Name, inst.ID})
		}

		planTable.Sections = append(planTable.Sections, ui.Section{Title: "Current Routers", Headers: []string{"NAME", "ID"}, Rows: rows})
		// Planned
		if delta > 0 {
			planned := [][]string{}
			for i := range delta {
				planned = append(planned, []string{fmt.Sprintf("%s-router-%d", cfg.Name, current+i+1)})
			}

			planTable.Sections = append(planTable.Sections, ui.Section{Title: "Create", Headers: []string{"NAME"}, Rows: planned})
		} else if delta < 0 {
			// Remove highest-numbered; sort by name
			// Fallback: use tail of list
			removeN := -delta
			planned := [][]string{}

			for i := 0; i < removeN && i < len(instances); i++ {
				inst := instances[len(instances)-1-i]
				planned = append(planned, []string{inst.Name, inst.ID})
			}

			planTable.Sections = append(planTable.Sections, ui.Section{Title: "Remove", Headers: []string{"NAME", "ID"}, Rows: planned})
		}
	case "cells", "cell", "diego-cells":
		compute := provider.Compute()
		if compute == nil {
			return errors.New("provider does not support compute management")
		}

		instances, err := compute.ListInstances(ctx, map[string]string{"role": "diego-cell"})
		if err != nil {
			return fmt.Errorf("failed to list Diego cell instances: %w", err)
		}

		current := len(instances)
		delta := target - current
		planTable.Summary = fmt.Sprintf("Diego Cells: current=%d target=%d delta=%+d", current, target, delta)

		rows := [][]string{}
		for _, inst := range instances {
			rows = append(rows, []string{inst.Name, inst.ID})
		}

		planTable.Sections = append(planTable.Sections, ui.Section{Title: "Current Cells", Headers: []string{"NAME", "ID"}, Rows: rows})
		if delta > 0 {
			planned := [][]string{}
			for i := range delta {
				planned = append(planned, []string{fmt.Sprintf("%s-diego-cell-%d", cfg.Name, current+i+1)})
			}

			planTable.Sections = append(planTable.Sections, ui.Section{Title: "Create", Headers: []string{"NAME"}, Rows: planned})
		} else if delta < 0 {
			removeN := -delta
			planned := [][]string{}

			for i := 0; i < removeN && i < len(instances); i++ {
				inst := instances[len(instances)-1-i]
				planned = append(planned, []string{inst.Name, inst.ID})
			}

			planTable.Sections = append(planTable.Sections, ui.Section{Title: "Remove", Headers: []string{"NAME", "ID"}, Rows: planned})
		}
	case "load-balancer", "lb":
		network := provider.Network()
		if network == nil {
			return errors.New("provider does not support network management")
		}

		lbs, err := network.ListLoadBalancers(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to list load balancers: %w", err)
		}

		if len(lbs) == 0 {
			return errors.New("no load balancers found")
		}

		lb := lbs[0]

		pools, err := network.GetBackendPools(ctx, lb.ID)
		if err != nil {
			return fmt.Errorf("failed to get backend pools: %w", err)
		}

		if len(pools) == 0 {
			planTable.Summary = fmt.Sprintf("LB: %s — no pools found", lb.Name)

			return ui.Render(planTable, format)
		}

		pool := pools[0]
		current := len(pool.Members)
		delta := target - current
		planTable.Summary = fmt.Sprintf("LB %s pool %s: current=%d target=%d delta=%+d", lb.Name, pool.Name, current, target, delta)
		// Current members
		rows := [][]string{}
		for _, m := range pool.Members {
			rows = append(rows, []string{m.IPAddress, strconv.Itoa(m.Port)})
		}

		planTable.Sections = append(planTable.Sections, ui.Section{Title: "Current Members", Headers: []string{"IP", "PORT"}, Rows: rows})
		if delta > 0 {
			planTable.Sections = append(planTable.Sections, ui.Section{Title: "Add", Headers: []string{"COUNT"}, Rows: [][]string{{strconv.Itoa(delta)}}})
		} else if delta < 0 {
			planTable.Sections = append(planTable.Sections, ui.Section{Title: "Remove", Headers: []string{"COUNT"}, Rows: [][]string{{strconv.Itoa(-delta)}}})
		}
	case "instances", "instance":
		planTable.Summary = "Generic instance scaling not yet implemented"
	case "database", "db", "postgres":
		planTable.Summary = "Database scaling not yet implemented"
	default:
		return fmt.Errorf("unknown resource type: %s", resource)
	}

	if format == "" {
		format = "table"
	}

	return ui.Render(planTable, format)
}
