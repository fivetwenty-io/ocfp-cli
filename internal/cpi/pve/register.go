package pve

import (
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// Register registers the Proxmox provider with the CPI registry.
func Register() error {
	err := cpi.Register("pve", NewProvider)
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

	// Convert generic config to PVE config
	var pveConfig *Config

	switch cfg := config.(type) {
	case *Config:
		pveConfig = cfg
	case map[string]interface{}:
		// Accept both PVE-native keys and the generic provider-config keys
		// emitted by bootstrap's buildProviderConfig (base_url, region, auth_token).
		host := firstNonEmpty(getString(cfg, "host"), getString(cfg, "base_url"), getString(cfg, "api_endpoint"))
		node := firstNonEmpty(getString(cfg, "node"), getString(cfg, "region"))
		tokenID := firstNonEmpty(getString(cfg, "token_id"), getString(cfg, "auth_token"))

		pveConfig = &Config{
			Host:           host,
			Node:           node,
			TokenID:        tokenID,
			TokenSecret:    getString(cfg, "token_secret"),
			Username:       getString(cfg, "username"),
			Password:       getString(cfg, "password"),
			Realm:          getString(cfg, "realm"),
			NetworkMode:    getString(cfg, "network_mode"),
			DefaultBridge:  getString(cfg, "default_bridge"),
			TemplateBridge: getString(cfg, "template_bridge"),
			SDNZone:        getString(cfg, "sdn_zone"),
			DefaultStorage: getString(cfg, "default_storage"),
			ISOStorage:     getString(cfg, "iso_storage"),
			VerifySSL:      getBool(cfg, "verify_ssl"),
			CAPath:         getString(cfg, "ca_path"),
			Timeout:        0,
			MaxRetries:     0,

			BlobstoreMode:      getString(cfg, "blobstore_mode"),
			BlobstoreEndpoint:  getString(cfg, "blobstore_endpoint"),
			BlobstoreRegion:    getString(cfg, "blobstore_region"),
			BlobstoreAccessKey: getString(cfg, "blobstore_access_key"),
			BlobstoreSecretKey: getString(cfg, "blobstore_secret_key"),
			BlobstoreCAPath:    getString(cfg, "blobstore_ca_path"),
			BlobstorePathStyle: getBoolDefault(cfg, "blobstore_path_style", true),
		}
	default:
		return nil, ErrInvalidConfigType(config)
	}

	// Validate required fields
	if pveConfig.Host == "" {
		return nil, ErrHostRequired
	}

	// Check for authentication - prefer API token, then username/password
	hasAPIToken := pveConfig.TokenID != "" && pveConfig.TokenSecret != ""
	hasUserPass := pveConfig.Username != "" && pveConfig.Password != ""

	if !hasAPIToken && !hasUserPass {
		return nil, ErrAPITokenRequired
	}

	return NewClient(pveConfig)
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

// firstNonEmpty returns the first non-empty string in the argument list, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
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

// getBoolDefault returns the bool value at key when present, otherwise the
// supplied default. Used for config keys whose absence should not be confused
// with an explicit false (e.g. blobstore_path_style defaults true).
func getBoolDefault(m map[string]interface{}, key string, def bool) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}

	return def
}
