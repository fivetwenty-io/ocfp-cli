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

const (
	// File permissions.
	ConfigFilePerm os.FileMode = 0600
	ConfigDirPerm  os.FileMode = 0750

	// Tabwriter padding.
	TabwriterPadding = 2
)

// NewEnvCmd creates the env command group.
func NewEnvCmd() *cobra.Command {
	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional fields
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

// newEnvListCmd creates the env list command.
func newEnvListCmd() *cobra.Command {
	return &cobra.Command{ //nolint:exhaustruct // Using zero values for optional fields
		Use:     "list",
		Short:   "List all available environments",
		Long:    `List all environments defined in the configuration files.`,
		Aliases: []string{"ls"},
		RunE:    runEnvList,
	}
}

// newEnvShowCmd creates the env show command.
func newEnvShowCmd() *cobra.Command {
	return &cobra.Command{ //nolint:exhaustruct // Using zero values for optional fields
		Use:   "show [environment]",
		Short: "Show environment details",
		Long: `Display detailed information about an environment.
If no environment is specified, shows the current environment.`,
		Aliases: []string{"info", "get"},
		Args:    cobra.MaximumNArgs(1),
		RunE:    runEnvShow,
	}
}

// newEnvSetCmd creates the env set command.
func newEnvSetCmd() *cobra.Command {
	return &cobra.Command{ //nolint:exhaustruct // Using zero values for optional fields
		Use:     "set <environment>",
		Short:   "Set the active environment",
		Long:    `Set the active environment for subsequent commands.`,
		Aliases: []string{"use", "switch"},
		Args:    cobra.ExactArgs(1),
		RunE:    runEnvSet,
	}
}

// newEnvExportCmd creates the env export command.
func newEnvExportCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional fields
		Use:   "export [environment]",
		Short: "Export environment variables",
		Long: `Export environment configuration as shell variables.
If no environment is specified, exports the current environment.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvExport(args, format)
		},
	}

	cmd.Flags().StringVar(&format, "format", "bash", "export format (bash, fish, powershell)")

	return cmd
}

// runEnvList lists all available environments.
func runEnvList(cmd *cobra.Command, args []string) error {
	log := logger.WithOperation("env-list")

	// Find all configuration files
	envs := findEnvironments()

	if len(envs) == 0 {
		_, _ = fmt.Fprint(os.Stdout, "No environments found\n")

		return nil
	}

	// Get current environment
	currentEnv := viper.GetString("bloc_name")
	if currentEnv == "" {
		currentEnv = os.Getenv("OCFP_BLOC_NAME")
	}

	// Create table writer
	tableWriter := tabwriter.NewWriter(os.Stdout, 0, 0, TabwriterPadding, ' ', 0)
	_, _ = fmt.Fprintln(tableWriter, "NAME\tPROVIDER\tREGION\tSTATUS\tCONFIG")
	_, _ = fmt.Fprintln(tableWriter, "----\t--------\t------\t------\t------")

	for _, env := range envs {
		status := ""
		if env.Name == currentEnv {
			status = "ACTIVE"
		}

		_, _ = fmt.Fprintf(tableWriter, "%s\t%s\t%s\t%s\t%s\n",
			env.Name, env.Provider, env.Region, status, env.ConfigFile)
	}

	_ = tableWriter.Flush()

	log.Debugf("Listed %d environments", len(envs))

	return nil
}

// runEnvShow displays details about an environment.
func runEnvShow(cmd *cobra.Command, args []string) error {
	log := logger.WithOperation("env-show")

	envName, err := getEnvironmentName(args)
	if err != nil {
		return err
	}

	cfg, err := config.LoadWithParams(viper.GetString("config.file"), envName)
	if err != nil {
		return fmt.Errorf("failed to load environment %s: %w", envName, err)
	}

	err = displayEnvironmentDetails(cfg)
	if err != nil {
		return err
	}

	log.Debugf("Displayed environment: %s", envName)

	return nil
}

// getEnvironmentName determines which environment to show.
func getEnvironmentName(args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}

	envName := viper.GetString("bloc_name")
	if envName == "" {
		return "", ErrNoEnvironmentSpecifiedAndNoCurrentSet
	}

	return envName, nil
}

// displayEnvironmentDetails displays all environment configuration details.
func displayEnvironmentDetails(cfg *config.Config) error {
	err := printSection("Environment", cfg.Name, true)
	if err != nil {
		return err
	}

	err = displayProviderConfiguration(cfg)
	if err != nil {
		return err
	}

	err = displayNetworkConfiguration(cfg)
	if err != nil {
		return err
	}

	err = displayBastionConfiguration(cfg)
	if err != nil {
		return err
	}

	err = displayBlocInformation(cfg)
	if err != nil {
		return err
	}

	return displayAvailabilityZones(cfg)
}

// printSection prints a section header with optional separator.
func printSection(label, value string, withSeparator bool) error {
	_, err := fmt.Fprintf(os.Stdout, "%s: %s\n", label, value)
	if err != nil {
		return fmt.Errorf("failed to write %s: %w", strings.ToLower(label), err)
	}

	if withSeparator {
		_, err := fmt.Fprintf(os.Stdout, "=====================================\n\n")
		if err != nil {
			return fmt.Errorf("failed to write separator: %w", err)
		}
	}

	return nil
}

// printField prints a field with proper formatting.
func printField(label, value string) error {
	_, err := fmt.Fprintf(os.Stdout, "  %-12s %s\n", label+":", value)
	if err != nil {
		return fmt.Errorf("failed to write %s: %w", strings.ToLower(label), err)
	}

	return nil
}

// printHeader prints a section header.
func printHeader(header string) error {
	_, err := fmt.Fprintf(os.Stdout, "\n%s:\n", header)
	if err != nil {
		return fmt.Errorf("failed to write %s header: %w", strings.ToLower(header), err)
	}

	return nil
}

// displayProviderConfiguration displays provider-related configuration.
func displayProviderConfiguration(cfg *config.Config) error {
	_, err := fmt.Fprintf(os.Stdout, "Provider Configuration:\n")
	if err != nil {
		return fmt.Errorf("failed to write provider header: %w", err)
	}

	fields := []struct{ label, value string }{
		{"Provider", cfg.Provider},
		{"IaaS", cfg.IaaS},
		{"Region", cfg.Region},
		{"Project ID", cfg.ProjectID},
		{"Org ID", cfg.OrgID},
	}

	for _, field := range fields {
		err := printField(field.label, field.value)
		if err != nil {
			return err
		}
	}

	return nil
}

// displayNetworkConfiguration displays network configuration if present.
func displayNetworkConfiguration(cfg *config.Config) error {
	if cfg.Network.Name == "" {
		return nil
	}

	err := printHeader("Network Configuration")
	if err != nil {
		return err
	}

	err = printField("Name", cfg.Network.Name)
	if err != nil {
		return err
	}

	err = printField("CIDR", cfg.Network.CIDR)
	if err != nil {
		return err
	}

	if len(cfg.Network.DNS) > 0 {
		return printField("DNS", strings.Join(cfg.Network.DNS, ", "))
	}

	return nil
}

// displayBastionConfiguration displays bastion configuration if present.
func displayBastionConfiguration(cfg *config.Config) error {
	if cfg.Bastion.Flavor == "" {
		return nil
	}

	err := printHeader("Bastion Configuration")
	if err != nil {
		return err
	}

	fields := []struct{ label, value string }{
		{"Flavor", cfg.Bastion.Flavor},
		{"Image", cfg.Bastion.Image},
		{"Key Pair", cfg.Bastion.Keypair},
	}

	for _, field := range fields {
		err := printField(field.label, field.value)
		if err != nil {
			return err
		}
	}

	return nil
}

// displayBlocInformation displays current bloc information.
func displayBlocInformation(cfg *config.Config) error {
	err := printHeader("Current Bloc")
	if err != nil {
		return err
	}

	err = printField("Name", cfg.Name)
	if err != nil {
		return err
	}

	if cfg.Type != "" {
		err := printField("Type", cfg.Type)
		if err != nil {
			return err
		}
	}

	if cfg.Environment != "" {
		return printField("Environment", cfg.Environment)
	}

	return nil
}

// displayAvailabilityZones displays availability zones if present.
func displayAvailabilityZones(cfg *config.Config) error {
	if len(cfg.AZs) == 0 {
		return nil
	}

	err := printHeader("Availability Zones")
	if err != nil {
		return err
	}

	for name, az := range cfg.AZs {
		_, err := fmt.Fprintf(os.Stdout, "  - %s: %s\n", name, az.Zone)
		if err != nil {
			return fmt.Errorf("failed to write AZ: %w", err)
		}
	}

	return nil
}

// runEnvSet sets the active environment.
func runEnvSet(cmd *cobra.Command, args []string) error {
	log := logger.WithOperation("env-set")
	envName := args[0]

	targetEnv, err := findTargetEnvironment(envName)
	if err != nil {
		return err
	}

	err = updateOCFPConfig(envName, targetEnv, log)
	if err != nil {
		return err
	}

	err = displayEnvironmentSwitch(envName, targetEnv)
	if err != nil {
		return err
	}

	log.Infof("Set active environment to: %s", envName)

	return nil
}

func findTargetEnvironment(envName string) (*environmentInfo, error) {
	envs := findEnvironments()

	for _, env := range envs {
		if env.Name == envName {
			return &env, nil
		}
	}

	return nil, ErrEnvironmentNotFound(envName)
}

func updateOCFPConfig(envName string, targetEnv *environmentInfo, log logger.Logger) error {
	ocfpConfigPath := filepath.Join(os.Getenv("HOME"), ".ocfp", "config.yml")

	ocfpConfig, err := readExistingOCFPConfig(ocfpConfigPath, log)
	if err != nil {
		return err
	}

	ocfpConfig["current_environment"] = envName
	ocfpConfig["bloc_name"] = envName
	ocfpConfig["config_file"] = targetEnv.ConfigFile

	return writeOCFPConfig(ocfpConfigPath, ocfpConfig)
}

func readExistingOCFPConfig(ocfpConfigPath string, log logger.Logger) (map[string]interface{}, error) {
	ocfpConfig := make(map[string]interface{})
	// #nosec G304 - ocfpConfigPath is constructed from safe paths
	data, err := os.ReadFile(ocfpConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ocfpConfig, nil
		}

		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	err = yaml.Unmarshal(data, &ocfpConfig)
	if err != nil {
		log.Warnf("Failed to parse existing config: %v", err)

		return ocfpConfig, nil
	}

	return ocfpConfig, nil
}

func writeOCFPConfig(ocfpConfigPath string, ocfpConfig map[string]interface{}) error {
	err := os.MkdirAll(filepath.Dir(ocfpConfigPath), ConfigDirPerm)
	if err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := yaml.Marshal(ocfpConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	err = os.WriteFile(ocfpConfigPath, data, ConfigFilePerm)
	if err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

func displayEnvironmentSwitch(envName string, targetEnv *environmentInfo) error {
	_, err := fmt.Fprintf(os.Stdout, "Switched to environment: %s\n", envName)
	if err != nil {
		return fmt.Errorf("failed to write switch message: %w", err)
	}

	_, err = fmt.Fprintf(os.Stdout, "Configuration file: %s\n", targetEnv.ConfigFile)
	if err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// runEnvExport exports environment variables.
func runEnvExport(args []string, format string) error {
	log := logger.WithOperation("env-export")

	// Determine which environment to export
	var envName string
	if len(args) > 0 {
		envName = args[0]
	} else {
		envName = viper.GetString("bloc_name")
		if envName == "" {
			return ErrNoEnvironmentSpecifiedAndNoCurrentSet
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
		err := exportBash(cfg)
		if err != nil {
			return fmt.Errorf("failed to export bash format: %w", err)
		}
	case "fish":
		err := exportFish(cfg)
		if err != nil {
			return fmt.Errorf("failed to export fish format: %w", err)
		}
	case "powershell", "ps1":
		err := exportPowerShell(cfg)
		if err != nil {
			return fmt.Errorf("failed to export powershell format: %w", err)
		}
	default:
		return ErrUnsupportedExportFormat(format)
	}

	log.Debugf("Exported environment variables for: %s", envName)

	return nil
}

// environmentInfo contains basic environment information.
type environmentInfo struct {
	Name       string
	Provider   string
	Region     string
	ConfigFile string
}

// findEnvironments searches for available environment configurations.
func findEnvironments() []environmentInfo {
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
			data, err := os.ReadFile(match) // #nosec G304 - match is from glob pattern
			if err != nil {
				continue
			}

			var cfg config.Config

			err = yaml.Unmarshal(data, &cfg)
			if err != nil {
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

	return envs
}

// shellExporter defines the interface for different shell export formats.
type shellExporter struct {
	commentFormat string
	varFormat     string
	shellName     string
}

// exportEnvironment exports environment variables using the specified shell format.
func (e *shellExporter) exportEnvironment(cfg *config.Config) error {
	vars := []struct {
		name  string
		value string
	}{
		{"OCFP_BLOC_NAME", cfg.Name},
		{"OCFP_PROVIDER", cfg.Provider},
		{"OCFP_IAAS", cfg.IaaS},
		{"OCFP_REGION", cfg.Region},
		{"OCFP_PROJECT_ID", cfg.ProjectID},
		{"OCFP_ORG_ID", cfg.OrgID},
	}

	_, err := fmt.Fprintf(os.Stdout, e.commentFormat, cfg.Name)
	if err != nil {
		return fmt.Errorf("failed to write %s environment header: %w", e.shellName, err)
	}

	for _, v := range vars {
		_, err = fmt.Fprintf(os.Stdout, e.varFormat, v.name, v.value)
		if err != nil {
			return fmt.Errorf("failed to write %s %s export: %w", e.shellName, v.name, err)
		}
	}

	if cfg.Network.Name != "" {
		networkVars := []struct {
			name  string
			value string
		}{
			{"OCFP_NETWORK_NAME", cfg.Network.Name},
			{"OCFP_NETWORK_CIDR", cfg.Network.CIDR},
		}

		for _, v := range networkVars {
			_, err = fmt.Fprintf(os.Stdout, e.varFormat, v.name, v.value)
			if err != nil {
				return fmt.Errorf("failed to write %s %s export: %w", e.shellName, v.name, err)
			}
		}
	}

	return nil
}

// exportBash exports environment variables in bash format.
func exportBash(cfg *config.Config) error {
	exporter := &shellExporter{
		commentFormat: "# OCFP Environment: %s\n",
		varFormat:     "export %s='%s'\n",
		shellName:     "bash",
	}

	return exporter.exportEnvironment(cfg)
}

// exportFish exports environment variables in fish format.
func exportFish(cfg *config.Config) error {
	exporter := &shellExporter{
		commentFormat: "# OCFP Environment: %s\n",
		varFormat:     "set -x %s '%s'\n",
		shellName:     "fish",
	}

	return exporter.exportEnvironment(cfg)
}

// exportPowerShell exports environment variables in PowerShell format.
func exportPowerShell(cfg *config.Config) error {
	exporter := &shellExporter{
		commentFormat: "# OCFP Environment: %s\n",
		varFormat:     "$env:%s = '%s'\n",
		shellName:     "PowerShell",
	}

	return exporter.exportEnvironment(cfg)
}
