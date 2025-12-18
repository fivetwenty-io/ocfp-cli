package proxmox

import (
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// Register registers the Proxmox provider with the CPI registry.
func Register() error {
	err := cpi.Register("proxmox", NewProvider)
	if err != nil {
		return fmt.Errorf("failed to register Proxmox provider: %w", err)
	}

	return nil
}

// NewProvider creates a new Proxmox provider instance.
//
//nolint:ireturn // Returns interface by design for provider abstraction
func NewProvider(config interface{}) (cpi.Provider, error) {
	// If config is nil, return an uninitialized client that will be configured later via Initialize
	if config == nil {
		return NewClient(nil)
	}

	// Convert generic config to Proxmox config
	var proxmoxConfig *Config

	switch cfg := config.(type) {
	case *Config:
		proxmoxConfig = cfg
	case map[string]interface{}:
		proxmoxConfig = &Config{
			Host:           getString(cfg, "host"),
			Node:           getString(cfg, "node"),
			TokenID:        getString(cfg, "token_id"),
			TokenSecret:    getString(cfg, "token_secret"),
			Username:       getString(cfg, "username"),
			Password:       getString(cfg, "password"),
			Realm:          getString(cfg, "realm"),
			NetworkMode:    getString(cfg, "network_mode"),
			DefaultBridge:  getString(cfg, "default_bridge"),
			SDNZone:        getString(cfg, "sdn_zone"),
			DefaultStorage: getString(cfg, "default_storage"),
			ISOStorage:     getString(cfg, "iso_storage"),
			VerifySSL:      getBool(cfg, "verify_ssl"),
			CAPath:         getString(cfg, "ca_path"),
			Timeout:        0,
			MaxRetries:     0,
		}
	default:
		return nil, ErrInvalidConfigType(config)
	}

	// Validate required fields
	if proxmoxConfig.Host == "" {
		return nil, ErrHostRequired
	}

	// Check for authentication - prefer API token, then username/password
	hasAPIToken := proxmoxConfig.TokenID != "" && proxmoxConfig.TokenSecret != ""
	hasUserPass := proxmoxConfig.Username != "" && proxmoxConfig.Password != ""

	if !hasAPIToken && !hasUserPass {
		return nil, ErrAPITokenRequired
	}

	return NewClient(proxmoxConfig)
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
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}

	return 0
}
