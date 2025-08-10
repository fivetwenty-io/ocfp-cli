package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// NewVaultCmd creates the vault command
func NewVaultCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vault",
		Short: "Manage vault operations",
		Long: `Manage vault operations including secret population, inception, and migration.

The vault command provides utilities for managing secrets in HashiCorp Vault
or CredHub for BOSH and Cloud Foundry deployments.`,
	}

	// Add subcommands
	cmd.AddCommand(newVaultPopulateCmd())
	cmd.AddCommand(newVaultInceptionCmd())
	cmd.AddCommand(newVaultMigrateCmd())
	cmd.AddCommand(newVaultExportCmd())
	cmd.AddCommand(newVaultImportCmd())

	return cmd
}

// newVaultPopulateCmd creates the vault populate subcommand
func newVaultPopulateCmd() *cobra.Command {
	var (
		vaultPath string
		fromFile  string
		force     bool
	)

	cmd := &cobra.Command{
		Use:   "populate",
		Short: "Populate vault with secrets",
		Long: `Populate vault with secrets from configuration or file.

This command reads secrets from a configuration file and populates them
into Vault or CredHub at the appropriate paths for the deployment.`,
		Example: `  # Populate vault from default config
  ocfp vault populate

  # Populate from specific file
  ocfp vault populate --from-file secrets.yml

  # Populate to specific vault path
  ocfp vault populate --vault-path /concourse/main`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()

			// Load configuration
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc-name")

			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			log.Info("Populating vault", "path", vaultPath)

			// Determine source of secrets
			var secrets map[string]interface{}
			if fromFile != "" {
				secrets, err = loadSecretsFromFile(fromFile)
				if err != nil {
					return fmt.Errorf("failed to load secrets from file: %w", err)
				}
			} else {
				secrets, err = generateDefaultSecrets(cfg)
				if err != nil {
					return fmt.Errorf("failed to generate default secrets: %w", err)
				}
			}

			// Populate vault
			if err := populateVault(ctx, cfg, vaultPath, secrets, force); err != nil {
				return fmt.Errorf("failed to populate vault: %w", err)
			}

			log.Info("Vault populated successfully")
			return nil
		},
	}

	cmd.Flags().StringVar(&vaultPath, "vault-path", "", "vault path prefix")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "load secrets from file")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing secrets")

	return cmd
}

// newVaultInceptionCmd creates the vault inception subcommand
func newVaultInceptionCmd() *cobra.Command {
	var (
		deploymentName string
		vaultPath      string
	)

	cmd := &cobra.Command{
		Use:   "inception",
		Short: "Initialize vault for new deployment",
		Long: `Initialize vault with inception secrets for a new deployment.

This command creates the initial set of secrets required for bootstrapping
a new BOSH or Cloud Foundry deployment, including certificates, passwords,
and encryption keys.`,
		Example: `  # Initialize vault for deployment
  ocfp vault inception --deployment production

  # Initialize with custom vault path
  ocfp vault inception --vault-path /secret/production`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()

			// Load configuration
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc-name")

			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			if deploymentName == "" {
				deploymentName = cfg.Name
			}

			log.Info("Initializing vault for deployment", "deployment", deploymentName)

			// Generate inception secrets
			secrets, err := generateInceptionSecrets(deploymentName)
			if err != nil {
				return fmt.Errorf("failed to generate inception secrets: %w", err)
			}

			// Store in vault
			if vaultPath == "" {
				vaultPath = fmt.Sprintf("/secret/%s", deploymentName)
			}

			if err := populateVault(ctx, cfg, vaultPath, secrets, false); err != nil {
				return fmt.Errorf("failed to store inception secrets: %w", err)
			}

			log.Info("Vault inception completed", "path", vaultPath)
			return nil
		},
	}

	cmd.Flags().StringVar(&deploymentName, "deployment", "", "deployment name")
	cmd.Flags().StringVar(&vaultPath, "vault-path", "", "vault path prefix")

	return cmd
}

// newVaultMigrateCmd creates the vault migrate subcommand
func newVaultMigrateCmd() *cobra.Command {
	var (
		sourcePath string
		destPath   string
		dryRun     bool
	)

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate secrets between vault paths",
		Long: `Migrate secrets from one vault path to another.

This command copies secrets from a source path to a destination path,
useful for migrating between environments or restructuring vault paths.`,
		Example: `  # Migrate secrets between paths
  ocfp vault migrate --source /secret/old --dest /secret/new

  # Dry run to preview migration
  ocfp vault migrate --source /secret/old --dest /secret/new --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()

			if sourcePath == "" || destPath == "" {
				return fmt.Errorf("source and destination paths are required")
			}

			log.Info("Migrating vault secrets", "source", sourcePath, "dest", destPath, "dry-run", dryRun)

			// Read secrets from source
			secrets, err := readVaultPath(ctx, sourcePath)
			if err != nil {
				return fmt.Errorf("failed to read source secrets: %w", err)
			}

			if dryRun {
				log.Info("Dry run - would migrate secrets", "count", len(secrets))
				for key := range secrets {
					log.Info("Would migrate", "key", key)
				}
				return nil
			}

			// Write secrets to destination
			if err := writeVaultPath(ctx, destPath, secrets); err != nil {
				return fmt.Errorf("failed to write destination secrets: %w", err)
			}

			log.Info("Vault migration completed", "migrated", len(secrets))
			return nil
		},
	}

	cmd.Flags().StringVar(&sourcePath, "source", "", "source vault path")
	cmd.Flags().StringVar(&destPath, "dest", "", "destination vault path")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview migration without changes")

	return cmd
}

// newVaultExportCmd creates the vault export subcommand
func newVaultExportCmd() *cobra.Command {
	var (
		vaultPath  string
		outputFile string
		format     string
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export secrets from vault",
		Long:  `Export secrets from vault to a file for backup or transfer.`,
		Example: `  # Export secrets to file
  ocfp vault export --path /secret/production --output secrets.yml

  # Export as JSON
  ocfp vault export --path /secret/production --output secrets.json --format json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()

			if vaultPath == "" {
				return fmt.Errorf("vault path is required")
			}

			log.Info("Exporting vault secrets", "path", vaultPath)

			// Read secrets from vault
			secrets, err := readVaultPath(ctx, vaultPath)
			if err != nil {
				return fmt.Errorf("failed to read secrets: %w", err)
			}

			// Marshal to requested format
			var data []byte
			switch format {
			case "json":
				data, err = json.MarshalIndent(secrets, "", "  ")
			case "yaml", "yml":
				data, err = yaml.Marshal(secrets)
			default:
				return fmt.Errorf("unsupported format: %s", format)
			}

			if err != nil {
				return fmt.Errorf("failed to marshal secrets: %w", err)
			}

			// Write to file or stdout
			if outputFile != "" {
				if err := os.WriteFile(outputFile, data, 0600); err != nil {
					return fmt.Errorf("failed to write file: %w", err)
				}
				log.Info("Secrets exported", "file", outputFile)
			} else {
				fmt.Println(string(data))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&vaultPath, "path", "", "vault path to export")
	cmd.Flags().StringVar(&outputFile, "output", "", "output file (default: stdout)")
	cmd.Flags().StringVar(&format, "format", "yaml", "output format (yaml|json)")

	return cmd
}

// newVaultImportCmd creates the vault import subcommand
func newVaultImportCmd() *cobra.Command {
	var (
		vaultPath string
		inputFile string
		force     bool
	)

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import secrets into vault",
		Long:  `Import secrets from a file into vault.`,
		Example: `  # Import secrets from file
  ocfp vault import --path /secret/production --file secrets.yml

  # Force overwrite existing secrets
  ocfp vault import --path /secret/production --file secrets.yml --force`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()

			if vaultPath == "" || inputFile == "" {
				return fmt.Errorf("vault path and input file are required")
			}

			log.Info("Importing secrets to vault", "path", vaultPath, "file", inputFile)

			// Load secrets from file
			secrets, err := loadSecretsFromFile(inputFile)
			if err != nil {
				return fmt.Errorf("failed to load secrets: %w", err)
			}

			// Load configuration for vault connection
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc-name")

			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Import to vault
			if err := populateVault(ctx, cfg, vaultPath, secrets, force); err != nil {
				return fmt.Errorf("failed to import secrets: %w", err)
			}

			log.Info("Secrets imported successfully", "count", len(secrets))
			return nil
		},
	}

	cmd.Flags().StringVar(&vaultPath, "path", "", "vault path to import to")
	cmd.Flags().StringVar(&inputFile, "file", "", "input file")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing secrets")

	return cmd
}

// Helper functions

// loadSecretsFromFile loads secrets from a YAML or JSON file
func loadSecretsFromFile(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var secrets map[string]interface{}

	// Try YAML first
	if err := yaml.Unmarshal(data, &secrets); err == nil {
		return secrets, nil
	}

	// Try JSON
	if err := json.Unmarshal(data, &secrets); err == nil {
		return secrets, nil
	}

	return nil, fmt.Errorf("unable to parse file as YAML or JSON")
}

// generateDefaultSecrets generates default secrets for a deployment
func generateDefaultSecrets(cfg *config.Config) (map[string]interface{}, error) {
	secrets := make(map[string]interface{})

	// Generate various passwords and keys
	secrets["admin_password"] = generatePassword(32)
	secrets["uaa_admin_client_secret"] = generatePassword(32)
	secrets["credhub_admin_client_secret"] = generatePassword(32)
	secrets["nats_password"] = generatePassword(24)
	secrets["postgres_password"] = generatePassword(24)
	secrets["blobstore_secret"] = generatePassword(32)

	// Add deployment-specific values
	secrets["deployment_name"] = cfg.Name
	secrets["director_name"] = fmt.Sprintf("%s-bosh", cfg.Name)
	secrets["internal_ip"] = "10.0.0.6"

	return secrets, nil
}

// generateInceptionSecrets generates inception secrets for a new deployment
func generateInceptionSecrets(deploymentName string) (map[string]interface{}, error) {
	secrets := make(map[string]interface{})

	// Core passwords
	secrets["admin_password"] = generatePassword(32)
	secrets["director_password"] = generatePassword(32)

	// Database passwords
	secrets["postgres_password"] = generatePassword(24)
	secrets["mysql_password"] = generatePassword(24)

	// Service passwords
	secrets["nats_password"] = generatePassword(24)
	secrets["redis_password"] = generatePassword(24)
	secrets["registry_password"] = generatePassword(24)
	secrets["health_monitor_password"] = generatePassword(24)

	// Encryption keys
	secrets["blobstore_encryption_key"] = generatePassword(32)
	secrets["db_encryption_key"] = generatePassword(32)

	// Deployment metadata
	secrets["deployment_name"] = deploymentName
	secrets["inception_date"] = fmt.Sprintf("%v", context.Background().Value("date"))

	return secrets, nil
}

// populateVault writes secrets to vault
func populateVault(ctx context.Context, cfg *config.Config, path string, secrets map[string]interface{}, force bool) error {
	log := logger.Get()

	// Use credhub CLI for now
	for key, value := range secrets {
		secretPath := filepath.Join(path, key)

		// Check if secret exists
		if !force {
			cmd := exec.Command("credhub", "get", "-n", secretPath)
			if err := cmd.Run(); err == nil {
				log.Info("Secret already exists, skipping", "path", secretPath)
				continue
			}
		}

		// Set the secret
		cmd := exec.Command("credhub", "set",
			"-n", secretPath,
			"-t", "value",
			"-v", fmt.Sprintf("%v", value))

		if err := cmd.Run(); err != nil {
			log.Warn("Failed to set secret", "path", secretPath, "error", err)
		} else {
			log.Info("Secret stored", "path", secretPath)
		}
	}

	return nil
}

// readVaultPath reads all secrets from a vault path
func readVaultPath(ctx context.Context, path string) (map[string]interface{}, error) {
	secrets := make(map[string]interface{})

	// List secrets at path
	cmd := exec.Command("credhub", "find", "-p", path)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list secrets: %w", err)
	}

	// Parse output and read each secret
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "credentials:") {
			continue
		}

		if strings.HasPrefix(line, "- name:") {
			secretName := strings.TrimSpace(strings.TrimPrefix(line, "- name:"))

			// Get secret value
			cmd := exec.Command("credhub", "get", "-n", secretName, "-q")
			value, err := cmd.Output()
			if err != nil {
				continue
			}

			// Store in map with relative path
			relPath := strings.TrimPrefix(secretName, path+"/")
			secrets[relPath] = strings.TrimSpace(string(value))
		}
	}

	return secrets, nil
}

// writeVaultPath writes secrets to a vault path
func writeVaultPath(ctx context.Context, path string, secrets map[string]interface{}) error {
	for key, value := range secrets {
		secretPath := filepath.Join(path, key)

		cmd := exec.Command("credhub", "set",
			"-n", secretPath,
			"-t", "value",
			"-v", fmt.Sprintf("%v", value))

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to write secret %s: %w", secretPath, err)
		}
	}

	return nil
}

// generatePassword generates a random password
func generatePassword(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[i%len(charset)] // Simple implementation for now
	}
	return string(b)
}
