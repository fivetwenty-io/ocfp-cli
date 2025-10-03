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

const (
	// File permissions.
	SSHKeyFileMode = 0600
)

var (
	sshValidHostPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-.])*[a-zA-Z0-9]$`)
	sshValidUserPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-_])*[a-zA-Z0-9]$`)
	sshValidPathPattern = regexp.MustCompile(`^[a-zA-Z0-9/._-]+$`)
)

// NewSSHCmd creates the SSH command.
func NewSSHCmd() *cobra.Command {
	var (
		user       string
		key        string
		sshOptions string
	)

	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "ssh [target]",
		Short: "Connect to bastion host or other servers",
		Long: `SSH connects to the bastion host or other servers in the OCFP environment.

The command automatically:
- Locates the bastion host's public IP address
- Finds the SSH key in standard locations
- Uses the correct private key to establish connection

SSH keys are searched in the following order:
1. ~/.ocfp/{bloc}/ssh/id_ed25519 (preferred)
2. ~/.ocfp/{bloc}/ssh/id_rsa (fallback)`,
		Example: `  # Connect to bastion host
  ocfp ssh --bloc production

  # Connect as specific user
  ocfp ssh --bloc production --user admin

  # Use specific SSH key
  ocfp ssh --bloc production --key /path/to/key.pem

  # Pass additional SSH options
  ocfp ssh --bloc production --ssh-options "-o StrictHostKeyChecking=no"`,
		Args: cobra.MaximumNArgs(1),
		RunE: runSSH,
	}

	cmd.SilenceUsage = true

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

	sshConfig, err := getSSHConfig(args)
	if err != nil {
		return err
	}

	cfg, provider, err := setupSSHProvider(ctx, sshConfig)
	if err != nil {
		return err
	}

	bastionIP, err := resolveBastionIP(ctx, provider, sshConfig, cfg.Name)
	if err != nil {
		return err
	}

	keyPath, err := resolveSSHKeyForSSH(sshConfig, cfg)
	if err != nil {
		return err
	}

	err = verifySSHKey(keyPath)
	if err != nil {
		return fmt.Errorf("SSH key verification failed: %w", err)
	}

	sshCmd := buildSSHCommand(bastionIP, sshConfig.User, keyPath, sshConfig.Options)

	log.Infof("Connecting to %s at %s as %s", sshConfig.Target, bastionIP, sshConfig.User)
	log.Debugf("Using SSH key: %s", keyPath)

	return executeSSH(ctx, sshCmd)
}

type sshConfig struct {
	BlocName string
	User     string
	KeyPath  string
	Options  string
	Target   string
}

func getSSHConfig(args []string) (*sshConfig, error) {
	blocName := viper.GetString("bloc")
	if blocName == "" {
		return nil, ErrBlocIsRequired
	}

	target := "bastion"
	if len(args) > 0 {
		target = args[0]
	}

	return &sshConfig{
		BlocName: blocName,
		User:     viper.GetString("ssh.user"),
		KeyPath:  viper.GetString("ssh.key"),
		Options:  viper.GetString("ssh.options"),
		Target:   target,
	}, nil
}

//nolint:ireturn // Returns interface by design for provider abstraction
func setupSSHProvider(ctx context.Context, sshCfg *sshConfig) (*config.Config, cpi.Provider, error) {
	cfg, err := config.LoadWithParams(viper.GetString("config.file"), sshCfg.BlocName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load configuration: %w", err)
	}

	if cfg.Provider == "" && cfg.IaaS == "" {
		return nil, nil, ErrProviderMustBeSpecifiedInBlocConfig(sshCfg.BlocName)
	}

	provider, err := cpi.GetProvider(cfg.Provider)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get provider %s: %w", cfg.Provider, err)
	}

	err = provider.Initialize(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize provider %s: %w", cfg.Provider, err)
	}

	return cfg, provider, nil
}

func resolveBastionIP(ctx context.Context, provider cpi.Provider, sshCfg *sshConfig, blocName string) (string, error) {
	if sshCfg.Target == "bastion" {
		return findBastionIP(ctx, provider, blocName)
	}

	return sshCfg.Target, nil
}

func resolveSSHKeyForSSH(sshCfg *sshConfig, cfg *config.Config) (string, error) {
	if sshCfg.KeyPath != "" {
		return sshCfg.KeyPath, nil
	}

	keyPath, err := findSSHKey(sshCfg.BlocName, cfg)
	if err != nil {
		return "", fmt.Errorf("failed to find SSH key: %w", err)
	}

	return keyPath, nil
}

// getBastionIP retrieves the bastion host's public IP address.
func getBastionIP(ctx context.Context, provider cpi.Provider, blocName string) (string, error) {
	// Deprecated: kept for compatibility in tests; delegate to shared helper
	return findBastionIP(ctx, provider, blocName)
}

// findSSHKey locates the SSH private key for the bastion.
//
//nolint:unparam // cfg reserved for future use in provider-specific key resolution
func findSSHKey(blocName string, cfg *config.Config) (string, error) {
	log := logger.WithOperation("findSSHKey")

	// Try Ed25519 key first (preferred)
	keyPath := filepath.Join(os.Getenv("HOME"), ".ocfp", blocName, "ssh", "id_ed25519")

	info, err := os.Stat(keyPath)
	if err == nil && info.Size() > 0 {
		log.Debugf("Found SSH key at: %s", keyPath)

		return keyPath, nil
	}

	// Fall back to RSA key
	rsaKeyPath := filepath.Join(os.Getenv("HOME"), ".ocfp", blocName, "ssh", "id_rsa")

	rsaInfo, rsaErr := os.Stat(rsaKeyPath)
	if rsaErr == nil && rsaInfo.Size() > 0 {
		log.Debugf("Found SSH key at: %s", rsaKeyPath)

		return rsaKeyPath, nil
	}

	return "", fmt.Errorf("SSH key not found at %s or %s: %w", keyPath, rsaKeyPath, err)
}

// verifySSHKey checks if the SSH key exists and has correct permissions.
func verifySSHKey(keyPath string) error {
	info, err := os.Stat(keyPath)
	if err != nil {
		return ErrSSHKeyNotFound(keyPath)
	}

	// Check permissions (should be 600 or 400)
	mode := info.Mode()
	if mode.Perm()&0077 != 0 {
		// Try to fix permissions
		err := os.Chmod(keyPath, SSHKeyFileMode)
		if err != nil {
			return ErrSSHKeyIncorrectPermissions(keyPath)
		}

		logger.WithOperation("verifySSHKey").Warnf("Fixed SSH key permissions for: %s", keyPath)
	}

	return nil
}

// buildSSHCommand constructs the SSH command with all options.
func buildSSHCommand(host, user, keyPath, extraOptions string) []string {
	cmd := []string{"ssh"}

	// Validate inputs
	err := security.ValidateInput(host, sshValidHostPattern)
	if err != nil {
		logger.WithOperation("buildSSHCommand").Errorf("invalid host: %v", err)

		return []string{"ssh", "--help"} // Return safe command
	}

	err = security.ValidateInput(user, sshValidUserPattern)
	if err != nil {
		logger.WithOperation("buildSSHCommand").Errorf("invalid user: %v", err)

		return []string{"ssh", "--help"} // Return safe command
	}

	if keyPath != "" {
		err = security.ValidateInput(keyPath, sshValidPathPattern)
		if err != nil {
			logger.WithOperation("buildSSHCommand").Errorf("invalid key path: %v", err)

			return []string{"ssh", "--help"} // Return safe command
		}
	}

	// Standard options
	cmd = append(cmd, "-o", "UserKnownHostsFile=/dev/null")
	cmd = append(cmd, "-o", "StrictHostKeyChecking=no")
	cmd = append(cmd, "-o", "LogLevel=ERROR")
	cmd = append(cmd, "-o", "IdentitiesOnly=yes")

	// Enable SSH agent forwarding if SSH_AUTH_SOCK is set
	if os.Getenv("SSH_AUTH_SOCK") != "" {
		cmd = append(cmd, "-A") // ForwardAgent yes
	}

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

// executeSSH executes the SSH command and attaches to stdin/stdout/stderr.
func executeSSH(ctx context.Context, sshCmd []string) error {
	log := logger.WithOperation("executeSSH")
	log.Debugf("Executing: %s", strings.Join(sshCmd, " "))

	// Validate that the command is ssh
	if len(sshCmd) == 0 || sshCmd[0] != "ssh" {
		return ErrInvalidSSHCommand
	}

	cmd := exec.CommandContext(ctx, sshCmd[0], sshCmd[1:]...) // #nosec G204 - command is validated above
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("ssh command failed: %w", err)
	}

	return nil
}
