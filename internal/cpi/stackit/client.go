package stackit

import (
	"context"
	"fmt"
	"net/http"
	"time"

	ocfpconfig "github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	stackitconfig "github.com/stackitcloud/stackit-sdk-go/core/config"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas"
	lb "github.com/stackitcloud/stackit-sdk-go/services/loadbalancer"
	objectstorage "github.com/stackitcloud/stackit-sdk-go/services/objectstorage"
)

// Client implements the STACKIT provider.
type Client struct {
	config *Config

	// Resource managers
	network      *NetworkManager
	compute      *ComputeManager
	storage      *StorageManager
	security     *SecurityManager
	loadBalancer *LoadBalancerManager

	// SDK clients (lazy)
	iaasClient *iaas.APIClient
	lbClient   *lb.APIClient
	objClient  *objectstorage.APIClient
}

// Config holds STACKIT-specific configuration.
type Config struct {
	ProjectID           string
	OrgID               string
	AuthToken           string //nolint:gosec // field name is descriptive, not a hardcoded secret
	ServiceAccountToken string
	ServiceAccountJSON  string
	Region              string
	BaseURL             string
	Timeout             time.Duration
	MaxRetries          int
}

// NewClient creates a new STACKIT client.
func NewClient(config *Config) (*Client, error) {
	// Allow nil config for uninitialized client (will be configured via Initialize)
	if config == nil {
		client := &Client{
			config:       nil,
			network:      nil,
			compute:      nil,
			storage:      nil,
			security:     nil,
			loadBalancer: nil,
			iaasClient:   nil,
			lbClient:     nil,
			objClient:    nil,
		}
		// Don't initialize managers yet since we don't have config
		return client, nil
	}

	// Set defaults
	if config.BaseURL == "" {
		// Default to IAAS API endpoint host. Users can override via config base_url/api_endpoint.
		config.BaseURL = "https://iaas.api.stackit.cloud"
	}

	if config.Timeout == 0 {
		config.Timeout = defaultHTTPTimeout
	}

	if config.MaxRetries == 0 {
		config.MaxRetries = 3
	}

	client := &Client{
		config:       config,
		network:      nil,
		compute:      nil,
		storage:      nil,
		security:     nil,
		loadBalancer: nil,
		iaasClient:   nil,
		lbClient:     nil,
		objClient:    nil,
	}

	// Initialize resource managers
	client.network = &NetworkManager{client: client}
	client.compute = &ComputeManager{client: client}
	client.storage = &StorageManager{client: client}
	client.security = &SecurityManager{client: client}
	client.loadBalancer = &LoadBalancerManager{client: client}

	return client, nil
}

// Provider interface implementation

// Name returns the provider name.
func (c *Client) Name() string {
	return "stackit"
}

// Region returns the configured region.
func (c *Client) Region() string {
	return c.config.Region
}

// Authenticate validates and stores credentials.
func (c *Client) Authenticate(ctx context.Context) error {
	logger.Debug("Authenticating with STACKIT")
	// Validate credentials by performing a lightweight SDK call
	cli, err := c.getIAASClient()
	if err != nil {
		return fmt.Errorf("failed to init IAAS client: %w", err)
	}

	_, err = cli.ListNetworks(ctx, c.config.ProjectID).Execute()
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	logger.Info("Successfully authenticated with STACKIT")

	return nil
}

// ValidateCredentials checks if credentials are valid.
func (c *Client) ValidateCredentials(ctx context.Context) error {
	return c.Authenticate(ctx)
}

// Network returns the network manager.
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) Network() cpi.NetworkManager {
	return c.network
}

// Compute returns the compute manager.
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) Compute() cpi.ComputeManager {
	return c.compute
}

// Storage returns the storage manager.
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) Storage() cpi.StorageManager {
	return c.storage
}

// Security returns the security manager.
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) Security() cpi.SecurityManager {
	return c.security
}

// LoadBalancer returns the load balancer manager.
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) LoadBalancer() cpi.LoadBalancerManager {
	return c.loadBalancer
}

// NetworkManager returns the network manager.
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) NetworkManager() cpi.NetworkManager {
	return c.network
}

// ComputeManager returns the compute manager.
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) ComputeManager() cpi.ComputeManager {
	return c.compute
}

// StorageManager returns the storage manager.
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) StorageManager() cpi.StorageManager {
	return c.storage
}

// SecurityManager returns the security manager.
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) SecurityManager() cpi.SecurityManager {
	return c.security
}

// LoadBalancerManager returns the load balancer manager.
//
//nolint:ireturn // Returns interface by design for manager abstraction
func (c *Client) LoadBalancerManager() cpi.LoadBalancerManager {
	return c.loadBalancer
}

// SupportsStorage returns true as STACKIT supports object storage.
func (c *Client) SupportsStorage() bool {
	return true
}

// Initialize initializes the provider with configuration.
//
//nolint:cyclop,funlen // Multiple config type checks and setup required
func (c *Client) Initialize(ctx context.Context, config interface{}) error {
	// Handle different config types
	var cfg *Config

	switch configValue := config.(type) {
	case *Config:
		cfg = configValue
	case *ocfpconfig.Config:
		// Convert OCFP config to STACKIT config
		cfg = &Config{
			ProjectID:           configValue.ProjectID,
			OrgID:               configValue.OrgID,
			AuthToken:           configValue.AuthToken,
			ServiceAccountToken: configValue.ServiceAccountToken,
			ServiceAccountJSON:  configValue.ServiceAccountJSON,
			Region:              configValue.Region,
			BaseURL:             configValue.APIEndpoint,
			Timeout:             0,
			MaxRetries:          0,
		}
	case map[string]interface{}:
		// Config was already parsed in NewProvider, just return success
		// The client is already properly initialized with authentication
		return nil
	default:
		return ErrInvalidConfigTypeForStackitProvider(config)
	}

	// Validate required fields
	if cfg.ProjectID == "" {
		return ErrProjectIDRequiredForStackitProvider
	}

	if cfg.OrgID == "" {
		return ErrOrgIDRequiredForStackitProvider
	}

	// Check for authentication - prefer service_account_json, then service_account_token, then auth_token
	hasServiceAccountJSON := cfg.ServiceAccountJSON != ""
	hasServiceAccountToken := cfg.ServiceAccountToken != ""
	hasAuthToken := cfg.AuthToken != ""

	if !hasServiceAccountJSON && !hasServiceAccountToken && !hasAuthToken {
		return ErrStackitAuthenticationRequired
	}

	// Set defaults
	if cfg.BaseURL == "" {
		// Default to IAAS API endpoint host. Users can override via config base_url/api_endpoint.
		cfg.BaseURL = "https://iaas.api.stackit.cloud"
	}

	if cfg.Timeout == 0 {
		cfg.Timeout = defaultHTTPTimeout
	}

	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}

	c.config = cfg

	// Initialize resource managers if not already set
	if c.network == nil {
		c.network = &NetworkManager{client: c}
	}

	if c.compute == nil {
		c.compute = &ComputeManager{client: c}
	}

	if c.storage == nil {
		c.storage = &StorageManager{client: c}
	}

	if c.security == nil {
		c.security = &SecurityManager{client: c}
	}

	if c.loadBalancer == nil {
		c.loadBalancer = &LoadBalancerManager{client: c}
	}

	// Authenticate
	err := c.Authenticate(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize STACKIT provider: %w", err)
	}

	return nil
}

// Cleanup performs cleanup operations.
func (c *Client) Cleanup(ctx context.Context) error {
	return nil
}

// buildBaseConfigOptions builds base SDK configuration options (region and auth) for all services.
func (c *Client) buildBaseConfigOptions() []stackitconfig.ConfigurationOption {
	opts := []stackitconfig.ConfigurationOption{}

	if c.config.Region != "" {
		opts = append(opts, stackitconfig.WithRegion(c.config.Region))
	}

	switch {
	case c.config.ServiceAccountJSON != "":
		opts = append(opts, stackitconfig.WithServiceAccountKey(c.config.ServiceAccountJSON))
	case c.config.ServiceAccountToken != "":
		opts = append(opts, stackitconfig.WithToken(c.config.ServiceAccountToken))
	case c.config.AuthToken != "":
		opts = append(opts, stackitconfig.WithToken(c.config.AuthToken))
	}

	return opts
}

// buildIAASConfigOptions builds IAAS-specific configuration options with BaseURL override.
func (c *Client) buildIAASConfigOptions() []stackitconfig.ConfigurationOption {
	opts := c.buildBaseConfigOptions()

	// Apply BaseURL override for IAAS if configured
	if c.config.BaseURL != "" {
		opts = append(opts, stackitconfig.WithEndpoint(c.config.BaseURL))
	}

	return opts
}

// buildObjectStorageConfigOptions builds Object Storage configuration options without BaseURL override.
// This lets the SDK use its built-in object storage endpoint.
func (c *Client) buildObjectStorageConfigOptions() []stackitconfig.ConfigurationOption {
	// Use base options only - let SDK determine the correct object storage endpoint
	return c.buildBaseConfigOptions()
}

// buildLoadBalancerConfigOptions builds Load Balancer configuration options without BaseURL override.
// This lets the SDK use its built-in load balancer endpoint.
func (c *Client) buildLoadBalancerConfigOptions() []stackitconfig.ConfigurationOption {
	// Use base options only - let SDK determine the correct load balancer endpoint
	return c.buildBaseConfigOptions()
}

// httpClientConfigurer is implemented by SDK clients that expose GetConfig().
type httpClientConfigurer interface {
	GetConfig() *stackitconfig.Configuration
}

// applyTimeout configures the HTTP client timeout on a SDK client if set.
func (c *Client) applyTimeout(cli httpClientConfigurer) {
	if c.config.Timeout <= 0 {
		return
	}

	if cli.GetConfig().HTTPClient == nil {
		cli.GetConfig().HTTPClient = &http.Client{
			Transport:     nil,
			CheckRedirect: nil,
			Jar:           nil,
			Timeout:       0,
		}
	}

	cli.GetConfig().HTTPClient.Timeout = c.config.Timeout
}

// getIAASClient returns a cached IAAS API client, initializing on first use.
func (c *Client) getIAASClient() (*iaas.APIClient, error) {
	if c.iaasClient != nil {
		return c.iaasClient, nil
	}

	cli, err := iaas.NewAPIClient(c.buildIAASConfigOptions()...)
	if err != nil {
		return nil, fmt.Errorf("failed to create IAAS client: %w", err)
	}

	c.applyTimeout(cli)

	c.iaasClient = cli

	return c.iaasClient, nil
}

// getObjectStorageClient returns a cached Object Storage API client, initializing on first use.
func (c *Client) getObjectStorageClient() (*objectstorage.APIClient, error) {
	if c.objClient != nil {
		return c.objClient, nil
	}

	cli, err := objectstorage.NewAPIClient(c.buildObjectStorageConfigOptions()...)
	if err != nil {
		return nil, fmt.Errorf("failed to create Object Storage client: %w", err)
	}

	c.applyTimeout(cli)

	c.objClient = cli

	return c.objClient, nil
}

// getLoadBalancerClient returns a cached Load Balancer API client, initializing on first use.
func (c *Client) getLoadBalancerClient() (*lb.APIClient, error) {
	if c.lbClient != nil {
		return c.lbClient, nil
	}

	cli, err := lb.NewAPIClient(c.buildLoadBalancerConfigOptions()...)
	if err != nil {
		return nil, fmt.Errorf("failed to create Load Balancer client: %w", err)
	}

	c.applyTimeout(cli)

	c.lbClient = cli

	return c.lbClient, nil
}

// No raw HTTP helpers are needed; all operations use official SDKs
