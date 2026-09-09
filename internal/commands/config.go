package commands

import (
	"github.com/spf13/cobra"
)

// NewConfigCmd creates the 'config' command group, which manages the CLI's
// own configuration and on-disk layout rather than any bloc. Its
// subcommands are deliberately excluded from command tracking (see
// internal/cli's setupCommandTracking): they move the very directories
// the tracker writes its locks into.
func NewConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "config",
		Short:        "Manage the ocfp CLI's own configuration and directory layout",
		SilenceUsage: true,
		Long: `Config manages the ocfp CLI's own configuration files and directory
layout, as opposed to the blocs those files describe.

Subcommands:
  migrate    Move a legacy ~/.ocfp layout into the XDG config/state/data
             directories`,
		Example: `  # Preview the legacy layout migration
  ocfp config migrate --dry-run

  # Migrate the legacy ~/.ocfp layout to XDG directories
  ocfp config migrate`,
	}

	cmd.AddCommand(newConfigMigrateCmd())

	return cmd
}
