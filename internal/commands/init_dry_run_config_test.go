package commands

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initCmdWithConfig builds an init command bound to viper, loads yaml as
// the config file's contents, and parses args as the command line.
func initCmdWithConfig(t *testing.T, yaml string, args ...string) (*initFlags, *cobra.Command) {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.SetConfigType("yaml")
	require.NoError(t, viper.ReadConfig(strings.NewReader(yaml)))

	f := &initFlags{}
	cmd := &cobra.Command{Use: "init"} //nolint:exhaustruct // Only the flags matter here.
	f.addFlags(cmd)
	f.bindViperFlags(cmd)
	require.NoError(t, cmd.ParseFlags(args))

	return f, cmd
}

// A config file that sets init.dry_run asks for a dry run, so init must
// honour it. Ignoring it would make a real run of a run meant to change
// nothing.
func TestInitDryRunComesFromTheConfigFile(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		args []string
		want bool
	}{
		{"config asks for a dry run", "init:\n  dry_run: true\n", nil, true},
		{"flag turns the config's dry run off", "init:\n  dry_run: true\n", []string{"--dry-run=false"}, false},
		{"flag asks for a dry run the config doesn't", "init:\n  dry_run: false\n", []string{"--dry-run"}, true},
		{"neither asks", "", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, cmd := initCmdWithConfig(t, tc.yaml, tc.args...)
			assert.Equal(t, tc.want, f.resolveDryRun(cmd))
		})
	}
}

// runInit settles the dry run before anything reads it, so every step of
// the run sees the config file's setting.
func TestRunInitTakesTheDryRunFromTheConfigFile(t *testing.T) {
	f, cmd := initCmdWithConfig(t, "init:\n  dry_run: true\n", "--skip-checks", "--yes")
	viper.Set("config", "/nonexistent/ocfp-config.yml")

	_ = f.runInit(cmd, []string{"pg"})

	assert.True(t, f.dryRun, "the config file's init.dry_run must reach the run")
}
