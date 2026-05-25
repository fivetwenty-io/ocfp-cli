package bootstrap

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// Storage-specific constants.
const (
	buildpacksRetentionDays = 30
	dropletsRetentionDays   = 7
	packagesRetentionDays   = 14
	blobstoreRetentionDays  = 90
	retryDelaySeconds       = 2
	providerStackit         = "stackit"

	// defaultBucketCapacity is the default capacity for preallocating bucket name slices.
	// Accounts for minimum expected buckets: 3 mgmt (bosh, artifacts, shield) + 3 ocf (bosh, artifacts, blobstore).
	defaultBucketCapacity = 6
)

// Bucket policy interfaces.
type (
	bucketVersioner interface {
		SetBucketVersioning(ctx context.Context, name string, enabled bool) error
	}

	bucketLifecycler interface {
		SetBucketLifecycle(ctx context.Context, name string, noncurrentDays int) error
	}
)

// ==============================================================================
// Bucket Creation
// ==============================================================================

// CreateBuckets creates the required storage buckets.
func (m *Manager) CreateBuckets(ctx context.Context) error {
	if !m.provider.SupportsStorage() {
		logger.Infof("Provider %s does not support storage, skipping bucket creation", m.options.Provider)

		return nil
	}

	storage := m.provider.StorageManager()
	bucketNames := m.getRequiredBucketNames()

	logger.Infof("Creating %d buckets", len(bucketNames))

	existing := m.getExistingBuckets(ctx, storage)

	// Ensure credentials bucket group exists
	m.ensureCredentialsGroup(ctx, storage)

	// Process each bucket
	var errs []error

	for _, name := range bucketNames {
		err := m.processBucket(ctx, storage, name, existing)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to process bucket %s: %w", name, err))
		}
	}

	if len(errs) > 0 {
		return ErrBucketCreationErrors(errs)
	}

	logger.Infof("All buckets processed successfully")

	return nil
}

func (m *Manager) getRequiredBucketNames() []string {
	// Pre-allocate with known minimum capacity (3 mgmt + 3 ocf)
	buckets := make([]string, 0, defaultBucketCapacity)

	// mgmt environment buckets
	mgmtBuckets := []string{
		m.options.BlocName + "-mgmt-bosh",
		m.options.BlocName + "-mgmt-artifacts",
		m.options.BlocName + "-mgmt-shield",
	}
	buckets = append(buckets, mgmtBuckets...)

	// ocf environment buckets
	ocfBuckets := []string{
		m.options.BlocName + "-ocf-bosh",
		m.options.BlocName + "-ocf-artifacts",
		m.options.BlocName + "-ocf-cf-packages",
		m.options.BlocName + "-ocf-cf-droplets",
		m.options.BlocName + "-ocf-cf-buildpacks",
		m.options.BlocName + "-ocf-cf-resource-pool",
		m.options.BlocName + "-ocf-shield",
	}
	buckets = append(buckets, ocfBuckets...)

	// Add any additional configured buckets
	for _, bucket := range m.config.Buckets {
		buckets = append(buckets, bucket.Name)
	}

	return buckets
}

func (m *Manager) getExistingBuckets(ctx context.Context, storage cpi.StorageManager) map[string]bool {
	existing := make(map[string]bool)

	buckets, err := storage.ListBuckets(ctx)
	if err != nil {
		logger.Warnf("Failed to list existing buckets: %v", err)

		return existing
	}

	for _, bucket := range buckets {
		existing[bucket.Name] = true
	}

	return existing
}

func (m *Manager) ensureCredentialsGroup(ctx context.Context, storage cpi.StorageManager) {
	// Skip if provider doesn't need credential groups
	if m.options.Provider != providerStackit {
		return
	}

	credentialsGroupName := m.options.BlocName + "-credentials"

	if existingGroup, _ := m.stateManager.GetResource("credentials_group", credentialsGroupName); existingGroup != nil {
		logger.Infof("Credentials group %s already exists", credentialsGroupName)

		return
	}

	logger.Infof("Creating credentials group: %s", credentialsGroupName)

	// Create credentials group using storage interface
	credentialsGroup, err := storage.CreateCredentialsGroup(ctx, &cpi.CredentialsGroupRequest{
		Name: credentialsGroupName,
		Tags: m.baseTags(),
	})
	if err != nil {
		logger.Errorf("Failed to create credentials group: %v", err)

		return
	}

	// Save to state
	err = m.stateManager.AddResource(&state.Resource{
		ID:         credentialsGroup.ID,
		Type:       "credentials_group",
		Name:       credentialsGroupName,
		Provider:   m.options.Provider,
		State:      string(cpi.ResourceStateActive),
		Properties: make(map[string]interface{}),
		Tags:       m.baseTags(),
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	})
	if err != nil {
		logger.Errorf("Failed to save credentials group to state: %v", err)
	}

	logger.Infof("Credentials group created successfully: %s", credentialsGroup.ID)
}

// ==============================================================================
// Bucket Operations
// ==============================================================================

func (m *Manager) processBucket(ctx context.Context, storage cpi.StorageManager, name string, existing map[string]bool) error {
	if existing[name] {
		_, _ = fmt.Fprintf(os.Stdout, "    • Bucket %s already exists, configuring policies\n", name)
		logger.Infof("Bucket %s already exists, configuring policies", name)
		m.configureBucketPolicies(ctx, storage, name)

		return nil
	}

	// Create new bucket
	_, _ = fmt.Fprintf(os.Stdout, "    • Creating bucket %s...\n", name)

	err := m.createBucket(ctx, storage, name)
	if err != nil {
		return err
	}

	// Configure policies for new bucket
	m.configureBucketPolicies(ctx, storage, name)

	return nil
}

func (m *Manager) createBucket(ctx context.Context, storage cpi.StorageManager, name string) error {
	logger.Infof("Creating bucket: %s", name)

	bucket, err := storage.CreateBucket(ctx, &cpi.BucketRequest{
		Name: name,
		Tags: m.baseTags(),
	})
	if err != nil {
		return fmt.Errorf("failed to create bucket: %w", err)
	}

	// Save bucket to state
	err = m.stateManager.AddResource(&state.Resource{
		ID:         bucket.ID,
		Type:       state.ResourceTypeBucket,
		Name:       name,
		Provider:   m.options.Provider,
		State:      string(cpi.ResourceStateActive),
		Properties: map[string]interface{}{"region": bucket.Region},
		Tags:       m.baseTags(),
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	})
	if err != nil {
		return fmt.Errorf("failed to save bucket to state: %w", err)
	}

	// Set output
	_ = m.stateManager.SetOutput(fmt.Sprintf("bucket_%s_name", strings.ReplaceAll(name, "-", "_")), name)

	logger.Infof("Bucket created successfully: %s", name)

	return nil
}

// ==============================================================================
// Bucket Policy Configuration
// ==============================================================================

func (m *Manager) configureBucketPolicies(ctx context.Context, storage cpi.StorageManager, name string) {
	if !m.shouldEnablePolicies() {
		return
	}

	versionProvider, lifecycleProvider := m.getBucketPolicyProviders(storage)
	if versionProvider == nil || lifecycleProvider == nil {
		logger.Warnf("Provider does not support bucket policies for %s", name)

		return
	}

	m.configureBucketByType(ctx, name, versionProvider, lifecycleProvider)
}

func (m *Manager) shouldEnablePolicies() bool {
	if m.config == nil {
		return false
	}

	return m.config.Blobstore.EnablePolicies || len(m.config.Buckets) > 0
}

func (m *Manager) getBucketPolicyProviders(storage cpi.StorageManager) (bucketVersioner, bucketLifecycler) { //nolint:ireturn // interface type checking
	if versioner, ok := storage.(bucketVersioner); ok {
		if lifecycler, ok := storage.(bucketLifecycler); ok {
			return versioner, lifecycler
		}
	}

	return nil, nil
}

func (m *Manager) configureBucketByType(ctx context.Context, name string, versionProvider bucketVersioner, lifecycleProvider bucketLifecycler) {
	switch {
	case strings.Contains(name, "buildpacks"):
		m.configureCFBuildpacksBucket(ctx, name, versionProvider, lifecycleProvider)
	case strings.Contains(name, "droplets"):
		m.configureCFDropletsBucket(ctx, name, versionProvider, lifecycleProvider)
	case strings.Contains(name, "app-packages") || strings.Contains(name, "packages"):
		m.configureCFAppPackagesBucket(ctx, name, versionProvider, lifecycleProvider)
	case strings.Contains(name, "bosh"):
		m.configureBoshBlobstoreBucket(ctx, name, versionProvider, lifecycleProvider)
	default:
		logger.Infof("No specific policy configuration for bucket: %s", name)
	}
}

func (m *Manager) configureCFBuildpacksBucket(ctx context.Context, name string, versionProvider bucketVersioner, lifecycleProvider bucketLifecycler) {
	versioning, days := m.blobstoreSettingsFor(name, m.config.Blobstore.CFBuildpacks, false, buildpacksRetentionDays)
	m.applyBucketSettings(ctx, name, versioning, days, versionProvider, lifecycleProvider)
}

func (m *Manager) configureCFDropletsBucket(ctx context.Context, name string, versionProvider bucketVersioner, lifecycleProvider bucketLifecycler) {
	versioning, days := m.blobstoreSettingsFor(name, m.config.Blobstore.CFDroplets, true, dropletsRetentionDays)
	m.applyBucketSettings(ctx, name, versioning, days, versionProvider, lifecycleProvider)
}

func (m *Manager) configureCFAppPackagesBucket(ctx context.Context, name string, versionProvider bucketVersioner, lifecycleProvider bucketLifecycler) {
	versioning, days := m.blobstoreSettingsFor(name, m.config.Blobstore.CFAppPackages, true, packagesRetentionDays)
	m.applyBucketSettings(ctx, name, versioning, days, versionProvider, lifecycleProvider)
}

func (m *Manager) configureBoshBlobstoreBucket(ctx context.Context, name string, versionProvider bucketVersioner, lifecycleProvider bucketLifecycler) {
	versioning, days := m.blobstoreSettingsFor(name, m.config.Blobstore.BoshBlobstore, true, blobstoreRetentionDays)
	m.applyBucketSettings(ctx, name, versioning, days, versionProvider, lifecycleProvider)
}

//nolint:unparam // bucketName reserved for future use in per-bucket configuration
func (m *Manager) blobstoreSettingsFor(_bucketName string, settings config.BucketSettings, defaultVersioning bool, defaultDays int) (bool, int) {
	if !m.config.Blobstore.EnablePolicies {
		return defaultVersioning, defaultDays
	}

	versioning := settings.Versioning

	days := settings.NoncurrentDays
	if !m.isValidNoncurrentDays(days) {
		days = defaultDays
	}

	return versioning, days
}

func (m *Manager) applyBucketSettings(ctx context.Context, name string, versioning bool, noncurrentDays int, versionProvider bucketVersioner, lifecycleProvider bucketLifecycler) {
	if versioning {
		err := versionProvider.SetBucketVersioning(ctx, name, versioning)
		if err != nil {
			logger.Warnf("Failed to configure versioning for %s: %v", name, err)
		} else {
			logger.Infof("Configured versioning for %s: %t", name, versioning)
		}
	}

	if m.isValidNoncurrentDays(noncurrentDays) {
		err := lifecycleProvider.SetBucketLifecycle(ctx, name, noncurrentDays)
		if err != nil {
			logger.Warnf("Failed to configure lifecycle for %s: %v", name, err)
		} else {
			logger.Infof("Configured lifecycle for %s: %d days", name, noncurrentDays)
		}
	}

	m.updateBucketResourceProperties(name, versioning, noncurrentDays)
}

func (m *Manager) isValidNoncurrentDays(days int) bool {
	return days > 0 && days <= 365
}

func (m *Manager) updateBucketResourceProperties(name string, versioning bool, noncurrentDays int) {
	if resource, _ := m.stateManager.GetResource("bucket", name); resource != nil {
		if resource.Properties == nil {
			resource.Properties = make(map[string]interface{})
		}

		resource.Properties["versioning"] = versioning

		if noncurrentDays > 0 {
			resource.Properties["lifecycle_noncurrent_days"] = noncurrentDays
		}

		_ = m.stateManager.UpdateResource(resource)
	}
}

// ==============================================================================
// Display Functions
// ==============================================================================
