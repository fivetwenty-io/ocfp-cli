package commands

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	// BootstrapTimeoutMinutes is the maximum duration in minutes for a bootstrap operation.
	BootstrapTimeoutMinutes = 30
)

// NewBootstrapCmd creates the bootstrap command.
func NewBootstrapCmd() *cobra.Command {
	var (
		blocs     string
		force     bool
		yes       bool
		dryRun    bool
		all       bool
		servers   bool
		volumes   bool
		snapshots bool
		buckets   bool
		secGroups bool
		network   bool
		publicIPs bool
		bastion   bool
		keypairs  bool
		artifacts bool
		output    string
	)

	cmd := &cobra.Command{
		Use:     "bootstrap",
		Short:   "Bootstrap new environment",
		Long:    getBootstrapLongDescription(),
		Example: getBootstrapExamples(),
		RunE:    runBootstrap,
	}

	addBootstrapFlags(cmd, &blocs, &output, &force, &yes, &dryRun, &all, &servers, &volumes, &snapshots, &buckets, &secGroups, &network, &publicIPs, &bastion, &keypairs, &artifacts)
	bindBootstrapViperFlags(cmd)

	return cmd
}

// getBootstrapLongDescription returns the long description for the bootstrap command.
func getBootstrapLongDescription() string {
	return `Bootstrap provisions the basic infrastructure for a new OCFP environment.

The command supports different modes:
- Default (--all): Create all bootstrap resources (network, security, compute, storage)
- Selective: Create only specified resource types
- Bastion only: Create only the bastion instance (--bastion)

Selective mode allows you to specify one or more resource types to create:
- --servers: Create compute instances (bastion) and associated keypairs
- --volumes: Create persistent volumes
- --snapshots: Create volume snapshots (if applicable)
- --buckets: Create storage buckets
- --security-groups: Create security groups
- --network: Create networks, subnets, and routers
- --public-ips: Create public IP addresses
- --key-pairs, --keys: Create only SSH key pairs

This includes (when --all or no flags specified):
- VPC/Network creation
- Subnet provisioning
- Security group setup with default rules
- Volume provisioning
- Bastion host deployment
- SSH keypair management`
}

// getBootstrapExamples returns the examples for the bootstrap command.
func getBootstrapExamples() string {
	return `  # Bootstrap using a specific config file
  ocfp bootstrap --config config/production.yml

  # Bootstrap using bloc name (creates all resources)
  ocfp bootstrap --bloc dev

  # Bootstrap without confirmation prompt (for automation)
  ocfp bootstrap --bloc dev -y

  # Bootstrap specific blocs
  ocfp bootstrap --bloc dev --blocs mgmt,ocf

  # Bootstrap only network infrastructure
  ocfp bootstrap --bloc dev --network

  # Bootstrap network and security groups
  ocfp bootstrap --bloc dev --network --security-groups

  # Bootstrap only public IPs (requires existing network)
  ocfp bootstrap --bloc dev --public-ips

  # Bootstrap network and public IPs together
  ocfp bootstrap --bloc dev --network --public-ips

  # Bootstrap only the bastion instance
  ocfp bootstrap --bloc dev --bastion

  # Bootstrap only SSH key pairs
  ocfp bootstrap --bloc dev --key-pairs

  # Bootstrap compute and storage
  ocfp bootstrap --bloc dev --servers --volumes --buckets`
}

// addBootstrapFlags adds all command flags to the bootstrap command.
func addBootstrapFlags(cmd *cobra.Command, blocs, output *string, force, yes, dryRun, all, servers, volumes, snapshots, buckets, secGroups, network, publicIPs, bastion, keypairs, artifacts *bool) {
	cmd.Flags().StringVarP(blocs, "blocs", "b", KeywordAll, "specific blocs to bootstrap (comma-separated)")
	cmd.Flags().BoolVar(force, "force", false, "skip confirmation prompts")
	cmd.Flags().BoolVarP(yes, "yes", "y", false, "skip confirmation prompt and proceed immediately")
	cmd.Flags().BoolVar(dryRun, "dry-run", false, "preview actions without making changes")
	cmd.Flags().BoolVar(all, "all", false, "create all bootstrap resources (default)")
	cmd.Flags().BoolVar(servers, "servers", false, "create compute instances and keypairs")
	cmd.Flags().BoolVar(volumes, "volumes", false, "create persistent volumes")
	cmd.Flags().BoolVar(snapshots, "snapshots", false, "create volume snapshots")
	cmd.Flags().BoolVar(buckets, "buckets", false, "create storage buckets")
	cmd.Flags().BoolVar(secGroups, "security-groups", false, "create security groups")
	cmd.Flags().BoolVar(network, "network", false, "create networks, subnets, routers, and public IPs")
	cmd.Flags().BoolVar(publicIPs, "public-ips", false, "create public IP addresses")
	cmd.Flags().BoolVar(bastion, "bastion", false, "create only bastion instance")
	cmd.Flags().BoolVar(keypairs, "key-pairs", false, "create only SSH key pairs")
	cmd.Flags().BoolVar(keypairs, "keys", false, "alias for --key-pairs")
	cmd.Flags().BoolVar(artifacts, "artifacts", false, "create only the ocfp-artifacts (RustFS) VM; requires artifacts.enabled in config")
	cmd.Flags().StringVar(output, "output", OutputTable, "output format: table|json|yaml (dry-run only)")
}

// bindBootstrapViperFlags binds all bootstrap flags to viper.
func bindBootstrapViperFlags(cmd *cobra.Command) {
	_ = viper.BindPFlag("bootstrap.blocs", cmd.Flags().Lookup("blocs"))
	_ = viper.BindPFlag("bootstrap.force", cmd.Flags().Lookup("force"))
	_ = viper.BindPFlag("bootstrap.yes", cmd.Flags().Lookup("yes"))
	_ = viper.BindPFlag("dry_run", cmd.Flags().Lookup("dry-run"))
	_ = viper.BindPFlag("bootstrap.all", cmd.Flags().Lookup("all"))
	_ = viper.BindPFlag("bootstrap.servers", cmd.Flags().Lookup("servers"))
	_ = viper.BindPFlag("bootstrap.volumes", cmd.Flags().Lookup("volumes"))
	_ = viper.BindPFlag("bootstrap.snapshots", cmd.Flags().Lookup("snapshots"))
	_ = viper.BindPFlag("bootstrap.buckets", cmd.Flags().Lookup("buckets"))
	_ = viper.BindPFlag("bootstrap.security_groups", cmd.Flags().Lookup("security-groups"))
	_ = viper.BindPFlag("bootstrap.network", cmd.Flags().Lookup("network"))
	_ = viper.BindPFlag("bootstrap.public_ips", cmd.Flags().Lookup("public-ips"))
	_ = viper.BindPFlag("bootstrap.bastion", cmd.Flags().Lookup("bastion"))
	_ = viper.BindPFlag("bootstrap.key_pairs", cmd.Flags().Lookup("key-pairs"))
	_ = viper.BindPFlag("bootstrap.artifacts", cmd.Flags().Lookup("artifacts"))
	_ = viper.BindPFlag("bootstrap.output", cmd.Flags().Lookup("output"))
}

func runBootstrap(cmd *cobra.Command, _args []string) error {
	// Silence usage on execution errors
	cmd.SilenceUsage = true

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
		return viper.GetString("bloc")
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
	single := viper.GetString("bloc")
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
	toRun := make([]string, 0, len(blocs))
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

	stateManager, err := createStateManager(blocName)
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
	// Logger appends {bloc}/logs/{command} itself; pass the state-class
	// root, not the legacy flat ~/.ocfp home.
	logDir := config.StateHome()

	err := logger.Initialize(logger.Config{
		Level:      viper.GetString("log_level"),
		Debug:      viper.GetBool("debug"),
		Verbose:    viper.GetBool("verbose"),
		Trace:      viper.GetBool("trace"),
		NoLog:      viper.GetBool("no_log"),
		LogDir:     logDir,
		BlocName:   blocName,
		Command:    "bootstrap",
		Subcommand: "", // Bootstrap has no subcommands
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

	// Debug logging
	logger.Debugf("Provider determined: iaas='%s' (from cfg.Provider='%s', cfg.IaaS='%s')", iaas, cfg.Provider, cfg.IaaS)

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

	// Add AWS-specific configuration
	if cfg.AccessKeyID != "" {
		providerConfig["access_key_id"] = cfg.AccessKeyID
	}

	if cfg.SecretAccessKey != "" {
		providerConfig["secret_access_key"] = cfg.SecretAccessKey
	}

	if cfg.SessionToken != "" {
		providerConfig["session_token"] = cfg.SessionToken
	}

	addServiceAccountConfig(providerConfig, cfg)
	addAPIEndpointConfig(providerConfig, cfg)
	addPVEProviderConfig(providerConfig, cfg)

	return providerConfig
}

// addPVEProviderConfig adds Proxmox VE-specific fields to the provider config map.
// PVE auth uses token_id/token_secret or username/password; bloc fields map directly.
func addPVEProviderConfig(providerConfig map[string]interface{}, cfg *config.Config) {
	if cfg.TokenSecret != "" {
		providerConfig["token_secret"] = cfg.TokenSecret
	}

	if cfg.Username != "" {
		providerConfig["username"] = cfg.Username
	}

	if cfg.Password != "" {
		providerConfig["password"] = cfg.Password
	}

	if cfg.Network.Name != "" {
		providerConfig["default_bridge"] = cfg.Network.Name
	}

	if cfg.TemplateBridge != "" {
		providerConfig["template_bridge"] = cfg.TemplateBridge
	}

	if cfg.TemplateSeedIP != "" {
		providerConfig["template_seed_ip"] = cfg.TemplateSeedIP

		// Guard each key by its own non-empty check, matching every other
		// field in this function, so an unset optional field is simply
		// absent from the map rather than forwarded as an empty string.
		if cfg.TemplateSeedGateway != "" {
			providerConfig["template_seed_gateway"] = cfg.TemplateSeedGateway
		}

		if cfg.TemplateSeedSearchDomain != "" {
			providerConfig["template_seed_searchdomain"] = cfg.TemplateSeedSearchDomain
		}

		if dns := resolveTemplateSeedDNS(cfg); len(dns) > 0 {
			providerConfig["template_seed_dns"] = dns
		}
	}

	// default_storage drives where template auto-provisioning places VM
	// disks; resolve it the same way configureCPI resolves vm_storage.
	if cfg.VMStorage != "" {
		providerConfig["default_storage"] = cfg.VMStorage
	} else if cfg.Artifacts.Data.StoragePool != "" {
		providerConfig["default_storage"] = cfg.Artifacts.Data.StoragePool
	}

	if cfg.IsoStorage != "" {
		providerConfig["iso_storage"] = cfg.IsoStorage
	}

	// VerifySSL is always passed so the provider sees an explicit value
	// (defaults to false in the bloc Config zero value).
	providerConfig["verify_ssl"] = cfg.VerifySSL
}

// resolveTemplateSeedDNS returns the resolver list to forward as
// template_seed_dns. Explicit TemplateSeedDNS always wins; otherwise fall
// back to the bloc's Network.DNSServers so the seed VM uses the same
// resolvers as the rest of the bloc. Returns nil when neither is set, in
// which case the pve provider applies its own default
// (defaultPVECloudInitDNS). This keeps the provider ignorant of bloc network
// structure, mirroring how resolveBastionDNSServers resolves DNS on the
// bootstrap side.
func resolveTemplateSeedDNS(cfg *config.Config) []string {
	if len(cfg.TemplateSeedDNS) > 0 {
		return cfg.TemplateSeedDNS
	}

	if len(cfg.Network.DNSServers) > 0 {
		return cfg.Network.DNSServers
	}

	return nil
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

// createStateManager initializes the state manager for the given bloc.
func createStateManager(blocName string) (*state.Manager, error) {
	// Get standard state directory for this bloc
	stateDir, err := state.GetStateDir(blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to determine state directory: %w", err)
	}

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create state manager: %w", err)
	}

	return stateManager, nil
}

// artifactsStepInScope reports whether the bootstrap run will execute the
// artifacts step, mirroring Manager.filterSteps: artifacts runs in full ("ALL")
// mode and, in selective mode, only when --artifacts is set; bastion-only and
// other selective modes skip it.
func artifactsStepInScope(o *bootstrap.Options) bool {
	if o.Bastion {
		return false
	}

	selective := o.Servers || o.Volumes || o.Snapshots || o.Buckets ||
		o.SecurityGroups || o.Network || o.PublicIPs || o.KeyPairs || o.Artifacts

	return o.Artifacts || !selective
}

// executeBootstrap performs the actual bootstrap execution.
func executeBootstrap(cfg *config.Config, provider cpi.Provider, stateManager *state.Manager, blocName, iaas, region string) error {
	// Load state for the bloc
	_, err := stateManager.Load(blocName)
	if err != nil {
		return fmt.Errorf("failed to load state for bloc %s: %w", blocName, err)
	}

	bootstrapOpts := &bootstrap.Options{
		BlocName:       blocName,
		Provider:       iaas,
		Region:         region,
		Force:          viper.GetBool("bootstrap.force"),
		Yes:            viper.GetBool("bootstrap.yes"),
		DryRun:         viper.GetBool("dry_run"),
		All:            viper.GetBool("bootstrap.all"),
		Bastion:        viper.GetBool("bootstrap.bastion"),
		Servers:        viper.GetBool("bootstrap.servers"),
		Volumes:        viper.GetBool("bootstrap.volumes"),
		Snapshots:      viper.GetBool("bootstrap.snapshots"),
		Buckets:        viper.GetBool("bootstrap.buckets"),
		SecurityGroups: viper.GetBool("bootstrap.security_groups"),
		Network:        viper.GetBool("bootstrap.network"),
		PublicIPs:      viper.GetBool("bootstrap.public_ips"),
		KeyPairs:       viper.GetBool("bootstrap.key_pairs") || viper.GetBool("bootstrap.keys"),
		Artifacts:      viper.GetBool("bootstrap.artifacts"),
		Output:         viper.GetString("bootstrap.output"),
		Timeout:        BootstrapTimeoutMinutes * time.Minute,
	}

	bootstrapManager := bootstrap.NewManager(cfg, provider, stateManager, bootstrapOpts)

	// The artifacts step's internal-ca TLS mode persists the bloc CA to the
	// inception vault, and it HARD-FAILS without one. Without this guard the
	// failure surfaces late — at step 8/9, after the bastion VM is already
	// created — leaving a half-built bloc (bastion up, no artifacts) and a
	// confusing "connection refused" against 127.0.0.1:8234. Ensure the
	// inception vault is up BEFORE any VM is created so the run either fully
	// succeeds or fails fast with an actionable message.
	if artifactsStepInScope(bootstrapOpts) &&
		cfg.Artifacts.Enabled && cfg.Artifacts.TLS.Mode == config.ArtifactsTLSModeInternalCA {
		err := ensureInceptionVault(blocName, viper.GetBool("test"))
		if err != nil {
			return fmt.Errorf("artifacts (internal-ca TLS) requires the inception vault, which could not be started "+
				"(start it manually with `ocfp vault inception --bloc %s`): %w", blocName, err)
		}
	}

	// Best-effort vault wiring: when vault is reachable, the bootstrap artifacts
	// step writes blobstore endpoint + creds to vault. When unreachable, the
	// step prints a warning and the operator follows up with `ocfp vault populate`.
	if vaultMgr, vaultErr := vault.NewManagerFromEnv(cfg, blocName); vaultErr == nil {
		bootstrapManager.SetSafe(vaultMgr.GetSafe())
	} else {
		logger.Debugf("vault unavailable for bootstrap auto-write: %v", vaultErr)
	}

	ctx := context.Background()

	err = bootstrapManager.Execute(ctx)
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
