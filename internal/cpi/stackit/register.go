package stackit

import (
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

func init() {
	// Register STACKIT provider
	cpi.Register("stackit", NewProvider)
}

// NewProvider creates a new STACKIT provider instance
func NewProvider(config interface{}) (cpi.Provider, error) {
	// Convert generic config to STACKIT config
	var stackitConfig *Config

	switch cfg := config.(type) {
	case *Config:
		stackitConfig = cfg
	case map[string]interface{}:
		stackitConfig = &Config{
			ProjectID: getString(cfg, "project_id"),
			OrgID:     getString(cfg, "org_id"),
			AuthToken: getString(cfg, "auth_token"),
			Region:    getString(cfg, "region"),
			BaseURL:   getString(cfg, "base_url"),
		}
	default:
		return nil, fmt.Errorf("invalid config type for STACKIT provider: %T", config)
	}

	// Validate required fields
	if stackitConfig.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required for STACKIT provider")
	}
	if stackitConfig.OrgID == "" {
		return nil, fmt.Errorf("org_id is required for STACKIT provider")
	}
	if stackitConfig.AuthToken == "" {
		return nil, fmt.Errorf("auth_token is required for STACKIT provider")
	}

	return NewClient(stackitConfig)
}

// getString safely gets a string from a map
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
