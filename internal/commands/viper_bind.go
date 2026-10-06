package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// bindFlagsOnRun binds each viper key to the named flag of cmd when cmd
// runs, not when the command tree is built. Viper keeps one binding per
// key, so a key that several commands bind to flags of their own (bloc,
// dry-run, ssh.user, ssh.key) belongs to whichever command registered
// last, and every other command then reads that command's unset flag.
// Binding in PreRunE hands the key to the command that is running. Any
// PreRunE already set on cmd runs after the bindings.
func bindFlagsOnRun(cmd *cobra.Command, keys map[string]string) {
	next := cmd.PreRunE

	cmd.PreRunE = func(c *cobra.Command, args []string) error {
		for key, flag := range keys {
			err := viper.BindPFlag(key, c.Flags().Lookup(flag))
			if err != nil {
				return fmt.Errorf("failed to bind --%s to %s: %w", flag, key, err)
			}
		}

		if next != nil {
			return next(c, args)
		}

		return nil
	}
}
