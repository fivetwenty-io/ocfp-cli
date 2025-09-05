package commands

import (
	"encoding/json"
	"fmt"
	"errors"
	"os"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// NewVaultCmd creates the vault command.
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

// newVaultPopulateCmd creates the vault populate subcommand.
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
			log := logger.Get()

			// Load configuration
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc")
			dryRun, _ := cmd.Flags().GetBool("dry-run")

			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Create vault manager
			manager, err := vault.NewManagerFromEnv(cfg, blocName)
			if err != nil {
				return fmt.Errorf("failed to create vault manager: %w", err)
			}
			defer func() { _ = manager.Close() }()

			// Handle subcommand (public-ips)
			var subcommand string
			if len(args) > 0 {
				subcommand = args[0]
			}

			// Create populate options
			opts := &vault.PopulateOptions{
				Subcommand: subcommand,
				DryRun:     dryRun,
				Force:      force,
			}

			// Handle file input
			if fromFile != "" {
				return errors.New("populate from file not yet implemented")
			}

			// Perform populate operation
			if err := manager.Populate(opts); err != nil {
				return fmt.Errorf("failed to populate vault: %w", err)
			}

			log.Info("Vault populated successfully")

			return nil
		},
	}

	cmd.Flags().StringVar(&vaultPath, "vault-path", "", "vault path prefix")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "load secrets from file")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing secrets")
	cmd.Flags().Bool("dry-run", false, "preview actions without making changes")

	return cmd
}

// newVaultInceptionCmd creates the vault inception subcommand.
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
			log := logger.Get()

			// Load configuration
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc")

			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			if deploymentName == "" {
				deploymentName = cfg.Name
			}

			log.Info("Initializing vault for deployment", "deployment", deploymentName)

			// Create vault manager
			manager, err := vault.NewManagerFromEnv(cfg, blocName)
			if err != nil {
				return fmt.Errorf("failed to create vault manager: %w", err)
			}
			defer func() { _ = manager.Close() }()

			// Generate inception secrets using the secret generator
			secretGen := vault.NewSecretGenerator()
			inceptionSecrets, err := secretGen.GenerateInceptionSecrets(deploymentName)
			if err != nil {
				return fmt.Errorf("failed to generate inception secrets: %w", err)
			}

			// Store in vault using path builder
			pathBuilder := vault.NewPathBuilder(cfg, blocName)
			inceptionPath := pathBuilder.GetInceptionPath()
			if vaultPath != "" {
				inceptionPath = strings.TrimPrefix(vaultPath, "/")
			}

			safe := manager.GetSafe()
			if err := safe.SetMultiple(inceptionPath, inceptionSecrets.ToMap()); err != nil {
				return fmt.Errorf("failed to store inception secrets: %w", err)
			}

			log.Info("Vault inception completed", "path", inceptionPath)

			return nil
		},
	}

	cmd.Flags().StringVar(&deploymentName, "deployment", "", "deployment name")
	cmd.Flags().StringVar(&vaultPath, "vault-path", "", "vault path prefix")

	return cmd
}

// newVaultMigrateCmd creates the vault migrate subcommand.
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
			log := logger.Get()

			// Load configuration
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc")
			force, _ := cmd.Flags().GetBool("force")

			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Create vault manager
			manager, err := vault.NewManagerFromEnv(cfg, blocName)
			if err != nil {
				return fmt.Errorf("failed to create vault manager: %w", err)
			}
			defer func() { _ = manager.Close() }()

			// Handle manual migration if source/dest paths specified
			if sourcePath != "" && destPath != "" {
				return manualMigrateVault(manager, sourcePath, destPath, dryRun)
			}

			// Otherwise do standard inception->production migration
			opts := &vault.MigrateOptions{
				DryRun: dryRun,
				Force:  force,
			}

			if err := manager.Migrate(opts); err != nil {
				return fmt.Errorf("failed to migrate vault: %w", err)
			}

			log.Info("Vault migration completed")

			return nil
		},
	}

	cmd.Flags().StringVar(&sourcePath, "source", "", "source vault path")
	cmd.Flags().StringVar(&destPath, "dest", "", "destination vault path")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview migration without changes")
	cmd.Flags().Bool("force", false, "skip confirmation prompts")

	return cmd
}

// newVaultExportCmd creates the vault export subcommand.
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
			log := logger.Get()

			if vaultPath == "" {
				return errors.New("vault path is required")
			}

			log.Info("Exporting vault secrets", "path", vaultPath)

			// Load configuration
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc")

			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Create vault manager
			manager, err := vault.NewManagerFromEnv(cfg, blocName)
			if err != nil {
				return fmt.Errorf("failed to create vault manager: %w", err)
			}
			defer func() { _ = manager.Close() }()

			// Export secrets
			safe := manager.GetSafe()
			secrets, err := safe.Export(strings.TrimPrefix(vaultPath, "/"))
			if err != nil {
				return fmt.Errorf("failed to export secrets: %w", err)
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
				err := os.WriteFile(outputFile, data, 0600)
				if err != nil {
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

// newVaultImportCmd creates the vault import subcommand.
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
			log := logger.Get()

			if vaultPath == "" || inputFile == "" {
				return errors.New("vault path and input file are required")
			}

			log.Info("Importing secrets to vault", "path", vaultPath, "file", inputFile)

			// Load secrets from file
			secrets, err := loadSecretsFromFile(inputFile)
			if err != nil {
				return fmt.Errorf("failed to load secrets: %w", err)
			}

			// Load configuration for vault connection
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc")

			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Create vault manager
			manager, err := vault.NewManagerFromEnv(cfg, blocName)
			if err != nil {
				return fmt.Errorf("failed to create vault manager: %w", err)
			}
			defer func() { _ = manager.Close() }()

			// Import to vault
			safe := manager.GetSafe()
			if err := safe.Import(strings.TrimPrefix(vaultPath, "/"), secrets); err != nil {
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

// loadSecretsFromFile loads secrets from a YAML or JSON file.
func loadSecretsFromFile(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path) // #nosec G304 - path comes from user input but is used for reading config
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

	return nil, errors.New("unable to parse file as YAML or JSON")
}

// manualMigrateVault performs manual migration between specified paths.
func manualMigrateVault(manager *vault.Manager, sourcePath, destPath string, dryRun bool) error {
	log := logger.Get()

	log.Info("Manual vault migration", "source", sourcePath, "dest", destPath, "dry-run", dryRun)

	safe := manager.GetSafe()

	// Export from source
	secrets, err := safe.Export(strings.TrimPrefix(sourcePath, "/"))
	if err != nil {
		return fmt.Errorf("failed to export from source: %w", err)
	}

	if dryRun {
		log.Info("Dry run - would migrate secrets", "count", len(secrets))

		for key := range secrets {
			log.Info("Would migrate", "key", key)
		}

		return nil
	}

	// Import to destination
	if err := safe.Import(strings.TrimPrefix(destPath, "/"), secrets); err != nil {
		return fmt.Errorf("failed to import to destination: %w", err)
	}

	log.Info("Manual vault migration completed", "migrated", len(secrets))

	return nil
}
