// Package proxmox implements the CPI provider for Proxmox Virtual Environment.
package proxmox

import (
	"context"
	"fmt"
	"sync"
	"time"

	ocfpconfig "github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/api/cloudinit"
	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/api/network"
	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/api/qemu"
	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/api/storage"
	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/api/tasks"
	pve "github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/client"
)

const (
	defaultHTTPTimeout  = 30 * time.Second
	defaultMaxRetries   = 3
	defaultNetworkMode  = "bridge"
	defaultBridge       = "vmbr0"
	defaultStorage      = "local-lvm"
	nodeRefreshInterval = 5 * time.Minute

	// bytesPerKB is the number of bytes in a kilobyte for memory calculations.
	bytesPerKB = 1024
	// cpuScoreWeight is the weight for CPU in node scoring (0-1 scale).
	cpuScoreWeight = 0.4
	// memScoreWeight is the weight for memory in node scoring (0-1 scale, implied via 1024 divisor).
	memScoreWeight = 0.6
)

// Config holds Proxmox-specific configuration.
type Config struct {
	// Connection settings
	Host string // Proxmox API URL (e.g., "https://pve.example.com:8006")
	Node string // Specific node (optional - auto-select if empty)

	// Authentication (API Token preferred)
	TokenID     string // API Token ID (e.g., "root@pam!mytoken")
	TokenSecret string // API Token secret
	Username    string // Username for password auth (fallback)
	Password    string //nolint:gosec // field name is descriptive, not a hardcoded secret
	Realm       string // Auth realm (default: "pam")

	// Network settings
	NetworkMode   string // "bridge" (default) or "sdn"
	DefaultBridge string // Default bridge (e.g., "vmbr0")
	SDNZone       string // SDN zone for SDN mode

	// Storage settings
	DefaultStorage string // Default storage pool (e.g., "local-lvm")
	ISOStorage     string // Storage for ISO images

	// Timeouts and retries
	Timeout    time.Duration
	MaxRetries int

	// TLS settings
	VerifySSL bool   // Verify SSL certificates
	CAPath    string // Custom CA certificate path
}

// NodeInfo holds information about a Proxmox node.
type NodeInfo struct {
	Name    string
	Status  string
	CPU     float64
	MaxCPU  float64
	Mem     int64
	MaxMem  int64
	Disk    int64
	MaxDisk int64
	Uptime  int64
}

// Client implements the Proxmox provider.
type Client struct {
	config *Config

	// Resource managers
	network      *NetworkManager
	compute      *ComputeManager
	storage      *StorageManager
	security     *SecurityManager
	loadBalancer *LoadBalancerManager

	// Proxmox API client
	pveClient pve.Client

	// Domain-specific services (lazy initialized)
	qemuService      qemu.Service
	storageService   storage.Service
	networkService   network.Service
	tasksService     tasks.Service
	cloudinitService cloudinit.Service

	// Cluster node cache
	nodes        []NodeInfo
	nodesUpdated time.Time
	nodesMutex   sync.RWMutex
}

// NewClient creates a new Proxmox client.
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
			pveClient:    nil,
		}

		return client, nil
	}

	// Set defaults
	if config.Timeout == 0 {
		config.Timeout = defaultHTTPTimeout
	}

	if config.MaxRetries == 0 {
		config.MaxRetries = defaultMaxRetries
	}

	if config.NetworkMode == "" {
		config.NetworkMode = defaultNetworkMode
	}

	if config.DefaultBridge == "" {
		config.DefaultBridge = defaultBridge
	}

	if config.DefaultStorage == "" {
		config.DefaultStorage = defaultStorage
	}

	if config.Realm == "" {
		config.Realm = "pam"
	}

	client := &Client{
		config:       config,
		network:      nil,
		compute:      nil,
		storage:      nil,
		security:     nil,
		loadBalancer: nil,
		pveClient:    nil,
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
	return "proxmox"
}

// Region returns the configured region (node or cluster name).
func (c *Client) Region() string {
	if c.config == nil {
		return ""
	}

	if c.config.Node != "" {
		return c.config.Node
	}

	return "pve-cluster"
}

// Authenticate validates and stores credentials.
func (c *Client) Authenticate(ctx context.Context) error {
	logger.Debug("Authenticating with Proxmox")

	if c.config == nil {
		return ErrConfigIsRequired
	}

	// Build client options
	opts := pve.Options{
		Host:      c.config.Host,
		Timeout:   c.config.Timeout,
		AutoLogin: true,
	}

	// Configure authentication
	switch {
	case c.config.TokenID != "" && c.config.TokenSecret != "":
		// API Token authentication (preferred)
		opts.APIToken = c.config.TokenSecret
		opts.APITokenName = c.config.TokenID
	case c.config.Username != "" && c.config.Password != "":
		// Username/password authentication
		opts.Username = c.config.Username
		if c.config.Realm != "" {
			opts.Username = fmt.Sprintf("%s@%s", c.config.Username, c.config.Realm)
		}

		opts.Password = c.config.Password
	default:
		return ErrAPITokenRequired
	}

	// Configure SSL verification
	if !c.config.VerifySSL {
		opts.SSLOptions = &pve.SSLOptions{
			VerifyMode: pve.SSLVerifyNone,
		}
	} else if c.config.CAPath != "" {
		opts.SSLOptions = &pve.SSLOptions{
			VerifyMode: pve.SSLVerifyFull,
			CACert:     c.config.CAPath,
		}
	}

	// Create client
	pveClient, err := pve.NewClient(opts)
	if err != nil {
		return fmt.Errorf("failed to create Proxmox client: %w", err)
	}

	c.pveClient = pveClient

	// Validate credentials by getting version info
	_, err = c.pveClient.GetCtx(ctx, "/version", nil)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	logger.Info("Successfully authenticated with Proxmox")

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

// SupportsStorage returns true as Proxmox supports storage operations.
func (c *Client) SupportsStorage() bool {
	return true
}

// Initialize initializes the provider with configuration.
func (c *Client) Initialize(ctx context.Context, config interface{}) error {
	cfg, err := c.parseProxmoxConfig(config)
	if err != nil {
		return err
	}

	// parseProxmoxConfig returns nil for map config type (already parsed)
	if cfg == nil {
		return nil
	}

	// Validate required fields
	if cfg.Host == "" {
		return ErrHostRequired
	}

	// Check for authentication
	hasAPIToken := cfg.TokenID != "" && cfg.TokenSecret != ""
	hasUserPass := cfg.Username != "" && cfg.Password != ""

	if !hasAPIToken && !hasUserPass {
		return ErrAPITokenRequired
	}

	applyProxmoxDefaults(cfg)

	c.config = cfg

	c.initProxmoxManagers()

	// Authenticate
	err = c.Authenticate(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize Proxmox provider: %w", err)
	}

	return nil
}

// applyProxmoxDefaults sets default values on a Proxmox config.
func applyProxmoxDefaults(cfg *Config) {
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultHTTPTimeout
	}

	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = defaultMaxRetries
	}

	if cfg.NetworkMode == "" {
		cfg.NetworkMode = defaultNetworkMode
	}

	if cfg.DefaultBridge == "" {
		cfg.DefaultBridge = defaultBridge
	}

	if cfg.DefaultStorage == "" {
		cfg.DefaultStorage = defaultStorage
	}

	if cfg.Realm == "" {
		cfg.Realm = "pam"
	}
}

// Cleanup performs cleanup operations.
func (c *Client) Cleanup(_ctx context.Context) error {
	// If using ticket-based auth, logout
	if c.pveClient != nil && c.config != nil && c.config.Username != "" {
		_ = c.pveClient.Logout()
	}

	return nil
}

// parseProxmoxConfig parses the configuration based on type.
// Returns nil config for map[string]interface{} (already parsed).
func (c *Client) parseProxmoxConfig(config interface{}) (*Config, error) {
	switch configValue := config.(type) {
	case *Config:
		return configValue, nil
	case *ocfpconfig.Config:
		return &Config{
			Host:           configValue.APIEndpoint,
			Node:           configValue.Region, // Use region as node
			TokenID:        configValue.AuthToken,
			TokenSecret:    configValue.Password, // Token secret may be in password field
			Username:       configValue.Username,
			Password:       configValue.Password,
			NetworkMode:    defaultNetworkMode,
			DefaultBridge:  defaultBridge,
			DefaultStorage: defaultStorage,
			Timeout:        0,
			MaxRetries:     0,
		}, nil
	case map[string]interface{}:
		// Config was already parsed in NewProvider, just return success
		return nil, nil
	default:
		return nil, ErrInvalidConfigType(config)
	}
}

// initProxmoxManagers initializes resource managers if not already set.
func (c *Client) initProxmoxManagers() {
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
}

// getQemuService returns the QEMU service, initializing on first use.
//
//nolint:ireturn // returns interface by design for PVE service abstraction
func (c *Client) getQemuService() qemu.Service {
	if c.qemuService == nil {
		c.qemuService = qemu.New(c.pveClient)
	}

	return c.qemuService
}

// getStorageService returns the storage service, initializing on first use.
//
//nolint:ireturn // returns interface by design for PVE service abstraction
func (c *Client) getStorageService() storage.Service {
	if c.storageService == nil {
		c.storageService = storage.New(c.pveClient)
	}

	return c.storageService
}

// getNetworkService returns the network service, initializing on first use.
//
//nolint:ireturn // returns interface by design for PVE service abstraction
func (c *Client) getNetworkService() network.Service {
	if c.networkService == nil {
		c.networkService = network.New(c.pveClient)
	}

	return c.networkService
}

// getTasksService returns the tasks service, initializing on first use.
//
//nolint:ireturn // returns interface by design for PVE service abstraction
func (c *Client) getTasksService() tasks.Service {
	if c.tasksService == nil {
		c.tasksService = tasks.New(c.pveClient)
	}

	return c.tasksService
}

// getCloudinitService returns the cloud-init service, initializing on first use.
//
//nolint:ireturn // returns interface by design for PVE service abstraction
func (c *Client) getCloudinitService() cloudinit.Service {
	if c.cloudinitService == nil {
		c.cloudinitService = cloudinit.New(c.pveClient)
	}

	return c.cloudinitService
}

// waitForTask waits for a Proxmox task to complete.
func (c *Client) waitForTask(ctx context.Context, node, upid string, timeoutSeconds int) error {
	if timeoutSeconds == 0 {
		timeoutSeconds = 300 // 5 minutes default
	}

	taskSvc := c.getTasksService()

	status, err := taskSvc.Wait(ctx, node, upid, &tasks.WaitOptions{
		TimeoutSeconds: timeoutSeconds,
		Backoff:        true,
	})
	if err != nil {
		return fmt.Errorf("task wait failed: %w", err)
	}

	if status.ExitStatus != "OK" && status.ExitStatus != "" {
		return ErrTaskFailedWithStatus(upid, status.ExitStatus)
	}

	return nil
}

// refreshNodes refreshes the cluster node cache.
func (c *Client) refreshNodes(ctx context.Context) error {
	c.nodesMutex.Lock()
	defer c.nodesMutex.Unlock()

	// Check if refresh is needed
	if time.Since(c.nodesUpdated) < nodeRefreshInterval && len(c.nodes) > 0 {
		return nil
	}

	// Get nodes from API
	resp, err := c.pveClient.GetCtx(ctx, "/nodes", nil)
	if err != nil {
		return fmt.Errorf("failed to get nodes: %w", err)
	}

	// Parse response
	data, ok := resp.([]interface{})
	if !ok {
		return fmt.Errorf("%w: %T", ErrUnexpectedResponseType, resp)
	}

	nodes := make([]NodeInfo, 0, len(data))
	for _, item := range data {
		nodeData, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		node := NodeInfo{
			Name:   getStringFromMap(nodeData, "node"),
			Status: getStringFromMap(nodeData, "status"),
		}

		if cpu, ok := nodeData["cpu"].(float64); ok {
			node.CPU = cpu
		}

		if maxcpu, ok := nodeData["maxcpu"].(float64); ok {
			node.MaxCPU = maxcpu
		}

		if mem, ok := nodeData["mem"].(float64); ok {
			node.Mem = int64(mem)
		}

		if maxmem, ok := nodeData["maxmem"].(float64); ok {
			node.MaxMem = int64(maxmem)
		}

		nodes = append(nodes, node)
	}

	c.nodes = nodes
	c.nodesUpdated = time.Now()

	return nil
}

// getNode returns the target node name, either configured or auto-selected.
func (c *Client) getNode(ctx context.Context) (string, error) {
	// If a specific node is configured, use it
	if c.config.Node != "" {
		return c.config.Node, nil
	}

	// Refresh node list
	err := c.refreshNodes(ctx)
	if err != nil {
		return "", err
	}

	// Auto-select: return first online node
	c.nodesMutex.RLock()
	defer c.nodesMutex.RUnlock()

	for _, node := range c.nodes {
		if node.Status == "online" {
			return node.Name, nil
		}
	}

	return "", ErrNoAvailableNode
}

// selectOptimalNode selects the best node based on available resources.
func (c *Client) selectOptimalNode(ctx context.Context, cpuReq int, memReqMB int) (string, error) {
	// If a specific node is configured, use it
	if c.config.Node != "" {
		return c.config.Node, nil
	}

	// Refresh node list
	err := c.refreshNodes(ctx)
	if err != nil {
		return "", err
	}

	c.nodesMutex.RLock()
	defer c.nodesMutex.RUnlock()

	var (
		bestNode  string
		bestScore float64 = -1
	)

	for _, node := range c.nodes {
		// Skip offline nodes
		if node.Status != "online" {
			continue
		}

		// Calculate available resources
		availCPU := node.MaxCPU - node.CPU
		availMemMB := float64(node.MaxMem-node.Mem) / bytesPerKB / bytesPerKB

		// Check if node has sufficient resources
		if availCPU < float64(cpuReq) || availMemMB < float64(memReqMB) {
			continue
		}

		// Score based on available resources (higher is better)
		// Weight: 40% CPU, 60% memory
		score := availCPU*cpuScoreWeight + (availMemMB/bytesPerKB)*memScoreWeight

		if score > bestScore {
			bestScore = score
			bestNode = node.Name
		}
	}

	if bestNode == "" {
		return "", ErrNoAvailableNode
	}

	return bestNode, nil
}

// getStringFromMap safely gets a string from a map.
func getStringFromMap(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}

	return ""
}

// getIntFromMap safely gets an int from a map.
func getIntFromMap(m map[string]interface{}, key string) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) { //nolint:varnamelen // n is clear in context
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}

	return 0
}
