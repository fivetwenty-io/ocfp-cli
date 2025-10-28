package commands

import (
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// bindFlagsToViper is a helper function to bind multiple command flags to viper configuration.
// It takes a map where keys are viper config keys and values are flag names to lookup.
// This reduces code duplication across multiple commands that need to bind flags to viper.
func bindFlagsToViper(cmd *cobra.Command, bindings map[string]string) {
	for viperKey, flagName := range bindings {
		_ = viper.BindPFlag(viperKey, cmd.Flags().Lookup(flagName))
	}
}
