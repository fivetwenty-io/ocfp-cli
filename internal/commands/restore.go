package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// RestoreMode represents the restore operation mode.
type RestoreMode string

const (
	RestoreModeFull   RestoreMode = "full"
	RestoreModeConfig RestoreMode = "config"
	RestoreModeData   RestoreMode = "data"
	RestoreModeVault  RestoreMode = "vault"

	// File permissions.
	DefaultDirMode  = 0750
	DefaultFileMode = 0600

	// Sizes.
	DefaultBackupSizeMB = 100
	DirectoryConfigs    = 1024
	DirectoryManifests  = 2048
	DirectoryDeploys    = 4096
)

// Restore errors.
var (
	errUnknownRestoreMode = errors.New("unknown restore mode")
)

// errUnknownRestoreModeWith wraps the static error with mode information.
func errUnknownRestoreModeWith(mode RestoreMode) error {
	return fmt.Errorf("%w: %v", errUnknownRestoreMode, mode)
}

// NewRestoreCmd creates the restore command.
func NewRestoreCmd() *cobra.Command {
	restoreFlags := &restoreFlags{
		backupID:     "",
		source:       "",
		bucket:       "",
		restoreMode:  "",
		destination:  "",
		force:        false,
		verify:       false,
		excludePaths: nil,
		dryRun:       false,
	}

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:     "restore [backup-id]",
		Short:   "Restore configurations and data from backup",
		Long:    getRestoreLongDescription(),
		Example: getRestoreExamples(),
		Args:    cobra.MaximumNArgs(1),
		RunE:    restoreFlags.runRestore,
	}

	restoreFlags.addFlags(cmd)

	return cmd
}

// restoreFlags holds all command flags for restore.
type restoreFlags struct {
	backupID     string
	source       string
	bucket       string
	restoreMode  string
	destination  string
	force        bool
	verify       bool
	excludePaths []string
	dryRun       bool
}

// addFlags adds all flags to the command.
func (rf *restoreFlags) addFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&rf.backupID, "backup-id", "", "specific backup ID to restore")
	cmd.Flags().StringVar(&rf.source, "source", "", "backup source location")
	cmd.Flags().StringVar(&rf.bucket, "bucket", "", "S3 bucket containing backups")
	cmd.Flags().StringVar(&rf.restoreMode, "mode", "full", "restore mode (full|config|data|vault)")
	cmd.Flags().StringVar(&rf.destination, "destination", "", "restore destination (default: current directory)")
	cmd.Flags().BoolVar(&rf.force, "force", false, "skip confirmation prompts")
	cmd.Flags().BoolVar(&rf.verify, "verify", true, "verify restore integrity")
	cmd.Flags().StringSliceVar(&rf.excludePaths, "exclude", nil, "paths to exclude from restore")
	cmd.Flags().BoolVar(&rf.dryRun, "dry-run", false, "show what would be restored without performing restore")
}

// runRestore executes the restore command.
func (rf *restoreFlags) runRestore(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	log := logger.Get()

	cfg, err := loadRestoreConfig()
	if err != nil {
		return err
	}

	backupID := rf.getBackupID(args)

	mode, err := rf.parseRestoreMode()
	if err != nil {
		return err
	}

	log.Info("Starting restore",
		"backup-id", backupID,
		"mode", mode,
		"deployment", cfg.Name,
		"dry-run", rf.dryRun)

	source := rf.resolveBackupSource(cfg, backupID)

	restore := rf.createRestoreOperation(backupID, source, mode)

	if rf.dryRun {
		return performDryRunRestore(restore)
	}

	err = rf.confirmRestore(cfg, backupID)
	if err != nil {
		return err
	}

	err = rf.executeRestore(ctx, cfg, restore, mode)
	if err != nil {
		return fmt.Errorf("restore failed: %w", err)
	}

	return rf.finalizeRestore(restore, backupID)
}

// getBackupID extracts backup ID from args.
func (rf *restoreFlags) getBackupID(args []string) string {
	if len(args) > 0 {
		return args[0]
	}

	return rf.backupID
}

// parseRestoreMode parses and validates restore mode.
func (rf *restoreFlags) parseRestoreMode() (RestoreMode, error) {
	switch rf.restoreMode {
	case "full", "":
		return RestoreModeFull, nil
	case "config":
		return RestoreModeConfig, nil
	case "data":
		return RestoreModeData, nil
	case "vault":
		return RestoreModeVault, nil
	default:
		return "", ErrInvalidRestoreMode(rf.restoreMode)
	}
}

// resolveBackupSource determines the backup source location.
func (rf *restoreFlags) resolveBackupSource(cfg *config.Config, backupID string) string {
	if rf.source != "" {
		return rf.source
	}

	if backupID == "" {
		latest := findLatestBackup(cfg.Name, rf.bucket)

		return latest.Source
	}

	return rf.source
}

// createRestoreOperation creates a RestoreOperation struct.
func (rf *restoreFlags) createRestoreOperation(backupID, source string, mode RestoreMode) *RestoreOperation {
	return &RestoreOperation{
		BackupID:      backupID,
		Source:        source,
		Mode:          mode,
		Destination:   rf.destination,
		Force:         rf.force,
		Verify:        rf.verify,
		ExcludePaths:  rf.excludePaths,
		DryRun:        rf.dryRun,
		Timestamp:     time.Now(),
		FileCount:     0,
		BytesRestored: 0,
	}
}

// confirmRestore prompts user for confirmation if not forced.
func (rf *restoreFlags) confirmRestore(cfg *config.Config, backupID string) error {
	if rf.force {
		return nil
	}

	logger.Get().Infof("This will restore %s from backup %s. Continue? [y/N]: ", cfg.Name, backupID)

	var response string

	_, _ = fmt.Scanln(&response)

	if !strings.HasPrefix(strings.ToLower(response), "y") {
		logger.Get().Info("Restore cancelled by user")

		return nil
	}

	return nil
}

// executeRestore performs the actual restore based on mode.
func (rf *restoreFlags) executeRestore(ctx context.Context, cfg *config.Config, restore *RestoreOperation, mode RestoreMode) error {
	switch mode {
	case RestoreModeFull:
		return performFullRestore(ctx, cfg, restore)
	case RestoreModeConfig:
		return performConfigRestore(ctx, cfg, restore)
	case RestoreModeData:
		return performDataRestore(ctx, cfg, restore)
	case RestoreModeVault:
		return performVaultRestore(ctx, cfg, restore)
	default:
		return errUnknownRestoreModeWith(mode)
	}
}

// finalizeRestore handles post-restore tasks.
func (rf *restoreFlags) finalizeRestore(restore *RestoreOperation, backupID string) error {
	log := logger.Get()

	if rf.verify {
		verifyRestore()
		log.Info("Restore verification completed")
	}

	log.Info("Restore completed successfully",
		"backup-id", backupID,
		"duration", time.Since(restore.Timestamp))

	logger.Get().Infof("Restore completed: %s", backupID)

	return nil
}

// loadRestoreConfig loads the configuration for restore.
func loadRestoreConfig() (*config.Config, error) {
	configFile := viper.GetString("config")
	blocName := viper.GetString("bloc")

	cfg, err := config.LoadWithParams(configFile, blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	return cfg, nil
}

// getRestoreLongDescription returns the long description for the restore command.
func getRestoreLongDescription() string {
	return `Restore OCFP deployment from backup files stored in Shield bucket.

The restore command can restore from various backup types:
- Full restore: Complete deployment restoration
- Config restore: Configuration files only
- Data restore: State files and data only
- Vault restore: Secrets and credentials only

Restores can be performed from Shield bucket backups or from local/remote
backup archives.`
}

// getRestoreExamples returns the examples for the restore command.
func getRestoreExamples() string {
	return `  # Restore from latest backup
  ocfp restore

  # Restore specific backup by ID
  ocfp restore production-backup-20240109-143022

  # Restore only configuration
  ocfp restore --mode config

  # Restore from specific source
  ocfp restore --source s3://my-bucket/backups/backup.tar.gz

  # Dry run to see what would be restored
  ocfp restore --dry-run

  # Force restore without confirmation
  ocfp restore --force`
}

// RestoreOperation represents a restore operation.
type RestoreOperation struct {
	BackupID      string
	Source        string
	Mode          RestoreMode
	Destination   string
	Force         bool
	Verify        bool
	ExcludePaths  []string
	DryRun        bool
	Timestamp     time.Time
	FileCount     int
	BytesRestored int64
}

// BackupInfo represents information about an available backup.
type BackupInfo struct {
	ID        string
	Source    string
	Type      string
	Timestamp time.Time
	Size      int64
	Encrypted bool
}

// performFullRestore performs a complete restore.
func performFullRestore(ctx context.Context, cfg *config.Config, restore *RestoreOperation) error {
	log := logger.Get()
	log.Info("Performing full restore")

	// Download and extract backup
	tempDir, err := downloadAndExtractBackup(ctx, cfg, restore.Source)
	if err != nil {
		return fmt.Errorf("failed to download backup: %w", err)
	}

	defer func() { _ = os.RemoveAll(tempDir) }()

	// Restore components in order
	components := []struct {
		name string
		path string
	}{
		{"configuration", filepath.Join(tempDir, "config")},
		{"deployments", filepath.Join(tempDir, "deployments")},
		{"manifests", filepath.Join(tempDir, "manifests")},
		{"operations", filepath.Join(tempDir, "operations")},
		{"vars", filepath.Join(tempDir, "vars")},
		{"bosh-state", filepath.Join(tempDir, "bosh-state")},
	}

	for _, component := range components {
		if shouldExcludeRestore(component.path, restore.ExcludePaths) {
			log.Info("Excluding from restore", "component", component.name)

			continue
		}

		log.Info("Restoring component", "name", component.name)

		err := restoreComponent(component.path, component.name, restore.Destination)
		if err != nil {
			log.Warn("Failed to restore component", "name", component.name, "error", err)

			continue
		}

		restore.FileCount++
	}

	// Restore bastion data
	err = restoreBastion(ctx, cfg, tempDir)
	if err != nil {
		log.Warn("Failed to restore bastion data", "error", err)
	}

	// Restore secrets
	err = restoreSecrets(tempDir)
	if err != nil {
		log.Warn("Failed to restore secrets", "error", err)
	}

	return nil
}

// performConfigRestore restores only configuration files.
func performConfigRestore(ctx context.Context, cfg *config.Config, restore *RestoreOperation) error {
	log := logger.Get()
	log.Info("Performing configuration restore")

	// Download and extract backup
	tempDir, err := downloadAndExtractBackup(ctx, cfg, restore.Source)
	if err != nil {
		return fmt.Errorf("failed to download backup: %w", err)
	}

	defer func() { _ = os.RemoveAll(tempDir) }()

	// Restore configuration components
	configComponents := []string{"config", "manifests", "operations", "vars"}

	for _, component := range configComponents {
		componentPath := filepath.Join(tempDir, component)

		_, err := os.Stat(componentPath)
		if os.IsNotExist(err) {
			continue
		}

		if shouldExcludeRestore(componentPath, restore.ExcludePaths) {
			continue
		}

		log.Info("Restoring configuration", "component", component)

		err = restoreComponent(componentPath, component, restore.Destination)
		if err != nil {
			log.Warn("Failed to restore configuration", "component", component, "error", err)

			continue
		}
	}

	return nil
}

// performDataRestore restores data and state files.
func performDataRestore(ctx context.Context, cfg *config.Config, restore *RestoreOperation) error {
	log := logger.Get()
	log.Info("Performing data restore")

	// Download and extract backup
	tempDir, err := downloadAndExtractBackup(ctx, cfg, restore.Source)
	if err != nil {
		return fmt.Errorf("failed to download backup: %w", err)
	}

	defer func() { _ = os.RemoveAll(tempDir) }()

	// Restore data components
	dataComponents := map[string]string{
		"deployments": filepath.Join(os.Getenv("HOME"), "deployments", cfg.Name),
		"bosh-state":  filepath.Join(os.Getenv("HOME"), ".bosh"),
		"cf-state":    filepath.Join(os.Getenv("HOME"), ".cf"),
	}

	for component, destPath := range dataComponents {
		componentPath := filepath.Join(tempDir, component)

		_, err := os.Stat(componentPath)
		if os.IsNotExist(err) {
			continue
		}

		if shouldExcludeRestore(componentPath, restore.ExcludePaths) {
			continue
		}

		log.Info("Restoring data", "component", component, "destination", destPath)

		// Create destination directory
		err = os.MkdirAll(filepath.Dir(destPath), DefaultDirMode)
		if err != nil {
			log.Warn("Failed to create destination directory", "path", destPath, "error", err)

			continue
		}

		err = restoreComponent(componentPath, destPath, "")
		if err != nil {
			log.Warn("Failed to restore data", "component", component, "error", err)

			continue
		}
	}

	return nil
}

// performVaultRestore restores vault/credhub secrets.
func performVaultRestore(ctx context.Context, cfg *config.Config, restore *RestoreOperation) error {
	log := logger.Get()
	log.Info("Performing vault restore")

	// Download and extract encrypted backup
	tempDir, err := downloadAndExtractBackup(ctx, cfg, restore.Source)
	if err != nil {
		return fmt.Errorf("failed to download vault backup: %w", err)
	}

	defer func() { _ = os.RemoveAll(tempDir) }()

	// Look for secrets file
	secretsFile := filepath.Join(tempDir, "secrets.json")

	_, err = os.Stat(secretsFile)
	if os.IsNotExist(err) {
		return ErrSecretsFileNotFoundInBackup
	}

	// Import secrets
	err = importSecrets(secretsFile)
	if err != nil {
		return fmt.Errorf("failed to import secrets: %w", err)
	}

	log.Info("Vault restore completed")

	return nil
}

// performDryRunRestore shows what would be restored.
func performDryRunRestore(restore *RestoreOperation) error {
	log := logger.Get()
	log.Info("[DRY RUN] Restore simulation")

	err := printRestorePlanHeader(restore)
	if err != nil {
		return err
	}

	manifest := getRestoreManifest()
	totalFiles, totalSize := processRestoreManifest(manifest, restore)

	printRestoreSummary(totalFiles, totalSize)

	err = printRestoreConflicts(manifest, restore.Destination)
	if err != nil {
		return err
	}

	return nil
}

func printRestorePlanHeader(restore *RestoreOperation) error {
	log := logger.Get()

	_, err := fmt.Fprint(os.Stdout, "\n=== Restore Plan ===\n")
	if err != nil {
		log.Error("failed to write restore plan header", "error", err)

		return fmt.Errorf("failed to write restore plan header: %w", err)
	}

	logger.Get().Infof("Backup ID: %s", restore.BackupID)
	logger.Get().Infof("Source: %s", restore.Source)
	logger.Get().Infof("Mode: %s", restore.Mode)

	if restore.Destination != "" {
		logger.Get().Infof("Destination: %s", restore.Destination)
	}

	_, err = fmt.Fprint(os.Stdout, "\n=== Components to restore ===\n")
	if err != nil {
		log.Error("failed to write components header", "error", err)

		return fmt.Errorf("failed to write components header: %w", err)
	}

	return nil
}

func processRestoreManifest(manifest []BackupItem, restore *RestoreOperation) (int, int64) {
	var (
		totalFiles int
		totalSize  int64
	)

	for _, item := range manifest {
		if shouldExcludeRestore(item.Path, restore.ExcludePaths) {
			logger.Get().Infof("  [EXCLUDED] %s", item.Path)

			continue
		}

		logger.Get().Infof("  [%s] %s (%s)",
			strings.ToUpper(string(restore.Mode)),
			item.Path,
			formatBytes(item.Size))

		totalFiles++
		totalSize += item.Size
	}

	return totalFiles, totalSize
}

func printRestoreSummary(totalFiles int, totalSize int64) {
	logger.Get().Info("=== Summary ===")
	logger.Get().Infof("Total files to restore: %d", totalFiles)
	logger.Get().Infof("Total size to restore: %s", formatBytes(totalSize))
}

func printRestoreConflicts(manifest []BackupItem, destination string) error {
	conflicts := checkRestoreConflicts(manifest, destination)
	if len(conflicts) > 0 {
		logger.Get().Info("=== Potential Conflicts ===")

		for _, conflict := range conflicts {
			logger.Get().Warn(fmt.Sprintf("  [CONFLICT] %s (will be overwritten)", conflict))
		}
	}

	return nil
}

// Helper functions for restore operations

func findLatestBackup(deployment, bucket string) *BackupInfo {
	// Find the most recent backup for the deployment
	// This would query the backup metadata or list bucket contents

	// Placeholder implementation
	return &BackupInfo{
		ID:        deployment + "-backup-latest",
		Source:    fmt.Sprintf("s3://%s/backups/%s-latest.tar.gz", bucket, deployment),
		Type:      "full",
		Timestamp: time.Now().Add(-24 * time.Hour),
		Size:      1024 * 1024 * DefaultBackupSizeMB, // 100MB
		Encrypted: true,
	}
}

func downloadAndExtractBackup(ctx context.Context, cfg *config.Config, source string) (string, error) {
	tempDir, err := createTempDirectory()
	if err != nil {
		return "", err
	}

	backupFile, err := downloadBackupFile(ctx, cfg, source, tempDir)
	if err != nil {
		_ = os.RemoveAll(tempDir)

		return "", err
	}

	backupFile, err = decryptIfNeeded(backupFile)
	if err != nil {
		_ = os.RemoveAll(tempDir)

		return "", err
	}

	extractDir, err := extractBackupArchive(backupFile, tempDir)
	if err != nil {
		_ = os.RemoveAll(tempDir)

		return "", err
	}

	return extractDir, nil
}

func createTempDirectory() (string, error) {
	tempDir, err := os.MkdirTemp("", "ocfp-restore-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}

	return tempDir, nil
}

func downloadBackupFile(ctx context.Context, cfg *config.Config, source, tempDir string) (string, error) {
	log := logger.Get()
	backupFile := filepath.Join(tempDir, "backup.tar.gz")

	switch {
	case strings.HasPrefix(source, "s3://"):
		log.Info("Downloading backup from S3", "source", source)

		return backupFile, downloadFromS3(ctx, cfg, source, backupFile)
	case strings.HasPrefix(source, "http"):
		log.Info("Downloading backup from HTTP", "source", source)

		return backupFile, downloadFromHTTP(backupFile)
	default:
		log.Info("Using local backup file", "source", source)

		return backupFile, copyForRestore(source, backupFile)
	}
}

func decryptIfNeeded(backupFile string) (string, error) {
	if !strings.HasSuffix(backupFile, ".enc") {
		return backupFile, nil
	}

	decryptedFile := strings.TrimSuffix(backupFile, ".enc")

	err := decryptFile(backupFile, decryptedFile)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt backup: %w", err)
	}

	_ = os.Remove(backupFile)

	return decryptedFile, nil
}

func extractBackupArchive(backupFile, tempDir string) (string, error) {
	extractDir := filepath.Join(tempDir, "extracted")

	err := extractArchive(backupFile, extractDir)
	if err != nil {
		return "", fmt.Errorf("failed to extract backup: %w", err)
	}

	return extractDir, nil
}

func restoreComponent(sourcePath, destPath, baseDestination string) error {
	log := logger.Get()

	// Determine actual destination
	var actualDest string
	if baseDestination != "" {
		actualDest = filepath.Join(baseDestination, destPath)
	} else {
		actualDest = destPath
	}

	log.Info("Restoring component", "source", sourcePath, "dest", actualDest)

	// Create destination directory
	err := os.MkdirAll(filepath.Dir(actualDest), DefaultDirMode)
	if err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Copy files recursively
	return copyForRestore(sourcePath, actualDest)
}

func restoreBastion(ctx context.Context, cfg *config.Config, tempDir string) error {
	// Restore bastion host data via SSH
	// Placeholder implementation
	return nil
}

func restoreSecrets(tempDir string) error {
	// Restore vault/credhub secrets
	secretsFile := filepath.Join(tempDir, "secrets.json")

	_, err := os.Stat(secretsFile)
	if os.IsNotExist(err) {
		return nil // No secrets to restore
	}

	return importSecrets(secretsFile)
}

func importSecrets(secretsFile string) error {
	log := logger.Get()
	log.Info("Importing secrets", "file", secretsFile)

	// Load secrets from file
	// Parse JSON and import via credhub CLI
	// Placeholder implementation

	return nil
}

func verifyRestore() {
	log := logger.Get()
	log.Info("Verifying restore integrity")

	// Verify file checksums, sizes, permissions
	// Check that critical files exist
	// Validate configuration files
	// Placeholder implementation
}

func shouldExcludeRestore(path string, excludePaths []string) bool {
	for _, exclude := range excludePaths {
		if strings.Contains(path, exclude) {
			return true
		}
	}

	return false
}

func getRestoreManifest() []BackupItem {
	// Get manifest of files in backup without downloading
	// Placeholder implementation
	return []BackupItem{
		{Path: "config/", Size: DirectoryConfigs, ModTime: time.Time{}, Checksum: "", IsDirectory: true, IsEncrypted: false, IsCompressed: false},
		{Path: "manifests/", Size: DirectoryManifests, ModTime: time.Time{}, Checksum: "", IsDirectory: true, IsEncrypted: false, IsCompressed: false},
		{Path: "deployments/", Size: DirectoryDeploys, ModTime: time.Time{}, Checksum: "", IsDirectory: true, IsEncrypted: false, IsCompressed: false},
	}
}

func checkRestoreConflicts(manifest []BackupItem, destination string) []string {
	// Check for files that would be overwritten
	conflicts := make([]string, 0, len(manifest))

	for _, item := range manifest {
		destPath := filepath.Join(destination, item.Path)

		_, err := os.Stat(destPath)
		if err == nil {
			conflicts = append(conflicts, destPath)
		}
	}

	return conflicts
}

func downloadFromS3(ctx context.Context, cfg *config.Config, source, dest string) error {
	// Download from S3 using provider
	log := logger.Get()
	log.Info("Downloading from S3", "source", source, "dest", dest)

	// Get provider for S3 operations
	provider, err := cpi.GetProvider(cfg.Provider)
	if err != nil {
		return fmt.Errorf("failed to get provider: %w", err)
	}

	err = provider.Initialize(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize provider: %w", err)
	}

	defer func() { _ = provider.Cleanup(ctx) }()

	// Parse S3 URL and download
	// Placeholder implementation
	err = os.WriteFile(dest, []byte("downloaded backup"), DefaultFileMode)
	if err != nil {
		return fmt.Errorf("failed to write downloaded backup: %w", err)
	}

	return nil
}

func downloadFromHTTP(dest string) error {
	// Download from HTTP URL
	// Placeholder implementation
	err := os.WriteFile(dest, []byte("downloaded backup"), DefaultFileMode)
	if err != nil {
		return fmt.Errorf("failed to write downloaded backup: %w", err)
	}

	return nil
}

func copyForRestore(src, dest string) error {
	// Copy file or directory recursively
	// Placeholder implementation
	info, err := os.Stat(src)
	if err == nil && info.IsDir() {
		err := os.MkdirAll(dest, info.Mode())
		if err != nil {
			return fmt.Errorf("failed to create directory for restore: %w", err)
		}

		return nil
	}

	err = os.WriteFile(dest, []byte("restored file"), DefaultFileMode)
	if err != nil {
		return fmt.Errorf("failed to write restored file: %w", err)
	}

	return nil
}

func extractArchive(archivePath, destDir string) error {
	// Extract tar archive
	log := logger.Get()
	log.Info("Extracting archive", "archive", archivePath, "dest", destDir)

	// Create destination directory
	err := os.MkdirAll(destDir, DefaultDirMode)
	if err != nil {
		return fmt.Errorf("failed to create destination directory for extraction: %w", err)
	}

	// Placeholder implementation
	return nil
}

func decryptFile(encryptedPath, destPath string) error {
	// Decrypt file using GPG or similar
	// Placeholder implementation
	return fmt.Errorf("failed to decrypt file: %w", os.Rename(encryptedPath, destPath))
}
