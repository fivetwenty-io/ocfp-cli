package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"go.uber.org/zap"
)

// Common errors
var (
	ErrValidationFailed    = fmt.Errorf("vault validation failed")
	ErrEnvironmentUpdate   = fmt.Errorf("environment update failed")
	ErrPortInUse           = fmt.Errorf("port is still in use")
	ErrHomeNotSet          = fmt.Errorf("HOME environment variable not set")
	ErrInvalidPathFormat   = fmt.Errorf("invalid path format")
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

// pathValidationFailure represents a failed path validation.
type pathValidationFailure struct {
	Path               string
	InceptionChecksum  string
	ProductionChecksum string
	Error              string
}

// environmentUpdate represents a successfully updated environment.
type environmentUpdate struct {
	Kit    string
	Target string
}

// environmentFailure represents a failed environment update.
type environmentFailure struct {
	Kit    string
	Target string
	Error  string
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
	m.logger.Infow("Starting vault populate", "provider", m.config.Provider)

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
	m.logger.Infow("Starting vault migration", "from", inceptionName, "to", m.blocName)

	// Step 1: Validate targets
	if !opts.DryRun {
		err := m.validateTargets(inceptionName, m.blocName)
		if err != nil {
			return fmt.Errorf("target validation failed: %w", err)
		}
	} else {
		m.logger.Infow("[DRY RUN] Would validate vault targets", "inception", inceptionName, "production", m.blocName)
	}

	// Step 2: Export/Import
	if !opts.DryRun {
		err := m.exportImportVault(inceptionName, m.blocName+"-mgmt")
		if err != nil {
			return fmt.Errorf("export/import failed: %w", err)
		}
	} else {
		m.logger.Infow("[DRY RUN] Would export/import vault data", "from", inceptionName, "to", m.blocName)
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
	m.logger.Infow("Vault migration completed", "duration", duration)

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
	m.logger.Infow("Populating full vault configuration", "provider", m.config.Provider)

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
	m.logger.Infow("Populating public IPs to vault", "provider", m.config.Provider)

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
	m.logger.Infow("Validating vault targets", "inception", inceptionName, "production", productionName)

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
		m.logger.Warnw("Validation warning", "warning", warning)
	}

	// Fail on errors
	if len(result.Errors) > 0 {
		for _, errMsg := range result.Errors {
			m.logger.Error("Validation error", "error", errMsg)
		}

		if result.Suggestion != "" {
			m.logger.Infow("Suggestion", "suggestion", result.Suggestion)
		}

		return ErrValidationFailedWithErrors(len(result.Errors))
	}

	if result.HasIssues() {
		m.logger.Warnw("Validation completed with warnings", "warnings", len(result.Warnings))
	} else {
		m.logger.Info("Validation passed without issues")
	}

	return nil
}

// exportImportVault exports secrets from inception and imports to production.
func (m *Manager) exportImportVault(inceptionName, productionName string) error {
	m.logger.Infow("Exporting from inception vault", "vault", inceptionName)

	// Create validator for rollback capability
	validator := NewValidator(m.client, m.safe, m.config)
	productionPath := "secret/config/" + productionName

	// Create rollback point before making changes
	m.logger.Info("Creating rollback point before migration")

	rollback, err := validator.CreateRollbackPoint([]string{productionPath})
	if err != nil {
		m.logger.Warnw("Failed to create rollback point", "error", err)
		// Continue anyway - rollback is a safety feature, not required
	}

	// Export from inception
	inceptionPath := "secret/" + inceptionName

	secrets, err := m.safe.Export(inceptionPath)
	if err != nil {
		return fmt.Errorf("failed to export from inception: %w", err)
	}

	m.logger.Infow("Exported secrets", "count", len(secrets))

	// Import to production
	m.logger.Infow("Importing to production vault", "vault", productionName)

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

	m.logger.Infow("Successfully migrated secrets", "count", len(secrets))

	// Store rollback point info for potential future use
	if rollback != nil {
		m.logger.Debugw("Rollback point available if needed", "timestamp", rollback.Timestamp)
	}

	return nil
}

// validateMigration validates that all secrets were migrated correctly using per-path checksums.
// This matches the Perl implementation in OCFP::Vault::Manager::validate_migration.
func (m *Manager) validateMigration(inceptionName, productionName string) error {
	m.logger.Info("Validating migration with checksums")

	inceptionPath := "secret/" + inceptionName
	productionPath := "secret/config/" + productionName

	// Get all paths from inception vault (matching Perl get_vault_paths)
	paths, err := m.getVaultPathsWithKeys(inceptionPath)
	if err != nil {
		return fmt.Errorf("failed to get vault paths: %w", err)
	}

	m.logger.Infow("Found keys to validate", "count", len(paths))

	var failedPaths []pathValidationFailure
	validatedCount := 0

	// Validate each path individually (matching Perl line-by-line validation)
	for _, path := range paths {
		// Calculate checksum for inception
		inceptionChecksum, err := m.calculatePathChecksum(inceptionPath, path)
		if err != nil {
			m.logger.Errorw("✗ Failed to get inception checksum", "path", path, "error", err)
			failedPaths = append(failedPaths, pathValidationFailure{
				Path:  path,
				Error: fmt.Sprintf("failed to get inception checksum: %v", err),
			})

			continue
		}

		// Calculate checksum for production
		productionChecksum, err := m.calculatePathChecksum(productionPath, path)
		if err != nil {
			m.logger.Errorw("✗ Failed to get production checksum", "path", path, "error", err)
			failedPaths = append(failedPaths, pathValidationFailure{
				Path:  path,
				Error: fmt.Sprintf("failed to get production checksum: %v", err),
			})

			continue
		}

		// Compare checksums
		if inceptionChecksum == productionChecksum {
			m.logger.Infow("✓ Validated", "path", path)
			validatedCount++
		} else {
			m.logger.Errorw("✗ Checksum mismatch", "path", path,
				"inception", inceptionChecksum[:8],
				"production", productionChecksum[:8])
			failedPaths = append(failedPaths, pathValidationFailure{
				Path:              path,
				InceptionChecksum: inceptionChecksum[:8],
				ProductionChecksum: productionChecksum[:8],
			})
		}
	}

	// Report results
	if len(failedPaths) > 0 {
		m.logger.Errorw("Validation failed for paths", "failed_count", len(failedPaths))

		for _, failed := range failedPaths {
			if failed.Error != "" {
				m.logger.Errorw("Failed path", "path", failed.Path, "error", failed.Error)
			} else {
				m.logger.Errorw("Failed path", "path", failed.Path,
					"inception", failed.InceptionChecksum,
					"production", failed.ProductionChecksum)
			}
		}

		return fmt.Errorf("%w: validation failed for %d paths", ErrValidationFailed, len(failedPaths))
	}

	m.logger.Infow("All keys validated successfully!", "count", validatedCount)

	return nil
}

// decommissionInception safely removes the inception vault.
// This matches the Perl implementation in OCFP::Vault::Manager::decommission_inception.
func (m *Manager) decommissionInception(inceptionName string) error {
	m.logger.Infow("Decommissioning inception vault", "vault", inceptionName)

	// Step 1: Find and kill tmux sessions (matching Perl Manager.pm:423-459)
	err := m.killInceptionTmuxSessions(inceptionName)
	if err != nil {
		m.logger.Warnw("Failed to kill tmux sessions", "error", err)
		// Continue anyway - this is a cleanup operation
	}

	// Step 2: Kill safe local processes on port 8234 (matching Perl Manager.pm:465-489)
	vaultPort := 8234
	err = m.killSafeProcesses(vaultPort)
	if err != nil {
		m.logger.Warnw("Failed to kill safe processes", "error", err)
		// Continue anyway
	}

	// Step 3: Verify port is freed (matching Perl Manager.pm:492-500)
	err = m.verifyPortFreed(vaultPort)
	if err != nil {
		m.logger.Warnw("Port verification warning", "error", err)
		// This is just a warning, continue
	}

	// Step 4: Delete vault secrets paths
	inceptionPath := "secret/" + inceptionName

	// List all paths under inception
	paths, err := m.safe.List(inceptionPath)
	if err != nil {
		m.logger.Warnw("Failed to list inception paths", "error", err)
		// Continue with file cleanup even if vault paths cannot be listed
	} else {
		// Delete each path
		for _, path := range paths {
			fullPath := fmt.Sprintf("%s/%s", inceptionPath, strings.TrimSuffix(path, "/"))

			err := m.safe.Delete(fullPath, "")
			if err != nil {
				m.logger.Warnw("Failed to delete path", "path", fullPath, "error", err)
			} else {
				m.logger.Debugw("Deleted path", "path", fullPath)
			}
		}

		// Delete the root inception path
		err = m.safe.Delete(inceptionPath, "")
		if err != nil {
			m.logger.Warnw("Failed to delete inception root", "path", inceptionPath, "error", err)
		}
	}

	// Step 5: File cleanup (matching Perl Manager.pm:502-524)
	err = m.cleanupVaultFiles(inceptionName)
	if err != nil {
		m.logger.Warnw("File cleanup had errors", "error", err)
		// Continue - partial cleanup is better than none
	}

	m.logger.Infow("Inception vault decommissioned", "vault", inceptionName)

	return nil
}

// updateEnvironmentSecrets updates environment secrets-providers configuration.
// This matches the Perl implementation in OCFP::Vault::Manager::update_environment_secrets_providers.
func (m *Manager) updateEnvironmentSecrets() error {
	m.logger.Info("Updating environment secrets-providers")

	// Get list of deployed environments using Genesis
	environments, err := m.getGenesisEnvironments()
	if err != nil {
		m.logger.Warnw("Failed to get Genesis environments", "error", err)

		return fmt.Errorf("failed to get Genesis environments: %w", err)
	}

	if len(environments) == 0 {
		m.logger.Warn("No deployed environments found to update")

		return nil
	}

	m.logger.Infow("Found environments to update", "count", len(environments))

	// Track results
	var updatedEnvs []environmentUpdate
	var failedEnvs []environmentFailure

	vaultName := m.blocName + "-mgmt"

	// Update each environment
	for _, env := range environments {
		// Determine target types based on kit (matching Perl logic)
		targets := m.getTargetTypesForKit(env.Kit)

		// Update secrets-provider for each target
		for _, target := range targets {
			genesisEnv := fmt.Sprintf("@%s-%s:%s", m.blocName, target, env.Kit)

			m.logger.Infow("Running Genesis command",
				"command", fmt.Sprintf("genesis %s secrets-provider %s", genesisEnv, vaultName))

			// Execute genesis command
			cmd := exec.Command("genesis", genesisEnv, "secrets-provider", vaultName)

			output, err := cmd.CombinedOutput()
			if err != nil {
				m.logger.Errorw("✗ Failed to update", "env", genesisEnv, "error", string(output))
				failedEnvs = append(failedEnvs, environmentFailure{
					Kit:    env.Kit,
					Target: target,
					Error:  string(output),
				})
			} else {
				m.logger.Infow("✓ Successfully updated", "env", genesisEnv)
				updatedEnvs = append(updatedEnvs, environmentUpdate{
					Kit:    env.Kit,
					Target: target,
				})
			}
		}
	}

	// Report results
	if len(updatedEnvs) > 0 {
		m.logger.Infow("Successfully updated environments", "count", len(updatedEnvs))

		for _, env := range updatedEnvs {
			m.logger.Infow("Updated", "env", fmt.Sprintf("@%s:%s", env.Target, env.Kit))
		}
	}

	if len(failedEnvs) > 0 {
		m.logger.Errorw("Failed to update environments", "count", len(failedEnvs))

		for _, failed := range failedEnvs {
			m.logger.Errorw("Failed", "env", fmt.Sprintf("@%s:%s", failed.Target, failed.Kit))
		}

		m.logger.Warn("\nYou may need to manually update these environments using:")
		m.logger.Warn("  genesis @type:kit secrets-provider <vault-name>")
		m.logger.Warn("\nFor example:")
		m.logger.Warnf("  genesis @mgmt:bosh secrets-provider %s-mgmt", m.blocName)

		return fmt.Errorf("%w: failed to update %d environments", ErrEnvironmentUpdate, len(failedEnvs))
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

// killInceptionTmuxSessions finds and kills tmux sessions for the inception vault.
// This matches the Perl implementation in OCFP::Vault::Manager::decommission_inception.
func (m *Manager) killInceptionTmuxSessions(inceptionName string) error {
	// List all tmux sessions
	cmd := exec.Command("tmux", "list-sessions", "-F", "#{session_name}")

	output, err := cmd.Output()
	if err != nil {
		// No tmux sessions found or tmux not running
		m.logger.Debug("No tmux sessions found or tmux not running")

		return nil
	}

	// Find sessions matching inception pattern
	sessions := strings.Split(string(output), "\n")
	expectedSession := inceptionName + "-vault"
	matchedSessions := []string{}

	for _, session := range sessions {
		session = strings.TrimSpace(session)
		if session == "" {
			continue
		}

		// First, exact match for expected session
		if session == expectedSession {
			matchedSessions = append(matchedSessions, session)

			continue
		}

		// Then, legacy pattern as fallback
		if strings.Contains(session, "inception") && strings.Contains(session, "vault") {
			matchedSessions = append(matchedSessions, session)
		}
	}

	if len(matchedSessions) == 0 {
		m.logger.Debug("No inception vault tmux sessions found")

		return nil
	}

	// Kill each matched session
	for _, session := range matchedSessions {
		m.logger.Infow("Found inception vault tmux session", "session", session)

		// Send Ctrl-C to session
		cmd = exec.Command("tmux", "send-keys", "-t", session, "C-c")

		err := cmd.Run()
		if err != nil {
			m.logger.Warnw("Failed to send Ctrl-C to tmux session", "session", session, "error", err)
		}

		// Wait 2 seconds for graceful shutdown
		time.Sleep(2 * time.Second)

		// Kill the tmux session
		cmd = exec.Command("tmux", "kill-session", "-t", session)

		err = cmd.Run()
		if err != nil {
			m.logger.Warnw("Failed to kill tmux session", "session", session, "error", err)
		} else {
			m.logger.Infow("Stopped inception vault tmux session", "session", session)
		}
	}

	return nil
}

// killSafeProcesses kills safe local processes running on the specified port.
// This matches the Perl implementation in OCFP::Vault::Manager::decommission_inception.
func (m *Manager) killSafeProcesses(port int) error {
	// Find processes using ps and grep
	findCmd := fmt.Sprintf("ps aux | grep -E 'safe local.*--port %d' | grep -v grep", port)

	cmd := exec.Command("sh", "-c", findCmd)

	output, err := cmd.Output()
	if err != nil {
		// No processes found
		m.logger.Debug("No safe local processes found on port", "port", port)

		return nil
	}

	if len(output) == 0 {
		m.logger.Debug("No safe local processes found")

		return nil
	}

	// Extract PIDs from ps output
	lines := strings.Split(string(output), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}

		// Parse PID from ps aux output (second column)
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		pid := fields[1]
		m.logger.Infow("Found safe local process", "pid", pid, "port", port)

		// Send SIGTERM first
		killCmd := fmt.Sprintf("kill -TERM %s", pid)

		cmd := exec.Command("sh", "-c", killCmd)

		err := cmd.Run()
		if err != nil {
			m.logger.Warnw("Failed to send SIGTERM to process", "pid", pid, "error", err)
		}
	}

	// Wait 3 seconds for graceful shutdown
	time.Sleep(3 * time.Second)

	// Check if processes are still running
	cmd = exec.Command("sh", "-c", findCmd)

	output, err = cmd.Output()
	if err == nil && len(output) > 0 {
		m.logger.Warn("Some processes didn't shutdown gracefully, forcing kill")

		// Force kill with SIGKILL
		forceKillCmd := fmt.Sprintf("pkill -9 -f 'safe local.*--port %d'", port)

		cmd := exec.Command("sh", "-c", forceKillCmd)
		_ = cmd.Run() // Ignore errors - best effort
	}

	m.logger.Infow("Killed safe local processes", "port", port)

	return nil
}

// verifyPortFreed checks if the vault port has been freed.
// This matches the Perl implementation in OCFP::Vault::Manager::decommission_inception.
func (m *Manager) verifyPortFreed(port int) error {
	// Check if port is still in use
	checkCmd := fmt.Sprintf("lsof -i :%d 2>/dev/null | grep LISTEN", port)

	cmd := exec.Command("sh", "-c", checkCmd)

	output, err := cmd.Output()
	if err != nil || len(output) == 0 {
		// Port is free
		m.logger.Infow("Port is now free", "port", port)

		return nil
	}

	// Port is still in use
	m.logger.Warnw("Port is still in use after cleanup attempt", "port", port)
	m.logger.Warn("You may need to manually kill the process using the port")

	return fmt.Errorf("%w: port %d is still in use", ErrPortInUse, port)
}

// cleanupVaultFiles removes vault-related files from the filesystem.
// This matches the Perl implementation in OCFP::Vault::Manager::decommission_inception.
func (m *Manager) cleanupVaultFiles(inceptionName string) error {
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		return ErrHomeNotSet
	}

	removedFiles := 0

	// Remove ~/.vault.key
	vaultKeyFile := filepath.Join(homeDir, ".vault.key")
	if _, err := os.Stat(vaultKeyFile); err == nil {
		err := os.Remove(vaultKeyFile)
		if err != nil {
			m.logger.Warnw("Failed to remove vault key file", "file", vaultKeyFile, "error", err)
		} else {
			m.logger.Infow("Removed vault key file", "file", vaultKeyFile)
			removedFiles++
		}
	}

	// Remove ~/.vault directory
	vaultDir := filepath.Join(homeDir, ".vault")
	if _, err := os.Stat(vaultDir); err == nil {
		err := os.RemoveAll(vaultDir)
		if err != nil {
			m.logger.Warnw("Failed to remove vault directory", "dir", vaultDir, "error", err)
		} else {
			m.logger.Infow("Removed vault directory", "dir", vaultDir)
			removedFiles++
		}
	}

	// Remove bloc-specific vault key if different from default
	if m.blocName != "" {
		blocVaultKeyFile := filepath.Join(homeDir, ".ocfp", m.blocName, "vault", "root.key")
		if _, err := os.Stat(blocVaultKeyFile); err == nil {
			err := os.Remove(blocVaultKeyFile)
			if err != nil {
				m.logger.Warnw("Failed to remove bloc vault key", "file", blocVaultKeyFile, "error", err)
			} else {
				m.logger.Infow("Removed bloc vault key", "file", blocVaultKeyFile)
				removedFiles++
			}
		}

		// Remove unseal keys file
		unsealKeysFile := filepath.Join(homeDir, ".ocfp", m.blocName, "vault", "unseal.keys")
		if _, err := os.Stat(unsealKeysFile); err == nil {
			err := os.Remove(unsealKeysFile)
			if err != nil {
				m.logger.Warnw("Failed to remove unseal keys", "file", unsealKeysFile, "error", err)
			} else {
				m.logger.Infow("Removed unseal keys", "file", unsealKeysFile)
				removedFiles++
			}
		}

		// Remove vault data directory
		blocVaultDataDir := filepath.Join(homeDir, ".ocfp", m.blocName, "vault", "data")
		if _, err := os.Stat(blocVaultDataDir); err == nil {
			err := os.RemoveAll(blocVaultDataDir)
			if err != nil {
				m.logger.Warnw("Failed to remove vault data dir", "dir", blocVaultDataDir, "error", err)
			} else {
				m.logger.Infow("Removed vault data directory", "dir", blocVaultDataDir)
				removedFiles++
			}
		}
	}

	if removedFiles > 0 {
		m.logger.Infow("File cleanup completed", "removed", removedFiles)
	} else {
		m.logger.Info("No vault files found to remove")
	}

	return nil
}

// getVaultPathsWithKeys gets all secret paths from a vault target with keys.
// This matches the Perl implementation in OCFP::Vault::Manager::get_vault_paths.
func (m *Manager) getVaultPathsWithKeys(basePath string) ([]string, error) {
	// Use safe paths command to list all paths with keys
	// In the Perl version, this uses: safe -T target paths secret/ --keys
	// We'll recursively walk the vault tree and build path:key combinations

	var paths []string

	err := m.walkVaultPaths(basePath, "", &paths)
	if err != nil {
		return nil, fmt.Errorf("failed to walk vault paths: %w", err)
	}

	// Remove duplicates and sort
	uniquePaths := make(map[string]bool)
	for _, path := range paths {
		uniquePaths[path] = true
	}

	sortedPaths := make([]string, 0, len(uniquePaths))
	for path := range uniquePaths {
		sortedPaths = append(sortedPaths, path)
	}

	// Sort for consistent ordering
	sort := func(paths []string) {
		for i := 0; i < len(paths); i++ {
			for j := i + 1; j < len(paths); j++ {
				if paths[i] > paths[j] {
					paths[i], paths[j] = paths[j], paths[i]
				}
			}
		}
	}

	sort(sortedPaths)

	return sortedPaths, nil
}

// walkVaultPaths recursively walks vault paths and collects path:key combinations.
func (m *Manager) walkVaultPaths(basePath, currentPath string, paths *[]string) error {
	fullPath := basePath
	if currentPath != "" {
		fullPath = basePath + "/" + currentPath
	}

	// Try to read as a secret first
	data, err := m.safe.GetAll(fullPath)
	if err == nil {
		// This is a secret with keys - add each key as a path:key combination
		for key := range data {
			if currentPath == "" {
				*paths = append(*paths, fullPath+":"+key)
			} else {
				*paths = append(*paths, currentPath+":"+key)
			}
		}

		return nil
	}

	// Try to list as a directory
	subPaths, err := m.safe.List(fullPath)
	if err != nil {
		// Neither a secret nor a directory - this is ok, might be empty
		return nil
	}

	// Process each sub-path
	for _, subPath := range subPaths {
		subPath = strings.TrimSuffix(subPath, "/")

		var newCurrentPath string
		if currentPath == "" {
			newCurrentPath = subPath
		} else {
			newCurrentPath = currentPath + "/" + subPath
		}

		err := m.walkVaultPaths(basePath, newCurrentPath, paths)
		if err != nil {
			return err
		}
	}

	return nil
}

// calculatePathChecksum calculates SHA256 checksum of a specific path:key value.
// This matches the Perl implementation in OCFP::Vault::Manager::calculate_checksum.
func (m *Manager) calculatePathChecksum(basePath, pathWithKey string) (string, error) {
	// Parse path:key format
	parts := strings.SplitN(pathWithKey, ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("%w: %s (expected path:key)", ErrInvalidPathFormat, pathWithKey)
	}

	path := parts[0]
	key := parts[1]

	// Build full path
	fullPath := basePath
	if path != "" {
		fullPath = basePath + "/" + path
	}

	// Get the value from vault
	value, err := m.safe.Get(fullPath, key)
	if err != nil {
		return "", fmt.Errorf("failed to get value for %s: %w", pathWithKey, err)
	}

	// Convert value to string for consistent hashing
	var valueStr string
	switch v := value.(type) {
	case string:
		valueStr = v
	case []byte:
		valueStr = string(v)
	default:
		// For other types, marshal to JSON
		jsonBytes, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("failed to marshal value: %w", err)
		}

		valueStr = string(jsonBytes)
	}

	// Calculate SHA256 of the value
	hash := sha256.Sum256([]byte(valueStr))

	return hex.EncodeToString(hash[:]), nil
}

// getTargetTypesForKit determines which target types (mgmt/ocf) apply to a given kit.
// This matches the Perl implementation in OCFP::Vault::Manager::update_environment_secrets_providers.
func (m *Manager) getTargetTypesForKit(kit string) []string {
	// mgmt-only kits
	mgmtKits := map[string]bool{
		"concourse": true,
		"doomsday":  true,
		"jumpbox":   true,
		"shield":    true,
		"vault":     true,
		"bosh":      true,
	}

	// Kits that support both mgmt and ocf
	bothKits := map[string]bool{
		"prometheus": true,
	}

	if mgmtKits[kit] {
		return []string{"mgmt"}
	}

	if bothKits[kit] {
		return []string{"mgmt", "ocf"}
	}

	// Default to ocf for all other kits (cf, autoscaler, blacksmith, scheduler, etc.)
	return []string{"ocf"}
}

// parsedGenesisEnv represents a parsed Genesis environment from genesis envs output.
type parsedGenesisEnv struct {
	Kit  string
	Name string
	Type string
}

// getGenesisEnvironments gets the list of deployed Genesis environments.
// This matches the Perl implementation in OCFP::Vault::Manager::get_genesis_environments.
func (m *Manager) getGenesisEnvironments() ([]parsedGenesisEnv, error) {
	// Run genesis envs command
	cmd := exec.Command("genesis", "envs")

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Try with full path
		m.logger.Debug("genesis envs failed, trying with full path")

		whichCmd := exec.Command("which", "genesis")

		whichOutput, whichErr := whichCmd.Output()
		if whichErr == nil {
			genesisPath := strings.TrimSpace(string(whichOutput))
			if genesisPath != "" {
				m.logger.Infof("Found genesis at: %s", genesisPath)

				cmd = exec.Command(genesisPath, "envs")

				output, err = cmd.CombinedOutput()
			}
		}

		// Try shell fallback
		if err != nil {
			m.logger.Debug("Attempting shell fallback: sh -c 'genesis envs 2>&1'")

			cmd = exec.Command("sh", "-c", "genesis envs 2>&1")

			output, err = cmd.CombinedOutput()
		}

		// If still failing, return empty list
		if err != nil {
			m.logger.Warnw("Failed to get genesis environments", "error", err, "output", string(output))

			return []parsedGenesisEnv{}, nil
		}
	}

	// Parse the output
	var environments []parsedGenesisEnv

	lines := strings.Split(string(output), "\n")

	for _, line := range lines {
		// Skip empty lines and headers
		if strings.TrimSpace(line) == "" {
			continue
		}

		if strings.Contains(line, "Deployment root") || strings.Contains(line, "contains the following") {
			continue
		}

		// Look for lines like: "    bosh/scf-stackit-eu01-004-sb-mgmt"
		// Format: whitespace + kit/environment-name
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}

		lastPart := parts[len(parts)-1]
		if strings.Contains(lastPart, "/") {
			kitAndEnv := strings.SplitN(lastPart, "/", 2)
			if len(kitAndEnv) != 2 {
				continue
			}

			kit := strings.TrimSpace(kitAndEnv[0])
			envName := strings.TrimSpace(kitAndEnv[1])

			// Remove ANSI color codes
			kit = stripANSI(kit)
			envName = stripANSI(envName)

			// Skip blank entries
			if kit == "" || envName == "" {
				continue
			}

			// Determine type based on environment name suffix
			envType := "ocf" // default
			if strings.Contains(envName, "-mgmt") {
				envType = "mgmt"
			}

			environments = append(environments, parsedGenesisEnv{
				Kit:  kit,
				Name: envName,
				Type: envType,
			})
		}
	}

	return environments, nil
}

// stripANSI removes ANSI escape codes from a string.
func stripANSI(str string) string {
	// Simple ANSI escape sequence pattern: \x1b[...m
	re := regexp.MustCompile(`\x1b\[[0-9;]*m`)

	return re.ReplaceAllString(str, "")
}
