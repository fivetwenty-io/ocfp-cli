// Package azure implements the CPI provider for Microsoft Azure.
package azure

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"

	ocfpconfig "github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	// ProviderName is the name of this provider.
	ProviderName = "azure"
)

// Client implements the Azure provider.
type Client struct {
	config *Config
	mu     sync.RWMutex

	// Azure credentials
	credential azcore.TokenCredential

	// ARM client options
	armClientOptions *arm.ClientOptions

	// Resource managers
	network      *NetworkManager
	compute      *ComputeManager
	storage      *StorageManager
	security     *SecurityManager
	loadBalancer *LoadBalancerManager

	// SDK clients (lazy-loaded)
	resourceGroupsClient        *armresources.ResourceGroupsClient
	virtualNetworksClient       *armnetwork.VirtualNetworksClient
	subnetsClient               *armnetwork.SubnetsClient
	publicIPAddressesClient     *armnetwork.PublicIPAddressesClient
	networkSecurityGroupsClient *armnetwork.SecurityGroupsClient
	securityRulesClient         *armnetwork.SecurityRulesClient
	routeTablesClient           *armnetwork.RouteTablesClient
	loadBalancersClient         *armnetwork.LoadBalancersClient
	virtualMachinesClient       *armcompute.VirtualMachinesClient
	disksClient                 *armcompute.DisksClient
	snapshotsClient             *armcompute.SnapshotsClient
	imagesClient                *armcompute.ImagesClient
	sshPublicKeysClient         *armcompute.SSHPublicKeysClient
	virtualMachineSizesClient   *armcompute.VirtualMachineSizesClient
	storageAccountsClient       *armstorage.AccountsClient
	blobContainersClient        *armstorage.BlobContainersClient

	clientsLoaded bool
}

// NewClient creates a new Azure client.
func NewClient(config *Config) (*Client, error) {
	// Allow nil config for uninitialized client (will be configured via Initialize)
	if config == nil {
		return &Client{
			config:        nil,
			credential:    nil,
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
		credential:    nil,
		clientsLoaded: false,
	}

	// Initialize resource managers
	client.network = &NetworkManager{client: client}
	client.compute = &ComputeManager{client: client}
	client.storage = &StorageManager{client: client}
	client.security = &SecurityManager{client: client}
	client.loadBalancer = &LoadBalancerManager{client: client}

	return client, nil
}

// Name returns the provider name.
func (c *Client) Name() string {
	return ProviderName
}

// Region returns the configured location (Azure's term for region).
func (c *Client) Region() string {
	if c.config == nil {
		return ""
	}

	return c.config.Location
}

// Initialize configures the Azure client with the provided configuration.
func (c *Client) Initialize(ctx context.Context, config interface{}) error {
	// Handle different config types
	cfg, err := c.parseConfig(ctx, config)
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

	// Initialize Azure credentials
	err = c.initializeCredential(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize Azure credentials: %w", err)
	}

	// Initialize resource managers
	c.initializeResourceManagers()

	logger.Debugw("Azure provider initialized", "location", c.config.Location, "subscription", c.config.SubscriptionID)

	return nil
}

// Authenticate validates Azure credentials.
func (c *Client) Authenticate(ctx context.Context) error {
	return c.ValidateCredentials(ctx)
}

// ValidateCredentials validates Azure credentials by attempting to list resource groups.
func (c *Client) ValidateCredentials(ctx context.Context) error {
	err := c.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	// Try to get the configured resource group to validate credentials
	_, err = c.resourceGroupsClient.Get(ctx, c.config.ResourceGroup, nil)
	if err != nil {
		// If resource group doesn't exist but we have valid credentials, that's okay
		// The error will indicate authentication vs not-found issues
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) {
			if respErr.StatusCode == http.StatusNotFound && c.config.CreateResourceGroup {
				// Resource group doesn't exist but we can create it later
				logger.Debug("Azure credentials validated, resource group will be created")

				return nil
			}
		}

		return WrapAzureError(err, "failed to validate Azure credentials")
	}

	logger.Debug("Azure credentials validated successfully")

	return nil
}

// Cleanup releases resources and closes connections.
func (c *Client) Cleanup(_ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Reset clients
	c.resourceGroupsClient = nil
	c.virtualNetworksClient = nil
	c.subnetsClient = nil
	c.publicIPAddressesClient = nil
	c.networkSecurityGroupsClient = nil
	c.securityRulesClient = nil
	c.routeTablesClient = nil
	c.loadBalancersClient = nil
	c.virtualMachinesClient = nil
	c.disksClient = nil
	c.snapshotsClient = nil
	c.imagesClient = nil
	c.sshPublicKeysClient = nil
	c.virtualMachineSizesClient = nil
	c.storageAccountsClient = nil
	c.blobContainersClient = nil
	c.credential = nil
	c.clientsLoaded = false

	logger.Debug("Azure provider cleaned up")

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

// EnsureResourceGroup creates the resource group if it doesn't exist and CreateResourceGroup is true.
func (c *Client) EnsureResourceGroup(ctx context.Context) error {
	err := c.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	// Check if resource group exists
	_, err = c.resourceGroupsClient.Get(ctx, c.config.ResourceGroup, nil)
	if err == nil {
		// Resource group exists
		return nil
	}

	// Check if we should create it
	if !c.config.CreateResourceGroup {
		return fmt.Errorf("%w: %s", ErrResourceGroupNotCreatable, c.config.ResourceGroup)
	}

	// Create the resource group
	_, err = c.resourceGroupsClient.CreateOrUpdate(ctx, c.config.ResourceGroup, armresources.ResourceGroup{
		Location: &c.config.Location,
		Tags:     c.buildDefaultTags(),
	}, nil)
	if err != nil {
		return WrapAzureError(err, "failed to create resource group")
	}

	logger.Infow("Created resource group", "name", c.config.ResourceGroup, "location", c.config.Location)

	return nil
}

// parseConfig parses the configuration based on type and returns a Config or nil for map types.
func (c *Client) parseConfig(ctx context.Context, config interface{}) (*Config, error) {
	switch configValue := config.(type) {
	case *Config:
		return configValue, nil
	case *ocfpconfig.Config:
		return c.convertOCFPConfig(configValue), nil
	case map[string]interface{}:
		return c.handleMapConfig(ctx)
	default:
		//nolint:err113 // Dynamic error with type info is appropriate here
		return nil, fmt.Errorf("invalid config type for Azure provider: expected *azure.Config or *config.Config, got %T", config)
	}
}

// convertOCFPConfig converts OCFP config to Azure config.
func (c *Client) convertOCFPConfig(configValue *ocfpconfig.Config) *Config {
	cfg := DefaultConfig()

	// Map common fields
	cfg.SubscriptionID = configValue.ProjectID // ProjectID maps to SubscriptionID for Azure
	cfg.Location = configValue.Region
	cfg.ResourceGroup = configValue.Name // Use bloc name as resource group

	// Check for Azure-specific fields in config
	// These would be in a future extension of the OCFP config

	return cfg
}

// handleMapConfig handles map[string]interface{} configuration type.
func (c *Client) handleMapConfig(ctx context.Context) (*Config, error) {
	// Config was already parsed in NewProvider and stored in c.config
	// Now we need to initialize the Azure credentials if not already done
	if c.config != nil && c.credential == nil {
		err := c.initializeCredential(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize Azure credentials: %w", err)
		}

		// Initialize resource managers if not already done
		if c.network == nil {
			c.initializeResourceManagers()
		}

		logger.Debugw("Azure provider initialized from map config", "location", c.config.Location)
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

// initializeCredential sets up Azure credentials based on configuration.
//
//nolint:funlen // credential setup with multiple auth strategies
func (c *Client) initializeCredential(_ctx context.Context) error { //nolint:unparam // ctx kept for future use and interface consistency
	c.mu.Lock()
	defer c.mu.Unlock()

	var (
		err  error
		cred azcore.TokenCredential
	)

	// Determine cloud configuration
	cloudConfig := c.getCloudConfiguration()

	// Create credential based on configuration priority
	switch {
	case c.config.ClientID != "" && c.config.ClientSecret != "":
		// Service Principal with client secret
		cred, err = azidentity.NewClientSecretCredential(
			c.config.TenantID,
			c.config.ClientID,
			c.config.ClientSecret,
			&azidentity.ClientSecretCredentialOptions{
				ClientOptions: policy.ClientOptions{
					Cloud: cloudConfig,
				},
			},
		)
		if err != nil {
			return fmt.Errorf("failed to create client secret credential: %w", err)
		}

		logger.Debug("Using Azure service principal credentials (client secret)")

	case c.config.ClientID != "" && c.config.ClientCertificate != "":
		// Service Principal with certificate
		certs, key, err := azidentity.ParseCertificates([]byte(c.config.ClientCertificate), nil)
		if err != nil {
			return fmt.Errorf("failed to parse certificate: %w", err)
		}

		cred, err = azidentity.NewClientCertificateCredential(
			c.config.TenantID,
			c.config.ClientID,
			certs,
			key,
			&azidentity.ClientCertificateCredentialOptions{
				ClientOptions: policy.ClientOptions{
					Cloud: cloudConfig,
				},
			},
		)
		if err != nil {
			return fmt.Errorf("failed to create client certificate credential: %w", err)
		}

		logger.Debug("Using Azure service principal credentials (certificate)")

	case c.config.UseManagedIdentity:
		// Managed Identity
		opts := &azidentity.ManagedIdentityCredentialOptions{
			ClientOptions: policy.ClientOptions{
				Cloud: cloudConfig,
			},
		}
		if c.config.UserAssignedIdentityID != "" {
			opts.ID = azidentity.ClientID(c.config.UserAssignedIdentityID)
		}

		cred, err = azidentity.NewManagedIdentityCredential(opts)
		if err != nil {
			return fmt.Errorf("failed to create managed identity credential: %w", err)
		}

		if c.config.UserAssignedIdentityID != "" {
			logger.Debugw("Using Azure user-assigned managed identity", "clientID", c.config.UserAssignedIdentityID)
		} else {
			logger.Debug("Using Azure system-assigned managed identity")
		}

	case c.config.UseAzureCLI:
		// Azure CLI credentials
		cred, err = azidentity.NewAzureCLICredential(&azidentity.AzureCLICredentialOptions{})
		if err != nil {
			return fmt.Errorf("failed to create Azure CLI credential: %w", err)
		}

		logger.Debug("Using Azure CLI credentials")

	default:
		// Default credential chain (tries all methods)
		cred, err = azidentity.NewDefaultAzureCredential(&azidentity.DefaultAzureCredentialOptions{
			ClientOptions: policy.ClientOptions{
				Cloud: cloudConfig,
			},
		})
		if err != nil {
			return fmt.Errorf("failed to create default Azure credential: %w", err)
		}

		logger.Debug("Using default Azure credential chain")
	}

	c.credential = cred

	// Set up ARM client options
	c.armClientOptions = &arm.ClientOptions{
		ClientOptions: policy.ClientOptions{
			Cloud: cloudConfig,
			Retry: policy.RetryOptions{
				MaxRetries: int32(c.config.MaxRetries), //nolint:gosec // MaxRetries is a small config value
			},
		},
	}

	logger.Debugw("Azure credentials initialized", "subscription", c.config.SubscriptionID)

	return nil
}

// getCloudConfiguration returns the appropriate cloud configuration.
func (c *Client) getCloudConfiguration() cloud.Configuration {
	switch c.config.GetCloudName() {
	case "AzureGovernment":
		return cloud.AzureGovernment
	case "AzureChina":
		return cloud.AzureChina
	default:
		return cloud.AzurePublic
	}
}

// ensureClientsLoaded initializes all SDK clients if not already done.
func (c *Client) ensureClientsLoaded(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.clientsLoaded {
		return nil
	}

	if c.config == nil {
		//nolint:err113 // Simple static error message
		return errors.New("client not initialized: config is nil")
	}

	if c.credential == nil {
		// Initialize credentials first
		c.mu.Unlock()
		err := c.initializeCredential(ctx)
		c.mu.Lock()
		if err != nil {
			return fmt.Errorf("failed to initialize credentials: %w", err)
		}
	}

	// Initialize Resource Groups client
	var err error

	c.resourceGroupsClient, err = armresources.NewResourceGroupsClient(c.config.SubscriptionID, c.credential, c.armClientOptions)
	if err != nil {
		return fmt.Errorf("failed to create resource groups client: %w", err)
	}

	err = c.initNetworkClients()
	if err != nil {
		return err
	}

	err = c.initComputeClients()
	if err != nil {
		return err
	}

	err = c.initStorageClients()
	if err != nil {
		return err
	}

	c.clientsLoaded = true

	logger.Debugw("Azure service clients loaded", "location", c.config.Location, "resourceGroup", c.config.ResourceGroup)

	return nil
}

// initNetworkClients initializes all network-related SDK clients.
func (c *Client) initNetworkClients() error {
	var err error

	c.virtualNetworksClient, err = armnetwork.NewVirtualNetworksClient(c.config.SubscriptionID, c.credential, c.armClientOptions)
	if err != nil {
		return fmt.Errorf("failed to create virtual networks client: %w", err)
	}

	c.subnetsClient, err = armnetwork.NewSubnetsClient(c.config.SubscriptionID, c.credential, c.armClientOptions)
	if err != nil {
		return fmt.Errorf("failed to create subnets client: %w", err)
	}

	c.publicIPAddressesClient, err = armnetwork.NewPublicIPAddressesClient(c.config.SubscriptionID, c.credential, c.armClientOptions)
	if err != nil {
		return fmt.Errorf("failed to create public IP addresses client: %w", err)
	}

	c.networkSecurityGroupsClient, err = armnetwork.NewSecurityGroupsClient(c.config.SubscriptionID, c.credential, c.armClientOptions)
	if err != nil {
		return fmt.Errorf("failed to create network security groups client: %w", err)
	}

	c.securityRulesClient, err = armnetwork.NewSecurityRulesClient(c.config.SubscriptionID, c.credential, c.armClientOptions)
	if err != nil {
		return fmt.Errorf("failed to create security rules client: %w", err)
	}

	c.routeTablesClient, err = armnetwork.NewRouteTablesClient(c.config.SubscriptionID, c.credential, c.armClientOptions)
	if err != nil {
		return fmt.Errorf("failed to create route tables client: %w", err)
	}

	c.loadBalancersClient, err = armnetwork.NewLoadBalancersClient(c.config.SubscriptionID, c.credential, c.armClientOptions)
	if err != nil {
		return fmt.Errorf("failed to create load balancers client: %w", err)
	}

	return nil
}

// initComputeClients initializes all compute-related SDK clients.
func (c *Client) initComputeClients() error {
	var err error

	c.virtualMachinesClient, err = armcompute.NewVirtualMachinesClient(c.config.SubscriptionID, c.credential, c.armClientOptions)
	if err != nil {
		return fmt.Errorf("failed to create virtual machines client: %w", err)
	}

	c.disksClient, err = armcompute.NewDisksClient(c.config.SubscriptionID, c.credential, c.armClientOptions)
	if err != nil {
		return fmt.Errorf("failed to create disks client: %w", err)
	}

	c.snapshotsClient, err = armcompute.NewSnapshotsClient(c.config.SubscriptionID, c.credential, c.armClientOptions)
	if err != nil {
		return fmt.Errorf("failed to create snapshots client: %w", err)
	}

	c.imagesClient, err = armcompute.NewImagesClient(c.config.SubscriptionID, c.credential, c.armClientOptions)
	if err != nil {
		return fmt.Errorf("failed to create images client: %w", err)
	}

	c.sshPublicKeysClient, err = armcompute.NewSSHPublicKeysClient(c.config.SubscriptionID, c.credential, c.armClientOptions)
	if err != nil {
		return fmt.Errorf("failed to create SSH public keys client: %w", err)
	}

	c.virtualMachineSizesClient, err = armcompute.NewVirtualMachineSizesClient(c.config.SubscriptionID, c.credential, c.armClientOptions)
	if err != nil {
		return fmt.Errorf("failed to create virtual machine sizes client: %w", err)
	}

	return nil
}

// initStorageClients initializes all storage-related SDK clients.
func (c *Client) initStorageClients() error {
	var err error

	c.storageAccountsClient, err = armstorage.NewAccountsClient(c.config.SubscriptionID, c.credential, c.armClientOptions)
	if err != nil {
		return fmt.Errorf("failed to create storage accounts client: %w", err)
	}

	c.blobContainersClient, err = armstorage.NewBlobContainersClient(c.config.SubscriptionID, c.credential, c.armClientOptions)
	if err != nil {
		return fmt.Errorf("failed to create blob containers client: %w", err)
	}

	return nil
}

// buildDefaultTags converts config default tags to Azure format.
func (c *Client) buildDefaultTags() map[string]*string {
	if c.config.DefaultTags == nil {
		return nil
	}

	tags := make(map[string]*string)

	for k, v := range c.config.DefaultTags {
		val := v
		tags[k] = &val
	}

	return tags
}

// getResourceGroup returns the configured resource group.
func (c *Client) getResourceGroup() string {
	if c.config == nil {
		return ""
	}

	return c.config.ResourceGroup
}

// getLocation returns the configured location.
func (c *Client) getLocation() string {
	if c.config == nil {
		return ""
	}

	return c.config.Location
}

// getSubscriptionID returns the configured subscription ID.
func (c *Client) getSubscriptionID() string {
	if c.config == nil {
		return ""
	}

	return c.config.SubscriptionID
}
