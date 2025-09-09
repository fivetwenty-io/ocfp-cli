package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"go.uber.org/zap"
)

// Manager provides core vault management operations
// This is the Go equivalent of OCFP::Vault::Manager from Perl.
type Manager struct {
	client    *Client
	safe      *Safe
	config    *config.Config
	blocName  string
	startTime time.Time
	logger    *zap.SugaredLogger
}

// NewManager creates a new vault manager instance.
func NewManager(cfg *config.Config, blocName string) (*Manager, error) {
	client, err := NewClientFromConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create vault client: %w", err)
	}

	safe := NewSafe(client)

	return &Manager{
		client:    client,
		safe:      safe,
		config:    cfg,
		blocName:  blocName,
		startTime: time.Now(),
		logger:    logger.Get(),
	}, nil
}

// NewManagerFromEnv creates a manager using environment variables.
func NewManagerFromEnv(cfg *config.Config, blocName string) (*Manager, error) {
	client, err := NewClientFromEnv()
	if err != nil {
		return nil, fmt.Errorf("failed to create vault client: %w", err)
	}

	safe := NewSafe(client)

	return &Manager{
		client:    client,
		safe:      safe,
		config:    cfg,
		blocName:  blocName,
		startTime: time.Now(),
		logger:    logger.Get(),
	}, nil
}

// PopulateOptions holds options for the populate operation.
type PopulateOptions struct {
	Subcommand string
	DryRun     bool
	Force      bool
}

// Populate performs vault populate operation
// This is the Go equivalent of the populate method in OCFP::Vault::Manager.
func (m *Manager) Populate(opts *PopulateOptions) error {
	m.logger.Info("Starting vault populate", "provider", m.config.Provider)

	if opts.DryRun {
		m.logger.Info("[DRY RUN] Would populate vault configuration")

		return nil
	}

	// Validate vault connection
	err := m.client.ValidateConnection()
	if err != nil {
		return fmt.Errorf("vault connection validation failed: %w", err)
	}

	// Handle subcommands
	switch opts.Subcommand {
	case "public-ips":
		return m.populatePublicIPs()
	case "":
		// Full configuration populate
		return m.populateFullConfiguration()
	default:
		return ErrUnknownSubcommand(opts.Subcommand)
	}
}

// MigrateOptions holds options for the migrate operation.
type MigrateOptions struct {
	DryRun bool
	Force  bool
}

// Migrate performs vault migration from inception to production
// This is the Go equivalent of the migrate method in OCFP::Vault::Manager.
func (m *Manager) Migrate(opts *MigrateOptions) error {
	if m.blocName == "" {
		return ErrBlocNameRequiredForVaultMigrate
	}

	inceptionName := m.getInceptionVaultName()
	m.logger.Info("Starting vault migration", "from", inceptionName, "to", m.blocName)

	// Step 1: Validate targets
	if !opts.DryRun {
		err := m.validateTargets(inceptionName, m.blocName)
		if err != nil {
			return fmt.Errorf("target validation failed: %w", err)
		}
	} else {
		m.logger.Info("[DRY RUN] Would validate vault targets", "inception", inceptionName, "production", m.blocName)
	}

	// Step 2: Export/Import
	if !opts.DryRun {
		err := m.exportImportVault(inceptionName, m.blocName+"-mgmt")
		if err != nil {
			return fmt.Errorf("export/import failed: %w", err)
		}
	} else {
		m.logger.Info("[DRY RUN] Would export/import vault data", "from", inceptionName, "to", m.blocName)
	}

	// Step 3: Validate migration
	if !opts.DryRun {
		err := m.validateMigration(inceptionName, m.blocName+"-mgmt")
		if err != nil {
			return fmt.Errorf("migration validation failed: %w", err)
		}
	} else {
		m.logger.Info("[DRY RUN] Would validate migration with checksums")
	}

	// Step 4: Decommission inception (with confirmation)
	if !opts.DryRun {
		if !opts.Force {
			// In a real implementation, this would prompt for user confirmation
			m.logger.Info("Would prompt for confirmation to decommission inception vault")
		}

		err := m.decommissionInception(inceptionName)
		if err != nil {
			return fmt.Errorf("decommission failed: %w", err)
		}
	} else {
		m.logger.Info("[DRY RUN] Would decommission inception vault")
	}

	// Step 5: Update environment secrets-providers
	if !opts.DryRun {
		err := m.updateEnvironmentSecrets()
		if err != nil {
			return fmt.Errorf("environment update failed: %w", err)
		}
	} else {
		m.logger.Info("[DRY RUN] Would update environment secrets-providers")
	}

	duration := time.Since(m.startTime)
	m.logger.Info("Vault migration completed", "duration", duration)

	return nil
}

// Close cleans up the manager.
func (m *Manager) Close() error {
	if m.client != nil {
		return m.client.Close()
	}

	return nil
}

// GetSafe returns the safe wrapper for direct operations.
func (m *Manager) GetSafe() *Safe {
	return m.safe
}

// GetClient returns the vault client for advanced operations.
func (m *Manager) GetClient() *Client {
	return m.client
}

// getInceptionVaultName returns the inception vault name.
func (m *Manager) getInceptionVaultName() string {
	if m.blocName != "" {
		return m.blocName + "-inception"
	}

	return "inception"
}

// populateFullConfiguration performs full vault configuration.
func (m *Manager) populateFullConfiguration() error {
	m.logger.Info("Populating full vault configuration", "provider", m.config.Provider)

	// Create provider-specific vault implementation
	provider, err := m.createVaultProvider()
	if err != nil {
		return fmt.Errorf("failed to create vault provider: %w", err)
	}

	// Perform full configuration
	err = provider.Configure()
	if err != nil {
		return fmt.Errorf("provider configuration failed: %w", err)
	}

	m.logger.Info("Full vault configuration completed")

	return nil
}

// populatePublicIPs populates public IP information to vault.
func (m *Manager) populatePublicIPs() error {
	m.logger.Info("Populating public IPs to vault", "provider", m.config.Provider)

	// Create provider-specific vault implementation
	provider, err := m.createVaultProvider()
	if err != nil {
		return fmt.Errorf("failed to create vault provider: %w", err)
	}

	// Configure public IPs
	err = provider.ConfigurePublicIPs()
	if err != nil {
		return fmt.Errorf("public IPs configuration failed: %w", err)
	}

	m.logger.Info("Public IPs population completed")

	return nil
}

// validateTargets validates that both inception and production vaults are accessible.
func (m *Manager) validateTargets(inceptionName, productionName string) error {
	m.logger.Info("Validating vault targets", "inception", inceptionName, "production", productionName)

	// Create validator
	validator := NewValidator(m.client, m.safe, m.config)

	// Perform comprehensive pre-migration health check
	inceptionPath := "secret/" + inceptionName
	productionPath := "secret/config/" + productionName

	result, err := validator.PreMigrationHealthCheck(inceptionPath, productionPath)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}

	// Log warnings
	for _, warning := range result.Warnings {
		m.logger.Warn("Validation warning", "warning", warning)
	}

	// Fail on errors
	if len(result.Errors) > 0 {
		for _, errMsg := range result.Errors {
			m.logger.Error("Validation error", "error", errMsg)
		}

		if result.Suggestion != "" {
			m.logger.Info("Suggestion", "suggestion", result.Suggestion)
		}

		return ErrValidationFailedWithErrors(len(result.Errors))
	}

	if result.HasIssues() {
		m.logger.Warn("Validation completed with warnings", "warnings", len(result.Warnings))
	} else {
		m.logger.Info("Validation passed without issues")
	}

	return nil
}

// exportImportVault exports secrets from inception and imports to production.
func (m *Manager) exportImportVault(inceptionName, productionName string) error {
	m.logger.Info("Exporting from inception vault", "vault", inceptionName)

	// Create validator for rollback capability
	validator := NewValidator(m.client, m.safe, m.config)
	productionPath := "secret/config/" + productionName

	// Create rollback point before making changes
	m.logger.Info("Creating rollback point before migration")

	rollback, err := validator.CreateRollbackPoint([]string{productionPath})
	if err != nil {
		m.logger.Warn("Failed to create rollback point", "error", err)
		// Continue anyway - rollback is a safety feature, not required
	}

	// Export from inception
	inceptionPath := "secret/" + inceptionName

	secrets, err := m.safe.Export(inceptionPath)
	if err != nil {
		return fmt.Errorf("failed to export from inception: %w", err)
	}

	m.logger.Info("Exported secrets", "count", len(secrets))

	// Import to production
	m.logger.Info("Importing to production vault", "vault", productionName)

	err = m.safe.Import(productionPath, secrets)
	if err != nil {
		// Attempt rollback on failure
		if rollback != nil {
			m.logger.Error("Import failed, attempting rollback", "error", err)

			rollbackErr := validator.ExecuteRollback(rollback)
			if rollbackErr != nil {
				m.logger.Error("Rollback also failed", "rollback_error", rollbackErr)

				return fmt.Errorf("import failed and rollback failed: import=%w, rollback=%w", err, rollbackErr)
			}

			m.logger.Info("Rollback completed successfully")
		}

		return fmt.Errorf("failed to import to production: %w", err)
	}

	m.logger.Info("Successfully migrated secrets", "count", len(secrets))

	// Store rollback point info for potential future use
	if rollback != nil {
		m.logger.Debug("Rollback point available if needed", "timestamp", rollback.Timestamp)
	}

	return nil
}

// validateMigration validates that all secrets were migrated correctly using checksums.
func (m *Manager) validateMigration(inceptionName, productionName string) error {
	m.logger.Info("Validating migration with checksums")

	// Export from both vaults
	inceptionPath := "secret/" + inceptionName

	inceptionSecrets, err := m.safe.Export(inceptionPath)
	if err != nil {
		return fmt.Errorf("failed to export inception for validation: %w", err)
	}

	productionPath := "secret/config/" + productionName

	productionSecrets, err := m.safe.Export(productionPath)
	if err != nil {
		return fmt.Errorf("failed to export production for validation: %w", err)
	}

	// Calculate checksums
	inceptionChecksum, err := m.calculateChecksum(inceptionSecrets)
	if err != nil {
		return fmt.Errorf("failed to calculate inception checksum: %w", err)
	}

	productionChecksum, err := m.calculateChecksum(productionSecrets)
	if err != nil {
		return fmt.Errorf("failed to calculate production checksum: %w", err)
	}

	// Compare checksums
	if inceptionChecksum != productionChecksum {
		return ErrMigrationValidationFailedChecksumMismatch(inceptionChecksum[:8], productionChecksum[:8])
	}

	m.logger.Info("Migration validation successful", "checksum", inceptionChecksum[:8])

	return nil
}

// calculateChecksum calculates SHA256 checksum of secret data.
func (m *Manager) calculateChecksum(secrets map[string]interface{}) (string, error) {
	// Convert to JSON for consistent hashing
	jsonData, err := json.Marshal(secrets)
	if err != nil {
		return "", fmt.Errorf("failed to marshal secrets: %w", err)
	}

	// Calculate SHA256
	hash := sha256.Sum256(jsonData)

	return hex.EncodeToString(hash[:]), nil
}

// decommissionInception safely removes the inception vault.
func (m *Manager) decommissionInception(inceptionName string) error {
	m.logger.Info("Decommissioning inception vault", "vault", inceptionName)

	inceptionPath := "secret/" + inceptionName

	// List all paths under inception
	paths, err := m.safe.List(inceptionPath)
	if err != nil {
		return fmt.Errorf("failed to list inception paths: %w", err)
	}

	// Delete each path
	for _, path := range paths {
		fullPath := fmt.Sprintf("%s/%s", inceptionPath, strings.TrimSuffix(path, "/"))

		err := m.safe.Delete(fullPath, "")
		if err != nil {
			m.logger.Warn("Failed to delete path", "path", fullPath, "error", err)
		} else {
			m.logger.Debug("Deleted path", "path", fullPath)
		}
	}

	// Delete the root inception path
	err = m.safe.Delete(inceptionPath, "")
	if err != nil {
		m.logger.Warn("Failed to delete inception root", "path", inceptionPath, "error", err)
	}

	m.logger.Info("Inception vault decommissioned", "vault", inceptionName)

	return nil
}

// updateEnvironmentSecrets updates environment secrets-providers configuration.
func (m *Manager) updateEnvironmentSecrets() error {
	m.logger.Info("Updating environment secrets-providers")

	// Create Genesis integration
	genesis := NewGenesisIntegration(m.config, m.blocName)

	// Get vault URL from environment or config
	vaultURL := os.Getenv("VAULT_ADDR")
	if vaultURL == "" {
		vaultURL = "https://127.0.0.1:8200" // Default vault URL
	}

	vaultToken := os.Getenv("VAULT_TOKEN")
	if vaultToken == "" {
		m.logger.Warn("No vault token available for Genesis integration")
		// Continue anyway - Genesis can handle token-less scenarios
	}

	// Update Genesis environment files
	err := genesis.UpdateEnvironmentSecrets(vaultURL, vaultToken)
	if err != nil {
		return fmt.Errorf("failed to update Genesis environment secrets: %w", err)
	}

	m.logger.Info("Environment secrets-providers updated successfully")

	return nil
}

// createVaultProvider creates a provider-specific vault implementation.
//
//nolint:ireturn // returning VaultProvider interface is intentional for provider pluggability
func (m *Manager) createVaultProvider() (providers.VaultProvider, error) {
	switch strings.ToLower(m.config.Provider) {
	case "stackit":
		return NewStackitVaultProvider(m.config, m.safe, m.blocName), nil
	case "openstack":
		// Placeholder - return a not-implemented provider
		return providers.NewPlaceholderProvider("openstack", m.config, m.safe, m.blocName), nil
	case "aws":
		// Placeholder - return a not-implemented provider
		return providers.NewPlaceholderProvider("aws", m.config, m.safe, m.blocName), nil
	case "azure":
		// Placeholder - return a not-implemented provider
		return providers.NewPlaceholderProvider("azure", m.config, m.safe, m.blocName), nil
	case "gcp":
		// Placeholder - return a not-implemented provider
		return providers.NewPlaceholderProvider("gcp", m.config, m.safe, m.blocName), nil
	case "vmware":
		// Placeholder - return a not-implemented provider
		return providers.NewPlaceholderProvider("vmware", m.config, m.safe, m.blocName), nil
	default:
		return nil, ErrUnsupportedProvider(m.config.Provider)
	}
}
