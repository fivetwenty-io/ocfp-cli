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
)

// NewRSyncCmd creates the rsync command.
func NewRSyncCmd() *cobra.Command {
	var (
		user         string
		key          string
		archive      bool
		compress     bool
		verbose      bool
		deleteFiles  bool
		dryRun       bool
		exclude      []string
		include      []string
		rsyncOptions string
	)

	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional fields
		Use:     "rsync <source> <destination>",
		Short:   "Synchronize files to/from bastion host",
		Long:    getRSyncLongDescription(),
		Example: getRSyncExamples(),
		Args:    cobra.ExactArgs(rsyncTwoArgs),
		RunE:    runRSync,
	}

	addRSyncFlags(cmd, &user, &key, &archive, &compress, &verbose, &deleteFiles, &dryRun, &exclude, &include, &rsyncOptions)
	bindRSyncViperFlags(cmd)

	return cmd
}

func getRSyncLongDescription() string {
	return `RSyncs files between the local machine and the bastion host using rsync.

The command supports bidirectional synchronization with advanced options:
- Archive mode preserves permissions, ownership, timestamps
- Compression for efficient transfer
- Delete mode for mirror synchronization
- Include/exclude patterns for selective sync

The bastion host is automatically discovered using the bloc configuration.`
}

func getRSyncExamples() string {
	return `  # Sync directory to bastion
  ocfp rsync --bloc production /local/dir/ bastion:/remote/dir/

  # Sync from bastion with compression
  ocfp rsync --bloc production --compress bastion:/remote/dir/ /local/dir/

  # Mirror sync with delete (removes extra files in destination)
  ocfp rsync --bloc production --delete /local/dir/ bastion:/remote/dir/

  # Dry run to see what would be synced
  ocfp rsync --bloc production --dry-run /local/dir/ bastion:/remote/dir/

  # Exclude certain files
  ocfp rsync --bloc production --exclude "*.tmp" --exclude ".git" /local/dir/ bastion:/remote/dir/`
}

func addRSyncFlags(cmd *cobra.Command, user, key *string, archive, compress, verbose, deleteFiles, dryRun *bool, exclude, include *[]string, rsyncOptions *string) {
	cmd.Flags().StringVar(user, "user", "ubuntu", "username for rsync")
	cmd.Flags().StringVar(key, "key", "", "path to SSH private key")
	cmd.Flags().BoolVarP(archive, "archive", "a", true, "archive mode (preserves permissions, etc)")
	cmd.Flags().BoolVarP(compress, "compress", "z", false, "compress data during transfer")
	cmd.Flags().BoolVarP(verbose, "verbose", "v", false, "verbose output")
	cmd.Flags().BoolVar(deleteFiles, "delete", false, "delete files in destination not present in source")
	cmd.Flags().BoolVar(dryRun, "dry-run", false, "perform a trial run with no changes made")
	cmd.Flags().StringSliceVar(exclude, "exclude", nil, "exclude files matching pattern")
	cmd.Flags().StringSliceVar(include, "include", nil, "include files matching pattern")
	cmd.Flags().StringVar(rsyncOptions, "rsync-options", "", "additional rsync options")
}

func bindRSyncViperFlags(cmd *cobra.Command) {
	bindFlagsToViper(cmd, map[string]string{
		"rsync.user":     "user",
		"rsync.key":      "key",
		"rsync.archive":  "archive",
		"rsync.compress": "compress",
		"rsync.verbose":  "verbose",
		"rsync.delete":   "delete",
		"rsync.dry_run":  "dry-run",
		"rsync.exclude":  "exclude",
		"rsync.include":  "include",
		"rsync.options":  "rsync-options",
	})
}

func runRSync(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	log := logger.WithOperation("rsync")

	config, err := parseRSyncConfig(args)
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
	blocName     string
	user         string
	keyPath      string
	archive      bool
	compress     bool
	verbose      bool
	deleteFlag   bool
	dryRun       bool
	exclude      []string
	include      []string
	rsyncOptions string
	source       string
	destination  string
}

type rsyncEnvironment struct {
	provider  cpi.Provider
	config    *config.Config
	bastionIP string
	keyPath   string
}

func parseRSyncConfig(args []string) (*rsyncConfig, error) {
	blocName := viper.GetString("bloc")
	if blocName == "" {
		return nil, ErrBlocIsRequired
	}

	return &rsyncConfig{
		blocName:     blocName,
		user:         viper.GetString("rsync.user"),
		keyPath:      viper.GetString("rsync.key"),
		archive:      viper.GetBool("rsync.archive"),
		compress:     viper.GetBool("rsync.compress"),
		verbose:      viper.GetBool("rsync.verbose"),
		deleteFlag:   viper.GetBool("rsync.delete"),
		dryRun:       viper.GetBool("rsync.dry_run"),
		exclude:      viper.GetStringSlice("rsync.exclude"),
		include:      viper.GetStringSlice("rsync.include"),
		rsyncOptions: viper.GetString("rsync.options"),
		source:       args[0],
		destination:  args[1],
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

	rsyncCmd := buildRSyncCommand(processedSource, processedDest, env.keyPath, config.archive, config.compress,
		config.verbose, config.deleteFlag, config.dryRun, config.exclude, config.include, config.rsyncOptions)

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
func buildRSyncCommand(source, destination, keyPath string, archive, compress, verbose,
	deleteFlag, dryRun bool, exclude, include []string, extraOptions string) []string {
	cmd := []string{"rsync"}

	// Add flags
	if archive {
		cmd = append(cmd, "-a")
	}

	if compress {
		cmd = append(cmd, "-z")
	}

	if verbose {
		cmd = append(cmd, "-v")
	}

	if deleteFlag {
		cmd = append(cmd, "--delete")
	}

	if dryRun {
		cmd = append(cmd, "--dry-run")
	}

	// Add progress indicator
	cmd = append(cmd, "--progress")

	// Add SSH options
	sshCmd := "ssh -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o LogLevel=ERROR"
	if keyPath != "" {
		sshCmd += " -i " + keyPath
	}

	cmd = append(cmd, "-e", sshCmd)

	// Add exclude patterns
	for _, pattern := range exclude {
		cmd = append(cmd, "--exclude", pattern)
	}

	// Add include patterns
	for _, pattern := range include {
		cmd = append(cmd, "--include", pattern)
	}

	// Add extra options if provided
	if extraOptions != "" {
		options := strings.Fields(extraOptions)
		cmd = append(cmd, options...)
	}

	// Add source and destination
	cmd = append(cmd, source, destination)

	return cmd
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
