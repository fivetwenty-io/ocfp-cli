// Package bootstrap handles infrastructure bootstrapping operations for OCFP environments.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

const (
	// resourceKeyParts is the expected number of parts when splitting a resource key (type.name).
	resourceKeyParts = 2
)

// Bootstrap errors.
var (
	ErrNoStateForRollback      = errors.New("no state available for rollback")
	ErrRollbackPartiallyFailed = errors.New("rollback partially failed")
)

// Options represents bootstrap options.
type Options struct {
	BlocName       string
	Provider       string
	Region         string
	Force          bool
	Yes            bool
	DryRun         bool
	All            bool
	Bastion        bool
	Artifacts      bool
	Servers        bool
	Volumes        bool
	Snapshots      bool
	Buckets        bool
	SecurityGroups bool
	Network        bool
	PublicIPs      bool
	KeyPairs       bool
	Output         string
	Timeout        time.Duration
}

// Manager handles the bootstrap process.
type Manager struct {
	config       *config.Config
	provider     cpi.Provider
	stateManager *state.Manager
	options      *Options
	metadata     *MetadataManager
}

// NewManager creates a new bootstrap manager.
func NewManager(cfg *config.Config, provider cpi.Provider, stateManager *state.Manager, opts *Options) *Manager {
	return &Manager{
		config:       cfg,
		provider:     provider,
		stateManager: stateManager,
		options:      opts,
		metadata:     NewMetadataManager(opts.BlocName),
	}
}

// StateManager returns the state manager.
func (m *Manager) StateManager() *state.Manager {
	return m.stateManager
}

// Execute executes the bootstrap process.
func (m *Manager) Execute(ctx context.Context) error {
	mode := m.getBootstrapMode()
	logger.Infof("Starting bootstrap for bloc=%s provider=%s region=%s mode=%s", m.options.BlocName, m.options.Provider, m.options.Region, mode)

	if m.options.DryRun {
		return m.renderDryRunPlan()
	}

	// Show pre-bootstrap summary and get user confirmation
	err := m.showBootstrapPlan(ctx)
	if err != nil {
		return err
	}

	allSteps := []bootstrapStep{
		{"Create Network", m.CreateNetwork, "network", true},
		{"Create Subnets", m.CreateSubnets, "network", true},
		{"Create Security Groups", m.CreateSecurityGroups, "security", true},
		{"Create Public IPs", m.CreatePublicIPs, "network", false},
		{"Create Key Pair", m.createKeyPair, "servers", true},
		// {"Create Volumes", m.createVolumes, "volumes", false},
		{"Create Bastion", m.CreateBastion, "servers", false},
		{"Create Artifacts", m.CreateArtifacts, "artifacts", false},
		{"Create Buckets", m.CreateBuckets, "buckets", false},
	}

	// Filter steps based on mode
	steps := m.filterSteps(allSteps)

	if len(steps) == 0 {
		logger.Warnf("No steps to execute based on selected flags")

		_, _ = fmt.Fprintf(os.Stdout, "\n⚠️  No resources selected for creation\n")

		return nil
	}

	totalSteps := len(steps)
	for stepIndex, step := range steps {
		// Show progress indicator
		_, _ = fmt.Fprintf(os.Stdout, "\n[%d/%d] %s...\n", stepIndex+1, totalSteps, step.name)
		logger.Infof("Executing step: %s", step.name)

		err := step.fn(ctx)
		if err != nil {
			return m.handleBootstrapStepError(ctx, err, stepIndex, totalSteps, step.name)
		}

		_, _ = fmt.Fprintf(os.Stdout, "  ✓ %s completed\n", step.name)
		logger.Infof("Completed step: %s", step.name)

		// Save state after each successful step — state persistence is critical
		// for subsequent commands (ssh bastion, teardown) to find resources
		saveErr := m.stateManager.Save()
		if saveErr != nil {
			return fmt.Errorf("failed to save state after %s: %w", step.name, saveErr)
		}
	}

	_, _ = fmt.Fprintf(os.Stdout, "\n✅ Bootstrap completed successfully for bloc=%s (mode: %s)\n", m.options.BlocName, mode)

	logger.Infof("Bootstrap completed successfully")

	return nil
}

// handleBootstrapStepError handles errors during bootstrap step execution with optional rollback.
func (m *Manager) handleBootstrapStepError(ctx context.Context, err error, stepIndex, totalSteps int, stepName string) error {
	// Save state before handling error so progress isn't lost
	_ = m.stateManager.Save()

	// Handle bootstrap failure with rollback option
	_, _ = fmt.Fprintf(os.Stderr, "\n❌ Bootstrap failed at step %d/%d: %s\n", stepIndex+1, totalSteps, stepName)
	_, _ = fmt.Fprintf(os.Stderr, "Error: %v\n\n", err)

	// Offer rollback if not in force mode and we've created resources
	if !m.options.Force && stepIndex > 0 {
		m.offerBootstrapRollback(ctx, stepIndex)
	}

	return fmt.Errorf("failed to %s: %w", strings.ToLower(stepName), err)
}

// offerBootstrapRollback prompts user for rollback and executes if confirmed.
func (m *Manager) offerBootstrapRollback(ctx context.Context, stepIndex int) {
	_, _ = fmt.Fprintf(os.Stdout, "⚠️  Bootstrap has created %d resources before failing.\n", stepIndex)
	_, _ = fmt.Fprintf(os.Stdout, "Would you like to rollback (delete) the partially-created resources? [y/N]: ")

	var response string

	_, scanErr := fmt.Scanln(&response)
	if scanErr == nil && strings.ToLower(strings.TrimSpace(response)) == "y" {
		logger.Infof("User requested rollback of partially-created resources")

		rollbackErr := m.rollbackBootstrap(ctx)
		if rollbackErr != nil {
			logger.Errorf("Rollback failed: %v", rollbackErr)
			_, _ = fmt.Fprintf(os.Stderr, "\n❌ Rollback failed: %v\n", rollbackErr)
			_, _ = fmt.Fprintf(os.Stderr, "You may need to manually clean up resources using: ocfp --bloc %s teardown\n", m.options.BlocName)
		} else {
			_, _ = fmt.Fprintf(os.Stdout, "\n✅ Rollback completed successfully\n")
		}
	} else {
		_, _ = fmt.Fprintf(os.Stdout, "\nSkipping rollback. Resources remain in cloud.\n")
		_, _ = fmt.Fprintf(os.Stdout, "To clean up later, run: ocfp --bloc %s teardown\n", m.options.BlocName)
	}
}

// getBootstrapMode returns a string describing the current bootstrap mode.
func (m *Manager) getBootstrapMode() string {
	if m.options.Bastion {
		return "BASTION (create bastion and dependencies only)"
	}

	// Check if any selective resource type flags are set
	if m.options.Servers || m.options.Volumes || m.options.Snapshots ||
		m.options.Buckets || m.options.SecurityGroups || m.options.Network ||
		m.options.PublicIPs || m.options.KeyPairs || m.options.Artifacts {
		selectedTypes := m.collectSelectedResourceTypes()

		return "SELECTIVE (create: " + strings.Join(selectedTypes, ", ") + ")"
	}

	return "ALL (create all bootstrap resources)"
}

// collectSelectedResourceTypes returns a list of selected resource type names.
func (m *Manager) collectSelectedResourceTypes() []string {
	selectedTypes := []string{}

	if m.options.Servers {
		selectedTypes = append(selectedTypes, "servers")
	}

	if m.options.Volumes {
		selectedTypes = append(selectedTypes, "volumes")
	}

	if m.options.Snapshots {
		selectedTypes = append(selectedTypes, "snapshots")
	}

	if m.options.Buckets {
		selectedTypes = append(selectedTypes, "buckets")
	}

	if m.options.SecurityGroups {
		selectedTypes = append(selectedTypes, "security-groups")
	}

	if m.options.Network {
		selectedTypes = append(selectedTypes, "network")
	}

	if m.options.PublicIPs {
		selectedTypes = append(selectedTypes, "public-ips")
	}

	if m.options.KeyPairs {
		selectedTypes = append(selectedTypes, "key-pairs")
	}

	if m.options.Artifacts {
		selectedTypes = append(selectedTypes, "artifacts")
	}

	return selectedTypes
}

type bootstrapStep struct {
	name     string
	fn       func(context.Context) error
	category string
	required bool
}

// filterSteps filters the steps based on the selected mode.
func (m *Manager) filterSteps(allSteps []bootstrapStep) []bootstrapStep {
	// If bastion flag is set, include bastion and all required dependencies
	if m.options.Bastion {
		return m.filterBastionSteps(allSteps)
	}

	// Check if any selective resource type flags are set
	if !m.isSelectiveModeActive() && !m.options.All {
		return allSteps
	}

	// Selective mode - filter based on flags
	if m.isSelectiveModeActive() {
		return m.filterSelectiveSteps(allSteps)
	}

	// --all flag or default behavior
	return allSteps
}

// filterBastionSteps filters steps for bastion mode.
func (m *Manager) filterBastionSteps(allSteps []bootstrapStep) []bootstrapStep {
	filtered := make([]bootstrapStep, 0, len(allSteps))
	for _, step := range allSteps {
		if step.required || step.category == "servers" {
			filtered = append(filtered, step)
		}
	}

	return filtered
}

// isSelectiveModeActive checks if any selective resource type flags are set.
func (m *Manager) isSelectiveModeActive() bool {
	return m.options.Servers || m.options.Volumes || m.options.Snapshots ||
		m.options.Buckets || m.options.SecurityGroups || m.options.Network ||
		m.options.PublicIPs || m.options.KeyPairs || m.options.Artifacts
}

// filterSelectiveSteps filters steps based on selective flags.
func (m *Manager) filterSelectiveSteps(allSteps []bootstrapStep) []bootstrapStep {
	filtered := make([]bootstrapStep, 0, len(allSteps))
	needsNetwork := m.options.Network || m.options.Servers

	for _, step := range allSteps {
		if m.shouldIncludeStep(step, needsNetwork) {
			filtered = append(filtered, step)
		}
	}

	return filtered
}

// shouldIncludeStep determines if a step should be included based on category and flags.
func (m *Manager) shouldIncludeStep(step bootstrapStep, needsNetwork bool) bool {
	switch step.category {
	case "network":
		return m.shouldIncludeNetworkStep(step, needsNetwork)
	case "security":
		return m.options.SecurityGroups || (m.options.Servers && step.required)
	case "servers":
		// If only key-pairs flag is set, only include the key pair step
		if m.options.KeyPairs && !m.options.Servers && !m.options.Bastion {
			return step.name == "Create Key Pair"
		}

		return m.options.Servers || m.options.Bastion || m.options.KeyPairs
	case "volumes":
		return m.options.Volumes
	case "buckets":
		return m.options.Buckets
	case "artifacts":
		return m.options.Artifacts
	default:
		return false
	}
}

// shouldIncludeNetworkStep determines if a network step should be included.
func (m *Manager) shouldIncludeNetworkStep(step bootstrapStep, needsNetwork bool) bool {
	if step.name == "Create Public IPs" {
		return m.options.PublicIPs || m.options.Network
	}

	return needsNetwork && (m.options.Network || step.required)
}

// baseTags returns the base tags for all resources.
func (m *Manager) baseTags() map[string]string {
	return m.metadata.BuildBaseTags()
}

// rollbackBootstrap cleans up resources created during a failed bootstrap attempt.
// This helps prevent orphaned resources in the cloud when bootstrap fails partway through.
func (m *Manager) rollbackBootstrap(ctx context.Context) error {
	logger.Infof("Starting bootstrap rollback for bloc=%s", m.options.BlocName)

	_, _ = fmt.Fprintf(os.Stdout, "\n🔄 Rolling back partially-created resources...\n")

	// Get all resources from state that were created during this bootstrap
	if m.stateManager.Current() == nil {
		logger.Warnf("No state available for rollback")

		return ErrNoStateForRollback
	}

	// Collect resources to delete in reverse order (opposite of creation order)
	resourcesToDelete := m.collectRollbackResources()

	if len(resourcesToDelete) == 0 {
		_, _ = fmt.Fprintf(os.Stdout, "  No resources found to rollback\n")

		return nil
	}

	_, _ = fmt.Fprintf(os.Stdout, "  Found %d resources to rollback\n", len(resourcesToDelete))

	// Delete resources in reverse order (bastion -> volumes -> security groups -> subnets -> network)
	deleteOrder := []string{state.ResourceTypeInstance, state.ResourceTypeVolume, state.ResourceTypeKeyPair, state.ResourceTypeBucket, state.ResourceTypePublicIP, state.ResourceTypeSecurityGroup, state.ResourceTypeSubnet, state.ResourceTypeNetwork}
	deletedCount := 0
	failedCount := 0

	for _, resourceType := range deleteOrder {
		resources := resourcesToDelete[resourceType]
		for _, resource := range resources {
			_, _ = fmt.Fprintf(os.Stdout, "  Deleting %s: %s (ID: %s)\n", resource.Type, resource.Name, resource.ID)
			logger.Infof("Rollback: deleting %s %s (ID: %s)", resource.Type, resource.Name, resource.ID)

			err := m.deleteResource(ctx, resource)
			if err != nil {
				logger.Errorf("Failed to delete %s %s: %v", resource.Type, resource.Name, err)
				_, _ = fmt.Fprintf(os.Stderr, "    ✗ Failed: %v\n", err)
				failedCount++

				continue
			}

			// Remove from state
			_ = m.stateManager.RemoveResource(resource.Type, resource.Name)

			_, _ = fmt.Fprintf(os.Stdout, "    ✓ Deleted\n")
			deletedCount++
		}
	}

	// Save state after rollback
	err := m.stateManager.Save()
	if err != nil {
		logger.Warnf("Failed to save state after rollback: %v", err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "\n📊 Rollback summary: %d/%d resources deleted successfully\n", deletedCount, deletedCount+failedCount)

	if failedCount > 0 {
		return fmt.Errorf("%w: %d resources could not be deleted", ErrRollbackPartiallyFailed, failedCount)
	}

	return nil
}

// collectRollbackResources collects all resources from state that should be rolled back.
func (m *Manager) collectRollbackResources() map[string][]*rollbackResource {
	resources := make(map[string][]*rollbackResource)

	for key, resource := range m.stateManager.Current().Resources {
		parts := strings.SplitN(key, ".", resourceKeyParts)
		if len(parts) != resourceKeyParts {
			continue
		}

		resourceType := parts[0]
		resourceName := parts[1]

		resources[resourceType] = append(resources[resourceType], &rollbackResource{
			Type: resourceType,
			ID:   resource.ID,
			Name: resourceName,
		})
	}

	return resources
}

// rollbackResource represents a resource to be rolled back.
type rollbackResource struct {
	Type string
	ID   string
	Name string
}

// deleteResource deletes a single resource during rollback.
func (m *Manager) deleteResource(ctx context.Context, resource *rollbackResource) error {
	switch resource.Type {
	case state.ResourceTypeInstance:
		return m.deleteInstance(ctx, resource.ID)
	case state.ResourceTypeVolume:
		return m.deleteVolume(ctx, resource.ID)
	case state.ResourceTypeKeyPair:
		return m.deleteKeyPair(ctx, resource.ID)
	case state.ResourceTypeBucket:
		return m.deleteBucket(ctx, resource.ID)
	case state.ResourceTypePublicIP:
		return m.deletePublicIP(ctx, resource.ID)
	case state.ResourceTypeSecurityGroup:
		return m.deleteSecurityGroup(ctx, resource.ID)
	case state.ResourceTypeSubnet:
		// For STACKIT, subnets are virtual and don't need deletion
		logger.Debugf("Skipping subnet deletion (virtual): %s", resource.ID)

		return nil
	case state.ResourceTypeNetwork:
		return m.deleteNetwork(ctx, resource.ID)
	default:
		logger.Warnf("Unknown resource type for rollback: %s", resource.Type)

		return nil
	}
}

func (m *Manager) deleteInstance(ctx context.Context, instanceID string) error {
	computeMgr := m.provider.ComputeManager()

	err := computeMgr.DeleteInstance(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to delete instance: %w", err)
	}

	return nil
}

func (m *Manager) deleteVolume(ctx context.Context, volumeID string) error {
	storageMgr := m.provider.StorageManager()
	if storageMgr == nil {
		return nil
	}

	err := storageMgr.DeleteVolume(ctx, volumeID)
	if err != nil {
		return fmt.Errorf("failed to delete volume: %w", err)
	}

	return nil
}

func (m *Manager) deleteKeyPair(ctx context.Context, keypairID string) error {
	computeMgr := m.provider.ComputeManager()

	err := computeMgr.DeleteKeyPair(ctx, keypairID)
	if err != nil {
		return fmt.Errorf("failed to delete keypair: %w", err)
	}

	return nil
}

func (m *Manager) deleteBucket(ctx context.Context, bucketName string) error {
	storageMgr := m.provider.StorageManager()
	if storageMgr == nil {
		return nil
	}

	// Empty bucket first
	_ = storageMgr.EmptyBucket(ctx, bucketName)

	err := storageMgr.DeleteBucket(ctx, bucketName)
	if err != nil {
		return fmt.Errorf("failed to delete bucket: %w", err)
	}

	return nil
}

func (m *Manager) deletePublicIP(ctx context.Context, publicIPID string) error {
	netMgr := m.provider.NetworkManager()
	if netMgr == nil {
		return nil
	}

	err := netMgr.ReleaseFloatingIP(ctx, publicIPID)
	if err != nil {
		return fmt.Errorf("failed to release floating IP: %w", err)
	}

	return nil
}

func (m *Manager) deleteSecurityGroup(ctx context.Context, sgID string) error {
	secMgr := m.provider.SecurityManager()
	if secMgr == nil {
		return nil
	}

	err := secMgr.DeleteSecurityGroup(ctx, sgID)
	if err != nil {
		return fmt.Errorf("failed to delete security group: %w", err)
	}

	return nil
}

func (m *Manager) deleteNetwork(ctx context.Context, networkID string) error {
	netMgr := m.provider.NetworkManager()
	if netMgr == nil {
		return nil
	}

	err := netMgr.DeleteNetwork(ctx, networkID)
	if err != nil {
		return fmt.Errorf("failed to delete network: %w", err)
	}

	return nil
}
