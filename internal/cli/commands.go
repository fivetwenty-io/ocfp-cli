package cli

import (
	"github.com/ocfp/ocfp-cli-go/internal/commands"
	"github.com/spf13/cobra"
)

// RegisterCommands adds all subcommands to the root command
func RegisterCommands(root *cobra.Command) {
	// Core provisioning commands
	root.AddCommand(commands.NewBootstrapCmd())
	root.AddCommand(commands.NewConfigureCmd())
	root.AddCommand(commands.NewTeardownCmd())

	// Environment and access commands
	root.AddCommand(commands.NewSSHCmd())
	root.AddCommand(commands.NewSCPCmd())
	root.AddCommand(commands.NewRSyncCmd())
	root.AddCommand(commands.NewEnvCmd())

	// Operational commands
	root.AddCommand(commands.NewInitCmd())
	root.AddCommand(commands.NewTestCmd())
	root.AddCommand(commands.NewVaultCmd())
	root.AddCommand(commands.NewLBCmd())
	root.AddCommand(commands.NewScaleCmd())
	root.AddCommand(commands.NewBackupCmd())
	root.AddCommand(commands.NewRestoreCmd())

	// Networking / Public IPs
	root.AddCommand(commands.NewPublicIPsCmd())

	// Provider and utility commands
	root.AddCommand(commands.NewProviderCmd())
	root.AddCommand(commands.NewTmuxCmd())
	root.AddCommand(commands.NewBastionCmd())
}

// Placeholder implementations for commands not yet created
// These will be replaced with actual implementations

func init() {
	// Register all commands with the root command
	RegisterCommands(rootCmd)
}
