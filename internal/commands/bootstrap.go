package commands

import (
	"context"
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

const (
	// Bootstrap timeout duration.
	BootstrapTimeoutMinutes = 30
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
		RunE: runBootstrap,
	}

	// Command-specific flags
	cmd.Flags().StringVarP(&blocs, "blocs", "b", KeywordAll, "specific blocs to bootstrap (comma-separated)")
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation prompts")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview actions without making changes")
	cmd.Flags().StringVar(&output, "output", OutputTable, "output format: table|json|yaml (dry-run only)")

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

	if blocsFlag == "" || blocsFlag == string(KeywordAll) {
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
	if singleBloc := getSingleBlocIfNoSelection(selected); singleBloc != "" {
		return runBootstrapForBloc(configFile, singleBloc)
	}

	configData, err := loadConfigData(configFile)
	if err != nil {
		return err
	}

	if len(configData.Blocs) == 0 {
		return handleNoBlocsInConfig(configFile)
	}

	toRun, err := determineBlocsToRun(selected, configData.Blocs)
	if err != nil {
		return err
	}

	return runBootstrapForBlocs(configFile, toRun)
}

// getSingleBlocIfNoSelection returns single bloc name if no selection provided.
func getSingleBlocIfNoSelection(selected []string) string {
	if len(selected) == 0 {
		return viper.GetString("bloc_name")
	}

	return ""
}

// loadConfigData loads and parses the configuration file.
func loadConfigData(configFile string) (*struct {
	Blocs map[string]interface{} `yaml:"blocs"`
}, error) {
	data, err := os.ReadFile(configFile) // #nosec G304 - configFile is from user but used for reading config
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", configFile, err)
	}

	var configData struct {
		Blocs map[string]interface{} `yaml:"blocs"`
	}

	err = yaml.Unmarshal(data, &configData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", configFile, err)
	}

	return &configData, nil
}

// handleNoBlocsInConfig handles the case when no blocs are defined in config.
func handleNoBlocsInConfig(configFile string) error {
	single := viper.GetString("bloc_name")
	if single == "" {
		return ErrNoBlocsFoundInConfigAndBlocNotProvided
	}

	return runBootstrapForBloc(configFile, single)
}

// determineBlocsToRun determines which blocs should be run based on selection.
func determineBlocsToRun(selected []string, availableBlocs map[string]interface{}) ([]string, error) {
	if len(selected) == 0 {
		return getAllBlocNames(availableBlocs), nil
	}

	toRun := filterSelectedBlocs(selected, availableBlocs)
	if len(toRun) == 0 {
		return nil, ErrNoMatchingBlocsFoundForSelection
	}

	return toRun, nil
}

// getAllBlocNames returns all bloc names from the configuration.
func getAllBlocNames(blocs map[string]interface{}) []string {
	toRun := []string{}
	for blocName := range blocs {
		toRun = append(toRun, blocName)
	}

	return toRun
}

// filterSelectedBlocs filters available blocs by selected names.
func filterSelectedBlocs(selected []string, availableBlocs map[string]interface{}) []string {
	want := make(map[string]bool)
	for _, blocName := range selected {
		want[blocName] = true
	}

	toRun := []string{}

	for blocName := range availableBlocs {
		if want[blocName] {
			toRun = append(toRun, blocName)
		}
	}

	return toRun
}

// runBootstrapForBlocs runs bootstrap for multiple blocs.
func runBootstrapForBlocs(configFile string, toRun []string) error {
	for _, name := range toRun {
		err := runBootstrapForBloc(configFile, name)
		if err != nil {
			return err
		}
	}

	return nil
}

func runBootstrapForBloc(configFile, blocName string) error {
	if blocName == "" {
		return ErrBlocIsRequired
	}

	err := initializeBlocLogger(blocName)
	if err != nil {
		return err
	}

	defer func() { _ = logger.Sync() }()

	cfg, err := loadBlocConfiguration(configFile, blocName)
	if err != nil {
		return err
	}

	iaas, region, err := determineProviderAndRegion(cfg)
	if err != nil {
		return err
	}

	providerConfig := buildProviderConfig(cfg, region)

	provider, err := createProvider(iaas, providerConfig)
	if err != nil {
		return err
	}

	defer func() { _ = provider.Cleanup(context.Background()) }()

	stateManager, err := createStateManager()
	if err != nil {
		return err
	}

	err = executeBootstrap(cfg, provider, stateManager, blocName, iaas, region)
	if err != nil {
		return err
	}

	finalizeBootstrap(stateManager, blocName, iaas, region)

	return nil
}

// initializeBlocLogger initializes the logger for a specific bloc.
func initializeBlocLogger(blocName string) error {
	logDir := filepath.Join(os.Getenv("HOME"), ".ocfp", "log")

	err := logger.Initialize(logger.Config{
		Level:      viper.GetString("log_level"),
		Debug:      viper.GetBool("debug"),
		Verbose:    viper.GetBool("verbose"),
		Trace:      viper.GetBool("trace"),
		NoLog:      viper.GetBool("no_log"),
		LogDir:     logDir,
		BlocName:   blocName,
		Command:    "bootstrap",
		RequestID:  os.Getenv("OCFP_REQUEST_ID"),
		DirectorID: "",
	})
	if err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}

	return nil
}

// loadBlocConfiguration loads the configuration for the specified bloc.
func loadBlocConfiguration(configFile, blocName string) (*config.Config, error) {
	cfg, err := config.LoadWithParams(configFile, blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration for bloc %s: %w", blocName, err)
	}

	return cfg, nil
}

// determineProviderAndRegion determines the provider and region from configuration.
func determineProviderAndRegion(cfg *config.Config) (string, string, error) {
	iaas := cfg.Provider
	if iaas == "" {
		iaas = cfg.IaaS
	}

	if iaas == "" {
		return "", "", ErrProviderMustBeSpecifiedInBlocConfig("")
	}

	region := cfg.Region
	if v := viper.GetString("region"); v != "" {
		region = v
	}

	return iaas, region, nil
}

// buildProviderConfig builds the provider configuration map.
func buildProviderConfig(cfg *config.Config, region string) map[string]interface{} {
	providerConfig := map[string]interface{}{
		"project_id": cfg.ProjectID,
		"org_id":     cfg.OrgID,
		"auth_token": cfg.AuthToken,
		"region":     region,
	}

	addServiceAccountConfig(providerConfig, cfg)
	addAPIEndpointConfig(providerConfig, cfg)

	return providerConfig
}

// addServiceAccountConfig adds service account configuration to provider config.
func addServiceAccountConfig(providerConfig map[string]interface{}, cfg *config.Config) {
	if cfg.ServiceAccountJSON != "" {
		providerConfig["service_account_json"] = cfg.ServiceAccountJSON

		return
	}

	if cfg.ServiceAccountToken != "" {
		providerConfig["service_account_token"] = cfg.ServiceAccountToken

		return
	}

	if cfg.ServiceAccountKeyPath != "" {
		content, err := os.ReadFile(cfg.ServiceAccountKeyPath)
		if err == nil {
			providerConfig["service_account_json"] = string(content)
		}
	}
}

// addAPIEndpointConfig adds API endpoint configuration if present.
func addAPIEndpointConfig(providerConfig map[string]interface{}, cfg *config.Config) {
	if cfg.APIEndpoint != "" {
		providerConfig["base_url"] = cfg.APIEndpoint
	}
}

// createProvider initializes the cloud provider
//
//nolint:ireturn // Returns interface by design for provider abstraction
func createProvider(iaas string, providerConfig map[string]interface{}) (cpi.Provider, error) {
	provider, err := cpi.CreateProvider(context.Background(), iaas, providerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create provider: %w", err)
	}

	return provider, nil
}

// createStateManager initializes the state manager.
func createStateManager() (*state.Manager, error) {
	stateManager, err := state.NewManager("")
	if err != nil {
		return nil, fmt.Errorf("failed to create state manager: %w", err)
	}

	return stateManager, nil
}

// executeBootstrap performs the actual bootstrap execution.
func executeBootstrap(cfg *config.Config, provider cpi.Provider, stateManager *state.Manager, blocName, iaas, region string) error {
	bootstrapOpts := &bootstrap.Options{
		BlocName: blocName,
		Provider: iaas,
		Region:   region,
		Force:    viper.GetBool("bootstrap.force"),
		DryRun:   viper.GetBool("dry_run"),
		Output:   viper.GetString("bootstrap.output"),
		Timeout:  BootstrapTimeoutMinutes * time.Minute,
	}

	bootstrapManager := bootstrap.NewManager(cfg, provider, stateManager, bootstrapOpts)
	ctx := context.Background()

	err := bootstrapManager.Execute(ctx)
	if err != nil {
		return fmt.Errorf("bootstrap failed for bloc %s: %w", blocName, err)
	}

	return nil
}

// finalizeBootstrap handles post-bootstrap tasks.
func finalizeBootstrap(stateManager *state.Manager, blocName, iaas, region string) {
	err := stateManager.Save()
	if err != nil {
		logger.Warnf("Failed to save final state for %s: %v", blocName, err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "\n✅ Bootstrap completed: bloc=%s provider=%s region=%s\n", blocName, iaas, region)
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
