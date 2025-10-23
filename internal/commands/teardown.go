package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	// Resource discovery buffer sizes.
	InitialResourcesBufferSize   = 16 // Initial buffer for discovered resources
	CloudResourcesBufferSize     = 32 // Buffer for cloud resource discovery
	NukeModeResourcesBufferSize  = 64 // Buffer for nuke mode resources
	StateResourcesBufferInitSize = 0  // Dynamic sizing based on state resources length

	// String split expectations.
	ExpectedResourceKeyParts = 2 // Expected parts when splitting resource keys

	// Retry configuration for resource deletion.
	MaxRetryAttempts       = 3                // Maximum retry attempts for 409 conflicts
	InitialRetryDelay      = 5 * time.Second  // Initial retry delay
	MaxRetryDelay          = 30 * time.Second // Maximum retry delay
	RetryDelayMultiplier   = 2                // Exponential backoff multiplier
	ConflictErrorIndicator = "409"            // HTTP 409 Conflict indicator in error messages

	// Teardown order priorities (reverse of bootstrap order).
	// CRITICAL: Instances and NICs must be deleted BEFORE security groups to avoid 409 conflicts.
	LoadBalancerPriority     = 1  // Delete load balancers first (may reference instances)
	InstancePriority         = 2  // Delete instances early (to release volumes, NICs, security groups)
	NetworkInterfacePriority = 3  // Delete network interfaces (after instances, before security groups)
	BucketPriority           = 4  // Delete buckets (reverse of bootstrap step 8)
	SnapshotPriority         = 5  // Delete snapshots before volumes
	VolumePriority           = 6  // Delete volumes (reverse of bootstrap step 6)
	KeyPairPriority          = 7  // Delete key pairs (reverse of bootstrap step 5)
	FloatingIPPriority       = 8  // Delete floating/public IPs (reverse of bootstrap step 4)
	SecurityGroupPriority    = 9  // Delete security groups (after instances/NICs freed them)
	SubnetRouterPriority     = 10 // Delete subnets and routers (reverse of bootstrap step 2)
	NetworkPriority          = 11 // Delete networks last (reverse of bootstrap step 1)
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
		bastion   bool
		keypairs  bool
		empty     bool
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

	addTeardownFlags(cmd, &force, &dryRun, &skip, &publicIPs, &all, &nuke, &servers, &volumes, &snapshots, &buckets, &secGroups, &network, &bastion, &keypairs, &empty, &output)
	bindTeardownViperFlags(cmd)

	return cmd
}

func getTeardownLongDescription() string {
	return `Teardown removes resources created by OCFP bootstrap and BOSH deployments.

SAFETY: Teardown ONLY manages resources with proper OCFP metadata (managed-by=ocfp, bloc=<name>).
Resources without these required metadata tags are NOT in scope and will be ignored completely.
This strict metadata filtering prevents accidental deletion of resources from other environments
or manually created resources.

EXCEPTION: Keypairs are filtered by name pattern ({bloc-name}-keypair) since most cloud providers
don't support tags for keypairs.

The command supports different modes:
- Default: Delete only bootstrap-created resources
- Selective: Delete only specified resource types (--servers, --volumes, --snapshots, --buckets, --security-groups, --network)
- Bastion: Delete only the bastion instance (--bastion)
- All: Delete all OCFP/BOSH-managed resources (--all)
- Nuke: Delete ALL resources in the project (--nuke, requires --force)
  WARNING: Nuke mode bypasses bloc name filtering and deletes ALL resources

Selective mode allows you to specify one or more resource types to delete.
Multiple flags can be combined (e.g., --bastion --security-groups):
- --bastion: Delete only the bastion instance
- --servers: Delete compute instances and associated keypairs
- --volumes: Delete persistent volumes
- --snapshots: Delete volume snapshots
- --buckets: Delete storage buckets
- --security-groups: Delete security groups
- --network: Delete networks, subnets, routers, and load balancers

Bucket Deletion Behavior:
By default, non-empty buckets are skipped with a warning. Use the --empty flag to
automatically empty buckets before deletion. This prevents accidental data loss.
- Default (no --empty): Skip non-empty buckets with warning
- With --empty: Empty non-empty buckets before deletion

Resources are deleted in dependency order to avoid conflicts:
1. Load balancers
2. Servers (to free volumes, network interfaces, and security groups)
3. Network interfaces (after server detachment)
4. Buckets (checked for emptiness, emptied if --empty flag provided)
5. Snapshots (to allow volume deletion)
6. Volumes
7. Key pairs
8. Public IPs (only if --public-ips flag is used)
9. Security groups (after all attachments removed)
10. Subnets
11. Networks (last)`
}

func getTeardownExamples() string {
	return `  # Interactive teardown (with confirmation)
  ocfp teardown --bloc production

  # Force teardown (no confirmation)
  ocfp teardown --bloc production --force

  # Dry run to preview deletions
  ocfp teardown --bloc production --dry-run

  # Delete only the bastion instance
  ocfp teardown --bloc production --bastion

  # Delete bastion without confirmation
  ocfp teardown --bloc production --bastion --force

  # Delete bastion and security groups (flags can be combined)
  ocfp teardown --bloc production --bastion --security-groups

  # Delete only servers (instances and keypairs)
  ocfp teardown --bloc production --servers

  # Delete only SSH key pairs
  ocfp teardown --bloc production --key-pairs

  # Delete only volumes and snapshots
  ocfp teardown --bloc production --volumes --snapshots

  # Delete buckets (skips non-empty buckets with warning)
  ocfp teardown --bloc production --buckets

  # Delete buckets and empty non-empty ones before deletion
  ocfp teardown --bloc production --buckets --empty

  # Delete only network resources
  ocfp teardown --bloc production --network

  # Delete multiple specific resource types
  ocfp teardown --bloc production --servers --volumes --buckets

  # Delete network resources including public IPs
  ocfp teardown --bloc production --network --public-ips

  # Delete all resources (default behavior)
  ocfp teardown --bloc production --all

  # Delete all resources and empty non-empty buckets
  ocfp teardown --bloc production --all --empty

  # Skip specific resource types
  ocfp teardown --bloc production --skip network --skip storage

  # DANGER: Delete ALL resources in project
  ocfp teardown --bloc production --nuke --force`
}

func addTeardownFlags(cmd *cobra.Command, force, dryRun *bool, skip *[]string, publicIPs, all, nuke, servers, volumes, snapshots, buckets, secGroups, network, bastion, keypairs, empty *bool, output *string) {
	cmd.Flags().BoolVar(force, "force", false, "skip confirmation prompts")
	cmd.Flags().BoolVar(dryRun, "dry-run", false, "preview what would be deleted without actually deleting")
	cmd.Flags().StringSliceVar(skip, "skip", []string{}, "skip deletion of specific resource types")
	cmd.Flags().BoolVar(publicIPs, "public-ips", false, "include public IPs in deletion")
	cmd.Flags().BoolVar(all, "all", false, "delete all OCFP/BOSH-managed resources")
	cmd.Flags().BoolVar(nuke, "nuke", false, "DANGER: delete ALL resources in project")
	cmd.Flags().BoolVar(empty, "empty", false, "empty non-empty buckets before deletion")
	cmd.Flags().StringVar(output, "output", OutputTable, "output format: table|json|yaml (for dry-run plan)")

	cmd.Flags().BoolVar(servers, "servers", false, "delete servers")
	cmd.Flags().BoolVar(volumes, "volumes", false, "delete volumes")
	cmd.Flags().BoolVar(snapshots, "snapshots", false, "delete snapshots")
	cmd.Flags().BoolVar(buckets, "buckets", false, "delete buckets")
	cmd.Flags().BoolVar(secGroups, "security-groups", false, "delete security groups")
	cmd.Flags().BoolVar(network, "network", false, "delete networks")
	cmd.Flags().BoolVar(bastion, "bastion", false, "delete only the bastion instance")
	cmd.Flags().BoolVar(keypairs, "key-pairs", false, "delete SSH key pairs")
	cmd.Flags().BoolVar(keypairs, "keys", false, "alias for --key-pairs")
}

func bindTeardownViperFlags(cmd *cobra.Command) {
	_ = viper.BindPFlag("teardown.force", cmd.Flags().Lookup("force"))
	_ = viper.BindPFlag("teardown.dry_run", cmd.Flags().Lookup("dry-run"))
	_ = viper.BindPFlag("teardown.skip", cmd.Flags().Lookup("skip"))
	_ = viper.BindPFlag("teardown.public_ips", cmd.Flags().Lookup("public-ips"))
	_ = viper.BindPFlag("teardown.all", cmd.Flags().Lookup("all"))
	_ = viper.BindPFlag("teardown.nuke", cmd.Flags().Lookup("nuke"))
	_ = viper.BindPFlag("teardown.bastion", cmd.Flags().Lookup("bastion"))
	_ = viper.BindPFlag("teardown.servers", cmd.Flags().Lookup("servers"))
	_ = viper.BindPFlag("teardown.volumes", cmd.Flags().Lookup("volumes"))
	_ = viper.BindPFlag("teardown.snapshots", cmd.Flags().Lookup("snapshots"))
	_ = viper.BindPFlag("teardown.buckets", cmd.Flags().Lookup("buckets"))
	_ = viper.BindPFlag("teardown.security_groups", cmd.Flags().Lookup("security-groups"))
	_ = viper.BindPFlag("teardown.network", cmd.Flags().Lookup("network"))
	_ = viper.BindPFlag("teardown.key_pairs", cmd.Flags().Lookup("key-pairs"))
	_ = viper.BindPFlag("teardown.empty", cmd.Flags().Lookup("empty"))
	_ = viper.BindPFlag("teardown.output", cmd.Flags().Lookup("output"))
}

func runTeardown(cmd *cobra.Command, args []string) error {
	// Silence usage on execution errors
	cmd.SilenceUsage = true

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
	log.Infow("Starting teardown", "mode", teardownConfig.Mode, "bloc", teardownConfig.BlocName)

	err = teardownManager.Execute(ctx)
	if err != nil {
		return fmt.Errorf("teardown failed: %w", err)
	}

	logger.Get().Info("✅ Teardown completed successfully!")

	_, _ = fmt.Fprintf(os.Stdout, "\n✅ Teardown completed successfully for bloc=%s\n", teardownConfig.BlocName)

	return nil
}

type teardownConfig struct {
	BlocName       string
	ConfigFile     string
	Force          bool
	DryRun         bool
	Nuke           bool
	All            bool
	PublicIPs      bool
	Bastion        bool
	Servers        bool
	Volumes        bool
	Snapshots      bool
	Buckets        bool
	SecurityGroups bool
	Network        bool
	KeyPairs       bool
	Empty          bool
	Skip           []string
	Mode           string
	Output         string
}

func getTeardownConfig() *teardownConfig {
	all := viper.GetBool("teardown.all")
	nuke := viper.GetBool("teardown.nuke")
	bastion := viper.GetBool("teardown.bastion")
	servers := viper.GetBool("teardown.servers")
	volumes := viper.GetBool("teardown.volumes")
	snapshots := viper.GetBool("teardown.snapshots")
	buckets := viper.GetBool("teardown.buckets")
	securityGroups := viper.GetBool("teardown.security_groups")
	network := viper.GetBool("teardown.network")
	keypairs := viper.GetBool("teardown.key_pairs") || viper.GetBool("teardown.keys")

	return &teardownConfig{
		BlocName:       viper.GetString("bloc"),
		ConfigFile:     viper.GetString("config"),
		Force:          viper.GetBool("teardown.force"),
		DryRun:         viper.GetBool("teardown.dry_run"),
		Nuke:           nuke,
		All:            all,
		PublicIPs:      viper.GetBool("teardown.public_ips"),
		Bastion:        bastion,
		Servers:        servers,
		Volumes:        volumes,
		Snapshots:      snapshots,
		Buckets:        buckets,
		SecurityGroups: securityGroups,
		Network:        network,
		KeyPairs:       keypairs,
		Empty:          viper.GetBool("teardown.empty"),
		Skip:           viper.GetStringSlice("teardown.skip"),
		Mode:           getTeardownMode(all, nuke, bastion, servers, volumes, snapshots, buckets, securityGroups, network),
		Output:         viper.GetString("teardown.output"),
	}
}

func initializeTeardownLogger(blocName string) (logger.Logger, error) {
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
		Command:    "teardown",
		Subcommand: "", // Teardown has no subcommands
		RequestID:  os.Getenv("OCFP_REQUEST_ID"),
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
	// Get standard state directory for this bloc
	stateDir, err := state.GetStateDir(blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to determine state directory: %w", err)
	}

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create state manager: %w", err)
	}

	_, err = stateManager.Load(blocName)
	if err != nil {
		log.Warnw("Failed to load state, will discover resources from cloud", "error", err)
	}

	return stateManager, nil
}

func createTeardownManager(cfg *config.Config, provider cpi.Provider, stateManager *state.Manager, teardownCfg *teardownConfig) *TeardownManager {
	teardownOpts := &TeardownOptions{
		BlocName:       teardownCfg.BlocName,
		Provider:       cfg.Provider,
		Force:          teardownCfg.Force,
		DryRun:         teardownCfg.DryRun,
		All:            teardownCfg.All,
		Nuke:           teardownCfg.Nuke,
		PublicIPs:      teardownCfg.PublicIPs,
		Bastion:        teardownCfg.Bastion,
		Servers:        teardownCfg.Servers,
		Volumes:        teardownCfg.Volumes,
		Snapshots:      teardownCfg.Snapshots,
		Buckets:        teardownCfg.Buckets,
		SecurityGroups: teardownCfg.SecurityGroups,
		Network:        teardownCfg.Network,
		KeyPairs:       teardownCfg.KeyPairs,
		Empty:          teardownCfg.Empty,
		Skip:           teardownCfg.Skip,
		Mode:           teardownCfg.Mode,
		Output:         teardownCfg.Output,
	}

	return NewTeardownManager(cfg, provider, stateManager, teardownOpts)
}

func getTeardownMode(all, nuke, bastion, servers, volumes, snapshots, buckets, securityGroups, network bool) string {
	if nuke {
		return "NUKE (delete ALL resources)"
	}

	if all {
		return "ALL (delete all OCFP/BOSH resources)"
	}

	// Check if any selective resource type flags are set (including bastion)
	if bastion || servers || volumes || snapshots || buckets || securityGroups || network {
		selectedTypes := collectTeardownTypes(bastion, servers, volumes, snapshots, buckets, securityGroups, network)

		return "SELECTIVE (delete: " + strings.Join(selectedTypes, ", ") + ")"
	}

	return "BOOTSTRAP (delete bootstrap-created resources)"
}

// collectTeardownTypes returns a list of selected resource types for teardown.
func collectTeardownTypes(bastion, servers, volumes, snapshots, buckets, securityGroups, network bool) []string {
	selectedTypes := []string{}

	if bastion {
		selectedTypes = append(selectedTypes, "bastion")
	}

	if servers {
		selectedTypes = append(selectedTypes, "servers")
	}

	if volumes {
		selectedTypes = append(selectedTypes, "volumes")
	}

	if snapshots {
		selectedTypes = append(selectedTypes, "snapshots")
	}

	if buckets {
		selectedTypes = append(selectedTypes, "buckets")
	}

	if securityGroups {
		selectedTypes = append(selectedTypes, "security-groups")
	}

	if network {
		selectedTypes = append(selectedTypes, "networks")
	}

	return selectedTypes
}

// TeardownOptions represents teardown configuration.
type TeardownOptions struct {
	BlocName       string
	Provider       string
	Force          bool
	DryRun         bool
	All            bool
	Nuke           bool
	PublicIPs      bool
	Bastion        bool
	Servers        bool
	Volumes        bool
	Snapshots      bool
	Buckets        bool
	SecurityGroups bool
	Network        bool
	KeyPairs       bool
	Empty          bool
	Skip           []string
	Mode           string
	Output         string
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

	// Try to acquire lock with force option support
	err := m.acquireLockWithForce()
	if err != nil {
		return err
	}

	defer func() { _ = m.stateManager.Unlock(m.options.BlocName) }()

	sortedResources, err := m.prepareResourcesForDeletion(ctx)
	if err != nil {
		return err
	}

	if len(sortedResources) == 0 {
		m.handleEmptyResources()

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

	failedCount := len(sortedResources) - deletedCount
	if failedCount > 0 {
		_, _ = fmt.Fprintf(os.Stderr, "\n⚠️  %d resource(s) failed to delete (see above for details)\n", failedCount)
	}

	_, _ = fmt.Fprintf(os.Stdout, "\n📊 Summary: %d/%d resources deleted successfully\n", deletedCount, len(sortedResources))
	log.Infow("Teardown completed", "deleted", deletedCount, "total", len(sortedResources))

	return nil
}

// DeleteResource deletes a single resource.
func (m *TeardownManager) DeleteResource(ctx context.Context, resource *ResourceToDelete) error {
	switch resource.Type {
	case ResourceInstance:
		return m.deleteComputeResource(ctx, resource)
	case ResourceVolume, ResourceSnapshot, ResourceBucket, "credentials_group":
		return m.deleteStorageResource(ctx, resource)
	case ResourceLoadBalancer, "floating_ip", "public_ip", ResourceSubnet, "network_interface", CategoryNetwork:
		return m.deleteNetworkResource(ctx, resource)
	case ResourceSecurityGroup:
		return m.deleteSecurityResource(ctx, resource)
	case "keypair":
		return m.deleteKeyPairResource(ctx, resource)
	default:
		return ErrUnsupportedResourceType(resource.Type)
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
	Tags         map[string]string
}

// TestFilterResources exposes filterResources for testing.
func (m *TeardownManager) TestFilterResources(resources []*ResourceToDelete) []*ResourceToDelete {
	return m.filterResources(resources)
}

// acquireLockWithForce attempts to acquire state lock, using force if enabled.
func (m *TeardownManager) acquireLockWithForce() error {
	log := logger.Get()

	err := m.stateManager.Lock(m.options.BlocName)
	if err != nil {
		// If force flag is set, break the lock and retry
		if m.options.Force {
			log.Warn("State is locked, but --force flag is set. Breaking lock...")

			_ = m.stateManager.Unlock(m.options.BlocName) // Force unlock
			err = m.stateManager.Lock(m.options.BlocName)
		}

		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "\n💡 Hint: If the previous teardown was interrupted, use --force to break the lock:\n")
			_, _ = fmt.Fprintf(os.Stderr, "   ocfp --bloc %s teardown --force\n\n", m.options.BlocName)

			return fmt.Errorf("failed to acquire state lock: %w", err)
		}
	}

	return nil
}

// handleEmptyResources handles the case when no resources are found for deletion.
func (m *TeardownManager) handleEmptyResources() {
	log := logger.Get()
	log.Info("No resources found to delete")

	// In dry-run mode, still show a plan (even if empty) for clarity
	if m.options.DryRun {
		_, _ = fmt.Fprintf(os.Stdout, "\n📋 No resources found matching criteria for bloc '%s'\n", m.options.BlocName)
		if m.options.Bastion || m.options.SecurityGroups || m.options.Servers || m.options.Volumes || m.options.Snapshots || m.options.Buckets || m.options.Network {
			_, _ = fmt.Fprintf(os.Stdout, "   Mode: %s\n", m.options.Mode)
		}
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
	failedResources := []*ResourceToDelete{}

	_, _ = fmt.Fprintf(os.Stdout, "\n🗑️  Deleting %d resources...\n\n", len(sortedResources))

	for i, resource := range sortedResources {
		progress := fmt.Sprintf("[%d/%d]", i+1, len(sortedResources))
		_, _ = fmt.Fprintf(os.Stdout, "%s Deleting %s: %s (ID: %s)...\n", progress, resource.Type, resource.Name, resource.ID)
		log.Infow("Deleting resource", "type", resource.Type, "name", resource.Name, "progress", progress)

		err := m.deleteResourceWithRetry(ctx, resource, log)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "  ✗ Failed to delete %s %s: %v\n", resource.Type, resource.Name, err)
			log.Errorw("Failed to delete resource", "type", resource.Type, "name", resource.Name, "error", err)
			failedResources = append(failedResources, resource)

			continue
		}

		_, _ = fmt.Fprintf(os.Stdout, "  ✓ Deleted %s: %s\n", resource.Type, resource.Name)
		deletedCount++

		m.updateStateAfterDeletion(resource, log)
	}

	// Retry failed resources that might have been blocked by dependencies
	if len(failedResources) > 0 {
		log.Infow("Retrying failed resources after initial deletion pass", "count", len(failedResources))
		_, _ = fmt.Fprintf(os.Stdout, "\n🔄 Retrying %d failed resources...\n\n", len(failedResources))

		retryDeleted := m.retryFailedResources(ctx, failedResources, log)
		deletedCount += retryDeleted
	}

	return deletedCount
}

// deleteResourceWithRetry attempts to delete a resource with exponential backoff for 409 conflicts.
func (m *TeardownManager) deleteResourceWithRetry(ctx context.Context, resource *ResourceToDelete, log logger.Logger) error {
	var lastErr error

	retryDelay := InitialRetryDelay

	for attempt := 1; attempt <= MaxRetryAttempts; attempt++ {
		err := m.DeleteResource(ctx, resource)
		if err == nil {
			return nil
		}

		lastErr = err

		// Check if it's a 404 Not Found error (already deleted) - treat as success
		if cpi.IsNotFound(err) || strings.Contains(err.Error(), "404") {
			log.Debugw("Resource not found (already deleted)", "resource", resource.Name, "type", resource.Type)

			return nil
		}

		// Check if it's a 409 Conflict error (resource in use)
		if !strings.Contains(err.Error(), ConflictErrorIndicator) {
			// Not a conflict error, don't retry
			return err
		}

		if attempt < MaxRetryAttempts {
			log.Debugw("Resource deletion failed with conflict, retrying",
				"resource", resource.Name,
				"type", resource.Type,
				"attempt", attempt,
				"max_attempts", MaxRetryAttempts,
				"retry_delay", retryDelay)

			_, _ = fmt.Fprintf(os.Stdout, "  ⏳ Resource in use, waiting %v before retry (attempt %d/%d)...\n",
				retryDelay, attempt, MaxRetryAttempts)

			time.Sleep(retryDelay)

			// Exponential backoff with maximum cap
			retryDelay *= RetryDelayMultiplier
			if retryDelay > MaxRetryDelay {
				retryDelay = MaxRetryDelay
			}
		}
	}

	return fmt.Errorf("failed after %d attempts: %w", MaxRetryAttempts, lastErr)
}

// retryFailedResources attempts to delete previously failed resources after other resources are deleted.
func (m *TeardownManager) retryFailedResources(ctx context.Context, failedResources []*ResourceToDelete, log logger.Logger) int {
	retryDeleted := 0

	for i, resource := range failedResources {
		progress := fmt.Sprintf("[%d/%d]", i+1, len(failedResources))
		_, _ = fmt.Fprintf(os.Stdout, "%s Retrying %s: %s (ID: %s)...\n", progress, resource.Type, resource.Name, resource.ID)
		log.Infow("Retrying resource deletion", "type", resource.Type, "name", resource.Name, "progress", progress)

		err := m.deleteResourceWithRetry(ctx, resource, log)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "  ✗ Still failed to delete %s %s: %v\n", resource.Type, resource.Name, err)
			log.Errorw("Failed to delete resource on retry", "type", resource.Type, "name", resource.Name, "error", err)

			continue
		}

		_, _ = fmt.Fprintf(os.Stdout, "  ✓ Deleted %s: %s\n", resource.Type, resource.Name)
		retryDeleted++

		m.updateStateAfterDeletion(resource, log)
	}

	return retryDeleted
}

func (m *TeardownManager) updateStateAfterDeletion(resource *ResourceToDelete, log logger.Logger) {
	err := m.stateManager.RemoveResource(resource.Type, resource.Name)
	if err != nil {
		// This can happen when resources are discovered from cloud but not in state
		// or when duplicate resources were filtered. It's informational, not an error.
		log.Debugw("Resource not found in state (may have been discovered from cloud)", "resource", resource.Name, "type", resource.Type)
	}

	err = m.stateManager.Save()
	if err != nil {
		log.Warnw("Failed to save state", "error", err)
	}
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
		log.Infow("Found resources in state", "count", len(stateResources))
		resources = append(resources, stateResources...)

		// Discover subnets for any networks found in state (subnets not always in state)
		m.discoverSubnetsForNetworks(ctx, stateResources, &resources, log)
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
	log := logger.Get()

	if m.stateManager.Current() == nil {
		return nil, ErrNoStateLoaded
	}

	resources := make([]*ResourceToDelete, 0, len(m.stateManager.Current().Resources))
	skippedCount := 0

	for key, resource := range m.stateManager.Current().Resources {
		parts := strings.SplitN(key, ".", ExpectedResourceKeyParts)
		if len(parts) != ExpectedResourceKeyParts {
			continue
		}

		// Skip if resource type should be skipped
		if m.shouldSkipResourceType(parts[0]) {
			continue
		}

		// SAFETY: Validate bloc name matches to prevent cross-bloc deletion
		// Only delete resources that belong to the current bloc
		if resource.Tags != nil {
			if blocTag, ok := resource.Tags["bloc"]; ok {
				if blocTag != m.options.BlocName {
					log.Debugw("Skipping resource from different bloc",
						"resource", parts[1],
						"resource_bloc", blocTag,
						"target_bloc", m.options.BlocName)

					skippedCount++

					continue
				}
			}
		}

		deps, _ := m.stateManager.GetDependencies(key)

		resources = append(resources, &ResourceToDelete{
			Type:         parts[0],
			ID:           resource.ID,
			Name:         parts[1],
			Dependencies: deps,
			State:        resource.State,
			Properties:   resource.Properties,
			Tags:         resource.Tags,
		})
	}

	if skippedCount > 0 {
		log.Infow("Skipped resources from other blocs for safety",
			"skipped_count", skippedCount,
			"target_bloc", m.options.BlocName)
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

// extractSubnetProperties extracts CIDR and type from subnet resource properties.
func extractSubnetProperties(resource *ResourceToDelete) (string, string) {
	var cidr, subnetType string

	if resource.Properties != nil {
		if c, ok := resource.Properties["cidr"].(string); ok {
			cidr = c
		}

		if t, ok := resource.Properties["type"].(string); ok {
			subnetType = t
		}
	}

	return cidr, subnetType
}

// formatMetadataForDisplay formats metadata tags for display in tables.
// Returns a compact string representation: "key1=val1, key2=val2".
// formatMetadataForDisplay formats metadata tags for display in tables.
// Note: Requires strict metadata format with hyphenated keys (managed-by, created-at).
// Resources without proper metadata will not be displayed.
func formatMetadataForDisplay(tags map[string]string) string {
	if len(tags) == 0 {
		return "-"
	}

	// Order: bloc, managed-by, created-at for consistent display
	result := ""
	if bloc, ok := tags["bloc"]; ok {
		result += "bloc=" + bloc
	}

	if managedBy, ok := tags["managed-by"]; ok && managedBy != "" {
		if result != "" {
			result += ", "
		}

		result += "managed-by=" + managedBy
	}

	if createdAt, ok := tags["created-at"]; ok && createdAt != "" {
		if result != "" {
			result += ", "
		}

		result += "created-at=" + createdAt
	}

	if result == "" {
		return "-"
	}

	return result
}

func (m *TeardownManager) discoverComputeResources(ctx context.Context, tagFilter map[string]string, resources *[]*ResourceToDelete, log logger.Logger) {
	compute := m.provider.Compute()
	if compute == nil {
		return
	}

	// Discover instances
	instances, err := compute.ListInstances(ctx, tagFilter)
	if err == nil {
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

		log.Infow("Discovered instances", "count", len(instances))
	}

	// Discover keypairs
	keypairs, err := compute.ListKeyPairs(ctx)
	if err == nil {
		matchingKeypairs := 0

		for _, keypair := range keypairs {
			// EXCEPTION: Keypairs don't support tags/labels in most cloud providers.
			// Filter by name pattern instead: OCFP keypairs follow the pattern {bloc-name}-keypair.
			// This is the only resource type that uses name-based filtering instead of metadata filtering.
			if strings.Contains(keypair.Name, tagFilter["bloc"]+"-keypair") {
				*resources = append(*resources, &ResourceToDelete{
					Type:         "keypair",
					ID:           keypair.ID,
					Name:         keypair.Name,
					Dependencies: nil,
					State:        "",
					Properties:   nil,
				})
				matchingKeypairs++
			}
		}

		log.Infow("Discovered keypairs", "count", matchingKeypairs)
	}
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

		log.Infow("Discovered volumes", "count", len(volumes))
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

		log.Infow("Discovered snapshots", "count", len(snapshots))
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

		log.Infow("Discovered buckets", "count", len(buckets))
	}
}

func (m *TeardownManager) discoverNetworkResources(ctx context.Context, tagFilter map[string]string, resources *[]*ResourceToDelete, log logger.Logger) {
	network := m.provider.Network()
	if network == nil {
		return
	}

	m.discoverNetworks(ctx, network, tagFilter, resources, log)
	m.discoverFloatingIPs(ctx, network, resources, log)

	if m.options.PublicIPs {
		m.discoverStackitPublicIPs(ctx, network, resources, log)
	}

	m.discoverNetworkInterfaces(ctx, network, tagFilter, resources, log)
	m.discoverLoadBalancers(ctx, network, tagFilter, resources, log)
}

func (m *TeardownManager) discoverNetworks(ctx context.Context, network cpi.NetworkManager, tagFilter map[string]string, resources *[]*ResourceToDelete, log logger.Logger) {
	networks, err := network.ListNetworks(ctx, tagFilter)
	if err != nil {
		return
	}

	for _, net := range networks {
		*resources = append(*resources, &ResourceToDelete{
			Type:         CategoryNetwork,
			ID:           net.ID,
			Name:         net.Name,
			Dependencies: nil,
			State:        "",
			Properties:   nil,
		})

		m.discoverSubnetsForNetwork(ctx, network, net, resources, log)
	}

	log.Infow("Discovered networks", "count", len(networks))
}

func (m *TeardownManager) discoverSubnetsForNetwork(ctx context.Context, network cpi.NetworkManager, net *cpi.Network, resources *[]*ResourceToDelete, log logger.Logger) {
	subnets, err := network.ListSubnets(ctx, net.ID)
	if err != nil {
		return
	}

	for _, subnet := range subnets {
		*resources = append(*resources, &ResourceToDelete{
			Type:         ResourceSubnet,
			ID:           subnet.ID,
			Name:         subnet.Name,
			Dependencies: nil,
			State:        "",
			Properties: map[string]interface{}{
				"cidr":              subnet.CIDR,
				"type":              subnet.Type,
				"availability_zone": subnet.AvailabilityZone,
			},
		})
	}

	log.Infow("Discovered subnets for network", "network", net.Name, "count", len(subnets))
}

func (m *TeardownManager) discoverFloatingIPs(ctx context.Context, network cpi.NetworkManager, resources *[]*ResourceToDelete, log logger.Logger) {
	floatingIPs, err := network.ListFloatingIPs(ctx)
	if err != nil {
		return
	}

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

	log.Infow("Discovered floating IPs", "count", len(floatingIPs))
}

func (m *TeardownManager) discoverLoadBalancers(ctx context.Context, network cpi.NetworkManager, tagFilter map[string]string, resources *[]*ResourceToDelete, log logger.Logger) {
	lbs, err := network.ListLoadBalancers(ctx, tagFilter)
	if err != nil {
		return
	}

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

	log.Infow("Discovered load balancers", "count", len(lbs))
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
		log.Warnw("Failed to list public IPs", "error", err)

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

	log.Infow("Discovered public IPs", "count", len(ips))
}

func (m *TeardownManager) discoverNetworkInterfaces(ctx context.Context, network cpi.NetworkManager, tagFilter map[string]string, resources *[]*ResourceToDelete, log logger.Logger) {
	// Type assertion to check if NetworkManager supports ListNetworkInterfaces
	type networkInterfaceLister interface {
		ListNetworkInterfaces(ctx context.Context, filters map[string]string) ([]*cpi.NetworkInterface, error)
	}

	niLister, ok := network.(networkInterfaceLister)
	if !ok {
		// Provider doesn't support network interface listing (e.g., AWS)
		log.Debug("Provider does not support network interface discovery")

		return
	}

	nics, err := niLister.ListNetworkInterfaces(ctx, tagFilter)
	if err != nil {
		log.Warnw("Failed to list network interfaces", "error", err)

		return
	}

	for _, nic := range nics {
		*resources = append(*resources, &ResourceToDelete{
			Type:         "network_interface",
			ID:           nic.ID,
			Name:         nic.Name,
			Dependencies: nil,
			State:        "",
			Properties: map[string]interface{}{
				"network_id":  nic.NetworkID,
				"instance_id": nic.InstanceID,
				"ipv4":        nic.IPv4,
			},
		})
	}

	log.Infow("Discovered network interfaces", "count", len(nics))
}

func (m *TeardownManager) discoverSubnetsForNetworks(ctx context.Context, stateResources []*ResourceToDelete, allResources *[]*ResourceToDelete, log logger.Logger) {
	network := m.provider.Network()
	if network == nil {
		return
	}

	// Build set of existing subnet IDs to prevent duplicates
	existingSubnetIDs := make(map[string]bool)

	for _, resource := range *allResources {
		if resource.Type == ResourceSubnet {
			existingSubnetIDs[resource.ID] = true
		}
	}

	// Find all network resources in state
	for _, resource := range stateResources {
		if resource.Type == "network" {
			// Discover subnets for this network from the cloud
			subnets, err := network.ListSubnets(ctx, resource.ID)
			if err != nil {
				log.Warnw("Failed to discover subnets for network", "network", resource.Name, "error", err)

				continue
			}

			addedCount := 0

			for _, subnet := range subnets {
				// Skip if subnet is already in the resources list
				if existingSubnetIDs[subnet.ID] {
					continue
				}

				*allResources = append(*allResources, &ResourceToDelete{
					Type:         ResourceSubnet,
					ID:           subnet.ID,
					Name:         subnet.Name,
					Dependencies: nil,
					State:        "",
					Properties: map[string]interface{}{
						"cidr":              subnet.CIDR,
						"type":              subnet.Type,
						"availability_zone": subnet.AvailabilityZone,
					},
				})
				existingSubnetIDs[subnet.ID] = true
				addedCount++
			}

			if addedCount > 0 {
				log.Infow("Discovered subnets for network", "network", resource.Name, "count", addedCount)
			}
		}
	}
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

	filteredCount := 0

	for _, secGroup := range secGroups {
		// Exclude the 'default' security group - it's provider-managed and cannot be deleted
		if secGroup.Name == "default" {
			log.Debugw("Skipping default security group", "id", secGroup.ID, "name", secGroup.Name)

			continue
		}

		// Skip load balancer-managed security groups - they are automatically deleted with the LB
		// STACKIT creates these with names like: loadbalancer/load-balancer-name/backend
		if strings.HasPrefix(secGroup.Name, "loadbalancer/") {
			log.Debugw("Skipping load balancer-managed security group", "id", secGroup.ID, "name", secGroup.Name)

			continue
		}

		*resources = append(*resources, &ResourceToDelete{
			Type:         ResourceSecurityGroup,
			ID:           secGroup.ID,
			Name:         secGroup.Name,
			Dependencies: nil,
			State:        "",
			Properties:   nil,
		})
		filteredCount++
	}

	log.Infow("Discovered security groups", "count", filteredCount, "total", len(secGroups))
}

// discoverAllResources finds ALL resources in the project (nuke mode).
func (m *TeardownManager) discoverAllResources(ctx context.Context) ([]*ResourceToDelete, error) {
	log := logger.Get()
	log.Warn("NUKE MODE: Discovering ALL resources in project")
	log.Warn("NUKE MODE bypasses bloc name filtering - ALL resources will be deleted regardless of bloc tags")

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

	log.Warnw("NUKE MODE: Found resources", "count", len(resources))

	return resources, nil
}

// isSelectiveModeActive checks if any selective resource type flags are set.
func (m *TeardownManager) isSelectiveModeActive() bool {
	return m.options.Bastion || m.options.Servers || m.options.Volumes ||
		m.options.Snapshots || m.options.Buckets || m.options.SecurityGroups || m.options.Network || m.options.KeyPairs
}

// shouldIncludeResource checks if a resource should be included based on all filters.
func (m *TeardownManager) shouldIncludeResource(resource *ResourceToDelete, bastionName string) bool {
	selectiveModeActive := m.isSelectiveModeActive()

	if selectiveModeActive {
		// In selective mode, only include resources of the specified types
		// Check if this is the bastion instance and bastion flag is set
		isBastionResource := resource.Type == "instance" && resource.Name == bastionName && m.options.Bastion

		// Check if resource matches other selective flags
		matchesOtherFlags := m.shouldIncludeResourceInSelectiveMode(resource)

		// Include resource if it matches bastion flag OR other selective flags
		if !isBastionResource && !matchesOtherFlags {
			return false
		}
	}

	if m.shouldSkipResourceType(resource.Type) {
		return false
	}

	// Skip floating IPs unless explicitly requested
	if resource.Type == ResourceFloatingIP && !m.options.PublicIPs {
		return false
	}

	// Skip public IPs unless explicitly requested
	if resource.Type == ResourcePublicIP && !m.options.PublicIPs {
		return false
	}

	return true
}

func (m *TeardownManager) filterResources(resources []*ResourceToDelete) []*ResourceToDelete {
	filtered := make([]*ResourceToDelete, 0, len(resources))

	bastionName := m.options.BlocName + "-bastion"

	for _, resource := range resources {
		if m.shouldIncludeResource(resource, bastionName) {
			filtered = append(filtered, resource)
		}
	}

	return filtered
}

// shouldIncludeResourceInSelectiveMode checks if a resource should be included when selective flags are set.
func (m *TeardownManager) shouldIncludeResourceInSelectiveMode(resource *ResourceToDelete) bool {
	switch resource.Type {
	case "instance":
		return m.options.Servers
	case ResourceVolume:
		return m.options.Volumes
	case ResourceSnapshot:
		return m.options.Snapshots
	case ResourceBucket:
		return m.options.Buckets
	case ResourceSecurityGroup:
		return m.options.SecurityGroups
	case CategoryNetwork, ResourceSubnet, "router":
		return m.options.Network
	case "keypair":
		return m.options.KeyPairs
	default:
		// For other resource types (like floating_ips), include them based on related flags
		// Floating/public IPs are network-related
		if resource.Type == ResourceFloatingIP || resource.Type == ResourcePublicIP {
			return m.options.Network && m.options.PublicIPs
		}
		// Load balancers are network-related
		if resource.Type == "loadbalancer" {
			return m.options.Network
		}

		return false
	}
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
			(skip == "security" && (resourceType == ResourceSecurityGroup)) {
			return true
		}
	}

	return false
}

// sortResourcesForDeletion sorts resources in the correct order for deletion.
// Order is the reverse of bootstrap creation order.
// CRITICAL: Instances/NICs must be deleted before security groups to avoid 409 conflicts.
func (m *TeardownManager) sortResourcesForDeletion(resources []*ResourceToDelete) []*ResourceToDelete {
	// Define deletion order (dependency-aware, optimized to prevent 409 conflicts)
	order := map[string]int{
		"loadbalancer":        LoadBalancerPriority,     // 1: Delete load balancers first
		"instance":            InstancePriority,         // 2: Delete instances early (releases volumes, NICs, SGs)
		"network_interface":   NetworkInterfacePriority, // 3: Delete network interfaces (after instances detached)
		"bucket":              BucketPriority,           // 4: Delete buckets
		ResourceSnapshot:      SnapshotPriority,         // 5: Delete snapshots before volumes
		ResourceVolume:        VolumePriority,           // 6: Delete volumes (after instances released them)
		"keypair":             KeyPairPriority,          // 7: Delete key pairs
		ResourceFloatingIP:    FloatingIPPriority,       // 8: Delete floating IPs
		ResourcePublicIP:      FloatingIPPriority,       // 8: Delete public IPs
		ResourceSecurityGroup: SecurityGroupPriority,    // 9: Delete security groups (after NICs removed)
		ResourceSubnet:        SubnetRouterPriority,     // 10: Delete subnets
		"router":              SubnetRouterPriority,     // 10: Delete routers
		"network":             NetworkPriority,          // 11: Delete networks last
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

// groupResourcesByType groups resources by their type for deletion plan.
func groupResourcesByType(resources []*ResourceToDelete) (map[string][]*ResourceToDelete, []string) {
	typeGroups := make(map[string][]*ResourceToDelete)
	types := []string{}

	for _, r := range resources {
		if _, ok := typeGroups[r.Type]; !ok {
			types = append(types, r.Type)
		}

		typeGroups[r.Type] = append(typeGroups[r.Type], r)
	}

	sort.Strings(types)

	return typeGroups, types
}

// buildResourceSection builds a table section for a specific resource type.
func buildResourceSection(typ string, resources []*ResourceToDelete) ui.Section {
	sort.Slice(resources, func(i, j int) bool { return resources[i].Name < resources[j].Name })

	// Use different headers for subnets to include CIDR and Type
	var headers []string
	if typ == ResourceSubnet {
		headers = []string{"NAME", "ID", "CIDR", "TYPE", "STATE", "METADATA"}
	} else {
		headers = []string{"NAME", "ID", "STATE", "METADATA"}
	}

	rows := make([][]string, 0, len(resources))
	for _, resource := range resources {
		state := resource.State
		metadata := formatMetadataForDisplay(resource.Tags)

		// Include CIDR and Type for subnets
		if typ == ResourceSubnet {
			cidr, subnetType := extractSubnetProperties(resource)
			rows = append(rows, []string{resource.Name, resource.ID, cidr, subnetType, state, metadata})
		} else {
			rows = append(rows, []string{resource.Name, resource.ID, state, metadata})
		}
	}

	return ui.Section{
		Title:   fmt.Sprintf("%s (%d)", strings.ToUpper(typ), len(resources)),
		Headers: headers,
		Rows:    rows,
	}
}

// showDeletionPlan displays what will be deleted.
func (m *TeardownManager) showDeletionPlan(resources []*ResourceToDelete) error {
	typeGroups, types := groupResourcesByType(resources)

	// Build plan table
	title := fmt.Sprintf("DRY RUN — Teardown Plan for bloc '%s' (%s)", m.options.BlocName, m.options.Mode)
	summary := fmt.Sprintf("Delete %d resources across %d types", len(resources), len(types))
	planTable := &ui.Table{
		Title:    title,
		Summary:  summary,
		Sections: nil,
	}

	for _, typ := range types {
		section := buildResourceSection(typ, typeGroups[typ])
		planTable.Sections = append(planTable.Sections, section)
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
	// Include bloc name in confirmation for safety
	confirmMsg := fmt.Sprintf("\nThis will permanently delete %d resources from bloc '%s'. Continue? [y/N]: ",
		resourceCount, m.options.BlocName)

	_, err := fmt.Fprint(os.Stdout, confirmMsg)
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

	err := compute.DeleteInstance(ctx, resource.ID)
	if err != nil {
		// If resource doesn't exist, that's success (already deleted)
		if cpi.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("failed to delete instance %s: %w", resource.ID, err)
	}

	return nil
}

func (m *TeardownManager) deleteStorageResource(ctx context.Context, resource *ResourceToDelete) error {
	storage := m.provider.Storage()
	if storage == nil {
		return ErrProviderDoesNotSupportStorageMgmt
	}

	switch resource.Type {
	case ResourceVolume:
		err := storage.DeleteVolume(ctx, resource.ID)
		if err != nil {
			// If resource doesn't exist, that's success (already deleted)
			if cpi.IsNotFound(err) {
				return nil
			}

			return fmt.Errorf("failed to delete volume %s: %w", resource.ID, err)
		}

		return nil
	case ResourceSnapshot:
		err := storage.DeleteSnapshot(ctx, resource.ID)
		if err != nil {
			// If resource doesn't exist, that's success (already deleted)
			if cpi.IsNotFound(err) {
				return nil
			}

			return fmt.Errorf("failed to delete snapshot %s: %w", resource.ID, err)
		}

		return nil
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

	// Check if bucket is empty
	isEmpty, err := storage.IsBucketEmpty(ctx, resource.ID)
	if err != nil {
		// If we can't check, treat as non-empty for safety
		log.Warnw("Failed to check if bucket is empty", "bucket", resource.ID, "error", err)

		isEmpty = false
	}

	// If bucket is not empty and --empty flag is not set, skip deletion
	if !isEmpty && !m.options.Empty {
		_, _ = fmt.Fprintf(os.Stderr, "  ⚠️  Bucket %s is not empty, skipping deletion. Use --empty to empty and delete.\n", resource.Name)
		log.Infow("Skipping non-empty bucket deletion", "bucket", resource.ID, "name", resource.Name)

		return nil // Not an error, just skip
	}

	// If bucket is not empty but --empty flag is set, empty it first
	if !isEmpty && m.options.Empty {
		log.Infow("Emptying bucket before deletion", "bucket", resource.ID, "name", resource.Name)
		_, _ = fmt.Fprintf(os.Stdout, "  🗑️  Emptying bucket %s before deletion...\n", resource.Name)

		err := storage.EmptyBucket(ctx, resource.ID)
		if err != nil && !cpi.IsNotFound(err) {
			log.Warnw("Failed to empty bucket", "bucket", resource.ID, "error", err)

			return fmt.Errorf("failed to empty bucket %s: %w", resource.ID, err)
		}
	}

	// Delete the bucket
	err = storage.DeleteBucket(ctx, resource.ID)
	if err != nil {
		// If resource doesn't exist, that's success (already deleted)
		if cpi.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("failed to delete bucket %s: %w", resource.ID, err)
	}

	return nil
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

	err := s.DeleteCredentialsGroup(ctx, resource.ID)
	if err != nil {
		// If resource doesn't exist, that's success (already deleted)
		if cpi.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("failed to delete credentials group %s: %w", resource.ID, err)
	}

	return nil
}

func (m *TeardownManager) deleteNetworkResource(ctx context.Context, resource *ResourceToDelete) error {
	network := m.provider.Network()
	if network == nil {
		return ErrProviderDoesNotSupportNetworkMgmt
	}

	switch resource.Type {
	case "loadbalancer":
		err := network.DeleteLoadBalancer(ctx, resource.ID)
		if err != nil {
			// If resource doesn't exist, that's success (already deleted)
			if cpi.IsNotFound(err) {
				return nil
			}

			return fmt.Errorf("failed to delete load balancer %s: %w", resource.ID, err)
		}

		// Wait for load balancer deletion to propagate and auto-created security groups to be cleaned up
		// STACKIT load balancers create security groups that are automatically deleted when the LB is deleted
		// but this happens asynchronously and may take a few seconds
		const loadBalancerDeletionWaitDuration = 3 * time.Second
		time.Sleep(loadBalancerDeletionWaitDuration)

		return nil
	case "floating_ip":
		err := network.ReleaseFloatingIP(ctx, resource.ID)
		if err != nil {
			// If resource doesn't exist, that's success (already deleted)
			if cpi.IsNotFound(err) {
				return nil
			}

			return fmt.Errorf("failed to release floating IP %s: %w", resource.ID, err)
		}

		return nil
	case "public_ip":
		return m.deletePublicIP(ctx, network, resource)
	case "network_interface":
		return m.deleteNetworkInterface(ctx, network, resource)
	case ResourceSubnet:
		return m.deleteSubnet(ctx, network, resource)
	case "network":
		err := network.DeleteNetwork(ctx, resource.ID)
		if err != nil {
			// If resource doesn't exist, that's success (already deleted)
			if cpi.IsNotFound(err) {
				return nil
			}

			return fmt.Errorf("failed to delete network %s: %w", resource.ID, err)
		}

		return nil
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

	err := s.DeletePublicIP(ctx, resource.ID)
	if err != nil {
		// If resource doesn't exist, that's success (already deleted)
		if cpi.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("failed to delete public IP %s: %w", resource.ID, err)
	}

	return nil
}

func (m *TeardownManager) deleteNetworkInterface(ctx context.Context, network cpi.NetworkManager, resource *ResourceToDelete) error {
	// Type assertion for providers that support network interface deletion
	type networkInterfaceDeleter interface {
		DeleteNetworkInterface(ctx context.Context, nicID string) error
	}

	niDeleter, ok := network.(networkInterfaceDeleter)
	if !ok {
		// Provider doesn't support network interface deletion (e.g., AWS manages NICs with instances)
		return nil
	}

	err := niDeleter.DeleteNetworkInterface(ctx, resource.ID)
	if err != nil {
		// If resource doesn't exist, that's success (already deleted)
		if cpi.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("failed to delete network interface %s: %w", resource.ID, err)
	}

	return nil
}

func (m *TeardownManager) deleteSubnet(ctx context.Context, network cpi.NetworkManager, resource *ResourceToDelete) error {
	// AWS requires subnets to be deleted before VPC can be deleted
	err := network.DeleteSubnet(ctx, resource.ID)
	if err != nil {
		// If resource doesn't exist, that's success (already deleted)
		if cpi.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("failed to delete subnet %s: %w", resource.ID, err)
	}

	return nil
}

func (m *TeardownManager) deleteSecurityResource(ctx context.Context, resource *ResourceToDelete) error {
	security := m.provider.Security()
	if security == nil {
		return ErrProviderDoesNotSupportSecurityMgmt
	}

	err := security.DeleteSecurityGroup(ctx, resource.ID)
	if err != nil {
		// If resource doesn't exist, that's success (already deleted)
		if cpi.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("failed to delete security group %s: %w", resource.ID, err)
	}

	return nil
}

func (m *TeardownManager) deleteKeyPairResource(ctx context.Context, resource *ResourceToDelete) error {
	compute := m.provider.Compute()
	if compute == nil {
		return ErrProviderDoesNotSupportComputeMgmt
	}

	err := compute.DeleteKeyPair(ctx, resource.ID)
	if err != nil {
		// If resource doesn't exist, that's success (already deleted)
		if cpi.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("failed to delete key pair %s: %w", resource.ID, err)
	}

	return nil
}
