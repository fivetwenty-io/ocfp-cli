package gcp

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// Register registers the GCP provider with the CPI registry.
func Register() error {
	err := cpi.Register("gcp", NewProvider)
	if err != nil {
		return fmt.Errorf("failed to register GCP provider: %w", err)
	}

	return nil
}

// NewProvider creates a new GCP provider instance.
//
//nolint:ireturn // Returns interface by design for provider abstraction
func NewProvider(config interface{}) (cpi.Provider, error) {
	// If config is nil, return an uninitialized client that will be configured later via Initialize
	if config == nil {
		return NewClient(nil)
	}

	gcpConfig, err := convertToGCPConfig(config)
	if err != nil {
		return nil, err
	}

	// Auto-detect project and credentials from environment if not set
	detectProjectFromEnv(gcpConfig)
	detectCredentialsFromEnv(gcpConfig)
	detectRegionFromEnv(gcpConfig)

	// Validate required fields
	if gcpConfig.ProjectID == "" {
		return nil, ErrProjectIDRequired
	}

	if gcpConfig.ServiceAccountJSON == "" {
		return nil, ErrServiceAccountRequired
	}

	return NewClient(gcpConfig)
}

// convertToGCPConfig converts generic config to GCP config.
func convertToGCPConfig(config interface{}) (*Config, error) {
	switch cfg := config.(type) {
	case *Config:
		return cfg, nil
	case map[string]interface{}:
		return &Config{
			// Authentication
			ProjectID:           getString(cfg, "project_id"),
			ServiceAccountJSON:  getString(cfg, "service_account_json"),
			ServiceAccountEmail: getString(cfg, "service_account_email"),
			ImpersonateTarget:   getString(cfg, "impersonate_target"),

			// Location
			Region: getString(cfg, "region"),
			Zone:   getString(cfg, "zone"),

			// Network settings
			NetworkName:               getString(cfg, "network_name"),
			EnableSharedVPC:           getBool(cfg, "enable_shared_vpc"),
			HostProjectID:             getString(cfg, "host_project_id"),
			EnablePrivateGoogleAccess: getBool(cfg, "enable_private_google_access"),

			// Retry and timeout settings
			Timeout:    getDuration(cfg, "timeout"),
			MaxRetries: getInt(cfg, "max_retries"),
			RetryMode:  getString(cfg, "retry_mode"),

			// Connection pooling
			MaxIdleConns:        getInt(cfg, "max_idle_conns"),
			MaxIdleConnsPerHost: getInt(cfg, "max_idle_conns_per_host"),
			IdleConnTimeout:     getDuration(cfg, "idle_conn_timeout"),

			// Advanced settings
			EnableOSLogin:           getBool(cfg, "enable_os_login"),
			EnableSerialPortLogging: getBool(cfg, "enable_serial_port_logging"),
			UseCustomEndpoint:       getBool(cfg, "use_custom_endpoint"),
			ComputeEndpoint:         getString(cfg, "compute_endpoint"),
			StorageEndpoint:         getString(cfg, "storage_endpoint"),
			DebugLogging:            getBool(cfg, "debug_logging"),

			// Labels
			DefaultLabels: getStringMap(cfg, "default_labels"),
		}, nil
	default:
		return nil, fmt.Errorf("%w: %T", ErrInvalidConfigType, config)
	}
}

// detectProjectFromEnv detects project from environment variables.
func detectProjectFromEnv(gcpConfig *Config) {
	if gcpConfig.ProjectID != "" {
		return
	}

	// Try standard GCP environment variables
	for _, envVar := range []string{"GOOGLE_PROJECT", "GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT", "CLOUDSDK_CORE_PROJECT"} {
		if project := os.Getenv(envVar); project != "" {
			gcpConfig.ProjectID = project
			return
		}
	}
}

// detectCredentialsFromEnv detects credentials from environment variables.
func detectCredentialsFromEnv(gcpConfig *Config) {
	if gcpConfig.ServiceAccountJSON != "" {
		return
	}

	// Check for service account file path
	if credPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); credPath != "" {
		gcpConfig.ServiceAccountJSON = credPath
	}
}

// detectRegionFromEnv detects region/zone from environment variables.
func detectRegionFromEnv(gcpConfig *Config) {
	if gcpConfig.Zone == "" {
		if zone := os.Getenv("GOOGLE_ZONE"); zone != "" {
			gcpConfig.Zone = zone
		} else if zone := os.Getenv("CLOUDSDK_COMPUTE_ZONE"); zone != "" {
			gcpConfig.Zone = zone
		}
	}

	if gcpConfig.Region == "" {
		if region := os.Getenv("GOOGLE_REGION"); region != "" {
			gcpConfig.Region = region
		} else if region := os.Getenv("CLOUDSDK_COMPUTE_REGION"); region != "" {
			gcpConfig.Region = region
		} else if gcpConfig.Zone != "" {
			// Derive region from zone
			gcpConfig.Region = GetRegionFromZone(gcpConfig.Zone)
		}
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

// getStringMap safely gets a string map from a map.
func getStringMap(m map[string]interface{}, key string) map[string]string {
	value, ok := m[key]
	if !ok {
		return nil
	}

	result := make(map[string]string)

	if mapVal, ok := value.(map[string]interface{}); ok {
		for k, v := range mapVal {
			if str, ok := v.(string); ok {
				result[k] = str
			}
		}
		return result
	}

	if mapVal, ok := value.(map[string]string); ok {
		return mapVal
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

// DiscoverProvider attempts to auto-detect GCP environment and credentials.
func DiscoverProvider() (*Config, error) {
	config := DefaultConfig()

	// Try to detect project
	detectProjectFromEnv(config)
	if config.ProjectID == "" {
		return nil, errors.New("unable to auto-detect GCP project")
	}

	// Try to detect credentials
	detectCredentialsFromEnv(config)
	if config.ServiceAccountJSON == "" {
		return nil, errors.New("unable to auto-detect GCP credentials")
	}

	// Try to detect region/zone
	detectRegionFromEnv(config)

	return config, nil
}

// IsGCPEnvironment checks if we're running in a GCP environment.
func IsGCPEnvironment() bool {
	// Check for GCP environment variables
	envVars := []string{
		"GOOGLE_PROJECT",
		"GOOGLE_CLOUD_PROJECT",
		"GCLOUD_PROJECT",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"CLOUDSDK_CORE_PROJECT",
	}

	for _, envVar := range envVars {
		if os.Getenv(envVar) != "" {
			return true
		}
	}

	// Could also check for GCP metadata service at http://metadata.google.internal
	// but that would require an HTTP call

	return false
}
