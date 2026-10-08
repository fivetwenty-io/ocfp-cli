package commands

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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

func TestBastionCmd_HasTheModeFlags(t *testing.T) { //nolint:paralleltest // NewBastionCmd binds flags into the global viper
	cmd := NewBastionCmd()

	for _, name := range []string{"genesis", "ocfp", "vault-boot-unit"} {
		flag := cmd.Flags().Lookup(name)
		require.NotNil(t, flag, "bastion must offer --%s", name)
		assert.Equal(t, "false", flag.DefValue)
	}

	// --config belongs to the root command as the config file path, so
	// bastion must not declare a flag of its own with that name.
	assert.Nil(t, cmd.Flags().Lookup("config"), "bastion must not shadow the root --config")
}

// newBastionUnderRoot puts the bastion command under a root that owns the
// global --config file flag, as the real CLI does, and records the action
// instead of running it.
func newBastionUnderRoot(t *testing.T) (*cobra.Command, *[]string) {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)

	root := &cobra.Command{Use: "ocfp", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().String("config", "", "config file path")
	require.NoError(t, viper.BindPFlag("config", root.PersistentFlags().Lookup("config")))

	bastionCmd := NewBastionCmd()

	var seen []string

	bastionCmd.RunE = func(cmd *cobra.Command, args []string) error {
		seen = append(seen, args...)
		_, err := bastionModeFromFlags(cmd)

		return err
	}

	root.AddCommand(bastionCmd)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	return root, &seen
}

// The global --config <path> flag must keep working on every bastion action,
// in both the "--config path" and "--config=path" spellings, and must not
// pick a mode.
func TestBastionCmd_GlobalConfigFileFlagStillWorks(t *testing.T) {
	for _, action := range []string{"init", "provision", "recycle"} {
		for _, spelling := range []string{"space", "equals"} {
			t.Run(action+" "+spelling, func(t *testing.T) {
				root, seen := newBastionUnderRoot(t)

				args := []string{"bastion", action, "--bloc", "lab"}
				if spelling == "space" {
					args = append(args, "--config", "/tmp/alt.yml")
				} else {
					args = append(args, "--config=/tmp/alt.yml")
				}

				root.SetArgs(args)

				require.NoError(t, root.Execute())
				assert.Equal(t, []string{action}, *seen)
				assert.Equal(t, "/tmp/alt.yml", viper.GetString("config"))

				bastionCmd, _, err := root.Find([]string{"bastion"})
				require.NoError(t, err)

				mode, err := bastionModeFromFlags(bastionCmd)
				require.NoError(t, err)
				assert.Equal(t, bastionModeFull, mode)
			})
		}
	}
}

func TestBastionCmd_ModeFlagsStillSelectOneMode(t *testing.T) {
	tests := map[string]bastionInitMode{
		"--genesis":         bastionModeGenesis,
		"--ocfp":            bastionModeOCFP,
		"--vault-boot-unit": bastionModeVaultBootUnit,
	}

	for flag, want := range tests {
		t.Run(flag, func(t *testing.T) {
			root, _ := newBastionUnderRoot(t)
			root.SetArgs([]string{"bastion", "init", "--bloc", "lab", flag})
			require.NoError(t, root.Execute())

			bastionCmd, _, err := root.Find([]string{"bastion"})
			require.NoError(t, err)

			mode, err := bastionModeFromFlags(bastionCmd)
			require.NoError(t, err)
			assert.Equal(t, want, mode)
		})
	}
}

func TestBastionHelp_SaysWhereTheConfigOnlyModeIs(t *testing.T) { //nolint:paralleltest // NewBastionCmd binds flags into the global viper
	assert.Contains(t, NewBastionCmd().Long, "ocfp init bastion --config")
}

// provision and recycle have no narrow modes, so they refuse the flags
// before they touch a bastion.
func TestBastionCmd_ModeFlagsAreRefusedOutsideInit(t *testing.T) {
	for _, action := range []string{"provision", "recycle"} {
		for _, flag := range []string{"--genesis", "--ocfp", "--vault-boot-unit"} {
			t.Run(action+" "+flag, func(t *testing.T) {
				cmd := NewBastionCmd()
				cmd.SetOut(&bytes.Buffer{})
				cmd.SetErr(&bytes.Buffer{})
				cmd.SetArgs([]string{action, flag})

				err := cmd.Execute()

				require.ErrorIs(t, err, ErrModeFlagOutsideBastion)
				assert.Contains(t, err.Error(), "bastion init "+flag)
			})
		}
	}
}

func TestBastionCmd_InitRefusesTwoModes(t *testing.T) {
	cmd := NewBastionCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"init", "--genesis", "--ocfp"})

	err := cmd.Execute()

	require.ErrorIs(t, err, ErrMutuallyExclusiveFlags)
}

// The error text of a mode starts in lower case, as Go error strings do,
// while the line printed for the operator starts with a capital.
func TestBastionInitMode_TextCasing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mode        bastionInitMode
		wantFailed  string
		wantHeading string
	}{
		{bastionModeGenesis, "genesis installation", "Genesis installation"},
		{bastionModeOCFP, "OCFP CLI installation", "OCFP CLI installation"},
		{bastionModeConfig, "configuration sync", "Configuration sync"},
		{bastionModeVaultBootUnit, "vault boot unit install", "Vault boot unit install"},
		{bastionModeFull, "bastion initialization", "Bastion initialization"},
	}

	for _, tc := range tests {
		t.Run(tc.mode.String(), func(t *testing.T) {
			t.Parallel()

			text := tc.mode.text()
			assert.Equal(t, tc.wantFailed, text.failed)
			assert.Equal(t, tc.wantHeading, text.failedHeading())
			assert.NotEmpty(t, text.done)
		})
	}
}

func TestBastionInitMode_StringNamesTheMode(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "full", bastionModeFull.String())
	assert.Equal(t, "genesis", bastionModeGenesis.String())
	assert.Equal(t, "ocfp", bastionModeOCFP.String())
	assert.Equal(t, "config", bastionModeConfig.String())
	assert.Equal(t, "vault-boot-unit", bastionModeVaultBootUnit.String())
}
