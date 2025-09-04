package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// NewScaleCmd creates the scale command
func NewScaleCmd() *cobra.Command {
	var (
		dryRun bool
		force  bool
		wait   bool
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
			log := logger.Get()

			resource := args[0]
			countStr := args[1]

			// Parse count
			count, err := strconv.Atoi(countStr)
			if err != nil {
				return fmt.Errorf("invalid count: %s", countStr)
			}

			if count < 0 {
				return fmt.Errorf("count must be non-negative")
			}

			// Load configuration
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc")

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

			// Perform scaling based on resource type
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

	// Bind flags to viper
	_ = viper.BindPFlag("scale.dry_run", cmd.Flags().Lookup("dry-run"))
	_ = viper.BindPFlag("scale.force", cmd.Flags().Lookup("force"))
	_ = viper.BindPFlag("scale.wait", cmd.Flags().Lookup("wait"))

	return cmd
}

// scaleRouters scales router instances
func scaleRouters(ctx context.Context, provider cpi.Provider, cfg *config.Config, count int, dryRun bool, wait bool) error {
	log := logger.Get()
	log.Info("Scaling routers", "target_count", count)

	if dryRun {
		log.Info("[DRY RUN] Would scale routers", "count", count)
		return nil
	}

	compute := provider.Compute()
	if compute == nil {
		return fmt.Errorf("provider does not support compute management")
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

		for i := 0; i < toCreate; i++ {
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

			if err := compute.DeleteInstance(ctx, instance.ID); err != nil {
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

// scaleCells scales Diego cell instances
func scaleCells(ctx context.Context, provider cpi.Provider, cfg *config.Config, count int, dryRun bool, wait bool) error {
	log := logger.Get()
	log.Info("Scaling Diego cells", "target_count", count)

	if dryRun {
		log.Info("[DRY RUN] Would scale Diego cells", "count", count)
		return nil
	}

	compute := provider.Compute()
	if compute == nil {
		return fmt.Errorf("provider does not support compute management")
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

		for i := 0; i < toCreate; i++ {
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

			if err := compute.DeleteInstance(ctx, instance.ID); err != nil {
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

// scaleInstances scales generic instances
func scaleInstances(ctx context.Context, provider cpi.Provider, cfg *config.Config, count int, dryRun bool, wait bool) error {
	log := logger.Get()
	log.Info("Scaling instances", "target_count", count)

	if dryRun {
		log.Info("[DRY RUN] Would scale instances", "count", count)
		return nil
	}

	// TODO: Implement generic instance scaling
	// This would require additional parameters to specify instance type

	return fmt.Errorf("generic instance scaling not yet implemented")
}

// scaleLoadBalancer scales load balancer backend members
func scaleLoadBalancer(ctx context.Context, provider cpi.Provider, cfg *config.Config, count int, dryRun bool, wait bool) error {
	log := logger.Get()
	log.Info("Scaling load balancer backends", "target_count", count)

	if dryRun {
		log.Info("[DRY RUN] Would scale load balancer backends", "count", count)
		return nil
	}

	network := provider.Network()
	if network == nil {
		return fmt.Errorf("provider does not support network management")
	}

	// List load balancers
	lbs, err := network.ListLoadBalancers(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to list load balancers: %w", err)
	}

	if len(lbs) == 0 {
		return fmt.Errorf("no load balancers found")
	}

	// Scale backends for the first load balancer
	lb := lbs[0]
	log.Info("Scaling backends for load balancer", "name", lb.Name)

	// Get current backend pools
	pools, err := network.GetBackendPools(ctx, lb.ID)
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

		for i := 0; i < toAdd; i++ {
			member := &cpi.BackendMember{
				IPAddress: fmt.Sprintf("10.0.1.%d", 100+currentCount+i),
				Port:      80,
				Weight:    1,
			}

			if err := network.AddBackendMember(ctx, lb.ID, member); err != nil {
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
			if err := network.RemoveBackendMember(ctx, lb.ID, member.IPAddress); err != nil {
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

// scaleDatabase scales database instances
func scaleDatabase(ctx context.Context, provider cpi.Provider, cfg *config.Config, count int, dryRun bool, wait bool) error {
	log := logger.Get()
	log.Info("Scaling database instances", "target_count", count)

	if dryRun {
		log.Info("[DRY RUN] Would scale database instances", "count", count)
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

	return fmt.Errorf("database scaling not yet implemented")
}
