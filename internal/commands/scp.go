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

// NewSCPCmd creates the SCP command.
func NewSCPCmd() *cobra.Command {
	var (
		user       string
		key        string
		recursive  bool
		scpOptions string
	)

	cmd := &cobra.Command{
		Use:   "scp <source> <destination>",
		Short: "Copy files to/from bastion host",
		Long: `SCP copies files between the local machine and the bastion host.

The command supports bidirectional transfers:
- Local to bastion: ocfp scp /local/file bastion:/remote/path
- Bastion to local: ocfp scp bastion:/remote/file /local/path

The bastion host is automatically discovered using the bloc configuration.`,
		Example: `  # Copy file to bastion
  ocfp scp --bloc production /local/file.txt bastion:/tmp/

  # Copy file from bastion
  ocfp scp --bloc production bastion:/etc/config.yml ./config.yml

  # Recursive copy of directory
  ocfp scp --bloc production -r /local/dir/ bastion:/remote/dir/

  # Use specific SSH key
  ocfp scp --bloc production --key ~/.ssh/custom-key.pem file.txt bastion:/tmp/`,
            Args: cobra.ExactArgs(2),
            RunE:   runSCP,
	}

	// Command-specific flags
	cmd.Flags().StringVar(&user, "user", "ubuntu", "username for SCP")
	cmd.Flags().StringVar(&key, "key", "", "path to SSH private key")
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "recursively copy directories")
	cmd.Flags().StringVar(&scpOptions, "scp-options", "", "additional SCP options")

	// Bind flags to viper
	_ = viper.BindPFlag("scp.user", cmd.Flags().Lookup("user"))
	_ = viper.BindPFlag("scp.key", cmd.Flags().Lookup("key"))
	_ = viper.BindPFlag("scp.recursive", cmd.Flags().Lookup("recursive"))
	_ = viper.BindPFlag("scp.options", cmd.Flags().Lookup("scp-options"))

	return cmd
}

func runSCP(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	log := logger.WithOperation("scp")

	// Get configuration values
	blocName := viper.GetString("bloc_name")
	user := viper.GetString("scp.user")
	keyPath := viper.GetString("scp.key")
	recursive := viper.GetBool("scp.recursive")
	scpOptions := viper.GetString("scp.options")

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
	bastionIP, err := getBastionIPForSCP(ctx, provider, blocName)
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
	processedSource := processSCPPath(source, bastionIP, user)
	processedDest := processSCPPath(destination, bastionIP, user)

	// Build SCP command
	scpCmd := buildSCPCommand(processedSource, processedDest, keyPath, recursive, scpOptions)

	log.Infof("Copying files: %s -> %s", source, destination)
	log.Debugf("Using SSH key: %s", keyPath)
	log.Debugf("Bastion IP: %s", bastionIP)

	// Execute SCP command
    return executeSCP(ctx, scpCmd)
}

// getBastionIPForSCP retrieves the bastion host's public IP address (reusing logic).
func getBastionIPForSCP(ctx context.Context, provider cpi.Provider, blocName string) (string, error) {
	// Delegate to shared helper for robust lookup
	return findBastionIP(ctx, provider, blocName)
}

// findSSHKeyForSCP locates the SSH private key for SCP.
// findSSHKeyForSCP and verifySSHKeyForSCP are removed in favor of shared helpers

// processSCPPath converts bastion: references to proper SCP format.
func processSCPPath(path, bastionIP, user string) string {
	if strings.HasPrefix(path, "bastion:") {
		// Replace bastion: with user@bastionIP:
		remotePath := strings.TrimPrefix(path, "bastion:")

		return fmt.Sprintf("%s@%s:%s", user, bastionIP, remotePath)
	}

	return path
}

// buildSCPCommand constructs the SCP command with all options.
func buildSCPCommand(source, destination, keyPath string, recursive bool, extraOptions string) []string {
	cmd := []string{"scp"}

	// Standard options
	cmd = append(cmd, "-o", "UserKnownHostsFile=/dev/null")
	cmd = append(cmd, "-o", "StrictHostKeyChecking=no")
	cmd = append(cmd, "-o", "LogLevel=ERROR")

	// Add key if specified
	if keyPath != "" {
		cmd = append(cmd, "-i", keyPath)
	}

	// Add recursive flag if needed
	if recursive {
		cmd = append(cmd, "-r")
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

// executeSCP executes the SCP command.
func executeSCP(ctx context.Context, scpCmd []string) error {
	log := logger.WithOperation("executeSCP")
	log.Debugf("Executing: %s", strings.Join(scpCmd, " "))

	// Validate that the command is scp
	if len(scpCmd) == 0 || scpCmd[0] != "scp" {
		return errors.New("invalid SCP command")
	}

    cmd := exec.CommandContext(ctx, scpCmd[0], scpCmd[1:]...) // #nosec G204 - command is validated above
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
