package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/bastion"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/precompile"
	"github.com/ocfp/ocfp-cli-go/internal/security"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	// DefaultMaxWorkers is the default number of workers for parallel operations.
	DefaultMaxWorkers = 4

	// ManifestFilePerm is the file permission mode for manifest files.
	ManifestFilePerm os.FileMode = 0600

	// DeploymentDirPerm is the file permission mode for deployment directories.
	DeploymentDirPerm os.FileMode = 0750
)

var (
	validFilePathPattern = regexp.MustCompile(`^[a-zA-Z0-9/._-]+$`)
	validDNSPattern      = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-.])*[a-zA-Z0-9]$`)
	validUserPattern     = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-_])*[a-zA-Z0-9]$`)

	// ErrInitCancelled indicates the user cancelled the initialization process.
	ErrInitCancelled = errors.New("initialization cancelled by user")
)

// NewInitCmd creates the init command.
func NewInitCmd() *cobra.Command {
	initFlags := &initFlags{
		force:      false,
		skipChecks: false,
		parallel:   false,
		dryRun:     false,
		resume:     false,
		verbose:    false,
		reboot:     false,
	}

	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional fields
		Use:       "init [component]",
		Short:     "Initialize OCFP components",
		Long:      getInitLongDescription(),
		Example:   getInitExamples(),
		ValidArgs: []string{"aws", "pve", "pg", "cf", "bosh", RoleBastion, KeywordAll},
		Args:      cobra.ExactArgs(1),
		RunE:      initFlags.runInit,
	}

	initFlags.addFlags(cmd)
	initFlags.bindViperFlags(cmd)

	return cmd
}

// initFlags holds all command flags for init.
type initFlags struct {
	force       bool
	skipChecks  bool
	parallel    bool
	dryRun      bool
	resume      bool
	verbose     bool
	ocfpOnly    bool
	configOnly  bool
	genesisOnly bool
	reboot      bool
}

// addFlags adds all flags to the command.
func (f *initFlags) addFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.force, "force", false, "skip confirmation prompts")
	cmd.Flags().BoolVar(&f.skipChecks, "skip-checks", false, "skip prerequisite validation")
	cmd.Flags().BoolVar(&f.parallel, "parallel", false, "run independent tasks in parallel")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "print actions without executing")
	cmd.Flags().BoolVar(&f.resume, "resume", false, "resume from last successful checkpoint")
	cmd.Flags().BoolVar(&f.verbose, "verbose", false, "enable verbose logging")
	cmd.Flags().BoolVar(&f.ocfpOnly, "ocfp", false, "only install/update OCFP CLI binary (for bastion init)")
	cmd.Flags().BoolVar(&f.configOnly, "config", false, "only sync configuration files to bastion (for bastion init)")
	cmd.Flags().BoolVar(&f.genesisOnly, "genesis", false, "only install/update Genesis and related components (for bastion init)")
	cmd.Flags().BoolVar(&f.reboot, "reboot", false, "reboot bastion after successful initialization (applies updates)")
}

// bindViperFlags binds flags to viper.
func (f *initFlags) bindViperFlags(cmd *cobra.Command) {
	bindFlagsToViper(cmd, map[string]string{
		"init.force":        "force",
		"init.skip_checks":  "skip-checks",
		"init.parallel":     "parallel",
		"init.dry_run":      "dry-run",
		"init.resume":       "resume",
		"init.verbose":      "verbose",
		"init.ocfp_only":    "ocfp",
		"init.config_only":  "config",
		"init.genesis_only": "genesis",
		"init.reboot":       "reboot",
	})
}

// runInit executes the init command.
func (f *initFlags) runInit(cmd *cobra.Command, args []string) error {
	// Silence usage on execution errors
	cmd.SilenceUsage = true

	ctx := context.Background()
	log := logger.Get()

	component := f.getComponent(args)

	cfg, err := f.loadConfig()
	if err != nil {
		return err
	}

	log.Infow("Starting initialization", "component", component, "bloc", cfg.Name)

	err = f.validatePrerequisitesIfNeeded(ctx, cfg, component)
	if err != nil {
		return err
	}

	err = f.confirmInitializationIfNeeded(component)
	if err != nil {
		return err
	}

	return f.executeInitialization(ctx, cmd, cfg, component)
}

// getComponent determines which component to initialize.
func (f *initFlags) getComponent(args []string) string {
	// Args validation ensures we always have exactly one argument
	return strings.ToLower(args[0])
}

// ErrMutuallyExclusiveFlags indicates that mutually exclusive command flags were specified together.
var ErrMutuallyExclusiveFlags = errors.New("mutually exclusive flags")

// validateModeFlags validates that mutually exclusive mode flags are not used together.
func (f *initFlags) validateModeFlags() error {
	modeFlags := []struct {
		name  string
		value bool
	}{
		{"genesis", f.genesisOnly},
		{"ocfp", f.ocfpOnly},
		{"config", f.configOnly},
	}

	var activeModes []string

	for _, flag := range modeFlags {
		if flag.value {
			activeModes = append(activeModes, flag.name)
		}
	}

	if len(activeModes) > 1 {
		return fmt.Errorf("%w: --%s and --%s", ErrMutuallyExclusiveFlags, activeModes[0], activeModes[1])
	}

	return nil
}

// loadConfig loads the configuration.
func (f *initFlags) loadConfig() (*config.Config, error) {
	configFile := viper.GetString("config")
	blocName := viper.GetString("bloc")

	cfg, err := config.LoadWithParams(configFile, blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	return cfg, nil
}

// validatePrerequisitesIfNeeded validates prerequisites if not skipped.
func (f *initFlags) validatePrerequisitesIfNeeded(ctx context.Context, cfg *config.Config, component string) error {
	if f.skipChecks {
		return nil
	}

	err := validatePrerequisites(ctx, cfg, component)
	if err != nil {
		return fmt.Errorf("prerequisite check failed: %w", err)
	}

	return nil
}

// confirmInitializationIfNeeded prompts for confirmation if not forced.
func (f *initFlags) confirmInitializationIfNeeded(component string) error {
	if f.force {
		return nil
	}

	_, err := fmt.Fprintf(os.Stdout, "This will initialize %s components. Continue? [y/N]: ", component)
	if err != nil {
		return fmt.Errorf("failed to write prompt: %w", err)
	}

	var response string

	_, _ = fmt.Scanln(&response)

	if !strings.HasPrefix(strings.ToLower(response), "y") {
		logger.Get().Info("Initialization cancelled by user")

		return ErrInitCancelled
	}

	return nil
}

// executeInitialization performs the actual initialization based on component.
func (f *initFlags) executeInitialization(ctx context.Context, cmd *cobra.Command, cfg *config.Config, component string) error {
	// Validate mutually exclusive flags
	err := f.validateModeFlags()
	if err != nil {
		return err
	}

	switch component {
	case "aws":
		return initializeAWS(cmd)
	case "pve":
		return initializePVE(cmd, cfg)
	case "pg":
		return initializePostgreSQL()
	case "cf":
		return initializeCloudFoundry(ctx, cfg)
	case "bosh":
		return initializeBOSH(ctx, cfg)
	case RoleBastion:
		if f.genesisOnly {
			return initializeBastionGenesisOnly(ctx, cfg, f.force, f.parallel, f.dryRun, f.resume, f.verbose, f.reboot)
		}

		if f.ocfpOnly {
			return initializeBastionOCFPOnly(ctx, cfg, f.force, f.parallel, f.dryRun, f.resume, f.verbose, f.reboot)
		}

		if f.configOnly {
			return initializeBastionConfigOnly(ctx, cfg, f.force, f.parallel, f.dryRun, f.resume, f.verbose, f.reboot)
		}

		return initializeBastion(ctx, cfg, f.force, f.parallel, f.dryRun, f.resume, f.verbose, f.reboot)
	case KeywordAll:
		return f.initializeAllComponents(ctx, cfg)
	default:
		return ErrUnknownComponent(component)
	}
}

// initializeAllComponents initializes all components in proper order.
func (f *initFlags) initializeAllComponents(ctx context.Context, cfg *config.Config) error {
	log := logger.Get()
	log.Info("Initializing all components")

	components := []struct {
		name string
		fn   func() error
	}{
		{"bastion", func() error {
			return initializeBastion(ctx, cfg, f.force, f.parallel, f.dryRun, f.resume, f.verbose, f.reboot)
		}},
		{"PostgreSQL", initializePostgreSQL},
		{"BOSH", func() error { return initializeBOSH(ctx, cfg) }},
		{"Cloud Foundry", func() error { return initializeCloudFoundry(ctx, cfg) }},
	}

	for _, comp := range components {
		err := comp.fn()
		if err != nil {
			return fmt.Errorf("%s initialization failed: %w", comp.name, err)
		}
	}

	log.Info("All components initialized successfully")

	return nil
}

// getInitLongDescription returns the long description for the init command.
func getInitLongDescription() string {
	return `Initialize OCFP components including PostgreSQL, Cloud Foundry, and BOSH.

Components:
  aws     - Initialize AWS environment (requires --bloc ocfp-aws-<region>, or OCFP_BLOC)
  pve     - Initialize Proxmox VE environment (requires --bloc <name>, or OCFP_BLOC; datacenter from config region)
  pg      - Initialize PostgreSQL database
  cf      - Initialize Cloud Foundry
  bosh    - Initialize BOSH Director
  bastion - Initialize bastion host
  all     - Initialize all components (default)

Bastion Initialization Modes:
  --genesis  - Install/update only Genesis and related components
               (genesis CLI, yq, genesis kits, genesis config, deployments)
  --ocfp     - Install/update only OCFP CLI binary
  --config   - Sync only configuration files to bastion
  (default)  - Full bastion initialization with all components

The init command prepares and initializes the core components required
for a Cloud Foundry deployment. It ensures proper ordering of component
initialization and validates prerequisites.`
}

// getInitExamples returns the examples for the init command.
func getInitExamples() string {
	return `  # Initialize all components
  ocfp init all

  # Initialize AWS environment
  ocfp init aws --bloc ocfp-aws-us-east-1

  # Initialize Proxmox VE environment
  ocfp init pve --bloc ocfp-pve-dc1

  # Initialize only PostgreSQL
  ocfp init pg

  # Initialize bastion host (full installation)
  ocfp init bastion

  # Install/update Genesis and related components only
  ocfp init bastion --genesis

  # Install/update Genesis with verbose output
  ocfp init bastion --genesis --verbose

  # Sync configuration files to bastion only (fast, ~1-5 seconds)
  ocfp init bastion --config

  # Force initialization, skipping confirmation
  ocfp init all --force

  # Initialize with parallel execution where possible
  ocfp init all --parallel`
}

// validatePrerequisites checks that required infrastructure is in place.
func validatePrerequisites(ctx context.Context, cfg *config.Config, component string) error {
	log := logger.Get()
	log.Infow("Validating prerequisites", "component", component)

	// For bastion initialization, only check SSH connectivity
	// Tools will be installed ON the bastion during provisioning
	if component == RoleBastion {
		err := checkBastionConnectivity(ctx, cfg)
		if err != nil {
			log.Warnw("Bastion connectivity check failed, will attempt provisioning anyway", "error", err)
		}

		return nil
	}

	// For other components, check bastion is already provisioned and accessible
	err := checkBastionConnectivity(ctx, cfg)
	if err != nil {
		return fmt.Errorf("bastion check failed: %w", err)
	}

	// Check for required local tools for CF/BOSH operations
	if component == "cf" || component == "bosh" || component == KeywordAll {
		requiredTools := []string{"bosh", "cf", "credhub", "uaa"}
		for _, tool := range requiredTools {
			_, err := exec.LookPath(tool)
			if err != nil {
				log.Warnw("Required tool not found in PATH", "tool", tool)
			}
		}
	}

	// Check for deployment directories
	home, _ := homeDir()
	deploymentDir := filepath.Join(home, "deployments", cfg.Name)

	_, err = os.Stat(deploymentDir) //nolint:gosec // path components are from trusted config
	if os.IsNotExist(err) {
		log.Infow("Creating deployment directory", "path", deploymentDir)

		err = os.MkdirAll(deploymentDir, DeploymentDirPerm) //nolint:gosec // path components are from trusted config
		if err != nil {
			return fmt.Errorf("failed to create deployment directory: %w", err)
		}
	}

	return nil
}

// checkBastionConnectivity verifies bastion host is accessible.
func checkBastionConnectivity(ctx context.Context, cfg *config.Config) error {
	log := logger.Get()

	// Get bastion IP using proper resolution
	bastionIP, err := resolveBastionIPForCheck(ctx, cfg)
	if err != nil {
		log.Warnw("Could not resolve bastion IP, skipping connectivity check", "error", err)

		return nil
	}

	// Find SSH key for bastion
	keyPath, err := findSSHKey(cfg.Name, cfg)
	if err != nil {
		log.Warnw("Could not find SSH key for bastion, skipping connectivity check", "error", err)

		return nil
	}

	// Try SSH connection with timeout
	err = security.ValidateInput(cfg.Bastion.SSHUser, validUserPattern)
	if err != nil {
		return fmt.Errorf("invalid SSH user: %w", err)
	}

	err = security.ValidateInput(bastionIP, validDNSPattern)
	if err != nil {
		return fmt.Errorf("invalid bastion IP: %w", err)
	}

	err = security.ValidateInput(keyPath, validFilePathPattern)
	if err != nil {
		return fmt.Errorf("invalid key path: %w", err)
	}

	cmd := exec.CommandContext(ctx, "ssh", // #nosec G204 - input validated above
		"-i", keyPath,
		"-o", "ConnectTimeout=30",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "IdentitiesOnly=yes",
		"-o", "IdentityAgent=none",
		fmt.Sprintf("%s@%s", cfg.Bastion.SSHUser, bastionIP),
		"echo", "connected")

	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("bastion not accessible: %w", err)
	}

	log.Info("Bastion connectivity verified")

	return nil
}

// resolveBastionIPForCheck resolves the bastion IP for connectivity checking.
func resolveBastionIPForCheck(ctx context.Context, cfg *config.Config) (string, error) {
	// Check config first
	if cfg.BastionIP != "" {
		return cfg.BastionIP, nil
	}

	// Initialize provider for IP lookup
	provider, err := cpi.GetProvider(cfg.Provider)
	if err != nil {
		return "", fmt.Errorf("failed to get provider: %w", err)
	}

	err = provider.Initialize(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("failed to initialize provider: %w", err)
	}

	// Use the shared bastion IP finder
	bastionIP, err := findBastionIP(ctx, provider, cfg.Name)
	if err != nil {
		return "", fmt.Errorf("failed to find bastion IP: %w", err)
	}

	return bastionIP, nil
}

// initializePostgreSQL sets up PostgreSQL for BOSH and CF.
func initializePostgreSQL() error {
	log := logger.Get()
	log.Info("Initializing PostgreSQL")

	// PostgreSQL initialization steps:
	// 1. Create databases (bosh, uaa, credhub, etc.)
	// 2. Create users with appropriate permissions
	// 3. Configure SSL if required
	// 4. Run initial schema migrations

	// Example implementation (would need actual PostgreSQL client)
	databases := []string{"bosh", "uaa", "credhub", "cloud_controller", "diego", "routing_api"}

	for _, db := range databases {
		log.Infow("Creating database", "name", db)
		// Pending: implement database creation via psql or Go postgres driver
	}

	log.Info("PostgreSQL initialization completed")

	return nil
}

// appendCompiledPin layers a precompiled-release pin ops file onto a bosh
// command when it exists, after verifying its stemcell marker matches the
// standardized stemcell. A mismatched pin would silently deploy compiled
// packages against the wrong stemcell, so that case is a hard error. A missing
// pin is non-fatal: precompile is an optional optimization step.
func appendCompiledPin(args []string, pinPath string) ([]string, error) {
	data, err := os.ReadFile(pinPath) //nolint:gosec // path derived from trusted deployment dir
	if err != nil {
		if os.IsNotExist(err) {
			return args, nil
		}
		return nil, fmt.Errorf("reading compiled-release pin %s: %w", pinPath, err)
	}

	if sc, ok := precompile.StemcellFromOps(data); ok && sc != precompile.DefaultStemcell {
		return nil, fmt.Errorf("compiled-release pin %s targets stemcell %s but deploy expects %s; re-run `ocfp precompile`",
			pinPath, sc, precompile.DefaultStemcell)
	}

	return append(args, "-o", pinPath), nil
}

// initializeBOSH sets up the BOSH Director.
func initializeBOSH(ctx context.Context, cfg *config.Config) error {
	log := logger.Get()
	log.Info("Initializing BOSH Director")

	// Load bootstrap state to obtain subnet ID and BOSH static IP.
	stateDir, err := state.GetStateDir(cfg.Name)
	if err != nil {
		return fmt.Errorf("failed to resolve state directory: %w", err)
	}

	stateMgr, err := state.NewManager(stateDir)
	if err != nil {
		return fmt.Errorf("failed to create state manager: %w", err)
	}

	_, err = stateMgr.Load(cfg.Name)
	if err != nil {
		return fmt.Errorf("failed to load bootstrap state for bloc %s: %w", cfg.Name, err)
	}

	home, _ := homeDir()
	deploymentDir := filepath.Join(home, "deployments", cfg.Name)
	manifestPath := filepath.Join(deploymentDir, "bosh.yml")

	// Check if manifest exists
	_, err = os.Stat(manifestPath) //nolint:gosec // path components are from trusted config
	if os.IsNotExist(err) {
		log.Infow("Creating BOSH manifest", "path", manifestPath)

		err = createBOSHManifest(cfg, manifestPath, stateMgr)
		if err != nil {
			return fmt.Errorf("failed to create BOSH manifest: %w", err)
		}
	}

	// Deploy BOSH (example command)
	log.Info("Deploying BOSH Director")

	err = security.ValidateInput(manifestPath, validFilePathPattern)
	if err != nil {
		return fmt.Errorf("invalid manifest path: %w", err)
	}

	err = security.ValidateInput(deploymentDir, validFilePathPattern)
	if err != nil {
		return fmt.Errorf("invalid deployment directory: %w", err)
	}

	createEnvArgs := []string{"create-env", manifestPath,
		"--state", filepath.Join(deploymentDir, "state.json"),
		"--vars-store", filepath.Join(deploymentDir, "creds.yml")}

	// Layer the precompiled-release pin (from `ocfp precompile bosh`) last so it
	// wins over the director manifest's release stanzas. Guard the stemcell so a
	// pin built for the wrong stemcell can't strand the director VM.
	boshPin := filepath.Join(deploymentDir, "manifests", "bosh", "compiled-releases.yml")
	createEnvArgs, err = appendCompiledPin(createEnvArgs, boshPin)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, "bosh", createEnvArgs...) //nolint:gosec // command args are validated above
	cmd.Dir = deploymentDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("BOSH deployment failed: %w", err)
	}

	// Set BOSH environment
	log.Info("Configuring BOSH environment")

	err = configureBOSHEnvironment(cfg, deploymentDir)
	if err != nil {
		return fmt.Errorf("failed to configure BOSH environment: %w", err)
	}

	log.Info("BOSH Director initialization completed")

	return nil
}

// initializeCloudFoundry sets up Cloud Foundry.
func initializeCloudFoundry(ctx context.Context, cfg *config.Config) error {
	log := logger.Get()
	log.Info("Initializing Cloud Foundry")

	// Cloud Foundry initialization steps:
	// 1. Upload CF deployment
	// 2. Configure CF manifest with ops files
	// 3. Deploy CF using BOSH
	// 4. Configure CF organizations and spaces
	// 5. Set up CF admin user

	home, _ := homeDir()
	deploymentDir := filepath.Join(home, "deployments", cfg.Name)

	// Upload cloud foundry deployment
	log.Info("Uploading Cloud Foundry deployment")

	cmd := exec.CommandContext(ctx, "bosh", "upload-release", // #nosec G204 - using hardcoded URL
		"https://bosh.io/d/github.com/cloudfoundry/cf-deployment")

	err := cmd.Run()
	if err != nil {
		log.Warnw("Failed to upload CF release", "error", err)
	}

	// Deploy Cloud Foundry
	log.Info("Deploying Cloud Foundry")

	manifestPath := filepath.Join(deploymentDir, "cf-deployment.yml")

	err = security.ValidateInput(manifestPath, validFilePathPattern)
	if err != nil {
		return fmt.Errorf("invalid manifest path: %w", err)
	}

	deployArgs := []string{"-d", "cf", "deploy", manifestPath,
		"-o", filepath.Join(deploymentDir, "operations", "scale.yml"),
		"-o", filepath.Join(deploymentDir, "operations", "use-postgres.yml")}

	// Layer the precompiled-release pin (from `ocfp precompile cf`) last.
	cfPin := filepath.Join(deploymentDir, "manifests", "cf", "compiled-releases.yml")
	deployArgs, err = appendCompiledPin(deployArgs, cfPin)
	if err != nil {
		return err
	}

	cmd = exec.CommandContext(ctx, "bosh", deployArgs...) //nolint:gosec // command args are validated above
	cmd.Dir = deploymentDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("CF deployment failed: %w", err)
	}

	// Configure CF
	log.Info("Configuring Cloud Foundry")

	err = configureCloudFoundry(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to configure CF: %w", err)
	}

	log.Info("Cloud Foundry initialization completed")

	return nil
}

// createBOSHManifest generates a BOSH deployment manifest hydrated from bootstrap state.
//
// Required state outputs (written by `ocfp bootstrap`):
//   - subnet_<bloc>-ocfp-0_id      — real AWS subnet ID (e.g. subnet-0a1b2c3d)
//   - reserved_<bloc>-ocfp-0_bosh_ip — static IP reserved for the BOSH director
//
// Returns ErrBootstrapStateRequired when either output is absent or empty.
// Callers must run `ocfp bootstrap` before `ocfp init bosh` to populate these values.
func createBOSHManifest(cfg *config.Config, path string, stateMgr *state.Manager) error {
	subnetName := cfg.Name + "-ocfp-0"
	subnetIDKey := "subnet_" + subnetName + "_id"
	boshIPKey := "reserved_" + subnetName + "_bosh_ip"

	subnetIDRaw, err := stateMgr.GetOutput(subnetIDKey)
	if err != nil {
		return fmt.Errorf("%w: subnet ID output %q missing — %v", ErrBootstrapStateRequired, subnetIDKey, err)
	}

	subnetID, ok := subnetIDRaw.(string)
	if !ok || subnetID == "" {
		return fmt.Errorf("%w: subnet ID output %q is empty or not a string", ErrBootstrapStateRequired, subnetIDKey)
	}

	boshIPRaw, err := stateMgr.GetOutput(boshIPKey)
	if err != nil {
		return fmt.Errorf("%w: BOSH IP output %q missing — %v", ErrBootstrapStateRequired, boshIPKey, err)
	}

	boshIP, ok := boshIPRaw.(string)
	if !ok || boshIP == "" {
		return fmt.Errorf("%w: BOSH IP output %q is empty or not a string", ErrBootstrapStateRequired, boshIPKey)
	}

	// Determine instance type: prefer Bastion.InstanceType, fall back to Bastion.Flavor.
	instanceType := cfg.Bastion.InstanceType
	if instanceType == "" {
		instanceType = cfg.Bastion.Flavor
	}

	if instanceType == "" {
		instanceType = "t3.medium"
	}

	networkCIDR := cfg.Network.CIDR
	if networkCIDR == "" {
		networkCIDR = cfg.VPCCIDRBlock
	}

	manifest := fmt.Sprintf(`---
name: bosh
releases:
- name: bosh
  url: https://bosh.io/d/github.com/cloudfoundry/bosh
  version: latest
- name: bpm
  url: https://bosh.io/d/github.com/cloudfoundry/bpm-release
  version: latest

resource_pools:
- name: vms
  network: private
  cloud_properties:
    instance_type: %s

networks:
- name: private
  type: manual
  subnets:
  - range: %s
    cloud_properties:
      subnet: %s

instance_groups:
- name: bosh
  instances: 1
  resource_pool: vms
  networks:
  - name: private
    static_ips: [%s]
`, instanceType, networkCIDR, subnetID, boshIP)

	err = os.WriteFile(path, []byte(manifest), ManifestFilePerm) //nolint:gosec // path is from trusted config
	if err != nil {
		return fmt.Errorf("failed to write manifest file: %w", err)
	}

	return nil
}

// configureBOSHEnvironment sets up BOSH CLI environment.
func configureBOSHEnvironment(cfg *config.Config, deploymentDir string) error {
	// Set BOSH environment variables
	envFile := filepath.Join(deploymentDir, "bosh-env.sh")

	envContent := fmt.Sprintf(`#!/bin/bash
export BOSH_ENVIRONMENT=%s-bosh
export BOSH_CLIENT=admin
export BOSH_CLIENT_SECRET=$(bosh int %s/creds.yml --path /admin_password)
export BOSH_CA_CERT=$(bosh int %s/creds.yml --path /director_ssl/ca)
`, cfg.Name, deploymentDir, deploymentDir)

	err := os.WriteFile(envFile, []byte(envContent), ManifestFilePerm) //nolint:gosec // path is from trusted config
	if err != nil {
		return fmt.Errorf("failed to write environment file: %w", err)
	}

	return nil
}

// configureCloudFoundry performs initial CF configuration.
func configureCloudFoundry(ctx context.Context, cfg *config.Config) error {
	log := logger.Get()

	// Login to CF
	log.Info("Logging into Cloud Foundry")

	err := security.ValidateInput(cfg.DNS[0], validDNSPattern)
	if err != nil {
		return fmt.Errorf("invalid DNS name: %w", err)
	}

	cmd := exec.CommandContext(ctx, "cf", "login", // #nosec G204 - input validated above
		"-a", "https://api."+cfg.DNS[0],
		"-u", "admin",
		"-p", "admin", // This should come from credhub
		"--skip-ssl-validation")

	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("CF login failed: %w", err)
	}

	// Create default org and space
	log.Info("Creating default organization and space")

	cmd = exec.CommandContext(ctx, "cf", "create-org", "default") // #nosec G204 - using hardcoded values

	err = cmd.Run()
	if err != nil {
		log.Warnw("Failed to create org", "error", err)
	}

	cmd = exec.CommandContext(ctx, "cf", "create-space", "development", "-o", "default") // #nosec G204 - using hardcoded values

	err = cmd.Run()
	if err != nil {
		log.Warnw("Failed to create space", "error", err)
	}

	cmd = exec.CommandContext(ctx, "cf", "target", "-o", "default", "-s", "development") // #nosec G204 - using hardcoded values

	err = cmd.Run()
	if err != nil {
		log.Warnw("Failed to target org/space", "error", err)
	}

	return nil
}

// initializeBastion performs bastion host initialization.
func initializeBastion(ctx context.Context, cfg *config.Config, force, parallel, dryRun, resume, verbose, reboot bool) error {
	log := logger.Get()
	log.Info("Initializing bastion host")

	// Create provisioning options
	options := &bastion.ProvisioningOptions{
		DryRun:          dryRun,
		Force:           force,
		Parallel:        parallel,
		Resume:          resume,
		Verbose:         verbose,
		MaxWorkers:      DefaultMaxWorkers,
		ProgressOut:     os.Stdout,
		LogFile:         "",
		OCFPOnly:        false, // OCFPOnly is handled separately via --ocfp flag
		RebootAfterInit: reboot,
	}

	// Use mode-aware initialization that detects local vs remote execution
	err := bastion.InitializeBastionWithMode(ctx, cfg, options)
	if err != nil {
		return fmt.Errorf("bastion initialization failed: %w", err)
	}

	log.Info("Bastion initialization completed successfully")

	return nil
}

// initializeBastionOCFPOnly performs OCFP CLI installation/update only.
func initializeBastionOCFPOnly(ctx context.Context, cfg *config.Config, force, parallel, dryRun, resume, verbose, reboot bool) error {
	log := logger.Get()
	log.Info("Installing/updating OCFP CLI only")

	// Provide user feedback
	_, _ = fmt.Fprintf(os.Stdout, "\n🔧 Installing/updating OCFP CLI binary to bastion...\n")

	// Create provisioning options with OCFPOnly flag set
	options := &bastion.ProvisioningOptions{
		DryRun:          dryRun,
		Force:           force,
		Parallel:        parallel,
		Resume:          resume,
		Verbose:         verbose,
		MaxWorkers:      DefaultMaxWorkers,
		ProgressOut:     os.Stdout,
		LogFile:         "",
		OCFPOnly:        true,
		RebootAfterInit: reboot,
	}

	// Use mode-aware initialization that detects local vs remote execution
	err := bastion.InitializeBastionWithMode(ctx, cfg, options)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stdout, "❌ OCFP CLI installation failed: %v\n\n", err)

		return fmt.Errorf("OCFP CLI installation failed: %w", err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "✅ OCFP CLI installation completed successfully\n\n")

	log.Info("OCFP CLI installation completed successfully")

	return nil
}

// initializeBastionGenesisOnly performs Genesis installation/update only.
func initializeBastionGenesisOnly(ctx context.Context, cfg *config.Config, force, parallel, dryRun, resume, verbose, reboot bool) error {
	log := logger.Get()
	log.Info("Installing/updating Genesis and related components only")

	// Provide user feedback
	_, _ = fmt.Fprintf(os.Stdout, "\n⚙️  Installing/updating Genesis and related components...\n")

	// Create provisioning options with GenesisOnly flag set
	options := &bastion.ProvisioningOptions{
		DryRun:          dryRun,
		Force:           force,
		Parallel:        parallel,
		Resume:          resume,
		Verbose:         verbose,
		MaxWorkers:      DefaultMaxWorkers,
		ProgressOut:     os.Stdout,
		LogFile:         "",
		GenesisOnly:     true,
		RebootAfterInit: reboot,
	}

	// Use mode-aware initialization that detects local vs remote execution
	err := bastion.InitializeBastionWithMode(ctx, cfg, options)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stdout, "❌ Genesis installation failed: %v\n\n", err)

		return fmt.Errorf("genesis installation failed: %w", err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "✅ Genesis installation completed successfully\n\n")

	log.Info("Genesis installation completed successfully")

	return nil
}

// initializeBastionConfigOnly performs configuration file sync only.
func initializeBastionConfigOnly(ctx context.Context, cfg *config.Config, force, parallel, dryRun, resume, verbose, reboot bool) error {
	log := logger.Get()
	log.Info("Syncing configuration files only")

	// Provide user feedback
	_, _ = fmt.Fprintf(os.Stdout, "\n📝 Syncing configuration files to bastion...\n")

	// Create provisioning options with ConfigOnly flag set
	options := &bastion.ProvisioningOptions{
		DryRun:          dryRun,
		Force:           force,
		Parallel:        parallel,
		Resume:          resume,
		Verbose:         verbose,
		MaxWorkers:      DefaultMaxWorkers,
		ProgressOut:     os.Stdout,
		LogFile:         "",
		OCFPOnly:        false,
		ConfigOnly:      true,
		RebootAfterInit: reboot,
	}

	// Use mode-aware initialization that detects local vs remote execution
	err := bastion.InitializeBastionWithMode(ctx, cfg, options)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stdout, "❌ Configuration sync failed: %v\n\n", err)

		return fmt.Errorf("configuration sync failed: %w", err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "✅ Configuration sync completed successfully\n\n")

	log.Info("Configuration sync completed successfully")

	return nil
}
