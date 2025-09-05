package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// NewBootstrapCmd creates the bootstrap command.
func NewBootstrapCmd() *cobra.Command {
	var (
		blocs  string
		force  bool
		dryRun bool
		output string
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
  ocfp bootstrap --bloc dev

  # Bootstrap specific blocs
  ocfp bootstrap --bloc dev --blocs mgmt,ocf`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBootstrap(cmd, args)
		},
	}

	// Command-specific flags
	cmd.Flags().StringVarP(&blocs, "blocs", "b", "all", "specific blocs to bootstrap (comma-separated)")
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation prompts")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview actions without making changes")
	cmd.Flags().StringVar(&output, "output", "table", "output format: table|json|yaml (dry-run only)")

	// Bind flags to viper
	_ = viper.BindPFlag("bootstrap.blocs", cmd.Flags().Lookup("blocs"))
	_ = viper.BindPFlag("bootstrap.force", cmd.Flags().Lookup("force"))
	_ = viper.BindPFlag("dry_run", cmd.Flags().Lookup("dry-run"))
	_ = viper.BindPFlag("bootstrap.output", cmd.Flags().Lookup("output"))

	return cmd
}

func runBootstrap(cmd *cobra.Command, args []string) error {
	// Determine blocs to run for
	blocsFlag := viper.GetString("bootstrap.blocs")

	configFile := viper.GetString("config")
	if configFile == "" {
		// Prefer the file viper loaded if any
		if used := viper.ConfigFileUsed(); used != "" {
			configFile = used
		}
	}

	if blocsFlag == "" || blocsFlag == "all" {
		// Run for all blocs in the config file
		// Fallback to single bloc via --bloc if config has no blocs
		err := runBootstrapForSelection(configFile, nil)
		if err != nil {
			return err
		}

		return nil
	}

	// Run for explicit list of blocs
	sel := []string{}

	for _, blocName := range splitAndTrim(blocsFlag) {
		if blocName != "" {
			sel = append(sel, blocName)
		}
	}
	err := runBootstrapForSelection(configFile, sel)

	if err != nil {
		return err
	}

	return nil
}

func runBootstrapForSelection(configFile string, selected []string) error {
	// If no selection provided, try single bloc via --bloc
	if len(selected) == 0 {
		single := viper.GetString("bloc_name")
		if single != "" {
			return runBootstrapForBloc(configFile, single)
		}
	}

	// Load config file to enumerate blocs
	data, err := os.ReadFile(configFile) // #nosec G304 - configFile is from user but used for reading config
	if err != nil {
		return fmt.Errorf("failed to read config file %s: %w", configFile, err)
	}

	var configData struct {
		Blocs map[string]interface{} `yaml:"blocs"`
	}
	if err := yaml.Unmarshal(data, &configData); err != nil {
		return fmt.Errorf("failed to parse config file %s: %w", configFile, err)
	}

	if len(configData.Blocs) == 0 {
		// No blocs defined; try single via --bloc
		single := viper.GetString("bloc_name")
		if single == "" {
			return errors.New("no blocs found in config and --bloc not provided")
		}

		return runBootstrapForBloc(configFile, single)
	}

	// Build list to run
	toRun := []string{}

	if len(selected) == 0 {
		for blocName := range configData.Blocs {
			toRun = append(toRun, blocName)
		}
	} else {
		// Filter by selected names
		want := map[string]bool{}
		for _, blocName := range selected {
			want[blocName] = true
		}

		for blocName := range configData.Blocs {
			if want[blocName] {
				toRun = append(toRun, blocName)
			}
		}

		if len(toRun) == 0 {
			return errors.New("no matching blocs found for selection")
		}
	}

	for _, name := range toRun {
		err := runBootstrapForBloc(configFile, name)
		if err != nil {
			return err
		}
	}

	return nil
}

func runBootstrapForBloc(configFile, blocName string) error {
	force := viper.GetBool("bootstrap.force")

	if blocName == "" {
		return errors.New("bloc is required")
	}
	// Provider will be derived from bloc configuration

	// Initialize logger per bloc
	logDir := filepath.Join(os.Getenv("HOME"), ".ocfp", "log")
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

	defer func() { _ = logger.Sync() }()

	// Load configuration for this bloc
	cfg, err := config.LoadWithParams(configFile, blocName)
	if err != nil {
		return fmt.Errorf("failed to load configuration for bloc %s: %w", blocName, err)
	}

	// Determine provider and region from configuration (allow --region override)
	iaas := cfg.Provider
	if iaas == "" {
		iaas = cfg.IaaS
	}

	if iaas == "" {
		return fmt.Errorf("provider must be specified in bloc config '%s'", blocName)
	}

	region := cfg.Region
	if v := viper.GetString("region"); v != "" {
		region = v
	}

	// Create provider config (include auth fields for providers like STACKIT)
	providerConfig := map[string]interface{}{
		"project_id": cfg.ProjectID,
		"org_id":     cfg.OrgID,
		"auth_token": cfg.AuthToken,
		"region":     region,
	}

	// Prefer service account JSON/token when present
	if cfg.ServiceAccountJSON != "" {
		providerConfig["service_account_json"] = cfg.ServiceAccountJSON
	}

	if cfg.ServiceAccountToken != "" {
		providerConfig["service_account_token"] = cfg.ServiceAccountToken
	}
	// If key path is provided and JSON not set, try to read it
	if providerConfig["service_account_json"] == nil && cfg.ServiceAccountKeyPath != "" {
		if content, err := os.ReadFile(cfg.ServiceAccountKeyPath); err == nil {
			providerConfig["service_account_json"] = string(content)
		}
	}
	// Map optional API endpoint override
	if cfg.APIEndpoint != "" {
		providerConfig["base_url"] = cfg.APIEndpoint
	}

	// Initialize provider
	provider, err := cpi.CreateProvider(context.Background(), iaas, providerConfig)
	if err != nil {
		return fmt.Errorf("failed to create provider: %w", err)
	}

	defer func() { _ = provider.Cleanup(context.Background()) }()

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
		Output:   viper.GetString("bootstrap.output"),
		Timeout:  30 * time.Minute,
	}
	bootstrapManager := bootstrap.NewManager(cfg, provider, stateManager, bootstrapOpts)

	// Execute bootstrap
	ctx := context.Background()
	if err := bootstrapManager.Execute(ctx); err != nil {
		return fmt.Errorf("bootstrap failed for bloc %s: %w", blocName, err)
	}

	if err := stateManager.Save(); err != nil {
		logger.Warnf("Failed to save final state for %s: %v", blocName, err)
	}

	fmt.Printf("\n✅ Bootstrap completed: bloc=%s provider=%s region=%s\n", blocName, iaas, region)

	return nil
}

func splitAndTrim(input string) []string {
	parts := []string{}
	curr := ""

	for charIndex := range len(input) {
		if input[charIndex] == ',' {
			if curr != "" {
				parts = append(parts, strings.TrimSpace(curr))
			}

			curr = ""
		} else {
			curr += string(input[charIndex])
		}
	}

	if curr != "" {
		parts = append(parts, strings.TrimSpace(curr))
	}

	return parts
}
