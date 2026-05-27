package vault

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/security"
	"go.uber.org/zap"
)

const (
	// GenesisDirMode is the file permission mode for Genesis environment directories.
	GenesisDirMode = 0750
	// GenesisFileMode is the file permission mode for Genesis environment files.
	GenesisFileMode = 0600
)

// GenesisIntegration handles Genesis environment file updates.
type GenesisIntegration struct {
	config   *config.Config
	blocName string
	logger   *zap.SugaredLogger
}

// NewGenesisIntegration creates a new Genesis integration helper.
func NewGenesisIntegration(cfg *config.Config, blocName string) *GenesisIntegration {
	return &GenesisIntegration{
		config:   cfg,
		blocName: blocName,
		logger:   logger.Get(),
	}
}

// BackupEnvironmentFile creates a backup of an environment file before modification.
func (gi *GenesisIntegration) BackupEnvironmentFile(filePath string) error {
	_, err := os.Stat(filePath)
	if os.IsNotExist(err) {
		return nil // No need to backup if file doesn't exist
	}

	backupPath := filePath + ".bak"

	// Read original file
	err = security.ValidateConfigPath(filePath)
	if err != nil {
		return fmt.Errorf("invalid file path: %w", err)
	}

	data, err := os.ReadFile(filePath) // #nosec G304 - filePath is validated above
	if err != nil {
		return fmt.Errorf("failed to read original file: %w", err)
	}

	// Write backup
	err = os.WriteFile(backupPath, data, GenesisFileMode)
	if err != nil {
		return fmt.Errorf("failed to write backup file: %w", err)
	}

	gi.logger.Debugw("Created backup file", "original", filePath, "backup", backupPath)

	return nil
}

// GetVaultPath returns the vault path for a specific environment.
func (gi *GenesisIntegration) GetVaultPath(envType string) string {
	return fmt.Sprintf("secret/config/%s/%s", gi.blocName, envType)
}

// findGenesisDirectory locates the Genesis environments directory.
func (gi *GenesisIntegration) findGenesisDirectory() (string, error) {
	possiblePaths := []string{
		filepath.Join(os.Getenv("HOME"), "ops", gi.blocName),
		filepath.Join(os.Getenv("HOME"), "genesis", gi.blocName),
		filepath.Join(os.Getenv("HOME"), "workspace", gi.blocName),
		filepath.Join(".", gi.blocName),
		filepath.Join("..", gi.blocName),
		"/opt/genesis/" + gi.blocName,
	}

	for _, path := range possiblePaths {
		if gi.isGenesisDirectory(path) {
			return path, nil
		}
	}

	if envPath := os.Getenv("GENESIS_ENVIRONMENT_PATH"); envPath != "" {
		if gi.isGenesisDirectory(envPath) {
			return envPath, nil
		}
	}

	discovered := gi.discoverGenesisEnvironmentCandidates(context.Background())
	for _, path := range discovered {
		if gi.isGenesisDirectory(path) {
			return path, nil
		}
	}

	return "", ErrGenesisDirectoryNotFound(gi.blocName)
}

// isGenesisDirectory checks if a path contains Genesis environment files.
func (gi *GenesisIntegration) isGenesisDirectory(path string) bool {
	// Check for common Genesis files/directories
	genesisMarkers := []string{
		".genesis",
		"mgmt.yml",
		"ocf.yml",
		gi.blocName + "-mgmt.yml",
		gi.blocName + "-ocf.yml",
	}

	for _, marker := range genesisMarkers {
		_, err := os.Stat(filepath.Join(path, marker)) //nolint:gosec // path components are from trusted config
		if err == nil {
			return true
		}
	}

	return false
}
