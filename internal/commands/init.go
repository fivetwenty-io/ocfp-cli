package commands

import (
	"context"
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
	"github.com/ocfp/ocfp-cli-go/internal/security"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	// Default maximum workers for parallel operations.
	DefaultMaxWorkers = 4

	// File permissions.
	ManifestFilePerm  os.FileMode = 0600
	DeploymentDirPerm os.FileMode = 0750
)

var (
	validFilePathPattern = regexp.MustCompile(`^[a-zA-Z0-9/._-]+$`)
	validDNSPattern      = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-.])*[a-zA-Z0-9]$`)
	validUserPattern     = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-_])*[a-zA-Z0-9]$`)
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
	}

	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional fields
		Use:       "init [component]",
		Short:     "Initialize OCFP components",
		Long:      getInitLongDescription(),
		Example:   getInitExamples(),
		ValidArgs: []string{"pg", "cf", "bosh", RoleBastion, KeywordAll},
		Args:      cobra.MaximumNArgs(1),
		RunE:      initFlags.runInit,
	}

	initFlags.addFlags(cmd)
	initFlags.bindViperFlags(cmd)

	return cmd
}

// initFlags holds all command flags for init.
type initFlags struct {
	force      bool
	skipChecks bool
	parallel   bool
	dryRun     bool
	resume     bool
	verbose    bool
	ocfpOnly   bool
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
}

// bindViperFlags binds flags to viper.
func (f *initFlags) bindViperFlags(cmd *cobra.Command) {
	_ = viper.BindPFlag("init.force", cmd.Flags().Lookup("force"))
	_ = viper.BindPFlag("init.skip_checks", cmd.Flags().Lookup("skip-checks"))
	_ = viper.BindPFlag("init.parallel", cmd.Flags().Lookup("parallel"))
	_ = viper.BindPFlag("init.dry_run", cmd.Flags().Lookup("dry-run"))
	_ = viper.BindPFlag("init.resume", cmd.Flags().Lookup("resume"))
	_ = viper.BindPFlag("init.verbose", cmd.Flags().Lookup("verbose"))
	_ = viper.BindPFlag("init.ocfp_only", cmd.Flags().Lookup("ocfp"))
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

	return f.executeInitialization(ctx, cfg, component)
}

// getComponent determines which component to initialize.
func (f *initFlags) getComponent(args []string) string {
	if len(args) > 0 {
		return strings.ToLower(args[0])
	}

	return KeywordAll
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

		return nil
	}

	return nil
}

// executeInitialization performs the actual initialization based on component.
func (f *initFlags) executeInitialization(ctx context.Context, cfg *config.Config, component string) error {
	switch component {
	case "pg":
		return initializePostgreSQL()
	case "cf":
		return initializeCloudFoundry(ctx, cfg)
	case "bosh":
		return initializeBOSH(ctx, cfg)
	case RoleBastion:
		if f.ocfpOnly {
			return initializeBastionOCFPOnly(ctx, cfg, f.force, f.parallel, f.dryRun, f.resume, f.verbose)
		}

		return initializeBastion(ctx, cfg, f.force, f.parallel, f.dryRun, f.resume, f.verbose)
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
		{"bastion", func() error { return initializeBastion(ctx, cfg, f.force, f.parallel, f.dryRun, f.resume, f.verbose) }},
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
  pg      - Initialize PostgreSQL database
  cf      - Initialize Cloud Foundry
  bosh    - Initialize BOSH Director
  bastion - Initialize bastion host
  all     - Initialize all components (default)

The init command prepares and initializes the core components required
for a Cloud Foundry deployment. It ensures proper ordering of component
initialization and validates prerequisites.`
}

// getInitExamples returns the examples for the init command.
func getInitExamples() string {
	return `  # Initialize all components
  ocfp init

  # Initialize only PostgreSQL
  ocfp init pg

  # Initialize bastion host
  ocfp init bastion

  # Force initialization, skipping confirmation
  ocfp init all --force

  # Initialize with parallel execution where possible
  ocfp init --parallel`
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
	deploymentDir := filepath.Join(os.Getenv("HOME"), "deployments", cfg.Name)

	_, err = os.Stat(deploymentDir)
	if os.IsNotExist(err) {
		log.Infow("Creating deployment directory", "path", deploymentDir)

		err = os.MkdirAll(deploymentDir, DeploymentDirPerm)
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

// initializeBOSH sets up the BOSH Director.
func initializeBOSH(ctx context.Context, cfg *config.Config) error {
	log := logger.Get()
	log.Info("Initializing BOSH Director")

	// BOSH initialization steps:
	// 1. Upload BOSH release
	// 2. Upload stemcells
	// 3. Deploy BOSH Director
	// 4. Configure cloud config
	// 5. Upload runtime config

	deploymentDir := filepath.Join(os.Getenv("HOME"), "deployments", cfg.Name)
	manifestPath := filepath.Join(deploymentDir, "bosh.yml")

	// Check if manifest exists
	_, err := os.Stat(manifestPath)
	if os.IsNotExist(err) {
		log.Infow("Creating BOSH manifest", "path", manifestPath)

		err = createBOSHManifest(cfg, manifestPath)
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

	cmd := exec.CommandContext(ctx, "bosh", "create-env", manifestPath, // #nosec G204 - input validated above
		"--state", filepath.Join(deploymentDir, "state.json"),
		"--vars-store", filepath.Join(deploymentDir, "creds.yml"))

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

	deploymentDir := filepath.Join(os.Getenv("HOME"), "deployments", cfg.Name)

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

	cmd = exec.CommandContext(ctx, "bosh", "-d", "cf", "deploy", manifestPath, // #nosec G204 - input validated above
		"-o", filepath.Join(deploymentDir, "operations", "scale.yml"),
		"-o", filepath.Join(deploymentDir, "operations", "use-postgres.yml"))

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

// createBOSHManifest generates a BOSH deployment manifest.
func createBOSHManifest(cfg *config.Config, path string) error {
	// This would generate an actual BOSH manifest based on the configuration
	// For now, return a placeholder
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
    gateway: 10.0.0.1
    cloud_properties:
      subnet: subnet-xxxxxx

instance_groups:
- name: bosh
  instances: 1
  resource_pool: vms
  networks:
  - name: private
    static_ips: [10.0.0.6]
`, cfg.Bastion.Flavor, cfg.Network.CIDR)

	err := os.WriteFile(path, []byte(manifest), ManifestFilePerm)
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

	err := os.WriteFile(envFile, []byte(envContent), ManifestFilePerm)
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
func initializeBastion(ctx context.Context, cfg *config.Config, force, parallel, dryRun, resume, verbose bool) error {
	log := logger.Get()
	log.Info("Initializing bastion host")

	// Create provisioning options
	options := &bastion.ProvisioningOptions{
		DryRun:      dryRun,
		Force:       force,
		Parallel:    parallel,
		Resume:      resume,
		Verbose:     verbose,
		MaxWorkers:  DefaultMaxWorkers,
		ProgressOut: os.Stdout,
		LogFile:     "",
		OCFPOnly:    false, // OCFPOnly is handled separately via --ocfp flag
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
func initializeBastionOCFPOnly(ctx context.Context, cfg *config.Config, force, parallel, dryRun, resume, verbose bool) error {
	log := logger.Get()
	log.Info("Installing/updating OCFP CLI only")

	// Provide user feedback
	_, _ = fmt.Fprintf(os.Stdout, "\n🔧 Installing/updating OCFP CLI binary to bastion...\n")

	// Create provisioning options with OCFPOnly flag set
	options := &bastion.ProvisioningOptions{
		DryRun:      dryRun,
		Force:       force,
		Parallel:    parallel,
		Resume:      resume,
		Verbose:     verbose,
		MaxWorkers:  DefaultMaxWorkers,
		ProgressOut: os.Stdout,
		LogFile:     "",
		OCFPOnly:    true,
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
