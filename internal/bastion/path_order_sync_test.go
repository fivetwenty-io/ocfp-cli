package bastion

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An already-provisioned bastion never reaches the full phase list, so the
// sync has to run the shell and system environment scripts itself, with the
// bloc name exported the way the phases export it.
func TestSyncShellPathOrder_RunsBothEnvironmentScriptsOnProvisionedBastion(t *testing.T) {
	t.Parallel()

	mock := &dropInMockSSHClient{}
	m := dropInManager("pve", mock, false)

	m.syncShellPathOrder(context.Background())
	require.Len(t, mock.commands, 2, "one script each for the shell and system environment: %v", mock.commands)

	for _, script := range mock.commands {
		assert.True(t, strings.HasPrefix(script, "bash -c "), script)
		// The script goes through bash -c with its quotes escaped.
		assert.Regexp(t, `export OCFP_BLOC=.{1,5}ocfp-lab-drgao`, script)
	}

	assert.Contains(t, mock.commands[0], "ocfp_converge_rc_files")
	assert.Contains(t, mock.commands[1], "/etc/environment")
	assert.Contains(t, mock.commands[1], "/etc/profile.d/ocfp.sh")
}

// The PATH order is a convenience, so a bastion that refuses it still gets
// through init, and the failure only warns.
func TestSyncShellPathOrder_FailureOnlyWarns(t *testing.T) {
	t.Parallel()

	mock := &dropInMockSSHClient{installErr: errors.New("exit status 1")}
	m := dropInManager("pve", mock, false)

	m.syncShellPathOrder(context.Background())
	assert.Len(t, mock.commands, 2, "a failed shell script must not stop the system script")
}

func TestSyncShellPathOrder_DryRunTouchesNothing(t *testing.T) {
	t.Parallel()

	mock := &dropInMockSSHClient{}
	m := dropInManager("pve", mock, true)

	m.syncShellPathOrder(context.Background())
	assert.Empty(t, mock.commands)
}
