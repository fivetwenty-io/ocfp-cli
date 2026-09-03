package aws

import (
	"net"
	"net/http"
	"time"
)

// Config holds AWS-specific configuration.
type Config struct {
	// Authentication
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string // #nosec G101 -- field name is descriptive, not a hardcoded secret
	Profile         string
	RoleARN         string
	RoleSessionName string

	// Region and endpoints
	Region             string
	EndpointURL        string
	STSEndpoint        string
	EC2Endpoint        string
	S3Endpoint         string
	ELBEndpoint        string
	CloudWatchEndpoint string

	// VPC settings
	VPCID              string
	VPCCIDRBlock       string
	EnableDNSHostnames bool
	EnableDNSSupport   bool

	// Network settings
	AvailabilityZones []string
	EnableNATGateway  bool
	EnableVPCPeering  bool

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

	// Tagging
	DefaultTags map[string]string

	// Advanced settings
	EnableIMDSv2             bool
	EnableDetailedMonitoring bool
	UsePathStyleS3           bool
	DisableSSL               bool
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
		Region:                   "us-east-1",
		Timeout:                  defaultTimeout * time.Second, //nolint:mnd // Using constant
		MaxRetries:               defaultMaxRetries,
		RetryMode:                "standard",
		MaxIdleConns:             defaultMaxIdleConns,
		MaxIdleConnsPerHost:      defaultMaxIdleConnsPerHost,
		IdleConnTimeout:          defaultIdleConnTimeout * time.Second,     //nolint:mnd // Using constant
		TLSHandshakeTimeout:      defaultTLSHandshakeTimeout * time.Second, //nolint:mnd // Using constant
		DialTimeout:              defaultDialTimeout * time.Second,         //nolint:mnd // Using constant
		KeepAlive:                defaultKeepAlive * time.Second,           //nolint:mnd // Using constant
		EnableDNSHostnames:       true,
		EnableDNSSupport:         true,
		EnableIMDSv2:             true,
		EnableDetailedMonitoring: false,
		UsePathStyleS3:           false,
		DisableSSL:               false,
		DebugLogging:             false,
		DefaultTags:              make(map[string]string),
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
	if c.Region == "" {
		return &ConfigError{Field: "Region", Message: "region is required"}
	}

	// If using static credentials, both access key and secret key must be provided
	if c.AccessKeyID != "" && c.SecretAccessKey == "" {
		return &ConfigError{Field: "SecretAccessKey", Message: "secret access key required when access key ID is provided"}
	}

	if c.SecretAccessKey != "" && c.AccessKeyID == "" {
		return &ConfigError{Field: "AccessKeyID", Message: "access key ID required when secret access key is provided"}
	}

	// Validate VPC CIDR if provided
	if c.VPCCIDRBlock != "" {
		if !isValidCIDR(c.VPCCIDRBlock) {
			return &ConfigError{Field: "VPCCIDRBlock", Message: "invalid CIDR block format"}
		}
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

// isValidCIDR validates an IPv4 CIDR block using the standard library.
func isValidCIDR(cidr string) bool {
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}

	return ip.To4() != nil
}

// ConfigError represents a configuration validation error.
type ConfigError struct {
	Field   string
	Message string
}

func (e *ConfigError) Error() string {
	return "config error: " + e.Field + ": " + e.Message
}
