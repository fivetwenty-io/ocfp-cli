// Package aws implements the CPI provider for Amazon Web Services.
package aws

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	ocfpconfig "github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	// ProviderName is the name of this provider.
	ProviderName = "aws"
)

// Client implements the AWS provider.
type Client struct {
	config *Config
	mu     sync.RWMutex

	// AWS SDK configuration
	awsConfig aws.Config

	// Resource managers
	network      *NetworkManager
	compute      *ComputeManager
	storage      *StorageManager
	security     *SecurityManager
	loadBalancer *LoadBalancerManager

	// SDK clients (lazy-loaded)
	ec2Client     *ec2.Client
	s3Client      *s3.Client
	elbClient     *elasticloadbalancingv2.Client
	stsClient     *sts.Client
	clientsLoaded bool
}

// NewClient creates a new AWS client.
func NewClient(config *Config) (*Client, error) {
	// Allow nil config for uninitialized client (will be configured via Initialize)
	if config == nil {
		return &Client{
			config:        nil,
			awsConfig:     aws.Config{},
			network:       nil,
			compute:       nil,
			storage:       nil,
			security:      nil,
			loadBalancer:  nil,
			ec2Client:     nil,
			s3Client:      nil,
			elbClient:     nil,
			stsClient:     nil,
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
		awsConfig:     aws.Config{},
		network:       nil,
		compute:       nil,
		storage:       nil,
		security:      nil,
		loadBalancer:  nil,
		ec2Client:     nil,
		s3Client:      nil,
		elbClient:     nil,
		stsClient:     nil,
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

// Region returns the configured region.
func (c *Client) Region() string {
	if c.config == nil {
		return ""
	}

	return c.config.Region
}

// Initialize configures the AWS client with the provided configuration.
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

	// Initialize AWS SDK config
	err = c.initializeAWSConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize AWS configuration: %w", err)
	}

	// Initialize resource managers
	c.initializeResourceManagers()

	logger.Debugw("AWS provider initialized", "region", c.config.Region)

	return nil
}

// Authenticate validates AWS credentials.
func (c *Client) Authenticate(ctx context.Context) error {
	return c.ValidateCredentials(ctx)
}

// ValidateCredentials validates AWS credentials using STS GetCallerIdentity.
func (c *Client) ValidateCredentials(ctx context.Context) error {
	err := c.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	_, err = c.stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return WrapAWSError(err, "failed to validate AWS credentials")
	}

	logger.Debug("AWS credentials validated successfully")

	return nil
}

// Cleanup releases resources and closes connections.
func (c *Client) Cleanup(_ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Reset clients
	c.ec2Client = nil
	c.s3Client = nil
	c.elbClient = nil
	c.stsClient = nil
	c.clientsLoaded = false

	logger.Debug("AWS provider cleaned up")

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
		return nil, fmt.Errorf("invalid config type for AWS provider: expected *aws.Config or *config.Config, got %T", config)
	}
}

// convertOCFPConfig converts OCFP config to AWS config.
func (c *Client) convertOCFPConfig(configValue *ocfpconfig.Config) *Config {
	cfg := &Config{
		AccessKeyID:     configValue.AccessKeyID,
		SecretAccessKey: configValue.SecretAccessKey,
		SessionToken:    configValue.SessionToken,
		Profile:         "", // Will be set to bloc name below if not specified
		Region:          configValue.Region,
	}

	// Default profile to bloc name if no credentials provided
	if cfg.AccessKeyID == "" && cfg.SecretAccessKey == "" && cfg.SessionToken == "" {
		cfg.Profile = configValue.Name
	}

	return cfg
}

// handleMapConfig handles map[string]interface{} configuration type.
func (c *Client) handleMapConfig(ctx context.Context) (*Config, error) {
	// Config was already parsed in NewProvider and stored in c.config
	// Now we need to initialize the AWS SDK config if not already done
	if c.config != nil && c.awsConfig.Region == "" {
		err := c.initializeAWSConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize AWS configuration: %w", err)
		}

		// Initialize resource managers if not already done
		if c.network == nil {
			c.initializeResourceManagers()
		}

		logger.Debugw("AWS provider initialized from map config", "region", c.config.Region)
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

func (c *Client) initializeAWSConfig(ctx context.Context) error {
	return c.realInitializeAWSConfig(ctx)
}

func (c *Client) ensureClientsLoaded(ctx context.Context) error {
	return c.realEnsureClientsLoaded(ctx)
}

func (c *Client) getEC2Client(ctx context.Context) (*ec2.Client, error) {
	err := c.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.ec2Client == nil {
		//nolint:err113 // Dynamic error acceptable for uninitialized client
		return nil, errors.New("EC2 client not initialized")
	}

	return c.ec2Client, nil
}

func (c *Client) getS3Client(ctx context.Context) (*s3.Client, error) {
	err := c.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.s3Client == nil {
		//nolint:err113 // Dynamic error acceptable for uninitialized client
		return nil, errors.New("S3 client not initialized")
	}

	return c.s3Client, nil
}

func (c *Client) getELBClient(ctx context.Context) (*elasticloadbalancingv2.Client, error) {
	err := c.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.elbClient == nil {
		//nolint:err113 // Dynamic error acceptable for uninitialized client
		return nil, errors.New("ELB client not initialized")
	}

	return c.elbClient, nil
}

//nolint:cyclop,funlen // Helper method after interface methods, initialization requires length
func (c *Client) realInitializeAWSConfig(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var opts []func(*awsconfig.LoadOptions) error

	// Set region
	opts = append(opts, awsconfig.WithRegion(c.config.Region))

	// Configure credentials based on priority order:
	// 1. Static credentials (AccessKeyID + SecretAccessKey)
	// 2. Profile-based credentials
	// 3. Environment variables (default behavior)
	// 4. Shared credentials file (default behavior)
	// 5. IAM role from EC2 instance metadata (default behavior)

	//nolint:gocritic // if-else chain is clearer than switch for credential selection
	if c.config.AccessKeyID != "" && c.config.SecretAccessKey != "" {
		// Static credentials provided
		opts = append(opts, awsconfig.WithCredentialsProvider(
			aws.CredentialsProviderFunc(func(_ctx context.Context) (aws.Credentials, error) {
				return aws.Credentials{
					AccessKeyID:     c.config.AccessKeyID,
					SecretAccessKey: c.config.SecretAccessKey,
					SessionToken:    c.config.SessionToken,
					Source:          "StaticCredentials",
				}, nil
			}),
		))

		logger.Debug("Using static AWS credentials")
	} else if c.config.Profile != "" {
		// Use shared config profile
		opts = append(opts, awsconfig.WithSharedConfigProfile(c.config.Profile))
		logger.Debugw("Using AWS profile", "profile", c.config.Profile)
	} else {
		// Use default credential chain (env vars, shared config, instance profile)
		logger.Debug("Using default AWS credential chain (env vars, shared config, instance profile)")
	}

	// Configure retry mode and strategy
	if c.config.RetryMode != "" || c.config.MaxRetries > 0 {
		var mode aws.RetryMode

		maxAttempts := c.config.MaxRetries
		if maxAttempts == 0 {
			maxAttempts = 3 //nolint:mnd // default retry attempts
		}

		switch c.config.RetryMode {
		case "adaptive":
			mode = aws.RetryModeAdaptive
		case "standard":
			mode = aws.RetryModeStandard
		default:
			mode = aws.RetryModeStandard
		}

		opts = append(opts,
			awsconfig.WithRetryMode(mode),
			awsconfig.WithRetryMaxAttempts(maxAttempts),
		)

		// Add custom retryer with exponential backoff and jitter
		opts = append(opts, awsconfig.WithRetryer(func() aws.Retryer {
			return retry.NewStandard(func(o *retry.StandardOptions) {
				o.MaxAttempts = maxAttempts
				o.MaxBackoff = 20 * time.Second //nolint:mnd // max backoff time
			})
		}))

		logger.Debugw("Configured retry strategy", "mode", mode, "maxAttempts", maxAttempts)
	}

	// Configure custom endpoint overrides if provided
	if c.config.EndpointURL != "" {
		logger.Debugw("Using custom AWS endpoint", "endpoint", c.config.EndpointURL)
	}

	// Add custom HTTP client with connection pooling if configured
	if c.config.MaxIdleConns > 0 || c.config.DialTimeout > 0 {
		httpClient := c.config.NewHTTPClient()
		opts = append(opts, awsconfig.WithHTTPClient(httpClient))

		logger.Debug("Configured custom HTTP client with connection pooling",
			"maxIdleConns", c.config.MaxIdleConns,
			"maxIdleConnsPerHost", c.config.MaxIdleConnsPerHost,
			"idleConnTimeout", c.config.IdleConnTimeout,
			"dialTimeout", c.config.DialTimeout)
	}

	// Load AWS configuration
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return WrapAWSError(err, "failed to load AWS configuration")
	}

	// Apply timeout configuration if specified
	if c.config.Timeout > 0 {
		logger.Debugw("Configured API timeout", "timeout", c.config.Timeout)
	}

	c.awsConfig = awsCfg

	// If role ARN is specified, configure STS assume role
	err = c.configureAssumeRole(ctx)
	if err != nil {
		return fmt.Errorf("failed to configure assume role: %w", err)
	}

	logger.Debugw("AWS SDK configuration initialized", "region", c.config.Region)

	return nil
}

// configureAssumeRole sets up STS assume role credentials.
//
//nolint:unparam // Helper method after main initialization
func (c *Client) configureAssumeRole(_ context.Context) error {
	// Skip if no role ARN configured
	if c.config.RoleARN == "" {
		return nil
	}

	// Create STS client for role assumption
	stsClient := sts.NewFromConfig(c.awsConfig)

	// Set up role session name
	sessionName := c.config.RoleSessionName
	if sessionName == "" {
		sessionName = fmt.Sprintf("ocfp-cli-%d", time.Now().Unix())
	}

	// Create credentials provider that assumes the role
	//nolint:varnamelen // opts is standard for function options
	roleProvider := stscreds.NewAssumeRoleProvider(stsClient, c.config.RoleARN, func(opts *stscreds.AssumeRoleOptions) {
		opts.RoleSessionName = sessionName

		if c.config.Timeout > 0 {
			// Set session duration (must be between 15 minutes and 12 hours)
			duration := c.config.Timeout

			const minDuration = 15 * time.Minute //nolint:mnd // AWS minimum

			const maxDuration = 12 * time.Hour //nolint:mnd // AWS maximum

			if duration < minDuration {
				duration = minDuration
			} else if duration > maxDuration {
				duration = maxDuration
			}

			opts.Duration = duration
		}
	})

	// Update AWS config to use the assume role credentials
	c.awsConfig.Credentials = aws.NewCredentialsCache(roleProvider)

	logger.Debugw("Configured STS assume role", "roleARN", c.config.RoleARN, "sessionName", sessionName)

	return nil
}

//nolint:unparam,funlen // Helper method after interface methods, client init requires length
func (c *Client) realEnsureClientsLoaded(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.clientsLoaded {
		return nil
	}

	if c.config == nil {
		//nolint:err113 // Simple static error message
		return errors.New("client not initialized: config is nil")
	}

	// Validate region before creating clients
	err := c.validateRegion(ctx)
	if err != nil {
		return fmt.Errorf("invalid region: %w", err)
	}

	// Initialize STS client first for credential validation
	if c.config.STSEndpoint != "" {
		c.stsClient = sts.NewFromConfig(c.awsConfig, func(o *sts.Options) {
			o.BaseEndpoint = aws.String(c.config.STSEndpoint)
		})
		logger.Debugw("Using custom STS endpoint", "endpoint", c.config.STSEndpoint)
	} else {
		c.stsClient = sts.NewFromConfig(c.awsConfig)
	}

	// Initialize EC2 client with optional custom endpoint
	if c.config.EC2Endpoint != "" {
		c.ec2Client = ec2.NewFromConfig(c.awsConfig, func(o *ec2.Options) {
			o.BaseEndpoint = aws.String(c.config.EC2Endpoint)
		})
		logger.Debugw("Using custom EC2 endpoint", "endpoint", c.config.EC2Endpoint)
	} else {
		c.ec2Client = ec2.NewFromConfig(c.awsConfig)
	}

	// Initialize S3 client with optional custom endpoint and path-style configuration
	if c.config.S3Endpoint != "" || c.config.UsePathStyleS3 {
		//nolint:varnamelen // opts is standard for function options
		c.s3Client = s3.NewFromConfig(c.awsConfig, func(opts *s3.Options) {
			if c.config.S3Endpoint != "" {
				opts.BaseEndpoint = aws.String(c.config.S3Endpoint)
				logger.Debugw("Using custom S3 endpoint", "endpoint", c.config.S3Endpoint)
			}

			opts.UsePathStyle = c.config.UsePathStyleS3
		})
	} else {
		c.s3Client = s3.NewFromConfig(c.awsConfig)
	}

	// Initialize ELB client with optional custom endpoint
	if c.config.ELBEndpoint != "" {
		c.elbClient = elasticloadbalancingv2.NewFromConfig(c.awsConfig, func(o *elasticloadbalancingv2.Options) {
			o.BaseEndpoint = aws.String(c.config.ELBEndpoint)
		})
		logger.Debugw("Using custom ELB endpoint", "endpoint", c.config.ELBEndpoint)
	} else {
		c.elbClient = elasticloadbalancingv2.NewFromConfig(c.awsConfig)
	}

	c.clientsLoaded = true

	logger.Debugw("AWS service clients loaded", "region", c.config.Region)

	return nil
}

// validateRegion validates the configured AWS region.
//
//nolint:unparam // Helper method after client loading
func (c *Client) validateRegion(_ context.Context) error {
	// List of valid AWS regions (as of 2024)
	validRegions := map[string]bool{
		// US regions
		"us-east-1":      true, // N. Virginia
		"us-east-2":      true, // Ohio
		"us-west-1":      true, // N. California
		"us-west-2":      true, // Oregon
		"us-gov-east-1":  true, // GovCloud East
		"us-gov-west-1":  true, // GovCloud West
		"us-isob-east-1": true, // ISO-B
		"us-iso-east-1":  true, // ISO
		"us-iso-west-1":  true, // ISO

		// Europe regions
		"eu-west-1":      true, // Ireland
		"eu-west-2":      true, // London
		"eu-west-3":      true, // Paris
		"eu-central-1":   true, // Frankfurt
		"eu-central-2":   true, // Zurich
		"eu-north-1":     true, // Stockholm
		"eu-south-1":     true, // Milan
		"eu-south-2":     true, // Spain
		"eu-isoe-west-1": true, // ISO-E

		// Asia Pacific regions
		"ap-south-1":     true, // Mumbai
		"ap-south-2":     true, // Hyderabad
		"ap-northeast-1": true, // Tokyo
		"ap-northeast-2": true, // Seoul
		"ap-northeast-3": true, // Osaka
		"ap-southeast-1": true, // Singapore
		"ap-southeast-2": true, // Sydney
		"ap-southeast-3": true, // Jakarta
		"ap-southeast-4": true, // Melbourne
		"ap-east-1":      true, // Hong Kong

		// Middle East and Africa
		"me-south-1":   true, // Bahrain
		"me-central-1": true, // UAE
		"af-south-1":   true, // Cape Town
		"il-central-1": true, // Israel

		// South America
		"sa-east-1": true, // São Paulo

		// Canada
		"ca-central-1": true, // Canada (Central)
		"ca-west-1":    true, // Canada (West)
	}

	if !validRegions[c.config.Region] {
		// If custom endpoint is set, allow any region (for testing/LocalStack)
		if c.config.EndpointURL != "" || c.config.EC2Endpoint != "" {
			logger.Debugw("Using custom endpoint with non-standard region", "region", c.config.Region)

			return nil
		}

		//nolint:perfsprint // Error message construction for better readability
		return &ConfigError{
			Field:   "Region",
			Message: fmt.Sprintf("invalid AWS region: %s", c.config.Region),
		}
	}

	logger.Debugw("Region validated", "region", c.config.Region)

	return nil
}

func wrapError(err error, message string) error {
	if err == nil {
		return nil
	}

	return WrapAWSError(err, message)
}
