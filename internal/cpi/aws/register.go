package aws

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

var (
	// ErrInvalidConfigType indicates the config type is not supported.
	ErrInvalidConfigType = errors.New("invalid config type for AWS provider")
	// ErrRegionRequired indicates that region is missing.
	ErrRegionRequired = errors.New("region is required for AWS provider")
	// ErrRegionAutoDetect indicates failure to auto-detect AWS region.
	ErrRegionAutoDetect = errors.New("unable to auto-detect AWS region")
)

// Register registers the AWS provider with the CPI registry.
func Register() error {
	err := cpi.Register("aws", NewProvider)
	if err != nil {
		return fmt.Errorf("failed to register AWS provider: %w", err)
	}

	return nil
}

// NewProvider creates a new AWS provider instance.
//
//nolint:ireturn // Returns interface by design for provider abstraction
func NewProvider(config interface{}) (cpi.Provider, error) {
	// If config is nil, return an uninitialized client that will be configured later via Initialize
	if config == nil {
		return NewClient(nil)
	}

	awsConfig, err := convertToAWSConfig(config)
	if err != nil {
		return nil, err
	}

	// Validate required fields
	if awsConfig.Region == "" {
		return nil, ErrRegionRequired
	}

	// Auto-detect region from environment if not set
	detectRegionFromEnv(awsConfig)

	// Auto-detect credentials from environment if not set
	detectCredentialsFromEnv(awsConfig)

	return NewClient(awsConfig)
}

// convertToAWSConfig converts generic config to AWS config.
func convertToAWSConfig(config interface{}) (*Config, error) {
	switch cfg := config.(type) {
	case *Config:
		return cfg, nil
	case map[string]interface{}:
		return &Config{
			// Authentication
			AccessKeyID:     getString(cfg, "access_key_id"),
			SecretAccessKey: getString(cfg, "secret_access_key"),
			SessionToken:    getString(cfg, "session_token"),
			Profile:         getString(cfg, "profile"),
			RoleARN:         getString(cfg, "role_arn"),
			RoleSessionName: getString(cfg, "role_session_name"),

			// Region and endpoints
			Region:             getString(cfg, "region"),
			EndpointURL:        getString(cfg, "endpoint_url"),
			STSEndpoint:        getString(cfg, "sts_endpoint"),
			EC2Endpoint:        getString(cfg, "ec2_endpoint"),
			S3Endpoint:         getString(cfg, "s3_endpoint"),
			ELBEndpoint:        getString(cfg, "elb_endpoint"),
			CloudWatchEndpoint: getString(cfg, "cloudwatch_endpoint"),

			// VPC settings
			VPCID:              getString(cfg, "vpc_id"),
			VPCCIDRBlock:       getString(cfg, "vpc_cidr_block"),
			EnableDNSHostnames: getBool(cfg, "enable_dns_hostnames"),
			EnableDNSSupport:   getBool(cfg, "enable_dns_support"),

			// Network settings
			AvailabilityZones: getStringSlice(cfg, "availability_zones"),
			EnableNATGateway:  getBool(cfg, "enable_nat_gateway"),
			EnableVPCPeering:  getBool(cfg, "enable_vpc_peering"),

			// Retry and timeout settings
			Timeout:    getDuration(cfg, "timeout"),
			MaxRetries: getInt(cfg, "max_retries"),
			RetryMode:  getString(cfg, "retry_mode"),

			// Connection pooling
			MaxIdleConns:        getInt(cfg, "max_idle_conns"),
			MaxIdleConnsPerHost: getInt(cfg, "max_idle_conns_per_host"),
			IdleConnTimeout:     getDuration(cfg, "idle_conn_timeout"),

			// Advanced settings
			EnableIMDSv2:             getBool(cfg, "enable_imdsv2"),
			EnableDetailedMonitoring: getBool(cfg, "enable_detailed_monitoring"),
			UsePathStyleS3:           getBool(cfg, "use_path_style_s3"),
			DisableSSL:               getBool(cfg, "disable_ssl"),
			DebugLogging:             getBool(cfg, "debug_logging"),
		}, nil
	default:
		return nil, fmt.Errorf("%w: %T", ErrInvalidConfigType, config)
	}
}

// detectRegionFromEnv detects region from environment variables.
func detectRegionFromEnv(awsConfig *Config) {
	if awsConfig.Region != "" {
		return
	}

	if region := os.Getenv("AWS_REGION"); region != "" {
		awsConfig.Region = region
	} else if region := os.Getenv("AWS_DEFAULT_REGION"); region != "" {
		awsConfig.Region = region
	}
}

// detectCredentialsFromEnv detects credentials from environment variables.
func detectCredentialsFromEnv(awsConfig *Config) {
	if awsConfig.AccessKeyID != "" || awsConfig.Profile != "" || awsConfig.RoleARN != "" {
		return
	}

	if accessKey := os.Getenv("AWS_ACCESS_KEY_ID"); accessKey != "" {
		awsConfig.AccessKeyID = accessKey
		if secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY"); secretKey != "" {
			awsConfig.SecretAccessKey = secretKey
		}

		if sessionToken := os.Getenv("AWS_SESSION_TOKEN"); sessionToken != "" {
			awsConfig.SessionToken = sessionToken
		}
	} else if profile := os.Getenv("AWS_PROFILE"); profile != "" {
		awsConfig.Profile = profile
	}
}

// getString safely gets a string from a map.
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}

	return ""
}

// getBool safely gets a bool from a map.
func getBool(m map[string]interface{}, key string) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}

	return false
}

// getInt safely gets an int from a map.
func getInt(m map[string]interface{}, key string) int {
	value, ok := m[key]
	if !ok {
		return 0
	}

	switch intValue := value.(type) {
	case int:
		return intValue
	case int64:
		return int(intValue)
	case float64:
		return int(intValue)
	}

	return 0
}

// getStringSlice safely gets a string slice from a map.
func getStringSlice(m map[string]interface{}, key string) []string {
	value, ok := m[key]
	if !ok {
		return nil
	}

	if slice, ok := value.([]interface{}); ok {
		result := make([]string, 0, len(slice))
		for _, item := range slice {
			if str, ok := item.(string); ok {
				result = append(result, str)
			}
		}

		return result
	}

	if slice, ok := value.([]string); ok {
		return slice
	}

	return nil
}

// getDuration safely gets a duration from a map.
func getDuration(m map[string]interface{}, key string) time.Duration {
	value, ok := m[key]
	if !ok {
		return 0
	}

	switch durationValue := value.(type) {
	case time.Duration:
		return durationValue
	case int64:
		return time.Duration(durationValue)
	case float64:
		return time.Duration(durationValue)
	case string:
		parsed, err := time.ParseDuration(durationValue)
		if err == nil {
			return parsed
		}
	}

	return 0
}

// DiscoverProvider attempts to auto-detect AWS environment and credentials.
func DiscoverProvider() (*Config, error) {
	config := &Config{}

	// Try to detect region
	if region := os.Getenv("AWS_REGION"); region != "" {
		config.Region = region
	} else if region := os.Getenv("AWS_DEFAULT_REGION"); region != "" {
		config.Region = region
	} else {
		// Try to read from EC2 instance metadata
		// This would require HTTP call to 169.254.169.254, skip for now
		// Default to us-east-1
		config.Region = "us-east-1"
	}

	// Try to detect credentials
	if accessKey := os.Getenv("AWS_ACCESS_KEY_ID"); accessKey != "" {
		config.AccessKeyID = accessKey
		if secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY"); secretKey != "" {
			config.SecretAccessKey = secretKey
		}

		if sessionToken := os.Getenv("AWS_SESSION_TOKEN"); sessionToken != "" {
			config.SessionToken = sessionToken
		}
	} else if profile := os.Getenv("AWS_PROFILE"); profile != "" {
		config.Profile = profile
	}

	// Check if we have enough information
	if config.Region == "" {
		return nil, ErrRegionAutoDetect
	}

	// Note: Credentials are optional here as the AWS SDK can use instance profiles
	// or other credential providers

	return config, nil
}

// IsAWSEnvironment checks if we're running in an AWS environment.
func IsAWSEnvironment() bool {
	// Check for AWS environment variables
	if os.Getenv("AWS_REGION") != "" || os.Getenv("AWS_DEFAULT_REGION") != "" {
		return true
	}

	if os.Getenv("AWS_ACCESS_KEY_ID") != "" || os.Getenv("AWS_PROFILE") != "" {
		return true
	}

	// Could also check for EC2 instance metadata service
	// but that would require an HTTP call
	// http://169.254.169.254/latest/meta-data/

	return false
}
