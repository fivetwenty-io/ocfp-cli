package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	// Command arguments.
	rsyncTwoArgs = 2

	// rsyncModelledFlagCount is how many rsync flags ocfp models as its own
	// boolean flags, used to size the slice that renders them.
	rsyncModelledFlagCount = 6
)

// NewRSyncCmd creates the rsync command.
func NewRSyncCmd() *cobra.Command {
	opts := &rsyncFlagTargets{} //nolint:exhaustruct // Every field is a pointer target filled in by pflag.

	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional fields
		Use:     "rsync <source> <destination> [-- <rsync flag>...]",
		Short:   "Synchronize files to/from bastion host",
		Long:    getRSyncLongDescription(),
		Example: getRSyncExamples(),
		Args:    validateRSyncArgs,
		RunE:    runRSync,
	}

	cmd.SetFlagErrorFunc(rsyncFlagError)
	addRSyncFlags(cmd, opts)
	bindRSyncViperFlags(cmd)

	return cmd
}

// rsyncFlagTargets holds the variables that pflag writes the command's own
// flags into. They are only read back through viper, so the struct exists to
// keep the flag registration readable.
type rsyncFlagTargets struct {
	user           string
	key            string
	archive        bool
	compress       bool
	verbose        bool
	deleteFiles    bool
	dryRun         bool
	itemizeChanges bool
	exclude        []string
	include        []string
	rsyncOptions   string
}

// rsyncFlagError explains the -- separator whenever cobra rejects a flag, so
// that an rsync flag ocfp does not model reads as a nudge rather than a wall.
func rsyncFlagError(_ *cobra.Command, err error) error {
	return fmt.Errorf("%w\n\nocfp rsync models only the most common rsync flags. "+
		"Put any other rsync flag after a -- separator and it is handed to rsync unchanged, for example:\n"+
		"  ocfp rsync --bloc <bloc> <source> <destination> -- --info=progress2 --filter=':- .gitignore'", err)
}

// rsyncPositionalArgs returns the arguments before the -- separator, which are
// the source and the destination.
func rsyncPositionalArgs(cmd *cobra.Command, args []string) []string {
	dash := cmd.ArgsLenAtDash()
	if dash < 0 || dash > len(args) {
		return args
	}

	return args[:dash]
}

// rsyncPassthroughArgs returns the arguments after the -- separator, which are
// handed to rsync verbatim and in the order the caller wrote them.
func rsyncPassthroughArgs(cmd *cobra.Command, args []string) []string {
	dash := cmd.ArgsLenAtDash()
	if dash < 0 || dash > len(args) {
		return nil
	}

	return args[dash:]
}

// validateRSyncArgs requires a source and a destination, counting only the
// arguments before the -- separator.
func validateRSyncArgs(cmd *cobra.Command, args []string) error {
	if len(rsyncPositionalArgs(cmd, args)) != rsyncTwoArgs {
		return ErrRsyncNeedsSourceAndDestination
	}

	return nil
}

func getRSyncLongDescription() string {
	return `RSyncs files between the local machine and the bastion host using rsync.

The command supports bidirectional synchronization using the bastion: prefix
convention:
- Local to bastion: ocfp rsync /local/dir/ bastion:/remote/dir/
- Bastion to local: ocfp rsync bastion:/remote/dir/ /local/dir/

The bastion: prefix is automatically resolved to the bastion host's public IP
address, which is discovered from the bloc configuration. The --bloc flag is
required to identify which environment to connect to.

Advanced options include:
- Archive mode preserves permissions, ownership, timestamps
- Compression for efficient transfer
- Delete mode for mirror synchronization
- Include/exclude patterns for selective sync
- Itemized change summaries for auditing a dry run

Only the most common rsync flags are modelled as ocfp flags. Every other rsync
flag goes after a -- separator, and everything after that separator is handed
to rsync unchanged and in the order it was written, so any rsync flag at all
can be used:
  ocfp rsync --bloc <bloc> <source> <destination> -- --info=progress2 --copy-links
The source and the destination stay in front of the separator, and ocfp's own
flags, such as --bloc, are never forwarded to rsync.

Prefer rsync over scp for large directories or repeated transfers, as rsync
uses delta transfers to only send changed portions of files.

SSH keys are searched in the following order:
1. ~/.local/share/ocfp/{bloc}/ssh/id_ed25519 (preferred, or $XDG_DATA_HOME/ocfp/{bloc}/ssh/id_ed25519 if set)
2. ~/.local/share/ocfp/{bloc}/ssh/id_rsa (fallback)
3. ~/.ocfp/{bloc}/ssh/id_ed25519 or id_rsa (legacy, used only if the above are absent)

Set OCFP_HOME to force the legacy ~/.ocfp layout for all three lookups.`
}

func getRSyncExamples() string {
	return `  # Sync directory to bastion
  ocfp rsync --bloc production /local/dir/ bastion:/remote/dir/

  # Sync from bastion to local
  ocfp rsync --bloc production bastion:/remote/dir/ /local/dir/

  # Sync from bastion with compression
  ocfp rsync --bloc production --compress bastion:/remote/dir/ /local/dir/

  # Mirror sync with delete (removes extra files in destination)
  ocfp rsync --bloc production --delete /local/dir/ bastion:/remote/dir/

  # Dry run to see what would be synced
  ocfp rsync --bloc production --dry-run /local/dir/ bastion:/remote/dir/

  # Exclude certain files
  ocfp rsync --bloc production --exclude "*.tmp" --exclude ".git" /local/dir/ bastion:/remote/dir/

  # Include only YAML files, exclude everything else
  ocfp rsync --bloc production --include "*.yml" --exclude "*" /local/configs/ bastion:/remote/configs/

  # Combine archive, verbose, and compress flags
  ocfp rsync --bloc production -a -v -z /local/dir/ bastion:/remote/dir/

  # Itemize every change a dry run would make
  ocfp rsync --bloc production --dry-run --itemize-changes /local/dir/ bastion:/remote/dir/

  # Hand rsync flags that ocfp does not model straight through after --
  ocfp rsync --bloc production /local/dir/ bastion:/remote/dir/ -- --info=progress2 --copy-links`
}

func addRSyncFlags(cmd *cobra.Command, opts *rsyncFlagTargets) {
	cmd.Flags().StringVar(&opts.user, "user", "ubuntu", "username for rsync")
	cmd.Flags().StringVar(&opts.key, "key", "", "path to SSH private key")
	cmd.Flags().BoolVarP(&opts.archive, "archive", "a", true, "archive mode (preserves permissions, etc)")
	cmd.Flags().BoolVarP(&opts.compress, "compress", "z", false, "compress data during transfer")
	cmd.Flags().BoolVarP(&opts.verbose, "verbose", "v", false, "verbose output")
	cmd.Flags().BoolVar(&opts.deleteFiles, "delete", false, "delete files in destination not present in source")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "perform a trial run with no changes made")
	cmd.Flags().BoolVarP(&opts.itemizeChanges, "itemize-changes", "i", false, "output a change-summary for all updates")
	cmd.Flags().StringSliceVar(&opts.exclude, "exclude", nil, "exclude files matching pattern")
	cmd.Flags().StringSliceVar(&opts.include, "include", nil, "include files matching pattern")
	cmd.Flags().StringVar(&opts.rsyncOptions, "rsync-options", "", "additional rsync options")
}

func bindRSyncViperFlags(cmd *cobra.Command) {
	bindFlagsToViper(cmd, map[string]string{
		"rsync.user":            "user",
		"rsync.key":             "key",
		"rsync.archive":         "archive",
		"rsync.compress":        "compress",
		"rsync.verbose":         "verbose",
		"rsync.delete":          "delete",
		"rsync.dry_run":         "dry-run",
		"rsync.itemize_changes": "itemize-changes",
		"rsync.exclude":         "exclude",
		"rsync.include":         "include",
		"rsync.options":         "rsync-options",
	})
}

func runRSync(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	log := logger.WithOperation("rsync")

	config, err := parseRSyncConfig(cmd, args)
	if err != nil {
		return err
	}

	environment, err := setupRSyncEnvironment(ctx, config)
	if err != nil {
		return err
	}

	return executeRSyncWithEnvironment(ctx, log, config, environment)
}

type rsyncConfig struct {
	blocName       string
	user           string
	keyPath        string
	archive        bool
	compress       bool
	verbose        bool
	deleteFlag     bool
	dryRun         bool
	itemizeChanges bool
	exclude        []string
	include        []string
	rsyncOptions   string
	passthrough    []string
	source         string
	destination    string
}

type rsyncEnvironment struct {
	provider  cpi.Provider
	config    *config.Config
	bastionIP string
	keyPath   string
}

func parseRSyncConfig(cmd *cobra.Command, args []string) (*rsyncConfig, error) {
	blocName := viper.GetString("bloc")
	if blocName == "" {
		return nil, ErrBlocIsRequired
	}

	positional := rsyncPositionalArgs(cmd, args)
	if len(positional) != rsyncTwoArgs {
		return nil, ErrRsyncNeedsSourceAndDestination
	}

	return &rsyncConfig{
		blocName:       blocName,
		user:           viper.GetString("rsync.user"),
		keyPath:        viper.GetString("rsync.key"),
		archive:        viper.GetBool("rsync.archive"),
		compress:       viper.GetBool("rsync.compress"),
		verbose:        viper.GetBool("rsync.verbose"),
		deleteFlag:     viper.GetBool("rsync.delete"),
		dryRun:         viper.GetBool("rsync.dry_run"),
		itemizeChanges: viper.GetBool("rsync.itemize_changes"),
		exclude:        viper.GetStringSlice("rsync.exclude"),
		include:        viper.GetStringSlice("rsync.include"),
		rsyncOptions:   viper.GetString("rsync.options"),
		passthrough:    rsyncPassthroughArgs(cmd, args),
		source:         positional[0],
		destination:    positional[1],
	}, nil
}

func setupRSyncEnvironment(ctx context.Context, config *rsyncConfig) (*rsyncEnvironment, error) {
	cfg, err := loadRSyncBlocConfig(config.blocName)
	if err != nil {
		return nil, err
	}

	provider, err := initializeRSyncProvider(ctx, cfg)
	if err != nil {
		return nil, err
	}

	bastionIP, err := getBastionIPForRSync(ctx, provider, config.blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to get bastion IP: %w", err)
	}

	keyPath, err := resolveSSHKey(config.keyPath, config.blocName, cfg)
	if err != nil {
		return nil, err
	}

	return &rsyncEnvironment{
		provider:  provider,
		config:    cfg,
		bastionIP: bastionIP,
		keyPath:   keyPath,
	}, nil
}

func loadRSyncBlocConfig(blocName string) (*config.Config, error) {
	cfg, err := config.LoadWithParams(viper.GetString("config"), blocName)
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}

	if cfg.Provider == "" && cfg.IaaS == "" {
		return nil, ErrProviderMustBeSpecifiedInBlocConfig(blocName)
	}

	return cfg, nil
}

//nolint:ireturn // Provider interface is needed for polymorphism
func initializeRSyncProvider(ctx context.Context, cfg *config.Config) (cpi.Provider, error) {
	provider, err := cpi.GetProvider(cfg.Provider)
	if err != nil {
		return nil, fmt.Errorf("failed to get provider %s: %w", cfg.Provider, err)
	}

	err = provider.Initialize(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize provider %s: %w", cfg.Provider, err)
	}

	return provider, nil
}

func resolveSSHKey(keyPath, blocName string, cfg *config.Config) (string, error) {
	if keyPath == "" {
		resolvedKey, err := findSSHKey(blocName, cfg)
		if err != nil {
			return "", fmt.Errorf("failed to find SSH key: %w", err)
		}

		keyPath = resolvedKey
	}

	err := verifySSHKey(keyPath)
	if err != nil {
		return "", fmt.Errorf("SSH key verification failed: %w", err)
	}

	return keyPath, nil
}

func executeRSyncWithEnvironment(ctx context.Context, log logger.Logger, config *rsyncConfig, env *rsyncEnvironment) error {
	processedSource := processRSyncPath(config.source, env.bastionIP, config.user)
	processedDest := processRSyncPath(config.destination, env.bastionIP, config.user)

	rsyncCmd := buildRSyncCommand(config, processedSource, processedDest, env.keyPath)

	if config.dryRun {
		log.Infof("Performing dry run: %s -> %s", config.source, config.destination)
	} else {
		log.Infof("Synchronizing: %s -> %s", config.source, config.destination)
	}

	log.Debugf("Using SSH key: %s", env.keyPath)
	log.Debugf("Bastion IP: %s", env.bastionIP)

	return executeRSync(ctx, rsyncCmd)
}

// getBastionIPForRSync retrieves the bastion host's public IP address.
func getBastionIPForRSync(ctx context.Context, provider cpi.Provider, blocName string) (string, error) {
	// Delegate to shared helper for robust lookup
	return findBastionIP(ctx, provider, blocName)
}

// findSSHKeyForRSync locates the SSH private key.
// findSSHKeyForRSync and verifySSHKeyForRSync are removed in favor of shared helpers

// processRSyncPath converts bastion: references to proper rsync format.
func processRSyncPath(path, bastionIP, user string) string {
	if strings.HasPrefix(path, "bastion:") {
		// Replace bastion: with user@bastionIP:
		remotePath := strings.TrimPrefix(path, "bastion:")

		return fmt.Sprintf("%s@%s:%s", user, bastionIP, remotePath)
	}

	return path
}

// buildRSyncCommand constructs the rsync command with all options.
func buildRSyncCommand(config *rsyncConfig, source, destination, keyPath string) []string {
	cmd := []string{"rsync"}

	cmd = append(cmd, rsyncModelledFlags(config)...)

	// Add progress indicator
	cmd = append(cmd, "--progress")

	// Add SSH options
	sshCmd := "ssh -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o LogLevel=ERROR"
	if keyPath != "" {
		sshCmd += " -i " + keyPath
	}

	cmd = append(cmd, "-e", sshCmd)

	// Add exclude patterns
	for _, pattern := range config.exclude {
		cmd = append(cmd, "--exclude", pattern)
	}

	// Add include patterns
	for _, pattern := range config.include {
		cmd = append(cmd, "--include", pattern)
	}

	// Add extra options if provided
	if config.rsyncOptions != "" {
		cmd = append(cmd, strings.Fields(config.rsyncOptions)...)
	}

	// Hand everything after the -- separator to rsync unchanged, keeping the
	// order the caller wrote it in.
	cmd = append(cmd, config.passthrough...)

	// Add source and destination
	cmd = append(cmd, source, destination)

	return cmd
}

// rsyncModelledFlags renders the rsync flags that ocfp models as its own.
func rsyncModelledFlags(config *rsyncConfig) []string {
	flags := make([]string, 0, rsyncModelledFlagCount)

	if config.archive {
		flags = append(flags, "-a")
	}

	if config.compress {
		flags = append(flags, "-z")
	}

	if config.verbose {
		flags = append(flags, "-v")
	}

	if config.deleteFlag {
		flags = append(flags, "--delete")
	}

	if config.dryRun {
		flags = append(flags, "--dry-run")
	}

	if config.itemizeChanges {
		flags = append(flags, "--itemize-changes")
	}

	return flags
}

// executeRSync executes the rsync command.
func executeRSync(ctx context.Context, rsyncCmd []string) error {
	log := logger.WithOperation("executeRSync")
	log.Debugf("Executing: %s", strings.Join(rsyncCmd, " "))

	// Validate that the command is rsync
	if len(rsyncCmd) == 0 || rsyncCmd[0] != "rsync" {
		return ErrInvalidRsyncCommand
	}

	cmd := exec.CommandContext(ctx, rsyncCmd[0], rsyncCmd[1:]...) // #nosec G204 - command is validated above
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("rsync command failed: %w", err)
	}

	return nil
}
