package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// NewLBCmd creates the load balancer command.
func NewLBCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lb",
		Short: "Manage operational load balancers",
		Long: `Manage load balancers for Cloud Foundry deployments.

The lb command provides functionality to create, delete, and manage
load balancers including adding/removing services and checking status.`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Initialize per-command file logger; keep stdout for UX
			blocName := viper.GetString("bloc_name")
			logDir := filepath.Join(os.Getenv("HOME"), ".ocfp", "log")

			return logger.Initialize(logger.Config{
				Level:     viper.GetString("log_level"),
				Debug:     viper.GetBool("debug"),
				Verbose:   viper.GetBool("verbose"),
				Trace:     viper.GetBool("trace"),
				NoLog:     viper.GetBool("no_log"),
				LogDir:    logDir,
				BlocName:  blocName,
				Command:   "lb",
				RequestID: os.Getenv("OCFP_REQUEST_ID"),
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

// newLBCreateCmd creates the lb create subcommand.
func newLBCreateCmd() *cobra.Command {
	var (
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
	)

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
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()

			if name == "" {
				return errors.New("load balancer name is required")
			}

			// Load configuration
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc")

			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			if dryRun {
				t := &ui.Table{Title: "DRY RUN — LB Create Plan"}
				t.Summary = fmt.Sprintf("Create load balancer '%s'", name)
				t.Sections = append(t.Sections, ui.Section{
					Title:   "Load Balancer",
					Headers: []string{"NAME", "TYPE", "PROTOCOL", "ALGORITHM", "PORT", "TARGET_PORT", "TAGS"},
					Rows: [][]string{{
						name, lbType, protocol, algorithm,
						strconv.Itoa(port), strconv.Itoa(targetPort), strings.Join(tags, ","),
					}},
				})
				if healthCheck {
					t.Sections = append(t.Sections, ui.Section{
						Title:   "Health Check",
						Headers: []string{"PATH", "INTERVAL", "TIMEOUT", "THRESHOLDS"},
						Rows:    [][]string{{"/health", "30", "5", "3/3"}},
					})
				}
				if output == "" {
					output = "table"
				}

				return ui.Render(t, strings.ToLower(output))
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

			network := provider.Network()
			if network == nil {
				return errors.New("provider does not support network management")
			}

			log.Info("Creating load balancer", "name", name, "type", lbType)

			// Create load balancer configuration
			lbConfig := &cpi.LoadBalancer{
				Name:       name,
				Type:       lbType,
				Algorithm:  algorithm,
				Port:       port,
				TargetPort: targetPort,
				Protocol:   protocol,
				Tags:       tags,
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
			if healthCheck {
				healthConfig := &cpi.HealthCheck{
					Path:               "/health",
					Interval:           30,
					Timeout:            5,
					HealthyThreshold:   3,
					UnhealthyThreshold: 3,
				}

				err := network.ConfigureHealthCheck(ctx, loadBalancer.ID, healthConfig)
				if err != nil {
					log.Warn("Failed to configure health check", "error", err)
				} else {
					log.Info("Health check configured")
				}
			}

			fmt.Printf("Load balancer created: %s (%s)\n", loadBalancer.Name, loadBalancer.IPAddress)

			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "load balancer name")
	cmd.Flags().StringVar(&lbType, "type", "external", "load balancer type (external|internal)")
	cmd.Flags().StringVar(&algorithm, "algorithm", "round-robin", "load balancing algorithm")
	cmd.Flags().IntVar(&port, "port", 80, "load balancer port")
	cmd.Flags().IntVar(&targetPort, "target-port", 0, "backend target port (defaults to lb port)")
	cmd.Flags().StringVar(&protocol, "protocol", "tcp", "protocol (tcp|http|https)")
	cmd.Flags().BoolVar(&healthCheck, "health-check", false, "enable health checks")
	cmd.Flags().StringSliceVar(&tags, "tags", nil, "tags to apply to load balancer")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview creation without making changes")
	cmd.Flags().StringVar(&output, "output", "table", "output format: table|json|yaml (for dry-run plan)")

	return cmd
}

// newLBDeleteCmd creates the lb delete subcommand.
func newLBDeleteCmd() *cobra.Command {
	var (
		force  bool
		all    bool
		dryRun bool
		output string
	)

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
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()

			if !all && len(args) == 0 {
				return errors.New("load balancer name required (or use --all)")
			}

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
			if err := provider.Initialize(ctx, cfg); err != nil {
				return fmt.Errorf("failed to initialize provider: %w", err)
			}
			defer func() { _ = provider.Cleanup(ctx) }()

			network := provider.Network()
			if network == nil {
				return errors.New("provider does not support network management")
			}

			// Get list of load balancers to delete
			var lbsToDelete []string

			if all {
				lbs, err := network.ListLoadBalancers(ctx, nil)
				if err != nil {
					return fmt.Errorf("failed to list load balancers: %w", err)
				}
				for _, lb := range lbs {
					lbsToDelete = append(lbsToDelete, lb.ID)
				}
				log.Info("Found load balancers to delete", "count", len(lbsToDelete))
			} else {
				lbsToDelete = []string{args[0]}
			}

			if dryRun {
				// Build plan
				t := &ui.Table{Title: "DRY RUN — LB Delete Plan"}
				rows := [][]string{}
				if all {
					lbs, err := network.ListLoadBalancers(ctx, nil)
					if err != nil {
						return fmt.Errorf("failed to list load balancers: %w", err)
					}
					for _, l := range lbs {
						rows = append(rows, []string{l.Name, l.ID, l.IPAddress, strconv.Itoa(l.Port), l.Type})
					}
				} else {
					if lb, err := network.GetLoadBalancer(ctx, args[0]); err == nil {
						rows = append(rows, []string{lb.Name, lb.ID, lb.IPAddress, strconv.Itoa(lb.Port), lb.Type})
					} else {
						rows = append(rows, []string{args[0], "(not found)", "", "", ""})
					}
				}
				t.Summary = fmt.Sprintf("Delete %d load balancer(s)", len(rows))
				t.Sections = append(t.Sections, ui.Section{Title: "Load Balancers", Headers: []string{"NAME", "ID", "IP", "PORT", "TYPE"}, Rows: rows})
				if output == "" {
					output = "table"
				}

				return ui.Render(t, strings.ToLower(output))
			}

			// Confirm deletion if not forced
			if !force {
				fmt.Printf("This will delete %d load balancer(s). Continue? [y/N]: ", len(lbsToDelete))
				var response string
				_, _ = fmt.Scanln(&response)
				if !strings.HasPrefix(strings.ToLower(response), "y") {
					log.Info("Deletion cancelled by user")

					return nil
				}
			}

			// Delete load balancers
			for _, lbID := range lbsToDelete {
				log.Info("Deleting load balancer", "id", lbID)
				err := network.DeleteLoadBalancer(ctx, lbID)
				if err != nil {
					log.Error("Failed to delete load balancer", "id", lbID, "error", err)
				} else {
					log.Info("Load balancer deleted", "id", lbID)
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation prompt")
	cmd.Flags().BoolVar(&all, "all", false, "delete all load balancers")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview deletions without making changes")
	cmd.Flags().StringVar(&output, "output", "table", "output format: table|json|yaml (for dry-run plan)")

	return cmd
}

// newLBListCmd creates the lb list subcommand.
func newLBListCmd() *cobra.Command {
	var (
		output string
		filter string
	)

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
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			_ = logger.Get() // log not used in list output

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
			if err := provider.Initialize(ctx, cfg); err != nil {
				return fmt.Errorf("failed to initialize provider: %w", err)
			}
			defer func() { _ = provider.Cleanup(ctx) }()

			network := provider.Network()
			if network == nil {
				return errors.New("provider does not support network management")
			}

			// Parse filters
			filters := make(map[string]string)
			if filter != "" {
				parts := strings.Split(filter, "=")
				if len(parts) == 2 {
					filters[parts[0]] = parts[1]
				}
			}

			// List load balancers
			lbs, err := network.ListLoadBalancers(ctx, filters)
			if err != nil {
				return fmt.Errorf("failed to list load balancers: %w", err)
			}

			if len(lbs) == 0 {
				fmt.Println("No load balancers found")

				return nil
			}

			// Build a UI table and render (table/json/yaml)
			t := &ui.Table{Title: "Load Balancers"}
			rows := make([][]string, 0, len(lbs))
			for _, lb := range lbs {
				created := lb.CreatedAt.Format(time.RFC3339)
				rows = append(rows, []string{lb.Name, lb.Type, lb.IPAddress, strconv.Itoa(lb.Port), lb.Status, created})
			}
			t.Sections = append(t.Sections, ui.Section{Title: fmt.Sprintf("%d items", len(rows)), Headers: []string{"NAME", "TYPE", "IP", "PORT", "STATUS", "CREATED"}, Rows: rows})
			if output == "" {
				output = "table"
			}

			return ui.Render(t, strings.ToLower(output))

		},
	}

	cmd.Flags().StringVar(&output, "output", "table", "output format (table|json|yaml)")
	cmd.Flags().StringVar(&filter, "filter", "", "filter results (key=value)")

	return cmd
}

// newLBStatusCmd creates the lb status subcommand.
func newLBStatusCmd() *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "status <name>",
		Short: "Get load balancer status",
		Long:  `Get detailed status information for a load balancer.`,
		Example: `  # Get status of a load balancer
  ocfp lb status cf-router`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			lbName := args[0]

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
			if err := provider.Initialize(ctx, cfg); err != nil {
				return fmt.Errorf("failed to initialize provider: %w", err)
			}
			defer func() { _ = provider.Cleanup(ctx) }()

			network := provider.Network()
			if network == nil {
				return errors.New("provider does not support network management")
			}

			// Get load balancer
			loadBalancer, err := network.GetLoadBalancer(ctx, lbName)
			if err != nil {
				return fmt.Errorf("failed to get load balancer: %w", err)
			}

			// Render via shared UI
			t := &ui.Table{Title: "Load Balancer: " + loadBalancer.Name}
			t.Sections = append(t.Sections, ui.Section{Title: "Details", Headers: []string{"ID", "TYPE", "STATUS", "IP", "PORT", "ALGORITHM", "CREATED"}, Rows: [][]string{{loadBalancer.ID, loadBalancer.Type, loadBalancer.Status, loadBalancer.IPAddress, strconv.Itoa(loadBalancer.Port), loadBalancer.Algorithm, loadBalancer.CreatedAt.Format(time.RFC3339)}}})

			if pools, err := network.GetBackendPools(ctx, loadBalancer.ID); err == nil && len(pools) > 0 {
				rows := [][]string{}
				for _, pool := range pools {
					rows = append(rows, []string{pool.Name, strconv.Itoa(len(pool.Members))})
				}
				t.Sections = append(t.Sections, ui.Section{Title: "Backend Pools", Headers: []string{"NAME", "MEMBERS"}, Rows: rows})
			}
			if health, err := network.GetLoadBalancerHealth(ctx, loadBalancer.ID); err == nil {
				t.Sections = append(t.Sections, ui.Section{Title: "Health Status", Headers: []string{"HEALTHY", "UNHEALTHY", "TOTAL"}, Rows: [][]string{{strconv.Itoa(health.Healthy), strconv.Itoa(health.Unhealthy), strconv.Itoa(health.Total)}}})
			}
			if output == "" {
				output = "table"
			}

			return ui.Render(t, strings.ToLower(output))
		},
	}
	cmd.Flags().StringVar(&output, "output", "table", "output format (table|json|yaml)")

	return cmd
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
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()

			lbName := args[0]
			serviceIP := args[1]

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
			if err := provider.Initialize(ctx, cfg); err != nil {
				return fmt.Errorf("failed to initialize provider: %w", err)
			}
			defer func() { _ = provider.Cleanup(ctx) }()

			network := provider.Network()
			if network == nil {
				return errors.New("provider does not support network management")
			}

			log.Info("Adding service to load balancer",
				"lb", lbName,
				"service", serviceIP,
				"port", port)

			// Get load balancer
			loadBalancer, err := network.GetLoadBalancer(ctx, lbName)
			if err != nil {
				return fmt.Errorf("failed to get load balancer: %w", err)
			}

			// Resolve token references (reserved:, public-ip:)
			if isToken(serviceIP) {
				resolved, err := resolveTargetIP(blocName, serviceIP)
				if err != nil {
					return fmt.Errorf("failed to resolve %s: %w", serviceIP, err)
				}
				serviceIP = resolved
			}

			// Create backend member
			member := &cpi.BackendMember{
				IPAddress:  serviceIP,
				Port:       port,
				TargetPort: targetPort,
				Weight:     weight,
			}

			if dryRun {
				t := &ui.Table{Title: "DRY RUN — LB Add Service Plan"}
				t.Summary = "Add backend to " + lbName
				if pools, err := network.GetBackendPools(ctx, loadBalancer.ID); err == nil && len(pools) > 0 {
					rows := [][]string{}
					for _, m := range pools[0].Members {
						rows = append(rows, []string{m.IPAddress, strconv.Itoa(m.Port)})
					}
					t.Sections = append(t.Sections, ui.Section{Title: "Current Members", Headers: []string{"IP", "PORT"}, Rows: rows})
				}
				t.Sections = append(t.Sections, ui.Section{Title: "Add", Headers: []string{"IP", "PORT", "TARGET_PORT", "WEIGHT"}, Rows: [][]string{{member.IPAddress, strconv.Itoa(member.Port), strconv.Itoa(member.TargetPort), strconv.Itoa(member.Weight)}}})
				if output == "" {
					output = "table"
				}

				return ui.Render(t, strings.ToLower(output))
			}

			// Add member to backend pool
			if err := network.AddBackendMember(ctx, loadBalancer.ID, member); err != nil {
				return fmt.Errorf("failed to add backend member: %w", err)
			}

			log.Info("Service added to load balancer successfully")
			fmt.Printf("Added %s to load balancer %s\n", serviceIP, lbName)

			return nil
		},
	}

	cmd.Flags().IntVar(&port, "port", 80, "service port")
	cmd.Flags().IntVar(&targetPort, "target-port", 0, "target port (defaults to service port)")
	cmd.Flags().IntVar(&weight, "weight", 1, "service weight for weighted load balancing")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview member addition without making changes")
	cmd.Flags().StringVar(&output, "output", "table", "output format: table|json|yaml (for dry-run plan)")

	return cmd
}

// resolveReservedIP resolves a reserved IP output from state based on
// the token form reserved:<key>[:index]. Default index is 0 (ocfp-0).
func resolveReservedIP(blocName string, token string) (string, error) {
	// token format: reserved:key or reserved:key:index
	parts := strings.Split(strings.TrimPrefix(token, "reserved:"), ":")
	if len(parts) == 0 || parts[0] == "" {
		return "", errors.New("invalid reserved format; expected reserved:<key>[:index]")
	}

	key := parts[0]

	index := "0"
	if len(parts) > 1 && parts[1] != "" {
		index = parts[1]
	}
	// Build output key: reserved_<bloc>-ocfp-<index>_<key>
	stateKey := fmt.Sprintf("reserved_%s-ocfp-%s_%s", blocName, index, key)

	stateManager, err := state.NewManager("")
	if err != nil {
		return "", fmt.Errorf("state manager: %w", err)
	}

	if _, err := stateManager.Load(blocName); err != nil {
		return "", fmt.Errorf("load state: %w", err)
	}

	val, err := stateManager.GetOutput(stateKey)
	if err != nil {
		return "", fmt.Errorf("output %s not found", stateKey)
	}

	ipAddress, ok := val.(string)
	if !ok || ipAddress == "" {
		return "", fmt.Errorf("output %s empty or not string", stateKey)
	}

	return ipAddress, nil
}

// isToken returns true if arg begins with a supported token prefix.
func isToken(s string) bool {
	return strings.HasPrefix(s, "reserved:") || strings.HasPrefix(s, "public-ip:")
}

// resolveTargetIP resolves tokens of the form:
//   - reserved:<key>[:index]
//   - public-ip:<job>[:index]
func resolveTargetIP(blocName string, token string) (string, error) {
	if strings.HasPrefix(token, "reserved:") {
		return resolveReservedIP(blocName, token)
	}

	if strings.HasPrefix(token, "public-ip:") {
		rest := strings.TrimPrefix(token, "public-ip:")

		parts := strings.Split(rest, ":")
		if len(parts) == 0 || parts[0] == "" {
			return "", errors.New("invalid public-ip token; expected public-ip:<job>[:index]")
		}

		job := parts[0]

		index := ""
		if len(parts) > 1 {
			index = parts[1]
		}

		ip, err := findPublicIPByJob(blocName, job, index)
		if err != nil {
			return "", err
		}

		if ip == "" {
			return "", fmt.Errorf("no matching public-ip for job %s index %s", job, index)
		}

		return ip, nil
	}

	return token, nil
}

// findPublicIPByJob reads state for public_ip resources with matching job and optional index.
func findPublicIPByJob(blocName, job, index string) (string, error) {
	stateManager, err := state.NewManager("")
	if err != nil {
		return "", fmt.Errorf("state manager: %w", err)
	}

	if _, err := stateManager.Load(blocName); err != nil {
		return "", fmt.Errorf("load state: %w", err)
	}

	res, err := stateManager.ListResources("public_ip")
	if err != nil {
		return "", err
	}

	for _, resource := range res {
		// check job via tags or properties
		var rjob string
		if resource.Tags != nil && resource.Tags["job"] != "" {
			rjob = resource.Tags["job"]
		}

		if rjob == "" {
			if v, ok := resource.Properties["job"].(string); ok {
				rjob = v
			}
		}

		if rjob != job {
			continue
		}
		// check index if provided
		if index != "" {
			var ridx string
			if resource.Tags != nil && resource.Tags["index"] != "" {
				ridx = resource.Tags["index"]
			}

			if ridx == "" {
				if v, ok := resource.Properties["index"].(string); ok {
					ridx = v
				}
			}

			if ridx != index {
				continue
			}
		}

		if addr, ok := resource.Properties["address"].(string); ok && addr != "" {
			return addr, nil
		}
	}

	return "", nil
}

// newLBRemoveServiceCmd creates the lb remove-service subcommand.
func newLBRemoveServiceCmd() *cobra.Command {
	var (
		force  bool
		dryRun bool
		output string
	)

	cmd := &cobra.Command{
		Use:   "remove-service <lb-name> <service-ip>",
		Short: "Remove a service from load balancer",
		Long:  `Remove a backend service from a load balancer pool.`,
		Example: `  # Remove a service from load balancer
  ocfp lb remove-service cf-router 10.0.1.10

  # Force removal without confirmation
  ocfp lb remove-service cf-router 10.0.1.10 --force`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()

			lbName := args[0]
			serviceIP := args[1]

			// Confirm removal if not forced
			if !force {
				fmt.Printf("Remove %s from load balancer %s? [y/N]: ", serviceIP, lbName)
				var response string
				_, _ = fmt.Scanln(&response)
				if !strings.HasPrefix(strings.ToLower(response), "y") {
					log.Info("Removal cancelled by user")

					return nil
				}
			}

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
			if err := provider.Initialize(ctx, cfg); err != nil {
				return fmt.Errorf("failed to initialize provider: %w", err)
			}
			defer func() { _ = provider.Cleanup(ctx) }()

			network := provider.Network()
			if network == nil {
				return errors.New("provider does not support network management")
			}

			log.Info("Removing service from load balancer", "lb", lbName, "service", serviceIP)

			// Get load balancer
			loadBalancer, err := network.GetLoadBalancer(ctx, lbName)
			if err != nil {
				return fmt.Errorf("failed to get load balancer: %w", err)
			}

			if dryRun {
				t := &ui.Table{Title: "DRY RUN — LB Remove Service Plan"}
				t.Summary = "Remove backend from " + lbName
				if pools, err := network.GetBackendPools(ctx, loadBalancer.ID); err == nil && len(pools) > 0 {
					rows := [][]string{}
					for _, m := range pools[0].Members {
						rows = append(rows, []string{m.IPAddress, strconv.Itoa(m.Port)})
					}
					t.Sections = append(t.Sections, ui.Section{Title: "Current Members", Headers: []string{"IP", "PORT"}, Rows: rows})
				}
				t.Sections = append(t.Sections, ui.Section{Title: "Remove", Headers: []string{"IP"}, Rows: [][]string{{serviceIP}}})
				if output == "" {
					output = "table"
				}

				return ui.Render(t, strings.ToLower(output))
			}

			// Remove member from backend pool
			if err := network.RemoveBackendMember(ctx, loadBalancer.ID, serviceIP); err != nil {
				return fmt.Errorf("failed to remove backend member: %w", err)
			}

			log.Info("Service removed from load balancer successfully")
			fmt.Printf("Removed %s from load balancer %s\n", serviceIP, lbName)

			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation prompt")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview member removal without making changes")
	cmd.Flags().StringVar(&output, "output", "table", "output format: table|json|yaml (for dry-run plan)")

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

	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Update load balancer configuration",
		Long:  `Update configuration settings for an existing load balancer.`,
		Example: `  # Update load balancing algorithm
  ocfp lb update cf-router --algorithm least-connections

  # Update health check settings
  ocfp lb update cf-router --health-check /health --timeout 10`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()

			lbName := args[0]

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
			if err := provider.Initialize(ctx, cfg); err != nil {
				return fmt.Errorf("failed to initialize provider: %w", err)
			}
			defer func() { _ = provider.Cleanup(ctx) }()

			network := provider.Network()
			if network == nil {
				return errors.New("provider does not support network management")
			}

			log.Info("Updating load balancer", "name", lbName)

			// Get existing load balancer
			loadBalancer, err := network.GetLoadBalancer(ctx, lbName)
			if err != nil {
				return fmt.Errorf("failed to get load balancer: %w", err)
			}

			// DRY-RUN plan
			if dryRun {
				t := &ui.Table{Title: "DRY RUN — LB Update Plan"}
				t.Summary = "Update load balancer " + lbName
				rows := [][]string{}
				if algorithm != "" && algorithm != loadBalancer.Algorithm {
					rows = append(rows, []string{"algorithm", loadBalancer.Algorithm, algorithm})
				}
				if healthCheck != "" {
					rows = append(rows, []string{"health_check.path", "/", healthCheck})
					rows = append(rows, []string{"health_check.timeout", "5", strconv.Itoa(timeout)})
				}
				if len(rows) == 0 {
					rows = append(rows, []string{"no-op", "", ""})
				}
				t.Sections = append(t.Sections, ui.Section{Title: "Changes", Headers: []string{"FIELD", "CURRENT", "NEW"}, Rows: rows})
				if output == "" {
					output = "table"
				}

				return ui.Render(t, strings.ToLower(output))
			}

			// Update fields if provided
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

			// Update health check if provided
			if healthCheck != "" {
				healthConfig := &cpi.HealthCheck{
					Path:               healthCheck,
					Interval:           30,
					Timeout:            timeout,
					HealthyThreshold:   3,
					UnhealthyThreshold: 3,
				}

				err := network.ConfigureHealthCheck(ctx, loadBalancer.ID, healthConfig)
				if err != nil {
					return fmt.Errorf("failed to update health check: %w", err)
				}
				log.Info("Health check configuration updated")
			}

			fmt.Printf("Load balancer %s updated successfully\n", lbName)

			return nil
		},
	}

	cmd.Flags().StringVar(&algorithm, "algorithm", "", "load balancing algorithm (round-robin|least-connections|ip-hash)")
	cmd.Flags().StringVar(&healthCheck, "health-check", "", "health check path")
	cmd.Flags().IntVar(&timeout, "timeout", 5, "health check timeout in seconds")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview updates without making changes")
	cmd.Flags().StringVar(&output, "output", "table", "output format: table|json|yaml (for dry-run plan)")

	return cmd
}
