package stackit

import (
    "errors"
    "fmt"

    "github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// Register registers the STACKIT provider with the CPI registry.
func Register() error {
    return cpi.Register("stackit", NewProvider)
}

// NewProvider creates a new STACKIT provider instance.
//nolint:ireturn // returning Provider interface is intentional for registry API
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
		}
	default:
		return nil, fmt.Errorf("invalid config type for STACKIT provider: %T", config)
	}

	// Validate required fields
	if stackitConfig.ProjectID == "" {
		return nil, errors.New("project_id is required for STACKIT provider")
	}

	if stackitConfig.OrgID == "" {
		return nil, errors.New("org_id is required for STACKIT provider")
	}

	// Check for authentication - prefer service_account_json, then service_account_token, then auth_token
	hasServiceAccountJSON := stackitConfig.ServiceAccountJSON != ""
	hasServiceAccountToken := stackitConfig.ServiceAccountToken != ""
	hasAuthToken := stackitConfig.AuthToken != ""

	if !hasServiceAccountJSON && !hasServiceAccountToken && !hasAuthToken {
		return nil, errors.New("STACKIT provider requires either 'service_account_json', 'service_account_token' or 'auth_token' to be set")
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
