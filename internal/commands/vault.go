package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/ocfp/ocfp-cli-go/internal/bastion"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

const (
	// VaultOutputFileMode is the file permission mode for vault output files.
	VaultOutputFileMode = 0600

	// VaultDirMode is the file permission mode for vault directories.
	VaultDirMode = 0750

	// VaultInceptionPort is the inception vault port used when no bloc is named.
	// Bloc-scoped runs resolve their own port via config.InceptionVaultPort.
	VaultInceptionPort = config.LegacyInceptionVaultPort
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

	// tmuxVerifyWait is the duration to wait after tmux session creation for verification.
	tmuxVerifyWait = 500 * time.Millisecond
)

var (
	// ErrSafeNotFound indicates the safe CLI is not installed.
	ErrSafeNotFound = errors.New("'safe' command not found - please install safe CLI")

	// ErrVaultNotFound indicates the vault CLI is not installed.
	ErrVaultNotFound = errors.New("'vault' command not found - please install vault")
	// ErrVaultNotReady indicates vault did not become ready within the timeout period.
	ErrVaultNotReady = errors.New("vault did not become ready within timeout")
	// ErrVaultStartupError indicates a vault startup error was detected in output.
	ErrVaultStartupError = errors.New("vault startup error detected in output")
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
		vaultPath          string
		fromFile           string
		force              bool
		kmsKeyARN          string
		blobstoreEndpoint  string
		blobstoreMode      string
		blobstoreRegion    string
		blobstoreAccessKey string
		blobstoreSecretKey string //nolint:gosec // descriptive flag var name
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
  ocfp vault populate --vault-path /concourse/main

  # Populate with AWS KMS key for BOSH disk encryption (AWS only)
  ocfp vault populate --kms-key-arn arn:aws:kms:us-east-1:123456789012:key/mrk-abc123

  # Populate with PVE blobstore endpoint (PVE only)
  ocfp vault populate --blobstore-endpoint https://s3.dc1.example.com`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultPopulate(cmd, args, fromFile, force, kmsKeyARN, vaultPopulateBlobstoreFlags{
				Endpoint:  blobstoreEndpoint,
				Mode:      blobstoreMode,
				Region:    blobstoreRegion,
				AccessKey: blobstoreAccessKey,
				SecretKey: blobstoreSecretKey,
			})
		},
	}

	cmd.Flags().StringVar(&vaultPath, "vault-path", "", "vault path prefix")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "load secrets from file")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing secrets")
	cmd.Flags().Bool("dry-run", false, "preview actions without making changes")
	cmd.Flags().StringVar(&kmsKeyARN, "kms-key-arn", "", "AWS KMS key ARN for BOSH disk encryption (AWS only; omit to skip KMS configuration)")
	cmd.Flags().StringVar(&blobstoreEndpoint, "blobstore-endpoint", "", "S3-compatible blobstore endpoint URL (PVE only; omit to skip blobstore endpoint configuration)")
	cmd.Flags().StringVar(&blobstoreMode, "blobstore-mode", "", "PVE blobstore mode: 'local' (default; skip buckets) or 'external' (S3-compatible)")
	cmd.Flags().StringVar(&blobstoreRegion, "blobstore-region", "", "S3 region for the PVE external blobstore (default 'us-east-1')")
	cmd.Flags().StringVar(&blobstoreAccessKey, "blobstore-access-key", "", "S3 access key for the PVE external blobstore")
	cmd.Flags().StringVar(&blobstoreSecretKey, "blobstore-secret-key", "", "S3 secret key for the PVE external blobstore") //nolint:gosec // CLI flag name, not a credential

	return cmd
}

// vaultPopulateBlobstoreFlags bundles the five blobstore-related CLI flags so
// the function signature for runVaultPopulate stays compact and downstream
// callers don't have to reorder positional args every time we add one.
type vaultPopulateBlobstoreFlags struct {
	Endpoint  string
	Mode      string
	Region    string
	AccessKey string
	SecretKey string //nolint:gosec // field name is descriptive
}

// runVaultPopulate executes the vault populate command.
func runVaultPopulate(cmd *cobra.Command, args []string, fromFile string, force bool, kmsKeyARN string, blobstoreFlags vaultPopulateBlobstoreFlags) error {
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
		Subcommand:         subcommand,
		DryRun:             dryRun,
		Force:              force,
		ProgressReporter:   reporter,
		KMSKeyARN:          kmsKeyARN,
		BlobstoreEndpoint:  blobstoreFlags.Endpoint,
		BlobstoreMode:      blobstoreFlags.Mode,
		BlobstoreRegion:    blobstoreFlags.Region,
		BlobstoreAccessKey: blobstoreFlags.AccessKey,
		BlobstoreSecretKey: blobstoreFlags.SecretKey,
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
		Use:     "inception",
		Aliases: []string{"init"},
		Short:   "Initialize inception vault for bootstrap",
		Long: `Initialize vault with inception secrets for a new deployment.

This command creates a local inception vault using 'safe local' running in a tmux session
with file-backed storage. The inception vault is used temporarily during bootstrap until the
production vault is available.

The vault runs on port 8234 by default and stores data in ~/.ocfp/{bloc}/vault/data.
Root and unseal keys are saved to ~/.ocfp/{bloc}/vault/{root.key,unseal.keys}.

The 'init' alias is available for operator convenience: 'ocfp vault init' is equivalent.`,
		Example: `  # Initialize inception vault
  ocfp vault inception
  ocfp vault init               # alias

  # Initialize with specific bloc
  ocfp vault inception --bloc production
  ocfp vault init --bloc production`,
		RunE: func(_cmd *cobra.Command, _args []string) error {
			return runVaultInception()
		},
	}

	return cmd
}

// getVaultInceptionPaths returns the paths for vault inception based on bloc name and test mode.
func getVaultInceptionPaths(blocName string, testMode bool) map[string]string {
	home, _ := homeDir()

	vaultDir := filepath.Join(home, ".vault")
	vaultKeyFile := filepath.Join(home, "vault.key")
	rootKeyFile := filepath.Join(home, "vault.key")
	unsealKeysFile := filepath.Join(home, "vault.key")
	tmuxSession := "inception-vault"
	vaultName := "inception"
	port := VaultInceptionPort
	logDir := filepath.Join(config.OcfpHome(), VaultInceptionLogDir)
	logFile := filepath.Join(logDir, VaultInceptionLogFile)

	if blocName != "" {
		vaultDir = filepath.Join(config.OcfpBlocDir(blocName), "vault", "data")
		rootKeyFile = filepath.Join(config.OcfpBlocDir(blocName), "vault", "root.key")
		unsealKeysFile = filepath.Join(config.OcfpBlocDir(blocName), "vault", "unseal.keys")
		tmuxSession = blocName + "-inception-vault"
		vaultName = blocName + "-inception"
		// Every other resource here is already bloc-scoped; the port must be
		// too, or concurrent bootstraps for different blocs fight over one
		// listener and the loser's secrets land in the winner's data dir.
		port = config.InceptionVaultPort(blocName)
		// `safe local` tees its startup output here and waitForVaultReady reads
		// it back, so a shared file lets one bloc decide readiness from another
		// bloc's output.
		logDir = filepath.Join(config.OcfpBlocDir(blocName), VaultInceptionLogDir)
		logFile = filepath.Join(logDir, VaultInceptionLogFile)
	}

	if testMode {
		vaultDir = filepath.Join(home, ".test-vault")
		vaultKeyFile = filepath.Join(home, "test-vault.key")
		rootKeyFile = filepath.Join(home, "test-vault.key")
		unsealKeysFile = filepath.Join(home, "test-vault.key")
		tmuxSession = "test-inception-vault"
		vaultName = "test-inception"
		port = TestVaultInceptionPort
	}

	paths := map[string]string{
		"vaultDir":       vaultDir,
		"vaultKeyFile":   vaultKeyFile,
		"rootKeyFile":    rootKeyFile,
		"unsealKeysFile": unsealKeysFile,
		"tmuxSession":    tmuxSession,
		"vaultName":      vaultName,
		"port":           strconv.Itoa(port),
		"logDir":         logDir,
		"logFile":        logFile,
	}

	paths["pidFile"] = filepath.Join(paths["vaultDir"], "vault.pid")

	return paths
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
			// If not in PATH, check known installation directories
			found := false

			for _, dir := range []string{"/usr/local/bin", "/home/linuxbrew/.linuxbrew/bin"} {
				explicitPath := filepath.Join(dir, cmd)

				_, statErr := os.Stat(explicitPath)
				if statErr == nil {
					cmdPath = explicitPath
					found = true

					break
				}
			}

			if !found {
				return cmdErr
			}
		}

		log.Infow("Found required command", "command", cmd, "path", cmdPath)
	}

	// Advisory: script command for non-PTY SSH fallback
	_, scriptErr := exec.LookPath("script")
	if scriptErr != nil {
		log.Warn("script command not found — tmux may fail in non-PTY SSH sessions")
	}

	return nil
}

// isVaultAlreadyRunning checks if the inception vault is already running and healthy.
// All three conditions must pass: tmux session exists + vault responds on port + safe target is set.
func isVaultAlreadyRunning(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) bool {
	// Check 1: tmux session exists
	//nolint:gosec // paths come from controlled getVaultInceptionPaths() function
	checkCmd := exec.CommandContext(ctx, "tmux", "has-session", "-t", paths["tmuxSession"])
	ensureTmuxEnv(checkCmd)

	if checkCmd.Run() != nil {
		log.Debugw("Tmux session not found", "session", paths["tmuxSession"])

		return false
	}

	// Check 2: vault responds on port
	vaultAddr := "http://127.0.0.1:" + paths["port"]

	if !vaultRespondsOnAddr(ctx, vaultAddr) {
		log.Debugw("Vault not responding", "addr", vaultAddr)

		return false
	}

	// Check 3: safe target contains the vault name
	cmd := exec.CommandContext(ctx, "safe", "target")

	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), paths["vaultName"]) {
		log.Debugw("Safe target not set", "expected", paths["vaultName"])

		return false
	}

	log.Debugw("Vault is fully operational", "session", paths["tmuxSession"], "vault", paths["vaultName"])

	return true
}

// cleanupCommand is one step of the inception vault cleanup plan. Keeping the
// plan as data lets tests assert that a bloc's cleanup reaches only that
// bloc's own session, target, and port.
type cleanupCommand struct {
	name string
	args []string
	tmux bool
}

// vaultCleanupCommands builds the cleanup plan for one bloc's inception vault.
//
// Every entry must be derived from paths, never from a shared constant: these
// commands kill processes and delete targets, so an unscoped entry tears down
// a concurrently bootstrapping sibling. The bare "inception-vault" session and
// "inception" safe target are deliberately not touched — they belong to a
// no-bloc run, which may be another operation happening at the same time.
func vaultCleanupCommands(paths map[string]string) []cleanupCommand {
	return []cleanupCommand{
		{name: "tmux", args: []string{"kill-session", "-t", paths["tmuxSession"]}, tmux: true},
		{name: "sh", args: []string{"-c", "lsof -ti :" + paths["port"] + " | xargs kill -9 2>/dev/null"}, tmux: false},
		{name: "pkill", args: []string{"-f", "safe local.*--port " + paths["port"]}, tmux: false},
	}
}

// vaultCleanupTargetCommands builds the plan run after processes have stopped.
func vaultCleanupTargetCommands(paths map[string]string) []cleanupCommand {
	return []cleanupCommand{
		{name: "safe", args: []string{"target", "delete", paths["vaultName"]}, tmux: false},
	}
}

// runCleanupCommands executes a cleanup plan, ignoring failures: a session or
// target that is already gone is the desired end state.
func runCleanupCommands(ctx context.Context, cmds []cleanupCommand) {
	for _, spec := range cmds {
		//nolint:gosec // args come from the controlled getVaultInceptionPaths() function
		cmd := exec.CommandContext(ctx, spec.name, spec.args...)
		if spec.tmux {
			ensureTmuxEnv(cmd)
		}

		_ = cmd.Run()
	}
}

// cleanupExistingVault removes this bloc's existing vault processes and files.
func cleanupExistingVault(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) {
	log.Infow("Cleaning up existing vault processes...",
		"session", paths["tmuxSession"], "port", paths["port"])

	runCleanupCommands(ctx, vaultCleanupCommands(paths))

	// Kill by PID file if present (backward compat with old background process approach)
	pidData, readErr := os.ReadFile(paths["pidFile"])
	if readErr == nil {
		pid, atoiErr := strconv.Atoi(strings.TrimSpace(string(pidData)))
		if atoiErr == nil {
			proc, findErr := os.FindProcess(pid)
			if findErr == nil {
				_ = proc.Signal(os.Kill)
			}
		}

		_ = os.Remove(paths["pidFile"])
	}

	time.Sleep(VaultCleanupWait)

	runCleanupCommands(ctx, vaultCleanupTargetCommands(paths))

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
	_ = os.Remove(paths["rootKeyFile"])
	_ = os.Remove(paths["unsealKeysFile"])
	_ = os.Remove(paths["pidFile"])

	log.Info("Cleanup completed")
}

// stripANSI removes ANSI escape sequences from a string.
func stripANSI(s string) string {
	re := regexp.MustCompile(`\x1b\[[0-9;]*m`)

	return re.ReplaceAllString(s, "")
}

// replaceOrAppendEnv replaces an environment variable in the slice, or appends it if not found.
func replaceOrAppendEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			env[i] = prefix + value

			return env
		}
	}

	return append(env, prefix+value)
}

// envValue returns the value of an environment variable from the slice.
func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}

	return ""
}

// hasTerminfo checks both letter-based (FreeBSD/macOS) and hex-based (Debian/Ubuntu) subdirectories.
func hasTerminfo(term, terminfoDirs string) bool {
	if strings.ContainsAny(term, "/\\") {
		return false
	}

	dirs := strings.Split(terminfoDirs, ":")
	dirs = append(dirs, "/usr/share/terminfo", "/lib/terminfo", "/usr/lib/terminfo")

	letterDir := string(term[0])
	hexDir := fmt.Sprintf("%02x", term[0])

	for _, dir := range dirs {
		if dir == "" {
			continue
		}

		for _, sub := range []string{letterDir, hexDir} {
			//nolint:gosec // dir comes from TERMINFO_DIRS env var and known safe paths; term validated above
			_, statErr := os.Stat(filepath.Join(dir, sub, term))
			if statErr == nil {
				return true
			}
		}
	}

	return false
}

// ensureTmuxEnv sets TERM and TERMINFO_DIRS on a command for tmux compatibility.
func ensureTmuxEnv(cmd *exec.Cmd) {
	env := os.Environ()

	brewTerminfo := "/home/linuxbrew/.linuxbrew/share/terminfo"

	terminfoDirs := os.Getenv("TERMINFO_DIRS")
	if terminfoDirs == "" {
		terminfoDirs = "/usr/share/terminfo:/lib/terminfo:" + brewTerminfo
	} else if !strings.Contains(terminfoDirs, brewTerminfo) {
		terminfoDirs += ":" + brewTerminfo
	}

	env = replaceOrAppendEnv(env, "TERMINFO_DIRS", terminfoDirs)

	currentTerm := envValue(env, "TERM")
	if currentTerm != "" && hasTerminfo(currentTerm, terminfoDirs) {
		cmd.Env = env

		return
	}

	for _, term := range []string{"xterm-256color", "screen-256color", "screen", "xterm", "dumb"} {
		if hasTerminfo(term, terminfoDirs) {
			env = replaceOrAppendEnv(env, "TERM", term)
			cmd.Env = env

			return
		}
	}

	env = replaceOrAppendEnv(env, "TERM", "dumb")
	cmd.Env = env
}

// createTmuxSession creates a new tmux session with fallbacks for non-PTY SSH environments.
func createTmuxSession(ctx context.Context, session string, log *zap.SugaredLogger) error {
	// Check if session already exists
	//nolint:gosec // session name is controlled internally
	checkCmd := exec.CommandContext(ctx, "tmux", "has-session", "-t", session)
	ensureTmuxEnv(checkCmd)

	if checkCmd.Run() == nil {
		log.Infow("Tmux session already exists", "session", session)

		return nil
	}

	// Attempt 1: direct tmux
	//nolint:gosec // session name is controlled internally
	cmd := exec.CommandContext(ctx, "tmux", "new-session", "-d", "-s", session)
	ensureTmuxEnv(cmd)

	output, err := cmd.CombinedOutput()
	if err == nil {
		log.Infow("Created tmux session (direct)", "session", session)

		return nil
	}

	log.Warnw("Direct tmux failed", "error", err, "output", string(output))

	// Attempt 2: tmux via script command (provides PTY for non-interactive SSH)
	_, lookErr := exec.LookPath("script")
	if lookErr == nil {
		scriptArg := "tmux new-session -d -s " + session

		//nolint:gosec // scriptArg is controlled internally
		cmd = exec.CommandContext(ctx, "script", "-qfec", scriptArg, "/dev/null")
		ensureTmuxEnv(cmd)

		output, err = cmd.CombinedOutput()
		if err == nil {
			// Verify session was created
			time.Sleep(tmuxVerifyWait)

			//nolint:gosec // session name is controlled internally
			verifyCmd := exec.CommandContext(ctx, "tmux", "has-session", "-t", session)
			ensureTmuxEnv(verifyCmd)

			if verifyCmd.Run() == nil {
				log.Infow("Created tmux session (via script)", "session", session)

				return nil
			}
		}

		log.Warnw("Script+tmux fallback failed", "error", err, "output", string(output))
	}

	// Attempt 3: tmux start-server then retry
	output, err = startTmuxServerAndRetry(ctx, session)
	if err == nil {
		log.Infow("Created tmux session (after start-server)", "session", session)

		return nil
	}

	return fmt.Errorf("%w: all attempts failed (last output: %s)", ErrTmuxSessionFailed, string(output))
}

// startTmuxServerAndRetry starts the tmux server and retries session creation.
func startTmuxServerAndRetry(ctx context.Context, session string) ([]byte, error) {
	serverCmd := exec.CommandContext(ctx, "tmux", "start-server")
	ensureTmuxEnv(serverCmd)
	_ = serverCmd.Run()

	time.Sleep(1 * time.Second)

	//nolint:gosec // session name is controlled internally
	cmd := exec.CommandContext(ctx, "tmux", "new-session", "-d", "-s", session)
	ensureTmuxEnv(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("tmux new-session failed: %w", err)
	}

	return output, nil
}

// startVaultInTmux starts the vault inside a tmux session with output captured via tee.
func startVaultInTmux(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error {
	log.Info("Starting vault in tmux session...")

	// Clean stale session
	//nolint:gosec // paths come from controlled getVaultInceptionPaths() function
	killCmd := exec.CommandContext(ctx, "tmux", "kill-session", "-t", paths["tmuxSession"])
	ensureTmuxEnv(killCmd)
	_ = killCmd.Run()

	time.Sleep(tmuxVerifyWait)

	// Create new session
	err := createTmuxSession(ctx, paths["tmuxSession"], log)
	if err != nil {
		return err
	}

	time.Sleep(1 * time.Second)

	// Build the safe local command — pipe through tee to also capture in log file
	//nolint:gosec // paths come from controlled getVaultInceptionPaths() function
	safeCmd := fmt.Sprintf("safe local --file %s --as %s --port %s 2>&1 | tee %s",
		paths["vaultDir"], paths["vaultName"], paths["port"], paths["logFile"])

	// Send command to tmux session
	//nolint:gosec // paths come from controlled getVaultInceptionPaths() function
	cmd := exec.CommandContext(ctx, "tmux", "send-keys", "-t", paths["tmuxSession"], safeCmd, "C-m")
	ensureTmuxEnv(cmd)

	sendOutput, sendErr := cmd.CombinedOutput()
	if sendErr != nil {
		return fmt.Errorf("failed to send command to tmux: %w (output: %s)", sendErr, string(sendOutput))
	}

	log.Infow("Vault command sent to tmux", "session", paths["tmuxSession"])
	time.Sleep(VaultInitWait)

	return nil
}

// waitForVaultReady waits for the vault to become ready by checking tmux pane, log file, and vault status.
func waitForVaultReady(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error {
	log.Info("Waiting for vault to initialize...")

	vaultAddr := "http://127.0.0.1:" + paths["port"]

	for attempt := range MaxVaultReadyAttempts {
		if attempt > 0 && attempt%5 == 0 {
			log.Info(".")
		}

		// Check 1: tmux capture-pane
		//nolint:gosec // paths come from controlled getVaultInceptionPaths() function
		captureCmd := exec.CommandContext(ctx, "tmux", "capture-pane", "-t", paths["tmuxSession"], "-p")
		ensureTmuxEnv(captureCmd)

		paneOutput, captureErr := captureCmd.Output()
		if captureErr == nil {
			paneStr := stripANSI(string(paneOutput))

			if strings.Contains(paneStr, "ERROR:") || strings.Contains(paneStr, "fatal:") ||
				strings.Contains(paneStr, "Unable to initialize") {
				log.Errorw("Vault startup error detected", "pane_output", paneStr)

				return ErrVaultStartupError
			}

			if strings.Contains(paneStr, "Now targeting") {
				log.Info("Vault initialized successfully!")

				return nil
			}
		}

		// Check 2: log file (populated by tee)
		logData, readErr := os.ReadFile(paths["logFile"])
		if readErr == nil {
			logStr := stripANSI(string(logData))

			if strings.Contains(logStr, "Now targeting") {
				log.Info("Vault initialized (detected from log file)!")

				return nil
			}
		}

		// Check 3: vault status as fallback
		cmd := exec.CommandContext(ctx, "vault", "status")

		cmd.Env = append(os.Environ(), "VAULT_ADDR="+vaultAddr)

		if cmd.Run() == nil {
			log.Info("Vault is ready!")

			return nil
		}

		time.Sleep(1 * time.Second)
	}

	dumpVaultDiagnostics(ctx, paths, log)

	return ErrVaultNotReady
}

// dumpVaultDiagnostics logs tmux pane and log file contents on vault readiness failure.
func dumpVaultDiagnostics(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) {
	//nolint:gosec // paths come from controlled getVaultInceptionPaths() function
	captureCmd := exec.CommandContext(ctx, "tmux", "capture-pane", "-t", paths["tmuxSession"], "-p", "-S", "-50")
	ensureTmuxEnv(captureCmd)

	paneOutput, paneErr := captureCmd.Output()
	if paneErr == nil {
		log.Errorw("Vault ready timeout - tmux pane dump", "output", string(paneOutput))
	}

	logData, logErr := os.ReadFile(paths["logFile"])
	if logErr == nil {
		log.Errorw("Vault ready timeout - log file dump", "output", string(logData))
	}
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

// saveVaultKeys extracts and persists vault seal key and root token.
func saveVaultKeys(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error {
	// Try tmux capture-pane first, then log file
	var outputStr string

	//nolint:gosec // paths come from controlled getVaultInceptionPaths() function
	captureCmd := exec.CommandContext(ctx, "tmux", "capture-pane", "-t", paths["tmuxSession"], "-p", "-S", "-50")
	ensureTmuxEnv(captureCmd)

	paneOutput, paneErr := captureCmd.Output()
	if paneErr == nil && len(paneOutput) > 0 {
		outputStr = stripANSI(string(paneOutput))
	}

	// Fallback to log file
	if outputStr == "" {
		logData, logErr := os.ReadFile(paths["logFile"])
		if logErr == nil {
			outputStr = stripANSI(string(logData))
		}
	}

	if outputStr == "" {
		log.Warn("No vault output available for key extraction")

		return nil
	}

	// Parse seal key: "Your Vault Seal Key is <key>" or "Vault Seal Key is <key>"
	sealKey := extractSealKey(outputStr)
	if sealKey != "" {
		err := os.WriteFile(paths["unsealKeysFile"], []byte(sealKey+"\n"), VaultOutputFileMode) //nolint:gosec // G703: path is the OCFP-managed vault output dir
		if err != nil {
			return fmt.Errorf("failed to write unseal key: %w", err)
		}

		log.Infow("Saved unseal key", "path", paths["unsealKeysFile"])
	} else {
		log.Warn("Seal key not found in output (may be pre-existing vault)")
	}

	// Extract root token from ~/.saferc
	return saveRootTokenFromSafeRC(paths, log)
}

// saveRootTokenFromSafeRC reads the root token from ~/.saferc and writes it to the key file.
func saveRootTokenFromSafeRC(paths map[string]string, log *zap.SugaredLogger) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Warnw("Could not determine home directory for .saferc", "error", err)

		return nil //nolint:nilerr // Non-fatal — vault is still running
	}

	safeRcPath := filepath.Join(homeDir, ".saferc")

	//nolint:gosec // safeRcPath is derived from os.UserHomeDir() and a fixed filename
	data, err := os.ReadFile(safeRcPath)
	if err != nil {
		log.Warnw("Could not read .saferc for root token", "error", err)

		return nil //nolint:nilerr // Non-fatal — vault is still running
	}

	var safeCfg struct {
		Current string `yaml:"current"`
		Vaults  map[string]struct {
			Token string `yaml:"token"`
		} `yaml:"vaults"`
	}

	unmarshalErr := yaml.Unmarshal(data, &safeCfg)
	if unmarshalErr != nil {
		log.Warnw("Could not parse .saferc", "error", unmarshalErr)

		return nil //nolint:nilerr // Non-fatal
	}

	// Look the token up by this bloc's own target name. The `current` pointer
	// is global to the workstation, so a sibling bloc reaching `safe target`
	// first would otherwise have its root token written into this bloc's
	// root.key — after which every later command authenticates against the
	// wrong vault.
	targetName := safeCfg.Current
	if paths["vaultName"] != "" {
		targetName = paths["vaultName"]
	}

	if v, ok := safeCfg.Vaults[targetName]; ok && v.Token != "" {
		err = os.WriteFile(paths["rootKeyFile"], []byte(v.Token+"\n"), VaultOutputFileMode)
		if err != nil {
			return fmt.Errorf("failed to write root token: %w", err)
		}

		log.Infow("Saved root token", "path", paths["rootKeyFile"], "target", targetName)
	} else {
		log.Warnw("Root token not found in .saferc", "target", targetName)
	}

	return nil
}

// extractSealKey parses a vault seal key from output containing "Vault Seal Key is <key>".
func extractSealKey(outputStr string) string {
	const marker = "Vault Seal Key is "

	idx := strings.Index(outputStr, marker)
	if idx == -1 {
		return ""
	}

	keyStart := idx + len(marker)
	keyEnd := strings.IndexByte(outputStr[keyStart:], '\n')

	if keyEnd == -1 {
		keyEnd = len(outputStr[keyStart:])
	}

	return strings.TrimSpace(outputStr[keyStart : keyStart+keyEnd])
}

// printVaultInfo displays information about the running vault.
func printVaultInfo(paths map[string]string, log *zap.SugaredLogger) {
	log.Info("=== Inception Vault Information ===")
	log.Info("")
	log.Infow("Vault details",
		"tmux_session", paths["tmuxSession"],
		"address", "http://127.0.0.1:"+paths["port"],
		"data_dir", paths["vaultDir"],
		"log", paths["logFile"],
		"root_token", paths["rootKeyFile"],
		"unseal_key", paths["unsealKeysFile"],
	)
	log.Info("")
	log.Info("Useful commands:")
	log.Infof("  View vault session:  tmux attach -t %s", paths["tmuxSession"])
	log.Info("  Detach from tmux:    Ctrl-B then D")
	log.Infof("  View vault logs:     tail -f %s", paths["logFile"])
	log.Infof("  Stop vault:          tmux kill-session -t %s", paths["tmuxSession"])
	log.Info("  Check vault status:  safe target")
	log.Info("")
}

// prepareVaultDirectories creates the log directory for vault inception.
// NOTE: Do NOT create paths["vaultDir"] here. safe local --file creates it.
// If it already exists, safe prompts for the unseal key interactively (stdin read)
// and hangs in automation.
func prepareVaultDirectories(paths map[string]string, log *zap.SugaredLogger) error {
	err := os.MkdirAll(paths["logDir"], VaultDirMode)
	if err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	log.Infow("Prepared directories", "logDir", paths["logDir"])

	return nil
}

// runVaultInception executes the vault inception command.
func runVaultInception() error {
	return ensureInceptionVault(viper.GetString("bloc"), viper.GetBool("test"))
}

// ensureInceptionVault starts (or verifies) the file-backed inception vault for
// the given bloc. It is idempotent — if the vault is already running and
// accessible it returns nil without doing anything. Exported within the package
// so bootstrap can guarantee the vault is up before the artifacts step, instead
// of failing late (after the bastion is created) on a dead vault.
func ensureInceptionVault(blocName string, testMode bool) error {
	log := logger.Get()

	paths := getVaultInceptionPaths(blocName, testMode)

	log.Info("=== Starting OCFP Vault Inception ===")
	log.Infow("Configuration",
		"bloc", blocName,
		"vault_name", paths["vaultName"],
		"port", paths["port"],
		"tmux_session", paths["tmuxSession"],
		"vault_dir", paths["vaultDir"],
	)

	err := checkVaultInceptionPrerequisites(log)
	if err != nil {
		return fmt.Errorf("prerequisite check failed: %w", err)
	}

	if isVaultAlreadyRunning(context.TODO(), paths, log) {
		log.Info("Inception vault is already running and accessible")
		printVaultInfo(paths, log)

		return nil
	}

	cleanupExistingVault(context.TODO(), paths, log)

	err = prepareVaultDirectories(paths, log)
	if err != nil {
		return err
	}

	err = startVaultInTmux(context.TODO(), paths, log)
	if err != nil {
		return fmt.Errorf("failed to start vault: %w", err)
	}

	err = waitForVaultReady(context.TODO(), paths, log)
	if err != nil {
		return fmt.Errorf("vault did not become ready: %w", err)
	}

	// safe local --file handles targeting automatically, but verify
	err = targetInceptionVault(context.TODO(), paths, log)
	if err != nil {
		return fmt.Errorf("failed to target vault: %w", err)
	}

	saveErr := saveVaultKeys(context.TODO(), paths, log)
	if saveErr != nil {
		log.Warnw("Failed to save vault keys", "error", saveErr)
	}

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
