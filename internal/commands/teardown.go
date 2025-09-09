package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	// Resource discovery buffer sizes.
	InitialResourcesBufferSize   = 16 // Initial buffer for discovered resources
	CloudResourcesBufferSize     = 32 // Buffer for cloud resource discovery
	NukeModeResourcesBufferSize  = 64 // Buffer for nuke mode resources
	StateResourcesBufferInitSize = 0  // Dynamic sizing based on state resources length

	// String split expectations.
	ExpectedResourceKeyParts = 2 // Expected parts when splitting resource keys

	// Teardown order priorities.
	LoadBalancerPriority  = 2 // Delete load balancers
	SnapshotPriority      = 3 // Delete snapshots before volumes
	VolumePriority        = 4 // Delete volumes
	BucketPriority        = 5 // Delete buckets
	FloatingIPPriority    = 6 // Delete floating IPs
	SecurityGroupPriority = 7 // Delete security groups
	SubnetRouterPriority  = 8 // Delete subnets and routers
	NetworkPriority       = 9 // Delete networks last
)

var (
	ErrTeardownCancelled = errors.New("teardown cancelled by user")
)

// NewTeardownCmd creates the teardown command.
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
		output    string
	)

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:     "teardown",
		Short:   "Remove all resources created by bootstrap",
		Long:    getTeardownLongDescription(),
		Example: getTeardownExamples(),
		RunE:    runTeardown,
	}

	addTeardownFlags(cmd, &force, &dryRun, &skip, &publicIPs, &all, &nuke, &servers, &volumes, &snapshots, &buckets, &secGroups, &network, &output)
	bindTeardownViperFlags(cmd)

	return cmd
}

func getTeardownLongDescription() string {
	return `Teardown removes resources created by OCFP bootstrap and BOSH deployments.

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
8. Public IPs (only if --public-ips flag is used)`
}

func getTeardownExamples() string {
	return `  # Interactive teardown (with confirmation)
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
  ocfp teardown --bloc production --nuke --force`
}

func addTeardownFlags(cmd *cobra.Command, force, dryRun *bool, skip *[]string, publicIPs, all, nuke, servers, volumes, snapshots, buckets, secGroups, network *bool, output *string) {
	cmd.Flags().BoolVar(force, "force", false, "skip confirmation prompts")
	cmd.Flags().BoolVar(dryRun, "dry-run", false, "preview what would be deleted without actually deleting")
	cmd.Flags().StringSliceVar(skip, "skip", []string{}, "skip deletion of specific resource types")
	cmd.Flags().BoolVar(publicIPs, "public-ips", false, "include public IPs in deletion")
	cmd.Flags().BoolVar(all, "all", false, "delete all OCFP/BOSH-managed resources")
	cmd.Flags().BoolVar(nuke, "nuke", false, "DANGER: delete ALL resources in project")
	cmd.Flags().StringVar(output, "output", OutputTable, "output format: table|json|yaml (for dry-run plan)")

	cmd.Flags().BoolVar(servers, "servers", false, "delete servers")
	cmd.Flags().BoolVar(volumes, "volumes", false, "delete volumes")
	cmd.Flags().BoolVar(snapshots, "snapshots", false, "delete snapshots")
	cmd.Flags().BoolVar(buckets, "buckets", false, "delete buckets")
	cmd.Flags().BoolVar(secGroups, "security-groups", false, "delete security groups")
	cmd.Flags().BoolVar(network, "network", false, "delete networks")
}

func bindTeardownViperFlags(cmd *cobra.Command) {
	_ = viper.BindPFlag("teardown.force", cmd.Flags().Lookup("force"))
	_ = viper.BindPFlag("teardown.dry_run", cmd.Flags().Lookup("dry-run"))
	_ = viper.BindPFlag("teardown.skip", cmd.Flags().Lookup("skip"))
	_ = viper.BindPFlag("teardown.public_ips", cmd.Flags().Lookup("public-ips"))
	_ = viper.BindPFlag("teardown.all", cmd.Flags().Lookup("all"))
	_ = viper.BindPFlag("teardown.nuke", cmd.Flags().Lookup("nuke"))
	_ = viper.BindPFlag("teardown.output", cmd.Flags().Lookup("output"))
}

func runTeardown(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	teardownConfig := getTeardownConfig()

	log, err := initializeTeardownLogger(teardownConfig.BlocName)
	if err != nil {
		return err
	}

	defer func() { _ = logger.Sync() }()

	err = validateTeardownConfig(teardownConfig)
	if err != nil {
		return err
	}

	cfg, provider, err := setupTeardownProvider(ctx, teardownConfig)
	if err != nil {
		return err
	}

	defer func() { _ = provider.Cleanup(ctx) }()

	stateManager, err := setupTeardownState(teardownConfig.BlocName, log)
	if err != nil {
		return err
	}

	teardownManager := createTeardownManager(cfg, provider, stateManager, teardownConfig)
	log.Info("Starting teardown", "mode", teardownConfig.Mode, "bloc", teardownConfig.BlocName)

	err = teardownManager.Execute(ctx)
	if err != nil {
		return fmt.Errorf("teardown failed: %w", err)
	}

	logger.Get().Info("✅ Teardown completed successfully!")

	return nil
}

type teardownConfig struct {
	BlocName   string
	ConfigFile string
	Force      bool
	DryRun     bool
	Nuke       bool
	All        bool
	PublicIPs  bool
	Skip       []string
	Mode       string
	Output     string
}

func getTeardownConfig() *teardownConfig {
	all := viper.GetBool("teardown.all")
	nuke := viper.GetBool("teardown.nuke")

	return &teardownConfig{
		BlocName:   viper.GetString("bloc_name"),
		ConfigFile: viper.GetString("config"),
		Force:      viper.GetBool("teardown.force"),
		DryRun:     viper.GetBool("teardown.dry_run"),
		Nuke:       nuke,
		All:        all,
		PublicIPs:  viper.GetBool("teardown.public_ips"),
		Skip:       viper.GetStringSlice("teardown.skip"),
		Mode:       getTeardownMode(all, nuke),
		Output:     viper.GetString("teardown.output"),
	}
}

func initializeTeardownLogger(blocName string) (logger.Logger, error) {
	logDir := filepath.Join(os.Getenv("HOME"), ".ocfp", "log")

	err := logger.Initialize(logger.Config{
		Level:     viper.GetString("log_level"),
		Debug:     viper.GetBool("debug"),
		Verbose:   viper.GetBool("verbose"),
		Trace:     viper.GetBool("trace"),
		NoLog:     viper.GetBool("no_log"),
		LogDir:    logDir,
		BlocName:  blocName,
		Command:   "teardown",
		RequestID: os.Getenv("OCFP_REQUEST_ID"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize logger: %w", err)
	}

	return logger.Get(), nil
}

func validateTeardownConfig(cfg *teardownConfig) error {
	if cfg.BlocName == "" {
		return ErrBlocIsRequired
	}

	if cfg.Nuke && !cfg.Force {
		return ErrNukeRequiresForceForSafety
	}

	return nil
}

//nolint:ireturn // Returns interface by design for provider abstraction
func setupTeardownProvider(ctx context.Context, teardownCfg *teardownConfig) (*config.Config, cpi.Provider, error) {
	cfg, err := config.LoadWithParams(teardownCfg.ConfigFile, teardownCfg.BlocName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load config: %w", err)
	}

	provider, err := cpi.GetProvider(cfg.Provider)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get provider: %w", err)
	}

	err = provider.Initialize(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize provider: %w", err)
	}

	return cfg, provider, nil
}

func setupTeardownState(blocName string, log logger.Logger) (*state.Manager, error) {
	stateManager, err := state.NewManager("")
	if err != nil {
		return nil, fmt.Errorf("failed to create state manager: %w", err)
	}

	_, err = stateManager.Load(blocName)
	if err != nil {
		log.Warn("Failed to load state, will discover resources from cloud", "error", err)
	}

	return stateManager, nil
}

func createTeardownManager(cfg *config.Config, provider cpi.Provider, stateManager *state.Manager, teardownCfg *teardownConfig) *TeardownManager {
	teardownOpts := &TeardownOptions{
		BlocName:  teardownCfg.BlocName,
		Provider:  cfg.Provider,
		Force:     teardownCfg.Force,
		DryRun:    teardownCfg.DryRun,
		All:       teardownCfg.All,
		Nuke:      teardownCfg.Nuke,
		PublicIPs: teardownCfg.PublicIPs,
		Skip:      teardownCfg.Skip,
		Mode:      teardownCfg.Mode,
		Output:    teardownCfg.Output,
	}

	return NewTeardownManager(cfg, provider, stateManager, teardownOpts)
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

// TeardownOptions represents teardown configuration.
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
	Output    string
}

// TeardownManager handles the teardown process.
type TeardownManager struct {
	config       *config.Config
	provider     cpi.Provider
	stateManager *state.Manager
	options      *TeardownOptions
}

// NewTeardownManager creates a new teardown manager.
func NewTeardownManager(cfg *config.Config, provider cpi.Provider, stateManager *state.Manager, opts *TeardownOptions) *TeardownManager {
	return &TeardownManager{
		config:       cfg,
		provider:     provider,
		stateManager: stateManager,
		options:      opts,
	}
}

// Execute performs the teardown process.
func (m *TeardownManager) Execute(ctx context.Context) error {
	log := logger.Get()

	err := m.stateManager.Lock(m.options.BlocName)
	if err != nil {
		return fmt.Errorf("failed to acquire state lock: %w", err)
	}

	defer func() { _ = m.stateManager.Unlock(m.options.BlocName) }()

	sortedResources, err := m.prepareResourcesForDeletion(ctx)
	if err != nil {
		return err
	}

	if len(sortedResources) == 0 {
		log.Info("No resources found to delete")

		return nil
	}

	err = m.handleDeletionPlan(sortedResources, log)
	if err != nil {
		return err
	}

	if m.options.DryRun {
		log.Info("Dry run completed - no resources were deleted")

		return nil
	}

	deletedCount := m.executeResourceDeletion(ctx, sortedResources, log)
	log.Info("Teardown completed", "deleted", deletedCount, "total", len(sortedResources))

	return nil
}

// DeleteResource deletes a single resource.
func (m *TeardownManager) DeleteResource(ctx context.Context, resource *ResourceToDelete) error {
	switch resource.Type {
	case "instance":
		return m.deleteComputeResource(ctx, resource)
	case ResourceVolume, ResourceSnapshot, "bucket", "credentials_group":
		return m.deleteStorageResource(ctx, resource)
	case "loadbalancer", "floating_ip", "public_ip", "subnet", "network":
		return m.deleteNetworkResource(ctx, resource)
	case "security_group":
		return m.deleteSecurityResource(ctx, resource)
	default:
		return ErrUnsupportedResourceType(resource.Type)
	}
}

func (m *TeardownManager) prepareResourcesForDeletion(ctx context.Context) ([]*ResourceToDelete, error) {
	resourcesToDelete, err := m.discoverResources(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to discover resources: %w", err)
	}

	return m.sortResourcesForDeletion(resourcesToDelete), nil
}

func (m *TeardownManager) handleDeletionPlan(sortedResources []*ResourceToDelete, log logger.Logger) error {
	err := m.showDeletionPlan(sortedResources)
	if err != nil {
		return fmt.Errorf("failed to show deletion plan: %w", err)
	}

	if !m.options.Force && !m.options.DryRun {
		if !m.confirmDeletion(len(sortedResources)) {
			log.Info("Teardown cancelled by user")

			return ErrTeardownCancelled
		}
	}

	return nil
}

func (m *TeardownManager) executeResourceDeletion(ctx context.Context, sortedResources []*ResourceToDelete, log logger.Logger) int {
	deletedCount := 0

	for i, resource := range sortedResources {
		log.Info("Deleting resource", "type", resource.Type, "name", resource.Name, "progress", fmt.Sprintf("%d/%d", i+1, len(sortedResources)))

		err := m.DeleteResource(ctx, resource)
		if err != nil {
			log.Error("Failed to delete resource", "type", resource.Type, "name", resource.Name, "error", err)

			continue
		}

		deletedCount++

		m.updateStateAfterDeletion(resource, log)
	}

	return deletedCount
}

func (m *TeardownManager) updateStateAfterDeletion(resource *ResourceToDelete, log logger.Logger) {
	err := m.stateManager.RemoveResource(resource.Type, resource.Name)
	if err != nil {
		log.Warn("Failed to remove resource from state", "resource", resource.Name, "error", err)
	}

	err = m.stateManager.Save()
	if err != nil {
		log.Warn("Failed to save state", "error", err)
	}
}

// ResourceToDelete represents a resource marked for deletion.
type ResourceToDelete struct {
	Type         string
	ID           string
	Name         string
	Dependencies []string
	State        string
	Properties   map[string]interface{}
}

// discoverResources finds all resources that should be deleted.
func (m *TeardownManager) discoverResources(ctx context.Context) ([]*ResourceToDelete, error) {
	log := logger.Get()

	// Start with a small preallocation to reduce growth
	resources := make([]*ResourceToDelete, 0, InitialResourcesBufferSize)

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
		cloudResources := m.discoverResourcesFromCloud(ctx)

		resources = append(resources, cloudResources...)
	}

	// Filter resources based on options
	return m.filterResources(resources), nil
}

// getResourcesFromState retrieves resources from the state file.
func (m *TeardownManager) getResourcesFromState() ([]*ResourceToDelete, error) {
	if m.stateManager.Current() == nil {
		return nil, ErrNoStateLoaded
	}

	resources := make([]*ResourceToDelete, 0, len(m.stateManager.Current().Resources))

	for key, resource := range m.stateManager.Current().Resources {
		parts := strings.SplitN(key, ".", ExpectedResourceKeyParts)
		if len(parts) != ExpectedResourceKeyParts {
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

// discoverResourcesFromCloud discovers resources by querying the cloud provider.
func (m *TeardownManager) discoverResourcesFromCloud(ctx context.Context) []*ResourceToDelete {
	log := logger.Get()
	resources := make([]*ResourceToDelete, 0, CloudResourcesBufferSize)

	tagFilter := buildTagFilter(m.options.BlocName)

	// Discover compute resources
	m.discoverComputeResources(ctx, tagFilter, &resources, log)

	// Discover storage resources
	m.discoverStorageResources(ctx, tagFilter, &resources, log)

	// Discover network resources
	m.discoverNetworkResources(ctx, tagFilter, &resources, log)

	// Discover security resources
	m.discoverSecurityResources(ctx, tagFilter, &resources, log)

	return resources
}

func buildTagFilter(blocName string) map[string]string {
	return map[string]string{
		"managed-by": "ocfp",
		"bloc":       blocName,
	}
}

func (m *TeardownManager) discoverComputeResources(ctx context.Context, tagFilter map[string]string, resources *[]*ResourceToDelete, log logger.Logger) {
	compute := m.provider.Compute()
	if compute == nil {
		return
	}

	instances, err := compute.ListInstances(ctx, tagFilter)
	if err != nil {
		return
	}

	for _, instance := range instances {
		*resources = append(*resources, &ResourceToDelete{
			Type:         "instance",
			ID:           instance.ID,
			Name:         instance.Name,
			Dependencies: nil,
			State:        "",
			Properties:   nil,
		})
	}

	log.Info("Discovered instances", "count", len(instances))
}

func (m *TeardownManager) discoverStorageResources(ctx context.Context, tagFilter map[string]string, resources *[]*ResourceToDelete, log logger.Logger) {
	storage := m.provider.Storage()
	if storage == nil {
		return
	}

	// Discover volumes
	volumes, err := storage.ListVolumes(ctx, tagFilter)
	if err == nil {
		for _, volume := range volumes {
			*resources = append(*resources, &ResourceToDelete{
				Type:         ResourceVolume,
				ID:           volume.ID,
				Name:         volume.Name,
				Dependencies: nil,
				State:        "",
				Properties:   nil,
			})
		}

		log.Info("Discovered volumes", "count", len(volumes))
	}

	// Discover snapshots
	snapshots, err := storage.ListSnapshots(ctx, "")
	if err == nil {
		for _, snapshot := range snapshots {
			*resources = append(*resources, &ResourceToDelete{
				Type:         ResourceSnapshot,
				ID:           snapshot.ID,
				Name:         snapshot.Name,
				Dependencies: nil,
				State:        "",
				Properties:   nil,
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
				*resources = append(*resources, &ResourceToDelete{
					Type:         "bucket",
					ID:           bucket.Name,
					Name:         bucket.Name,
					Dependencies: nil,
					State:        "",
					Properties:   nil,
				})
			}
		}

		log.Info("Discovered buckets", "count", len(buckets))
	}
}

func (m *TeardownManager) discoverNetworkResources(ctx context.Context, tagFilter map[string]string, resources *[]*ResourceToDelete, log logger.Logger) {
	network := m.provider.Network()
	if network == nil {
		return
	}

	// Discover networks
	networks, err := network.ListNetworks(ctx, tagFilter)
	if err == nil {
		for _, net := range networks {
			*resources = append(*resources, &ResourceToDelete{
				Type:         "network",
				ID:           net.ID,
				Name:         net.Name,
				Dependencies: nil,
				State:        "",
				Properties:   nil,
			})
		}

		log.Info("Discovered networks", "count", len(networks))
	}

	// Discover floating IPs
	floatingIPs, err := network.ListFloatingIPs(ctx)
	if err == nil {
		for _, fip := range floatingIPs {
			*resources = append(*resources, &ResourceToDelete{
				Type:         "floating_ip",
				ID:           fip.ID,
				Name:         fip.Address,
				Dependencies: nil,
				State:        "",
				Properties:   nil,
			})
		}

		log.Info("Discovered floating IPs", "count", len(floatingIPs))
	}

	// Discover STACKIT public IPs if requested
	if m.options.PublicIPs {
		m.discoverStackitPublicIPs(ctx, network, resources, log)
	}

	// Discover load balancers
	lbs, err := network.ListLoadBalancers(ctx, tagFilter)
	if err == nil {
		for _, lb := range lbs {
			*resources = append(*resources, &ResourceToDelete{
				Type:         "loadbalancer",
				ID:           lb.ID,
				Name:         lb.Name,
				Dependencies: nil,
				State:        "",
				Properties:   nil,
			})
		}

		log.Info("Discovered load balancers", "count", len(lbs))
	}
}

func (m *TeardownManager) discoverStackitPublicIPs(ctx context.Context, network cpi.NetworkManager, resources *[]*ResourceToDelete, log logger.Logger) {
	type stackitPublicIPLister interface {
		ListPublicIPsWithFilters(ctx context.Context, filters map[string]string) ([]*cpi.PublicIP, error)
	}

	stackitPublicIPList, ok := network.(stackitPublicIPLister)
	if !ok {
		return
	}

	filters := map[string]string{
		"label:managed-by": "ocfp",
		"label:bloc":       m.config.Name,
	}

	ips, err := stackitPublicIPList.ListPublicIPsWithFilters(ctx, filters)
	if err != nil {
		log.Warn("Failed to list public IPs", "error", err)

		return
	}

	for _, publicIP := range ips {
		*resources = append(*resources, &ResourceToDelete{
			Type:         "public_ip",
			ID:           publicIP.ID,
			Name:         publicIP.Address,
			Dependencies: nil,
			State:        "",
			Properties: map[string]interface{}{
				"job":   publicIP.Labels["job"],
				"index": publicIP.Labels["index"],
			},
		})
	}

	log.Info("Discovered public IPs", "count", len(ips))
}

func (m *TeardownManager) discoverSecurityResources(ctx context.Context, tagFilter map[string]string, resources *[]*ResourceToDelete, log logger.Logger) {
	security := m.provider.Security()
	if security == nil {
		return
	}

	secGroups, err := security.ListSecurityGroups(ctx, tagFilter)
	if err != nil {
		return
	}

	for _, sg := range secGroups {
		*resources = append(*resources, &ResourceToDelete{
			Type:         "security_group",
			ID:           sg.ID,
			Name:         sg.Name,
			Dependencies: nil,
			State:        "",
			Properties:   nil,
		})
	}

	log.Info("Discovered security groups", "count", len(secGroups))
}

// discoverAllResources finds ALL resources in the project (nuke mode).
func (m *TeardownManager) discoverAllResources(ctx context.Context) ([]*ResourceToDelete, error) {
	log := logger.Get()
	log.Warn("NUKE MODE: Discovering ALL resources in project")

	resources := make([]*ResourceToDelete, 0, NukeModeResourcesBufferSize)

	// List ALL instances
	if compute := m.provider.Compute(); compute != nil {
		instances, err := compute.ListInstances(ctx, nil)
		if err == nil {
			for _, instance := range instances {
				resources = append(resources, &ResourceToDelete{
					Type:         "instance",
					ID:           instance.ID,
					Name:         instance.Name,
					Dependencies: nil,
					State:        "",
					Properties:   nil,
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
					Type:         ResourceVolume,
					ID:           volume.ID,
					Name:         volume.Name,
					Dependencies: nil,
					State:        "",
					Properties:   nil,
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
					Type:         "network",
					ID:           net.ID,
					Name:         net.Name,
					Dependencies: nil,
					State:        "",
					Properties:   nil,
				})
			}
		}
	}

	log.Warn("NUKE MODE: Found resources", "count", len(resources))

	return resources, nil
}

// filterResources filters resources based on skip options.
func (m *TeardownManager) filterResources(resources []*ResourceToDelete) []*ResourceToDelete {
	filtered := make([]*ResourceToDelete, 0, len(resources))

	for _, resource := range resources {
		if m.shouldSkipResourceType(resource.Type) {
			continue
		}

		// Skip floating IPs unless explicitly requested
		if resource.Type == ResourceFloatingIP && !m.options.PublicIPs {
			continue
		}

		// Skip public IPs unless explicitly requested
		if resource.Type == ResourcePublicIP && !m.options.PublicIPs {
			continue
		}

		filtered = append(filtered, resource)
	}

	return filtered
}

// shouldSkipResourceType checks if a resource type should be skipped.
func (m *TeardownManager) shouldSkipResourceType(resourceType string) bool {
	for _, skip := range m.options.Skip {
		if skip == resourceType {
			return true
		}
		// Support skipping by category
		if (skip == "storage" && (resourceType == ResourceVolume || resourceType == ResourceSnapshot || resourceType == ResourceBucket)) ||
			(skip == CategoryNetwork && (resourceType == CategoryNetwork || resourceType == ResourceSubnet || resourceType == "router")) ||
			(skip == "security" && (resourceType == "security_group")) {
			return true
		}
	}

	return false
}

// sortResourcesForDeletion sorts resources in the correct order for deletion.
func (m *TeardownManager) sortResourcesForDeletion(resources []*ResourceToDelete) []*ResourceToDelete {
	// Define deletion order (most dependent first)
	order := map[string]int{
		"instance":       1,                     // Delete instances first to free volumes and networks
		"loadbalancer":   LoadBalancerPriority,  // Delete load balancers
		ResourceSnapshot: SnapshotPriority,      // Delete snapshots before volumes
		ResourceVolume:   VolumePriority,        // Delete volumes
		"bucket":         BucketPriority,        // Delete buckets
		"floating_ip":    FloatingIPPriority,    // Delete floating IPs
		"security_group": SecurityGroupPriority, // Delete security groups
		"subnet":         SubnetRouterPriority,  // Delete subnets before networks
		"router":         SubnetRouterPriority,  // Delete routers
		"network":        NetworkPriority,       // Delete networks last
	}

	sort.Slice(resources, func(first, second int) bool {
		orderI := order[resources[first].Type]

		orderJ := order[resources[second].Type]
		if orderI == orderJ {
			// Same order, sort by name
			return resources[first].Name < resources[second].Name
		}

		return orderI < orderJ
	})

	return resources
}

// showDeletionPlan displays what will be deleted.
func (m *TeardownManager) showDeletionPlan(resources []*ResourceToDelete) error {
	// Group by type
	typeGroups := make(map[string][]*ResourceToDelete)
	types := []string{}

	for _, r := range resources {
		if _, ok := typeGroups[r.Type]; !ok {
			types = append(types, r.Type)
		}

		typeGroups[r.Type] = append(typeGroups[r.Type], r)
	}

	sort.Strings(types)

	// Build plan table
	title := fmt.Sprintf("DRY RUN — Teardown Plan for bloc '%s' (%s)", m.options.BlocName, m.options.Mode)
	summary := fmt.Sprintf("Delete %d resources across %d types", len(resources), len(types))
	planTable := &ui.Table{
		Title:    title,
		Summary:  summary,
		Sections: nil,
	}

	for _, typ := range types {
		list := typeGroups[typ]
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })

		rows := make([][]string, 0, len(list))
		for _, r := range list {
			state := r.State
			rows = append(rows, []string{r.Name, r.ID, state})
		}

		planTable.Sections = append(planTable.Sections, ui.Section{
			Title:   fmt.Sprintf("%s (%d)", strings.ToUpper(typ), len(list)),
			Headers: []string{"NAME", "ID", "STATE"},
			Rows:    rows,
		})
	}

	if len(m.options.Skip) > 0 {
		planTable.Sections = append(planTable.Sections, ui.Section{
			Title:   "Skipped",
			Headers: []string{"TYPES"},
			Rows:    [][]string{{strings.Join(m.options.Skip, ", ")}},
		})
	}

	format := strings.ToLower(strings.TrimSpace(m.options.Output))
	if format == "" {
		format = OutputTable
	}

	err := ui.Render(planTable, format)
	if err != nil {
		return fmt.Errorf("failed to render teardown plan: %w", err)
	}

	return nil
}

// confirmDeletion asks user for confirmation.
func (m *TeardownManager) confirmDeletion(resourceCount int) bool {
	_, err := fmt.Fprintf(os.Stdout, "\nThis will permanently delete %d resources. Continue? [y/N]: ", resourceCount)
	if err != nil {
		logger.Get().Error(fmt.Sprintf("Failed to write confirmation prompt: %v", err))

		return false
	}

	var response string

	_, _ = fmt.Scanln(&response)

	return strings.HasPrefix(strings.ToLower(response), "y")
}

func (m *TeardownManager) deleteComputeResource(ctx context.Context, resource *ResourceToDelete) error {
	compute := m.provider.Compute()
	if compute == nil {
		return ErrProviderDoesNotSupportComputeMgmt
	}

	return fmt.Errorf("failed to delete instance %s: %w", resource.ID, compute.DeleteInstance(ctx, resource.ID))
}

func (m *TeardownManager) deleteStorageResource(ctx context.Context, resource *ResourceToDelete) error {
	storage := m.provider.Storage()
	if storage == nil {
		return ErrProviderDoesNotSupportStorageMgmt
	}

	switch resource.Type {
	case ResourceVolume:
		return fmt.Errorf("failed to delete volume %s: %w", resource.ID, storage.DeleteVolume(ctx, resource.ID))
	case ResourceSnapshot:
		return fmt.Errorf("failed to delete snapshot %s: %w", resource.ID, storage.DeleteSnapshot(ctx, resource.ID))
	case "bucket":
		return m.deleteBucket(ctx, storage, resource)
	case "credentials_group":
		return m.deleteCredentialsGroup(ctx, storage, resource)
	default:
		return ErrUnsupportedStorageResourceType(resource.Type)
	}
}

func (m *TeardownManager) deleteBucket(ctx context.Context, storage cpi.StorageManager, resource *ResourceToDelete) error {
	log := logger.Get()

	// Empty bucket first if needed
	err := storage.EmptyBucket(ctx, resource.ID)
	if err != nil {
		log.Warn("Failed to empty bucket", "bucket", resource.ID, "error", err)
	}

	return fmt.Errorf("failed to delete bucket %s: %w", resource.ID, storage.DeleteBucket(ctx, resource.ID))
}

func (m *TeardownManager) deleteCredentialsGroup(ctx context.Context, storage cpi.StorageManager, resource *ResourceToDelete) error {
	// STACKIT-specific
	type stackitCreds interface {
		DeleteCredentialsGroup(ctx context.Context, id string) error
	}

	s, ok := storage.(stackitCreds)
	if !ok {
		return ErrProviderDoesNotSupportCredGroupDeletion
	}

	return fmt.Errorf("failed to delete credentials group %s: %w", resource.ID, s.DeleteCredentialsGroup(ctx, resource.ID))
}

func (m *TeardownManager) deleteNetworkResource(ctx context.Context, resource *ResourceToDelete) error {
	network := m.provider.Network()
	if network == nil {
		return ErrProviderDoesNotSupportNetworkMgmt
	}

	switch resource.Type {
	case "loadbalancer":
		return fmt.Errorf("failed to delete load balancer %s: %w", resource.ID, network.DeleteLoadBalancer(ctx, resource.ID))
	case "floating_ip":
		return fmt.Errorf("failed to release floating IP %s: %w", resource.ID, network.ReleaseFloatingIP(ctx, resource.ID))
	case "public_ip":
		return m.deletePublicIP(ctx, network, resource)
	case "subnet":
		return m.deleteSubnet(resource)
	case "network":
		return fmt.Errorf("failed to delete network %s: %w", resource.ID, network.DeleteNetwork(ctx, resource.ID))
	default:
		return ErrUnsupportedNetworkResourceType(resource.Type)
	}
}

func (m *TeardownManager) deletePublicIP(ctx context.Context, network cpi.NetworkManager, resource *ResourceToDelete) error {
	// STACKIT-specific public IP deletion via type assertion
	type stackitPublicIP interface {
		DeletePublicIP(ctx context.Context, id string) error
	}

	s, ok := network.(stackitPublicIP)
	if !ok {
		return ErrProviderDoesNotSupportPublicIPDeletion
	}

	return fmt.Errorf("failed to delete public IP %s: %w", resource.ID, s.DeletePublicIP(ctx, resource.ID))
}

func (m *TeardownManager) deleteSubnet(resource *ResourceToDelete) error {
	log := logger.Get()
	// Subnet deletion is typically handled by network deletion
	log.Info("Subnet will be deleted with network", "subnet", resource.Name)

	return nil
}

func (m *TeardownManager) deleteSecurityResource(ctx context.Context, resource *ResourceToDelete) error {
	security := m.provider.Security()
	if security == nil {
		return ErrProviderDoesNotSupportSecurityMgmt
	}

	return fmt.Errorf("failed to delete security group %s: %w", resource.ID, security.DeleteSecurityGroup(ctx, resource.ID))
}
