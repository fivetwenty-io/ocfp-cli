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

// NewSCPCmd creates the SCP command
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
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSCP(cmd, args)
		},
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
	bastionIP, err := getBastionIPForSCP(ctx, provider, blocName)
	if err != nil {
		return fmt.Errorf("failed to get bastion IP: %w", err)
	}

	// Find SSH key if not specified
	if keyPath == "" {
		keyPath, err = findSSHKeyForSCP(blocName, cfg)
		if err != nil {
			return fmt.Errorf("failed to find SSH key: %w", err)
		}
	}

	// Verify key exists and has correct permissions
	if err := verifySSHKeyForSCP(keyPath); err != nil {
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
	return executeSCP(scpCmd)
}

// getBastionIPForSCP retrieves the bastion host's public IP address (reusing logic)
func getBastionIPForSCP(ctx context.Context, provider cpi.Provider, blocName string) (string, error) {
	log := logger.WithOperation("getBastionIPForSCP")

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

		if len(floatingIPs) == 0 {
			return "", fmt.Errorf("bastion %s has no public IP address", bastion.Name)
		}

		bastion.FloatingIP = floatingIPs[0].Address
	}

	log.Debugf("Found bastion IP: %s", bastion.FloatingIP)
	return bastion.FloatingIP, nil
}

// findSSHKeyForSCP locates the SSH private key for SCP
func findSSHKeyForSCP(blocName string, cfg *config.Config) (string, error) {
	log := logger.WithOperation("findSSHKeyForSCP")

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

// verifySSHKeyForSCP checks if the SSH key exists and has correct permissions
func verifySSHKeyForSCP(keyPath string) error {
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
		logger.WithOperation("verifySSHKeyForSCP").Warnf("Fixed SSH key permissions for: %s", keyPath)
	}

	return nil
}

// processSCPPath converts bastion: references to proper SCP format
func processSCPPath(path, bastionIP, user string) string {
	if strings.HasPrefix(path, "bastion:") {
		// Replace bastion: with user@bastionIP:
		remotePath := strings.TrimPrefix(path, "bastion:")
		return fmt.Sprintf("%s@%s:%s", user, bastionIP, remotePath)
	}
	return path
}

// buildSCPCommand constructs the SCP command with all options
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

// executeSCP executes the SCP command
func executeSCP(scpCmd []string) error {
	log := logger.WithOperation("executeSCP")
	log.Debugf("Executing: %s", strings.Join(scpCmd, " "))

	cmd := exec.Command(scpCmd[0], scpCmd[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
