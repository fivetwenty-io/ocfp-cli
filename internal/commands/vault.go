package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bastion"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

const (
	// VaultOutputFileMode is the file permission mode for vault output files.
	VaultOutputFileMode = 0600

	// VaultDirMode is the file permission mode for vault directories.
	VaultDirMode = 0750

	// VaultInceptionPort is the default port for the inception vault server.
	VaultInceptionPort = 8234
	// TestVaultInceptionPort is the port for testing the inception vault server.
	TestVaultInceptionPort = 8235
	// VaultInceptionLogDir is the directory for vault inception logs (relative to OcfpHome).
	VaultInceptionLogDir = "logs/vault"
	// VaultInceptionLogFile is the filename for vault inception logs.
	VaultInceptionLogFile = "vault-inception.log"
	// MaxVaultReadyAttempts is the maximum number of attempts to wait for vault readiness.
	MaxVaultReadyAttempts = 30

	// VaultCleanupWait is the duration to wait after process termination before cleanup.
	VaultCleanupWait = 2 * time.Second

	// VaultInitWait is the duration to wait after vault startup before initialization.
	VaultInitWait = 5 * time.Second
)

var (
	// ErrSafeNotFound indicates the safe CLI is not installed.
	ErrSafeNotFound = errors.New("'safe' command not found - please install safe CLI")

	// ErrTmuxNotFound indicates tmux is not installed.
	ErrTmuxNotFound = errors.New("'tmux' command not found - please install tmux")

	// ErrVaultNotFound indicates the vault CLI is not installed.
	ErrVaultNotFound = errors.New("'vault' command not found - please install vault")
	// ErrVaultNotReady indicates vault did not become ready within the timeout period.
	ErrVaultNotReady = errors.New("vault did not become ready within timeout")
	// ErrTmuxFailed indicates failure to create a tmux session for vault.
	ErrTmuxFailed = errors.New("failed to create tmux session")
	// ErrVaultStartupError indicates a vault startup error was detected in tmux output.
	ErrVaultStartupError = errors.New("vault startup error detected in tmux output")
	// ErrVaultTargetVerify indicates failure to verify the inception vault target.
	ErrVaultTargetVerify = errors.New("failed to verify inception vault target")
)

// NewVaultCmd creates the vault command.
func NewVaultCmd() *cobra.Command {
	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "vault",
		Short: "Manage vault operations",
		Long: `Manage vault operations including secret population, inception, and migration.

The vault command provides utilities for managing secrets in HashiCorp Vault
or CredHub for BOSH and Cloud Foundry deployments.`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Call parent's PersistentPreRun to ensure viper is properly set up
			if cmd.Parent() != nil && cmd.Parent().PersistentPreRun != nil {
				cmd.Parent().PersistentPreRun(cmd, args)
			}

			// Get bloc name directly from root command's flags
			blocName := ""

			if rootCmd := cmd.Root(); rootCmd != nil {
				if blocFlag := rootCmd.PersistentFlags().Lookup("bloc"); blocFlag != nil {
					blocName = blocFlag.Value.String()
					// Set in viper for consistency
					viper.Set("bloc", blocName)
				}
			}

			// Use new path structure: ~/.ocfp (not ~/.ocfp/logs)
			logDir := config.OcfpHome()

			return logger.Initialize(logger.Config{
				Level:      viper.GetString("log_level"),
				Debug:      viper.GetBool("debug"),
				Verbose:    viper.GetBool("verbose"),
				Trace:      viper.GetBool("trace"),
				NoLog:      viper.GetBool("no_log"),
				LogDir:     logDir,
				BlocName:   blocName,
				Command:    "vault",
				Subcommand: "", // Will be set by subcommand prerun if applicable
				RequestID:  os.Getenv("OCFP_REQUEST_ID"),
			})
		},
	}

	// Add subcommands
	cmd.AddCommand(newVaultPopulateCmd())
	cmd.AddCommand(newVaultInceptionCmd())
	cmd.AddCommand(newVaultTeardownCmd())
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

	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional fields
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
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultPopulate(cmd, args, fromFile, force)
		},
	}

	cmd.Flags().StringVar(&vaultPath, "vault-path", "", "vault path prefix")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "load secrets from file")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing secrets")
	cmd.Flags().Bool("dry-run", false, "preview actions without making changes")

	return cmd
}

// runVaultPopulate executes the vault populate command.
func runVaultPopulate(cmd *cobra.Command, args []string, fromFile string, force bool) error {
	log := logger.Get()
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	// Load configuration and create manager
	manager, err := loadConfigAndManager()
	if err != nil {
		return err
	}

	defer func() { _ = manager.Close() }()

	// Handle subcommand (public-ips)
	var subcommand string
	if len(args) > 0 {
		subcommand = args[0]
	}

	// Detect output mode for progress reporting
	mode := bastion.SelectOutputMode(os.Stdout)

	// Create progress tracking structure (the provider will report detailed phases)
	progress := &bastion.ProvisioningProgress{
		CurrentStep:    "",
		CompletedSteps: 0,
		TotalSteps:     0, // Will be determined by provider
	}

	// Create progress reporter
	reporter := bastion.NewProgressReporter(os.Stdout, mode, progress)

	// Create populate options
	opts := &vault.PopulateOptions{
		Subcommand:       subcommand,
		DryRun:           dryRun,
		Force:            force,
		ProgressReporter: reporter,
	}

	// Handle file input
	if fromFile != "" {
		return ErrPopulateFromFileNotImplemented
	}

	// Perform populate operation (provider will report all phases)
	err = manager.Populate(opts)
	if err != nil {
		return fmt.Errorf("failed to populate vault: %w", err)
	}

	log.Info("Vault populated successfully")

	return nil
}

// newVaultInceptionCmd creates the vault inception subcommand.
func newVaultInceptionCmd() *cobra.Command {
	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional fields
		Use:   "inception",
		Short: "Initialize inception vault for bootstrap",
		Long: `Initialize vault with inception secrets for a new deployment.

This command creates a local inception vault using 'safe local' running in a tmux session.
The inception vault is used temporarily during bootstrap until the production vault is available.

The vault runs on port 8234 by default and stores data in ~/.ocfp/{bloc}/vault/data.
Root and unseal keys are saved to ~/.ocfp/{bloc}/vault/{root.key,unseal.keys}.`,
		Example: `  # Initialize inception vault
  ocfp vault inception

  # Initialize with specific bloc
  ocfp vault inception --bloc production`,
		RunE: func(_cmd *cobra.Command, _args []string) error {
			return runVaultInception()
		},
	}

	return cmd
}

// getVaultInceptionPaths returns the paths for vault inception based on bloc name and test mode.
func getVaultInceptionPaths(blocName string, testMode bool) map[string]string {
	homeDir := os.Getenv("HOME")

	vaultDir := filepath.Join(homeDir, ".vault")
	vaultKeyFile := filepath.Join(homeDir, "vault.key")
	rootKeyFile := filepath.Join(homeDir, "vault.key")
	unsealKeysFile := filepath.Join(homeDir, "vault.key")
	tmuxSession := "inception-vault"
	vaultName := "inception"
	port := VaultInceptionPort

	if blocName != "" {
		vaultDir = filepath.Join(config.OcfpBlocDir(blocName), "vault", "data")
		rootKeyFile = filepath.Join(config.OcfpBlocDir(blocName), "vault", "root.key")
		unsealKeysFile = filepath.Join(config.OcfpBlocDir(blocName), "vault", "unseal.keys")
		tmuxSession = blocName + "-inception-vault"
		vaultName = blocName + "-inception"
	}

	if testMode {
		vaultDir = filepath.Join(homeDir, ".test-vault")
		vaultKeyFile = filepath.Join(homeDir, "test-vault.key")
		rootKeyFile = filepath.Join(homeDir, "test-vault.key")
		unsealKeysFile = filepath.Join(homeDir, "test-vault.key")
		tmuxSession = "test-inception-vault"
		vaultName = "test-inception"
		port = TestVaultInceptionPort
	}

	return map[string]string{
		"vaultDir":       vaultDir,
		"vaultKeyFile":   vaultKeyFile,
		"rootKeyFile":    rootKeyFile,
		"unsealKeysFile": unsealKeysFile,
		"tmuxSession":    tmuxSession,
		"vaultName":      vaultName,
		"port":           strconv.Itoa(port),
		"logDir":         filepath.Join(config.OcfpHome(), VaultInceptionLogDir),
		"logFile":        filepath.Join(config.OcfpHome(), VaultInceptionLogDir, VaultInceptionLogFile),
	}
}

// checkVaultInceptionPrerequisites verifies required commands are available.
func checkVaultInceptionPrerequisites(log *zap.SugaredLogger) error {
	requiredCommands := map[string]error{
		"safe":  ErrSafeNotFound,
		"vault": ErrVaultNotFound,
		"tmux":  ErrTmuxNotFound,
	}

	for cmd, cmdErr := range requiredCommands {
		// First try the PATH
		cmdPath, err := exec.LookPath(cmd)
		if err != nil {
			// If not in PATH, try /usr/local/bin explicitly (where bastion tools are installed)
			explicitPath := filepath.Join("/usr/local/bin", cmd)

			_, statErr := os.Stat(explicitPath)
			if statErr != nil {
				return cmdErr
			}

			cmdPath = explicitPath
		}

		log.Infow("Found required command", "command", cmd, "path", cmdPath)
	}

	return nil
}

// cleanupExistingVault removes any existing vault processes and files.
func cleanupExistingVault(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) {
	log.Info("Cleaning up existing vault processes...")

	// Kill tmux session (ignore errors if it doesn't exist)
	//nolint:gosec // paths come from controlled getVaultInceptionPaths() function
	cmd := exec.CommandContext(ctx, "tmux", "kill-session", "-t", paths["tmuxSession"])
	_ = cmd.Run()

	// Also kill bare inception-vault session if using a prefixed name (migration from older systems)
	if paths["tmuxSession"] != "inception-vault" {
		cmd = exec.CommandContext(ctx, "tmux", "kill-session", "-t", "inception-vault")
		_ = cmd.Run()
	}

	// Kill any process listening on the vault port
	//nolint:gosec // paths come from controlled getVaultInceptionPaths() function
	cmd = exec.CommandContext(ctx, "sh", "-c", "lsof -ti :"+paths["port"]+" | xargs kill -9 2>/dev/null")
	_ = cmd.Run()

	// Also try to kill safe local processes by pattern
	//nolint:gosec // paths come from controlled getVaultInceptionPaths() function
	cmd = exec.CommandContext(ctx, "pkill", "-f", "safe local.*--port "+paths["port"])
	_ = cmd.Run()

	time.Sleep(VaultCleanupWait)

	// Remove safe target if it exists
	//nolint:gosec // paths come from controlled getVaultInceptionPaths() function
	cmd = exec.CommandContext(ctx, "safe", "target", "delete", paths["vaultName"])
	_ = cmd.Run() // Ignore errors if target doesn't exist

	// Also delete bare inception target if using a prefixed name (migration from older systems)
	if paths["vaultName"] != "inception" {
		cmd = exec.CommandContext(ctx, "safe", "target", "delete", "inception")
		_ = cmd.Run()
	}

	// Remove vault data directory and all its contents
	err := os.RemoveAll(paths["vaultDir"])
	if err != nil && !os.IsNotExist(err) {
		log.Warnw("Failed to remove vault data directory", "error", err)
	}

	// Also remove the parent vault directory if empty
	vaultParent := filepath.Dir(paths["vaultDir"])
	_ = os.Remove(vaultParent) // Ignore error - will fail if not empty, which is fine

	// Remove safe's vault metadata (safe local stores metadata in ~/.vault/)
	homeDir, err := os.UserHomeDir()
	if err == nil {
		vaultMetaDir := filepath.Join(homeDir, ".vault", paths["vaultName"])

		err = os.RemoveAll(vaultMetaDir)
		if err != nil && !os.IsNotExist(err) {
			log.Warnw("Failed to remove vault metadata", "error", err)
		}
	}

	// Remove vault key files
	_ = os.Remove(paths["vaultKeyFile"])
	_ = os.Remove(paths["rootKeyFile"])
	_ = os.Remove(paths["unsealKeysFile"])

	log.Info("Cleanup completed")
}

// startVaultInTmux starts the vault in a tmux session.
func startVaultInTmux(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error {
	log.Info("Starting vault in tmux session...")

	// Kill both prefixed and bare session names defensively
	//nolint:gosec // paths come from controlled getVaultInceptionPaths() function
	killCmd := exec.CommandContext(ctx, "tmux", "kill-session", "-t", paths["tmuxSession"])
	_ = killCmd.Run()

	if paths["tmuxSession"] != "inception-vault" {
		killCmd = exec.CommandContext(ctx, "tmux", "kill-session", "-t", "inception-vault")
		_ = killCmd.Run()
	}

	// Create tmux session with retry
	var createErr error

	for attempt := range 2 {
		if attempt > 0 {
			log.Info("Retrying tmux session creation...")
			time.Sleep(1 * time.Second)
		}

		//nolint:gosec // paths come from controlled getVaultInceptionPaths() function
		cmd := exec.CommandContext(ctx, "tmux", "new-session", "-d", "-s", paths["tmuxSession"])

		output, err := cmd.CombinedOutput()
		if err == nil {
			createErr = nil

			break
		}

		createErr = err
		log.Warnw("tmux new-session failed",
			"attempt", attempt+1,
			"error", err,
			"output", string(output))

		// Log existing sessions for diagnostics
		listCmd := exec.CommandContext(ctx, "tmux", "list-sessions")

		listOutput, listErr := listCmd.CombinedOutput()
		if listErr != nil {
			log.Warnw("tmux list-sessions failed", "error", listErr)
		} else {
			log.Infow("Existing tmux sessions", "sessions", string(listOutput))
		}

		// Kill the session in case it exists but in a bad state
		//nolint:gosec // paths come from controlled getVaultInceptionPaths() function
		killCmd = exec.CommandContext(ctx, "tmux", "kill-session", "-t", paths["tmuxSession"])
		_ = killCmd.Run()
	}

	if createErr != nil {
		return fmt.Errorf("failed to create tmux session after retries: %w", createErr)
	}

	time.Sleep(1 * time.Second)

	// Start safe local vault with memory backend (inception is temporary)
	safeCmd := fmt.Sprintf("safe local -m --as %s --port %s",
		paths["vaultName"],
		paths["port"])

	//nolint:gosec // paths come from controlled getVaultInceptionPaths() function
	cmd := exec.CommandContext(ctx, "tmux", "send-keys", "-t", paths["tmuxSession"], safeCmd, "C-m")

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to send command to tmux: %w", err)
	}

	log.Infow("Vault started in tmux", "session", paths["tmuxSession"])
	time.Sleep(VaultInitWait) // Give vault time to initialize

	return nil
}

// waitForVaultReady waits for the vault to become ready.
func waitForVaultReady(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error {
	log.Info("Waiting for vault to initialize...")

	vaultAddr := "http://127.0.0.1:" + paths["port"]

	for attempt := range MaxVaultReadyAttempts {
		if attempt > 0 && attempt%5 == 0 {
			log.Info(".")
		}

		// Check tmux output for success indicators first (more reliable for safe local)
		//nolint:gosec // paths come from controlled getVaultInceptionPaths() function
		cmd := exec.CommandContext(ctx, "tmux", "capture-pane", "-t", paths["tmuxSession"], "-p", "-S", "-")

		output, err := cmd.Output()
		if err == nil {
			outputStr := string(output)

			// Check for errors first
			if strings.Contains(outputStr, "ERROR:") ||
				strings.Contains(outputStr, "fatal:") ||
				strings.Contains(outputStr, "Unable to initialize") {
				return ErrVaultStartupError
			}

			// Check for success indicators
			if strings.Contains(outputStr, "Now targeting") &&
				(strings.Contains(outputStr, "MEMORY-BACKED") ||
					strings.Contains(outputStr, "Storing data")) {
				log.Info("")
				log.Info("Vault initialized successfully!")

				return nil
			}
		}

		// Also try vault status as a fallback
		cmd = exec.CommandContext(ctx, "vault", "status")

		cmd.Env = append(os.Environ(), "VAULT_ADDR="+vaultAddr)

		err = cmd.Run()
		if err == nil {
			log.Info("")
			log.Info("Vault is ready!")

			return nil
		}

		time.Sleep(1 * time.Second)
	}

	return ErrVaultNotReady
}

// targetInceptionVault sets the safe target to the inception vault.
func targetInceptionVault(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error {
	log.Info("Targeting inception vault...")

	vaultURL := "http://127.0.0.1:" + paths["port"]

	//nolint:gosec // paths come from controlled getVaultInceptionPaths() function
	cmd := exec.CommandContext(ctx, "safe", "target", paths["vaultName"], vaultURL)

	err := cmd.Run()
	if err != nil {
		// Try without URL (might already be configured)
		//nolint:gosec // paths come from controlled getVaultInceptionPaths() function
		cmd = exec.CommandContext(ctx, "safe", "target", paths["vaultName"])

		err = cmd.Run()
		if err != nil {
			return fmt.Errorf("failed to target inception vault: %w", err)
		}
	}

	// Verify target was set (safe target outputs to stderr)
	cmd = exec.CommandContext(ctx, "safe", "target")

	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), paths["vaultName"]) {
		return ErrVaultTargetVerify
	}

	log.Infow("Successfully targeting inception vault", "vault", paths["vaultName"])

	return nil
}

// saveVaultKeys saves vault keys to appropriate files.
func saveVaultKeys(paths map[string]string, log *zap.SugaredLogger) {
	// For inception vault, keys are managed by safe local in memory mode
	// FUTURE ENHANCEMENT: For non-inception vaults, implement key capture from tmux output.
	// This would save root token to paths["rootKeyFile"] and unseal keys to paths["unsealKeysFile"].
	// Track implementation in issue: https://github.com/ocfp/ocfp-cli-go/issues/XXX
	if strings.Contains(paths["vaultName"], "inception") {
		log.Info("Inception vault uses pre-configured authentication (memory mode)")
		log.Info("Keys are managed automatically by safe")

		return
	}

	// For non-inception vaults, we would save keys here
	// This is left for future implementation
}

// printVaultInfo displays information about the running vault.
func printVaultInfo(paths map[string]string, log *zap.SugaredLogger) {
	log.Info("=== Inception Vault Information ===")
	log.Info("")
	log.Infow("Vault details",
		"tmux_session", paths["tmuxSession"],
		"address", "http://127.0.0.1:"+paths["port"],
		"log", paths["logFile"],
	)
	log.Info("")
	log.Info("Useful commands:")
	log.Infof("  View vault session:  tmux attach -t %s", paths["tmuxSession"])
	log.Info("  Detach from tmux:    Ctrl-B then D")
	log.Infof("  View vault logs:     tail -f %s", paths["logFile"])
	log.Infof("  Stop vault:          tmux kill-session -t %s", paths["tmuxSession"])
	log.Info("  Check vault status:  safe target")
	log.Info("  Test vault:          safe tree")
	log.Info("")
}

// runVaultInception executes the vault inception command.
func runVaultInception() error {
	log := logger.Get()
	blocName := viper.GetString("bloc")
	testMode := viper.GetBool("test")

	// Get correct paths
	paths := getVaultInceptionPaths(blocName, testMode)

	log.Info("=== Starting OCFP Vault Inception ===")
	log.Infow("Configuration",
		"bloc", blocName,
		"vault_name", paths["vaultName"],
		"port", paths["port"],
		"tmux_session", paths["tmuxSession"],
		"vault_dir", paths["vaultDir"],
	)

	// Step 1: Check prerequisites
	err := checkVaultInceptionPrerequisites(log)
	if err != nil {
		return fmt.Errorf("prerequisite check failed: %w", err)
	}

	// Step 2: Cleanup any existing vault
	cleanupExistingVault(context.TODO(), paths, log)

	// Step 3: Create vault directory
	err = os.MkdirAll(paths["vaultDir"], VaultDirMode)
	if err != nil {
		return fmt.Errorf("failed to create vault directory: %w", err)
	}

	log.Infow("Created vault directory", "path", paths["vaultDir"])

	// Step 4: Create log directory
	err = os.MkdirAll(paths["logDir"], VaultDirMode)
	if err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// Step 5: Start vault in tmux session
	err = startVaultInTmux(context.TODO(), paths, log)
	if err != nil {
		return fmt.Errorf("failed to start vault: %w", err)
	}

	// Step 6: Wait for vault to be ready
	err = waitForVaultReady(context.TODO(), paths, log)
	if err != nil {
		return fmt.Errorf("vault did not become ready: %w", err)
	}

	// Step 7: Target the inception vault
	err = targetInceptionVault(context.TODO(), paths, log)
	if err != nil {
		return fmt.Errorf("failed to target vault: %w", err)
	}

	// Step 8: Save vault keys
	saveVaultKeys(paths, log)

	log.Info("=== Vault Inception Completed Successfully ===")
	printVaultInfo(paths, log)

	return nil
}

// newVaultTeardownCmd creates the vault teardown subcommand.
func newVaultTeardownCmd() *cobra.Command {
	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional fields
		Use:   "teardown",
		Short: "Stop and clean up inception vault",
		Long:  `Stops the inception vault and removes all associated files and sessions.`,
		Example: `  # Teardown inception vault
  ocfp vault teardown

  # Teardown with specific bloc
  ocfp vault teardown --bloc production`,
		RunE: func(_cmd *cobra.Command, _args []string) error {
			return runVaultTeardown()
		},
	}

	return cmd
}

// runVaultTeardown executes the vault teardown command.
func runVaultTeardown() error {
	log := logger.Get()
	blocName := viper.GetString("bloc")
	testMode := viper.GetBool("test")

	paths := getVaultInceptionPaths(blocName, testMode)

	log.Info("=== Tearing Down Inception Vault ===")

	cleanupExistingVault(context.TODO(), paths, log)

	log.Info("=== Teardown Completed ===")

	return nil
}

// newVaultMigrateCmd creates the vault migrate subcommand.
//
//nolint:funlen // cobra command setup with long description and examples is inherently verbose
func newVaultMigrateCmd() *cobra.Command {
	var (
		sourcePath string
		destPath   string
		dryRun     bool
	)

	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional fields
		Use:   "migrate",
		Short: "Migrate secrets between vault instances",
		Long: `Migrate secrets from inception vault to production vault.

This command performs streaming key-by-key migration from the temporary
inception vault to the permanent production vault. Each secret key is:

  1. Exported from inception vault
  2. Imported to production vault
  3. Validated with SHA256 checksums
  4. Displayed in real-time tree format

The migration stops on first error and a snapshot is created before
migration begins for safety. All keys are migrated with inline validation.

Expected output format:
  secret/
  ├─ config/
  │  ├─ :domains 370f7e38 → 370f7e38 ✓
  │  └─ :provider 4d434f6d → 4d434f6d ✓

Checksum format: first 8 characters of SHA256 hash
Status indicators: ✓ success | ✗ failure

Output Modes:
  Interactive: Full tree display with colors and Unicode box-drawing characters
  Concise:     Tree display without colors (for logging/CI)
  JSON:        Structured JSON output for programmatic consumption
  YAML:        Structured YAML output for programmatic consumption

The output mode is automatically detected based on terminal capabilities.`,
		Example: `  # Migrate from inception to production vault
  ocfp vault migrate

  # Dry run to preview migration
  ocfp vault migrate --dry-run

  # Force migration without confirmation
  ocfp vault migrate --force

  # Manual migration between specific vault paths (advanced)
  ocfp vault migrate --source /secret/old --dest /secret/new

  # Output to JSON for automation
  OUTPUT_MODE=json ocfp vault migrate

  # Output to YAML for automation
  OUTPUT_MODE=yaml ocfp vault migrate`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _args []string) error {
			return runVaultMigrate(cmd, sourcePath, destPath, dryRun)
		},
	}

	cmd.Flags().StringVar(&sourcePath, "source", "", "source vault path")
	cmd.Flags().StringVar(&destPath, "dest", "", "destination vault path")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview migration without changes")
	cmd.Flags().Bool("force", false, "skip confirmation prompts")

	return cmd
}

// runVaultMigrate executes the vault migrate command.
func runVaultMigrate(cmd *cobra.Command, sourcePath, destPath string, dryRun bool) error {
	// Use OCFP_BLOC for logging path (not the vault target name from .saferc)
	// The bloc name determines the log directory structure: ~/.ocfp/{bloc}/logs/vault/migrate/
	blocName := viper.GetString("bloc")
	if blocName == "" {
		blocName = os.Getenv("OCFP_BLOC")
	}

	// Reinitialize logger with migrate subcommand for proper log path
	logDir := config.OcfpHome()

	err := logger.Initialize(logger.Config{
		Level:      viper.GetString("log_level"),
		Debug:      viper.GetBool("debug"),
		Verbose:    viper.GetBool("verbose"),
		Trace:      viper.GetBool("trace"),
		NoLog:      viper.GetBool("no_log"),
		LogDir:     logDir,
		BlocName:   blocName,
		Command:    "vault",
		Subcommand: "migrate",
		RequestID:  os.Getenv("OCFP_REQUEST_ID"),
	})
	if err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}

	log := logger.Get()
	force, _ := cmd.Flags().GetBool("force")

	// Load configuration and create manager
	manager, err := loadConfigAndManager()
	if err != nil {
		return err
	}

	defer func() { _ = manager.Close() }()

	// Handle manual migration if source/dest paths specified
	if sourcePath != "" && destPath != "" {
		return manualMigrateVault(manager, sourcePath, destPath, dryRun)
	}

	// Detect output mode
	outputMode := bastion.SelectOutputMode(os.Stdout)

	// Otherwise do standard inception->production migration
	opts := &vault.MigrateOptions{
		DryRun:     dryRun,
		Force:      force,
		OutputMode: outputMode,
	}

	err = manager.Migrate(opts)
	if err != nil {
		return fmt.Errorf("failed to migrate vault: %w", err)
	}

	log.Info("Vault migration completed")

	return nil
}

// newVaultExportCmd creates the vault export subcommand.
func newVaultExportCmd() *cobra.Command {
	var (
		vaultPath  string
		outputFile string
		format     string
	)

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export secrets from vault",
		Long:  `Export secrets from vault to a file for backup or transfer.`,
		Example: `  # Export secrets to file
  ocfp vault export --path /secret/production --output secrets.yml

  # Export as JSON
  ocfp vault export --path /secret/production --output secrets.json --format json`,
		RunE: func(_cmd *cobra.Command, _args []string) error {
			return runVaultExport(vaultPath, outputFile, format)
		},
	}

	cmd.Flags().StringVar(&vaultPath, "path", "", "vault path to export")
	cmd.Flags().StringVar(&outputFile, "output", "", "output file (default: stdout)")
	cmd.Flags().StringVar(&format, "format", "yaml", "output format (yaml|json)")

	return cmd
}

// runVaultExport executes the vault export command.
func runVaultExport(vaultPath, outputFile, format string) error {
	log := logger.Get()

	if vaultPath == "" {
		return ErrVaultPathIsRequired
	}

	log.Infow("Exporting vault secrets", "path", vaultPath)

	// Load configuration and create manager
	manager, err := loadConfigAndManager()
	if err != nil {
		return err
	}

	defer func() { _ = manager.Close() }()

	// Export secrets
	safe := manager.GetSafe()

	secrets, err := safe.Export(strings.TrimPrefix(vaultPath, "/"))
	if err != nil {
		return fmt.Errorf("failed to export secrets: %w", err)
	}

	// Marshal secrets to data
	data, err := marshalSecrets(secrets, format)
	if err != nil {
		return err
	}

	// Write output
	if outputFile != "" {
		err := os.WriteFile(outputFile, data, VaultOutputFileMode)
		if err != nil {
			return fmt.Errorf("failed to write file: %w", err)
		}

		log.Infow("Secrets exported", "file", outputFile)
	} else {
		_, err := fmt.Fprint(os.Stdout, string(data)+"\n")
		if err != nil {
			return fmt.Errorf("failed to write output: %w", err)
		}
	}

	return nil
}

// newVaultImportCmd creates the vault import subcommand.
//
//nolint:funlen // Command setup requires many lines
func newVaultImportCmd() *cobra.Command {
	var (
		vaultPath string
		inputFile string
		force     bool
	)

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import secrets into vault",
		Long:  `Import secrets from a file into vault.`,
		Example: `  # Import secrets from file
  ocfp vault import --path /secret/production --file secrets.yml

  # Force overwrite existing secrets
  ocfp vault import --path /secret/production --file secrets.yml --force`,
		RunE: func(_cmd *cobra.Command, _args []string) error {
			log := logger.Get()

			if vaultPath == "" || inputFile == "" {
				return ErrVaultPathAndInputFileRequired
			}

			log.Infow("Importing secrets to vault", "path", vaultPath, "file", inputFile)

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

			err = safe.Import(strings.TrimPrefix(vaultPath, "/"), secrets)
			if err != nil {
				return fmt.Errorf("failed to import secrets: %w", err)
			}

			log.Infow("Secrets imported successfully", "count", len(secrets))

			return nil
		},
	}

	cmd.Flags().StringVar(&vaultPath, "path", "", "vault path to import to")
	cmd.Flags().StringVar(&inputFile, "file", "", "input file")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing secrets")

	return cmd
}

// Helper functions

// loadConfigAndManager loads configuration and creates vault manager.
func loadConfigAndManager() (*vault.Manager, error) {
	configFile := viper.GetString("config")
	blocName := viper.GetString("bloc")

	// Fallback to environment variable if viper doesn't have it
	// This handles cases where config file might override env var binding
	if blocName == "" {
		blocName = os.Getenv("OCFP_BLOC")
	}

	// Validate bloc name is provided
	if blocName == "" {
		return nil, ErrBlocFlagOrEnvVarRequired
	}

	cfg, err := config.LoadWithParams(configFile, blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	manager, err := vault.NewManagerFromEnv(cfg, blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to create vault manager: %w", err)
	}

	return manager, nil
}

// marshalSecrets marshals secrets to the specified format.
func marshalSecrets(secrets map[string]interface{}, format string) ([]byte, error) {
	var (
		data []byte
		err  error
	)

	switch format {
	case "json":
		data, err = json.MarshalIndent(secrets, "", "  ")
	case "yaml", "yml":
		data, err = yaml.Marshal(secrets)
	default:
		return nil, ErrUnsupportedFormat(format)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to marshal secrets: %w", err)
	}

	return data, nil
}

// loadSecretsFromFile loads secrets from a YAML or JSON file.
func loadSecretsFromFile(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path) // #nosec G304 - path comes from user input but is used for reading config
	if err != nil {
		return nil, fmt.Errorf("failed to read secrets file: %w", err)
	}

	var secrets map[string]interface{}

	// Try YAML first
	err = yaml.Unmarshal(data, &secrets)
	if err == nil {
		return secrets, nil
	}

	// Try JSON
	err = json.Unmarshal(data, &secrets)
	if err == nil {
		return secrets, nil
	}

	return nil, ErrUnableToParseFileAsYAMLOrJSON
}

// manualMigrateVault performs manual migration between specified paths.
func manualMigrateVault(manager *vault.Manager, sourcePath, destPath string, dryRun bool) error {
	log := logger.Get()

	log.Infow("Manual vault migration", "source", sourcePath, "dest", destPath, "dry-run", dryRun)

	safe := manager.GetSafe()

	// Export from source
	secrets, err := safe.Export(strings.TrimPrefix(sourcePath, "/"))
	if err != nil {
		return fmt.Errorf("failed to export from source: %w", err)
	}

	if dryRun {
		log.Infow("Dry run - would migrate secrets", "count", len(secrets))

		for key := range secrets {
			log.Infow("Would migrate", "key", key)
		}

		return nil
	}

	// Import to destination
	err = safe.Import(strings.TrimPrefix(destPath, "/"), secrets)
	if err != nil {
		return fmt.Errorf("failed to import to destination: %w", err)
	}

	log.Infow("Manual vault migration completed", "migrated", len(secrets))

	return nil
}
