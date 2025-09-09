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

const (
	// Standard network ports.
	PortSSH       = 22
	PortHTTP      = 80
	PortHTTPS     = 443
	PortCFSSH     = 2222
	PortWSS       = 4443
	PortBoshAgent = 6868
	PortUAA       = 8443
	PortCredHub   = 8844
	PortBoshDir   = 25555
)

type configureOptions struct {
	dryRun          bool
	skipRoutes      bool
	skipFloatingIPs bool
	skipSecGroups   bool
	skipBastion     bool
}

// NewConfigureCmd creates the configure command.
func NewConfigureCmd() *cobra.Command {
	opts := &configureOptions{}

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
  ocfp configure --bloc production

  # Dry run to see what would be configured
  ocfp configure --bloc production --dry-run

  # Skip specific configuration steps
  ocfp configure --bloc production --skip-routes --skip-floating-ips`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigure(opts)
		},
	}

	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "preview configuration without applying")
	cmd.Flags().BoolVar(&opts.skipRoutes, "skip-routes", false, "skip route configuration")
	cmd.Flags().BoolVar(&opts.skipFloatingIPs, "skip-floating-ips", false, "skip floating IP configuration")
	cmd.Flags().BoolVar(&opts.skipSecGroups, "skip-security-groups", false, "skip security group configuration")
	cmd.Flags().BoolVar(&opts.skipBastion, "skip-bastion", false, "skip bastion configuration")

	// Bind flags to viper
	_ = viper.BindPFlag("configure.dry_run", cmd.Flags().Lookup("dry-run"))
	_ = viper.BindPFlag("configure.skip_routes", cmd.Flags().Lookup("skip-routes"))
	_ = viper.BindPFlag("configure.skip_floating_ips", cmd.Flags().Lookup("skip-floating-ips"))
	_ = viper.BindPFlag("configure.skip_security_groups", cmd.Flags().Lookup("skip-security-groups"))
	_ = viper.BindPFlag("configure.skip_bastion", cmd.Flags().Lookup("skip-bastion"))

	return cmd
}

func runConfigure(opts *configureOptions) error {
	ctx := context.Background()
	log := logger.Get()

	// Load configuration
	configFile := viper.GetString("config")
	blocName := viper.GetString("bloc")

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
	err = provider.Initialize(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize provider: %w", err)
	}

	defer func() { _ = provider.Cleanup(ctx) }()

	log.Info("Starting configuration", "provider", cfg.Provider, "dry-run", opts.dryRun)

	// Configure security groups
	if !opts.skipSecGroups {
		err := configureSecurityGroups(ctx, provider, opts.dryRun)
		if err != nil {
			return fmt.Errorf("failed to configure security groups: %w", err)
		}
	}

	// Configure network routes
	if !opts.skipRoutes {
		err := configureRoutes(ctx, provider, opts.dryRun)
		if err != nil {
			return fmt.Errorf("failed to configure routes: %w", err)
		}
	}

	// Configure floating IPs
	if !opts.skipFloatingIPs {
		err := configureFloatingIPs(ctx, provider, opts.dryRun)
		if err != nil {
			return fmt.Errorf("failed to configure floating IPs: %w", err)
		}
	}

	// Configure bastion
	if !opts.skipBastion {
		err := configureBastion(ctx, provider, opts.dryRun)
		if err != nil {
			return fmt.Errorf("failed to configure bastion: %w", err)
		}
	}

	if opts.dryRun {
		log.Info("Dry run completed - no changes were made")
	} else {
		log.Info("Configuration completed successfully")
	}

	return nil
}

// configureSecurityGroups configures security group rules.
func configureSecurityGroups(ctx context.Context, provider cpi.Provider, dryRun bool) error {
	log := logger.Get()
	log.Info("Configuring security groups")

	security := provider.Security()
	if security == nil {
		return ErrProviderDoesNotSupportSecurityMgmt
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
		rules := getSecurityGroupRules(group.Name)
		if len(rules) > 0 {
			applySecurityRules(ctx, security, group, rules)
		}
	}

	return nil
}

// getSecurityGroupRules returns the security rules for a given security group name.
func getSecurityGroupRules(groupName string) []cpi.SecurityRule {
	switch groupName {
	case "bosh-sg":
		return []cpi.SecurityRule{
			{Protocol: "tcp", PortRangeMin: PortSSH, PortRangeMax: PortSSH, RemoteIPCIDR: "0.0.0.0/0", Description: "SSH", Direction: "ingress"},
			{Protocol: "tcp", PortRangeMin: PortBoshAgent, PortRangeMax: PortBoshAgent, RemoteIPCIDR: "10.0.0.0/8", Description: "BOSH Agent", Direction: "ingress"},
			{Protocol: "tcp", PortRangeMin: PortBoshDir, PortRangeMax: PortBoshDir, RemoteIPCIDR: "10.0.0.0/8", Description: "BOSH Director", Direction: "ingress"},
			{Protocol: "tcp", PortRangeMin: PortUAA, PortRangeMax: PortUAA, RemoteIPCIDR: "10.0.0.0/8", Description: "UAA", Direction: "ingress"},
			{Protocol: "tcp", PortRangeMin: PortCredHub, PortRangeMax: PortCredHub, RemoteIPCIDR: "10.0.0.0/8", Description: "CredHub", Direction: "ingress"},
		}
	case "ocf-sg":
		return []cpi.SecurityRule{
			{Protocol: "tcp", PortRangeMin: PortHTTP, PortRangeMax: PortHTTP, RemoteIPCIDR: "0.0.0.0/0", Description: "HTTP", Direction: "ingress"},
			{Protocol: "tcp", PortRangeMin: PortHTTPS, PortRangeMax: PortHTTPS, RemoteIPCIDR: "0.0.0.0/0", Description: "HTTPS", Direction: "ingress"},
			{Protocol: "tcp", PortRangeMin: PortCFSSH, PortRangeMax: PortCFSSH, RemoteIPCIDR: "0.0.0.0/0", Description: "CF SSH", Direction: "ingress"},
			{Protocol: "tcp", PortRangeMin: PortWSS, PortRangeMax: PortWSS, RemoteIPCIDR: "0.0.0.0/0", Description: "WSS", Direction: "ingress"},
		}
	default:
		return nil
	}
}

// applySecurityRules applies a set of security rules to a security group.
func applySecurityRules(ctx context.Context, security cpi.SecurityManager, group *cpi.SecurityGroup, rules []cpi.SecurityRule) {
	log := logger.Get()

	for _, rule := range rules {
		err := security.AddSecurityRule(ctx, group.ID, &rule)
		if err != nil {
			log.Warn("Failed to add rule", "error", err, "rule", rule.Description)
		} else {
			log.Info("Added security rule", "group", group.Name, "rule", rule.Description)
		}
	}
}

// configureRoutes configures network routes.
func configureRoutes(ctx context.Context, provider cpi.Provider, dryRun bool) error {
	log := logger.Get()
	log.Info("Configuring network routes")

	network := provider.Network()
	if network == nil {
		return ErrProviderDoesNotSupportNetworkMgmt
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

		// Pending: add route configuration based on bloc requirements
	}

	return nil
}

// configureFloatingIPs associates floating IPs with instances.
func configureFloatingIPs(ctx context.Context, provider cpi.Provider, dryRun bool) error {
	log := logger.Get()
	log.Info("Configuring floating IPs")

	network := provider.Network()
	if network == nil {
		return ErrProviderDoesNotSupportNetworkMgmt
	}

	compute := provider.Compute()
	if compute == nil {
		return ErrProviderDoesNotSupportComputeMgmt
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
		return associateFloatingIPWithBastion(ctx, network, instances[0], ips[0], dryRun)
	}

	return nil
}

func associateFloatingIPWithBastion(ctx context.Context, network cpi.NetworkManager, bastion *cpi.Instance, floatingIP *cpi.FloatingIP, dryRun bool) error {
	log := logger.Get()

	if floatingIP.InstanceID == "" {
		if dryRun {
			log.Info("[DRY RUN] Would associate floating IP with bastion",
				"ip", floatingIP.Address, "instance", bastion.Name)

			return nil
		}

		err := network.AssociateFloatingIP(ctx, floatingIP.ID, bastion.ID)
		if err != nil {
			return fmt.Errorf("failed to associate floating IP: %w", err)
		}

		log.Info("Associated floating IP with bastion",
			"ip", floatingIP.Address, "instance", bastion.Name)

		return nil
	}

	log.Info("Floating IP already associated", "ip", floatingIP.Address)

	return nil
}

// configureBastion finalizes bastion host configuration.
func configureBastion(ctx context.Context, provider cpi.Provider, dryRun bool) error {
	log := logger.Get()
	log.Info("Configuring bastion host")

	compute := provider.Compute()
	if compute == nil {
		return ErrProviderDoesNotSupportComputeMgmt
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

	// Pending: add bastion configuration steps
	// - Install required packages
	// - Configure SSH
	// - Set up jump host configuration
	// - Configure firewall rules

	return nil
}
