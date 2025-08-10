package commands

import (
	"context"
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// NewConfigureCmd creates the configure command
func NewConfigureCmd() *cobra.Command {
	var (
		dryRun          bool
		skipRoutes      bool
		skipFloatingIPs bool
		skipSecGroups   bool
		skipBastion     bool
	)

	cmd := &cobra.Command{
		Use:   "configure",
		Short: "Apply configuration to provisioned infrastructure",
		Long: `Configure applies configuration settings to already provisioned infrastructure.

This command:
- Configures security group rules for network access
- Sets up network routes between subnets
- Associates floating IPs with instances
- Configures storage settings
- Finalizes bastion host configuration

The configure command should be run after bootstrap to apply the final
configuration to your infrastructure.`,
		Example: `  # Apply all configurations
  ocfp configure --bloc-name production

  # Dry run to see what would be configured
  ocfp configure --bloc-name production --dry-run

  # Skip specific configuration steps
  ocfp configure --bloc-name production --skip-routes --skip-floating-ips`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()

			// Load configuration
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc-name")

			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
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
			defer provider.Cleanup(ctx)

			log.Info("Starting configuration", "provider", cfg.Provider, "dry-run", dryRun)

			// Configure security groups
			if !skipSecGroups {
				if err := configureSecurityGroups(ctx, provider, cfg, dryRun); err != nil {
					return fmt.Errorf("failed to configure security groups: %w", err)
				}
			}

			// Configure network routes
			if !skipRoutes {
				if err := configureRoutes(ctx, provider, cfg, dryRun); err != nil {
					return fmt.Errorf("failed to configure routes: %w", err)
				}
			}

			// Configure floating IPs
			if !skipFloatingIPs {
				if err := configureFloatingIPs(ctx, provider, cfg, dryRun); err != nil {
					return fmt.Errorf("failed to configure floating IPs: %w", err)
				}
			}

			// Configure bastion
			if !skipBastion {
				if err := configureBastion(ctx, provider, cfg, dryRun); err != nil {
					return fmt.Errorf("failed to configure bastion: %w", err)
				}
			}

			if dryRun {
				log.Info("Dry run completed - no changes were made")
			} else {
				log.Info("Configuration completed successfully")
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview configuration without applying")
	cmd.Flags().BoolVar(&skipRoutes, "skip-routes", false, "skip route configuration")
	cmd.Flags().BoolVar(&skipFloatingIPs, "skip-floating-ips", false, "skip floating IP configuration")
	cmd.Flags().BoolVar(&skipSecGroups, "skip-security-groups", false, "skip security group configuration")
	cmd.Flags().BoolVar(&skipBastion, "skip-bastion", false, "skip bastion configuration")

	// Bind flags to viper
	_ = viper.BindPFlag("configure.dry_run", cmd.Flags().Lookup("dry-run"))
	_ = viper.BindPFlag("configure.skip_routes", cmd.Flags().Lookup("skip-routes"))
	_ = viper.BindPFlag("configure.skip_floating_ips", cmd.Flags().Lookup("skip-floating-ips"))
	_ = viper.BindPFlag("configure.skip_security_groups", cmd.Flags().Lookup("skip-security-groups"))
	_ = viper.BindPFlag("configure.skip_bastion", cmd.Flags().Lookup("skip-bastion"))

	return cmd
}

// configureSecurityGroups configures security group rules
func configureSecurityGroups(ctx context.Context, provider cpi.Provider, cfg *config.Config, dryRun bool) error {
	log := logger.Get()
	log.Info("Configuring security groups")

	security := provider.Security()
	if security == nil {
		return fmt.Errorf("provider does not support security management")
	}

	// List existing security groups
	groups, err := security.ListSecurityGroups(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to list security groups: %w", err)
	}

	for _, group := range groups {
		log.Info("Found security group", "name", group.Name, "id", group.ID)

		if dryRun {
			log.Info("[DRY RUN] Would configure rules for security group", "name", group.Name)
			continue
		}

		// Add default rules based on group type
		switch group.Name {
		case "bosh-sg":
			// BOSH director security group rules
			rules := []cpi.SecurityRule{
				{Protocol: "tcp", PortRangeMin: 22, PortRangeMax: 22, RemoteIPCIDR: "0.0.0.0/0", Description: "SSH", Direction: "ingress"},
				{Protocol: "tcp", PortRangeMin: 6868, PortRangeMax: 6868, RemoteIPCIDR: "10.0.0.0/8", Description: "BOSH Agent", Direction: "ingress"},
				{Protocol: "tcp", PortRangeMin: 25555, PortRangeMax: 25555, RemoteIPCIDR: "10.0.0.0/8", Description: "BOSH Director", Direction: "ingress"},
				{Protocol: "tcp", PortRangeMin: 8443, PortRangeMax: 8443, RemoteIPCIDR: "10.0.0.0/8", Description: "UAA", Direction: "ingress"},
				{Protocol: "tcp", PortRangeMin: 8844, PortRangeMax: 8844, RemoteIPCIDR: "10.0.0.0/8", Description: "CredHub", Direction: "ingress"},
			}

			for _, rule := range rules {
				if err := security.AddSecurityRule(ctx, group.ID, &rule); err != nil {
					log.Warn("Failed to add rule", "error", err, "rule", rule.Description)
				} else {
					log.Info("Added security rule", "group", group.Name, "rule", rule.Description)
				}
			}

		case "ocf-sg":
			// Cloud Foundry security group rules
			rules := []cpi.SecurityRule{
				{Protocol: "tcp", PortRangeMin: 80, PortRangeMax: 80, RemoteIPCIDR: "0.0.0.0/0", Description: "HTTP", Direction: "ingress"},
				{Protocol: "tcp", PortRangeMin: 443, PortRangeMax: 443, RemoteIPCIDR: "0.0.0.0/0", Description: "HTTPS", Direction: "ingress"},
				{Protocol: "tcp", PortRangeMin: 2222, PortRangeMax: 2222, RemoteIPCIDR: "0.0.0.0/0", Description: "CF SSH", Direction: "ingress"},
				{Protocol: "tcp", PortRangeMin: 4443, PortRangeMax: 4443, RemoteIPCIDR: "0.0.0.0/0", Description: "WSS", Direction: "ingress"},
			}

			for _, rule := range rules {
				if err := security.AddSecurityRule(ctx, group.ID, &rule); err != nil {
					log.Warn("Failed to add rule", "error", err, "rule", rule.Description)
				} else {
					log.Info("Added security rule", "group", group.Name, "rule", rule.Description)
				}
			}
		}
	}

	return nil
}

// configureRoutes configures network routes
func configureRoutes(ctx context.Context, provider cpi.Provider, cfg *config.Config, dryRun bool) error {
	log := logger.Get()
	log.Info("Configuring network routes")

	network := provider.Network()
	if network == nil {
		return fmt.Errorf("provider does not support network management")
	}

	// List routers
	routers, err := network.ListRouters(ctx)
	if err != nil {
		return fmt.Errorf("failed to list routers: %w", err)
	}

	for _, router := range routers {
		log.Info("Found router", "name", router.Name, "id", router.ID)

		if dryRun {
			log.Info("[DRY RUN] Would configure routes for router", "name", router.Name)
			continue
		}

		// TODO: Add route configuration based on bloc requirements
	}

	return nil
}

// configureFloatingIPs associates floating IPs with instances
func configureFloatingIPs(ctx context.Context, provider cpi.Provider, cfg *config.Config, dryRun bool) error {
	log := logger.Get()
	log.Info("Configuring floating IPs")

	network := provider.Network()
	if network == nil {
		return fmt.Errorf("provider does not support network management")
	}

	compute := provider.Compute()
	if compute == nil {
		return fmt.Errorf("provider does not support compute management")
	}

	// List floating IPs
	ips, err := network.ListFloatingIPs(ctx)
	if err != nil {
		return fmt.Errorf("failed to list floating IPs: %w", err)
	}

	// Find bastion instance
	instances, err := compute.ListInstances(ctx, map[string]string{"role": "bastion"})
	if err != nil {
		return fmt.Errorf("failed to list instances: %w", err)
	}

	if len(instances) > 0 && len(ips) > 0 {
		bastion := instances[0]
		floatingIP := ips[0]

		if floatingIP.InstanceID == "" {
			if dryRun {
				log.Info("[DRY RUN] Would associate floating IP with bastion",
					"ip", floatingIP.Address, "instance", bastion.Name)
			} else {
				if err := network.AssociateFloatingIP(ctx, floatingIP.ID, bastion.ID); err != nil {
					return fmt.Errorf("failed to associate floating IP: %w", err)
				}
				log.Info("Associated floating IP with bastion",
					"ip", floatingIP.Address, "instance", bastion.Name)
			}
		} else {
			log.Info("Floating IP already associated", "ip", floatingIP.Address)
		}
	}

	return nil
}

// configureBastion finalizes bastion host configuration
func configureBastion(ctx context.Context, provider cpi.Provider, cfg *config.Config, dryRun bool) error {
	log := logger.Get()
	log.Info("Configuring bastion host")

	compute := provider.Compute()
	if compute == nil {
		return fmt.Errorf("provider does not support compute management")
	}

	// Find bastion instance
	instances, err := compute.ListInstances(ctx, map[string]string{"role": "bastion"})
	if err != nil {
		return fmt.Errorf("failed to list instances: %w", err)
	}

	if len(instances) == 0 {
		log.Warn("No bastion instance found")
		return nil
	}

	bastion := instances[0]
	log.Info("Found bastion", "name", bastion.Name, "id", bastion.ID)

	if dryRun {
		log.Info("[DRY RUN] Would configure bastion", "name", bastion.Name)
		return nil
	}

	// TODO: Add bastion configuration steps
	// - Install required packages
	// - Configure SSH
	// - Set up jump host configuration
	// - Configure firewall rules

	return nil
}
