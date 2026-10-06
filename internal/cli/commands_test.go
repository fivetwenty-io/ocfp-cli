package cli

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// TestRegisterCommands_SharedViperKeysFollowTheInvokedCommand covers the
// commands that bind the same viper key to a flag of their own. Viper keeps
// one binding per key, so a binding made while the tree is built belongs to
// whichever command registers last, and every other command then reads that
// command's unset flag. Each case parses the flags of the command under test
// the way cobra does before RunE, runs its PreRunE, and expects viper to
// report the value given on that command's own command line.
func TestRegisterCommands_SharedViperKeysFollowTheInvokedCommand(t *testing.T) {
	cases := []struct {
		name    string
		command string
		args    []string
		want    map[string]any
	}{
		{
			name:    "bastion dry-run",
			command: "bastion",
			args:    []string{"--dry-run"},
			want:    map[string]any{"dry-run": true},
		},
		{
			name:    "bastion ssh user and key",
			command: "bastion",
			args:    []string{"--user", "alice", "--key", "/keys/bastion"},
			want:    map[string]any{"ssh.user": "alice", "ssh.key": "/keys/bastion"},
		},
		{
			name:    "ssh user and key",
			command: "ssh",
			args:    []string{"--user", "alice", "--key", "/keys/ssh"},
			want:    map[string]any{"ssh.user": "alice", "ssh.key": "/keys/ssh"},
		},
		{
			name:    "artifacts dry-run, user, and key",
			command: "artifacts",
			args:    []string{"--dry-run", "--user", "alice", "--key", "/keys/artifacts"},
			want:    map[string]any{"dry-run": true, "ssh.user": "alice", "ssh.key": "/keys/artifacts"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			viper.Reset()
			t.Cleanup(viper.Reset)

			root := createRootCommand()
			RegisterCommands(root)

			cmd, _, err := root.Find([]string{tc.command})
			require.NoError(t, err)
			require.Equal(t, tc.command, cmd.Name())

			require.NoError(t, cmd.ParseFlags(tc.args))

			if cmd.PreRunE != nil {
				require.NoError(t, cmd.PreRunE(cmd, nil))
			}

			for key, want := range tc.want {
				require.Equal(t, want, viper.Get(key), "viper key %q", key)
			}
		})
	}
}
