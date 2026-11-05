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
// sync has to run the shell environment script itself, with the bloc name
// exported the way the phases export it.
func TestSyncShellPathOrder_RunsShellEnvironmentOnProvisionedBastion(t *testing.T) {
	t.Parallel()

	mock := &dropInMockSSHClient{}
	m := dropInManager("pve", mock, false)

	m.syncShellPathOrder(context.Background())
	require.Len(t, mock.commands, 1, "one script for the shell environment: %v", mock.commands)

	script := mock.commands[0]
	assert.Contains(t, script, "ocfp_converge_rc_files")
	assert.Contains(t, script, "OCFP_BLOC")
	assert.True(t, strings.HasPrefix(script, "bash -c "), script)
}

// The PATH order is a convenience, so a bastion that refuses it still gets
// through init, and the failure only warns.
func TestSyncShellPathOrder_FailureOnlyWarns(t *testing.T) {
	t.Parallel()

	mock := &dropInMockSSHClient{installErr: errors.New("exit status 1")}
	m := dropInManager("pve", mock, false)

	m.syncShellPathOrder(context.Background())
	assert.NotEmpty(t, mock.commands, "the script should still have been attempted")
}

func TestSyncShellPathOrder_DryRunTouchesNothing(t *testing.T) {
	t.Parallel()

	mock := &dropInMockSSHClient{}
	m := dropInManager("pve", mock, true)

	m.syncShellPathOrder(context.Background())
	assert.Empty(t, mock.commands)
}
