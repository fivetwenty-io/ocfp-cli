package commands

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadEndpointsConfig_EmptyBlocReturnsErrBlocIsRequired verifies
// loadEndpointsConfig uses ErrBlocIsRequired — not the near-identical
// ErrBlocRequired — when no --bloc value is set, matching every other
// bloc-scoped listing command's config-loading convention.
func TestLoadEndpointsConfig_EmptyBlocReturnsErrBlocIsRequired(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.Set("bloc", "")

	_, err := loadEndpointsConfig()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBlocIsRequired)
}

// TestNewEndpointsCmd_AliasesAndFlags verifies the flat command's Use,
// Aliases (D-03's exact requirement), and both flags with their defaults.
func TestNewEndpointsCmd_AliasesAndFlags(t *testing.T) {
	cmd := NewEndpointsCmd()

	assert.Equal(t, "endpoints", cmd.Use)
	assert.Equal(t, []string{"dns", "domains"}, cmd.Aliases)
	assert.Empty(t, cmd.Commands(), "endpoints is a flat command with no child subcommands")

	outputFlag := cmd.Flags().Lookup("output")
	require.NotNil(t, outputFlag, "expected an --output flag")
	assert.Equal(t, "table", outputFlag.DefValue)

	noResolveFlag := cmd.Flags().Lookup("no-resolve")
	require.NotNil(t, noResolveFlag, "expected a --no-resolve flag")
	assert.Equal(t, "false", noResolveFlag.DefValue)
}
