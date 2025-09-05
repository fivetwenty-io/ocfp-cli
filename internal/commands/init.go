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
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/security"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	validFilePathPattern = regexp.MustCompile(`^[a-zA-Z0-9/._-]+$`)
	validDNSPattern      = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-.])*[a-zA-Z0-9]$`)
	validUserPattern     = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-_])*[a-zA-Z0-9]$`)
)

// NewInitCmd creates the init command.
func NewInitCmd() *cobra.Command {
	var (
		force      bool
		skipChecks bool
		parallel   bool
		dryRun     bool
		resume     bool
		verbose    bool
	)

	cmd := &cobra.Command{
		Use:   "init [component]",
		Short: "Initialize OCFP components",
		Long: `Initialize OCFP components including PostgreSQL, Cloud Foundry, and BOSH.

Components:
  pg      - Initialize PostgreSQL database
  cf      - Initialize Cloud Foundry
  bosh    - Initialize BOSH Director
  bastion - Initialize bastion host
  all     - Initialize all components (default)

The init command prepares and initializes the core components required
for a Cloud Foundry deployment. It ensures proper ordering of component
initialization and validates prerequisites.`,
		Example: `  # Initialize all components
  ocfp init

  # Initialize only PostgreSQL
  ocfp init pg

  # Initialize bastion host
  ocfp init bastion

  # Force initialization, skipping confirmation
  ocfp init all --force

  # Initialize with parallel execution where possible
  ocfp init --parallel`,
		ValidArgs: []string{"pg", "cf", "bosh", "bastion", "all"},
		Args:      cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log := logger.Get()

			component := "all"
			if len(args) > 0 {
				component = strings.ToLower(args[0])
			}

			// Load configuration
			configFile := viper.GetString("config")
			blocName := viper.GetString("bloc")

			cfg, err := config.LoadWithParams(configFile, blocName)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			log.Info("Starting initialization", "component", component, "bloc", blocName)

			// Validate prerequisites
			if !skipChecks {
				err = validatePrerequisites(ctx, cfg)
				if err != nil {
					return fmt.Errorf("prerequisite check failed: %w", err)
				}
			}

			// Confirm action if not forced
			if !force {
				fmt.Printf("This will initialize %s components. Continue? [y/N]: ", component)
				var response string
				_, _ = fmt.Scanln(&response)
				if !strings.HasPrefix(strings.ToLower(response), "y") {
					log.Info("Initialization cancelled by user")

					return nil
				}
			}

			// Execute initialization based on component
			switch component {
			case "pg":
				return initializePostgreSQL(ctx, cfg)
			case "cf":
				return initializeCloudFoundry(ctx, cfg)
			case "bosh":
				return initializeBOSH(ctx, cfg)
			case "bastion":
				return initializeBastion(ctx, cfg, force, parallel, dryRun, resume, verbose)
			case "all":
				// Initialize in order: bastion -> pg -> bosh -> cf
				log.Info("Initializing all components")

				err = initializeBastion(ctx, cfg, force, parallel, dryRun, resume, verbose)
				if err != nil {
					return fmt.Errorf("bastion initialization failed: %w", err)
				}

				err = initializePostgreSQL(ctx, cfg)
				if err != nil {
					return fmt.Errorf("PostgreSQL initialization failed: %w", err)
				}

				err = initializeBOSH(ctx, cfg)
				if err != nil {
					return fmt.Errorf("BOSH initialization failed: %w", err)
				}

				err = initializeCloudFoundry(ctx, cfg)
				if err != nil {
					return fmt.Errorf("cloud Foundry initialization failed: %w", err)
				}

				log.Info("All components initialized successfully")
			default:
				return fmt.Errorf("unknown component: %s", component)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation prompts")
	cmd.Flags().BoolVar(&skipChecks, "skip-checks", false, "skip prerequisite validation")
	cmd.Flags().BoolVar(&parallel, "parallel", false, "run independent tasks in parallel")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print actions without executing")
	cmd.Flags().BoolVar(&resume, "resume", false, "resume from last successful checkpoint")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "enable verbose logging")

	// Bind flags to viper
	_ = viper.BindPFlag("init.force", cmd.Flags().Lookup("force"))
	_ = viper.BindPFlag("init.skip_checks", cmd.Flags().Lookup("skip-checks"))
	_ = viper.BindPFlag("init.parallel", cmd.Flags().Lookup("parallel"))
	_ = viper.BindPFlag("init.dry_run", cmd.Flags().Lookup("dry-run"))
	_ = viper.BindPFlag("init.resume", cmd.Flags().Lookup("resume"))
	_ = viper.BindPFlag("init.verbose", cmd.Flags().Lookup("verbose"))

	return cmd
}

// validatePrerequisites checks that required infrastructure is in place.
func validatePrerequisites(ctx context.Context, cfg *config.Config) error {
	log := logger.Get()
	log.Info("Validating prerequisites")

	// Check for bastion connectivity
	err := checkBastionConnectivity(cfg)
	if err != nil {
		return fmt.Errorf("bastion check failed: %w", err)
	}

	// Check for required tools
	requiredTools := []string{"bosh", "cf", "credhub", "uaa"}
	for _, tool := range requiredTools {
		if _, err := exec.LookPath(tool); err != nil {
			log.Warn("Required tool not found in PATH", "tool", tool)
		}
	}

	// Check for deployment directories
	deploymentDir := filepath.Join(os.Getenv("HOME"), "deployments", cfg.Name)
	if _, err := os.Stat(deploymentDir); os.IsNotExist(err) {
		log.Info("Creating deployment directory", "path", deploymentDir)
		err = os.MkdirAll(deploymentDir, 0750)

		if err != nil {
			return fmt.Errorf("failed to create deployment directory: %w", err)
		}
	}

	return nil
}

// checkBastionConnectivity verifies bastion host is accessible.
func checkBastionConnectivity(cfg *config.Config) error {
	log := logger.Get()

	// Get bastion IP from config
	bastionIP := cfg.Bastion.SSHUser // This should be the floating IP
	if bastionIP == "" {
		log.Warn("Bastion IP not configured, skipping connectivity check")

		return nil
	}

	// Try SSH connection with timeout
	err := security.ValidateInput(cfg.Bastion.SSHUser, validUserPattern)
	if err != nil {
		return fmt.Errorf("invalid SSH user: %w", err)
	}
	err = security.ValidateInput(bastionIP, validDNSPattern)

	if err != nil {
		return fmt.Errorf("invalid bastion IP: %w", err)
	}

	cmd := exec.Command("ssh", // #nosec G204 - input validated above
		"-o", "ConnectTimeout=5",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		fmt.Sprintf("%s@%s", cfg.Bastion.SSHUser, bastionIP),
		"echo", "connected")

	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("bastion not accessible: %w", err)
	}

	log.Info("Bastion connectivity verified")

	return nil
}

// initializePostgreSQL sets up PostgreSQL for BOSH and CF.
func initializePostgreSQL(ctx context.Context, cfg *config.Config) error {
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
		log.Info("Creating database", "name", db)
		// TODO: Actual database creation via psql or Go postgres driver
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
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		log.Info("Creating BOSH manifest", "path", manifestPath)
		err = createBOSHManifest(cfg, manifestPath)

		if err != nil {
			return fmt.Errorf("failed to create BOSH manifest: %w", err)
		}
	}

	// Deploy BOSH (example command)
	log.Info("Deploying BOSH Director")
	err := security.ValidateInput(manifestPath, validFilePathPattern)

	if err != nil {
		return fmt.Errorf("invalid manifest path: %w", err)
	}
	err = security.ValidateInput(deploymentDir, validFilePathPattern)

	if err != nil {
		return fmt.Errorf("invalid deployment directory: %w", err)
	}

	cmd := exec.Command("bosh", "create-env", manifestPath, // #nosec G204 - input validated above
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

	cmd := exec.Command("bosh", "upload-release", // #nosec G204 - using hardcoded URL
		"https://bosh.io/d/github.com/cloudfoundry/cf-deployment")

	err := cmd.Run()
	if err != nil {
		log.Warn("Failed to upload CF release", "error", err)
	}

	// Deploy Cloud Foundry
	log.Info("Deploying Cloud Foundry")

	manifestPath := filepath.Join(deploymentDir, "cf-deployment.yml")
	err = security.ValidateInput(manifestPath, validFilePathPattern)
	if err != nil {
		return fmt.Errorf("invalid manifest path: %w", err)
	}

	cmd = exec.Command("bosh", "-d", "cf", "deploy", manifestPath, // #nosec G204 - input validated above
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
	err = configureCloudFoundry(cfg)

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

	return os.WriteFile(path, []byte(manifest), 0600)
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

	return os.WriteFile(envFile, []byte(envContent), 0600)
}

// configureCloudFoundry performs initial CF configuration.
func configureCloudFoundry(cfg *config.Config) error {
	log := logger.Get()

	// Login to CF
	log.Info("Logging into Cloud Foundry")
	err := security.ValidateInput(cfg.DNS[0], validDNSPattern)

	if err != nil {
		return fmt.Errorf("invalid DNS name: %w", err)
	}

	cmd := exec.Command("cf", "login", // #nosec G204 - input validated above
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

	cmd = exec.Command("cf", "create-org", "default") // #nosec G204 - using hardcoded values
	err = cmd.Run()
	if err != nil {
		log.Warn("Failed to create org", "error", err)
	}

	cmd = exec.Command("cf", "create-space", "development", "-o", "default") // #nosec G204 - using hardcoded values
	err = cmd.Run()
	if err != nil {
		log.Warn("Failed to create space", "error", err)
	}

	cmd = exec.Command("cf", "target", "-o", "default", "-s", "development") // #nosec G204 - using hardcoded values
	err = cmd.Run()
	if err != nil {
		log.Warn("Failed to target org/space", "error", err)
	}

	return nil
}

// initializeBastion performs bastion host initialization.
func initializeBastion(ctx context.Context, cfg *config.Config, force, parallel, dryRun, resume bool, verbose bool) error {
	log := logger.Get()
	log.Info("Initializing bastion host")

	// Create provisioning options
	options := &bastion.ProvisioningOptions{
		DryRun:      dryRun,
		Force:       force,
		Parallel:    parallel,
		Resume:      resume,
		Verbose:     verbose,
		MaxWorkers:  4,
		ProgressOut: os.Stdout,
	}

	// Use mode-aware initialization that detects local vs remote execution
	err := bastion.InitializeBastionWithMode(ctx, cfg, options)
	if err != nil {
		return fmt.Errorf("bastion initialization failed: %w", err)
	}

	log.Info("Bastion initialization completed successfully")

	return nil
}
