package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// NewRSyncCmd creates the rsync command
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
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRSync(cmd, args)
		},
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
		return fmt.Errorf("bloc is required")
	}

	// Load configuration first - this will provide the provider if not set via flags
	cfg, err := config.LoadWithParams(viper.GetString("config.file"), blocName)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Get iaas provider - either from flags/env or from loaded configuration
	iaas := viper.GetString("iaas")
	if iaas == "" {
		// Try to get provider from loaded configuration
		if cfg.Provider != "" {
			iaas = cfg.Provider
		} else if cfg.IaaS != "" {
			iaas = cfg.IaaS
		}
	}

	// Now validate we have a provider
	if iaas == "" {
		return fmt.Errorf("iaas provider is required (not found in flags or configuration)")
	}

	// Create provider config from the loaded configuration
	providerConfig := map[string]interface{}{
		"project_id":            cfg.ProjectID,
		"org_id":                cfg.OrgID,
		"auth_token":            cfg.AuthToken,
		"service_account_token": cfg.ServiceAccountToken,
		"service_account_json":  cfg.ServiceAccountJSON,
		"region":                cfg.Region,
	}

	// Initialize provider
	provider, err := cpi.CreateProvider(ctx, iaas, providerConfig)
	if err != nil {
		return fmt.Errorf("failed to initialize provider %s: %w", iaas, err)
	}

	// Get bastion IP address
	bastionIP, err := getBastionIPForRSync(ctx, provider, blocName)
	if err != nil {
		return fmt.Errorf("failed to get bastion IP: %w", err)
	}

	// Find SSH key if not specified
	if keyPath == "" {
		keyPath, err = findSSHKeyForRSync(blocName, cfg)
		if err != nil {
			return fmt.Errorf("failed to find SSH key: %w", err)
		}
	}

	// Verify key exists and has correct permissions
	if err := verifySSHKeyForRSync(keyPath); err != nil {
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
	return executeRSync(rsyncCmd)
}

// getBastionIPForRSync retrieves the bastion host's public IP address
func getBastionIPForRSync(ctx context.Context, provider cpi.Provider, blocName string) (string, error) {
	log := logger.WithOperation("getBastionIPForRSync")

	// List instances with bastion tag/label
	filters := map[string]string{
		"label.bloc":      blocName,
		"label.component": "bastion",
	}

	instances, err := provider.Compute().ListInstances(ctx, filters)
	if err != nil {
		return "", fmt.Errorf("failed to list instances: %w", err)
	}

	if len(instances) == 0 {
		return "", fmt.Errorf("no bastion host found for bloc %s", blocName)
	}

	// Get the first bastion's floating IP
	bastion := instances[0]
	if bastion.FloatingIP == "" {
		// Try to find associated floating IP
		floatingIPs, err := provider.Network().ListFloatingIPs(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to list floating IPs: %w", err)
		}

		// Find floating IP associated with this instance
		for _, fip := range floatingIPs {
			if fip.InstanceID == bastion.ID {
				bastion.FloatingIP = fip.Address
				break
			}
		}

		if bastion.FloatingIP == "" {
			return "", fmt.Errorf("bastion %s has no public IP address", bastion.Name)
		}
	}

	log.Debugf("Found bastion IP: %s", bastion.FloatingIP)
	return bastion.FloatingIP, nil
}

// findSSHKeyForRSync locates the SSH private key
func findSSHKeyForRSync(blocName string, cfg *config.Config) (string, error) {
	log := logger.WithOperation("findSSHKeyForRSync")

	// Search paths in order of preference
	searchPaths := []string{
		// 1. ~/.ocfp/keys/{bloc_name}-bastion/id_rsa
		filepath.Join(os.Getenv("HOME"), ".ocfp", "keys", blocName+"-bastion", "id_rsa"),
		// 2. ~/.ssh/{bloc_name}-bastion
		filepath.Join(os.Getenv("HOME"), ".ssh", blocName+"-bastion"),
		// 3. ~/.ssh/{bloc_name}-bastion.pem
		filepath.Join(os.Getenv("HOME"), ".ssh", blocName+"-bastion.pem"),
		// 4. From config ssh_key_storage_dir
		filepath.Join(cfg.SSHKeyStorageDir, blocName+"-bastion"),
		filepath.Join(cfg.SSHKeyStorageDir, blocName+"-bastion.pem"),
	}

	for _, path := range searchPaths {
		if _, err := os.Stat(path); err == nil {
			log.Debugf("Found SSH key at: %s", path)
			return path, nil
		}
	}

	// Try to find any key with bastion in the name
	sshDir := filepath.Join(os.Getenv("HOME"), ".ssh")
	entries, err := os.ReadDir(sshDir)
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.Contains(entry.Name(), "bastion") {
				path := filepath.Join(sshDir, entry.Name())
				log.Debugf("Found SSH key at: %s", path)
				return path, nil
			}
		}
	}

	return "", fmt.Errorf("could not find SSH key for bastion. Searched paths: %v", searchPaths)
}

// verifySSHKeyForRSync checks if the SSH key exists and has correct permissions
func verifySSHKeyForRSync(keyPath string) error {
	info, err := os.Stat(keyPath)
	if err != nil {
		return fmt.Errorf("SSH key not found: %s", keyPath)
	}

	// Check permissions (should be 600 or 400)
	mode := info.Mode()
	if mode.Perm()&0077 != 0 {
		// Try to fix permissions
		if err := os.Chmod(keyPath, 0600); err != nil {
			return fmt.Errorf("SSH key has incorrect permissions and couldn't fix: %s", keyPath)
		}
		logger.WithOperation("verifySSHKeyForRSync").Warnf("Fixed SSH key permissions for: %s", keyPath)
	}

	return nil
}

// processRSyncPath converts bastion: references to proper rsync format
func processRSyncPath(path, bastionIP, user string) string {
	if strings.HasPrefix(path, "bastion:") {
		// Replace bastion: with user@bastionIP:
		remotePath := strings.TrimPrefix(path, "bastion:")
		return fmt.Sprintf("%s@%s:%s", user, bastionIP, remotePath)
	}
	return path
}

// buildRSyncCommand constructs the rsync command with all options
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
		sshCmd += fmt.Sprintf(" -i %s", keyPath)
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

// executeRSync executes the rsync command
func executeRSync(rsyncCmd []string) error {
	log := logger.WithOperation("executeRSync")
	log.Debugf("Executing: %s", strings.Join(rsyncCmd, " "))

	// Validate that the command is rsync
	if len(rsyncCmd) == 0 || rsyncCmd[0] != "rsync" {
		return fmt.Errorf("invalid rsync command")
	}

	cmd := exec.Command(rsyncCmd[0], rsyncCmd[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
