package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// NewBootstrapCmd creates the bootstrap command
func NewBootstrapCmd() *cobra.Command {
	var (
		blocs string
		force bool
	)

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Bootstrap new environment",
		Long: `Bootstrap provisions the basic infrastructure for a new OCFP environment.

This includes:
- VPC/Network creation
- Subnet provisioning
- Security group setup with default rules
- Volume provisioning
- Bastion host deployment
- SSH keypair management`,
		Example: `  # Bootstrap using a specific config file
  ocfp bootstrap --config config/production.yml

  # Bootstrap using bloc name
  ocfp bootstrap --bloc-name dev

  # Bootstrap specific blocs
  ocfp bootstrap --bloc-name dev --blocs mgmt,ocf`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBootstrap(cmd, args)
		},
	}

	// Command-specific flags
	cmd.Flags().StringVarP(&blocs, "blocs", "b", "all", "specific blocs to bootstrap (comma-separated)")
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation prompts")

	// Bind flags to viper
	_ = viper.BindPFlag("bootstrap.blocs", cmd.Flags().Lookup("blocs"))
	_ = viper.BindPFlag("bootstrap.force", cmd.Flags().Lookup("force"))

	return cmd
}

func runBootstrap(cmd *cobra.Command, args []string) error {
	// Get configuration values
	blocName := viper.GetString("bloc_name")
	iaas := viper.GetString("iaas")
	region := viper.GetString("region")
	force := viper.GetBool("bootstrap.force")
	configFile := viper.GetString("config")

	// Validate required configuration
	if blocName == "" {
		return fmt.Errorf("bloc-name is required")
	}
	if iaas == "" {
		return fmt.Errorf("iaas provider is required")
	}

	// Initialize logger
	logDir := filepath.Join(os.Getenv("HOME"), ".ocfp", "logs")
	if err := logger.Initialize(logger.Config{
		Level:     viper.GetString("log_level"),
		Debug:     viper.GetBool("debug"),
		Verbose:   viper.GetBool("verbose"),
		Trace:     viper.GetBool("trace"),
		NoLog:     viper.GetBool("no_log"),
		LogDir:    logDir,
		BlocName:  blocName,
		Command:   "bootstrap",
		RequestID: os.Getenv("OCFP_REQUEST_ID"),
	}); err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}
	defer logger.Sync()

	// Load configuration
	cfg, err := config.LoadWithParams(configFile, blocName)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Create provider config
	providerConfig := map[string]interface{}{
		"project_id": cfg.ProjectID,
		"org_id":     cfg.OrgID,
		"auth_token": cfg.AuthToken,
		"region":     region,
	}

	// Initialize provider
	provider, err := cpi.CreateProvider(context.Background(), iaas, providerConfig)
	if err != nil {
		return fmt.Errorf("failed to create provider: %w", err)
	}
	defer provider.Cleanup(context.Background())

	// Initialize state manager
	stateManager, err := state.NewManager("")
	if err != nil {
		return fmt.Errorf("failed to create state manager: %w", err)
	}

	// Create bootstrap manager
	bootstrapOpts := &bootstrap.Options{
		BlocName: blocName,
		Provider: iaas,
		Region:   region,
		Force:    force,
		DryRun:   viper.GetBool("dry_run"),
		Timeout:  30 * time.Minute,
	}

	bootstrapManager := bootstrap.NewManager(cfg, provider, stateManager, bootstrapOpts)

	// Execute bootstrap
	ctx := context.Background()
	if err := bootstrapManager.Execute(ctx); err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}

	// Save final state
	if err := stateManager.Save(); err != nil {
		logger.Warnf("Failed to save final state: %v", err)
	}

	fmt.Printf("\n✅ Bootstrap completed successfully!\n")
	fmt.Printf("Bloc: %s\n", blocName)
	fmt.Printf("Provider: %s\n", iaas)
	fmt.Printf("Region: %s\n", region)

	return nil
}
