package azure

import (
	"net"
	"net/http"
	"time"
)

// Config holds Azure-specific configuration.
type Config struct {
	// Authentication - Service Principal
	SubscriptionID    string
	TenantID          string
	ClientID          string
	ClientSecret      string // #nosec G101 -- field name is descriptive, not a hardcoded secret
	ClientCertificate string // Path to certificate for cert-based auth

	// Authentication - Managed Identity
	UseManagedIdentity     bool
	ManagedIdentityType    string // "SystemAssigned" or "UserAssigned"
	UserAssignedIdentityID string // For user-assigned managed identity

	// Authentication - Azure CLI
	UseAzureCLI bool // Use az CLI for authentication

	// Location and Resource Organization
	Location            string // Azure region (e.g., "eastus", "westeurope")
	ResourceGroup       string // Default resource group name
	CreateResourceGroup bool   // Auto-create resource group if missing

	// Network settings
	VNetName                    string
	VNetAddressSpace            string // CIDR block for VNet (e.g., "10.0.0.0/16")
	AvailabilityZones           []string
	EnableAcceleratedNetworking bool

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

	// Tagging (Azure uses tags, similar to AWS)
	DefaultTags map[string]string

	// Advanced settings
	CloudName                string // "AzurePublic", "AzureGovernment", "AzureChina"
	CustomEndpoint           string // For Azure Stack or testing
	DisableInstanceMetadata  bool
	EnableDiagnosticsLogging bool
	DebugLogging             bool
}

const (
	defaultTimeout             = 30
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
		Location:                    "eastus",
		CloudName:                   "AzurePublic",
		Timeout:                     defaultTimeout * time.Second, //nolint:mnd // Using constant
		MaxRetries:                  defaultMaxRetries,
		RetryMode:                   "standard",
		MaxIdleConns:                defaultMaxIdleConns,
		MaxIdleConnsPerHost:         defaultMaxIdleConnsPerHost,
		IdleConnTimeout:             defaultIdleConnTimeout * time.Second,     //nolint:mnd // Using constant
		TLSHandshakeTimeout:         defaultTLSHandshakeTimeout * time.Second, //nolint:mnd // Using constant
		DialTimeout:                 defaultDialTimeout * time.Second,         //nolint:mnd // Using constant
		KeepAlive:                   defaultKeepAlive * time.Second,           //nolint:mnd // Using constant
		CreateResourceGroup:         false,
		EnableAcceleratedNetworking: true,
		DisableInstanceMetadata:     false,
		EnableDiagnosticsLogging:    false,
		DebugLogging:                false,
		DefaultTags:                 make(map[string]string),
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
		ExpectContinueTimeout: 1 * time.Second, //nolint:mnd // standard HTTP timeout
		ForceAttemptHTTP2:     true,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   c.Timeout,
	}
}

// Validate checks if the configuration is valid.
func (c *Config) Validate() error {
	if c.SubscriptionID == "" {
		return &ConfigError{Field: "SubscriptionID", Message: "subscription ID is required"}
	}

	if c.Location == "" {
		return &ConfigError{Field: "Location", Message: "location is required"}
	}

	if c.ResourceGroup == "" {
		return &ConfigError{Field: "ResourceGroup", Message: "resource group is required"}
	}

	err := c.validateServicePrincipal()
	if err != nil {
		return err
	}

	err = c.validateNetworkAndRetry()
	if err != nil {
		return err
	}

	return nil
}

// isValidCIDR performs basic CIDR validation.
func isValidCIDR(cidr string) bool {
	_, _, err := net.ParseCIDR(cidr)

	return err == nil
}

// ConfigError represents a configuration validation error.
type ConfigError struct {
	Field   string
	Message string
}

func (e *ConfigError) Error() string {
	return "config error: " + e.Field + ": " + e.Message
}

// GetCloudName returns the cloud name, defaulting to AzurePublic.
func (c *Config) GetCloudName() string {
	if c.CloudName == "" {
		return "AzurePublic"
	}

	return c.CloudName
}

// HasServicePrincipalCredentials returns true if service principal credentials are configured.
func (c *Config) HasServicePrincipalCredentials() bool {
	return c.ClientID != "" && c.TenantID != "" && (c.ClientSecret != "" || c.ClientCertificate != "")
}

// HasManagedIdentity returns true if managed identity is configured.
func (c *Config) HasManagedIdentity() bool {
	return c.UseManagedIdentity
}

// validateServicePrincipal validates service principal credential fields.
func (c *Config) validateServicePrincipal() error {
	if c.ClientID == "" {
		return nil
	}

	if c.TenantID == "" {
		return &ConfigError{Field: "TenantID", Message: "tenant ID required when client ID is provided"}
	}

	if c.ClientSecret == "" && c.ClientCertificate == "" && !c.UseManagedIdentity {
		return &ConfigError{Field: "ClientSecret", Message: "client secret or certificate required when client ID is provided"}
	}

	return nil
}

// validateNetworkAndRetry validates network, retry, and cloud name settings.
func (c *Config) validateNetworkAndRetry() error {
	validCloudNames := map[string]bool{
		"AzurePublic":     true,
		"AzureGovernment": true,
		"AzureChina":      true,
		"":                true, // empty defaults to AzurePublic
	}

	if c.VNetAddressSpace != "" && !isValidCIDR(c.VNetAddressSpace) {
		return &ConfigError{Field: "VNetAddressSpace", Message: "invalid CIDR block format"}
	}

	if c.MaxRetries < 0 {
		return &ConfigError{Field: "MaxRetries", Message: "max retries cannot be negative"}
	}

	if c.RetryMode != "" && c.RetryMode != "standard" && c.RetryMode != "adaptive" {
		return &ConfigError{Field: "RetryMode", Message: "retry mode must be 'standard' or 'adaptive'"}
	}

	if !validCloudNames[c.CloudName] {
		return &ConfigError{Field: "CloudName", Message: "cloud name must be 'AzurePublic', 'AzureGovernment', or 'AzureChina'"}
	}

	return nil
}
