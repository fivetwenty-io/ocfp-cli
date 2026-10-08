package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --vault-boot-unit installs the inception vault's boot unit and nothing
// else, so init bastion must offer it as a flag.
func TestInitCmd_HasVaultBootUnitFlag(t *testing.T) {
	t.Parallel()

	cmd := NewInitCmd()

	flag := cmd.Flags().Lookup("vault-boot-unit")
	require.NotNil(t, flag, "init must offer --vault-boot-unit")
	assert.Equal(t, "false", flag.DefValue)
	assert.Contains(t, cmd.Long, "--vault-boot-unit", "the command help must describe the mode")
}

// Each narrow mode runs one piece of init, so asking for two of them at
// once is refused rather than settled by whichever the code checks first.
func TestValidateModeFlags_VaultBootUnitIsExclusive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		flags initFlags
		ok    bool
	}{
		{"alone", initFlags{vaultBootUnit: true}, true},
		{"with --genesis", initFlags{vaultBootUnit: true, genesisOnly: true}, false},
		{"with --ocfp", initFlags{vaultBootUnit: true, ocfpOnly: true}, false},
		{"with --config", initFlags{vaultBootUnit: true, configOnly: true}, false},
		{"with --force", initFlags{vaultBootUnit: true, force: true}, true},
		{"with --dry-run", initFlags{vaultBootUnit: true, dryRun: true}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.flags.validateModeFlags()
			if tc.ok {
				assert.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrMutuallyExclusiveFlags)
			assert.Contains(t, err.Error(), "--vault-boot-unit")
		})
	}
}
