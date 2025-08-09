package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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
  ocfp ssh --bloc-name production

  # Connect as specific user
  ocfp ssh --bloc-name production --user admin

  # Use specific SSH key
  ocfp ssh --bloc-name production --key /path/to/key.pem

  # Pass additional SSH options
  ocfp ssh --bloc-name production --ssh-options "-o StrictHostKeyChecking=no"`,
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
	viper.BindPFlag("ssh.user", cmd.Flags().Lookup("user"))
	viper.BindPFlag("ssh.key", cmd.Flags().Lookup("key"))
	viper.BindPFlag("ssh.options", cmd.Flags().Lookup("ssh-options"))

	return cmd
}

func runSSH(cmd *cobra.Command, args []string) error {
	// Get configuration values
	blocName := viper.GetString("bloc_name")
	iaas := viper.GetString("iaas")
	user := viper.GetString("ssh.user")
	key := viper.GetString("ssh.key")
	sshOptions := viper.GetString("ssh.options")

	// Determine target
	target := "bastion"
	if len(args) > 0 {
		target = args[0]
	}

	// Validate required configuration
	if blocName == "" {
		return fmt.Errorf("bloc-name is required")
	}
	if iaas == "" {
		return fmt.Errorf("iaas provider is required")
	}

	// TODO: Load configuration
	// TODO: Initialize provider
	// TODO: Discover bastion IP
	// TODO: Find SSH key
	// TODO: Execute SSH connection

	// Placeholder output
	fmt.Printf("SSH connection details:\n")
	fmt.Printf("  Bloc: %s\n", blocName)
	fmt.Printf("  Provider: %s\n", iaas)
	fmt.Printf("  Target: %s\n", target)
	fmt.Printf("  User: %s\n", user)
	if key != "" {
		fmt.Printf("  Key: %s\n", key)
	}
	if sshOptions != "" {
		fmt.Printf("  SSH Options: %s\n", sshOptions)
	}

	fmt.Println("\n[This is a placeholder - SSH implementation pending]")

	return nil
}