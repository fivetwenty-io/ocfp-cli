package vault

import (
	"context"
	"strings"
	"testing"

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
	assert.Contains(t, joined, "8234", "safe process kill must be port-scoped")
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
