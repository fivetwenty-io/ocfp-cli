package state

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// ReconcileOptions holds configuration for reconciliation operations.
type ReconcileOptions struct {
	// DryRun previews changes without modifying state
	DryRun bool

	// Strategy determines how resources are merged
	Strategy MergeStrategy

	// Force skips confirmation prompts
	Force bool
}

// MergeStrategy defines how discovered resources are merged with state.
type MergeStrategy int

const (
	// MergeStrategyAddOnly only adds newly discovered resources.
	MergeStrategyAddOnly MergeStrategy = iota

	// MergeStrategyUpdate adds new resources and updates existing ones.
	MergeStrategyUpdate

	// MergeStrategyFull adds, updates, and removes resources no longer in provider.
	MergeStrategyFull
)

// String returns the string representation of a merge strategy.
func (s MergeStrategy) String() string {
	switch s {
	case MergeStrategyAddOnly:
		return "add-only"
	case MergeStrategyUpdate:
		return "update"
	case MergeStrategyFull:
		return "full"
	default:
		return "unknown"
	}
}

// ParseMergeStrategy converts a string to a MergeStrategy.
func ParseMergeStrategy(strategyStr string) (MergeStrategy, error) {
	switch strategyStr {
	case "add-only":
		return MergeStrategyAddOnly, nil
	case "update":
		return MergeStrategyUpdate, nil
	case "full":
		return MergeStrategyFull, nil
	default:
		return MergeStrategyAddOnly, fmt.Errorf("%w: %s", ErrUnknownMergeStrategy, strategyStr)
	}
}

// ReconcileResult contains the results of a reconciliation operation.
type ReconcileResult struct {
	// ResourcesAdded is the count of new resources added to state
	ResourcesAdded int

	// ResourcesUpdated is the count of existing resources updated
	ResourcesUpdated int

	// ResourcesRemoved is the count of resources removed from state
	ResourcesRemoved int

	// ResourcesUnchanged is the count of resources that didn't change
	ResourcesUnchanged int

	// TotalDiscovered is the total count of resources discovered from provider
	TotalDiscovered int

	// Errors encountered during reconciliation
	Errors []error

	// Duration of the reconciliation operation
	Duration time.Duration

	// DiffSet contains the detailed differences between state and discovered resources
	DiffSet *DiffSet
}

// Reconciler manages state reconciliation with cloud providers.
type Reconciler struct {
	provider cpi.Provider
	manager  *Manager
	blocName string
}

// NewReconciler creates a new state reconciler.
func NewReconciler(provider cpi.Provider, manager *Manager, blocName string) (*Reconciler, error) {
	if provider == nil {
		return nil, ErrProviderNil
	}

	if manager == nil {
		return nil, ErrStateManagerNil
	}

	if blocName == "" {
		return nil, ErrBlocNameEmpty
	}

	return &Reconciler{
		provider: provider,
		manager:  manager,
		blocName: blocName,
	}, nil
}

// Reconcile performs state reconciliation with the cloud provider.
//
// This is the main orchestration method that:
// 1. Loads current state from the state manager
// 2. Discovers all resources from the cloud provider
// 3. Compares discovered resources with current state
// 4. Merges changes according to the specified strategy
// 5. Updates the state file (unless in dry-run mode)
//
// Returns a ReconcileResult summarizing the operation and any errors encountered.
func (r *Reconciler) Reconcile(ctx context.Context, opts ReconcileOptions) (*ReconcileResult, error) {
	startTime := time.Now()
	result := &ReconcileResult{
		Errors: make([]error, 0),
	}

	logger.Infof("Starting state reconciliation for bloc: %s", r.blocName)
	logger.Infof("Merge strategy: %s", opts.Strategy)

	if opts.DryRun {
		logger.Info("DRY RUN mode: no changes will be made to state")
	}

	// Step 1: Load current state
	logger.Info("Loading current state...")

	currentState, err := r.manager.Load(r.blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to load current state: %w", err)
	}

	// Initialize state provider/region if empty
	if currentState.Provider == "" {
		currentState.Provider = r.provider.Name()
	}

	if currentState.Region == "" {
		currentState.Region = r.provider.Region()
	}

	logger.Infof("Current state loaded: %d resources", len(currentState.Resources))

	// Step 2: Discover resources from provider
	logger.Info("Discovering resources from provider...")

	discoveredResources, discoverErrors := r.discoverResources(ctx)
	result.TotalDiscovered = len(discoveredResources)
	result.Errors = append(result.Errors, discoverErrors...)

	if len(discoverErrors) > 0 {
		logger.Warnf("Encountered %d errors during resource discovery", len(discoverErrors))
	}

	logger.Infof("Discovered %d resources from provider", len(discoveredResources))

	// Step 3: Compare resources
	logger.Info("Comparing discovered resources with current state...")

	diffSet := CompareResources(currentState.Resources, discoveredResources)

	logger.Infof("Comparison complete: %s", diffSet.Summary())
	logger.Debugf("Added: %d resources", len(diffSet.Added))
	logger.Debugf("Modified: %d resources", len(diffSet.Modified))
	logger.Debugf("Deleted: %d resources", len(diffSet.Deleted))
	logger.Debugf("Unchanged: %d resources", len(diffSet.Unchanged))

	// Store DiffSet in result for detailed reporting
	result.DiffSet = diffSet

	// Update result statistics
	// Set the counts from diffSet (same for both dry-run and non-dry-run)
	result.ResourcesAdded = len(diffSet.Added)
	result.ResourcesUpdated = len(diffSet.Modified)
	result.ResourcesRemoved = len(diffSet.Deleted)
	result.ResourcesUnchanged = len(diffSet.Unchanged)

	// Step 4: Merge changes
	if !opts.DryRun {
		logger.Info("Merging changes according to strategy...")

		mergeOpts := MergeOptions{
			Strategy:         opts.Strategy,
			PreserveDeleted:  false,
			UpdateTimestamps: true,
		}

		mergeResult, err := MergeResources(currentState, diffSet, mergeOpts)
		if err != nil {
			return nil, fmt.Errorf("failed to merge resources: %w", err)
		}

		// Update result with actual merge counts
		result.ResourcesAdded = mergeResult.ResourcesAdded
		result.ResourcesUpdated = mergeResult.ResourcesUpdated
		result.ResourcesRemoved = mergeResult.ResourcesDeleted
		result.ResourcesUnchanged = len(diffSet.Unchanged)

		logger.Infof("Merge complete: added=%d updated=%d removed=%d skipped=%d",
			mergeResult.ResourcesAdded,
			mergeResult.ResourcesUpdated,
			mergeResult.ResourcesDeleted,
			mergeResult.ResourcesSkipped)
	} else {
		logger.Info("Skipping merge (dry-run mode)")
	}

	// Step 5: Update state file
	if !opts.DryRun {
		logger.Info("Updating state file with automatic backup...")

		err = r.manager.SaveWithBackup()
		if err != nil {
			return nil, fmt.Errorf("failed to save state: %w", err)
		}

		logger.Info("State file updated successfully with backup")
	} else {
		logger.Info("Skipping state update (dry-run mode)")
	}

	// Calculate duration
	result.Duration = time.Since(startTime)

	logger.Infof("Reconciliation complete in %s", result.Duration)
	logger.Infof("Summary: added=%d updated=%d removed=%d unchanged=%d",
		result.ResourcesAdded,
		result.ResourcesUpdated,
		result.ResourcesRemoved,
		result.ResourcesUnchanged)

	return result, nil
}

// ValidateProvider ensures the provider is properly initialized.
func (r *Reconciler) ValidateProvider(ctx context.Context) error {
	logger.Debug("Validating provider credentials...")

	err := r.provider.ValidateCredentials(ctx)
	if err != nil {
		return fmt.Errorf("provider credential validation failed: %w", err)
	}

	logger.Debugf("Provider validated: %s (region: %s)", r.provider.Name(), r.provider.Region())

	return nil
}

// discoveryResult holds resources and errors from a discovery operation.
type discoveryResult struct {
	resources []*Resource
	errors    []error
	category  string
}

// discoverResources discovers all resources from the provider using parallel execution.
func (r *Reconciler) discoverResources(ctx context.Context) ([]*Resource, []error) {
	const (
		// resourceCategoryCount is the number of resource categories we discover in parallel.
		resourceCategoryCount = 3
	)

	var waitGroup sync.WaitGroup

	results := make(chan discoveryResult, resourceCategoryCount)

	// Launch parallel discovery goroutines for each resource category
	waitGroup.Add(resourceCategoryCount)

	go func() {
		defer waitGroup.Done()

		resources, errors := r.discoverNetworkResources(ctx)
		results <- discoveryResult{
			resources: resources,
			errors:    errors,
			category:  string(CategoryNetwork),
		}
	}()

	go func() {
		defer waitGroup.Done()

		resources, errors := r.discoverComputeResources(ctx)
		results <- discoveryResult{
			resources: resources,
			errors:    errors,
			category:  string(CategoryCompute),
		}
	}()

	go func() {
		defer waitGroup.Done()

		resources, errors := r.discoverStorageResources(ctx)
		results <- discoveryResult{
			resources: resources,
			errors:    errors,
			category:  string(CategoryStorage),
		}
	}()

	// Close results channel when all goroutines complete
	go func() {
		waitGroup.Wait()
		close(results)
	}()

	// Aggregate results
	allResources := make([]*Resource, 0)
	allErrors := make([]error, 0)

	for result := range results {
		logger.Debugf("Discovered %d %s resources", len(result.resources), result.category)
		allResources = append(allResources, result.resources...)
		allErrors = append(allErrors, result.errors...)
	}

	return allResources, allErrors
}

// discoverNetworkResources discovers all network-related resources.
func (r *Reconciler) discoverNetworkResources(ctx context.Context) ([]*Resource, []error) {
	logger.Debug("Discovering network resources...")

	networkMgr := r.provider.NetworkManager()
	if networkMgr == nil {
		logger.Warn("Network manager not available for this provider")

		return nil, nil
	}

	resources := make([]*Resource, 0)
	errors := make([]error, 0)

	// Discover networks/VPCs
	networks, err := networkMgr.ListNetworks(ctx, nil)
	if err != nil {
		logger.Warnf("Failed to list networks: %v", err)
		errors = append(errors, fmt.Errorf("list networks: %w", err))
	} else {
		for _, network := range networks {
			resource := &Resource{
				ID:       network.ID,
				Type:     ResourceTypeNetwork,
				Name:     network.Name,
				Provider: r.provider.Name(),
				State:    string(network.State),
				Properties: map[string]interface{}{
					"cidr":   network.CIDR,
					"region": network.Region,
				},
				Tags:      network.Tags,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			resources = append(resources, resource)
		}

		logger.Debugf("Discovered %d networks", len(networks))
	}

	// Discover subnets
	subnets, err := networkMgr.ListSubnets(ctx, "")
	if err != nil {
		logger.Warnf("Failed to list subnets: %v", err)
		errors = append(errors, fmt.Errorf("list subnets: %w", err))
	} else {
		for _, subnet := range subnets {
			resource := &Resource{
				ID:       subnet.ID,
				Type:     ResourceTypeSubnet,
				Name:     subnet.Name,
				Provider: r.provider.Name(),
				State:    string(subnet.State),
				Properties: map[string]interface{}{
					"cidr":        subnet.CIDR,
					"network_id":  subnet.NetworkID,
					"subnet_type": subnet.Type,
				},
				Tags:      subnet.Tags,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			resources = append(resources, resource)
		}

		logger.Debugf("Discovered %d subnets", len(subnets))
	}

	// Discover security groups
	secGroups, err := networkMgr.ListSecurityGroups(ctx, nil)
	if err != nil {
		logger.Warnf("Failed to list security groups: %v", err)
		errors = append(errors, fmt.Errorf("list security groups: %w", err))
	} else {
		for _, secGroup := range secGroups {
			resource := &Resource{
				ID:       secGroup.ID,
				Type:     ResourceTypeSecurityGroup,
				Name:     secGroup.Name,
				Provider: r.provider.Name(),
				State:    "active",
				Properties: map[string]interface{}{
					"description": secGroup.Description,
					"rules_count": len(secGroup.Rules),
				},
				Tags:      secGroup.Tags,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			resources = append(resources, resource)
		}

		logger.Debugf("Discovered %d security groups", len(secGroups))
	}

	// Discover public IPs
	publicIPs, err := networkMgr.ListPublicIPs(ctx)
	if err != nil {
		logger.Warnf("Failed to list public IPs: %v", err)
		errors = append(errors, fmt.Errorf("list public IPs: %w", err))
	} else {
		for _, pip := range publicIPs {
			resource := &Resource{
				ID:       pip.ID,
				Type:     ResourceTypePublicIP,
				Name:     pip.Name,
				Provider: r.provider.Name(),
				State:    pip.Status,
				Properties: map[string]interface{}{
					"ip_address":  pip.IPAddress,
					"instance_id": pip.InstanceID,
					"job":         pip.Job,
				},
				Tags:      pip.Tags,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			resources = append(resources, resource)
		}

		logger.Debugf("Discovered %d public IPs", len(publicIPs))
	}

	// Discover load balancers
	loadBalancers, err := networkMgr.ListLoadBalancers(ctx, nil)
	if err != nil {
		logger.Warnf("Failed to list load balancers: %v", err)
		errors = append(errors, fmt.Errorf("list load balancers: %w", err))
	} else {
		for _, loadBalancer := range loadBalancers {
			// Convert Tags []string to map[string]string
			tags := make(map[string]string)
			for _, tag := range loadBalancer.Tags {
				tags[tag] = "true"
			}

			resource := &Resource{
				ID:       loadBalancer.ID,
				Type:     ResourceTypeLoadBalancer,
				Name:     loadBalancer.Name,
				Provider: r.provider.Name(),
				State:    string(loadBalancer.State),
				Properties: map[string]interface{}{
					"ip_address":    loadBalancer.IPAddress,
					"type":          loadBalancer.Type,
					"protocol":      loadBalancer.Protocol,
					"backend_count": len(loadBalancer.Backends),
				},
				Tags:      tags,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			resources = append(resources, resource)
		}

		logger.Debugf("Discovered %d load balancers", len(loadBalancers))
	}

	logger.Infof("Network discovery complete: %d resources, %d errors", len(resources), len(errors))

	return resources, errors
}

// discoverComputeResources discovers all compute-related resources.
func (r *Reconciler) discoverComputeResources(ctx context.Context) ([]*Resource, []error) {
	logger.Debug("Discovering compute resources...")

	computeMgr := r.provider.ComputeManager()
	if computeMgr == nil {
		logger.Warn("Compute manager not available for this provider")

		return nil, nil
	}

	resources := make([]*Resource, 0)
	errors := make([]error, 0)

	// Discover instances
	instances, err := computeMgr.ListInstances(ctx, nil)
	if err != nil {
		logger.Warnf("Failed to list instances: %v", err)
		errors = append(errors, fmt.Errorf("list instances: %w", err))
	} else {
		for _, instance := range instances {
			ipAddresses := make([]string, 0)
			if instance.PrivateIP != "" {
				ipAddresses = append(ipAddresses, instance.PrivateIP)
			}

			if instance.PublicIP != "" {
				ipAddresses = append(ipAddresses, instance.PublicIP)
			}

			if instance.FloatingIP != "" {
				ipAddresses = append(ipAddresses, instance.FloatingIP)
			}

			resource := &Resource{
				ID:       instance.ID,
				Type:     ResourceTypeInstance,
				Name:     instance.Name,
				Provider: r.provider.Name(),
				State:    string(instance.State),
				Properties: map[string]interface{}{
					"flavor":       instance.Flavor,
					"image":        instance.Image,
					"ip_addresses": ipAddresses,
					"network_id":   instance.NetworkID,
					"key_pair":     instance.KeyPair,
				},
				Tags:      instance.Tags,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			resources = append(resources, resource)
		}

		logger.Debugf("Discovered %d instances", len(instances))
	}

	// Discover key pairs
	keyPairs, err := computeMgr.ListKeyPairs(ctx)
	if err != nil {
		logger.Warnf("Failed to list key pairs: %v", err)
		errors = append(errors, fmt.Errorf("list key pairs: %w", err))
	} else {
		for _, keyPair := range keyPairs {
			resource := &Resource{
				ID:       keyPair.ID,
				Type:     ResourceTypeKeyPair,
				Name:     keyPair.Name,
				Provider: r.provider.Name(),
				State:    "active",
				Properties: map[string]interface{}{
					"fingerprint": keyPair.Fingerprint,
				},
				Tags:      make(map[string]string),
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			resources = append(resources, resource)
		}

		logger.Debugf("Discovered %d key pairs", len(keyPairs))
	}

	// Discover volumes
	volumes, err := computeMgr.ListVolumes(ctx, nil)
	if err != nil {
		logger.Warnf("Failed to list volumes: %v", err)
		errors = append(errors, fmt.Errorf("list volumes: %w", err))
	} else {
		for _, volume := range volumes {
			resource := &Resource{
				ID:       volume.ID,
				Type:     ResourceTypeVolume,
				Name:     volume.Name,
				Provider: r.provider.Name(),
				State:    string(volume.State),
				Properties: map[string]interface{}{
					"size_gb":     volume.Size,
					"volume_type": volume.Type,
					"encrypted":   volume.Encrypted,
					"attached_to": volume.AttachedTo,
				},
				Tags:      volume.Tags,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			resources = append(resources, resource)
		}

		logger.Debugf("Discovered %d volumes", len(volumes))
	}

	logger.Infof("Compute discovery complete: %d resources, %d errors", len(resources), len(errors))

	return resources, errors
}

// discoverStorageResources discovers all storage-related resources.
func (r *Reconciler) discoverStorageResources(ctx context.Context) ([]*Resource, []error) {
	logger.Debug("Discovering storage resources...")

	storageMgr := r.provider.StorageManager()
	if storageMgr == nil {
		logger.Warn("Storage manager not available for this provider")

		return nil, nil
	}

	resources := make([]*Resource, 0)
	errors := make([]error, 0)

	// Check if provider supports storage
	if !r.provider.SupportsStorage() {
		logger.Debug("Provider does not support object storage")

		return resources, errors
	}

	// Discover buckets
	buckets, err := storageMgr.ListBuckets(ctx)
	if err != nil {
		logger.Warnf("Failed to list buckets: %v", err)
		errors = append(errors, fmt.Errorf("list buckets: %w", err))
	} else {
		for _, bucket := range buckets {
			resource := &Resource{
				ID:       bucket.Name, // Buckets use name as ID
				Type:     ResourceTypeBucket,
				Name:     bucket.Name,
				Provider: r.provider.Name(),
				State:    "active",
				Properties: map[string]interface{}{
					"region": bucket.Region,
				},
				Tags:      make(map[string]string),
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			resources = append(resources, resource)
		}

		logger.Debugf("Discovered %d buckets", len(buckets))
	}

	logger.Infof("Storage discovery complete: %d resources, %d errors", len(resources), len(errors))

	return resources, errors
}
