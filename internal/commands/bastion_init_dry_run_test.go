package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// "ocfp bastion init" settles its dry run by the rule "ocfp init bastion"
// uses, so a config file that asks for dry runs never gets a real one from
// either entry point. A --dry-run on the command line wins either way. The
// config file's top-level dry-run key, which the command has always read,
// can only ask for a dry run, never turn off the one init.dry_run asks for.
func TestBastionInitTakesTheDryRunFromTheConfigFile(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		args []string
		want bool
	}{
		{"config asks for a dry run", "init:\n  dry_run: true\n", nil, true},
		{"flag turns the config's dry run off", "init:\n  dry_run: true\n", []string{"--dry-run=false"}, false},
		{"flag asks for a dry run the config doesn't", "init:\n  dry_run: false\n", []string{"--dry-run"}, true},
		{"own key can't turn init's dry run off", "dry-run: false\ninit:\n  dry_run: true\n", nil, true},
		{"own key asks for a dry run", "dry-run: true\n", nil, true},
		{"flag turns the own key's dry run off", "dry-run: true\n", []string{"--dry-run=false"}, false},
		{"neither asks", "", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, calls := bastionInitUnderRoot(t, tc.yaml)
			root.SetArgs(append([]string{"bastion", "init", "--bloc", "lab-dry", "--config", blocConfigFile(t)}, tc.args...))

			require.NoError(t, root.Execute())
			assert.Equal(t, []bool{tc.want}, *calls)
		})
	}
}

// bastionInitUnderRoot puts the real bastion command under a root that owns
// the global --config flag, with yaml as the CLI config viper has read, and
// records the dry-run setting each bastion init gets instead of running it.
func bastionInitUnderRoot(t *testing.T, yaml string) (*cobra.Command, *[]bool) {
	t.Helper()

	// The command must reach no real home directory, even if it ran.
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "OCFP_HOME"} {
		t.Setenv(key, t.TempDir())
	}

	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.SetConfigType("yaml")
	require.NoError(t, viper.ReadConfig(strings.NewReader(yaml)))

	var calls []bool

	saved := runBastionInitFn
	runBastionInitFn = func(_ context.Context, _ *config.Config, mode bastionInitMode, params bastionRunParams) error {
		assert.Equal(t, bastionModeFull, mode)

		calls = append(calls, params.dryRun)

		return nil
	}

	t.Cleanup(func() { runBastionInitFn = saved })

	root := &cobra.Command{Use: "ocfp", SilenceUsage: true, SilenceErrors: true} //nolint:exhaustruct // Only the flags matter here.
	root.PersistentFlags().String("config", "", "config file path")
	require.NoError(t, viper.BindPFlag("config", root.PersistentFlags().Lookup("config")))
	root.AddCommand(NewBastionCmd())
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	return root, &calls
}

// blocConfigFile writes a config file that defines the bloc lab-dry.
func blocConfigFile(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "ocfp.yml")
	require.NoError(t, os.WriteFile(path, []byte("blocs:\n  lab-dry:\n    provider: aws\n    bastion_ip: 10.0.0.9\n"), 0o600))

	return path
}

// The help says that "ocfp bastion init" reads init.dry_run, that the flag
// wins over it, and what the top-level dry-run key can do.
func TestBastionHelp_SaysItReadsInitDryRun(t *testing.T) { //nolint:paralleltest // NewBastionCmd binds flags into the global viper
	long := NewBastionCmd().Long
	assert.Contains(t, long, "init.dry_run")
	assert.Contains(t, long, "--dry-run=false wins over that")
	assert.Contains(t, long, "dry-run key can only ask for a dry run")
}
