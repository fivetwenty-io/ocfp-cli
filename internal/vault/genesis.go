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

// GenesisIntegration handles Genesis environment file updates
type GenesisIntegration struct {
	config   *config.Config
	blocName string
	logger   *zap.SugaredLogger
}

// NewGenesisIntegration creates a new Genesis integration helper
func NewGenesisIntegration(cfg *config.Config, blocName string) *GenesisIntegration {
	return &GenesisIntegration{
		config:   cfg,
		blocName: blocName,
		logger:   logger.Get(),
	}
}

// GenesisEnvironment represents a Genesis environment configuration
type GenesisEnvironment struct {
	Name             string                 `yaml:"name,omitempty"`
	Version          string                 `yaml:"version,omitempty"`
	SecretsProviders []SecretsProvider      `yaml:"secrets_providers,omitempty"`
	Params           map[string]interface{} `yaml:"params,omitempty"`
	Features         []string               `yaml:"features,omitempty"`
	Kit              KitInfo                `yaml:"kit,omitempty"`
}

// SecretsProvider represents a secrets provider configuration
type SecretsProvider struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	URL  string `yaml:"url,omitempty"`
}

// KitInfo represents Genesis kit information
type KitInfo struct {
	Name    string `yaml:"name,omitempty"`
	Version string `yaml:"version,omitempty"`
}

// UpdateEnvironmentSecrets updates Genesis environment files with new vault information
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
		if err := gi.updateEnvironmentFile(genesisDir, envType, vaultURL, vaultToken); err != nil {
			gi.logger.Warn("Failed to update environment file", "env_type", envType, "error", err)
			// Don't fail completely - continue with other environments
		}
	}

	gi.logger.Info("Completed Genesis environment secrets provider updates")
	return nil
}

// findGenesisDirectory locates the Genesis environments directory
func (gi *GenesisIntegration) findGenesisDirectory() (string, error) {
	// Common Genesis directory locations
	possiblePaths := []string{
		filepath.Join(os.Getenv("HOME"), "ops", gi.blocName),
		filepath.Join(os.Getenv("HOME"), "genesis", gi.blocName),
		filepath.Join(os.Getenv("HOME"), "workspace", gi.blocName),
		filepath.Join(".", gi.blocName),
		filepath.Join("..", gi.blocName),
		fmt.Sprintf("/opt/genesis/%s", gi.blocName),
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

	return "", fmt.Errorf("genesis directory not found for bloc %s", gi.blocName)
}

// isGenesisDirectory checks if a path contains Genesis environment files
func (gi *GenesisIntegration) isGenesisDirectory(path string) bool {
	// Check for common Genesis files/directories
	genesisMarkers := []string{
		".genesis",
		"mgmt.yml",
		"ocf.yml",
		fmt.Sprintf("%s-mgmt.yml", gi.blocName),
		fmt.Sprintf("%s-ocf.yml", gi.blocName),
	}

	for _, marker := range genesisMarkers {
		if _, err := os.Stat(filepath.Join(path, marker)); err == nil {
			return true
		}
	}

	return false
}

// updateEnvironmentFile updates a specific environment file
func (gi *GenesisIntegration) updateEnvironmentFile(genesisDir, envType, vaultURL, vaultToken string) error {
	// Determine environment file name
	envFileName := gi.getEnvironmentFileName(envType)
	envFilePath := filepath.Join(genesisDir, envFileName)

	gi.logger.Debug("Updating environment file", "file", envFilePath)

	// Check if file exists
	if _, err := os.Stat(envFilePath); os.IsNotExist(err) {
		gi.logger.Info("Environment file does not exist, creating", "file", envFilePath)
		return gi.createEnvironmentFile(envFilePath, envType, vaultURL, vaultToken)
	}

	// Read existing file
	env, err := gi.readEnvironmentFile(envFilePath)
	if err != nil {
		return fmt.Errorf("failed to read environment file %s: %w", envFilePath, err)
	}

	// Update secrets providers
	env.SecretsProviders = gi.createSecretsProviders(vaultURL, vaultToken)

	// Write updated file
	if err := gi.writeEnvironmentFile(envFilePath, env); err != nil {
		return fmt.Errorf("failed to write environment file %s: %w", envFilePath, err)
	}

	gi.logger.Info("Updated environment file", "file", envFilePath)
	return nil
}

// getEnvironmentFileName determines the environment file name
func (gi *GenesisIntegration) getEnvironmentFileName(envType string) string {
	// Try bloc-specific name first
	blocSpecific := fmt.Sprintf("%s-%s.yml", gi.blocName, envType)

	// Common patterns
	patterns := []string{
		blocSpecific,
		fmt.Sprintf("%s.yml", envType),
		fmt.Sprintf("%s-environment.yml", envType),
	}

	return patterns[0] // Return the bloc-specific name as default
}

// readEnvironmentFile reads a Genesis environment file
func (gi *GenesisIntegration) readEnvironmentFile(filePath string) (*GenesisEnvironment, error) {
	if err := security.ValidateConfigPath(filePath); err != nil {
		return nil, fmt.Errorf("invalid file path: %w", err)
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	var env GenesisEnvironment
	if err := yaml.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	return &env, nil
}

// createEnvironmentFile creates a new Genesis environment file
func (gi *GenesisIntegration) createEnvironmentFile(filePath, envType, vaultURL, vaultToken string) error {
	env := &GenesisEnvironment{
		Name:             fmt.Sprintf("%s-%s", gi.blocName, envType),
		SecretsProviders: gi.createSecretsProviders(vaultURL, vaultToken),
		Params:           gi.createDefaultParams(envType),
		Features:         gi.createDefaultFeatures(envType),
		Kit:              gi.createKitInfo(envType),
	}

	return gi.writeEnvironmentFile(filePath, env)
}

// writeEnvironmentFile writes a Genesis environment file
func (gi *GenesisIntegration) writeEnvironmentFile(filePath string, env *GenesisEnvironment) error {
	// Ensure directory exists
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Marshal to YAML
	data, err := yaml.Marshal(env)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %w", err)
	}

	// Write file
	if err := os.WriteFile(filePath, data, 0600); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// createSecretsProviders creates secrets provider configuration
func (gi *GenesisIntegration) createSecretsProviders(vaultURL, vaultToken string) []SecretsProvider {
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
		})
	}

	return providers
}

// hasCredhubConfig checks if Credhub configuration exists
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

// createDefaultParams creates default parameters for environment type
func (gi *GenesisIntegration) createDefaultParams(envType string) map[string]interface{} {
	params := map[string]interface{}{
		"name":       fmt.Sprintf("%s-%s", gi.blocName, envType),
		"vault_path": fmt.Sprintf("secret/config/%s/%s", gi.blocName, envType),
	}

	// Add environment-specific parameters
	switch envType {
	case "mgmt":
		params["bosh_env"] = "mgmt"
		params["features"] = []string{"bosh-dns", "jumpbox"}
	case "ocf":
		params["cf_env"] = "ocf"
		params["features"] = []string{"cf-deployment", "route-registrar"}
	}

	return params
}

// createDefaultFeatures creates default features for environment type
func (gi *GenesisIntegration) createDefaultFeatures(envType string) []string {
	switch envType {
	case "mgmt":
		return []string{"skip-bosh-dns-healthcheck", "bosh-dns"}
	case "ocf":
		return []string{"small-footprint", "cf-deployment"}
	}

	return []string{}
}

// createKitInfo creates kit information for environment type
func (gi *GenesisIntegration) createKitInfo(envType string) KitInfo {
	switch envType {
	case "mgmt":
		return KitInfo{
			Name:    "bosh",
			Version: "latest",
		}
	case "ocf":
		return KitInfo{
			Name:    "cf",
			Version: "latest",
		}
	}

	return KitInfo{}
}

// BackupEnvironmentFile creates a backup of an environment file before modification
func (gi *GenesisIntegration) BackupEnvironmentFile(filePath string) error {
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return nil // No need to backup if file doesn't exist
	}

	backupPath := filePath + ".bak"

	// Read original file
	if err := security.ValidateConfigPath(filePath); err != nil {
		return fmt.Errorf("invalid file path: %w", err)
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read original file: %w", err)
	}

	// Write backup
	if err := os.WriteFile(backupPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write backup file: %w", err)
	}

	gi.logger.Debug("Created backup file", "original", filePath, "backup", backupPath)
	return nil
}

// ValidateEnvironmentFile validates Genesis environment file format
func (gi *GenesisIntegration) ValidateEnvironmentFile(filePath string) error {
	env, err := gi.readEnvironmentFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read environment file: %w", err)
	}

	// Basic validation
	if env.Name == "" {
		return fmt.Errorf("environment name is required")
	}

	if len(env.SecretsProviders) == 0 {
		return fmt.Errorf("at least one secrets provider is required")
	}

	// Validate secrets providers
	for _, provider := range env.SecretsProviders {
		if provider.Name == "" {
			return fmt.Errorf("secrets provider name is required")
		}
		if provider.Type == "" {
			return fmt.Errorf("secrets provider type is required")
		}
		if provider.Type == "vault" && provider.URL == "" {
			return fmt.Errorf("vault secrets provider requires URL")
		}
	}

	gi.logger.Debug("Environment file validation passed", "file", filePath)
	return nil
}

// GetVaultPath returns the vault path for a specific environment
func (gi *GenesisIntegration) GetVaultPath(envType string) string {
	return fmt.Sprintf("secret/config/%s/%s", gi.blocName, envType)
}
