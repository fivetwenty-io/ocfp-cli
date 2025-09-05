package stackit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

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
	AuthToken           string
	ServiceAccountToken string
	ServiceAccountJSON  string
	Region              string
	BaseURL             string
	Timeout             time.Duration
	MaxRetries          int
}

// NewClient creates a new STACKIT client.
func NewClient(config *Config) (*Client, error) {
	if config == nil {
		return nil, errors.New("config is required")
	}

	// Set defaults
	if config.BaseURL == "" {
		// Default to IAAS API endpoint host. Users can override via config base_url/api_endpoint.
		config.BaseURL = "https://iaas.api.stackit.cloud"
	}

	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	if config.MaxRetries == 0 {
		config.MaxRetries = 3
	}

	client := &Client{
		config: config,
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

	if _, err := cli.ListNetworks(ctx, c.config.ProjectID).Execute(); err != nil {
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
func (c *Client) Network() cpi.NetworkManager {
	return c.network
}

// Compute returns the compute manager.
func (c *Client) Compute() cpi.ComputeManager {
	return c.compute
}

// Storage returns the storage manager.
func (c *Client) Storage() cpi.StorageManager {
	return c.storage
}

// Security returns the security manager.
func (c *Client) Security() cpi.SecurityManager {
	return c.security
}

// LoadBalancer returns the load balancer manager.
func (c *Client) LoadBalancer() cpi.LoadBalancerManager {
	return c.loadBalancer
}

// Initialize initializes the provider with configuration.
func (c *Client) Initialize(ctx context.Context, config interface{}) error {
	// Handle different config types
	var cfg *Config
	switch v := config.(type) {
	case *Config:
		cfg = v
	case map[string]interface{}:
		// Config was already parsed in NewProvider, just return success
		// The client is already properly initialized with authentication
		return nil
	default:
		return fmt.Errorf("invalid config type for STACKIT provider: %T", config)
	}

	// Set defaults
	if cfg.BaseURL == "" {
		// Default to IAAS API endpoint host. Users can override via config base_url/api_endpoint.
		cfg.BaseURL = "https://iaas.api.stackit.cloud"
	}

	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
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

// getIAASClient returns a cached IAAS API client, initializing on first use.
func (c *Client) getIAASClient() (*iaas.APIClient, error) {
	if c.iaasClient != nil {
		return c.iaasClient, nil
	}

	opts := []stackitconfig.ConfigurationOption{}

	// Region (e.g., "eu01")
	if c.config.Region != "" {
		opts = append(opts, stackitconfig.WithRegion(c.config.Region))
	}
	// Optional explicit endpoint override
	if c.config.BaseURL != "" {
		opts = append(opts, stackitconfig.WithEndpoint(c.config.BaseURL))
	}

	// Auth selection: prefer service account JSON, then service account token, then auth token
	if c.config.ServiceAccountJSON != "" {
		opts = append(opts, stackitconfig.WithServiceAccountKey(c.config.ServiceAccountJSON))
	} else if c.config.ServiceAccountToken != "" {
		opts = append(opts, stackitconfig.WithToken(c.config.ServiceAccountToken))
	} else if c.config.AuthToken != "" {
		opts = append(opts, stackitconfig.WithToken(c.config.AuthToken))
	}

	cli, err := iaas.NewAPIClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create IAAS client: %w", err)
	}
	// Configure timeout after client creation to avoid nil HTTPClient in options
	if c.config.Timeout > 0 {
		if cli.GetConfig().HTTPClient == nil {
			cli.GetConfig().HTTPClient = &http.Client{}
		}

		cli.GetConfig().HTTPClient.Timeout = c.config.Timeout
	}

	c.iaasClient = cli

	return c.iaasClient, nil
}

// getObjectStorageClient returns a cached Object Storage API client, initializing on first use.
func (c *Client) getObjectStorageClient() (*objectstorage.APIClient, error) {
	if c.objClient != nil {
		return c.objClient, nil
	}

	opts := []stackitconfig.ConfigurationOption{}

	if c.config.Region != "" {
		opts = append(opts, stackitconfig.WithRegion(c.config.Region))
	}

	if c.config.BaseURL != "" {
		opts = append(opts, stackitconfig.WithEndpoint(c.config.BaseURL))
	}

	if c.config.ServiceAccountJSON != "" {
		opts = append(opts, stackitconfig.WithServiceAccountKey(c.config.ServiceAccountJSON))
	} else if c.config.ServiceAccountToken != "" {
		opts = append(opts, stackitconfig.WithToken(c.config.ServiceAccountToken))
	} else if c.config.AuthToken != "" {
		opts = append(opts, stackitconfig.WithToken(c.config.AuthToken))
	}

	cli, err := objectstorage.NewAPIClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create Object Storage client: %w", err)
	}

	if c.config.Timeout > 0 {
		if cli.GetConfig().HTTPClient == nil {
			cli.GetConfig().HTTPClient = &http.Client{}
		}

		cli.GetConfig().HTTPClient.Timeout = c.config.Timeout
	}

	c.objClient = cli

	return c.objClient, nil
}

// getLoadBalancerClient returns a cached Load Balancer API client, initializing on first use.
func (c *Client) getLoadBalancerClient() (*lb.APIClient, error) {
	if c.lbClient != nil {
		return c.lbClient, nil
	}

	opts := []stackitconfig.ConfigurationOption{}
	if c.config.Region != "" {
		opts = append(opts, stackitconfig.WithRegion(c.config.Region))
	}

	if c.config.ServiceAccountJSON != "" {
		opts = append(opts, stackitconfig.WithServiceAccountKey(c.config.ServiceAccountJSON))
	} else if c.config.ServiceAccountToken != "" {
		opts = append(opts, stackitconfig.WithToken(c.config.ServiceAccountToken))
	} else if c.config.AuthToken != "" {
		opts = append(opts, stackitconfig.WithToken(c.config.AuthToken))
	}

	cli, err := lb.NewAPIClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create Load Balancer client: %w", err)
	}

	if c.config.Timeout > 0 {
		if cli.GetConfig().HTTPClient == nil {
			cli.GetConfig().HTTPClient = &http.Client{}
		}

		cli.GetConfig().HTTPClient.Timeout = c.config.Timeout
	}

	c.lbClient = cli

	return c.lbClient, nil
}

// No raw HTTP helpers are needed; all operations use official SDKs
