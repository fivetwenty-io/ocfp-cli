// Package pve implements the CPI provider for Proxmox Virtual Environment.
package pve

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	ocfpconfig "github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"

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

	// Blobstore configuration.
	// BlobstoreMode is "local" (default) or "external". Local mode skips
	// bucket creation entirely. External mode requires an S3-compatible
	// endpoint and credentials (Ceph RGW, RustFS, etc.).
	BlobstoreMode      string
	BlobstoreEndpoint  string
	BlobstoreRegion    string
	BlobstoreAccessKey string
	BlobstoreSecretKey string //nolint:gosec // field name is descriptive, not a hardcoded secret
	BlobstoreCAPath    string // path to CA bundle for self-signed endpoints
	BlobstorePathStyle bool   // true for Ceph/RustFS (default true)
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

	// Domain-specific services (lazy initialized). Protected by
	// servicesMu — getXService() are called concurrently from multiple
	// manager goroutines.
	servicesMu     sync.Mutex
	qemuService    qemu.Service
	storageService storage.Service
	networkService network.Service
	tasksService   tasks.Service

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
	return "pve"
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
	logger.Debug("Authenticating with PVE")

	if c.config == nil {
		return ErrConfigIsRequired
	}

	// Build client options. The apiclient's Options.Host must be a bare
	// hostname (no scheme, no port); scheme is implied by Options.Protocol
	// (default https) and port by Options.Port (default 8006). Operators
	// commonly pass api_endpoint as a full URL — parse and split here so
	// either form works.
	host, port, protocol := splitPVEEndpoint(c.config.Host)

	opts := pve.Options{
		Host:      host,
		Port:      port,
		Protocol:  protocol,
		Timeout:   c.config.Timeout,
		AutoLogin: true,
	}

	// Configure authentication
	switch {
	case c.config.TokenID != "" && c.config.TokenSecret != "":
		// API Token authentication (preferred). The apiclient parses the
		// full "user@realm!name=secret" string; APITokenName is the literal
		// header prefix ("PVEAPIToken"), not the token ID.
		opts.APIToken = fmt.Sprintf("%s=%s", c.config.TokenID, c.config.TokenSecret)
		opts.APITokenName = "PVEAPIToken"
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
		return fmt.Errorf("failed to create PVE client: %w", err)
	}

	c.pveClient = pveClient

	// Validate credentials by getting version info
	_, err = c.pveClient.GetCtx(ctx, "/version", nil)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	logger.Info("Successfully authenticated with PVE")

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

// SupportsStorage reports whether the configured blobstore mode is external.
//
// PVE has no native object-storage layer, so bucket creation only makes sense
// when an S3-compatible endpoint (Ceph RGW, RustFS, etc.) is configured.
// Local mode (the default) makes the bootstrap bucket-creation step a no-op,
// preventing the long stream of ErrBucketsNotSupported errors that otherwise
// fail step 7/7.
func (c *Client) SupportsStorage() bool {
	if c == nil || c.config == nil {
		return false
	}

	return strings.EqualFold(c.config.BlobstoreMode, "external")
}

// Initialize initializes the provider with configuration.
func (c *Client) Initialize(ctx context.Context, config interface{}) error {
	cfg, err := c.parsePVEConfig(config)
	if err != nil {
		return err
	}

	// parsePVEConfig returns nil for map config type because NewProvider already
	// parsed the map into c.config. Authenticate still has to run, so reuse
	// the stored config rather than bailing out.
	if cfg == nil {
		if c.config == nil {
			return ErrConfigIsRequired
		}

		applyPVEDefaults(c.config)
		c.initPVEManagers()

		err = c.Authenticate(ctx)
		if err != nil {
			return fmt.Errorf("failed to initialize PVE provider: %w", err)
		}

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

	applyPVEDefaults(cfg)

	c.config = cfg

	c.initPVEManagers()

	// Authenticate
	err = c.Authenticate(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize PVE provider: %w", err)
	}

	return nil
}

// splitPVEEndpoint accepts an endpoint in any of these forms and returns the
// bare host, port (0 when unspecified so the apiclient applies its default),
// and protocol ("" when unspecified so the apiclient defaults to https):
//
//   - "pve.example.com"               → ("pve.example.com", 0, "")
//   - "pve.example.com:8006"          → ("pve.example.com", 8006, "")
//   - "https://pve.example.com:8006"  → ("pve.example.com", 8006, "https")
//   - "http://pve.example.com:8006"   → ("pve.example.com", 8006, "http")
//
// Trailing path components and userinfo are discarded. Invalid ports are
// ignored (zero returned) so the apiclient's default takes effect.
func splitPVEEndpoint(endpoint string) (host string, port int, protocol string) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", 0, ""
	}

	// If a scheme is present, use net/url to parse.
	if strings.Contains(endpoint, "://") {
		u, err := url.Parse(endpoint)
		if err == nil {
			protocol = u.Scheme
			host = u.Hostname()

			if p := u.Port(); p != "" {
				if n, err := strconv.Atoi(p); err == nil {
					port = n
				}
			}

			return host, port, protocol
		}
	}

	// No scheme: may be "host" or "host:port".
	if strings.Contains(endpoint, ":") {
		h, p, err := splitHostPort(endpoint)
		if err == nil {
			host = h
			if n, err := strconv.Atoi(p); err == nil {
				port = n
			}

			return host, port, ""
		}
	}

	return endpoint, 0, ""
}

// splitHostPort splits "host:port" without resolving anything. Returns an
// error if the format is unrecognised.
func splitHostPort(hp string) (host string, port string, err error) {
	idx := strings.LastIndex(hp, ":")
	if idx <= 0 || idx == len(hp)-1 {
		return "", "", fmt.Errorf("invalid host:port %q", hp) //nolint:err113 // descriptive error, not caller-testable
	}

	return hp[:idx], hp[idx+1:], nil
}

// applyPVEDefaults sets default values on a Proxmox VE config.
func applyPVEDefaults(cfg *Config) {
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

// parsePVEConfig parses the configuration based on type.
// Returns nil config for map[string]interface{} (already parsed).
//
// Auth mode rules for *ocfpconfig.Config:
//   - API token auth:  AuthToken != "" AND TokenSecret != ""  → sets TokenID + TokenSecret; Username/Password ignored.
//   - User/pass auth:  Username != "" AND Password != ""      → sets Username + Password; TokenID/TokenSecret empty.
//   - Mixed/partial:   AuthToken set but TokenSecret empty, or both modes partially set → ErrMixedAuthConfig.
//   - Neither:         all four empty → no auth set; Initialize will catch it via ErrAPITokenRequired.
func (c *Client) parsePVEConfig(config interface{}) (*Config, error) {
	switch configValue := config.(type) {
	case *Config:
		return configValue, nil
	case *ocfpconfig.Config:
		hasTokenID := configValue.AuthToken != ""
		hasTokenSecret := configValue.TokenSecret != ""
		hasUsername := configValue.Username != ""
		hasPassword := configValue.Password != ""

		apiTokenMode := hasTokenID && hasTokenSecret
		userPassMode := hasUsername && hasPassword

		// Detect mixed/partial auth: any overlap or incomplete token pair is an error.
		switch {
		case apiTokenMode && userPassMode:
			// Both fully set — ambiguous; operator must choose one mode.
			return nil, ErrMixedAuthConfig
		case hasTokenID && !hasTokenSecret && !userPassMode:
			// AuthToken set but no TokenSecret and no fallback user/pass mode.
			return nil, ErrMixedAuthConfig
		case hasTokenSecret && !hasTokenID:
			// TokenSecret set without AuthToken — incomplete token pair.
			return nil, ErrMixedAuthConfig
		}

		cfg := &Config{
			Host:           configValue.APIEndpoint,
			Node:           configValue.Region,
			NetworkMode:    defaultNetworkMode,
			DefaultBridge:  defaultBridge,
			DefaultStorage: defaultStorage,
			Timeout:        0,
			MaxRetries:     0,
		}

		if apiTokenMode {
			// API token auth: use AuthToken as token_id, TokenSecret as token_secret.
			cfg.TokenID = configValue.AuthToken
			cfg.TokenSecret = configValue.TokenSecret
		} else if userPassMode {
			// Username/password auth: Password is the user's password, not a token secret.
			cfg.Username = configValue.Username
			cfg.Password = configValue.Password
		}
		// Neither set: leave auth fields empty; Initialize catches via ErrAPITokenRequired.

		return cfg, nil
	case map[string]interface{}:
		// Config was already parsed in NewProvider, just return success
		return nil, nil
	default:
		return nil, ErrInvalidConfigType(config)
	}
}

// initPVEManagers initializes resource managers if not already set.
func (c *Client) initPVEManagers() {
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
	c.servicesMu.Lock()
	defer c.servicesMu.Unlock()

	if c.qemuService == nil {
		c.qemuService = qemu.New(c.pveClient)
	}

	return c.qemuService
}

// getStorageService returns the storage service, initializing on first use.
//
//nolint:ireturn // returns interface by design for PVE service abstraction
func (c *Client) getStorageService() storage.Service {
	c.servicesMu.Lock()
	defer c.servicesMu.Unlock()

	if c.storageService == nil {
		c.storageService = storage.New(c.pveClient)
	}

	return c.storageService
}

// getNetworkService returns the network service, initializing on first use.
//
//nolint:ireturn // returns interface by design for PVE service abstraction
func (c *Client) getNetworkService() network.Service {
	c.servicesMu.Lock()
	defer c.servicesMu.Unlock()

	if c.networkService == nil {
		c.networkService = network.New(c.pveClient)
	}

	return c.networkService
}

// getTasksService returns the tasks service, initializing on first use.
//
//nolint:ireturn // returns interface by design for PVE service abstraction
func (c *Client) getTasksService() tasks.Service {
	c.servicesMu.Lock()
	defer c.servicesMu.Unlock()

	if c.tasksService == nil {
		c.tasksService = tasks.New(c.pveClient)
	}

	return c.tasksService
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
