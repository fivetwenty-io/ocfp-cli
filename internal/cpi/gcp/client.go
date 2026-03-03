// Package gcp implements the CPI provider for Google Cloud Platform.
package gcp

import (
	"context"
	"errors"
	"fmt"
	"sync"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	ocfpconfig "github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	// ProviderName is the name of this provider.
	ProviderName = "gcp"
)

// Client implements the GCP provider.
type Client struct {
	config *Config
	mu     sync.RWMutex

	// Resource managers
	network      *NetworkManager
	compute      *ComputeManager
	storage      *StorageManager
	security     *SecurityManager
	loadBalancer *LoadBalancerManager

	// GCP SDK clients (lazy-loaded)
	instancesClient    *compute.InstancesClient
	disksClient        *compute.DisksClient
	snapshotsClient    *compute.SnapshotsClient
	imagesClient       *compute.ImagesClient
	machineTypesClient *compute.MachineTypesClient
	networksClient     *compute.NetworksClient
	subnetworksClient  *compute.SubnetworksClient
	firewallsClient    *compute.FirewallsClient
	addressesClient    *compute.AddressesClient
	routersClient      *compute.RoutersClient
	storageClient      *storage.Client

	// Load balancing clients
	forwardingRulesClient    *compute.ForwardingRulesClient
	targetPoolsClient        *compute.TargetPoolsClient
	backendServicesClient    *compute.BackendServicesClient
	healthChecksClient       *compute.HealthChecksClient
	regionHealthChecksClient *compute.RegionHealthChecksClient

	clientsLoaded bool
}

// NewClient creates a new GCP client.
func NewClient(config *Config) (*Client, error) {
	// Allow nil config for uninitialized client (will be configured via Initialize)
	if config == nil {
		return &Client{
			config:        nil,
			clientsLoaded: false,
		}, nil
	}

	// Validate configuration
	err := config.Validate()
	if err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	client := &Client{
		config:        config,
		clientsLoaded: false,
	}

	// Initialize resource managers
	client.initializeResourceManagers()

	return client, nil
}

// Name returns the provider name.
func (c *Client) Name() string {
	return ProviderName
}

// Region returns the configured region.
func (c *Client) Region() string {
	if c.config == nil {
		return ""
	}

	return c.config.Region
}

// Zone returns the configured zone.
func (c *Client) Zone() string {
	if c.config == nil {
		return ""
	}

	return c.config.Zone
}

// ProjectID returns the configured project ID.
func (c *Client) ProjectID() string {
	if c.config == nil {
		return ""
	}

	return c.config.ProjectID
}

// Initialize configures the GCP client with the provided configuration.
func (c *Client) Initialize(_ctx context.Context, config interface{}) error {
	// Handle different config types
	cfg, err := c.parseConfig(config)
	if err != nil {
		return err
	}

	// Early return for map config type (already initialized)
	if cfg == nil {
		return nil
	}

	err = cfg.Validate()
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	c.mu.Lock()
	c.config = cfg
	c.mu.Unlock()

	// Initialize resource managers
	c.initializeResourceManagers()

	logger.Debugw("GCP provider initialized",
		"project", c.config.ProjectID,
		"region", c.config.Region,
		"zone", c.config.Zone)

	return nil
}

// Authenticate validates GCP credentials.
func (c *Client) Authenticate(ctx context.Context) error {
	return c.ValidateCredentials(ctx)
}

// ValidateCredentials validates GCP credentials by making a test API call.
func (c *Client) ValidateCredentials(ctx context.Context) error {
	err := c.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	// Try to list projects or make a simple API call to validate credentials
	// Use a simple list call that requires minimal permissions
	c.mu.RLock()
	projectID := c.config.ProjectID
	zone := c.config.Zone
	c.mu.RUnlock()

	// Try to list machine types (low-privilege operation)
	req := &computepb.ListMachineTypesRequest{
		Project:    projectID,
		Zone:       zone,
		MaxResults: proto(uint32(1)),
	}

	it := c.machineTypesClient.List(ctx, req)

	_, err = it.Next()
	if err != nil && !errors.Is(err, iterator.Done) {
		return WrapGCPError(err, "failed to validate GCP credentials")
	}

	logger.Debug("GCP credentials validated successfully")

	return nil
}

// closeable is an interface for clients that can be closed.
type closeable interface {
	Close() error
}

// closeClient closes a client and appends any error to the slice.
func closeClient(c closeable, errs *[]error) {
	if c == nil {
		return
	}

	err := c.Close()
	if err != nil {
		*errs = append(*errs, err)
	}
}

// Cleanup releases resources and closes connections.
//
//nolint:funlen // sequential cleanup steps must remain together
func (c *Client) Cleanup(_ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var errs []error

	closeClient(c.instancesClient, &errs)
	c.instancesClient = nil

	closeClient(c.disksClient, &errs)
	c.disksClient = nil

	closeClient(c.snapshotsClient, &errs)
	c.snapshotsClient = nil

	closeClient(c.imagesClient, &errs)
	c.imagesClient = nil

	closeClient(c.machineTypesClient, &errs)
	c.machineTypesClient = nil

	closeClient(c.networksClient, &errs)
	c.networksClient = nil

	closeClient(c.subnetworksClient, &errs)
	c.subnetworksClient = nil

	closeClient(c.firewallsClient, &errs)
	c.firewallsClient = nil

	closeClient(c.addressesClient, &errs)
	c.addressesClient = nil

	closeClient(c.routersClient, &errs)
	c.routersClient = nil

	closeClient(c.storageClient, &errs)
	c.storageClient = nil

	closeClient(c.forwardingRulesClient, &errs)
	c.forwardingRulesClient = nil

	closeClient(c.targetPoolsClient, &errs)
	c.targetPoolsClient = nil

	closeClient(c.backendServicesClient, &errs)
	c.backendServicesClient = nil

	closeClient(c.healthChecksClient, &errs)
	c.healthChecksClient = nil

	closeClient(c.regionHealthChecksClient, &errs)
	c.regionHealthChecksClient = nil

	c.clientsLoaded = false

	logger.Debug("GCP provider cleaned up")

	if len(errs) > 0 {
		return fmt.Errorf("%w: %v", ErrCleanupFailed, errs)
	}

	return nil
}

// NetworkManager returns the network manager.
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) NetworkManager() cpi.NetworkManager {
	return c.network
}

// Network returns the network manager (legacy).
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) Network() cpi.NetworkManager {
	return c.network
}

// ComputeManager returns the compute manager.
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) ComputeManager() cpi.ComputeManager {
	return c.compute
}

// Compute returns the compute manager (legacy).
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) Compute() cpi.ComputeManager {
	return c.compute
}

// StorageManager returns the storage manager.
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) StorageManager() cpi.StorageManager {
	return c.storage
}

// Storage returns the storage manager (legacy).
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) Storage() cpi.StorageManager {
	return c.storage
}

// SecurityManager returns the security manager.
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) SecurityManager() cpi.SecurityManager {
	return c.security
}

// Security returns the security manager (legacy).
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) Security() cpi.SecurityManager {
	return c.security
}

// LoadBalancerManager returns the load balancer manager.
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) LoadBalancerManager() cpi.LoadBalancerManager {
	return c.loadBalancer
}

// LoadBalancer returns the load balancer manager (legacy).
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) LoadBalancer() cpi.LoadBalancerManager {
	return c.loadBalancer
}

// SupportsStorage indicates whether this provider supports storage operations.
func (c *Client) SupportsStorage() bool {
	return true
}

// parseConfig parses the configuration based on type.
func (c *Client) parseConfig(config interface{}) (*Config, error) {
	switch configValue := config.(type) {
	case *Config:
		return configValue, nil
	case *ocfpconfig.Config:
		return c.convertOCFPConfig(configValue), nil
	case map[string]interface{}:
		return c.handleMapConfig()
	default:
		return nil, fmt.Errorf("%w: expected *gcp.Config or *config.Config, got %T", ErrInvalidConfigType, config)
	}
}

// convertOCFPConfig converts OCFP config to GCP config.
func (c *Client) convertOCFPConfig(configValue *ocfpconfig.Config) *Config {
	cfg := DefaultConfig()

	// Map OCFP config fields to GCP config
	if configValue.ProjectID != "" {
		cfg.ProjectID = configValue.ProjectID
	}

	// Service account JSON can be content or path
	if configValue.ServiceAccountJSON != "" {
		cfg.ServiceAccountJSON = configValue.ServiceAccountJSON
	} else if configValue.ServiceAccountKeyPath != "" {
		cfg.ServiceAccountJSON = configValue.ServiceAccountKeyPath
	}

	if configValue.Region != "" {
		// In GCP, region may be specified as zone
		if len(configValue.Region) > 2 && configValue.Region[len(configValue.Region)-2] == '-' {
			// Looks like a zone (e.g., us-central1-a)
			cfg.Zone = configValue.Region
			cfg.Region = GetRegionFromZone(configValue.Region)
		} else {
			cfg.Region = configValue.Region
		}
	}

	return cfg
}

// handleMapConfig handles map[string]interface{} configuration type.
func (c *Client) handleMapConfig() (*Config, error) {
	// Config was already parsed in NewProvider and stored in c.config
	if c.config != nil && c.network == nil {
		c.initializeResourceManagers()
		logger.Debugw("GCP provider initialized from map config",
			"project", c.config.ProjectID,
			"region", c.config.Region)
	}

	return nil, nil
}

// initializeResourceManagers initializes all resource managers.
func (c *Client) initializeResourceManagers() {
	c.network = &NetworkManager{client: c}
	c.compute = &ComputeManager{client: c}
	c.storage = &StorageManager{client: c}
	c.security = &SecurityManager{client: c}
	c.loadBalancer = &LoadBalancerManager{client: c}
}

// ensureClientsLoaded ensures all GCP SDK clients are loaded.
func (c *Client) ensureClientsLoaded(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.clientsLoaded {
		return nil
	}

	if c.config == nil {
		return ErrClientNotInitialized
	}

	// Get service account credentials
	creds, err := c.config.GetServiceAccountCredentials()
	if err != nil {
		return fmt.Errorf("failed to get service account credentials: %w", err)
	}

	// Create client options
	opts := []option.ClientOption{
		option.WithCredentialsJSON(creds),
	}

	// Add custom endpoint if configured
	if c.config.UseCustomEndpoint && c.config.ComputeEndpoint != "" {
		opts = append(opts, option.WithEndpoint(c.config.ComputeEndpoint))
	}

	err = c.initComputeClients(ctx, opts)
	if err != nil {
		return err
	}

	err = c.initLoadBalancingClients(ctx, opts)
	if err != nil {
		return err
	}

	err = c.initStorageClient(ctx, creds)
	if err != nil {
		return err
	}

	c.clientsLoaded = true

	logger.Debugw("GCP service clients loaded",
		"project", c.config.ProjectID,
		"region", c.config.Region)

	return nil
}

// initComputeClients initializes all GCP compute SDK clients.
//
//nolint:funlen // sequential SDK client initialization for each GCP compute service
func (c *Client) initComputeClients(ctx context.Context, opts []option.ClientOption) error {
	var err error

	c.instancesClient, err = compute.NewInstancesRESTClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create instances client: %w", err)
	}

	c.disksClient, err = compute.NewDisksRESTClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create disks client: %w", err)
	}

	c.snapshotsClient, err = compute.NewSnapshotsRESTClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create snapshots client: %w", err)
	}

	c.imagesClient, err = compute.NewImagesRESTClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create images client: %w", err)
	}

	c.machineTypesClient, err = compute.NewMachineTypesRESTClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create machine types client: %w", err)
	}

	c.networksClient, err = compute.NewNetworksRESTClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create networks client: %w", err)
	}

	c.subnetworksClient, err = compute.NewSubnetworksRESTClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create subnetworks client: %w", err)
	}

	c.firewallsClient, err = compute.NewFirewallsRESTClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create firewalls client: %w", err)
	}

	c.addressesClient, err = compute.NewAddressesRESTClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create addresses client: %w", err)
	}

	c.routersClient, err = compute.NewRoutersRESTClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create routers client: %w", err)
	}

	return nil
}

// initLoadBalancingClients initializes all GCP load balancing SDK clients.
func (c *Client) initLoadBalancingClients(ctx context.Context, opts []option.ClientOption) error {
	var err error

	c.forwardingRulesClient, err = compute.NewForwardingRulesRESTClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create forwarding rules client: %w", err)
	}

	c.targetPoolsClient, err = compute.NewTargetPoolsRESTClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create target pools client: %w", err)
	}

	c.backendServicesClient, err = compute.NewBackendServicesRESTClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create backend services client: %w", err)
	}

	c.healthChecksClient, err = compute.NewHealthChecksRESTClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create health checks client: %w", err)
	}

	c.regionHealthChecksClient, err = compute.NewRegionHealthChecksRESTClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create region health checks client: %w", err)
	}

	return nil
}

// initStorageClient initializes the GCP storage SDK client.
func (c *Client) initStorageClient(ctx context.Context, creds []byte) error {
	storageOpts := []option.ClientOption{
		option.WithCredentialsJSON(creds),
	}

	if c.config.UseCustomEndpoint && c.config.StorageEndpoint != "" {
		storageOpts = append(storageOpts, option.WithEndpoint(c.config.StorageEndpoint))
	}

	var err error

	c.storageClient, err = storage.NewClient(ctx, storageOpts...)
	if err != nil {
		return fmt.Errorf("failed to create storage client: %w", err)
	}

	return nil
}

// getConfig returns the client configuration.
func (c *Client) getConfig() *Config {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.config
}

// Helper function to create pointer to value.
func proto[T any](v T) *T {
	return &v
}
