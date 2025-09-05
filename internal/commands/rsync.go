package commands

import (
    "context"
    "fmt"
    "errors"
    "os"
    "os/exec"
    "strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// NewRSyncCmd creates the rsync command.
func NewRSyncCmd() *cobra.Command {
	var (
		user         string
		key          string
		archive      bool
		compress     bool
		verbose      bool
		delete       bool
		dryRun       bool
		exclude      []string
		include      []string
		rsyncOptions string
	)

	cmd := &cobra.Command{
		Use:   "rsync <source> <destination>",
		Short: "Synchronize files to/from bastion host",
		Long: `RSyncs files between the local machine and the bastion host using rsync.

The command supports bidirectional synchronization with advanced options:
- Archive mode preserves permissions, ownership, timestamps
- Compression for efficient transfer
- Delete mode for mirror synchronization
- Include/exclude patterns for selective sync

The bastion host is automatically discovered using the bloc configuration.`,
		Example: `  # Sync directory to bastion
  ocfp rsync --bloc production /local/dir/ bastion:/remote/dir/

  # Sync from bastion with compression
  ocfp rsync --bloc production --compress bastion:/remote/dir/ /local/dir/

  # Mirror sync with delete (removes extra files in destination)
  ocfp rsync --bloc production --delete /local/dir/ bastion:/remote/dir/

  # Dry run to see what would be synced
  ocfp rsync --bloc production --dry-run /local/dir/ bastion:/remote/dir/

  # Exclude certain files
  ocfp rsync --bloc production --exclude "*.tmp" --exclude ".git" /local/dir/ bastion:/remote/dir/`,
            Args: cobra.ExactArgs(2),
            RunE:   runRSync,
	}

	// Command-specific flags
	cmd.Flags().StringVar(&user, "user", "ubuntu", "username for rsync")
	cmd.Flags().StringVar(&key, "key", "", "path to SSH private key")
	cmd.Flags().BoolVarP(&archive, "archive", "a", true, "archive mode (preserves permissions, etc)")
	cmd.Flags().BoolVarP(&compress, "compress", "z", false, "compress data during transfer")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	cmd.Flags().BoolVar(&delete, "delete", false, "delete files in destination not present in source")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "perform a trial run with no changes made")
	cmd.Flags().StringSliceVar(&exclude, "exclude", nil, "exclude files matching pattern")
	cmd.Flags().StringSliceVar(&include, "include", nil, "include files matching pattern")
	cmd.Flags().StringVar(&rsyncOptions, "rsync-options", "", "additional rsync options")

	// Bind flags to viper
	_ = viper.BindPFlag("rsync.user", cmd.Flags().Lookup("user"))
	_ = viper.BindPFlag("rsync.key", cmd.Flags().Lookup("key"))
	_ = viper.BindPFlag("rsync.archive", cmd.Flags().Lookup("archive"))
	_ = viper.BindPFlag("rsync.compress", cmd.Flags().Lookup("compress"))
	_ = viper.BindPFlag("rsync.verbose", cmd.Flags().Lookup("verbose"))
	_ = viper.BindPFlag("rsync.delete", cmd.Flags().Lookup("delete"))
	_ = viper.BindPFlag("rsync.dry_run", cmd.Flags().Lookup("dry-run"))
	_ = viper.BindPFlag("rsync.exclude", cmd.Flags().Lookup("exclude"))
	_ = viper.BindPFlag("rsync.include", cmd.Flags().Lookup("include"))
	_ = viper.BindPFlag("rsync.options", cmd.Flags().Lookup("rsync-options"))

	return cmd
}

func runRSync(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	log := logger.WithOperation("rsync")

	// Get configuration values
	blocName := viper.GetString("bloc_name")
	user := viper.GetString("rsync.user")
	keyPath := viper.GetString("rsync.key")
	archive := viper.GetBool("rsync.archive")
	compress := viper.GetBool("rsync.compress")
	verbose := viper.GetBool("rsync.verbose")
	deleteFlag := viper.GetBool("rsync.delete")
	dryRun := viper.GetBool("rsync.dry_run")
	exclude := viper.GetStringSlice("rsync.exclude")
	include := viper.GetStringSlice("rsync.include")
	rsyncOptions := viper.GetString("rsync.options")

	source := args[0]
	destination := args[1]

	// Validate required configuration
	if blocName == "" {
		return errors.New("bloc is required")
	}

	// Load configuration; provider and region come from bloc config
	cfg, err := config.LoadWithParams(viper.GetString("config.file"), blocName)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	if cfg.Provider == "" && cfg.IaaS == "" {
		return errors.New("provider must be specified in bloc config")
	}

	// Initialize provider using bloc configuration
	provider, err := cpi.GetProvider(cfg.Provider)
	if err != nil {
		return fmt.Errorf("failed to get provider %s: %w", cfg.Provider, err)
	}

	if err := provider.Initialize(ctx, cfg); err != nil {
		return fmt.Errorf("failed to initialize provider %s: %w", cfg.Provider, err)
	}

	// Get bastion IP address
	bastionIP, err := getBastionIPForRSync(ctx, provider, blocName)
	if err != nil {
		return fmt.Errorf("failed to get bastion IP: %w", err)
	}

    // Find SSH key if not specified
    if keyPath == "" {
        keyPath, err = findSSHKey(blocName, cfg)
        if err != nil {
            return fmt.Errorf("failed to find SSH key: %w", err)
        }
    }

    // Verify key exists and has correct permissions
    if err := verifySSHKey(keyPath); err != nil {
        return fmt.Errorf("SSH key verification failed: %w", err)
    }

	// Process source and destination paths
	processedSource := processRSyncPath(source, bastionIP, user)
	processedDest := processRSyncPath(destination, bastionIP, user)

	// Build rsync command
	rsyncCmd := buildRSyncCommand(processedSource, processedDest, keyPath, archive, compress,
		verbose, deleteFlag, dryRun, exclude, include, rsyncOptions)

	if dryRun {
		log.Infof("Performing dry run: %s -> %s", source, destination)
	} else {
		log.Infof("Synchronizing: %s -> %s", source, destination)
	}

	log.Debugf("Using SSH key: %s", keyPath)
	log.Debugf("Bastion IP: %s", bastionIP)

	// Execute rsync command
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
		return errors.New("invalid rsync command")
	}

    cmd := exec.CommandContext(ctx, rsyncCmd[0], rsyncCmd[1:]...) // #nosec G204 - command is validated above
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
