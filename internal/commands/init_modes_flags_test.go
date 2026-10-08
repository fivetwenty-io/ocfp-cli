package commands

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runInitArgs runs "ocfp init" with the given arguments and returns the
// error. A refusal must come before the config load, the prerequisite
// check, and the prompt, so none of them can be what these tests see.
func runInitArgs(t *testing.T, args ...string) error {
	t.Helper()

	cmd := NewInitCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)

	return cmd.Execute()
}

// A mode flag asks for one piece of bastion init, so every other component
// refuses it and names the command that does what was asked.
func TestRunInit_ModeFlagsAreRefusedOutsideBastion(t *testing.T) {
	flags := []string{"--genesis", "--ocfp", "--config", "--vault-boot-unit"}
	components := []string{"all", "aws", "pve", "pg", "cf", "bosh"}

	for _, component := range components {
		for _, flag := range flags {
			t.Run(component+" "+flag, func(t *testing.T) {
				err := runInitArgs(t, "--skip-checks", "--force", flag, component)

				require.ErrorIs(t, err, ErrModeFlagOutsideBastion)
				assert.Contains(t, err.Error(), "ocfp init bastion "+flag)
			})
		}
	}
}

// Two modes at once are refused before init loads the config or asks
// anything, not after the prompt has been answered.
func TestRunInit_ModeFlagsExclusiveBeforeTheConfigLoads(t *testing.T) {
	err := runInitArgs(t, "--genesis", "--ocfp", "bastion")

	require.ErrorIs(t, err, ErrMutuallyExclusiveFlags)
}
