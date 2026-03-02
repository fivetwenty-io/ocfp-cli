package gcp

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"time"
)

// Config holds GCP-specific configuration.
type Config struct {
	// Authentication
	ProjectID            string // GCP Project ID (required)
	ServiceAccountJSON   string // Service account JSON content or path to file
	ServiceAccountEmail  string // Service account email (for impersonation)
	ImpersonateTarget    string // Target service account for impersonation

	// Location
	Region string // Default region (e.g., us-central1)
	Zone   string // Default zone (e.g., us-central1-a)

	// Network settings
	NetworkName               string // Default VPC network name
	EnableSharedVPC           bool   // Enable Shared VPC support
	HostProjectID             string // Host project for Shared VPC
	EnablePrivateGoogleAccess bool   // Enable Private Google Access for subnets

	// Retry and timeout settings
	Timeout    time.Duration
	MaxRetries int
	RetryMode  string // standard, adaptive

	// Connection pooling and HTTP settings
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	IdleConnTimeout     time.Duration
	TLSHandshakeTimeout time.Duration
	DialTimeout         time.Duration
	KeepAlive           time.Duration

	// Labeling (GCP uses labels instead of tags)
	DefaultLabels map[string]string

	// Advanced settings
	EnableOSLogin             bool   // Use OS Login for SSH instead of metadata keys
	EnableSerialPortLogging   bool   // Enable serial port output
	UseCustomEndpoint         bool   // Use custom API endpoint
	ComputeEndpoint           string // Custom Compute API endpoint
	StorageEndpoint           string // Custom Storage API endpoint
	DebugLogging              bool
	UserAgent                 string // Custom user agent for API calls
}

const (
	defaultTimeout             = 120
	defaultMaxRetries          = 3
	defaultMaxIdleConns        = 100
	defaultMaxIdleConnsPerHost = 10
	defaultIdleConnTimeout     = 90
	defaultTLSHandshakeTimeout = 10
	defaultDialTimeout         = 30
	defaultKeepAlive           = 30
)

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Region:                    "us-central1",
		Zone:                      "us-central1-a",
		Timeout:                   defaultTimeout * time.Second,
		MaxRetries:                defaultMaxRetries,
		RetryMode:                 "standard",
		MaxIdleConns:              defaultMaxIdleConns,
		MaxIdleConnsPerHost:       defaultMaxIdleConnsPerHost,
		IdleConnTimeout:           defaultIdleConnTimeout * time.Second,
		TLSHandshakeTimeout:       defaultTLSHandshakeTimeout * time.Second,
		DialTimeout:               defaultDialTimeout * time.Second,
		KeepAlive:                 defaultKeepAlive * time.Second,
		EnablePrivateGoogleAccess: true,
		EnableOSLogin:             false,
		EnableSerialPortLogging:   false,
		DebugLogging:              false,
		DefaultLabels:             make(map[string]string),
		UserAgent:                 "ocfp-cli/1.0",
	}
}

// NewHTTPClient creates a customized HTTP client with connection pooling.
func (c *Config) NewHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   c.DialTimeout,
			KeepAlive: c.KeepAlive,
		}).DialContext,
		MaxIdleConns:          c.MaxIdleConns,
		MaxIdleConnsPerHost:   c.MaxIdleConnsPerHost,
		IdleConnTimeout:       c.IdleConnTimeout,
		TLSHandshakeTimeout:   c.TLSHandshakeTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   c.Timeout,
	}
}

// Validate checks if the configuration is valid.
func (c *Config) Validate() error {
	if c.ProjectID == "" {
		return &ConfigError{Field: "ProjectID", Message: "project ID is required"}
	}

	// Service account JSON is required for authentication
	if c.ServiceAccountJSON == "" {
		return &ConfigError{Field: "ServiceAccountJSON", Message: "service account JSON is required"}
	}

	// If using Shared VPC, host project must be specified
	if c.EnableSharedVPC && c.HostProjectID == "" {
		return &ConfigError{Field: "HostProjectID", Message: "host project ID required when Shared VPC is enabled"}
	}

	// Validate retry settings
	if c.MaxRetries < 0 {
		return &ConfigError{Field: "MaxRetries", Message: "max retries cannot be negative"}
	}

	if c.RetryMode != "" && c.RetryMode != "standard" && c.RetryMode != "adaptive" {
		return &ConfigError{Field: "RetryMode", Message: "retry mode must be 'standard' or 'adaptive'"}
	}

	return nil
}

// GetServiceAccountCredentials returns the service account JSON credentials.
// It handles both inline JSON and file paths.
func (c *Config) GetServiceAccountCredentials() ([]byte, error) {
	if c.ServiceAccountJSON == "" {
		return nil, &ConfigError{Field: "ServiceAccountJSON", Message: "service account JSON is required"}
	}

	// Check if it's a file path
	if _, err := os.Stat(c.ServiceAccountJSON); err == nil {
		return os.ReadFile(c.ServiceAccountJSON)
	}

	// Try to parse as JSON directly
	var js json.RawMessage
	if err := json.Unmarshal([]byte(c.ServiceAccountJSON), &js); err == nil {
		return []byte(c.ServiceAccountJSON), nil
	}

	return nil, &ConfigError{Field: "ServiceAccountJSON", Message: "invalid service account JSON: not a valid file path or JSON content"}
}

// GetNetworkProject returns the project ID for network resources.
// For Shared VPC, this returns the host project; otherwise, the main project.
func (c *Config) GetNetworkProject() string {
	if c.EnableSharedVPC && c.HostProjectID != "" {
		return c.HostProjectID
	}
	return c.ProjectID
}

// GetRegionFromZone extracts the region from a zone (e.g., "us-central1-a" -> "us-central1").
func GetRegionFromZone(zone string) string {
	if len(zone) < 2 {
		return zone
	}
	// Find the last hyphen and take everything before it
	lastHyphen := -1
	for i := len(zone) - 1; i >= 0; i-- {
		if zone[i] == '-' {
			lastHyphen = i
			break
		}
	}
	if lastHyphen > 0 {
		return zone[:lastHyphen]
	}
	return zone
}

// ConfigError represents a configuration validation error.
type ConfigError struct {
	Field   string
	Message string
}

func (e *ConfigError) Error() string {
	return "config error: " + e.Field + ": " + e.Message
}
