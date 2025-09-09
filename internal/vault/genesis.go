package vault

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/security"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

const (
	// File permissions.
	GenesisDirMode  = 0750
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

// GenesisEnvironment represents a Genesis environment configuration.
type GenesisEnvironment struct {
	Name             string                 `yaml:"name,omitempty"`
	Version          string                 `yaml:"version,omitempty"`
	SecretsProviders []SecretsProvider      `yaml:"secretsProviders,omitempty"`
	Params           map[string]interface{} `yaml:"params,omitempty"`
	Features         []string               `yaml:"features,omitempty"`
	Kit              KitInfo                `yaml:"kit,omitempty"`
}

// SecretsProvider represents a secrets provider configuration.
type SecretsProvider struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	URL  string `yaml:"url,omitempty"`
}

// KitInfo represents Genesis kit information.
type KitInfo struct {
	Name    string `yaml:"name,omitempty"`
	Version string `yaml:"version,omitempty"`
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

	gi.logger.Debug("Created backup file", "original", filePath, "backup", backupPath)

	return nil
}

// GetVaultPath returns the vault path for a specific environment.
func (gi *GenesisIntegration) GetVaultPath(envType string) string {
	return fmt.Sprintf("secret/config/%s/%s", gi.blocName, envType)
}

// UpdateEnvironmentSecrets updates Genesis environment files with new vault information.
func (gi *GenesisIntegration) UpdateEnvironmentSecrets(vaultURL, vaultToken string) error {
	gi.logger.Info("Updating Genesis environment secrets providers", "bloc", gi.blocName)

	// Find Genesis environments directory
	genesisDir, err := gi.findGenesisDirectory()
	if err != nil {
		return fmt.Errorf("failed to find Genesis directory: %w", err)
	}

	gi.logger.Debug("Found Genesis directory", "path", genesisDir)

	// Update environment files for each environment type
	environments := []string{"mgmt", "ocf"}
	for _, envType := range environments {
		err := gi.updateEnvironmentFile(genesisDir, envType, vaultURL)
		if err != nil {
			gi.logger.Warn("Failed to update environment file", "env_type", envType, "error", err)
			// Don't fail completely - continue with other environments
		}
	}

	gi.logger.Info("Completed Genesis environment secrets provider updates")

	return nil
}

// ValidateEnvironmentFile validates Genesis environment file format.
func (gi *GenesisIntegration) ValidateEnvironmentFile(filePath string) error {
	env, err := gi.readEnvironmentFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read environment file: %w", err)
	}

	// Basic validation
	if env.Name == "" {
		return ErrEnvironmentNameRequired
	}

	if len(env.SecretsProviders) == 0 {
		return ErrAtLeastOneSecretsProviderRequired
	}

	// Validate secrets providers
	for _, provider := range env.SecretsProviders {
		if provider.Name == "" {
			return ErrSecretsProviderNameRequired
		}

		if provider.Type == "" {
			return ErrSecretsProviderTypeRequired
		}

		if provider.Type == "vault" && provider.URL == "" {
			return ErrVaultSecretsProviderRequiresURL
		}
	}

	gi.logger.Debug("Environment file validation passed", "file", filePath)

	return nil
}

// createDefaultFeatures creates default features for environment type.
func (gi *GenesisIntegration) createDefaultFeatures(envType string) []string {
	switch envType {
	case MgmtEnvType:
		return []string{"skip-bosh-dns-healthcheck", "bosh-dns"}
	case OCFEnvType:
		return []string{"small-footprint", "cf-deployment"}
	}

	return []string{}
}

// createDefaultParams creates default parameters for environment type.
func (gi *GenesisIntegration) createDefaultParams(envType string) map[string]interface{} {
	params := map[string]interface{}{
		"name":       fmt.Sprintf("%s-%s", gi.blocName, envType),
		"vault_path": fmt.Sprintf("secret/config/%s/%s", gi.blocName, envType),
	}

	// Add environment-specific parameters
	switch envType {
	case MgmtEnvType:
		params["bosh_env"] = "mgmt"
		params["features"] = []string{"bosh-dns", "jumpbox"}
	case OCFEnvType:
		params["cf_env"] = "ocf"
		params["features"] = []string{"cf-deployment", "route-registrar"}
	}

	return params
}

// createEnvironmentFile creates a new Genesis environment file.
func (gi *GenesisIntegration) createEnvironmentFile(filePath, envType, vaultURL string) error {
	env := &GenesisEnvironment{
		Name:             fmt.Sprintf("%s-%s", gi.blocName, envType),
		Version:          "",
		SecretsProviders: gi.createSecretsProviders(vaultURL),
		Params:           gi.createDefaultParams(envType),
		Features:         gi.createDefaultFeatures(envType),
		Kit:              gi.createKitInfo(envType),
	}

	return gi.writeEnvironmentFile(filePath, env)
}

// createKitInfo creates kit information for environment type.
func (gi *GenesisIntegration) createKitInfo(envType string) KitInfo {
	switch envType {
	case MgmtEnvType:
		return KitInfo{
			Name:    "bosh",
			Version: "latest",
		}
	case OCFEnvType:
		return KitInfo{
			Name:    "cf",
			Version: "latest",
		}
	}

	return KitInfo{
		Name:    "",
		Version: "",
	}
}

// createSecretsProviders creates secrets provider configuration.
func (gi *GenesisIntegration) createSecretsProviders(vaultURL string) []SecretsProvider {
	providers := []SecretsProvider{
		{
			Name: "vault",
			Type: "vault",
			URL:  vaultURL,
		},
	}

	// Add credhub as secondary provider if configured
	if gi.hasCredhubConfig() {
		providers = append(providers, SecretsProvider{
			Name: "credhub",
			Type: "credhub",
			URL:  "",
		})
	}

	return providers
}

// findGenesisDirectory locates the Genesis environments directory.
func (gi *GenesisIntegration) findGenesisDirectory() (string, error) {
	// Common Genesis directory locations
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

	// Check if GENESIS_ENVIRONMENT_PATH is set
	if envPath := os.Getenv("GENESIS_ENVIRONMENT_PATH"); envPath != "" {
		if gi.isGenesisDirectory(envPath) {
			return envPath, nil
		}
	}

	return "", ErrGenesisDirectoryNotFound(gi.blocName)
}

// getEnvironmentFileName determines the environment file name.
func (gi *GenesisIntegration) getEnvironmentFileName(envType string) string {
	// Try bloc-specific name first
	blocSpecific := fmt.Sprintf("%s-%s.yml", gi.blocName, envType)

	// Common patterns
	patterns := []string{
		blocSpecific,
		envType + ".yml",
		envType + "-environment.yml",
	}

	return patterns[0] // Return the bloc-specific name as default
}

// hasCredhubConfig checks if Credhub configuration exists.
func (gi *GenesisIntegration) hasCredhubConfig() bool {
	// Check for common Credhub environment variables or config
	credhubIndicators := []string{
		"CREDHUB_SERVER",
		"CREDHUB_CLIENT",
		"CREDHUB_SECRET",
	}

	for _, env := range credhubIndicators {
		if os.Getenv(env) != "" {
			return true
		}
	}

	return false
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
		_, err := os.Stat(filepath.Join(path, marker))
		if err == nil {
			return true
		}
	}

	return false
}

// readEnvironmentFile reads a Genesis environment file.
func (gi *GenesisIntegration) readEnvironmentFile(filePath string) (*GenesisEnvironment, error) {
	err := security.ValidateConfigPath(filePath)
	if err != nil {
		return nil, fmt.Errorf("invalid file path: %w", err)
	}

	data, err := os.ReadFile(filePath) // #nosec G304 - filePath is validated above
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	var env GenesisEnvironment

	err = yaml.Unmarshal(data, &env)
	if err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	return &env, nil
}

// updateEnvironmentFile updates a specific environment file.
func (gi *GenesisIntegration) updateEnvironmentFile(genesisDir, envType, vaultURL string) error {
	// Determine environment file name
	envFileName := gi.getEnvironmentFileName(envType)
	envFilePath := filepath.Join(genesisDir, envFileName)

	gi.logger.Debug("Updating environment file", "file", envFilePath)

	// Check if file exists
	_, err := os.Stat(envFilePath)
	if os.IsNotExist(err) {
		gi.logger.Info("Environment file does not exist, creating", "file", envFilePath)

		return gi.createEnvironmentFile(envFilePath, envType, vaultURL)
	}

	// Read existing file
	env, err := gi.readEnvironmentFile(envFilePath)
	if err != nil {
		return fmt.Errorf("failed to read environment file %s: %w", envFilePath, err)
	}

	// Update secrets providers
	env.SecretsProviders = gi.createSecretsProviders(vaultURL)

	// Write updated file
	err = gi.writeEnvironmentFile(envFilePath, env)
	if err != nil {
		return fmt.Errorf("failed to write environment file %s: %w", envFilePath, err)
	}

	gi.logger.Info("Updated environment file", "file", envFilePath)

	return nil
}

// writeEnvironmentFile writes a Genesis environment file.
func (gi *GenesisIntegration) writeEnvironmentFile(filePath string, env *GenesisEnvironment) error {
	// Ensure directory exists
	dir := filepath.Dir(filePath)

	err := os.MkdirAll(dir, GenesisDirMode)
	if err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Marshal to YAML
	data, err := yaml.Marshal(env)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %w", err)
	}

	// Write file
	err = os.WriteFile(filePath, data, GenesisFileMode)
	if err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}
