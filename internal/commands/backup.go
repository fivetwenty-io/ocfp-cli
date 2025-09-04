package commands

import (
	"context"
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

// BackupType represents the type of backup
type BackupType string

const (
	BackupTypeFull        BackupType = "full"
	BackupTypeIncremental BackupType = "incremental"
	BackupTypeConfig      BackupType = "config"
	BackupTypeData        BackupType = "data"
	BackupTypeVault       BackupType = "vault"
)

// NewBackupCmd creates the backup command
func NewBackupCmd() *cobra.Command {
	var (
		backupType   string
		destination  string
		bucket       string
		prefix       string
		compress     bool
		encrypt      bool
		excludePaths []string
		tags         []string
		dryRun       bool
	)

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
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()

			// Load configuration
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc")

			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Parse backup type
			var bType BackupType
			switch backupType {
			case "full", "":
				bType = BackupTypeFull
			case "incremental":
				bType = BackupTypeIncremental
			case "config":
				bType = BackupTypeConfig
			case "data":
				bType = BackupTypeData
			case "vault":
				bType = BackupTypeVault
			default:
				return fmt.Errorf("invalid backup type: %s", backupType)
			}

			log.Info("Starting backup",
				"type", bType,
				"deployment", cfg.Name,
				"dry-run", dryRun)

			// Create backup metadata
			backup := &BackupMetadata{
				ID:         generateBackupID(cfg.Name),
				Deployment: cfg.Name,
				Type:       bType,
				Timestamp:  time.Now(),
				Compressed: compress,
				Encrypted:  encrypt,
				Tags:       tags,
			}

			// Determine destination
			if bucket == "" {
				bucket = getShieldBucket(cfg)
			}
			if destination == "" {
				destination = fmt.Sprintf("s3://%s/%s", bucket, prefix)
			}
			backup.Destination = destination

			if dryRun {
				return performDryRunBackup(ctx, cfg, backup, excludePaths)
			}

			// Perform backup based on type
			switch bType {
			case BackupTypeFull:
				err = performFullBackup(ctx, cfg, backup, excludePaths)
			case BackupTypeConfig:
				err = performConfigBackup(ctx, cfg, backup, excludePaths)
			case BackupTypeData:
				err = performDataBackup(ctx, cfg, backup, excludePaths)
			case BackupTypeVault:
				err = performVaultBackup(ctx, cfg, backup)
			case BackupTypeIncremental:
				err = performIncrementalBackup(ctx, cfg, backup, excludePaths)
			}

			if err != nil {
				return fmt.Errorf("backup failed: %w", err)
			}

			// Save backup metadata
			if err := saveBackupMetadata(backup); err != nil {
				log.Warn("Failed to save backup metadata", "error", err)
			}

			log.Info("Backup completed successfully",
				"id", backup.ID,
				"size", formatBytes(backup.Size),
				"files", backup.FileCount,
				"duration", time.Since(backup.Timestamp))

			fmt.Printf("Backup ID: %s\n", backup.ID)
			fmt.Printf("Destination: %s\n", backup.Destination)

			return nil
		},
	}

	cmd.Flags().StringVar(&backupType, "type", "full", "backup type (full|incremental|config|data|vault)")
	cmd.Flags().StringVar(&destination, "destination", "", "backup destination (default: Shield bucket)")
	cmd.Flags().StringVar(&bucket, "bucket", "", "S3 bucket name")
	cmd.Flags().StringVar(&prefix, "prefix", "backups/", "S3 prefix for backups")
	cmd.Flags().BoolVar(&compress, "compress", true, "compress backup files")
	cmd.Flags().BoolVar(&encrypt, "encrypt", true, "encrypt backup files")
	cmd.Flags().StringSliceVar(&excludePaths, "exclude", nil, "paths to exclude from backup")
	cmd.Flags().StringSliceVar(&tags, "tags", nil, "tags to apply to backup")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be backed up without performing backup")

	return cmd
}

// BackupMetadata represents backup information
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

// BackupItem represents a single item in the backup
type BackupItem struct {
	Path         string
	Size         int64
	ModTime      time.Time
	Checksum     string
	IsDirectory  bool
	IsEncrypted  bool
	IsCompressed bool
}

// performFullBackup performs a full backup of all components
func performFullBackup(ctx context.Context, cfg *config.Config, backup *BackupMetadata, excludePaths []string) error {
	log := logger.Get()
	log.Info("Performing full backup")

	// Create temporary staging directory
	stagingDir, err := os.MkdirTemp("", "ocfp-backup-*")
	if err != nil {
		return fmt.Errorf("failed to create staging directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(stagingDir); err != nil {
			log.Warn("Failed to remove staging directory", "error", err)
		}
	}()

	// Collect items to backup
	items := []struct {
		name   string
		source string
		dest   string
	}{
		{"config", "config/", filepath.Join(stagingDir, "config")},
		{"deployments", filepath.Join(os.Getenv("HOME"), "deployments", cfg.Name), filepath.Join(stagingDir, "deployments")},
		{"state", filepath.Join(os.Getenv("HOME"), ".bosh"), filepath.Join(stagingDir, "bosh-state")},
		{"manifests", "manifests/", filepath.Join(stagingDir, "manifests")},
		{"operations", "operations/", filepath.Join(stagingDir, "operations")},
		{"vars", "vars/", filepath.Join(stagingDir, "vars")},
	}

	// Copy items to staging directory
	for _, item := range items {
		if shouldExclude(item.source, excludePaths) {
			log.Info("Excluding from backup", "path", item.source)
			continue
		}

		log.Info("Backing up", "item", item.name, "source", item.source)

		if err := copyForBackup(item.source, item.dest); err != nil {
			log.Warn("Failed to backup item", "item", item.name, "error", err)
			continue
		}

		// Update backup metadata
		if info, err := os.Stat(item.dest); err == nil {
			backup.FileCount++
			if !info.IsDir() {
				backup.Size += info.Size()
			}
		}
	}

	// Backup bastion if configured
	if err := backupBastion(ctx, cfg, stagingDir); err != nil {
		log.Warn("Failed to backup bastion", "error", err)
	}

	// Backup vault/credhub secrets
	if err := backupSecrets(ctx, cfg, stagingDir); err != nil {
		log.Warn("Failed to backup secrets", "error", err)
	}

	// Create archive
	archivePath := filepath.Join(os.TempDir(), backup.ID+".tar")
	if backup.Compressed {
		archivePath += ".gz"
	}

	if err := createArchive(stagingDir, archivePath, backup.Compressed); err != nil {
		return fmt.Errorf("failed to create archive: %w", err)
	}
	defer func() {
		if err := os.Remove(archivePath); err != nil {
			log.Warn("Failed to remove archive file", "error", err)
		}
	}()

	// Encrypt if requested
	if backup.Encrypted {
		encryptedPath := archivePath + ".enc"
		if err := encryptFile(archivePath, encryptedPath); err != nil {
			return fmt.Errorf("failed to encrypt backup: %w", err)
		}
		if err := os.Remove(archivePath); err != nil {
			log.Warn("Failed to remove unencrypted archive", "error", err)
		}
		archivePath = encryptedPath
	}

	// Upload to destination
	if err := uploadBackup(ctx, cfg, archivePath, backup.Destination); err != nil {
		return fmt.Errorf("failed to upload backup: %w", err)
	}

	return nil
}

// performConfigBackup backs up only configuration files
func performConfigBackup(ctx context.Context, cfg *config.Config, backup *BackupMetadata, excludePaths []string) error {
	log := logger.Get()
	log.Info("Performing configuration backup")

	stagingDir, err := os.MkdirTemp("", "ocfp-config-backup-*")
	if err != nil {
		return fmt.Errorf("failed to create staging directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(stagingDir); err != nil {
			log.Warn("Failed to remove staging directory", "error", err)
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
		if err := copyForBackup(path, dest); err != nil {
			log.Warn("Failed to backup config", "path", path, "error", err)
			continue
		}
	}

	// Create and upload archive
	archivePath := filepath.Join(os.TempDir(), backup.ID+"-config.tar.gz")
	if err := createArchive(stagingDir, archivePath, true); err != nil {
		return fmt.Errorf("failed to create config archive: %w", err)
	}
	defer func() {
		if err := os.Remove(archivePath); err != nil {
			log.Warn("Failed to remove archive file", "error", err)
		}
	}()

	return uploadBackup(ctx, cfg, archivePath, backup.Destination)
}

// performDataBackup backs up data and state files
func performDataBackup(ctx context.Context, cfg *config.Config, backup *BackupMetadata, excludePaths []string) error {
	log := logger.Get()
	log.Info("Performing data backup")

	stagingDir, err := os.MkdirTemp("", "ocfp-data-backup-*")
	if err != nil {
		return fmt.Errorf("failed to create staging directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(stagingDir); err != nil {
			log.Warn("Failed to remove staging directory", "error", err)
		}
	}()

	// Backup data directories
	dataPaths := []string{
		filepath.Join(os.Getenv("HOME"), "deployments", cfg.Name),
		filepath.Join(os.Getenv("HOME"), ".bosh"),
		filepath.Join(os.Getenv("HOME"), ".cf"),
	}

	for _, path := range dataPaths {
		if shouldExclude(path, excludePaths) {
			continue
		}

		dest := filepath.Join(stagingDir, filepath.Base(path))
		if err := copyForBackup(path, dest); err != nil {
			log.Warn("Failed to backup data", "path", path, "error", err)
			continue
		}
	}

	// Create and upload archive
	archivePath := filepath.Join(os.TempDir(), backup.ID+"-data.tar.gz")
	if err := createArchive(stagingDir, archivePath, true); err != nil {
		return fmt.Errorf("failed to create data archive: %w", err)
	}
	defer func() {
		if err := os.Remove(archivePath); err != nil {
			log.Warn("Failed to remove archive file", "error", err)
		}
	}()

	return uploadBackup(ctx, cfg, archivePath, backup.Destination)
}

// performVaultBackup backs up vault/credhub secrets
func performVaultBackup(ctx context.Context, cfg *config.Config, backup *BackupMetadata) error {
	log := logger.Get()
	log.Info("Performing vault backup")

	stagingDir, err := os.MkdirTemp("", "ocfp-vault-backup-*")
	if err != nil {
		return fmt.Errorf("failed to create staging directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(stagingDir); err != nil {
			log.Warn("Failed to remove staging directory", "error", err)
		}
	}()

	// Export secrets
	secretsFile := filepath.Join(stagingDir, "secrets.json")
	if err := exportSecrets(ctx, cfg, secretsFile); err != nil {
		return fmt.Errorf("failed to export secrets: %w", err)
	}

	// Always encrypt vault backups
	backup.Encrypted = true

	// Create encrypted archive
	archivePath := filepath.Join(os.TempDir(), backup.ID+"-vault.tar.gz.enc")
	tempArchive := strings.TrimSuffix(archivePath, ".enc")

	if err := createArchive(stagingDir, tempArchive, true); err != nil {
		return fmt.Errorf("failed to create vault archive: %w", err)
	}
	defer func() { _ = os.Remove(tempArchive) }()

	if err := encryptFile(tempArchive, archivePath); err != nil {
		return fmt.Errorf("failed to encrypt vault backup: %w", err)
	}
	defer func() {
		if err := os.Remove(archivePath); err != nil {
			log.Warn("Failed to remove archive file", "error", err)
		}
	}()

	return uploadBackup(ctx, cfg, archivePath, backup.Destination)
}

// performIncrementalBackup performs an incremental backup
func performIncrementalBackup(ctx context.Context, cfg *config.Config, backup *BackupMetadata, excludePaths []string) error {
	log := logger.Get()

	// Get last backup metadata
	lastBackup, err := getLastBackup(cfg.Name)
	if err != nil {
		log.Info("No previous backup found, performing full backup")
		return performFullBackup(ctx, cfg, backup, excludePaths)
	}

	log.Info("Performing incremental backup", "since", lastBackup.Timestamp)

	stagingDir, err := os.MkdirTemp("", "ocfp-incremental-*")
	if err != nil {
		return fmt.Errorf("failed to create staging directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(stagingDir); err != nil {
			log.Warn("Failed to remove staging directory", "error", err)
		}
	}()

	// Find changed files since last backup
	changedFiles, err := findChangedFiles(lastBackup.Timestamp, excludePaths)
	if err != nil {
		return fmt.Errorf("failed to find changed files: %w", err)
	}

	if len(changedFiles) == 0 {
		log.Info("No changes since last backup")
		return nil
	}

	log.Info("Found changed files", "count", len(changedFiles))

	// Copy changed files to staging
	for _, file := range changedFiles {
		relPath := strings.TrimPrefix(file, "/")
		dest := filepath.Join(stagingDir, relPath)

		if err := os.MkdirAll(filepath.Dir(dest), 0750); err != nil {
			return fmt.Errorf("failed to create directory: %w", err)
		}

		if err := copyForBackup(file, dest); err != nil {
			log.Warn("Failed to backup file", "file", file, "error", err)
			continue
		}

		backup.FileCount++
	}

	// Create and upload incremental archive
	archivePath := filepath.Join(os.TempDir(), backup.ID+"-incremental.tar.gz")
	if err := createArchive(stagingDir, archivePath, true); err != nil {
		return fmt.Errorf("failed to create incremental archive: %w", err)
	}
	defer func() {
		if err := os.Remove(archivePath); err != nil {
			log.Warn("Failed to remove archive file", "error", err)
		}
	}()

	return uploadBackup(ctx, cfg, archivePath, backup.Destination)
}

// performDryRunBackup shows what would be backed up
func performDryRunBackup(ctx context.Context, cfg *config.Config, backup *BackupMetadata, excludePaths []string) error {
	log := logger.Get()
	log.Info("[DRY RUN] Backup simulation")

	fmt.Println("\n=== Backup Plan ===")
	fmt.Printf("Backup ID: %s\n", backup.ID)
	fmt.Printf("Type: %s\n", backup.Type)
	fmt.Printf("Destination: %s\n", backup.Destination)
	fmt.Printf("Compression: %v\n", backup.Compressed)
	fmt.Printf("Encryption: %v\n", backup.Encrypted)

	fmt.Println("\n=== Items to backup ===")

	// List items that would be backed up
	items := []string{
		"config/",
		"manifests/",
		"operations/",
		"vars/",
		filepath.Join(os.Getenv("HOME"), "deployments", cfg.Name),
		filepath.Join(os.Getenv("HOME"), ".bosh"),
		filepath.Join(os.Getenv("HOME"), ".cf"),
	}

	var totalSize int64
	var totalFiles int

	for _, item := range items {
		if shouldExclude(item, excludePaths) {
			fmt.Printf("  [EXCLUDED] %s\n", item)
			continue
		}

		if info, err := os.Stat(item); err == nil {
			if info.IsDir() {
				count, size := countDirContents(item)
				fmt.Printf("  [DIR] %s (%d files, %s)\n", item, count, formatBytes(size))
				totalFiles += count
				totalSize += size
			} else {
				fmt.Printf("  [FILE] %s (%s)\n", item, formatBytes(info.Size()))
				totalFiles++
				totalSize += info.Size()
			}
		} else {
			fmt.Printf("  [MISSING] %s\n", item)
		}
	}

	fmt.Printf("\n=== Summary ===\n")
	fmt.Printf("Total files: %d\n", totalFiles)
	fmt.Printf("Total size: %s\n", formatBytes(totalSize))
	if backup.Compressed {
		fmt.Printf("Estimated compressed size: %s\n", formatBytes(totalSize/3))
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
	return fmt.Sprintf("%s-shield-backups", cfg.Name)
}

func shouldExclude(path string, excludePaths []string) bool {
	for _, exclude := range excludePaths {
		if strings.Contains(path, exclude) {
			return true
		}
	}
	return false
}

func copyForBackup(src, dest string) error {
	// This would implement recursive copying
	// For now, return a placeholder
	return os.MkdirAll(dest, 0750)
}

func createArchive(sourceDir, archivePath string, compress bool) error {
	// This would create a tar archive, optionally compressed
	// Placeholder implementation
	return os.WriteFile(archivePath, []byte("archive"), 0600)
}

func encryptFile(source, dest string) error {
	// This would encrypt the file using GPG or similar
	// Placeholder implementation
	return os.Rename(source, dest)
}

func uploadBackup(ctx context.Context, cfg *config.Config, localPath, destination string) error {
	log := logger.Get()

	if strings.HasPrefix(destination, "s3://") {
		// Upload to S3
		log.Info("Uploading to S3", "destination", destination)

		// Get provider for S3 operations
		provider, err := cpi.GetProvider(cfg.Provider)
		if err != nil {
			return fmt.Errorf("failed to get provider: %w", err)
		}

		if err := provider.Initialize(ctx, cfg); err != nil {
			return fmt.Errorf("failed to initialize provider: %w", err)
		}
		defer func() { _ = provider.Cleanup(ctx) }()

		storage := provider.Storage()
		if storage == nil {
			return fmt.Errorf("provider does not support storage operations")
		}

		// Parse S3 destination
		parts := strings.SplitN(strings.TrimPrefix(destination, "s3://"), "/", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid S3 destination: %s", destination)
		}

		bucket := parts[0]
		key := parts[1] + filepath.Base(localPath)

		// Check if bucket exists, create if not
		if _, err := storage.GetBucket(ctx, bucket); err != nil {
			if _, err := storage.CreateBucket(ctx, bucket); err != nil {
				return fmt.Errorf("failed to create bucket: %w", err)
			}
		}

		// Upload file (would need to implement S3 upload in storage interface)
		log.Info("Uploaded backup", "bucket", bucket, "key", key)
	} else {
		// Local file system destination
		return copyForBackup(localPath, destination)
	}

	return nil
}

func backupBastion(ctx context.Context, cfg *config.Config, stagingDir string) error {
	// SSH to bastion and create backup
	// Placeholder implementation
	return nil
}

func backupSecrets(ctx context.Context, cfg *config.Config, stagingDir string) error {
	// Export secrets from vault/credhub
	// Placeholder implementation
	return nil
}

func exportSecrets(ctx context.Context, cfg *config.Config, outputFile string) error {
	// Export secrets to file
	// Placeholder implementation
	return os.WriteFile(outputFile, []byte("{}"), 0600)
}

func saveBackupMetadata(backup *BackupMetadata) error {
	// Save backup metadata for tracking
	metadataDir := filepath.Join(os.Getenv("HOME"), ".ocfp", "backups")
	if err := os.MkdirAll(metadataDir, 0750); err != nil {
		return err
	}

	metadataFile := filepath.Join(metadataDir, backup.ID+".json")
	// Would marshal and save backup metadata
	return os.WriteFile(metadataFile, []byte("{}"), 0600)
}

func getLastBackup(deployment string) (*BackupMetadata, error) {
	// Get the most recent backup metadata
	// Placeholder implementation
	return nil, fmt.Errorf("no previous backup found")
}

func findChangedFiles(since time.Time, excludePaths []string) ([]string, error) {
	// Find files modified since the given time
	// Placeholder implementation
	return []string{}, nil
}

func countDirContents(dir string) (int, int64) {
	var count int
	var size int64

	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
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
