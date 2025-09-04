package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/security"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	sshValidHostPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-.])*[a-zA-Z0-9]$`)
	sshValidUserPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-_])*[a-zA-Z0-9]$`)
	sshValidPathPattern = regexp.MustCompile(`^[a-zA-Z0-9/._-]+$`)
)

// NewSSHCmd creates the SSH command
func NewSSHCmd() *cobra.Command {
	var (
		user       string
		key        string
		sshOptions string
	)

	cmd := &cobra.Command{
		Use:   "ssh [target]",
		Short: "Connect to bastion host or other servers",
		Long: `SSH connects to the bastion host or other servers in the OCFP environment.

The command automatically:
- Locates the bastion host's public IP address
- Finds the SSH key in standard locations
- Uses the correct private key to establish connection

SSH keys are searched in the following order:
1. ~/.ocfp/keys/{bloc_name}-bastion/id_rsa
2. ~/.ssh/{environment-name}-{bastion_keypair}
3. Configured ssh_key_storage_dir`,
		Example: `  # Connect to bastion host
  ocfp ssh --bloc production

  # Connect as specific user
  ocfp ssh --bloc production --user admin

  # Use specific SSH key
  ocfp ssh --bloc production --key /path/to/key.pem

  # Pass additional SSH options
  ocfp ssh --bloc production --ssh-options "-o StrictHostKeyChecking=no"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSSH(cmd, args)
		},
	}

	// Command-specific flags
	cmd.Flags().StringVar(&user, "user", "ubuntu", "username for SSH login")
	cmd.Flags().StringVar(&key, "key", "", "path to SSH private key")
	cmd.Flags().StringVar(&sshOptions, "ssh-options", "", "additional SSH options")

	// Bind flags to viper
	_ = viper.BindPFlag("ssh.user", cmd.Flags().Lookup("user"))
	_ = viper.BindPFlag("ssh.key", cmd.Flags().Lookup("key"))
	_ = viper.BindPFlag("ssh.options", cmd.Flags().Lookup("ssh-options"))

	return cmd
}

func runSSH(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	log := logger.WithOperation("ssh")

	// Get configuration values
	blocName := viper.GetString("bloc_name")
	user := viper.GetString("ssh.user")
	keyPath := viper.GetString("ssh.key")
	sshOptions := viper.GetString("ssh.options")

	// Determine target
	target := "bastion"
	if len(args) > 0 {
		target = args[0]
	}

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
	var bastionIP string
	if target == "bastion" {
		bastionIP, err = getBastionIP(ctx, provider, blocName)
		if err != nil {
			return fmt.Errorf("failed to get bastion IP: %w", err)
		}
	} else {
		// For non-bastion targets, we'd need to look up the target IP
		// This could be another instance or a service
		bastionIP = target
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

	// Build SSH command
	sshCmd := buildSSHCommand(bastionIP, user, keyPath, sshOptions)

	log.Infof("Connecting to %s at %s as %s", target, bastionIP, user)
	log.Debugf("Using SSH key: %s", keyPath)

	// Execute SSH command
	return executeSSH(sshCmd)
}

// getBastionIP retrieves the bastion host's public IP address
func getBastionIP(ctx context.Context, provider cpi.Provider, blocName string) (string, error) {
	log := logger.WithOperation("getBastionIP")

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

// findSSHKey locates the SSH private key for the bastion
func findSSHKey(blocName string, cfg *config.Config) (string, error) {
	log := logger.WithOperation("findSSHKey")

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

// verifySSHKey checks if the SSH key exists and has correct permissions
func verifySSHKey(keyPath string) error {
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
		logger.WithOperation("verifySSHKey").Warnf("Fixed SSH key permissions for: %s", keyPath)
	}

	return nil
}

// buildSSHCommand constructs the SSH command with all options
func buildSSHCommand(host, user, keyPath, extraOptions string) []string {
	cmd := []string{"ssh"}

	// Validate inputs
	if err := security.ValidateInput(host, sshValidHostPattern); err != nil {
		logger.WithOperation("buildSSHCommand").Errorf("invalid host: %v", err)
		return []string{"ssh", "--help"} // Return safe command
	}
	if err := security.ValidateInput(user, sshValidUserPattern); err != nil {
		logger.WithOperation("buildSSHCommand").Errorf("invalid user: %v", err)
		return []string{"ssh", "--help"} // Return safe command
	}
	if keyPath != "" {
		if err := security.ValidateInput(keyPath, sshValidPathPattern); err != nil {
			logger.WithOperation("buildSSHCommand").Errorf("invalid key path: %v", err)
			return []string{"ssh", "--help"} // Return safe command
		}
	}

	// Standard options
	cmd = append(cmd, "-o", "UserKnownHostsFile=/dev/null")
	cmd = append(cmd, "-o", "StrictHostKeyChecking=no")
	cmd = append(cmd, "-o", "LogLevel=ERROR")

	// Add key if specified
	if keyPath != "" {
		cmd = append(cmd, "-i", keyPath)
	}

	// Add extra options if provided - sanitize by only allowing safe SSH options
	if extraOptions != "" {
		// Only allow specific safe SSH options
		allowedOptions := map[string]bool{
			"-p": true, "-v": true, "-q": true, "-4": true, "-6": true,
			"-o": true, "-L": true, "-R": true, "-D": true,
		}
		options := strings.Fields(extraOptions)
		for _, opt := range options {
			if strings.HasPrefix(opt, "-") {
				if !allowedOptions[opt] {
					logger.WithOperation("buildSSHCommand").Warnf("skipping unsafe SSH option: %s", opt)
					continue
				}
			}
			cmd = append(cmd, opt)
		}
	}

	// Add user@host
	cmd = append(cmd, fmt.Sprintf("%s@%s", user, host))

	return cmd
}

// executeSSH executes the SSH command and attaches to stdin/stdout/stderr
func executeSSH(sshCmd []string) error {
	log := logger.WithOperation("executeSSH")
	log.Debugf("Executing: %s", strings.Join(sshCmd, " "))

	// Validate that the command is ssh
	if len(sshCmd) == 0 || sshCmd[0] != "ssh" {
		return fmt.Errorf("invalid SSH command")
	}

	cmd := exec.Command(sshCmd[0], sshCmd[1:]...) // #nosec G204 - command is validated above
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
