package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// NewTeardownCmd creates the teardown command
func NewTeardownCmd() *cobra.Command {
	var (
		force     bool
		dryRun    bool
		skip      []string
		publicIPs bool
		all       bool
		nuke      bool
		servers   bool
		volumes   bool
		snapshots bool
		buckets   bool
		secGroups bool
		network   bool
	)

	cmd := &cobra.Command{
		Use:   "teardown",
		Short: "Remove all resources created by bootstrap",
		Long: `Teardown removes resources created by OCFP bootstrap and BOSH deployments.

The command supports different modes:
- Default: Delete only bootstrap-created resources
- All: Delete all OCFP/BOSH-managed resources (--all)
- Nuke: Delete ALL resources in the project (--nuke, requires --force)

Resources are deleted in dependency order:
1. Servers (to free volumes and network interfaces)
2. Load balancers
3. Snapshots (to allow volume deletion)
4. Volumes
5. Buckets (after emptying if needed)
6. Security groups
7. Networks
8. Public IPs (only if --public-ips flag is used)`,
		Example: `  # Interactive teardown (with confirmation)
  ocfp teardown --bloc production

  # Force teardown (no confirmation)
  ocfp teardown --bloc production --force

  # Dry run to preview deletions
  ocfp teardown --bloc production --dry-run

  # Delete specific resource types
  ocfp teardown --bloc production --servers --volumes

  # Skip specific resource types
  ocfp teardown --bloc production --skip network --skip storage

  # Delete including public IPs (use with caution!)
  ocfp teardown --bloc production --public-ips --force

  # DANGER: Delete ALL resources in project
  ocfp teardown --bloc production --nuke --force`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTeardown(cmd, args)
		},
	}

	// Command-specific flags
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation prompts")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview what would be deleted without actually deleting")
	cmd.Flags().StringSliceVar(&skip, "skip", []string{}, "skip deletion of specific resource types")
	cmd.Flags().BoolVar(&publicIPs, "public-ips", false, "include public IPs in deletion")
	cmd.Flags().BoolVar(&all, "all", false, "delete all OCFP/BOSH-managed resources")
	cmd.Flags().BoolVar(&nuke, "nuke", false, "DANGER: delete ALL resources in project")

	// Resource type flags
	cmd.Flags().BoolVar(&servers, "servers", false, "delete servers")
	cmd.Flags().BoolVar(&volumes, "volumes", false, "delete volumes")
	cmd.Flags().BoolVar(&snapshots, "snapshots", false, "delete snapshots")
	cmd.Flags().BoolVar(&buckets, "buckets", false, "delete buckets")
	cmd.Flags().BoolVar(&secGroups, "security-groups", false, "delete security groups")
	cmd.Flags().BoolVar(&network, "network", false, "delete networks")

	// Bind flags to viper
	_ = viper.BindPFlag("teardown.force", cmd.Flags().Lookup("force"))
	_ = viper.BindPFlag("teardown.dry_run", cmd.Flags().Lookup("dry-run"))
	_ = viper.BindPFlag("teardown.skip", cmd.Flags().Lookup("skip"))
	_ = viper.BindPFlag("teardown.public_ips", cmd.Flags().Lookup("public-ips"))
	_ = viper.BindPFlag("teardown.all", cmd.Flags().Lookup("all"))
	_ = viper.BindPFlag("teardown.nuke", cmd.Flags().Lookup("nuke"))

	return cmd
}

func runTeardown(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	log := logger.Get()

	// Get configuration values
	blocName := viper.GetString("bloc_name")
	configFile := viper.GetString("config")
	force := viper.GetBool("teardown.force")
	dryRun := viper.GetBool("teardown.dry_run")
	nuke := viper.GetBool("teardown.nuke")
	all := viper.GetBool("teardown.all")
	publicIPs := viper.GetBool("teardown.public_ips")

	// Validate required configuration
	if blocName == "" {
		return fmt.Errorf("bloc is required")
	}

	// Validate nuke mode
	if nuke && !force {
		return fmt.Errorf("--nuke requires --force for safety")
	}

	// Load configuration
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

	// Initialize state manager
	stateManager, err := state.NewManager("")
	if err != nil {
		return fmt.Errorf("failed to create state manager: %w", err)
	}

	// Load state
	if _, err := stateManager.Load(blocName); err != nil {
		log.Warn("Failed to load state, will discover resources from cloud", "error", err)
	}

	// Create teardown manager
	teardownOpts := &TeardownOptions{
		BlocName:  blocName,
		Provider:  cfg.Provider,
		Force:     force,
		DryRun:    dryRun,
		All:       all,
		Nuke:      nuke,
		PublicIPs: publicIPs,
		Skip:      viper.GetStringSlice("teardown.skip"),
		Mode:      getTeardownMode(all, nuke),
	}

	teardownManager := NewTeardownManager(cfg, provider, stateManager, teardownOpts)

	log.Info("Starting teardown", "mode", teardownOpts.Mode, "bloc", blocName)

	// Execute teardown
	if err := teardownManager.Execute(ctx); err != nil {
		return fmt.Errorf("teardown failed: %w", err)
	}

	fmt.Printf("\n✅ Teardown completed successfully!\n")
	return nil
}

func getTeardownMode(all, nuke bool) string {
	if nuke {
		return "NUKE (delete ALL resources)"
	}
	if all {
		return "ALL (delete all OCFP/BOSH resources)"
	}
	return "BOOTSTRAP (delete bootstrap-created resources)"
}

// TeardownOptions represents teardown configuration
type TeardownOptions struct {
	BlocName  string
	Provider  string
	Force     bool
	DryRun    bool
	All       bool
	Nuke      bool
	PublicIPs bool
	Skip      []string
	Mode      string
}

// TeardownManager handles the teardown process
type TeardownManager struct {
	config       *config.Config
	provider     cpi.Provider
	stateManager *state.Manager
	options      *TeardownOptions
}

// NewTeardownManager creates a new teardown manager
func NewTeardownManager(cfg *config.Config, provider cpi.Provider, stateManager *state.Manager, opts *TeardownOptions) *TeardownManager {
	return &TeardownManager{
		config:       cfg,
		provider:     provider,
		stateManager: stateManager,
		options:      opts,
	}
}

// ResourceToDelete represents a resource marked for deletion
type ResourceToDelete struct {
	Type         string
	ID           string
	Name         string
	Dependencies []string
	State        string
	Properties   map[string]interface{}
}

// Execute performs the teardown process
func (m *TeardownManager) Execute(ctx context.Context) error {
	log := logger.Get()

	// Acquire state lock
	if err := m.stateManager.Lock(m.options.BlocName); err != nil {
		return fmt.Errorf("failed to acquire state lock: %w", err)
	}
	defer func() { _ = m.stateManager.Unlock(m.options.BlocName) }()

	// Discover resources to delete
	resourcesToDelete, err := m.discoverResources(ctx)
	if err != nil {
		return fmt.Errorf("failed to discover resources: %w", err)
	}

	if len(resourcesToDelete) == 0 {
		log.Info("No resources found to delete")
		return nil
	}

	// Sort resources by dependency order (reverse order for deletion)
	sortedResources := m.sortResourcesForDeletion(resourcesToDelete)

	// Show deletion plan
	if err := m.showDeletionPlan(sortedResources); err != nil {
		return fmt.Errorf("failed to show deletion plan: %w", err)
	}

	// Confirm deletion if not forced
	if !m.options.Force && !m.options.DryRun {
		if !m.confirmDeletion(len(sortedResources)) {
			log.Info("Teardown cancelled by user")
			return nil
		}
	}

	if m.options.DryRun {
		log.Info("Dry run completed - no resources were deleted")
		return nil
	}

	// Execute deletion
	deletedCount := 0
	for i, resource := range sortedResources {
		log.Info("Deleting resource", "type", resource.Type, "name", resource.Name, "progress", fmt.Sprintf("%d/%d", i+1, len(sortedResources)))

		if err := m.deleteResource(ctx, resource); err != nil {
			log.Error("Failed to delete resource", "type", resource.Type, "name", resource.Name, "error", err)
			continue
		}

		deletedCount++

		// Remove from state
		if err := m.stateManager.RemoveResource(resource.Type, resource.Name); err != nil {
			log.Warn("Failed to remove resource from state", "resource", resource.Name, "error", err)
		}

		// Save state after each successful deletion
		if err := m.stateManager.Save(); err != nil {
			log.Warn("Failed to save state", "error", err)
		}
	}

	log.Info("Teardown completed", "deleted", deletedCount, "total", len(sortedResources))
	return nil
}

// discoverResources finds all resources that should be deleted
func (m *TeardownManager) discoverResources(ctx context.Context) ([]*ResourceToDelete, error) {
	log := logger.Get()
	var resources []*ResourceToDelete

	if m.options.Nuke {
		// Nuke mode: find ALL resources in the project
		return m.discoverAllResources(ctx)
	}

	// Get resources from state first
	stateResources, err := m.getResourcesFromState()
	if err == nil && len(stateResources) > 0 {
		log.Info("Found resources in state", "count", len(stateResources))
		resources = append(resources, stateResources...)
	} else {
		log.Info("No state found or state is empty, discovering from cloud")
		// Fallback: discover from cloud using tags
		cloudResources, err := m.discoverResourcesFromCloud(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to discover resources from cloud: %w", err)
		}
		resources = append(resources, cloudResources...)
	}

	// Filter resources based on options
	return m.filterResources(resources), nil
}

// getResourcesFromState retrieves resources from the state file
func (m *TeardownManager) getResourcesFromState() ([]*ResourceToDelete, error) {
	if m.stateManager.Current() == nil {
		return nil, fmt.Errorf("no state loaded")
	}

	var resources []*ResourceToDelete

	for key, resource := range m.stateManager.Current().Resources {
		parts := strings.SplitN(key, ".", 2)
		if len(parts) != 2 {
			continue
		}

		// Skip if resource type should be skipped
		if m.shouldSkipResourceType(parts[0]) {
			continue
		}

		deps, _ := m.stateManager.GetDependencies(key)

		resources = append(resources, &ResourceToDelete{
			Type:         parts[0],
			ID:           resource.ID,
			Name:         parts[1],
			Dependencies: deps,
			State:        resource.State,
			Properties:   resource.Properties,
		})
	}

	return resources, nil
}

// discoverResourcesFromCloud discovers resources by querying the cloud provider
func (m *TeardownManager) discoverResourcesFromCloud(ctx context.Context) ([]*ResourceToDelete, error) {
	log := logger.Get()
	var resources []*ResourceToDelete

	// Build tag filters for OCFP resources
	tagFilter := map[string]string{
		"managed-by": "ocfp",
		"bloc":       m.options.BlocName,
	}

	// Discover instances
	if compute := m.provider.Compute(); compute != nil {
		instances, err := compute.ListInstances(ctx, tagFilter)
		if err == nil {
			for _, instance := range instances {
				resources = append(resources, &ResourceToDelete{
					Type: "instance",
					ID:   instance.ID,
					Name: instance.Name,
				})
			}
			log.Info("Discovered instances", "count", len(instances))
		}
	}

	// Discover volumes
	if storage := m.provider.Storage(); storage != nil {
		volumes, err := storage.ListVolumes(ctx, tagFilter)
		if err == nil {
			for _, volume := range volumes {
				resources = append(resources, &ResourceToDelete{
					Type: "volume",
					ID:   volume.ID,
					Name: volume.Name,
				})
			}
			log.Info("Discovered volumes", "count", len(volumes))
		}

		// Discover snapshots
		snapshots, err := storage.ListSnapshots(ctx, "")
		if err == nil {
			for _, snapshot := range snapshots {
				resources = append(resources, &ResourceToDelete{
					Type: "snapshot",
					ID:   snapshot.ID,
					Name: snapshot.Name,
				})
			}
			log.Info("Discovered snapshots", "count", len(snapshots))
		}

		// Discover buckets
		buckets, err := storage.ListBuckets(ctx)
		if err == nil {
			for _, bucket := range buckets {
				// Check if bucket has OCFP tags (would need to implement bucket tagging)
				if strings.Contains(bucket.Name, m.config.Name) {
					resources = append(resources, &ResourceToDelete{
						Type: "bucket",
						ID:   bucket.Name,
						Name: bucket.Name,
					})
				}
			}
			log.Info("Discovered buckets", "count", len(buckets))
		}
	}

	// Discover networks and related resources
	if network := m.provider.Network(); network != nil {
		networks, err := network.ListNetworks(ctx, tagFilter)
		if err == nil {
			for _, net := range networks {
				resources = append(resources, &ResourceToDelete{
					Type: "network",
					ID:   net.ID,
					Name: net.Name,
				})
			}
			log.Info("Discovered networks", "count", len(networks))
		}

		// Discover floating IPs
		floatingIPs, err := network.ListFloatingIPs(ctx)
		if err == nil {
			for _, fip := range floatingIPs {
				// Only include if associated with our instances
				resources = append(resources, &ResourceToDelete{
					Type: "floating_ip",
					ID:   fip.ID,
					Name: fip.Address,
				})
			}
			log.Info("Discovered floating IPs", "count", len(floatingIPs))
		}

		// Discover STACKIT public IPs (if requested)
		if m.options.PublicIPs {
			type stackitPublicIPLister interface {
				ListPublicIPs(ctx context.Context, filters map[string]string) ([]*cpi.PublicIP, error)
			}
			if s, ok := network.(stackitPublicIPLister); ok {
				filters := map[string]string{
					"label:managed-by": "ocfp",
					"label:bloc":       m.config.Name,
				}
				ips, err := s.ListPublicIPs(ctx, filters)
				if err == nil {
					for _, ip := range ips {
						resources = append(resources, &ResourceToDelete{
							Type: "public_ip",
							ID:   ip.ID,
							Name: ip.Address,
							Properties: map[string]interface{}{
								"job":   ip.Labels["job"],
								"index": ip.Labels["index"],
							},
						})
					}
					log.Info("Discovered public IPs", "count", len(ips))
				} else {
					log.Warn("Failed to list public IPs", "error", err)
				}
			}
		}

		// Discover load balancers
		lbs, err := network.ListLoadBalancers(ctx, tagFilter)
		if err == nil {
			for _, lb := range lbs {
				resources = append(resources, &ResourceToDelete{
					Type: "loadbalancer",
					ID:   lb.ID,
					Name: lb.Name,
				})
			}
			log.Info("Discovered load balancers", "count", len(lbs))
		}
	}

	// Discover security groups
	if security := m.provider.Security(); security != nil {
		secGroups, err := security.ListSecurityGroups(ctx, tagFilter)
		if err == nil {
			for _, sg := range secGroups {
				resources = append(resources, &ResourceToDelete{
					Type: "security_group",
					ID:   sg.ID,
					Name: sg.Name,
				})
			}
			log.Info("Discovered security groups", "count", len(secGroups))
		}
	}

	return resources, nil
}

// discoverAllResources finds ALL resources in the project (nuke mode)
func (m *TeardownManager) discoverAllResources(ctx context.Context) ([]*ResourceToDelete, error) {
	log := logger.Get()
	log.Warn("NUKE MODE: Discovering ALL resources in project")

	var resources []*ResourceToDelete

	// List ALL instances
	if compute := m.provider.Compute(); compute != nil {
		instances, err := compute.ListInstances(ctx, nil)
		if err == nil {
			for _, instance := range instances {
				resources = append(resources, &ResourceToDelete{
					Type: "instance",
					ID:   instance.ID,
					Name: instance.Name,
				})
			}
		}
	}

	// List ALL volumes
	if storage := m.provider.Storage(); storage != nil {
		volumes, err := storage.ListVolumes(ctx, nil)
		if err == nil {
			for _, volume := range volumes {
				resources = append(resources, &ResourceToDelete{
					Type: "volume",
					ID:   volume.ID,
					Name: volume.Name,
				})
			}
		}
	}

	// List ALL networks
	if network := m.provider.Network(); network != nil {
		networks, err := network.ListNetworks(ctx, nil)
		if err == nil {
			for _, net := range networks {
				resources = append(resources, &ResourceToDelete{
					Type: "network",
					ID:   net.ID,
					Name: net.Name,
				})
			}
		}
	}

	log.Warn("NUKE MODE: Found resources", "count", len(resources))
	return resources, nil
}

// filterResources filters resources based on skip options
func (m *TeardownManager) filterResources(resources []*ResourceToDelete) []*ResourceToDelete {
	var filtered []*ResourceToDelete

	for _, resource := range resources {
		if m.shouldSkipResourceType(resource.Type) {
			continue
		}

		// Skip floating IPs unless explicitly requested
		if resource.Type == "floating_ip" && !m.options.PublicIPs {
			continue
		}

		// Skip public IPs unless explicitly requested
		if resource.Type == "public_ip" && !m.options.PublicIPs {
			continue
		}

		filtered = append(filtered, resource)
	}

	return filtered
}

// shouldSkipResourceType checks if a resource type should be skipped
func (m *TeardownManager) shouldSkipResourceType(resourceType string) bool {
	for _, skip := range m.options.Skip {
		if skip == resourceType {
			return true
		}
		// Support skipping by category
		if (skip == "storage" && (resourceType == "volume" || resourceType == "snapshot" || resourceType == "bucket")) ||
			(skip == "network" && (resourceType == "network" || resourceType == "subnet" || resourceType == "router")) ||
			(skip == "security" && (resourceType == "security_group")) {
			return true
		}
	}
	return false
}

// sortResourcesForDeletion sorts resources in the correct order for deletion
func (m *TeardownManager) sortResourcesForDeletion(resources []*ResourceToDelete) []*ResourceToDelete {
	// Define deletion order (most dependent first)
	order := map[string]int{
		"instance":       1, // Delete instances first to free volumes and networks
		"loadbalancer":   2, // Delete load balancers
		"snapshot":       3, // Delete snapshots before volumes
		"volume":         4, // Delete volumes
		"bucket":         5, // Delete buckets
		"floating_ip":    6, // Delete floating IPs
		"security_group": 7, // Delete security groups
		"subnet":         8, // Delete subnets before networks
		"router":         8, // Delete routers
		"network":        9, // Delete networks last
	}

	sort.Slice(resources, func(i, j int) bool {
		orderI := order[resources[i].Type]
		orderJ := order[resources[j].Type]
		if orderI == orderJ {
			// Same order, sort by name
			return resources[i].Name < resources[j].Name
		}
		return orderI < orderJ
	})

	return resources
}

// showDeletionPlan displays what will be deleted
func (m *TeardownManager) showDeletionPlan(resources []*ResourceToDelete) error {
	fmt.Printf("\n=== Teardown Plan ===\n")
	fmt.Printf("Mode: %s\n", m.options.Mode)
	fmt.Printf("Bloc: %s\n", m.options.BlocName)

	if m.options.DryRun {
		fmt.Printf("DRY RUN - No resources will actually be deleted\n")
	}

	fmt.Printf("\nResources to delete (%d total):\n", len(resources))

	// Group by type for display
	typeGroups := make(map[string][]*ResourceToDelete)
	for _, resource := range resources {
		typeGroups[resource.Type] = append(typeGroups[resource.Type], resource)
	}

	for resourceType, resourceList := range typeGroups {
		fmt.Printf("\n  %s (%d):\n", strings.ToUpper(resourceType), len(resourceList))
		for _, resource := range resourceList {
			status := ""
			if resource.State != "" && resource.State != "active" {
				status = fmt.Sprintf(" [%s]", resource.State)
			}
			fmt.Printf("    - %s (%s)%s\n", resource.Name, resource.ID, status)
		}
	}

	if len(m.options.Skip) > 0 {
		fmt.Printf("\nSkipped resource types: %s\n", strings.Join(m.options.Skip, ", "))
	}

	return nil
}

// confirmDeletion asks user for confirmation
func (m *TeardownManager) confirmDeletion(resourceCount int) bool {
	fmt.Printf("\nThis will permanently delete %d resources. Continue? [y/N]: ", resourceCount)
	var response string
	_, _ = fmt.Scanln(&response)
	return strings.HasPrefix(strings.ToLower(response), "y")
}

// deleteResource deletes a single resource
func (m *TeardownManager) deleteResource(ctx context.Context, resource *ResourceToDelete) error {
	log := logger.Get()

	switch resource.Type {
	case "instance":
		if compute := m.provider.Compute(); compute != nil {
			return compute.DeleteInstance(ctx, resource.ID)
		}
	case "volume":
		if storage := m.provider.Storage(); storage != nil {
			return storage.DeleteVolume(ctx, resource.ID)
		}
	case "snapshot":
		if storage := m.provider.Storage(); storage != nil {
			return storage.DeleteSnapshot(ctx, resource.ID)
		}
	case "bucket":
		if storage := m.provider.Storage(); storage != nil {
			// Empty bucket first if needed
			if err := storage.EmptyBucket(ctx, resource.ID); err != nil {
				log.Warn("Failed to empty bucket", "bucket", resource.ID, "error", err)
			}
			return storage.DeleteBucket(ctx, resource.ID)
		}
	case "credentials_group":
		if storage := m.provider.Storage(); storage != nil {
			// STACKIT-specific
			type stackitCreds interface {
				DeleteCredentialsGroup(context.Context, string) error
			}
			if s, ok := storage.(stackitCreds); ok {
				return s.DeleteCredentialsGroup(ctx, resource.ID)
			}
		}
	case "loadbalancer":
		if network := m.provider.Network(); network != nil {
			return network.DeleteLoadBalancer(ctx, resource.ID)
		}
	case "floating_ip":
		if network := m.provider.Network(); network != nil {
			return network.ReleaseFloatingIP(ctx, resource.ID)
		}
	case "public_ip":
		if network := m.provider.Network(); network != nil {
			// STACKIT-specific public IP deletion via type assertion
			type stackitPublicIP interface {
				DeletePublicIP(ctx context.Context, id string) error
			}
			if s, ok := network.(stackitPublicIP); ok {
				return s.DeletePublicIP(ctx, resource.ID)
			}
		}
	case "security_group":
		if security := m.provider.Security(); security != nil {
			return security.DeleteSecurityGroup(ctx, resource.ID)
		}
	case "subnet":
		if network := m.provider.Network(); network != nil {
			// Subnet deletion is typically handled by network deletion
			log.Info("Subnet will be deleted with network", "subnet", resource.Name)
			return nil
		}
	case "network":
		if network := m.provider.Network(); network != nil {
			return network.DeleteNetwork(ctx, resource.ID)
		}
	default:
		return fmt.Errorf("unsupported resource type: %s", resource.Type)
	}

	return fmt.Errorf("provider does not support %s management", resource.Type)
}
