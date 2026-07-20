package vault

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureLocalCommands swaps the runLocalCommand seam for a recorder and
// returns the recorded command lines plus a restore func.
func captureLocalCommands(t *testing.T) *[]string {
	t.Helper()

	var recorded []string

	original := runLocalCommand
	runLocalCommand = func(_ context.Context, name string, args ...string) error {
		recorded = append(recorded, name+" "+strings.Join(args, " "))

		return nil
	}

	t.Cleanup(func() { runLocalCommand = original })

	return &recorded
}

func TestTeardownLocalInception_BlocScoped(t *testing.T) {
	recorded := captureLocalCommands(t)

	err := TeardownLocalInception(context.Background(), "ocfp-lab-drgao")
	require.NoError(t, err)

	assert.Contains(t, *recorded, "tmux kill-session -t ocfp-lab-drgao-inception-vault")
	assert.Contains(t, *recorded, "safe target delete ocfp-lab-drgao-inception")

	joined := strings.Join(*recorded, "\n")
	assert.Contains(t, joined, "pkill", "must stop local safe processes")
	assert.Contains(t, joined, strconv.Itoa(config.InceptionVaultPort("ocfp-lab-drgao")),
		"safe process kill must be scoped to this bloc's port")
}

// TestTeardownLocalInception_DoesNotKillForeignBlocPort is the regression guard
// for the cross-bloc eviction bug: concurrent bootstraps for different blocs on
// one workstation must never tear down each other's inception vault.
func TestTeardownLocalInception_DoesNotKillForeignBlocPort(t *testing.T) {
	recorded := captureLocalCommands(t)

	err := TeardownLocalInception(context.Background(), "ocfp-lab-drgao")
	require.NoError(t, err)

	foreignPort := strconv.Itoa(config.InceptionVaultPort("ocfp-lab-drhu"))
	joined := strings.Join(*recorded, "\n")

	assert.NotContains(t, joined, foreignPort, "must not target a sibling bloc's port")
	assert.NotContains(t, joined, "8234", "must not target the shared legacy port")
}

func TestTeardownLocalInception_BareNamesUseLegacyPort(t *testing.T) {
	recorded := captureLocalCommands(t)

	err := TeardownLocalInception(context.Background(), "")
	require.NoError(t, err)

	joined := strings.Join(*recorded, "\n")
	assert.Contains(t, joined, strconv.Itoa(config.LegacyInceptionVaultPort))
}

func TestTeardownLocalInception_BareNamesWithoutBloc(t *testing.T) {
	recorded := captureLocalCommands(t)

	err := TeardownLocalInception(context.Background(), "")
	require.NoError(t, err)

	assert.Contains(t, *recorded, "tmux kill-session -t inception-vault")
	assert.Contains(t, *recorded, "safe target delete inception")
}

func TestTeardownLocalInception_RejectsInvalidBlocName(t *testing.T) {
	recorded := captureLocalCommands(t)

	err := TeardownLocalInception(context.Background(), "bad bloc; rm -rf /")
	require.Error(t, err)
	assert.Empty(t, *recorded, "no commands may run for an invalid bloc name")
}

func TestTeardownLocalInception_DoesNotRemoveVaultData(t *testing.T) {
	recorded := captureLocalCommands(t)

	err := TeardownLocalInception(context.Background(), "ocfp-lab-drgao")
	require.NoError(t, err)

	joined := strings.Join(*recorded, "\n")
	assert.NotContains(t, joined, "rm ", "teardown must not delete vault data")
}
