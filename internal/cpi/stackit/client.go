package stackit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// Client implements the STACKIT provider
type Client struct {
	config      *Config
	httpClient  *http.Client
	rateLimiter *cpi.RateLimiter

	// Resource managers
	network      *NetworkManager
	compute      *ComputeManager
	storage      *StorageManager
	security     *SecurityManager
	loadBalancer *LoadBalancerManager
}

// Config holds STACKIT-specific configuration
type Config struct {
	ProjectID  string
	OrgID      string
	AuthToken  string
	Region     string
	BaseURL    string
	Timeout    time.Duration
	MaxRetries int
	RateLimit  int
}

// NewClient creates a new STACKIT client
func NewClient(config *Config) (*Client, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}

	// Set defaults
	if config.BaseURL == "" {
		config.BaseURL = "https://api.stackit.cloud"
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if config.MaxRetries == 0 {
		config.MaxRetries = 3
	}
	if config.RateLimit == 0 {
		config.RateLimit = 10 // requests per second
	}

	// Create HTTP client with optimized transport settings
	httpClient := &http.Client{
		Timeout: config.Timeout,
		Transport: &http.Transport{
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			MaxConnsPerHost:       100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		},
	}

	// Create rate limiter
	rateLimiter := cpi.NewRateLimiter(config.RateLimit, config.RateLimit*2)

	client := &Client{
		config:      config,
		httpClient:  httpClient,
		rateLimiter: rateLimiter,
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

// Name returns the provider name
func (c *Client) Name() string {
	return "stackit"
}

// Region returns the configured region
func (c *Client) Region() string {
	return c.config.Region
}

// Authenticate validates and stores credentials
func (c *Client) Authenticate(ctx context.Context) error {
	logger.Debug("Authenticating with STACKIT")

	// Validate token by making a simple API call
	req, err := c.newRequest(ctx, "GET", "/v1/projects/"+c.config.ProjectID, nil)
	if err != nil {
		return fmt.Errorf("failed to create auth request: %w", err)
	}

	resp, err := c.do(ctx, req)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("authentication failed: status %d", resp.StatusCode)
	}

	logger.Info("Successfully authenticated with STACKIT")
	return nil
}

// ValidateCredentials checks if credentials are valid
func (c *Client) ValidateCredentials(ctx context.Context) error {
	return c.Authenticate(ctx)
}

// Network returns the network manager
func (c *Client) Network() cpi.NetworkManager {
	return c.network
}

// Compute returns the compute manager
func (c *Client) Compute() cpi.ComputeManager {
	return c.compute
}

// Storage returns the storage manager
func (c *Client) Storage() cpi.StorageManager {
	return c.storage
}

// Security returns the security manager
func (c *Client) Security() cpi.SecurityManager {
	return c.security
}

// LoadBalancer returns the load balancer manager
func (c *Client) LoadBalancer() cpi.LoadBalancerManager {
	return c.loadBalancer
}

// Initialize initializes the provider with configuration
func (c *Client) Initialize(ctx context.Context, config interface{}) error {
	cfg, ok := config.(*Config)
	if !ok {
		return fmt.Errorf("invalid config type for STACKIT provider")
	}

	// Set defaults
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.stackit.cloud"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.RateLimit == 0 {
		cfg.RateLimit = 10 // requests per second
	}

	c.config = cfg

	// Create HTTP client if not already set
	if c.httpClient == nil {
		c.httpClient = &http.Client{
			Timeout: cfg.Timeout,
			Transport: &http.Transport{
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   10,
				MaxConnsPerHost:       100,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 10 * time.Second,
			},
		}
	}

	// Create rate limiter if not already set
	if c.rateLimiter == nil {
		c.rateLimiter = cpi.NewRateLimiter(cfg.RateLimit, cfg.RateLimit*2)
	}

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
	if err := c.Authenticate(ctx); err != nil {
		return fmt.Errorf("failed to initialize STACKIT provider: %w", err)
	}

	return nil
}

// Cleanup performs cleanup operations
func (c *Client) Cleanup(ctx context.Context) error {
	// Stop rate limiter
	if c.rateLimiter != nil {
		c.rateLimiter.Stop()
	}

	// Close idle connections
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}

	return nil
}

// HTTP helper methods

// newRequest creates a new HTTP request
func (c *Client) newRequest(ctx context.Context, method, path string, body interface{}) (*http.Request, error) {
	url := c.config.BaseURL + path

	var bodyReader io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = strings.NewReader(string(jsonData))
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, err
	}

	// Set headers
	req.Header.Set("Authorization", "Bearer "+c.config.AuthToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Project-ID", c.config.ProjectID)
	req.Header.Set("X-Organization-ID", c.config.OrgID)

	return req, nil
}

// do executes an HTTP request with rate limiting and retry
func (c *Client) do(ctx context.Context, req *http.Request) (*http.Response, error) {
	// Apply rate limiting
	if err := c.rateLimiter.Wait(ctx); err != nil {
		return nil, err
	}

	// Execute with retry
	var resp *http.Response
	err := cpi.WithRetry(ctx, &cpi.RetryConfig{
		MaxAttempts:  c.config.MaxRetries,
		InitialDelay: 1 * time.Second,
		MaxDelay:     10 * time.Second,
		Multiplier:   2.0,
	}, func(ctx context.Context) error {
		var err error
		resp, err = c.httpClient.Do(req)
		if err != nil {
			return &cpi.ProviderError{
				Provider: "stackit",
				Code:     "NetworkError",
				Message:  err.Error(),
			}
		}

		// Check for retryable status codes
		if resp.StatusCode >= 500 || resp.StatusCode == 429 {
			return &cpi.ProviderError{
				Provider: "stackit",
				Code:     fmt.Sprintf("%d", resp.StatusCode),
				Message:  "Server error",
			}
		}

		return nil
	})

	return resp, err
}

// parseError parses an error response
func (c *Client) parseError(resp *http.Response) error {
	if resp.Body == nil {
		return &cpi.ProviderError{
			Provider: "stackit",
			Code:     fmt.Sprintf("%d", resp.StatusCode),
			Message:  fmt.Sprintf("Request failed with status %d", resp.StatusCode),
		}
	}

	// Read the body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &cpi.ProviderError{
			Provider: "stackit",
			Code:     fmt.Sprintf("%d", resp.StatusCode),
			Message:  fmt.Sprintf("Request failed with status %d", resp.StatusCode),
		}
	}

	// Try to parse as JSON first
	var errorResp struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Details string `json:"details"`
	}

	if err := json.Unmarshal(body, &errorResp); err == nil {
		// Use the parsed error message
		message := errorResp.Error
		if message == "" {
			message = errorResp.Message
		}
		if message == "" {
			message = errorResp.Details
		}
		if message != "" {
			return &cpi.ProviderError{
				Provider: "stackit",
				Code:     fmt.Sprintf("%d", resp.StatusCode),
				Message:  message,
			}
		}
	}

	// If JSON parsing failed or no message found, use plain text if available
	if len(body) > 0 {
		message := string(body)
		// Trim whitespace and check if it's meaningful
		if trimmed := strings.TrimSpace(message); trimmed != "" {
			return &cpi.ProviderError{
				Provider: "stackit",
				Code:     fmt.Sprintf("%d", resp.StatusCode),
				Message:  trimmed,
			}
		}
	}

	// Fallback to generic message
	return &cpi.ProviderError{
		Provider: "stackit",
		Code:     fmt.Sprintf("%d", resp.StatusCode),
		Message:  fmt.Sprintf("Request failed with status %d", resp.StatusCode),
	}
}
