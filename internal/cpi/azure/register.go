package azure

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

var (
	// ErrInvalidConfigType indicates the config type is not supported.
	ErrInvalidConfigType = errors.New("invalid config type for Azure provider")
	// ErrSubscriptionIDRequired indicates that subscription ID is missing.
	ErrSubscriptionIDRequired = errors.New("subscription ID is required for Azure provider")
	// ErrLocationRequired indicates that location is missing.
	ErrLocationRequired = errors.New("location is required for Azure provider")
	// ErrResourceGroupRequired indicates that resource group is missing.
	ErrResourceGroupRequired = errors.New("resource group is required for Azure provider")
)

// Register registers the Azure provider with the CPI registry.
func Register() error {
	err := cpi.Register("azure", NewProvider)
	if err != nil {
		return fmt.Errorf("failed to register Azure provider: %w", err)
	}

	return nil
}

// NewProvider creates a new Azure provider instance.
//
//nolint:ireturn // Returns interface by design for provider abstraction
func NewProvider(config interface{}) (cpi.Provider, error) {
	// If config is nil, return an uninitialized client that will be configured later via Initialize
	if config == nil {
		return NewClient(nil)
	}

	azureConfig, err := convertToAzureConfig(config)
	if err != nil {
		return nil, err
	}

	// Auto-detect configuration from environment
	detectSubscriptionFromEnv(azureConfig)
	detectCredentialsFromEnv(azureConfig)
	detectLocationFromEnv(azureConfig)

	// Validate required fields after environment detection
	if azureConfig.SubscriptionID == "" {
		return nil, ErrSubscriptionIDRequired
	}

	if azureConfig.Location == "" {
		return nil, ErrLocationRequired
	}

	return NewClient(azureConfig)
}

// convertToAzureConfig converts generic config to Azure config.
func convertToAzureConfig(config interface{}) (*Config, error) {
	switch cfg := config.(type) {
	case *Config:
		return cfg, nil
	case map[string]interface{}:
		return &Config{
			// Authentication - Service Principal
			SubscriptionID:    getString(cfg, "subscription_id"),
			TenantID:          getString(cfg, "tenant_id"),
			ClientID:          getString(cfg, "client_id"),
			ClientSecret:      getString(cfg, "client_secret"),
			ClientCertificate: getString(cfg, "client_certificate"),

			// Authentication - Managed Identity
			UseManagedIdentity:     getBool(cfg, "use_managed_identity"),
			ManagedIdentityType:    getString(cfg, "managed_identity_type"),
			UserAssignedIdentityID: getString(cfg, "user_assigned_identity_id"),

			// Authentication - Azure CLI
			UseAzureCLI: getBool(cfg, "use_azure_cli"),

			// Location and Resource Organization
			Location:            getString(cfg, "location"),
			ResourceGroup:       getString(cfg, "resource_group"),
			CreateResourceGroup: getBool(cfg, "create_resource_group"),

			// Network settings
			VNetName:                    getString(cfg, "vnet_name"),
			VNetAddressSpace:            getString(cfg, "vnet_address_space"),
			AvailabilityZones:           getStringSlice(cfg, "availability_zones"),
			EnableAcceleratedNetworking: getBoolDefault(cfg, "enable_accelerated_networking", true),

			// Retry and timeout settings
			Timeout:    getDuration(cfg, "timeout"),
			MaxRetries: getInt(cfg, "max_retries"),
			RetryMode:  getString(cfg, "retry_mode"),

			// Connection pooling
			MaxIdleConns:        getInt(cfg, "max_idle_conns"),
			MaxIdleConnsPerHost: getInt(cfg, "max_idle_conns_per_host"),
			IdleConnTimeout:     getDuration(cfg, "idle_conn_timeout"),
			TLSHandshakeTimeout: getDuration(cfg, "tls_handshake_timeout"),
			DialTimeout:         getDuration(cfg, "dial_timeout"),
			KeepAlive:           getDuration(cfg, "keep_alive"),

			// Tagging
			DefaultTags: getStringMap(cfg, "default_tags"),

			// Advanced settings
			CloudName:                getString(cfg, "cloud_name"),
			CustomEndpoint:           getString(cfg, "custom_endpoint"),
			DisableInstanceMetadata:  getBool(cfg, "disable_instance_metadata"),
			EnableDiagnosticsLogging: getBool(cfg, "enable_diagnostics_logging"),
			DebugLogging:             getBool(cfg, "debug_logging"),
		}, nil
	default:
		return nil, fmt.Errorf("%w: %T", ErrInvalidConfigType, config)
	}
}

// detectSubscriptionFromEnv detects subscription ID from environment variables.
func detectSubscriptionFromEnv(azureConfig *Config) {
	if azureConfig.SubscriptionID != "" {
		return
	}

	if subscriptionID := os.Getenv("AZURE_SUBSCRIPTION_ID"); subscriptionID != "" {
		azureConfig.SubscriptionID = subscriptionID
	}
}

// detectCredentialsFromEnv detects credentials from environment variables.
func detectCredentialsFromEnv(azureConfig *Config) {
	// Skip if credentials are already configured
	if azureConfig.ClientID != "" || azureConfig.UseManagedIdentity || azureConfig.UseAzureCLI {
		return
	}

	// Try service principal credentials
	if clientID := os.Getenv("AZURE_CLIENT_ID"); clientID != "" {
		azureConfig.ClientID = clientID

		if tenantID := os.Getenv("AZURE_TENANT_ID"); tenantID != "" {
			azureConfig.TenantID = tenantID
		}

		if clientSecret := os.Getenv("AZURE_CLIENT_SECRET"); clientSecret != "" {
			azureConfig.ClientSecret = clientSecret
		} else if certPath := os.Getenv("AZURE_CLIENT_CERTIFICATE_PATH"); certPath != "" {
			azureConfig.ClientCertificate = certPath
		}
	}

	// Try managed identity flag
	if useMI := os.Getenv("AZURE_USE_MANAGED_IDENTITY"); useMI == "true" || useMI == "1" {
		azureConfig.UseManagedIdentity = true
		if userAssignedID := os.Getenv("AZURE_CLIENT_ID"); userAssignedID != "" && azureConfig.ClientID == "" {
			azureConfig.UserAssignedIdentityID = userAssignedID
		}
	}
}

// detectLocationFromEnv detects location from environment variables.
func detectLocationFromEnv(azureConfig *Config) {
	if azureConfig.Location != "" {
		return
	}

	if location := os.Getenv("AZURE_LOCATION"); location != "" {
		azureConfig.Location = location
	} else if location := os.Getenv("AZURE_REGION"); location != "" {
		azureConfig.Location = location
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

// getBoolDefault safely gets a bool from a map with a default value.
func getBoolDefault(m map[string]interface{}, key string, defaultVal bool) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}

	return defaultVal
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

	if strMap, ok := value.(map[string]string); ok {
		return strMap
	}

	if ifaceMap, ok := value.(map[string]interface{}); ok {
		result := make(map[string]string)
		for k, v := range ifaceMap {
			if str, ok := v.(string); ok {
				result[k] = str
			}
		}
		return result
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

// DiscoverProvider attempts to auto-detect Azure environment and credentials.
func DiscoverProvider() (*Config, error) {
	config := DefaultConfig()

	// Try to detect subscription ID
	if subscriptionID := os.Getenv("AZURE_SUBSCRIPTION_ID"); subscriptionID != "" {
		config.SubscriptionID = subscriptionID
	}

	// Try to detect location
	if location := os.Getenv("AZURE_LOCATION"); location != "" {
		config.Location = location
	} else if location := os.Getenv("AZURE_REGION"); location != "" {
		config.Location = location
	}

	// Try to detect resource group
	if rg := os.Getenv("AZURE_RESOURCE_GROUP"); rg != "" {
		config.ResourceGroup = rg
	}

	// Try to detect credentials
	if clientID := os.Getenv("AZURE_CLIENT_ID"); clientID != "" {
		config.ClientID = clientID
		if tenantID := os.Getenv("AZURE_TENANT_ID"); tenantID != "" {
			config.TenantID = tenantID
		}
		if clientSecret := os.Getenv("AZURE_CLIENT_SECRET"); clientSecret != "" {
			config.ClientSecret = clientSecret
		}
	}

	// Check for managed identity
	if useMI := os.Getenv("AZURE_USE_MANAGED_IDENTITY"); useMI == "true" || useMI == "1" {
		config.UseManagedIdentity = true
	}

	return config, nil
}

// IsAzureEnvironment checks if we're running in an Azure environment.
func IsAzureEnvironment() bool {
	// Check for Azure environment variables
	if os.Getenv("AZURE_SUBSCRIPTION_ID") != "" {
		return true
	}

	if os.Getenv("AZURE_CLIENT_ID") != "" {
		return true
	}

	if os.Getenv("AZURE_TENANT_ID") != "" {
		return true
	}

	// Check for Azure Instance Metadata Service marker
	if os.Getenv("AZURE_USE_MANAGED_IDENTITY") == "true" {
		return true
	}

	return false
}
