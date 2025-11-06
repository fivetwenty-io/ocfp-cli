package vault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"gopkg.in/yaml.v3"
)

// Vault operation timing constants.
const (
	gracefulShutdownWaitSeconds   = 2 // Seconds to wait for graceful shutdown after Ctrl-C
	processTerminationWaitSeconds = 3 // Seconds to wait after SIGTERM before force kill
	pathKeyDelimiterParts         = 2 // Expected parts when splitting path:key format
	minPsAuxFields                = 2 // Minimum fields in ps aux output to extract PID
)

// Common errors.
var (
	ErrValidationFailed  = errors.New("vault validation failed")
	ErrEnvironmentUpdate = errors.New("environment update failed")
	ErrPortInUse         = errors.New("port is still in use")
	ErrHomeNotSet        = errors.New("HOME environment variable not set")
	ErrInvalidPathFormat = errors.New("invalid path format")
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
	Subcommand       string
	DryRun           bool
	Force            bool
	ProgressReporter ProgressReporter
}

// ProgressReporter defines the interface for progress reporting during vault operations.
type ProgressReporter interface {
	ReportPhaseStart(phase string, index, total int)
	ReportPhaseComplete(phase string, duration time.Duration)
	ReportSubtaskProgress(phase string, current, total int, label string)
	ReportError(phase string, err error, attempt, maxAttempts int)
	ReportFinalSummary(success bool, duration time.Duration, phases int, errors int)
}

// Populate performs vault populate operation
// This is the Go equivalent of the populate method in OCFP::Vault::Manager.
func (m *Manager) Populate(opts *PopulateOptions) error {
	m.logger.Infow("Starting vault populate", "provider", m.config.Provider)

	if opts.DryRun {
		m.logger.Info("[DRY RUN] Would populate vault configuration")
		// For dry-run, still report basic progress
		if opts.ProgressReporter != nil {
			opts.ProgressReporter.ReportPhaseStart("dry-run", 0, 1)
			opts.ProgressReporter.ReportPhaseComplete("dry-run", 0)
			opts.ProgressReporter.ReportFinalSummary(true, 0, 1, 0)
		}
		return nil
	}

	// Validate vault connection
	err := m.client.ValidateConnection()
	if err != nil {
		return fmt.Errorf("vault connection validation failed: %w", err)
	}

	// Handle subcommands
	var populateErr error
	switch opts.Subcommand {
	case "public-ips":
		populateErr = m.populatePublicIPs(opts.ProgressReporter)
	case "":
		// Full configuration populate (provider reports all phases)
		populateErr = m.populateFullConfiguration(opts.ProgressReporter)
	default:
		return ErrUnknownSubcommand(opts.Subcommand)
	}

	if populateErr != nil {
		return populateErr
	}

	return nil
}

// MigrateOptions holds options for the migrate operation.
type MigrateOptions struct {
	DryRun bool
	Force  bool
}

// Migrate performs vault migration from inception to production
// This is the Go equivalent of the migrate method in OCFP::Vault::Manager.
func (m *Manager) Migrate(opts *MigrateOptions) error {
	inceptionName := m.getInceptionVaultName()

	// Get production vault name from .saferc current target
	productionName, err := m.getProductionVaultName()
	if err != nil {
		return fmt.Errorf("failed to determine production vault: %w", err)
	}

	m.logger.Infow("Starting vault migration", "from", inceptionName, "to", productionName)

	// Step 1: Validate targets
	if !opts.DryRun {
		err := m.validateTargets(inceptionName, productionName)
		if err != nil {
			return fmt.Errorf("target validation failed: %w", err)
		}
	} else {
		m.logger.Infow("[DRY RUN] Would validate vault targets", "inception", inceptionName, "production", productionName)
	}

	// Step 2: Create snapshot of inception vault for safety
	var snapshotPath string
	if !opts.DryRun {
		m.logger.Info("Creating safety snapshot of inception vault...")
		snapshotPath, err = m.snapshotInceptionVault(inceptionName)
		if err != nil {
			return fmt.Errorf("snapshot creation failed: %w", err)
		}
		m.logger.Infow("✓ Snapshot created successfully", "path", snapshotPath)
	} else {
		m.logger.Info("[DRY RUN] Would create snapshot of inception vault")
	}

	// Step 3: Export/Import
	var secretCount int
	if !opts.DryRun {
		secretCount, err = m.exportImportVault(inceptionName, productionName)
		if err != nil {
			return fmt.Errorf("export/import failed: %w", err)
		}
		// CRITICAL: Ensure we actually exported something
		if secretCount == 0 {
			return fmt.Errorf("CRITICAL: export returned 0 secrets - refusing to proceed (snapshot saved at: %s)", snapshotPath)
		}
		m.logger.Infow("✓ Export/Import completed", "secrets", secretCount)
	} else {
		m.logger.Infow("[DRY RUN] Would export/import vault data", "from", inceptionName, "to", productionName)
	}

	// Step 4: Validate migration with checksums
	var validatedCount int
	if !opts.DryRun {
		m.logger.Info("\nValidating migration with SHA256 checksums...")
		validatedCount, err = m.validateMigration(inceptionName, productionName)
		if err != nil {
			return fmt.Errorf("migration validation failed: %w (snapshot available at: %s)", err, snapshotPath)
		}
		// CRITICAL: Ensure validation actually validated something
		if validatedCount == 0 {
			return fmt.Errorf("CRITICAL: validation checked 0 secrets - refusing to proceed (snapshot saved at: %s)", snapshotPath)
		}
		// CRITICAL: Ensure validation count matches export count
		if validatedCount != secretCount {
			return fmt.Errorf("CRITICAL: validation mismatch - exported %d secrets but validated %d (snapshot saved at: %s)",
				secretCount, validatedCount, snapshotPath)
		}
		m.logger.Infow("✓ All secrets validated successfully", "validated", validatedCount)
	} else {
		m.logger.Info("[DRY RUN] Would validate migration with checksums")
	}

	// Step 5: Confirmation prompt before decommission
	if !opts.DryRun {
		if !opts.Force {
			m.logger.Warn("\n⚠️  CRITICAL OPERATION: About to decommission inception vault")
			m.logger.Infow("Migration Summary",
				"exported", secretCount,
				"validated", validatedCount,
				"snapshot", snapshotPath)

			confirmed, err := m.confirmDecommission()
			if err != nil {
				return fmt.Errorf("confirmation failed: %w", err)
			}
			if !confirmed {
				m.logger.Info("Decommission cancelled by user - inception vault preserved")
				m.logger.Infow("Migration completed but inception vault NOT decommissioned", "snapshot", snapshotPath)
				return nil
			}
		}

		// ONLY NOW is it safe to decommission
		err := m.decommissionInception(inceptionName)
		if err != nil {
			return fmt.Errorf("decommission failed: %w", err)
		}
		m.logger.Infow("✓ Inception vault decommissioned", "vault", inceptionName)
	} else {
		m.logger.Info("[DRY RUN] Would prompt for confirmation to decommission inception vault")
	}

	// Step 6: Update environment secrets-providers
	if !opts.DryRun {
		err := m.updateEnvironmentSecrets()
		if err != nil {
			return fmt.Errorf("environment update failed: %w", err)
		}
	} else {
		m.logger.Info("[DRY RUN] Would update environment secrets-providers")
	}

	duration := time.Since(m.startTime)
	m.logger.Infow("Vault migration completed successfully", "duration", duration)
	if snapshotPath != "" {
		m.logger.Infow("Snapshot can be safely deleted", "path", snapshotPath)
	}

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

// getInceptionVaultName returns the inception vault name with fallback logic.
// Tries bloc-specific name first, then falls back to simple "inception".
func (m *Manager) getInceptionVaultName() string {
	// Try bloc-specific name first if we have a bloc
	if m.blocName != "" {
		blocInception := m.blocName + "-inception"
		// Check if this target exists in .saferc
		if m.targetExistsInSaferc(blocInception) {
			return blocInception
		}
	}

	// Fall back to simple "inception" name
	return "inception"
}

// getProductionVaultName returns the production vault name by reading current target from .saferc.
func (m *Manager) getProductionVaultName() (string, error) {
	// Read current target from .saferc
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		return "", ErrHomeNotSet
	}

	saferc := filepath.Join(homeDir, ".saferc")

	data, err := os.ReadFile(saferc)
	if err != nil {
		return "", fmt.Errorf("failed to read .saferc: %w", err)
	}

	// Parse .saferc YAML
	var config struct {
		Current string `yaml:"current"`
	}

	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return "", fmt.Errorf("failed to parse .saferc: %w", err)
	}

	if config.Current == "" {
		// Fall back to bloc-mgmt if no current target set
		if m.blocName != "" {
			return m.blocName + "-mgmt", nil
		}
		return "", fmt.Errorf("no current vault target set in .saferc")
	}

	return config.Current, nil
}

// targetExistsInSaferc checks if a vault target exists in .saferc.
func (m *Manager) targetExistsInSaferc(targetName string) bool {
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		return false
	}

	saferc := filepath.Join(homeDir, ".saferc")

	data, err := os.ReadFile(saferc)
	if err != nil {
		return false
	}

	// Parse .saferc YAML
	var config struct {
		Vaults map[string]interface{} `yaml:"vaults"`
	}

	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return false
	}

	_, exists := config.Vaults[targetName]
	return exists
}

// populateFullConfiguration performs full vault configuration.
func (m *Manager) populateFullConfiguration(reporter ProgressReporter) error {
	m.logger.Infow("Populating full vault configuration", "provider", m.config.Provider)

	// Create provider-specific vault implementation
	provider, err := m.createVaultProvider()
	if err != nil {
		return fmt.Errorf("failed to create vault provider: %w", err)
	}

	// Perform full configuration (provider reports all phases)
	err = provider.Configure(reporter)
	if err != nil {
		return fmt.Errorf("provider configuration failed: %w", err)
	}

	m.logger.Info("Full vault configuration completed")

	return nil
}

// populatePublicIPs populates public IP information to vault.
func (m *Manager) populatePublicIPs(reporter ProgressReporter) error {
	m.logger.Infow("Populating public IPs to vault", "provider", m.config.Provider)

	// Create provider-specific vault implementation
	provider, err := m.createVaultProvider()
	if err != nil {
		return fmt.Errorf("failed to create vault provider: %w", err)
	}

	// Configure public IPs (provider reports phase progress)
	err = provider.ConfigurePublicIPs(reporter, 1, 1)
	if err != nil {
		return fmt.Errorf("public IPs configuration failed: %w", err)
	}

	m.logger.Info("Public IPs population completed")

	return nil
}

// validateTargets validates that both inception and production vault instances are accessible.
// This checks TWO DIFFERENT VAULT INSTANCES can be reached.
func (m *Manager) validateTargets(inceptionName, productionName string) error {
	m.logger.Infow("Validating vault targets", "inception", inceptionName, "production", productionName)

	// Validate inception vault target
	m.logger.Infow("Checking inception vault target", "target", inceptionName)

	inceptionClient, err := m.createClientForTarget(inceptionName)
	if err != nil {
		return fmt.Errorf("failed to connect to inception vault: %w", err)
	}
	defer inceptionClient.Close()

	err = inceptionClient.ValidateConnection()
	if err != nil {
		return fmt.Errorf("inception vault validation failed: %w", err)
	}

	m.logger.Infow("✓ Inception vault accessible", "target", inceptionName)

	// Validate production vault target
	m.logger.Infow("Checking production vault target", "target", productionName)

	productionClient, err := m.createClientForTarget(productionName)
	if err != nil {
		return fmt.Errorf("failed to connect to production vault: %w", err)
	}
	defer productionClient.Close()

	err = productionClient.ValidateConnection()
	if err != nil {
		return fmt.Errorf("production vault validation failed: %w", err)
	}

	m.logger.Infow("✓ Production vault accessible", "target", productionName)

	m.logger.Info("All vault targets validated successfully")

	return nil
}

// exportImportVault exports secrets from inception vault and imports to production vault.
// This migrates data between TWO DIFFERENT VAULT INSTANCES (not just paths in one vault).
// Returns the count of secrets migrated.
func (m *Manager) exportImportVault(inceptionName, productionName string) (int, error) {
	m.logger.Infow("Migrating from inception vault to production vault",
		"from", inceptionName, "to", productionName)

	// Create clients for BOTH vault targets (two different vault instances)
	inceptionClient, err := m.createClientForTarget(inceptionName)
	if err != nil {
		return 0, fmt.Errorf("failed to create inception vault client: %w", err)
	}
	defer inceptionClient.Close()

	productionClient, err := m.createClientForTarget(productionName)
	if err != nil {
		return 0, fmt.Errorf("failed to create production vault client: %w", err)
	}
	defer productionClient.Close()

	// Create Safe wrappers for each vault instance
	inceptionSafe := NewSafe(inceptionClient)
	productionSafe := NewSafe(productionClient)

	// Export from inception vault instance
	m.logger.Info("\nExporting secrets from inception vault...")
	fmt.Println()

	secrets, err := inceptionSafe.Export("secret/")
	if err != nil {
		return 0, fmt.Errorf("failed to export from inception vault: %w", err)
	}

	// Count the number of secret paths (nested maps need recursive counting)
	secretCount := m.countSecretPaths(secrets)

	// Print detailed export results
	m.printExportedPaths(secrets, 0)
	fmt.Println()

	m.logger.Infow("✓ Exported secrets from inception", "paths", secretCount)

	// SAFETY CHECK: Warn if no secrets were found
	if secretCount == 0 {
		m.logger.Warn("⚠️  WARNING: No secrets found in inception vault at secret/ path")
		m.logger.Warn("⚠️  This may indicate the vault is empty or the path is incorrect")
		return 0, nil // Return 0, not an error - let caller decide what to do
	}

	// Import to production vault instance
	m.logger.Info("\nImporting secrets to production vault...")
	fmt.Println()

	err = m.importWithProgress(productionSafe, "secret/", secrets)
	if err != nil {
		return 0, fmt.Errorf("failed to import to production vault: %w", err)
	}

	fmt.Println()
	m.logger.Infow("✓ Successfully migrated secrets between vault instances", "paths", secretCount)

	return secretCount, nil
}

// validateMigration validates that all secrets were migrated correctly using per-path checksums.
// This validates across TWO DIFFERENT VAULT INSTANCES.
// Returns the count of successfully validated secrets.
func (m *Manager) validateMigration(inceptionName, productionName string) (int, error) {
	// Create clients for both vault instances
	inceptionClient, err := m.createClientForTarget(inceptionName)
	if err != nil {
		return 0, fmt.Errorf("failed to create inception client: %w", err)
	}
	defer inceptionClient.Close()

	productionClient, err := m.createClientForTarget(productionName)
	if err != nil {
		return 0, fmt.Errorf("failed to create production client: %w", err)
	}
	defer productionClient.Close()

	// Create Safe wrappers for each vault instance
	inceptionSafe := NewSafe(inceptionClient)
	productionSafe := NewSafe(productionClient)

	// Get all paths from inception vault
	paths, err := m.getVaultPathsWithKeysFromSafe(inceptionSafe, "secret/")
	if err != nil {
		return 0, fmt.Errorf("failed to get vault paths from inception: %w", err)
	}

	m.logger.Infow("Found keys to validate", "count", len(paths))

	// SAFETY CHECK: Warn if no paths were found
	if len(paths) == 0 {
		m.logger.Warn("⚠️  WARNING: No secret paths found in inception vault for validation")
		m.logger.Warn("⚠️  This indicates the vault is empty or paths are inaccessible")
		return 0, nil // Return 0, not an error - let caller decide
	}

	// Validate checksums across the two vault instances
	failedPaths, validatedCount := m.validateAllPathsAcrossVaults(
		inceptionSafe, productionSafe, "secret/", "secret/", paths)

	if len(failedPaths) > 0 {
		m.reportValidationFailures(failedPaths)

		return validatedCount, fmt.Errorf("%w: validation failed for %d paths", ErrValidationFailed, len(failedPaths))
	}

	m.logger.Infow("✓ All keys validated successfully!", "validated", validatedCount)

	return validatedCount, nil
}

// validateAllPathsAcrossVaults validates checksums for all paths across two vault instances.
func (m *Manager) validateAllPathsAcrossVaults(
	inceptionSafe, productionSafe *Safe,
	inceptionPath, productionPath string,
	paths []string) ([]pathValidationFailure, int) {
	var failedPaths []pathValidationFailure

	validatedCount := 0

	for _, path := range paths {
		failure := m.validateSinglePathAcrossVaults(
			inceptionSafe, productionSafe, inceptionPath, productionPath, path)
		if failure != nil {
			failedPaths = append(failedPaths, *failure)
		} else {
			m.logger.Infow("✓ Validated", "path", path)

			validatedCount++
		}
	}

	return failedPaths, validatedCount
}

// validateSinglePathAcrossVaults validates a single path's checksums across two vault instances.
func (m *Manager) validateSinglePathAcrossVaults(
	inceptionSafe, productionSafe *Safe,
	inceptionPath, productionPath, path string) *pathValidationFailure {
	// Print path being validated (without newline, we'll add status after)
	fmt.Printf("  %-60s ", path)

	inceptionChecksum, err := m.calculatePathChecksumFromSafe(inceptionSafe, inceptionPath, path)
	if err != nil {
		fmt.Printf("\033[31m✗\033[0m (failed to get inception checksum: %v)\n", err)

		return &pathValidationFailure{
			Path:  path,
			Error: fmt.Sprintf("failed to get inception checksum: %v", err),
		}
	}

	productionChecksum, err := m.calculatePathChecksumFromSafe(productionSafe, productionPath, path)
	if err != nil {
		fmt.Printf("\033[31m✗\033[0m (failed to get production checksum: %v)\n", err)

		return &pathValidationFailure{
			Path:  path,
			Error: fmt.Sprintf("failed to get production checksum: %v", err),
		}
	}

	if inceptionChecksum == productionChecksum {
		// Print checksums and success marker
		fmt.Printf("%s → %s \033[32m✓\033[0m\n", inceptionChecksum[:8], productionChecksum[:8])
		return nil
	}

	// Print checksums and failure marker
	fmt.Printf("%s → %s \033[31m✗\033[0m (mismatch)\n", inceptionChecksum[:8], productionChecksum[:8])

	return &pathValidationFailure{
		Path:               path,
		InceptionChecksum:  inceptionChecksum[:8],
		ProductionChecksum: productionChecksum[:8],
	}
}

// reportValidationFailures logs all validation failures.
func (m *Manager) reportValidationFailures(failedPaths []pathValidationFailure) {
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
}

// decommissionInception safely removes the inception vault.
// This matches the Perl implementation in OCFP::Vault::Manager::decommission_inception.
//
//nolint:unparam // error return is for future error handling, maintains consistent interface
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

	updatedEnvs, failedEnvs := m.updateAllEnvironments(environments)
	m.reportEnvironmentUpdateResults(updatedEnvs, failedEnvs)

	if len(failedEnvs) > 0 {
		return fmt.Errorf("%w: failed to update %d environments", ErrEnvironmentUpdate, len(failedEnvs))
	}

	m.logger.Info("Environment secrets-providers updated successfully")

	return nil
}

// updateAllEnvironments updates secrets providers for all environments.
func (m *Manager) updateAllEnvironments(environments []parsedGenesisEnv) ([]environmentUpdate, []environmentFailure) {
	var (
		updatedEnvs []environmentUpdate
		failedEnvs  []environmentFailure
	)

	vaultName := m.blocName + "-mgmt"

	for _, env := range environments {
		targets := m.getTargetTypesForKit(env.Kit)

		for _, target := range targets {
			updated, failed := m.updateEnvironmentTarget(env.Kit, target, vaultName)
			if failed != nil {
				failedEnvs = append(failedEnvs, *failed)
			} else if updated != nil {
				updatedEnvs = append(updatedEnvs, *updated)
			}
		}
	}

	return updatedEnvs, failedEnvs
}

// updateEnvironmentTarget updates secrets provider for a single environment target.
func (m *Manager) updateEnvironmentTarget(kit, target, vaultName string) (*environmentUpdate, *environmentFailure) {
	genesisEnv := fmt.Sprintf("@%s-%s:%s", m.blocName, target, kit)

	m.logger.Infow("Running Genesis command",
		"command", fmt.Sprintf("genesis %s secrets-provider %s", genesisEnv, vaultName))

	ctx := context.Background()
	// #nosec G204 - genesis, genesisEnv and vaultName are constructed from validated internal state
	cmd := exec.CommandContext(ctx, "genesis", genesisEnv, "secrets-provider", vaultName)

	output, err := cmd.CombinedOutput()
	if err != nil {
		m.logger.Errorw("✗ Failed to update", "env", genesisEnv, "error", string(output))

		return nil, &environmentFailure{
			Kit:    kit,
			Target: target,
			Error:  string(output),
		}
	}

	m.logger.Infow("✓ Successfully updated", "env", genesisEnv)

	return &environmentUpdate{
		Kit:    kit,
		Target: target,
	}, nil
}

// reportEnvironmentUpdateResults logs the results of environment updates.
func (m *Manager) reportEnvironmentUpdateResults(updatedEnvs []environmentUpdate, failedEnvs []environmentFailure) {
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
	}
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
		return NewAWSVaultProvider(m.config, m.safe, m.blocName), nil
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
	sessions, err := m.listTmuxSessions()
	if err != nil {
		return err
	}

	matchedSessions := m.filterInceptionSessions(sessions, inceptionName)
	if len(matchedSessions) == 0 {
		m.logger.Debug("No inception vault tmux sessions found")

		return nil
	}

	ctx := context.Background()

	for _, session := range matchedSessions {
		m.logger.Infow("Found inception vault tmux session", "session", session)

		//nolint:noinlineerr // error is passed to logger for context
		if err := m.killTmuxSession(ctx, session); err != nil {
			m.logger.Warnw("Failed to kill tmux session", "session", session, "error", err)
		} else {
			m.logger.Infow("Stopped inception vault tmux session", "session", session)
		}
	}

	return nil
}

// listTmuxSessions retrieves all active tmux session names.
//
//nolint:unparam // error return for interface consistency and future implementation
func (m *Manager) listTmuxSessions() ([]string, error) {
	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "tmux", "list-sessions", "-F", "#{session_name}")

	output, cmdErr := cmd.Output()
	if cmdErr != nil {
		// No tmux sessions found or tmux not running - intentionally not an error
		m.logger.Debug("No tmux sessions found or tmux not running")

		return nil, nil //nolint:nilerr // No tmux sessions is not an error condition
	}

	var sessions []string

	for _, session := range strings.Split(string(output), "\n") {
		session = strings.TrimSpace(session)
		if session != "" {
			sessions = append(sessions, session)
		}
	}

	return sessions, nil
}

// filterInceptionSessions filters sessions matching inception vault pattern.
func (m *Manager) filterInceptionSessions(sessions []string, inceptionName string) []string {
	expectedSession := inceptionName + "-vault"

	var matchedSessions []string

	for _, session := range sessions {
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

	return matchedSessions
}

// ErrInvalidTmuxSession indicates an invalid tmux session name.
var ErrInvalidTmuxSession = errors.New("invalid tmux session name")

// killTmuxSession attempts graceful shutdown then kills a tmux session.
func (m *Manager) killTmuxSession(ctx context.Context, session string) error {
	// Validate session name to prevent command injection
	if !isValidTmuxSession(session) {
		m.logger.Warnw("Skipping invalid tmux session name", "session", session)

		return fmt.Errorf("%w: %s", ErrInvalidTmuxSession, session)
	}

	// Send Ctrl-C to session
	// #nosec G204 - session name is validated above
	cmd := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, "C-c")

	err := cmd.Run()
	if err != nil {
		m.logger.Warnw("Failed to send Ctrl-C to tmux session", "session", session, "error", err)
	}

	// Wait for graceful shutdown
	time.Sleep(gracefulShutdownWaitSeconds * time.Second)

	// Kill the tmux session
	// #nosec G204 - session name is validated above
	cmd = exec.CommandContext(ctx, "tmux", "kill-session", "-t", session)

	//nolint:noinlineerr // error wrapping provides context
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to kill tmux session: %w", err)
	}

	return nil
}

// killSafeProcesses kills safe local processes running on the specified port.
// This matches the Perl implementation in OCFP::Vault::Manager::decommission_inception.
//
//nolint:unparam // error return maintains consistency; best-effort operations don't fail
func (m *Manager) killSafeProcesses(port int) error {
	ctx := context.Background()

	// Find processes using ps and grep
	findCmd := fmt.Sprintf("ps aux | grep -E 'safe local.*--port %d' | grep -v grep", port)

	// #nosec G204 - port is an integer constant (8234), findCmd contains no user input
	cmd := exec.CommandContext(ctx, "sh", "-c", findCmd)

	output, cmdErr := cmd.Output()
	if cmdErr != nil {
		// No processes found - intentionally not an error
		m.logger.Debug("No safe local processes found on port", "port", port)

		return nil //nolint:nilerr // No processes found is not an error condition
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
		if len(fields) < minPsAuxFields {
			continue
		}

		pid := fields[1]
		m.logger.Infow("Found safe local process", "pid", pid, "port", port)

		// Send SIGTERM first
		killCmd := "kill -TERM " + pid

		// #nosec G204 - pid is extracted from ps aux output and validated as numeric field
		killCmdExec := exec.CommandContext(ctx, "sh", "-c", killCmd)

		killErr := killCmdExec.Run()
		if killErr != nil {
			m.logger.Warnw("Failed to send SIGTERM to process", "pid", pid, "error", killErr)
		}
	}

	// Wait for graceful shutdown
	time.Sleep(processTerminationWaitSeconds * time.Second)

	// Check if processes are still running
	// #nosec G204 - findCmd is constructed from integer port constant, no user input
	checkCmd := exec.CommandContext(ctx, "sh", "-c", findCmd)

	checkOutput, checkErr := checkCmd.Output()
	if checkErr == nil && len(checkOutput) > 0 {
		m.logger.Warn("Some processes didn't shutdown gracefully, forcing kill")

		// Force kill with SIGKILL
		forceKillCmd := fmt.Sprintf("pkill -9 -f 'safe local.*--port %d'", port)

		// #nosec G204 - forceKillCmd is constructed from integer port constant, no user input
		forceCmd := exec.CommandContext(ctx, "sh", "-c", forceKillCmd)
		_ = forceCmd.Run() // Ignore errors - best effort
	}

	m.logger.Infow("Killed safe local processes", "port", port)

	return nil
}

// verifyPortFreed checks if the vault port has been freed.
// This matches the Perl implementation in OCFP::Vault::Manager::decommission_inception.
func (m *Manager) verifyPortFreed(port int) error {
	ctx := context.Background()

	// Check if port is still in use
	checkCmd := fmt.Sprintf("lsof -i :%d 2>/dev/null | grep LISTEN", port)

	// #nosec G204 - checkCmd is constructed from integer port constant, no user input
	cmd := exec.CommandContext(ctx, "sh", "-c", checkCmd)

	output, cmdErr := cmd.Output()
	if cmdErr != nil || len(output) == 0 {
		// Port is free - intentionally not an error
		m.logger.Infow("Port is now free", "port", port)

		return nil //nolint:nilerr // Port being free is success, not an error
	}

	// Port is still in use
	m.logger.Warnw("Port is still in use after cleanup attempt", "port", port)
	m.logger.Warn("You may need to manually kill the process using the port")

	return fmt.Errorf("%w: port %d is still in use", ErrPortInUse, port)
}

// cleanupVaultFiles removes vault-related files from the filesystem.
// This matches the Perl implementation in OCFP::Vault::Manager::decommission_inception.
func (m *Manager) cleanupVaultFiles(_ string) error {
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		return ErrHomeNotSet
	}

	removedFiles := 0

	removedFiles += m.removeGlobalVaultFiles(homeDir)
	removedFiles += m.removeBlocSpecificVaultFiles(homeDir)

	m.logCleanupResults(removedFiles)

	return nil
}

// removeGlobalVaultFiles removes global vault files (~/.vault.key and ~/.vault).
func (m *Manager) removeGlobalVaultFiles(homeDir string) int {
	removedCount := 0

	vaultKeyFile := filepath.Join(homeDir, ".vault.key")
	if m.removeFileIfExists(vaultKeyFile, "vault key file") {
		removedCount++
	}

	vaultDir := filepath.Join(homeDir, ".vault")
	if m.removeDirIfExists(vaultDir, "vault directory") {
		removedCount++
	}

	return removedCount
}

// removeBlocSpecificVaultFiles removes bloc-specific vault files.
func (m *Manager) removeBlocSpecificVaultFiles(homeDir string) int {
	if m.blocName == "" {
		return 0
	}

	removedCount := 0

	blocVaultKeyFile := filepath.Join(homeDir, ".ocfp", m.blocName, "vault", "root.key")
	if m.removeFileIfExists(blocVaultKeyFile, "bloc vault key") {
		removedCount++
	}

	unsealKeysFile := filepath.Join(homeDir, ".ocfp", m.blocName, "vault", "unseal.keys")
	if m.removeFileIfExists(unsealKeysFile, "unseal keys") {
		removedCount++
	}

	blocVaultDataDir := filepath.Join(homeDir, ".ocfp", m.blocName, "vault", "data")
	if m.removeDirIfExists(blocVaultDataDir, "vault data directory") {
		removedCount++
	}

	return removedCount
}

// removeFileIfExists removes a file if it exists and returns true if removed.
func (m *Manager) removeFileIfExists(path, description string) bool {
	_, err := os.Stat(path)
	if err != nil {
		return false
	}

	err = os.Remove(path)
	if err != nil {
		m.logger.Warnw("Failed to remove "+description, "file", path, "error", err)

		return false
	}

	m.logger.Infow("Removed "+description, "file", path)

	return true
}

// removeDirIfExists removes a directory if it exists and returns true if removed.
func (m *Manager) removeDirIfExists(path, description string) bool {
	_, err := os.Stat(path)
	if err != nil {
		return false
	}

	err = os.RemoveAll(path)
	if err != nil {
		m.logger.Warnw("Failed to remove "+description, "dir", path, "error", err)

		return false
	}

	m.logger.Infow("Removed "+description, "dir", path)

	return true
}

// logCleanupResults logs the file cleanup results.
func (m *Manager) logCleanupResults(removedFiles int) {
	if removedFiles > 0 {
		m.logger.Infow("File cleanup completed", "removed", removedFiles)
	} else {
		m.logger.Info("No vault files found to remove")
	}
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
	data, getErr := m.safe.GetAll(fullPath)
	if getErr == nil {
		// This is a secret with keys - add each key as a path:key combination
		// Store as relative path from basePath (or empty string if at base)
		for key := range data {
			// Always use currentPath (relative to basePath), never fullPath
			*paths = append(*paths, currentPath+":"+key)
		}

		return nil
	}

	// Try to list as a directory
	subPaths, listErr := m.safe.List(fullPath)
	if listErr != nil {
		// Neither a secret nor a directory - intentionally not an error
		return nil //nolint:nilerr // Empty or non-existent paths are expected during vault walk
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
	parts := strings.SplitN(pathWithKey, ":", pathKeyDelimiterParts)
	if len(parts) != pathKeyDelimiterParts {
		return "", fmt.Errorf("%w: %s (expected path:key)", ErrInvalidPathFormat, pathWithKey)
	}

	relativePath := parts[0]
	key := parts[1]

	// Build full path from base + relative path
	// relativePath is relative to basePath (may be empty for base-level secrets)
	fullPath := basePath
	if relativePath != "" && !strings.HasPrefix(relativePath, ":") {
		fullPath = basePath + "/" + relativePath
	}

	// Get the value from vault
	value, err := m.safe.Get(fullPath, key)
	if err != nil {
		return "", fmt.Errorf("failed to get value for %s: %w", pathWithKey, err)
	}

	// Convert value to string for consistent hashing
	var valueStr string
	switch typedValue := value.(type) {
	case string:
		valueStr = typedValue
	case []byte:
		valueStr = string(typedValue)
	default:
		// For other types, marshal to JSON
		jsonBytes, err := json.Marshal(typedValue)
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
//
//nolint:unparam // error return for interface consistency and future implementation
func (m *Manager) getGenesisEnvironments() ([]parsedGenesisEnv, error) {
	output, err := m.executeGenesisEnvsCommand()
	if err != nil {
		m.logger.Warnw("Failed to get genesis environments", "error", err, "output", string(output))

		return []parsedGenesisEnv{}, nil
	}

	return m.parseGenesisOutput(output), nil
}

// executeGenesisEnvsCommand runs genesis envs with fallback strategies.
func (m *Manager) executeGenesisEnvsCommand() ([]byte, error) {
	ctx := context.Background()

	// Run genesis envs command
	cmd := exec.CommandContext(ctx, "genesis", "envs")

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Try with full path
		m.logger.Debug("genesis envs failed, trying with full path")

		whichCmd := exec.CommandContext(ctx, "which", "genesis")

		whichOutput, whichErr := whichCmd.Output()
		if whichErr == nil {
			genesisPath := strings.TrimSpace(string(whichOutput))
			if genesisPath != "" {
				m.logger.Infof("Found genesis at: %s", genesisPath)

				// #nosec G204 - genesisPath is from system 'which' command for genesis binary
				cmd = exec.CommandContext(ctx, genesisPath, "envs")

				output, err = cmd.CombinedOutput()
			}
		}

		// Try shell fallback
		if err != nil {
			m.logger.Debug("Attempting shell fallback: sh -c 'genesis envs 2>&1'")

			cmd = exec.CommandContext(ctx, "sh", "-c", "genesis envs 2>&1")

			output, err = cmd.CombinedOutput()
		}
	}

	if err != nil {
		return output, fmt.Errorf("failed to execute genesis envs command: %w", err)
	}

	return output, nil
}

// parseGenesisOutput parses genesis envs command output into environment structs.
func (m *Manager) parseGenesisOutput(output []byte) []parsedGenesisEnv {
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

		if env := m.buildParsedEnv(parts[len(parts)-1]); env != nil {
			environments = append(environments, *env)
		}
	}

	return environments
}

// buildParsedEnv creates a parsedGenesisEnv from a kit/environment string.
func (m *Manager) buildParsedEnv(kitEnvStr string) *parsedGenesisEnv {
	if !strings.Contains(kitEnvStr, "/") {
		return nil
	}

	kitAndEnv := strings.SplitN(kitEnvStr, "/", pathKeyDelimiterParts)
	if len(kitAndEnv) != pathKeyDelimiterParts {
		return nil
	}

	kit := strings.TrimSpace(kitAndEnv[0])
	envName := strings.TrimSpace(kitAndEnv[1])

	// Remove ANSI color codes
	kit = stripANSI(kit)
	envName = stripANSI(envName)

	// Skip blank entries
	if kit == "" || envName == "" {
		return nil
	}

	// Determine type based on environment name suffix
	envType := "ocf" // default
	if strings.Contains(envName, "-mgmt") {
		envType = "mgmt"
	}

	return &parsedGenesisEnv{
		Kit:  kit,
		Name: envName,
		Type: envType,
	}
}

// isValidTmuxSession validates a tmux session name to prevent command injection.
// Tmux session names should only contain alphanumeric characters, hyphens, and underscores.
func isValidTmuxSession(session string) bool {
	// Allow alphanumeric, hyphens, underscores, and dots (common in session names)
	validPattern := regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

	return validPattern.MatchString(session)
}

// stripANSI removes ANSI escape codes from a string.
func stripANSI(str string) string {
	// Simple ANSI escape sequence pattern: \x1b[...m
	re := regexp.MustCompile(`\x1b\[[0-9;]*m`)

	return re.ReplaceAllString(str, "")
}

// createClientForTarget creates a vault client for a specific safe target by reading ~/.saferc.
func (m *Manager) createClientForTarget(targetName string) (*Client, error) {
	m.logger.Debugw("Creating client for vault target", "target", targetName)

	// Read .saferc to get target configuration
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		return nil, ErrHomeNotSet
	}

	saferc := filepath.Join(homeDir, ".saferc")

	data, err := os.ReadFile(saferc)
	if err != nil {
		return nil, fmt.Errorf("failed to read .saferc: %w", err)
	}

	// Parse .saferc YAML
	var config struct {
		Vaults map[string]struct {
			URL        string `yaml:"url"`
			Token      string `yaml:"token"`
			SkipVerify bool   `yaml:"skip_verify"`
		} `yaml:"vaults"`
	}

	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse .saferc: %w", err)
	}

	// Look up the target
	target, ok := config.Vaults[targetName]
	if !ok {
		return nil, fmt.Errorf("vault target '%s' not found in .saferc", targetName)
	}

	if target.URL == "" {
		return nil, fmt.Errorf("vault target '%s' has no URL configured", targetName)
	}

	m.logger.Infow("Found vault target", "target", targetName, "url", target.URL, "skip_verify", target.SkipVerify)

	// Create client for this specific vault instance
	vaultConfig := &Config{
		Address:   target.URL,
		Token:     target.Token,
		Namespace: "", // Targets typically don't use namespaces
		TLSSkip:   target.SkipVerify,
	}

	client, err := NewClient(vaultConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create client for target '%s': %w", targetName, err)
	}

	return client, nil
}

// getVaultPathsWithKeysFromSafe gets all secret paths from a specific Safe instance.
func (m *Manager) getVaultPathsWithKeysFromSafe(safe *Safe, basePath string) ([]string, error) {
	var paths []string

	err := m.walkVaultPathsFromSafe(safe, basePath, "", &paths)
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

// walkVaultPathsFromSafe recursively walks vault paths from a specific Safe instance.
func (m *Manager) walkVaultPathsFromSafe(safe *Safe, basePath, currentPath string, paths *[]string) error {
	fullPath := basePath
	if currentPath != "" {
		fullPath = basePath + "/" + currentPath
	}

	// Try to read as a secret first
	data, getErr := safe.GetAll(fullPath)
	if getErr == nil {
		// This is a secret with keys - add each key as a path:key combination
		// Store as relative path from basePath (or empty string if at base)
		for key := range data {
			// Always use currentPath (relative to basePath), never fullPath
			*paths = append(*paths, currentPath+":"+key)
		}

		return nil
	}

	// Try to list as a directory
	subPaths, listErr := safe.List(fullPath)
	if listErr != nil {
		// Neither a secret nor a directory - intentionally not an error
		return nil //nolint:nilerr // Empty or non-existent paths are expected during vault walk
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

		err := m.walkVaultPathsFromSafe(safe, basePath, newCurrentPath, paths)
		if err != nil {
			return err
		}
	}

	return nil
}

// calculatePathChecksumFromSafe calculates SHA256 checksum from a specific Safe instance.
func (m *Manager) calculatePathChecksumFromSafe(safe *Safe, basePath, pathWithKey string) (string, error) {
	// Parse path:key format
	parts := strings.SplitN(pathWithKey, ":", pathKeyDelimiterParts)
	if len(parts) != pathKeyDelimiterParts {
		return "", fmt.Errorf("%w: %s (expected path:key)", ErrInvalidPathFormat, pathWithKey)
	}

	relativePath := parts[0]
	key := parts[1]

	// Build full path from base + relative path
	// relativePath is relative to basePath (may be empty for base-level secrets)
	fullPath := basePath
	if relativePath != "" && !strings.HasPrefix(relativePath, ":") {
		fullPath = basePath + "/" + relativePath
	}

	// Get the value from vault
	value, err := safe.Get(fullPath, key)
	if err != nil {
		return "", fmt.Errorf("failed to get value for %s: %w", pathWithKey, err)
	}

	// Convert value to string for consistent hashing
	var valueStr string
	switch typedValue := value.(type) {
	case string:
		valueStr = typedValue
	case []byte:
		valueStr = string(typedValue)
	default:
		// For other types, marshal to JSON
		jsonBytes, err := json.Marshal(typedValue)
		if err != nil {
			return "", fmt.Errorf("failed to marshal value: %w", err)
		}

		valueStr = string(jsonBytes)
	}

	// Calculate SHA256 of the value
	hash := sha256.Sum256([]byte(valueStr))

	return hex.EncodeToString(hash[:]), nil
}

// countSecretPaths recursively counts the number of secret paths in the export map structure.
// The export structure can be nested, so we need to recursively count all leaf nodes.
func (m *Manager) countSecretPaths(data map[string]interface{}) int {
	if len(data) == 0 {
		return 0
	}

	count := 0

	for _, value := range data {
		switch typedValue := value.(type) {
		case map[string]interface{}:
			// Check if this looks like a secret (has non-map values) or a path (has only map values)
			hasLeafValues := false
			for _, v := range typedValue {
				if _, isMap := v.(map[string]interface{}); !isMap {
					hasLeafValues = true
					break
				}
			}

			if hasLeafValues {
				// This is a secret with key-value pairs
				count++
			} else {
				// This is a path with sub-paths, recurse
				count += m.countSecretPaths(typedValue)
			}
		default:
			// This is a leaf value, count it as a secret
			count++
		}
	}

	return count
}

// snapshotInceptionVault creates a safety snapshot of the inception vault before migration.
// The snapshot is saved to ~/.ocfp/{bloc}/vault/snapshots/inception/{timestamp}.json
// Returns the path to the snapshot file.
func (m *Manager) snapshotInceptionVault(inceptionName string) (string, error) {
	// Create snapshot directory
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		return "", ErrHomeNotSet
	}

	snapshotDir := filepath.Join(homeDir, ".ocfp", m.blocName, "vault", "snapshots", "inception")
	err := os.MkdirAll(snapshotDir, 0700)
	if err != nil {
		return "", fmt.Errorf("failed to create snapshot directory: %w", err)
	}

	// Generate snapshot filename with timestamp
	timestamp := time.Now().Format("20060102-150405")
	snapshotPath := filepath.Join(snapshotDir, fmt.Sprintf("%s.json", timestamp))

	// Create inception client
	inceptionClient, err := m.createClientForTarget(inceptionName)
	if err != nil {
		return "", fmt.Errorf("failed to create inception client: %w", err)
	}
	defer inceptionClient.Close()

	// Export all secrets from inception
	inceptionSafe := NewSafe(inceptionClient)
	secrets, err := inceptionSafe.Export("secret/")
	if err != nil {
		return "", fmt.Errorf("failed to export inception vault for snapshot: %w", err)
	}

	// Marshal to JSON with indentation for human readability
	jsonData, err := json.MarshalIndent(secrets, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal snapshot data: %w", err)
	}

	// Write to file
	err = os.WriteFile(snapshotPath, jsonData, 0600)
	if err != nil {
		return "", fmt.Errorf("failed to write snapshot file: %w", err)
	}

	m.logger.Infow("Created snapshot", "path", snapshotPath, "size_bytes", len(jsonData))

	return snapshotPath, nil
}

// confirmDecommission prompts the user for confirmation before decommissioning the inception vault.
// Returns true if the user confirms, false if they decline.
func (m *Manager) confirmDecommission() (bool, error) {
	// Check if we're in an interactive terminal
	// If stdin is not a terminal, we can't prompt
	if !m.isInteractiveTerminal() {
		m.logger.Warn("Non-interactive terminal detected - use --force to skip confirmation")
		return false, fmt.Errorf("interactive confirmation required but terminal is non-interactive")
	}

	// Print confirmation prompt
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════════════════════════════╗")
	fmt.Println("║                    ⚠️  CRITICAL OPERATION ⚠️                       ║")
	fmt.Println("║                                                                   ║")
	fmt.Println("║  About to PERMANENTLY DECOMMISSION the inception vault:          ║")
	fmt.Println("║                                                                   ║")
	fmt.Println("║  This will:                                                       ║")
	fmt.Println("║    • Kill all inception vault processes                           ║")
	fmt.Println("║    • Delete all inception vault data                              ║")
	fmt.Println("║    • Remove all inception vault files                             ║")
	fmt.Println("║                                                                   ║")
	fmt.Println("║  This operation CANNOT be undone!                                 ║")
	fmt.Println("╚═══════════════════════════════════════════════════════════════════╝")
	fmt.Println()
	fmt.Print("Type 'yes' to proceed with decommission: ")

	// Read user input
	var response string
	_, err := fmt.Scanln(&response)
	if err != nil {
		return false, fmt.Errorf("failed to read confirmation: %w", err)
	}

	// Check for exact "yes" response
	response = strings.TrimSpace(strings.ToLower(response))
	if response == "yes" {
		m.logger.Info("User confirmed decommission")
		return true, nil
	}

	m.logger.Info("User declined decommission")
	return false, nil
}

// isInteractiveTerminal checks if the program is running in an interactive terminal.
func (m *Manager) isInteractiveTerminal() bool {
	// Check if stdin is a terminal
	fileInfo, err := os.Stdin.Stat()
	if err != nil {
		return false
	}

	// If stdin is a character device, it's interactive
	return (fileInfo.Mode() & os.ModeCharDevice) != 0
}

// printExportedPaths recursively prints exported secret paths for visibility.
func (m *Manager) printExportedPaths(data map[string]interface{}, depth int) {
	if len(data) == 0 {
		return
	}

	// Sort keys for consistent output
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	// Simple bubble sort
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[i] > keys[j] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}

	indent := strings.Repeat("  ", depth)

	for _, key := range keys {
		value := data[key]
		switch typedValue := value.(type) {
		case map[string]interface{}:
			// Check if this is a secret (has non-map values) or a path (has only map values)
			hasLeafValues := false
			for _, v := range typedValue {
				if _, isMap := v.(map[string]interface{}); !isMap {
					hasLeafValues = true
					break
				}
			}

			if hasLeafValues {
				// This is a secret with key-value pairs
				fmt.Printf("%s\033[32m✓\033[0m secret/%s\n", indent, key)
			} else {
				// This is a path with sub-paths, recurse
				if depth == 0 {
					fmt.Printf("%s\033[34m→\033[0m secret/%s/\n", indent, key)
				} else {
					fmt.Printf("%s\033[34m→\033[0m %s/\n", indent, key)
				}
				m.printExportedPaths(typedValue, depth+1)
			}
		default:
			// Leaf value
			fmt.Printf("%s\033[32m✓\033[0m secret/%s\n", indent, key)
		}
	}
}

// importWithProgress imports secrets with progress output.
func (m *Manager) importWithProgress(safe *Safe, basePath string, data map[string]interface{}) error {
	return m.importRecursiveWithProgress(safe, basePath, data, 0)
}

// importRecursiveWithProgress recursively imports secrets with progress output.
func (m *Manager) importRecursiveWithProgress(safe *Safe, basePath string, data map[string]interface{}, depth int) error {
	if len(data) == 0 {
		return nil
	}

	// Sort keys for consistent output
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	// Simple bubble sort
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[i] > keys[j] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}

	indent := strings.Repeat("  ", depth)

	for _, subPath := range keys {
		value := data[subPath]
		fullPath := filepath.Join(basePath, subPath)

		if valueMap, ok := value.(map[string]interface{}); ok {
			// Check if this is a secret (has non-map values) or a path (has only map values)
			hasLeafValues := false
			for _, v := range valueMap {
				if _, isMap := v.(map[string]interface{}); !isMap {
					hasLeafValues = true
					break
				}
			}

			if hasLeafValues {
				// This is a secret with key-value pairs, import it
				fmt.Printf("%s\033[32m✓\033[0m %s\n", indent, fullPath)
				err := safe.SetMultiple(fullPath, valueMap)
				if err != nil {
					fmt.Printf("%s\033[31m✗\033[0m %s (error: %v)\n", indent, fullPath, err)
					return fmt.Errorf("failed to import to %s: %w", fullPath, err)
				}
			} else {
				// This is a path with sub-paths, recurse
				fmt.Printf("%s\033[34m→\033[0m %s/\n", indent, fullPath)
				err := m.importRecursiveWithProgress(safe, basePath, valueMap, depth+1)
				if err != nil {
					return err
				}
			}
		} else {
			// This is a single value, import as a single key
			fmt.Printf("%s\033[32m✓\033[0m %s\n", indent, fullPath)
			err := safe.Set(fullPath, "value", value)
			if err != nil {
				fmt.Printf("%s\033[31m✗\033[0m %s (error: %v)\n", indent, fullPath, err)
				return fmt.Errorf("failed to import single value to %s: %w", fullPath, err)
			}
		}
	}

	return nil
}
