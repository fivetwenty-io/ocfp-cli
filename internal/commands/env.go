package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// NewEnvCmd creates the env command group
func NewEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Manage OCFP environments",
		Long: `Manage OCFP environments including listing, switching, and displaying information.

Environments are defined in configuration files and represent different deployment targets
such as development, staging, and production.`,
	}

	// Add subcommands
	cmd.AddCommand(
		newEnvListCmd(),
		newEnvShowCmd(),
		newEnvSetCmd(),
		newEnvExportCmd(),
	)

	return cmd
}

// newEnvListCmd creates the env list command
func newEnvListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all available environments",
		Long:    `List all environments defined in the configuration files.`,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvList(cmd, args)
		},
	}
}

// newEnvShowCmd creates the env show command
func newEnvShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show [environment]",
		Short: "Show environment details",
		Long: `Display detailed information about an environment.
If no environment is specified, shows the current environment.`,
		Aliases: []string{"info", "get"},
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvShow(cmd, args)
		},
	}
}

// newEnvSetCmd creates the env set command
func newEnvSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "set <environment>",
		Short:   "Set the active environment",
		Long:    `Set the active environment for subsequent commands.`,
		Aliases: []string{"use", "switch"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvSet(cmd, args)
		},
	}
}

// newEnvExportCmd creates the env export command
func newEnvExportCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "export [environment]",
		Short: "Export environment variables",
		Long: `Export environment configuration as shell variables.
If no environment is specified, exports the current environment.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvExport(cmd, args, format)
		},
	}

	cmd.Flags().StringVar(&format, "format", "bash", "export format (bash, fish, powershell)")

	return cmd
}

// runEnvList lists all available environments
func runEnvList(cmd *cobra.Command, args []string) error {
	log := logger.WithOperation("env-list")

	// Find all configuration files
	envs, err := findEnvironments()
	if err != nil {
		return fmt.Errorf("failed to find environments: %w", err)
	}

	if len(envs) == 0 {
		fmt.Println("No environments found")
		return nil
	}

	// Get current environment
	currentEnv := viper.GetString("bloc_name")
	if currentEnv == "" {
		currentEnv = os.Getenv("OCFP_BLOC_NAME")
	}

	// Create table writer
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPROVIDER\tREGION\tSTATUS\tCONFIG")
	fmt.Fprintln(w, "----\t--------\t------\t------\t------")

	for _, env := range envs {
		status := ""
		if env.Name == currentEnv {
			status = "ACTIVE"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			env.Name, env.Provider, env.Region, status, env.ConfigFile)
	}

	w.Flush()
	log.Debugf("Listed %d environments", len(envs))

	return nil
}

// runEnvShow displays details about an environment
func runEnvShow(cmd *cobra.Command, args []string) error {
	log := logger.WithOperation("env-show")

	// Determine which environment to show
	envName := ""
	if len(args) > 0 {
		envName = args[0]
	} else {
		envName = viper.GetString("bloc_name")
		if envName == "" {
			return fmt.Errorf("no environment specified and no current environment set")
		}
	}

	// Load the environment configuration
	cfg, err := config.LoadWithParams(viper.GetString("config.file"), envName)
	if err != nil {
		return fmt.Errorf("failed to load environment %s: %w", envName, err)
	}

	// Display environment details
	fmt.Printf("Environment: %s\n", cfg.Name)
	fmt.Printf("=====================================\n\n")

	fmt.Printf("Provider Configuration:\n")
	fmt.Printf("  Provider:    %s\n", cfg.Provider)
	fmt.Printf("  IaaS:        %s\n", cfg.IaaS)
	fmt.Printf("  Region:      %s\n", cfg.Region)
	fmt.Printf("  Project ID:  %s\n", cfg.ProjectID)
	fmt.Printf("  Org ID:      %s\n", cfg.OrgID)

	if cfg.Network.Name != "" {
		fmt.Printf("\nNetwork Configuration:\n")
		fmt.Printf("  Name:        %s\n", cfg.Network.Name)
		fmt.Printf("  CIDR:        %s\n", cfg.Network.CIDR)
		if len(cfg.Network.DNS) > 0 {
			fmt.Printf("  DNS:         %s\n", strings.Join(cfg.Network.DNS, ", "))
		}
	}

	if cfg.Bastion.Flavor != "" {
		fmt.Printf("\nBastion Configuration:\n")
		fmt.Printf("  Flavor:      %s\n", cfg.Bastion.Flavor)
		fmt.Printf("  Image:       %s\n", cfg.Bastion.Image)
		fmt.Printf("  Key Pair:    %s\n", cfg.Bastion.Keypair)
	}

	if len(cfg.Blocs) > 0 {
		fmt.Printf("\nBlocs:\n")
		for _, bloc := range cfg.Blocs {
			fmt.Printf("  - %s (%s)\n", bloc.Name, bloc.Type)
		}
	}

	if len(cfg.AZs) > 0 {
		fmt.Printf("\nAvailability Zones:\n")
		for name, az := range cfg.AZs {
			fmt.Printf("  - %s: %s\n", name, az.Zone)
		}
	}

	log.Debugf("Displayed environment: %s", envName)

	return nil
}

// runEnvSet sets the active environment
func runEnvSet(cmd *cobra.Command, args []string) error {
	log := logger.WithOperation("env-set")

	envName := args[0]

	// Verify the environment exists
	envs, err := findEnvironments()
	if err != nil {
		return fmt.Errorf("failed to find environments: %w", err)
	}

	found := false
	var targetEnv *environmentInfo
	for _, env := range envs {
		if env.Name == envName {
			found = true
			targetEnv = &env
			break
		}
	}

	if !found {
		return fmt.Errorf("environment '%s' not found", envName)
	}

	// Update the OCFP configuration file
	ocfpConfigPath := filepath.Join(os.Getenv("HOME"), ".ocfp", "config.yml")

	// Read existing config or create new
	ocfpConfig := make(map[string]interface{})
	if data, err := os.ReadFile(ocfpConfigPath); err == nil {
		if err := yaml.Unmarshal(data, &ocfpConfig); err != nil {
			log.Warnf("Failed to parse existing config: %v", err)
		}
	}

	// Update the current environment
	ocfpConfig["current_environment"] = envName
	ocfpConfig["bloc_name"] = envName
	ocfpConfig["config_file"] = targetEnv.ConfigFile

	// Write back the config
	if err := os.MkdirAll(filepath.Dir(ocfpConfigPath), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := yaml.Marshal(ocfpConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(ocfpConfigPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	fmt.Printf("Switched to environment: %s\n", envName)
	fmt.Printf("Configuration file: %s\n", targetEnv.ConfigFile)

	log.Infof("Set active environment to: %s", envName)

	return nil
}

// runEnvExport exports environment variables
func runEnvExport(cmd *cobra.Command, args []string, format string) error {
	log := logger.WithOperation("env-export")

	// Determine which environment to export
	envName := ""
	if len(args) > 0 {
		envName = args[0]
	} else {
		envName = viper.GetString("bloc_name")
		if envName == "" {
			return fmt.Errorf("no environment specified and no current environment set")
		}
	}

	// Load the environment configuration
	cfg, err := config.LoadWithParams(viper.GetString("config.file"), envName)
	if err != nil {
		return fmt.Errorf("failed to load environment %s: %w", envName, err)
	}

	// Export based on format
	switch format {
	case "bash", "sh":
		exportBash(cfg)
	case "fish":
		exportFish(cfg)
	case "powershell", "ps1":
		exportPowerShell(cfg)
	default:
		return fmt.Errorf("unsupported export format: %s", format)
	}

	log.Debugf("Exported environment variables for: %s", envName)

	return nil
}

// environmentInfo contains basic environment information
type environmentInfo struct {
	Name       string
	Provider   string
	Region     string
	ConfigFile string
}

// findEnvironments searches for available environment configurations
func findEnvironments() ([]environmentInfo, error) {
	var envs []environmentInfo

	// Search paths for configuration files
	searchPaths := []string{
		filepath.Join(os.Getenv("HOME"), ".ocfp", "configs"),
		filepath.Join(os.Getenv("HOME"), ".ocfp"),
		"./configs",
		".",
	}

	for _, searchPath := range searchPaths {
		// Look for YAML files
		pattern := filepath.Join(searchPath, "*.yml")
		matches, _ := filepath.Glob(pattern)

		pattern = filepath.Join(searchPath, "*.yaml")
		matches2, _ := filepath.Glob(pattern)
		matches = append(matches, matches2...)

		for _, match := range matches {
			// Try to load and parse the file
			data, err := os.ReadFile(match)
			if err != nil {
				continue
			}

			var cfg config.Config
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				continue
			}

			// Skip if it doesn't look like a valid OCFP config
			if cfg.Name == "" || cfg.Provider == "" {
				continue
			}

			envs = append(envs, environmentInfo{
				Name:       cfg.Name,
				Provider:   cfg.Provider,
				Region:     cfg.Region,
				ConfigFile: match,
			})
		}
	}

	return envs, nil
}

// exportBash exports environment variables in bash format
func exportBash(cfg *config.Config) {
	fmt.Printf("# OCFP Environment: %s\n", cfg.Name)
	fmt.Printf("export OCFP_BLOC_NAME='%s'\n", cfg.Name)
	fmt.Printf("export OCFP_PROVIDER='%s'\n", cfg.Provider)
	fmt.Printf("export OCFP_IAAS='%s'\n", cfg.IaaS)
	fmt.Printf("export OCFP_REGION='%s'\n", cfg.Region)
	fmt.Printf("export OCFP_PROJECT_ID='%s'\n", cfg.ProjectID)
	fmt.Printf("export OCFP_ORG_ID='%s'\n", cfg.OrgID)
	if cfg.Network.Name != "" {
		fmt.Printf("export OCFP_NETWORK_NAME='%s'\n", cfg.Network.Name)
		fmt.Printf("export OCFP_NETWORK_CIDR='%s'\n", cfg.Network.CIDR)
	}
}

// exportFish exports environment variables in fish format
func exportFish(cfg *config.Config) {
	fmt.Printf("# OCFP Environment: %s\n", cfg.Name)
	fmt.Printf("set -x OCFP_BLOC_NAME '%s'\n", cfg.Name)
	fmt.Printf("set -x OCFP_PROVIDER '%s'\n", cfg.Provider)
	fmt.Printf("set -x OCFP_IAAS '%s'\n", cfg.IaaS)
	fmt.Printf("set -x OCFP_REGION '%s'\n", cfg.Region)
	fmt.Printf("set -x OCFP_PROJECT_ID '%s'\n", cfg.ProjectID)
	fmt.Printf("set -x OCFP_ORG_ID '%s'\n", cfg.OrgID)
	if cfg.Network.Name != "" {
		fmt.Printf("set -x OCFP_NETWORK_NAME '%s'\n", cfg.Network.Name)
		fmt.Printf("set -x OCFP_NETWORK_CIDR '%s'\n", cfg.Network.CIDR)
	}
}

// exportPowerShell exports environment variables in PowerShell format
func exportPowerShell(cfg *config.Config) {
	fmt.Printf("# OCFP Environment: %s\n", cfg.Name)
	fmt.Printf("$env:OCFP_BLOC_NAME = '%s'\n", cfg.Name)
	fmt.Printf("$env:OCFP_PROVIDER = '%s'\n", cfg.Provider)
	fmt.Printf("$env:OCFP_IAAS = '%s'\n", cfg.IaaS)
	fmt.Printf("$env:OCFP_REGION = '%s'\n", cfg.Region)
	fmt.Printf("$env:OCFP_PROJECT_ID = '%s'\n", cfg.ProjectID)
	fmt.Printf("$env:OCFP_ORG_ID = '%s'\n", cfg.OrgID)
	if cfg.Network.Name != "" {
		fmt.Printf("$env:OCFP_NETWORK_NAME = '%s'\n", cfg.Network.Name)
		fmt.Printf("$env:OCFP_NETWORK_CIDR = '%s'\n", cfg.Network.CIDR)
	}
}
