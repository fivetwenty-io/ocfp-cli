package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectBastionMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                         string
		genesis, ocfp, cfg, bootUnit bool
		want                         bastionInitMode
		wantErr                      bool
	}{
		{name: "none", want: bastionModeFull},
		{name: "genesis", genesis: true, want: bastionModeGenesis},
		{name: "ocfp", ocfp: true, want: bastionModeOCFP},
		{name: "config", cfg: true, want: bastionModeConfig},
		{name: "boot unit", bootUnit: true, want: bastionModeVaultBootUnit},
		{name: "two", genesis: true, bootUnit: true, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := selectBastionMode(tc.genesis, tc.ocfp, tc.cfg, tc.bootUnit)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrMutuallyExclusiveFlags)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestBuildBastionOptions(t *testing.T) {
	t.Parallel()

	params := bastionRunParams{force: true, parallel: true, dryRun: true, resume: true, verbose: true, reboot: true}

	full := buildBastionOptions(bastionModeFull, params)
	assert.True(t, full.Force && full.Parallel && full.DryRun && full.Resume && full.Verbose && full.RebootAfterInit)
	assert.False(t, full.GenesisOnly || full.OCFPOnly || full.ConfigOnly || full.VaultBootUnitOnly)

	assert.True(t, buildBastionOptions(bastionModeGenesis, params).GenesisOnly)
	assert.True(t, buildBastionOptions(bastionModeOCFP, params).OCFPOnly)
	assert.True(t, buildBastionOptions(bastionModeConfig, params).ConfigOnly)

	unit := buildBastionOptions(bastionModeVaultBootUnit, params)
	assert.True(t, unit.VaultBootUnitOnly)
	assert.True(t, unit.Force && unit.DryRun && unit.Verbose)
	assert.False(t, unit.Parallel || unit.Resume || unit.RebootAfterInit, "the boot unit mode ignores these")
}
