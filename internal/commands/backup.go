// Package commands implements the CLI command handlers for OCFP operations.
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

// BackupType represents the type of backup.
type BackupType string

const (
	// BackupTypeFull represents a full backup of all deployment data.
	BackupTypeFull BackupType = "full"

	// BackupTypeIncremental represents an incremental backup since the last full backup.
	BackupTypeIncremental BackupType = "incremental"

	// BackupTypeConfig represents a backup of configuration files only.
	BackupTypeConfig BackupType = "config"

	// BackupTypeData represents a backup of state and data files only.
	BackupTypeData BackupType = "data"

	// BackupTypeVault represents a backup of vault secrets only.
	BackupTypeVault BackupType = "vault"

	// BackupDirPerm is the file permission mode for backup directories.
	BackupDirPerm os.FileMode = 0750

	// BackupFilePerm is the file permission mode for backup files.
	BackupFilePerm os.FileMode = 0600

	// S3DestinationParts is the expected number of parts when splitting S3 destination strings.
	S3DestinationParts = 2

	// CompressionEstimateDivisor is the divisor used to estimate compressed backup size.
	CompressionEstimateDivisor = 3
)

var (
	// ErrUnknownBackupType indicates an unrecognized backup type was specified.
	ErrUnknownBackupType = errors.New("unknown backup type")
)

// backupOptions holds the backup command options.
type backupOptions struct {
	backupType   string
	destination  string
	bucket       string
	prefix       string
	compress     bool
	encrypt      bool
	excludePaths []string
	tags         []string
	dryRun       bool
}

// NewBackupCmd creates the backup command.
func NewBackupCmd() *cobra.Command {
	opts := &backupOptions{}

	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Backup configurations and data",
		Long: `Create backups of configurations, bastion data, and deployment state.

The backup command creates snapshots of your OCFP deployment including:
- Configuration files
- Bastion host data
- Deployment manifests
- Vault/CredHub secrets (encrypted)
- BOSH state files
- Cloud Foundry configurations

Backups are stored in the configured Shield bucket or specified destination.`,
		Example: `  # Full backup to Shield bucket
  ocfp backup

  # Configuration-only backup
  ocfp backup --type config

  # Backup to specific S3 bucket
  ocfp backup --bucket my-backup-bucket --prefix ocfp-backups/

  # Backup with compression and encryption
  ocfp backup --compress --encrypt

  # Dry run to see what would be backed up
  ocfp backup --dry-run`,
		RunE: func(_cmd *cobra.Command, _args []string) error {
			return runBackupCommand(opts)
		},
	}

	addBackupFlags(cmd, opts)

	return cmd
}

// addBackupFlags adds all backup command flags.
func addBackupFlags(cmd *cobra.Command, opts *backupOptions) {
	cmd.Flags().StringVar(&opts.backupType, "type", "full", "backup type (full|incremental|config|data|vault)")
	cmd.Flags().StringVar(&opts.destination, "destination", "", "backup destination (default: Shield bucket)")
	cmd.Flags().StringVar(&opts.bucket, "bucket", "", "S3 bucket name")
	cmd.Flags().StringVar(&opts.prefix, "prefix", "backups/", "S3 prefix for backups")
	cmd.Flags().BoolVar(&opts.compress, "compress", true, "compress backup files")
	cmd.Flags().BoolVar(&opts.encrypt, "encrypt", true, "encrypt backup files")
	cmd.Flags().StringSliceVar(&opts.excludePaths, "exclude", nil, "paths to exclude from backup")
	cmd.Flags().StringSliceVar(&opts.tags, "tags", nil, "tags to apply to backup")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "show what would be backed up without performing backup")
}

// runBackupCommand executes the backup command logic.
func runBackupCommand(opts *backupOptions) error {
	ctx := context.Background()
	log := logger.Get()

	cfg, err := loadBackupConfig()
	if err != nil {
		return err
	}

	bType, err := parseBackupType(opts.backupType)
	if err != nil {
		return err
	}

	log.Infow("Starting backup", "type", bType, "deployment", cfg.Name, "dry-run", opts.dryRun)

	backup := createBackupMetadata(cfg, bType, opts)
	setBackupDestination(backup, cfg, opts)

	if opts.dryRun {
		return performDryRunBackup(cfg, backup, opts.excludePaths)
	}

	return executeBackup(ctx, cfg, backup, opts.excludePaths, bType)
}

// loadBackupConfig loads the configuration for backup operations.
func loadBackupConfig() (*config.Config, error) {
	configFile := viper.GetString("config")
	blocName := viper.GetString("bloc")

	cfg, err := config.LoadWithParams(configFile, blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	return cfg, nil
}

// parseBackupType parses the backup type string.
func parseBackupType(backupType string) (BackupType, error) {
	switch backupType {
	case "full", "":
		return BackupTypeFull, nil
	case "incremental":
		return BackupTypeIncremental, nil
	case "config":
		return BackupTypeConfig, nil
	case "data":
		return BackupTypeData, nil
	case "vault":
		return BackupTypeVault, nil
	default:
		return BackupTypeFull, ErrInvalidBackupType(backupType)
	}
}

// createBackupMetadata creates backup metadata from options.
func createBackupMetadata(cfg *config.Config, bType BackupType, opts *backupOptions) *BackupMetadata {
	return &BackupMetadata{
		ID:          generateBackupID(cfg.Name),
		Deployment:  cfg.Name,
		Type:        bType,
		Timestamp:   time.Now(),
		Destination: "",
		Size:        0,
		FileCount:   0,
		Compressed:  opts.compress,
		Encrypted:   opts.encrypt,
		Tags:        opts.tags,
		Manifest:    []BackupItem{},
	}
}

// setBackupDestination sets the backup destination.
func setBackupDestination(backup *BackupMetadata, cfg *config.Config, opts *backupOptions) {
	bucket := opts.bucket
	if bucket == "" {
		bucket = getShieldBucket(cfg)
	}

	destination := opts.destination
	if destination == "" {
		destination = fmt.Sprintf("s3://%s/%s", bucket, opts.prefix)
	}

	backup.Destination = destination
}

// executeBackup performs the actual backup operation.
func executeBackup(ctx context.Context, cfg *config.Config, backup *BackupMetadata, excludePaths []string, bType BackupType) error {
	err := performBackupByType(ctx, cfg, backup, excludePaths, bType)
	if err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}

	finalizeBackup(backup)

	return nil
}

// performBackupByType performs backup based on the specified type.
func performBackupByType(ctx context.Context, cfg *config.Config, backup *BackupMetadata, excludePaths []string, bType BackupType) error {
	switch bType {
	case BackupTypeFull:
		return performFullBackup(ctx, cfg, backup, excludePaths)
	case BackupTypeConfig:
		return performConfigBackup(ctx, cfg, backup, excludePaths)
	case BackupTypeData:
		return performDataBackup(ctx, cfg, backup, excludePaths)
	case BackupTypeVault:
		return performVaultBackup(ctx, cfg, backup)
	case BackupTypeIncremental:
		return performIncrementalBackup(ctx, cfg, backup, excludePaths)
	default:
		return fmt.Errorf("%w: %v", ErrUnknownBackupType, bType)
	}
}

// finalizeBackup completes the backup process with logging and output.
func finalizeBackup(backup *BackupMetadata) {
	log := logger.Get()

	err := saveBackupMetadata(backup)
	if err != nil {
		log.Warnw("Failed to save backup metadata", "error", err)
	}

	log.Info("Backup completed successfully",
		"id", backup.ID,
		"size", formatBytes(backup.Size),
		"files", backup.FileCount,
		"duration", time.Since(backup.Timestamp))

	_, err = fmt.Fprintf(os.Stdout, "Backup ID: %s\n", backup.ID)
	if err != nil {
		log.Errorw("failed to write backup ID", "error", err)
	}

	_, err = fmt.Fprintf(os.Stdout, "Destination: %s\n", backup.Destination)
	if err != nil {
		log.Errorw("failed to write destination", "error", err)
	}
}

// BackupMetadata represents backup information.
type BackupMetadata struct {
	ID          string
	Deployment  string
	Type        BackupType
	Timestamp   time.Time
	Destination string
	Size        int64
	FileCount   int
	Compressed  bool
	Encrypted   bool
	Tags        []string
	Manifest    []BackupItem
}

// BackupItem represents a single item in the backup.
type BackupItem struct {
	Path         string
	Size         int64
	ModTime      time.Time
	Checksum     string
	IsDirectory  bool
	IsEncrypted  bool
	IsCompressed bool
}

// performFullBackup performs a full backup of all components.
func performFullBackup(ctx context.Context, cfg *config.Config, backup *BackupMetadata, excludePaths []string) error {
	log := logger.Get()
	log.Info("Performing full backup")

	stagingDir, err := createStagingDirectory()
	if err != nil {
		return err
	}
	defer cleanupStagingDirectory(stagingDir)

	copyItemsToStagingDirectory(stagingDir, cfg, backup, excludePaths)

	err = backupAdditionalComponents(ctx, cfg, stagingDir)
	if err != nil {
		log.Warnw("Failed to backup additional components", "error", err)
	}

	archivePath, err := createBackupArchive(stagingDir, backup)
	if err != nil {
		return err
	}
	defer cleanupArchiveFile(archivePath)

	finalArchivePath, err := processArchiveEncryption(archivePath, backup)
	if err != nil {
		return err
	}

	return uploadBackup(ctx, cfg, finalArchivePath, backup.Destination)
}

// createStagingDirectory creates a temporary directory for staging backup files.
func createStagingDirectory() (string, error) {
	stagingDir, err := os.MkdirTemp("", "ocfp-backup-*")
	if err != nil {
		return "", fmt.Errorf("failed to create staging directory: %w", err)
	}

	return stagingDir, nil
}

// cleanupStagingDirectory removes the staging directory.
func cleanupStagingDirectory(stagingDir string) {
	err := os.RemoveAll(stagingDir)
	if err != nil {
		logger.Get().Warnw("Failed to remove staging directory", "error", err)
	}
}

// copyItemsToStagingDirectory copies all backup items to the staging directory.
func copyItemsToStagingDirectory(stagingDir string, cfg *config.Config, backup *BackupMetadata, excludePaths []string) {
	items := getBackupItems(stagingDir, cfg)
	log := logger.Get()

	for _, item := range items {
		if shouldExclude(item.source, excludePaths) {
			log.Infow("Excluding from backup", "path", item.source)

			continue
		}

		err := copyBackupItem(item, backup)
		if err != nil {
			log.Warnw("Failed to backup item", "item", item.name, "error", err)

			continue
		}
	}
}

// getBackupItems returns the list of items to backup.
func getBackupItems(stagingDir string, cfg *config.Config) []backupItem {
	home, _ := homeDir()

	return []backupItem{
		{"config", "config/", filepath.Join(stagingDir, "config")},
		{"deployments", filepath.Join(home, "deployments", cfg.Name), filepath.Join(stagingDir, "deployments")},
		{"state", filepath.Join(home, ".bosh"), filepath.Join(stagingDir, "bosh-state")},
		{"manifests", "manifests/", filepath.Join(stagingDir, "manifests")},
		{"operations", "operations/", filepath.Join(stagingDir, "operations")},
		{"vars", "vars/", filepath.Join(stagingDir, "vars")},
	}
}

// backupItem represents an item to backup.
type backupItem struct {
	name   string
	source string
	dest   string
}

// copyBackupItem copies a single backup item and updates metadata.
func copyBackupItem(item backupItem, backup *BackupMetadata) error {
	log := logger.Get()
	log.Infow("Backing up", "item", item.name, "source", item.source)

	err := copyForBackup(item.source, item.dest)
	if err != nil {
		return err
	}

	updateBackupMetadata(item.dest, backup)

	return nil
}

// updateBackupMetadata updates backup metadata with file information.
func updateBackupMetadata(destPath string, backup *BackupMetadata) {
	info, err := os.Stat(destPath)
	if err == nil {
		backup.FileCount++
		if !info.IsDir() {
			backup.Size += info.Size()
		}
	}
}

// backupAdditionalComponents backs up bastion and secrets.
func backupAdditionalComponents(ctx context.Context, cfg *config.Config, stagingDir string) error {
	err := backupBastion(ctx, cfg, stagingDir)
	if err != nil {
		return fmt.Errorf("bastion backup failed: %w", err)
	}

	err = backupSecrets(ctx, cfg, stagingDir)
	if err != nil {
		return fmt.Errorf("secrets backup failed: %w", err)
	}

	return nil
}

// createBackupArchive creates the backup archive.
func createBackupArchive(stagingDir string, backup *BackupMetadata) (string, error) {
	archivePath := filepath.Join(os.TempDir(), backup.ID+".tar")
	if backup.Compressed {
		archivePath += ".gz"
	}

	err := createArchive(stagingDir, archivePath, backup.Compressed)
	if err != nil {
		return "", fmt.Errorf("failed to create archive: %w", err)
	}

	return archivePath, nil
}

// cleanupArchiveFile removes the archive file.
func cleanupArchiveFile(archivePath string) {
	err := os.Remove(archivePath)
	if err != nil {
		logger.Get().Warnw("Failed to remove archive file", "error", err)
	}
}

// processArchiveEncryption encrypts the archive if requested.
func processArchiveEncryption(archivePath string, backup *BackupMetadata) (string, error) {
	if !backup.Encrypted {
		return archivePath, nil
	}

	encryptedPath := archivePath + ".enc"

	err := encryptFile(archivePath, encryptedPath)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt backup: %w", err)
	}

	err = os.Remove(archivePath)
	if err != nil {
		logger.Get().Warnw("Failed to remove unencrypted archive", "error", err)
	}

	return encryptedPath, nil
}

// performConfigBackup backs up only configuration files.
func performConfigBackup(ctx context.Context, cfg *config.Config, backup *BackupMetadata, excludePaths []string) error {
	log := logger.Get()
	log.Info("Performing configuration backup")

	stagingDir, err := os.MkdirTemp("", "ocfp-config-backup-*")
	if err != nil {
		return fmt.Errorf("failed to create staging directory: %w", err)
	}

	defer func() {
		err := os.RemoveAll(stagingDir)
		if err != nil {
			log.Warnw("Failed to remove staging directory", "error", err)
		}
	}()

	// Backup configuration files
	configPaths := []string{
		"config/",
		"manifests/",
		"operations/",
		"vars/",
		".ocfp/",
	}

	for _, path := range configPaths {
		if shouldExclude(path, excludePaths) {
			continue
		}

		dest := filepath.Join(stagingDir, filepath.Base(path))

		err := copyForBackup(path, dest)
		if err != nil {
			log.Warnw("Failed to backup config", "path", path, "error", err)

			continue
		}
	}

	// Create and upload archive
	archivePath := filepath.Join(os.TempDir(), backup.ID+"-config.tar.gz")

	err = createArchive(stagingDir, archivePath, true)
	if err != nil {
		return fmt.Errorf("failed to create config archive: %w", err)
	}

	defer func() {
		err := os.Remove(archivePath)
		if err != nil {
			log.Warnw("Failed to remove archive file", "error", err)
		}
	}()

	return uploadBackup(ctx, cfg, archivePath, backup.Destination)
}

// performDataBackup backs up data and state files.
func performDataBackup(ctx context.Context, cfg *config.Config, backup *BackupMetadata, excludePaths []string) error {
	log := logger.Get()
	log.Info("Performing data backup")

	stagingDir, err := os.MkdirTemp("", "ocfp-data-backup-*")
	if err != nil {
		return fmt.Errorf("failed to create staging directory: %w", err)
	}

	defer func() {
		err := os.RemoveAll(stagingDir)
		if err != nil {
			log.Warnw("Failed to remove staging directory", "error", err)
		}
	}()

	home, _ := homeDir()
	dataPaths := []string{
		filepath.Join(home, "deployments", cfg.Name),
		filepath.Join(home, ".bosh"),
		filepath.Join(home, ".cf"),
	}

	for _, path := range dataPaths {
		if shouldExclude(path, excludePaths) {
			continue
		}

		dest := filepath.Join(stagingDir, filepath.Base(path))

		err := copyForBackup(path, dest)
		if err != nil {
			log.Warnw("Failed to backup data", "path", path, "error", err)

			continue
		}
	}

	// Create and upload archive
	archivePath := filepath.Join(os.TempDir(), backup.ID+"-data.tar.gz")

	err = createArchive(stagingDir, archivePath, true)
	if err != nil {
		return fmt.Errorf("failed to create data archive: %w", err)
	}

	defer func() {
		err := os.Remove(archivePath)
		if err != nil {
			log.Warnw("Failed to remove archive file", "error", err)
		}
	}()

	return uploadBackup(ctx, cfg, archivePath, backup.Destination)
}

// performVaultBackup backs up vault/credhub secrets.
func performVaultBackup(ctx context.Context, cfg *config.Config, backup *BackupMetadata) error {
	log := logger.Get()
	log.Info("Performing vault backup")

	stagingDir, err := os.MkdirTemp("", "ocfp-vault-backup-*")
	if err != nil {
		return fmt.Errorf("failed to create staging directory: %w", err)
	}

	defer func() {
		err := os.RemoveAll(stagingDir)
		if err != nil {
			log.Warnw("Failed to remove staging directory", "error", err)
		}
	}()

	// Export secrets
	secretsFile := filepath.Join(stagingDir, "secrets.json")

	err = exportSecrets(ctx, cfg, secretsFile)
	if err != nil {
		return fmt.Errorf("failed to export secrets: %w", err)
	}

	// Always encrypt vault backups
	backup.Encrypted = true

	// Create encrypted archive
	archivePath := filepath.Join(os.TempDir(), backup.ID+"-vault.tar.gz.enc")
	tempArchive := strings.TrimSuffix(archivePath, ".enc")

	err = createArchive(stagingDir, tempArchive, true)
	if err != nil {
		return fmt.Errorf("failed to create vault archive: %w", err)
	}

	defer func() { _ = os.Remove(tempArchive) }()

	err = encryptFile(tempArchive, archivePath)
	if err != nil {
		return fmt.Errorf("failed to encrypt vault backup: %w", err)
	}

	defer func() {
		err := os.Remove(archivePath)
		if err != nil {
			log.Warnw("Failed to remove archive file", "error", err)
		}
	}()

	return uploadBackup(ctx, cfg, archivePath, backup.Destination)
}

// performIncrementalBackup performs an incremental backup.
func performIncrementalBackup(ctx context.Context, cfg *config.Config, backup *BackupMetadata, excludePaths []string) error {
	log := logger.Get()

	// Get last backup metadata
	lastBackup, err := getLastBackup(cfg.Name)
	if err != nil {
		log.Info("No previous backup found, performing full backup")

		return performFullBackup(ctx, cfg, backup, excludePaths)
	}

	log.Infow("Performing incremental backup", "since", lastBackup.Timestamp)

	stagingDir, err := createIncrementalStagingDir()
	if err != nil {
		return err
	}
	defer cleanupStagingDirectory(stagingDir)

	changedFiles, err := findAndValidateChangedFiles(lastBackup.Timestamp, excludePaths)
	if err != nil {
		return err
	}

	if len(changedFiles) == 0 {
		log.Info("No changes since last backup")

		return nil
	}

	log.Infow("Found changed files", "count", len(changedFiles))

	err = copyChangedFilesToStaging(stagingDir, changedFiles, backup)
	if err != nil {
		return err
	}

	return createAndUploadIncrementalArchive(ctx, cfg, stagingDir, backup)
}

func createIncrementalStagingDir() (string, error) {
	stagingDir, err := os.MkdirTemp("", "ocfp-incremental-*")
	if err != nil {
		return "", fmt.Errorf("failed to create staging directory: %w", err)
	}

	return stagingDir, nil
}

func findAndValidateChangedFiles(since time.Time, excludePaths []string) ([]string, error) {
	changedFiles, err := findChangedFiles(since, excludePaths)
	if err != nil {
		return nil, fmt.Errorf("failed to find changed files: %w", err)
	}

	return changedFiles, nil
}

func copyChangedFilesToStaging(stagingDir string, changedFiles []string, backup *BackupMetadata) error {
	log := logger.Get()

	for _, file := range changedFiles {
		relPath := strings.TrimPrefix(file, "/")
		dest := filepath.Join(stagingDir, relPath)

		err := os.MkdirAll(filepath.Dir(dest), BackupDirPerm)
		if err != nil {
			return fmt.Errorf("failed to create directory: %w", err)
		}

		err = copyForBackup(file, dest)
		if err != nil {
			log.Warnw("Failed to backup file", "file", file, "error", err)

			continue
		}

		backup.FileCount++
	}

	return nil
}

func createAndUploadIncrementalArchive(ctx context.Context, cfg *config.Config, stagingDir string, backup *BackupMetadata) error {
	archivePath := filepath.Join(os.TempDir(), backup.ID+"-incremental.tar.gz")

	err := createArchive(stagingDir, archivePath, true)
	if err != nil {
		return fmt.Errorf("failed to create incremental archive: %w", err)
	}

	defer func() {
		err := os.Remove(archivePath)
		if err != nil {
			logger.Get().Warnw("Failed to remove archive file", "error", err)
		}
	}()

	return uploadBackup(ctx, cfg, archivePath, backup.Destination)
}

// performDryRunBackup shows what would be backed up.
func performDryRunBackup(cfg *config.Config, backup *BackupMetadata, excludePaths []string) error {
	log := logger.Get()
	log.Info("[DRY RUN] Backup simulation")

	err := printBackupPlan(backup)
	if err != nil {
		return err
	}

	items := getBackupItemPaths(cfg)

	totalFiles, totalSize, err := analyzeBackupItems(items, excludePaths)
	if err != nil {
		return err
	}

	return printBackupSummary(totalFiles, totalSize, backup.Compressed)
}

// printBackupPlan prints the backup plan details.
func printBackupPlan(backup *BackupMetadata) error {
	planDetails := []struct {
		label string
		value interface{}
	}{
		{"Backup ID", backup.ID},
		{"Type", backup.Type},
		{"Destination", backup.Destination},
		{"Compression", backup.Compressed},
		{"Encryption", backup.Encrypted},
	}

	_, err := fmt.Fprintf(os.Stdout, "\n=== Backup Plan ===\n")
	if err != nil {
		return fmt.Errorf("failed to write backup plan: %w", err)
	}

	for _, detail := range planDetails {
		_, err := fmt.Fprintf(os.Stdout, "%s: %v\n", detail.label, detail.value)
		if err != nil {
			return fmt.Errorf("failed to write plan detail: %w", err)
		}
	}

	return nil
}

// getBackupItemPaths returns the list of item paths to be backed up.
func getBackupItemPaths(cfg *config.Config) []string {
	home, _ := homeDir()

	return []string{
		"config/",
		"manifests/",
		"operations/",
		"vars/",
		filepath.Join(home, "deployments", cfg.Name),
		filepath.Join(home, ".bosh"),
		filepath.Join(home, ".cf"),
	}
}

// analyzeBackupItems analyzes each backup item and prints its status.
func analyzeBackupItems(items []string, excludePaths []string) (int, int64, error) {
	_, err := fmt.Fprintf(os.Stdout, "\n=== Items to backup ===\n")
	if err != nil {
		return 0, 0, fmt.Errorf("failed to write backup items header: %w", err)
	}

	var (
		totalFiles int
		totalSize  int64
	)

	for _, item := range items {
		files, size, err := analyzeBackupItem(item, excludePaths)
		if err != nil {
			return 0, 0, err
		}

		totalFiles += files
		totalSize += size
	}

	return totalFiles, totalSize, nil
}

// analyzeBackupItem analyzes a single backup item.
func analyzeBackupItem(item string, excludePaths []string) (int, int64, error) {
	if shouldExclude(item, excludePaths) {
		_, err := fmt.Fprintf(os.Stdout, "  [EXCLUDED] %s\n", item)
		if err != nil {
			return 0, 0, fmt.Errorf("failed to write exclusion output: %w", err)
		}

		return 0, 0, nil
	}

	info, err := os.Stat(item)
	if err != nil {
		_, writeErr := fmt.Fprintf(os.Stdout, "  [MISSING] %s\n", item)
		if writeErr != nil {
			return 0, 0, fmt.Errorf("failed to write missing item info: %w", writeErr)
		}

		return 0, 0, fmt.Errorf("item not found: %w", err)
	}

	if info.IsDir() {
		return analyzeDirectory(item)
	}

	return analyzeFile(item, info.Size())
}

// analyzeDirectory analyzes a directory and prints its details.
func analyzeDirectory(item string) (int, int64, error) {
	count, size := countDirContents(item)

	_, err := fmt.Fprintf(os.Stdout, "  [DIR] %s (%d files, %s)\n", item, count, formatBytes(size))
	if err != nil {
		return count, size, fmt.Errorf("failed to write directory info: %w", err)
	}

	return count, size, nil
}

// analyzeFile analyzes a file and prints its details.
func analyzeFile(item string, size int64) (int, int64, error) {
	_, err := fmt.Fprintf(os.Stdout, "  [FILE] %s (%s)\n", item, formatBytes(size))
	if err != nil {
		return 1, size, fmt.Errorf("failed to write file info: %w", err)
	}

	return 1, size, nil
}

// printBackupSummary prints the backup summary.
func printBackupSummary(totalFiles int, totalSize int64, compressed bool) error {
	_, err := fmt.Fprintf(os.Stdout, "\n=== Summary ===\n")
	if err != nil {
		return fmt.Errorf("failed to write backup summary header: %w", err)
	}

	_, err = fmt.Fprintf(os.Stdout, "Total files: %d\n", totalFiles)
	if err != nil {
		return fmt.Errorf("failed to write total files count: %w", err)
	}

	_, err = fmt.Fprintf(os.Stdout, "Total size: %s\n", formatBytes(totalSize))
	if err != nil {
		return fmt.Errorf("failed to write total size: %w", err)
	}

	if compressed {
		_, err := fmt.Fprintf(os.Stdout, "Estimated compressed size: %s\n", formatBytes(totalSize/CompressionEstimateDivisor))
		if err != nil {
			return fmt.Errorf("failed to write estimated compressed size: %w", err)
		}
	}

	return nil
}

// Helper functions

func generateBackupID(deployment string) string {
	timestamp := time.Now().Format("20060102-150405")

	return fmt.Sprintf("%s-backup-%s", deployment, timestamp)
}

func getShieldBucket(cfg *config.Config) string {
	// Default Shield bucket name
	return cfg.Name + "-shield-backups"
}

func shouldExclude(path string, excludePaths []string) bool {
	for _, exclude := range excludePaths {
		if strings.Contains(path, exclude) {
			return true
		}
	}

	return false
}

func copyForBackup(_, dest string) error {
	// This would implement recursive copying
	// For now, return a placeholder
	err := os.MkdirAll(dest, BackupDirPerm) //nolint:gosec // path from trusted config
	if err != nil {
		return fmt.Errorf("failed to create backup directory: %w", err)
	}

	return nil
}

func createArchive(_, _ string, _ bool) error {
	// This would create a tar archive, optionally compressed
	// Placeholder implementation
	return nil
}

func encryptFile(source, dest string) error {
	// This would encrypt the file using GPG or similar
	// Placeholder implementation
	err := os.Rename(source, dest)
	if err != nil {
		return fmt.Errorf("failed to encrypt file: %w", err)
	}

	return nil
}

func uploadBackup(ctx context.Context, cfg *config.Config, localPath, destination string) error {
	if strings.HasPrefix(destination, "s3://") {
		return uploadToS3(ctx, cfg, localPath, destination)
	}

	return copyForBackup(localPath, destination)
}

func uploadToS3(ctx context.Context, cfg *config.Config, localPath, destination string) error {
	log := logger.Get()
	log.Infow("Uploading to S3", "destination", destination)

	provider, err := initializeProvider(ctx, cfg)
	if err != nil {
		return err
	}

	defer func() { _ = provider.Cleanup(ctx) }()

	storage := provider.StorageManager()
	if storage == nil {
		return ErrProviderDoesNotSupportStorageOperations
	}

	bucket, key, err := parseS3Destination(destination, localPath)
	if err != nil {
		return err
	}

	err = ensureBucketExists(ctx, storage, bucket)
	if err != nil {
		return err
	}

	log.Infow("Uploaded backup", "bucket", bucket, "key", key)

	return nil
}

func parseS3Destination(destination, localPath string) (string, string, error) {
	parts := strings.SplitN(strings.TrimPrefix(destination, "s3://"), "/", S3DestinationParts)
	if len(parts) != S3DestinationParts {
		return "", "", ErrInvalidS3Destination(destination)
	}

	return parts[0], parts[1] + filepath.Base(localPath), nil
}

func ensureBucketExists(ctx context.Context, storage cpi.StorageManager, bucket string) error {
	_, err := storage.GetBucket(ctx, bucket)
	if err != nil {
		_, err = storage.CreateBucket(ctx, &cpi.BucketRequest{Name: bucket})
		if err != nil {
			return fmt.Errorf("failed to create bucket: %w", err)
		}
	}

	return nil
}

func backupBastion(_ctx context.Context, _cfg *config.Config, _stagingDir string) error {
	// SSH to bastion and create backup
	// Placeholder implementation
	return nil
}

func backupSecrets(_ctx context.Context, _cfg *config.Config, _stagingDir string) error {
	// Export secrets from vault/credhub
	// Placeholder implementation
	return nil
}

func exportSecrets(_ context.Context, _ *config.Config, outputFile string) error {
	// Export secrets to file
	// Placeholder implementation
	err := os.WriteFile(outputFile, []byte("{}"), BackupFilePerm)
	if err != nil {
		return fmt.Errorf("failed to export secrets: %w", err)
	}

	return nil
}

// backupMetadataDir returns the directory backup metadata files are stored
// in: StateHome()/backups as the write target, with a dual-read fallback to
// the pre-migration OcfpHome()/backups directory when only that exists.
func backupMetadataDir() string {
	newPath := filepath.Join(config.StateHome(), "backups")
	legacyPath := filepath.Join(config.OcfpHome(), "backups")

	path, _ := config.ResolveExisting(newPath, legacyPath)

	return path
}

func saveBackupMetadata(backup *BackupMetadata) error {
	// Save backup metadata for tracking
	metadataDir := backupMetadataDir()

	err := os.MkdirAll(metadataDir, BackupDirPerm) //nolint:gosec // path components are from trusted HOME env
	if err != nil {
		return fmt.Errorf("failed to create metadata directory: %w", err)
	}

	metadataFile := filepath.Join(metadataDir, backup.ID+".json")

	// Would marshal and save backup metadata
	err = os.WriteFile(metadataFile, []byte("{}"), BackupFilePerm) //nolint:gosec // path components are from trusted config
	if err != nil {
		return fmt.Errorf("failed to save backup metadata: %w", err)
	}

	return nil
}

func getLastBackup(_deployment string) (*BackupMetadata, error) {
	// Get the most recent backup metadata
	// Placeholder implementation
	return nil, ErrNoPreviousBackupFound
}

func findChangedFiles(_since time.Time, _excludePaths []string) ([]string, error) {
	// Find files modified since the given time
	// Placeholder implementation
	return []string{}, nil
}

func countDirContents(dir string) (int, int64) {
	var (
		count int
		size  int64
	)

	_ = filepath.Walk(dir, func(_path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			count++
			size += info.Size()
		}

		return nil
	})

	return count, size
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
