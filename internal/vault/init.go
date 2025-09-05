package vault

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/vault/api"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"go.uber.org/zap"
)

// InitManager handles vault initialization and unsealing
type InitManager struct {
	client *Client
	config *config.Config
	logger *zap.SugaredLogger
}

// NewInitManager creates a new vault init manager
func NewInitManager(client *Client, cfg *config.Config) *InitManager {
	return &InitManager{
		client: client,
		config: cfg,
		logger: logger.Get(),
	}
}

// InitRequest holds parameters for vault initialization
type InitRequest struct {
	SecretShares      int
	SecretThreshold   int
	RecoveryShares    int
	RecoveryThreshold int
	StoredShares      int
	RootTokenPGPKey   string
	PGPKeys           []string
}

// InitResponse holds the result of vault initialization
type InitResponse struct {
	Keys            []string
	KeysBase64      []string
	RecoveryKeys    []string
	RecoveryKeysB64 []string
	RootToken       string
	Initialized     bool
}

// DefaultInitRequest returns sensible defaults for vault initialization
func DefaultInitRequest() *InitRequest {
	return &InitRequest{
		SecretShares:      5,
		SecretThreshold:   3,
		RecoveryShares:    0,
		RecoveryThreshold: 0,
		StoredShares:      0,
	}
}

// InitializeVault initializes a new vault instance
func (im *InitManager) InitializeVault(req *InitRequest) (*InitResponse, error) {
	im.logger.Info("Initializing vault", "shares", req.SecretShares, "threshold", req.SecretThreshold)

	// Check if vault is already initialized
	initialized, err := im.IsInitialized()
	if err != nil {
		return nil, fmt.Errorf("failed to check initialization status: %w", err)
	}

	if initialized {
		im.logger.Info("Vault is already initialized")
		return &InitResponse{Initialized: true}, nil
	}

	// Prepare init request
	initReq := &api.InitRequest{
		SecretShares:      req.SecretShares,
		SecretThreshold:   req.SecretThreshold,
		RecoveryShares:    req.RecoveryShares,
		RecoveryThreshold: req.RecoveryThreshold,
		StoredShares:      req.StoredShares,
		RootTokenPGPKey:   req.RootTokenPGPKey,
		PGPKeys:           req.PGPKeys,
	}

	// Initialize vault
	resp, err := im.client.client.Sys().Init(initReq)
	if err != nil {
		return nil, fmt.Errorf("vault initialization failed: %w", err)
	}

	result := &InitResponse{
		Keys:            resp.Keys,
		KeysBase64:      resp.KeysB64,
		RecoveryKeys:    resp.RecoveryKeys,
		RecoveryKeysB64: resp.RecoveryKeysB64,
		RootToken:       resp.RootToken,
		Initialized:     true,
	}

	im.logger.Info("Vault initialization completed successfully")
	return result, nil
}

// IsInitialized checks if vault is initialized
func (im *InitManager) IsInitialized() (bool, error) {
	status, err := im.client.client.Sys().InitStatus()
	if err != nil {
		return false, fmt.Errorf("failed to get init status: %w", err)
	}

	return status, nil
}

// GetSealStatus gets the current seal status
func (im *InitManager) GetSealStatus() (*api.SealStatusResponse, error) {
	status, err := im.client.client.Sys().SealStatus()
	if err != nil {
		return nil, fmt.Errorf("failed to get seal status: %w", err)
	}

	return status, nil
}

// IsSealed checks if vault is sealed
func (im *InitManager) IsSealed() (bool, error) {
	status, err := im.GetSealStatus()
	if err != nil {
		return false, err
	}

	return status.Sealed, nil
}

// UnsealVault unseals vault using the provided keys
func (im *InitManager) UnsealVault(keys []string) error {
	im.logger.Info("Starting vault unseal process", "keys_provided", len(keys))

	// Check current seal status
	status, err := im.GetSealStatus()
	if err != nil {
		return fmt.Errorf("failed to get seal status: %w", err)
	}

	if !status.Sealed {
		im.logger.Info("Vault is already unsealed")
		return nil
	}

	im.logger.Info("Vault seal status", "sealed", status.Sealed, "threshold", status.T, "shares", status.N, "progress", status.Progress)

	// Unseal with provided keys
	for keyIndex, key := range keys {
		im.logger.Debug("Providing unseal key", "key_number", keyIndex+1, "total", len(keys))

		unsealResp, err := im.client.client.Sys().Unseal(key)
		if err != nil {
			return fmt.Errorf("failed to provide unseal key %d: %w", keyIndex+1, err)
		}

		im.logger.Debug("Unseal progress", "progress", unsealResp.Progress, "threshold", unsealResp.T)

		if !unsealResp.Sealed {
			im.logger.Info("Vault successfully unsealed", "keys_used", keyIndex+1)
			return nil
		}
	}

	// Check final status
	finalStatus, err := im.GetSealStatus()
	if err != nil {
		return fmt.Errorf("failed to get final seal status: %w", err)
	}

	if finalStatus.Sealed {
		return fmt.Errorf("vault still sealed after providing %d keys (needed %d out of %d)",
			len(keys), finalStatus.T, finalStatus.N)
	}

	im.logger.Info("Vault unseal completed successfully")
	return nil
}

// SealVault seals the vault (requires root token)
func (im *InitManager) SealVault() error {
	im.logger.Info("Sealing vault")

	if err := im.client.client.Sys().Seal(); err != nil {
		return fmt.Errorf("failed to seal vault: %w", err)
	}

	im.logger.Info("Vault sealed successfully")
	return nil
}

// AutoUnsealFromEnv attempts to unseal vault using keys from environment
func (im *InitManager) AutoUnsealFromEnv() error {
	im.logger.Info("Attempting auto-unseal from environment variables")

	// Check if already unsealed
	sealed, err := im.IsSealed()
	if err != nil {
		return fmt.Errorf("failed to check seal status: %w", err)
	}

	if !sealed {
		im.logger.Info("Vault is already unsealed")
		return nil
	}

	// Look for unseal keys in environment variables
	var keys []string
	for keyNumber := 1; keyNumber <= 10; keyNumber++ { // Check up to 10 keys
		keyName := fmt.Sprintf("VAULT_UNSEAL_KEY_%d", keyNumber)
		if key := getEnvOrDefault(keyName, ""); key != "" {
			keys = append(keys, key)
		}
	}

	// Also check for a single key or comma-separated keys
	if singleKey := getEnvOrDefault("VAULT_UNSEAL_KEY", ""); singleKey != "" {
		// Split by comma in case multiple keys are provided
		for _, key := range strings.Split(singleKey, ",") {
			key = strings.TrimSpace(key)
			if key != "" {
				keys = append(keys, key)
			}
		}
	}

	if len(keys) == 0 {
		return fmt.Errorf("no unseal keys found in environment variables")
	}

	im.logger.Info("Found unseal keys in environment", "count", len(keys))

	return im.UnsealVault(keys)
}

// WaitForVaultReady waits for vault to be ready and unsealed
func (im *InitManager) WaitForVaultReady(timeout time.Duration) error {
	im.logger.Info("Waiting for vault to be ready", "timeout", timeout)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for vault to be ready")
		case <-ticker.C:
			// Check if vault is initialized and unsealed
			initialized, err := im.IsInitialized()
			if err != nil {
				im.logger.Debug("Vault not accessible yet", "error", err)
				continue
			}

			if !initialized {
				im.logger.Debug("Vault not initialized yet")
				continue
			}

			sealed, err := im.IsSealed()
			if err != nil {
				im.logger.Debug("Cannot check seal status", "error", err)
				continue
			}

			if sealed {
				im.logger.Debug("Vault is sealed")
				continue
			}

			// Test authentication
			if err := im.client.ValidateConnection(); err != nil {
				im.logger.Debug("Vault authentication not ready", "error", err)
				continue
			}

			im.logger.Info("Vault is ready and accessible")
			return nil
		}
	}
}

// InitializeAndUnseal performs complete vault initialization and unsealing
func (im *InitManager) InitializeAndUnseal(req *InitRequest) (*InitResponse, error) {
	im.logger.Info("Starting complete vault initialization and unseal process")

	// Initialize vault
	resp, err := im.InitializeVault(req)
	if err != nil {
		return nil, fmt.Errorf("initialization failed: %w", err)
	}

	// If vault was already initialized, just return
	if len(resp.Keys) == 0 {
		im.logger.Info("Vault was already initialized, skipping unseal")
		return resp, nil
	}

	// Set the root token for unsealing
	if resp.RootToken != "" {
		im.client.client.SetToken(resp.RootToken)
		im.logger.Debug("Root token set for unsealing")
	}

	// Unseal using the generated keys
	if len(resp.Keys) > 0 {
		// Use minimum required keys for unsealing
		keysToUse := resp.Keys
		if len(keysToUse) > req.SecretThreshold {
			keysToUse = keysToUse[:req.SecretThreshold]
		}

		if err := im.UnsealVault(keysToUse); err != nil {
			im.logger.Error("Unseal failed after initialization", "error", err)
			return resp, fmt.Errorf("unseal failed: %w", err)
		}
	}

	im.logger.Info("Vault initialization and unseal completed successfully")
	return resp, nil
}

// GetHealth gets vault health status
func (im *InitManager) GetHealth() (*api.HealthResponse, error) {
	health, err := im.client.client.Sys().Health()
	if err != nil {
		return nil, fmt.Errorf("failed to get health status: %w", err)
	}

	return health, nil
}

// IsHealthy checks if vault is healthy
func (im *InitManager) IsHealthy() (bool, error) {
	health, err := im.GetHealth()
	if err != nil {
		return false, err
	}

	// Vault is healthy if initialized, not sealed, and not in standby
	return health.Initialized && !health.Sealed, nil
}
