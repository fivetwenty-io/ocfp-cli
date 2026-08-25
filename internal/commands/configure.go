package commands

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ocfp/ocfp-cli-go/internal/bastion"
	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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
		RunE: func(_cmd *cobra.Command, _args []string) error {
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

//
//nolint:funlen // sequential configuration phases cannot be meaningfully split
func runConfigure(opts *configureOptions) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	log.Infow("Starting configuration", "provider", cfg.Provider, "dry-run", opts.dryRun)

	// Configure security groups
	if !opts.skipSecGroups {
		err := configureSecurityGroups(ctx, provider, cfg, blocName, opts.dryRun)
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
		err := configureFloatingIPs(ctx, provider, blocName, opts.dryRun)
		if err != nil {
			return fmt.Errorf("failed to configure floating IPs: %w", err)
		}
	}

	// Configure bastion
	if !opts.skipBastion {
		err := configureBastion(ctx, cfg, provider, blocName, opts.dryRun)
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

// configureSecurityGroups reconciles existing security groups against the
// rule definitions derived from config — the same definitions bootstrap
// creates them from — so config changes (e.g. allowed_ingress_ips) take
// effect on `ocfp configure` without re-running bootstrap. Reconciliation is
// add-only: missing rules are added, extra rules are left alone.
func configureSecurityGroups(ctx context.Context, provider cpi.Provider, cfg *config.Config, blocName string, dryRun bool) error {
	log := logger.Get()
	log.Info("Configuring security groups")

	security := provider.SecurityManager()
	if security == nil {
		return ErrProviderDoesNotSupportSecurityMgmt
	}

	// List existing security groups
	groups, err := security.ListSecurityGroups(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to list security groups: %w", err)
	}

	ruleDefs := bootstrap.DefaultSecurityGroupRules(cfg)

	for _, group := range groups {
		// Groups are named "<bloc>-<short name>"; groups ocfp has no
		// definition for (including hand-made ones) are left untouched.
		shortName := strings.TrimPrefix(group.Name, blocName+"-")

		expected, ok := ruleDefs[shortName]
		if !ok || shortName == group.Name {
			log.Infow("Skipping security group with no ocfp definition", "name", group.Name)

			continue
		}

		if dryRun {
			log.Infow("[DRY RUN] Would reconcile rules for security group", "name", group.Name)

			continue
		}

		err := reconcileSecurityRules(ctx, security, group, expected)
		if err != nil {
			log.Warnw("Failed to reconcile security group", "name", group.Name, "error", err)
		}
	}

	return nil
}

// reconcileSecurityRules adds the expected rules a security group is missing.
// Existing rules are never removed.
func reconcileSecurityRules(ctx context.Context, security cpi.SecurityManager, group *cpi.SecurityGroup, expected []*cpi.SecurityRule) error {
	log := logger.Get()

	current, err := security.ListSecurityRules(ctx, group.ID)
	if err != nil {
		return fmt.Errorf("failed to list rules for group %s: %w", group.Name, err)
	}

	for _, rule := range expected {
		exists := false

		for _, cur := range current {
			if bootstrap.RulesMatch(cur, rule) {
				exists = true

				break
			}
		}

		if exists {
			continue
		}

		err := security.AddSecurityRule(ctx, group.ID, rule)
		if err != nil {
			log.Warnw("Failed to add rule", "group", group.Name, "rule", rule.Description, "error", err)
		} else {
			log.Infow("Added security rule", "group", group.Name, "rule", rule.Description)
		}
	}

	return nil
}

// configureRoutes configures network routes.
func configureRoutes(ctx context.Context, provider cpi.Provider, dryRun bool) error {
	log := logger.Get()
	log.Info("Configuring network routes")

	network := provider.NetworkManager()
	if network == nil {
		return ErrProviderDoesNotSupportNetworkMgmt
	}

	// List routers
	routers, err := network.ListRouters(ctx)
	if err != nil {
		return fmt.Errorf("failed to list routers: %w", err)
	}

	for _, router := range routers {
		log.Infow("Found router", "name", router.Name, "id", router.ID)

		if dryRun {
			log.Infow("[DRY RUN] Would configure routes for router", "name", router.Name)

			continue
		}

		// Pending: add route configuration based on bloc requirements
	}

	return nil
}

// configureFloatingIPs associates floating IPs with instances.
func configureFloatingIPs(ctx context.Context, provider cpi.Provider, blocName string, dryRun bool) error {
	log := logger.Get()
	log.Info("Configuring floating IPs")

	network := provider.NetworkManager()
	if network == nil {
		return ErrProviderDoesNotSupportNetworkMgmt
	}

	// List floating IPs scoped to this bloc to avoid associating EIPs from other blocs.
	ips, err := network.ListFloatingIPs(ctx, map[string]string{
		"bloc":       blocName,
		"managed-by": "ocfp",
	})
	if err != nil {
		return fmt.Errorf("failed to list floating IPs: %w", err)
	}

	log.Infow("Found floating IPs for bloc", "bloc", blocName, "count", len(ips))

	// Find bastion instance using robust multi-strategy discovery
	bastion, err := findBastionInstance(ctx, provider, blocName)
	if err != nil {
		log.Warnw("No bastion instance found for floating IP association", "error", err)

		return nil //nolint:nilerr // bastion not found is non-fatal
	}

	log.Infow("Found bastion for floating IP association", "name", bastion.Name, "id", bastion.ID)

	if len(ips) > 0 {
		log.Infow("Associating floating IP with bastion",
			"ip_id", ips[0].ID, "ip_address", ips[0].Address,
			"bastion_id", bastion.ID, "bastion_name", bastion.Name)

		return associateFloatingIPWithBastion(ctx, network, bastion, ips[0], dryRun)
	}

	log.Warnw("No floating IPs found for bloc", "bloc", blocName)

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

	log.Infow("Floating IP already associated", "ip", floatingIP.Address)

	return nil
}

// configureBastion finalizes bastion host configuration.
func configureBastion(ctx context.Context, cfg *config.Config, provider cpi.Provider, blocName string, dryRun bool) error {
	log := logger.Get()
	log.Info("Configuring bastion host")

	if provider.ComputeManager() == nil {
		return ErrProviderDoesNotSupportComputeMgmt
	}

	// Find bastion instance to verify it exists before provisioning
	bastionInstance, err := findBastionInstance(ctx, provider, blocName)
	if err != nil {
		log.Warnw("No bastion instance found", "error", err)

		return nil //nolint:nilerr // bastion not found is non-fatal
	}

	log.Infow("Found bastion", "name", bastionInstance.Name, "id", bastionInstance.ID)

	// Resolve the bastion's current public IP from live cloud data.
	// This bypasses the state cache which may hold a stale IP from a
	// previous bootstrap (e.g., if the EC2 instance was replaced).
	bastionIP := resolveBastionPublicIP(ctx, provider, bastionInstance, blocName)
	if bastionIP != "" {
		log.Infow("Resolved live bastion IP", "ip", bastionIP)
		cfg.BastionIP = bastionIP           // getBastionIP() checks config.BastionIP first
		cacheBastionIP(blocName, bastionIP) // update state cache for future commands
	}

	// Provision bastion via the Go Manager (23-phase orchestration)
	provOpts := &bastion.ProvisioningOptions{
		DryRun:      dryRun,
		ProgressOut: os.Stdout,
	}

	log.Info("Initializing bastion provisioning")

	err = bastion.InitializeBastionWithMode(ctx, cfg, provOpts)
	if err != nil {
		return fmt.Errorf("bastion provisioning failed: %w", err)
	}

	log.Info("Bastion provisioning completed successfully")

	return nil
}

// resolveBastionPublicIP resolves the bastion's current public IP from live
// cloud data, bypassing the local state cache.
func resolveBastionPublicIP(ctx context.Context, provider cpi.Provider, inst *cpi.Instance, blocName string) string {
	log := logger.Get()

	// Check instance's direct public/floating IP
	if ip := firstNonEmpty(inst.FloatingIP, inst.PublicIP); ip != "" {
		log.Debugw("Bastion IP from instance", "ip", ip, "instance", inst.Name)

		return ip
	}

	// Check floating IPs associated by instance ID
	network := provider.NetworkManager()
	if network == nil {
		return ""
	}

	fips, err := network.ListFloatingIPs(ctx, map[string]string{
		"bloc":       blocName,
		"managed-by": "ocfp",
	})
	if err != nil {
		log.Debugw("Failed to list floating IPs for bastion IP resolution", "error", err)

		return ""
	}

	for _, fip := range fips {
		if fip.InstanceID == inst.ID && fip.Address != "" {
			log.Debugw("Bastion IP from floating IP", "ip", fip.Address, "instance", inst.Name)

			return fip.Address
		}
	}

	return ""
}
