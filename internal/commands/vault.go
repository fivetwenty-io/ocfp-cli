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
	// VaultInceptionLogDir is the bloc-scoped directory suffix for vault
	// inception logs, joined onto config.StateHome()/<bloc> (or, when no
	// bloc is named, appended as "vault" onto config.GetLogDir()).
	VaultInceptionLogDir = "logs/vault"
	// VaultInceptionLogFile is the filename for vault inception logs.
	VaultInceptionLogFile = "vault-inception.log"
	// MaxVaultReadyAttempts is how many seconds to wait for the vault to
	// become ready. A raft node has to elect itself leader before safe can
	// set it up, which takes longer than the file backend ever did.
	MaxVaultReadyAttempts = 60

	// VaultInitWait is the duration to wait after vault startup before initialization.
	VaultInitWait = 5 * time.Second

	// tmuxVerifyWait is the duration to wait after tmux session creation for verification.
	tmuxVerifyWait = 500 * time.Millisecond
)

var (
	// ErrSafeNotFound indicates the safe CLI is not installed.
	ErrSafeNotFound = errors.New("'safe' command not found - please install safe CLI")

	// ErrVaultNotReady indicates vault did not become ready within the timeout period.
	ErrVaultNotReady = errors.New("vault did not become ready within timeout")
	// ErrVaultStartupError indicates a vault startup error was detected in output.
	ErrVaultStartupError = errors.New("vault startup error detected in output")
	// ErrVaultKeysRejected indicates the saved root token or unseal key did not
	// open the vault. It is the only startup failure that says anything about
	// the keys on disk.
	ErrVaultKeysRejected = errors.New("the saved inception vault keys were rejected")
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

			// Logger appends {bloc}/logs/{command} itself; pass the
			// state-class root, not the legacy flat ~/.ocfp home.
			logDir := config.StateHome()

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
	cmd.AddCommand(newVaultReservedIPsCmd())
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
		forceReallocate    bool
		kmsKeyARN          string
		blobstoreEndpoint  string
		blobstoreMode      string
		blobstoreRegion    string
		blobstoreAccessKey string
		blobstoreSecretKey string // #nosec -- descriptive flag var name
	)

	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional fields
		Use:   "populate",
		Short: "Populate vault with secrets",
		Long: `Populate vault with secrets from configuration or file.

This command reads secrets from a configuration file and populates them
into Vault or CredHub at the appropriate paths for the deployment.

An optional phase name limits the run to one part of the tree:

  public-ips     write the public IP records
  reserved-ips   write the reserved-ips records (PVE only)
  fqdns          add the FQDN keys vault does not hold yet, for the mgmt and
                 ocf planes (PVE only). Existing keys are never changed unless
                 --force is given. Use --dry-run to list the additions.`,
		Example: `  # Populate vault from default config
  ocfp vault populate

  # Populate from specific file
  ocfp vault populate --from-file secrets.yml

  # Populate to specific vault path
  ocfp vault populate --vault-path /concourse/main

  # Populate with AWS KMS key for BOSH disk encryption (AWS only)
  ocfp vault populate --kms-key-arn arn:aws:kms:us-east-1:123456789012:key/mrk-abc123

  # Add only the missing FQDN keys, per plane (PVE only)
  ocfp vault populate fqdns

  # Preview the FQDN additions (with values) without writing
  ocfp vault populate fqdns --dry-run

  # Overwrite existing FQDN keys too; the dry run shows old and new values
  ocfp vault populate fqdns --force --dry-run

  # Populate with PVE blobstore endpoint (PVE only)
  ocfp vault populate --blobstore-endpoint https://s3.dc1.example.com`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultPopulate(cmd, args, vaultPopulateFlags{
				FromFile:        fromFile,
				Force:           force,
				ForceReallocate: forceReallocate,
				KMSKeyARN:       kmsKeyARN,
			}, vaultPopulateBlobstoreFlags{
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
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing secrets (for the fqdns phase, existing FQDN keys)")
	cmd.Flags().BoolVar(&forceReallocate, "force-reallocate", false,
		"move reserved IPs onto this build's derived addresses (recreates the VMs holding them; "+
			"omit to keep the addresses vault records and report the divergence)")
	cmd.Flags().Bool("dry-run", false, "preview actions without making changes")
	cmd.Flags().StringVar(&kmsKeyARN, "kms-key-arn", "", "AWS KMS key ARN for BOSH disk encryption (AWS only; omit to skip KMS configuration)")
	cmd.Flags().StringVar(&blobstoreEndpoint, "blobstore-endpoint", "", "S3-compatible blobstore endpoint URL (PVE only; omit to skip blobstore endpoint configuration)")
	cmd.Flags().StringVar(&blobstoreMode, "blobstore-mode", "", "PVE blobstore mode: 'local' (default; skip buckets) or 'external' (S3-compatible)")
	cmd.Flags().StringVar(&blobstoreRegion, "blobstore-region", "", "S3 region for the PVE external blobstore (default 'us-east-1')")
	cmd.Flags().StringVar(&blobstoreAccessKey, "blobstore-access-key", "", "S3 access key for the PVE external blobstore")
	cmd.Flags().StringVar(&blobstoreSecretKey, "blobstore-secret-key", "", "S3 secret key for the PVE external blobstore") // #nosec -- CLI flag name, not a credential

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
	SecretKey string // #nosec -- field name is descriptive
}

// vaultPopulateFlags bundles the non-blobstore populate flags for the same
// reason.
type vaultPopulateFlags struct {
	FromFile        string
	Force           bool
	ForceReallocate bool
	KMSKeyARN       string
}

// runVaultPopulate executes the vault populate command.
func runVaultPopulate(
	cmd *cobra.Command, args []string, flags vaultPopulateFlags, blobstoreFlags vaultPopulateBlobstoreFlags,
) error {
	log := logger.Get()
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	// Load configuration and create manager
	manager, err := loadConfigAndManager()
	if err != nil {
		return err
	}

	defer func() { _ = manager.Close() }()

	// Handle subcommand (public-ips, reserved-ips, fqdns)
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
		Force:              flags.Force,
		ProgressReporter:   reporter,
		KMSKeyARN:          flags.KMSKeyARN,
		BlobstoreEndpoint:  blobstoreFlags.Endpoint,
		BlobstoreMode:      blobstoreFlags.Mode,
		BlobstoreRegion:    blobstoreFlags.Region,
		BlobstoreAccessKey: blobstoreFlags.AccessKey,
		BlobstoreSecretKey: blobstoreFlags.SecretKey,
		ForceReallocate:    flags.ForceReallocate,
	}

	// Handle file input
	if flags.FromFile != "" {
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

The vault listens on a port derived from the bloc name, in the range
18234-19233, so several blocs can run an inception vault side by side. Port
8234 is the legacy value, used only when no bloc is named. Override with
OCFP_VAULT_INCEPTION_PORT or the bloc's own config. It stores data in
~/.local/share/ocfp/{bloc}/vault/data (or $XDG_DATA_HOME/ocfp/{bloc}/vault/data if set).
Root and unseal keys are saved to ~/.local/share/ocfp/{bloc}/vault/{root.key,unseal.keys}.
Falls back to the legacy ~/.ocfp/{bloc}/vault/... layout when only that exists;
set OCFP_HOME to force it.

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
	logDir := filepath.Join(config.GetLogDir(), "vault")
	logFile := filepath.Join(logDir, VaultInceptionLogFile)
	lockFile := filepath.Join(config.StateHome(), inceptionLockFileName)

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
		// bloc's output. Logs are state, not data, so this resolves under
		// StateHome() (with a dual-read fallback to the pre-migration
		// ~/.ocfp/<bloc>/logs/vault directory when only that exists) rather
		// than OcfpBlocDir(), which is DataHome()-rooted.
		newLogDir := filepath.Join(config.StateHome(), blocName, VaultInceptionLogDir)
		legacyLogDir := filepath.Join(config.OcfpHome(), blocName, VaultInceptionLogDir)
		logDir, _ = config.ResolveExisting(newLogDir, legacyLogDir)
		logFile = filepath.Join(logDir, VaultInceptionLogFile)
		// The lock sits outside <bloc>/vault because the archive renames that
		// directory while the lock is held.
		lockFile = filepath.Join(config.StateHome(), blocName, inceptionLockFileName)
	}

	if testMode {
		vaultDir = filepath.Join(home, ".test-vault")
		vaultKeyFile = filepath.Join(home, "test-vault.key")
		rootKeyFile = filepath.Join(home, "test-vault.key")
		unsealKeysFile = filepath.Join(home, "test-vault.key")
		tmuxSession = "test-inception-vault"
		vaultName = "test-inception"
		port = TestVaultInceptionPort
		lockFile = filepath.Join(config.StateHome(), "test-"+inceptionLockFileName)
	}

	paths := map[string]string{
		"vaultDir":       vaultDir,
		"vaultKeyFile":   vaultKeyFile,
		"rootKeyFile":    rootKeyFile,
		"unsealKeysFile": unsealKeysFile,
		"tmuxSession":    tmuxSession,
		"vaultName":      vaultName,
		"port":           strconv.Itoa(port),
		"clusterPort":    "",
		"logDir":         logDir,
		"logFile":        logFile,
		"lockFile":       lockFile,
	}

	// An API port too high to leave room for its cluster port leaves the
	// entry empty; requireClusterPort turns that into an error before any
	// work starts.
	clusterPort, err := config.InceptionVaultClusterPort(port)
	if err == nil {
		paths["clusterPort"] = strconv.Itoa(clusterPort)
	}

	paths["pidFile"] = filepath.Join(paths["vaultDir"], "vault.pid")

	return paths
}

// requireClusterPort reports why paths has no cluster port. The port is
// derived from the API port, so the only way to lose it is an API port too
// high to leave room for the offset; recomputing it recovers that error.
func requireClusterPort(paths map[string]string) error {
	if paths["clusterPort"] != "" {
		return nil
	}

	port, err := strconv.Atoi(paths["port"])
	if err != nil {
		return fmt.Errorf("invalid inception vault port %q: %w", paths["port"], err)
	}

	_, err = config.InceptionVaultClusterPort(port)
	if err != nil {
		return fmt.Errorf("cannot derive the inception vault cluster port: %w", err)
	}

	return nil
}

// inceptionCommandFallbackDirs are searched for inception vault tools that
// are not on PATH, which happens in non-interactive SSH sessions on the
// bastion where linuxbrew's bin directory is missing.
//
//nolint:gochecknoglobals // fixed list, read-only
var inceptionCommandFallbackDirs = []string{"/usr/local/bin", "/home/linuxbrew/.linuxbrew/bin"}

// findInceptionCommand resolves a tool on PATH, then in the fallback
// directories, and returns its path.
func findInceptionCommand(name string) (string, bool) {
	cmdPath, err := exec.LookPath(name)
	if err == nil {
		return cmdPath, true
	}

	for _, dir := range inceptionCommandFallbackDirs {
		explicitPath := filepath.Join(dir, name)

		_, statErr := os.Stat(explicitPath) // #nosec G703 -- dir is a fixed fallback and name a fixed tool or a validated engine name
		if statErr == nil {
			return explicitPath, true
		}
	}

	return "", false
}

// checkVaultInceptionPrerequisites verifies required commands are available,
// that safe is new enough to run a raft-backed inception vault, and which
// engine safe will run. Either engine will do. It returns the absolute path
// of the safe it checked, so the vault is started by that same binary.
func checkVaultInceptionPrerequisites(ctx context.Context, log *zap.SugaredLogger) (inceptionTools, error) {
	requiredCommands := map[string]error{
		"safe": ErrSafeNotFound,
		"tmux": ErrTmuxNotFound,
	}

	for cmd, cmdErr := range requiredCommands {
		cmdPath, found := findInceptionCommand(cmd)
		if !found {
			return inceptionTools{}, cmdErr
		}

		log.Infow("Found required command", "command", cmd, "path", cmdPath)
	}

	engine, err := resolveInceptionEngine()
	if err != nil {
		return inceptionTools{}, err
	}

	log.Infow("Found vault engine", "engine", engine.name, "path", engine.path)

	safePath, _ := findInceptionCommand("safe")

	safePath, err = filepath.Abs(safePath)
	if err != nil {
		return inceptionTools{}, fmt.Errorf("cannot resolve the path of safe: %w", err)
	}

	err = checkSafeCompatibility(ctx, safePath)
	if err != nil {
		return inceptionTools{}, err
	}

	// Advisory: script command for non-PTY SSH fallback
	_, scriptErr := exec.LookPath("script")
	if scriptErr != nil {
		log.Warn("script command not found — tmux may fail in non-PTY SSH sessions")
	}

	return inceptionTools{safe: safePath, engine: engine}, nil
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
		// Only listeners, and never this process. A bare `lsof -ti :PORT` also
		// lists every client with a socket connected to that port, and the
		// liveness probe that decides to run this cleanup leaves exactly such
		// a socket open on our own side, so the unfiltered form had the CLI
		// SIGKILL itself partway through `vault inception` (exit 137).
		{name: "sh", args: []string{"-c",
			"lsof -ti :" + paths["port"] + " -sTCP:LISTEN 2>/dev/null" +
				" | grep -vx " + strconv.Itoa(os.Getpid()) +
				" | xargs -r kill -9 2>/dev/null"}, tmux: false},
		{name: "pkill", args: []string{"-f", "safe local.*--port " + paths["port"]}, tmux: false},
	}
}

// vaultCleanupTargetCommands builds the plan run after processes have stopped.
func vaultCleanupTargetCommands(paths map[string]string) []cleanupCommand {
	return []cleanupCommand{
		{name: "safe", args: []string{"target", "delete", paths["vaultName"]}, tmux: false},
	}
}

// cleanupExistingVault stops this bloc's inception vault and moves its data
// and keys aside. A vault that will not stop is left exactly where it is,
// because archiving data out from under a running engine is how a store gets
// corrupted.
func cleanupExistingVault(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error {
	err := stopInceptionVault(ctx, paths, log)
	if err != nil {
		return err
	}

	archived, err := archiveAndForgetVault(paths, time.Now().Format("20060102-150405"), log)
	if err != nil {
		return fmt.Errorf("failed to archive the inception vault: %w", err)
	}

	if archived != "" {
		log.Infow("Previous vault state kept", "archive", archived)
	}

	log.Info("Cleanup completed")

	return nil
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
			_, statErr := os.Stat(filepath.Join(dir, sub, term)) // #nosec G703 -- dir comes from TERMINFO_DIRS env var and known safe paths; term validated above
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
	checkCmd := exec.CommandContext(ctx, "tmux", "has-session", "-t", session) // #nosec G204 -- session name is controlled internally
	ensureTmuxEnv(checkCmd)

	if checkCmd.Run() == nil {
		log.Infow("Tmux session already exists", "session", session)

		return nil
	}

	// Attempt 1: direct tmux
	cmd := exec.CommandContext(ctx, "tmux", "new-session", "-d", "-s", session) // #nosec G204 -- session name is controlled internally
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

		cmd = exec.CommandContext(ctx, "script", "-qfec", scriptArg, "/dev/null") // #nosec G204 -- scriptArg is controlled internally
		ensureTmuxEnv(cmd)

		output, err = cmd.CombinedOutput()
		if err == nil {
			// Verify session was created
			time.Sleep(tmuxVerifyWait)

			verifyCmd := exec.CommandContext(ctx, "tmux", "has-session", "-t", session) // #nosec G204 -- session name is controlled internally
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

	cmd := exec.CommandContext(ctx, "tmux", "new-session", "-d", "-s", session) // #nosec G204 -- session name is controlled internally
	ensureTmuxEnv(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("tmux new-session failed: %w", err)
	}

	return output, nil
}

// safeLocalMode chooses between starting a new inception vault and reopening
// an existing one.
type safeLocalMode int

const (
	// safeLocalFresh starts a new, empty vault.
	safeLocalFresh safeLocalMode = iota
	// safeLocalRestart reopens an initialized vault with its saved keys.
	safeLocalRestart
)

// shellQuote quotes s for a POSIX shell. Inside single quotes nothing is
// special except the quote itself, which is closed, escaped, and reopened.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// buildSafeLocalCommand builds the line typed into the bloc's tmux session to
// run its inception vault on raft storage.
//
// The line runs the safe ocfp checked by its absolute path, so a different
// safe in the engine's directory can never answer instead. The PATH prefix
// and --engine make that safe run the same engine binary ocfp resolved, so
// the two can never disagree about which engine owns the data.
// The line carries paths to the key files and never their contents, because
// anything typed into tmux lands in the pane, its scrollback, and the log.
//
// A fresh start reads stdin from /dev/null. If safe unexpectedly finds an
// initialized store, it then reads an empty unseal key and exits at once
// instead of waiting forever on a prompt nobody will answer. A restart feeds
// the saved unseal key on stdin and hands safe the saved root token, which
// spares it from calling generate-root.
func buildSafeLocalCommand(paths map[string]string, tools inceptionTools, mode safeLocalMode) string {
	args := []string{
		"PATH=" + shellQuote(filepath.Dir(tools.engine.path)) + `:"$PATH"`,
		shellQuote(tools.safe), "local",
		"--raft", shellQuote(paths["vaultDir"]),
		"--as", shellQuote(paths["vaultName"]),
		"--port", paths["port"],
		"--cluster-port", paths["clusterPort"],
		"--engine", tools.engine.name,
	}

	stdin := "/dev/null"

	if mode == safeLocalRestart {
		args = append(args, "--root-token-file", shellQuote(paths["rootKeyFile"]))
		stdin = shellQuote(paths["unsealKeysFile"])
	}

	args = append(args, "<", stdin, "2>&1", "|", "tee", shellQuote(paths["logFile"]))

	return strings.Join(args, " ")
}

// tmuxSendKeysLimit is the most ocfp will type into a tmux pane in one line.
// tmux send-keys cuts input off at about 1024 bytes, and a cut line would run
// a different command from the one built.
const tmuxSendKeysLimit = 1024

// safeLocalLauncherMode lets only the user read or run the launcher. It holds
// paths, never key values, but nobody else has reason to see them.
const safeLocalLauncherMode = 0o700

// vaultKeyDirMode keeps the directory that holds a bloc's vault data and keys
// private to the user.
const vaultKeyDirMode = 0o700

// ErrSafeLocalLineTooLong reports a launcher path too long to type into tmux.
var ErrSafeLocalLineTooLong = errors.New("the inception vault launch line is too long for tmux")

// writeSafeLocalLauncher writes the safe local command for paths into a
// script in the bloc's log directory and returns the script's path.
//
// tmux send-keys would cut the full command off once long data, key, and log
// paths push it past about 1024 bytes, so the pane runs this script instead.
// The script carries only what buildSafeLocalCommand builds, which names the
// key files and never holds their contents. It lives in the log directory
// because ocfp owns that directory in every layout, and it is replaced whole
// on every start so an old launcher's contents or permissions never linger.
func writeSafeLocalLauncher(paths map[string]string, tools inceptionTools, mode safeLocalMode) (string, error) {
	script := filepath.Join(paths["logDir"], paths["vaultName"]+"-start.sh")
	body := "#!/bin/sh\n" + buildSafeLocalCommand(paths, tools, mode) + "\n"

	tmp, err := os.CreateTemp(paths["logDir"], ".start-*.sh")
	if err != nil {
		return "", fmt.Errorf("failed to create the inception vault launcher: %w", err)
	}

	tmpName := tmp.Name()

	_, err = tmp.WriteString(body)
	if err == nil {
		err = tmp.Chmod(safeLocalLauncherMode)
	}

	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}

	if err == nil {
		err = os.Rename(tmpName, script)
	}

	if err != nil {
		_ = os.Remove(tmpName) // the half-written temp file is ocfp's own, never vault data

		return "", fmt.Errorf("failed to write the inception vault launcher %s: %w", script, err)
	}

	return script, nil
}

// safeLocalLauncherLine is the line typed into tmux to run script. It is
// refused when it would not fit in one send-keys, rather than typed cut off.
func safeLocalLauncherLine(script string) (string, error) {
	line := "sh " + shellQuote(script)
	if len(line) >= tmuxSendKeysLimit {
		return "", fmt.Errorf("%w: %d bytes for %s", ErrSafeLocalLineTooLong, len(line), script)
	}

	return line, nil
}

// startVaultInTmux starts the vault inside a tmux session with output captured via tee.
func startVaultInTmux(
	ctx context.Context, paths map[string]string, tools inceptionTools, mode safeLocalMode, log *zap.SugaredLogger,
) error {
	log.Info("Starting vault in tmux session...")

	// The launcher is written before any session is touched, so a launcher
	// that cannot be written or typed leaves the pane as it was.
	script, err := writeSafeLocalLauncher(paths, tools, mode)
	if err != nil {
		return err
	}

	safeCmd, err := safeLocalLauncherLine(script)
	if err != nil {
		return err
	}

	err = setAsidePreviousVaultLog(paths["logFile"])
	if err != nil {
		return err
	}

	// Clean stale session
	killCmd := exec.CommandContext(ctx, "tmux", "kill-session", "-t", paths["tmuxSession"]) // #nosec G204 -- paths come from controlled getVaultInceptionPaths() function
	ensureTmuxEnv(killCmd)
	_ = killCmd.Run()

	time.Sleep(tmuxVerifyWait)

	// Create new session
	err = createTmuxSession(ctx, paths["tmuxSession"], log)
	if err != nil {
		return err
	}

	time.Sleep(1 * time.Second)

	// Send command to tmux session
	cmd := exec.CommandContext(ctx, "tmux", "send-keys", "-t", paths["tmuxSession"], safeCmd, "C-m") // #nosec G204 -- the launcher path is shell-quoted by safeLocalLauncherLine
	ensureTmuxEnv(cmd)

	sendOutput, sendErr := cmd.CombinedOutput()
	if sendErr != nil {
		return fmt.Errorf("failed to send command to tmux: %w (output: %s)", sendErr, string(sendOutput))
	}

	log.Infow("Vault command sent to tmux", "session", paths["tmuxSession"])
	time.Sleep(VaultInitWait)

	return nil
}

// safeNonFatalWarnings match the "!! " lines safe prints when it cannot save
// the root token in ~/.saferc. The vault keeps running after them and safe
// still goes on to print "Now targeting", so they must not end the wait.
//
//nolint:gochecknoglobals // fixed list, read-only
var safeNonFatalWarnings = []*regexp.Regexp{
	regexp.MustCompile(`^!! Unable to save the root token in ~/\.saferc`),
	regexp.MustCompile(`^!! The \S+ server at \S+ is still running\.$`),
	regexp.MustCompile(`^!! Its root token is `),
	regexp.MustCompile(`^!! To reach it: `),
	regexp.MustCompile(`^!! You are still authenticated to it`),
}

// safeTokenRefusals are the endings safe gives its "The root token in <file>"
// line when the engine refused the saved token, or the token is not root.
//
//nolint:gochecknoglobals // fixed list, read-only
var safeTokenRefusals = []string{" was rejected by ", " is not a root token"}

// unsealKeyRefusals are what an engine says, lower-cased, when it refuses the
// unseal key itself: a key that does not decrypt the keyring, one too short,
// one that is not hex or base64, and an empty one. safe wraps every unseal
// error as "Unable to unseal", including timeouts and engine faults, so only
// these mark the key as wrong.
//
//nolint:gochecknoglobals // fixed list, read-only
var unsealKeyRefusals = []string{
	"message authentication failed",
	"invalid key",
	"must be a valid hex or base64 string",
	"'key' must be specified",
}

// isSafeKeyFailure reports whether a "!! " line says the saved root token or
// unseal key does not open the vault. Only such a line may lead the caller to
// archive a vault and start a fresh one.
func isSafeKeyFailure(line string) bool {
	if strings.Contains(line, "The root token in ") {
		for _, refusal := range safeTokenRefusals {
			if strings.Contains(line, refusal) {
				return true
			}
		}

		return false
	}

	if !strings.Contains(line, "Unable to unseal") {
		return false
	}

	lower := strings.ToLower(line)

	for _, refusal := range unsealKeyRefusals {
		if strings.Contains(lower, refusal) {
			return true
		}
	}

	return false
}

// vaultSecretMarkers precede the secret values safe prints, the seal key of a
// new vault and a root token it could not save.
//
//nolint:gochecknoglobals // fixed list, read-only
var vaultSecretMarkers = []string{"Seal Key is ", "token is "}

// redactVaultOutput blanks everything after a secret marker on each line, so
// safe's output can be logged or quoted in an error.
func redactVaultOutput(s string) string {
	lines := strings.Split(s, "\n")

	for i, line := range lines {
		for _, marker := range vaultSecretMarkers {
			idx := strings.Index(line, marker)
			if idx >= 0 {
				line = line[:idx+len(marker)] + "[redacted]"
			}
		}

		lines[i] = line
	}

	return strings.Join(lines, "\n")
}

// classifyVaultStartup reads safe's output so far. It reports ready once safe
// prints "Now targeting", ErrVaultKeysRejected when the saved keys were
// refused, and ErrVaultStartupError for any other failure.
//
// The difference matters to the caller. A key failure means the keys on disk
// do not open this vault; anything else, such as a busy port or an unseal
// request that timed out, says nothing about the keys or the data and must
// never lead to archiving.
func classifyVaultStartup(output string) (bool, error) {
	if strings.Contains(output, "Now targeting") {
		return true, nil
	}

	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "!! ") && !isSafeNonFatalWarning(line) {
			if isSafeKeyFailure(line) {
				return false, fmt.Errorf("%w: %s", ErrVaultKeysRejected, redactVaultOutput(line))
			}

			return false, fmt.Errorf("%w: %s", ErrVaultStartupError, redactVaultOutput(line))
		}

		if strings.Contains(line, "ERROR:") || strings.Contains(line, "fatal:") ||
			strings.Contains(line, "Unable to initialize") {
			return false, fmt.Errorf("%w: %s", ErrVaultStartupError, redactVaultOutput(line))
		}
	}

	return false, nil
}

// isSafeNonFatalWarning reports whether a "!! " line is one safe prints while
// it keeps the vault running.
func isSafeNonFatalWarning(line string) bool {
	for _, pattern := range safeNonFatalWarnings {
		if pattern.MatchString(line) {
			return true
		}
	}

	return false
}

// vaultStartupOutput returns safe's output so far, from the tmux pane and the
// log file that tee writes, with colour codes removed.
func vaultStartupOutput(ctx context.Context, paths map[string]string) string {
	pane, _ := vaultOps.run(ctx, cleanupCommand{
		name: "tmux", args: []string{"capture-pane", "-t", paths["tmuxSession"], "-p", "-S", "-200"}, tmux: true,
	})

	logData, _ := os.ReadFile(paths["logFile"])

	return stripANSI(string(pane) + "\n" + string(logData))
}

// waitForVaultReady waits for safe to report the vault ready, and stops early
// when safe reports a failure.
//
// Only "Now targeting" counts as ready. An engine can answer on its port, and
// even be unsealed, before safe has finished setting it up, so a status check
// that something answers is not enough.
func waitForVaultReady(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error {
	log.Info("Waiting for vault to initialize...")

	for attempt := range MaxVaultReadyAttempts {
		if attempt > 0 && attempt%5 == 0 {
			log.Info(".")
		}

		ready, err := classifyVaultStartup(vaultStartupOutput(ctx, paths))
		if err != nil {
			log.Errorw("Vault startup failed", "error", err)

			return err
		}

		if ready {
			log.Info("Vault initialized successfully!")

			return nil
		}

		vaultOps.sleep(time.Second)
	}

	dumpVaultDiagnostics(ctx, paths, log)

	return ErrVaultNotReady
}

// dumpVaultDiagnostics logs the tmux pane and the log file after the vault
// failed to become ready. safe prints the seal key of a new vault, so the
// output is redacted before it is logged.
func dumpVaultDiagnostics(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) {
	pane, paneErr := vaultOps.run(ctx, cleanupCommand{
		name: "tmux", args: []string{"capture-pane", "-t", paths["tmuxSession"], "-p", "-S", "-50"}, tmux: true,
	})
	if paneErr == nil {
		log.Errorw("Vault ready timeout - tmux pane dump", "output", redactVaultOutput(stripANSI(string(pane))))
	}

	logData, logErr := os.ReadFile(paths["logFile"])
	if logErr == nil {
		log.Errorw("Vault ready timeout - log file dump", "output", redactVaultOutput(stripANSI(string(logData))))
	}
}

// targetInceptionVault points safe at the inception vault and checks that
// safe now lists the bloc's target at the bloc's port. The check reads every
// target rather than the current one, which a sibling bloc can move at any
// moment.
func targetInceptionVault(ctx context.Context, safePath string, paths map[string]string, log *zap.SugaredLogger) error {
	log.Info("Targeting inception vault...")

	vaultURL := "http://127.0.0.1:" + paths["port"]

	cmd := exec.CommandContext(ctx, safePath, "target", paths["vaultName"], vaultURL) // #nosec G204 -- safePath is the validated safe binary and the args come from getVaultInceptionPaths()

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to target inception vault: %w", err)
	}

	if !inceptionTargetRegistered(ctx, safePath, paths) {
		return ErrVaultTargetVerify
	}

	log.Infow("Successfully targeting inception vault", "vault", paths["vaultName"])

	return nil
}

// saveVaultKeys extracts and persists vault seal key and root token.
func saveVaultKeys(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error {
	// Try tmux capture-pane first, then log file
	var outputStr string

	captureCmd := exec.CommandContext(ctx, "tmux", "capture-pane", "-t", paths["tmuxSession"], "-p", "-S", "-50") // #nosec G204 -- paths come from controlled getVaultInceptionPaths() function
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
		err := os.WriteFile(paths["unsealKeysFile"], []byte(sealKey+"\n"), VaultOutputFileMode) // #nosec G703 -- path is the OCFP-managed vault output dir
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

	data, err := os.ReadFile(safeRcPath) // #nosec G304 -- safeRcPath is derived from os.UserHomeDir() and a fixed filename
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
// The data directory is left to safe local --raft, which creates it. An empty
// data directory no longer matters either way: safe treats a store as
// initialized only when it holds vault.db or raft/, and a fresh start reads
// stdin from /dev/null, so nothing can wait on an unseal prompt.
func prepareVaultDirectories(paths map[string]string, log *zap.SugaredLogger) error {
	err := os.MkdirAll(paths["logDir"], VaultDirMode)
	if err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// An archive renames <bloc>/vault away, and the keys of the vault that
	// starts next are written beside its data, so the parent must exist
	// before safe starts. In the legacy layout the parent is the home
	// directory, which already exists.
	vaultRoot := filepath.Dir(paths["vaultDir"])

	err = os.MkdirAll(vaultRoot, vaultKeyDirMode)
	if err != nil {
		return fmt.Errorf("failed to create the vault directory %s: %w", vaultRoot, err)
	}

	log.Infow("Prepared directories", "logDir", paths["logDir"], "vaultDir", vaultRoot)

	return nil
}

// runVaultInception executes the vault inception command.
func runVaultInception() error {
	return ensureInceptionVault(viper.GetString("bloc"), viper.GetBool("test"))
}

// ensureInceptionVault brings the given bloc's raft-backed inception vault up,
// or leaves it alone when it is already healthy. A stopped vault is restarted
// in place with its saved keys, and reconcileInceptionVault holds the rules
// for everything else. Exported within the package so bootstrap can guarantee
// the vault is up before the artifacts step, instead of failing late (after
// the bastion is created) on a dead vault.
func ensureInceptionVault(blocName string, testMode bool) error {
	paths := getVaultInceptionPaths(blocName, testMode)

	return withInceptionVaultLock(paths, func() error {
		return ensureInceptionVaultLocked(blocName, paths)
	})
}

// ensureInceptionVaultLocked does the work of ensureInceptionVault while the
// bloc's inception vault lock is held.
func ensureInceptionVaultLocked(blocName string, paths map[string]string) error {
	log := logger.Get()

	log.Info("=== Starting OCFP Vault Inception ===")
	log.Infow("Configuration",
		"bloc", blocName,
		"vault_name", paths["vaultName"],
		"port", paths["port"],
		"cluster_port", paths["clusterPort"],
		"tmux_session", paths["tmuxSession"],
		"vault_dir", paths["vaultDir"],
	)

	err := requireClusterPort(paths)
	if err != nil {
		return err
	}

	tools, err := checkVaultInceptionPrerequisites(context.TODO(), log)
	if err != nil {
		return fmt.Errorf("prerequisite check failed: %w", err)
	}

	return reconcileInceptionVault(context.TODO(), &inceptionRun{
		paths: paths,
		tools: tools,
		steps: newInceptionSteps(tools),
		log:   log,
	})
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

	return withInceptionVaultLock(paths, func() error {
		log.Info("=== Tearing Down Inception Vault ===")

		err := guardInceptionTeardown(context.TODO(), paths, probeInceptionVault, ownsInceptionVault)
		if err != nil {
			return fmt.Errorf("teardown refused: %w", err)
		}

		err = cleanupExistingVault(context.TODO(), paths, log)
		if err != nil {
			return fmt.Errorf("teardown failed: %w", err)
		}

		log.Info("=== Teardown Completed ===")

		return nil
	})
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
	// The bloc name determines the log directory structure:
	// {StateHome}/{bloc}/logs/vault/migrate/
	blocName := viper.GetString("bloc")
	if blocName == "" {
		blocName = os.Getenv("OCFP_BLOC")
	}

	// Reinitialize logger with migrate subcommand for proper log path.
	// Logger appends {bloc}/logs/{command}/{subcommand} itself; pass the
	// state-class root, not the legacy flat ~/.ocfp home.
	logDir := config.StateHome()

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
