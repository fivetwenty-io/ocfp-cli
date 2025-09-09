package stackit

import (
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// Register registers the STACKIT provider with the CPI registry.
func Register() error {
	err := cpi.Register("stackit", NewProvider)
	if err != nil {
		return fmt.Errorf("failed to register STACKIT provider: %w", err)
	}

	return nil
}

// NewProvider creates a new STACKIT provider instance.
//
//nolint:ireturn // Returns interface by design for provider abstraction
func NewProvider(config interface{}) (cpi.Provider, error) {
	// Convert generic config to STACKIT config
	var stackitConfig *Config

	switch cfg := config.(type) {
	case *Config:
		stackitConfig = cfg
	case map[string]interface{}:
		stackitConfig = &Config{
			ProjectID:           getString(cfg, "project_id"),
			OrgID:               getString(cfg, "org_id"),
			AuthToken:           getString(cfg, "auth_token"),
			ServiceAccountToken: getString(cfg, "service_account_token"),
			ServiceAccountJSON:  getString(cfg, "service_account_json"),
			Region:              getString(cfg, "region"),
			BaseURL:             getString(cfg, "base_url"),
			Timeout:             0,
			MaxRetries:          0,
		}
	default:
		return nil, ErrInvalidConfigTypeForStackitProvider(config)
	}

	// Validate required fields
	if stackitConfig.ProjectID == "" {
		return nil, ErrProjectIDRequiredForStackitProvider
	}

	if stackitConfig.OrgID == "" {
		return nil, ErrOrgIDRequiredForStackitProvider
	}

	// Check for authentication - prefer service_account_json, then service_account_token, then auth_token
	hasServiceAccountJSON := stackitConfig.ServiceAccountJSON != ""
	hasServiceAccountToken := stackitConfig.ServiceAccountToken != ""
	hasAuthToken := stackitConfig.AuthToken != ""

	if !hasServiceAccountJSON && !hasServiceAccountToken && !hasAuthToken {
		return nil, ErrStackitAuthenticationRequired
	}

	return NewClient(stackitConfig)
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
