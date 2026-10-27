package commands

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findSSHConfigCmd resolves "ocfp ssh config <args>" through the real ssh
// command tree and parses the flags the way Cobra does before a run, without
// running the command.
func findSSHConfigCmd(t *testing.T, args ...string) {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)

	root := NewSSHCmd()

	cmd, rest, err := root.Find(append([]string{"config"}, args...))
	require.NoError(t, err)
	require.Equal(t, "config", cmd.Name())

	require.NoError(t, cmd.ParseFlags(rest))
	require.NotNil(t, cmd.PreRunE, "ssh config binds its flags before it runs")
	require.NoError(t, cmd.PreRunE(cmd, cmd.Flags().Args()))
}

func TestSSHConfigAcceptsKeyFlag(t *testing.T) { //nolint:paralleltest // viper is global
	findSSHConfigCmd(t, "--key", "/keys/mine")

	assert.Equal(t, "/keys/mine", viper.GetString("ssh.key"),
		"runSSHConfig reads the key from ssh.key")
}

func TestSSHConfigWithoutKeyFlagLeavesKeyUnset(t *testing.T) { //nolint:paralleltest // viper is global
	findSSHConfigCmd(t)

	assert.Empty(t, viper.GetString("ssh.key"))
}
