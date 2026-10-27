package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func notATerminal() bool { return false }

// Without a terminal there is nobody to answer the prompt, so init must
// say which flag to pass rather than report a cancellation nobody made.
func TestConfirmInitialization_NoTerminalNamesTheFlag(t *testing.T) {
	t.Parallel()

	f := &initFlags{isTerminal: notATerminal}

	err := f.confirmInitializationIfNeeded(RoleBastion)

	require.ErrorIs(t, err, ErrInitNeedsConfirmation)
	require.NotErrorIs(t, err, ErrInitCancelled)
	assert.Contains(t, err.Error(), "--yes")
	assert.Contains(t, err.Error(), "ocfp init bastion")
}

func TestConfirmInitialization_YesAnswersThePrompt(t *testing.T) {
	t.Parallel()

	f := &initFlags{yes: true, isTerminal: notATerminal}

	require.NoError(t, f.confirmInitializationIfNeeded(RoleBastion))
}

func TestConfirmInitialization_ForceStillSkipsThePrompt(t *testing.T) {
	t.Parallel()

	f := &initFlags{force: true, isTerminal: notATerminal}

	require.NoError(t, f.confirmInitializationIfNeeded(RoleBastion))
}

// --yes only answers the prompt. --force also reinstalls the ocfp CLI, and
// --yes must not pick that up.
func TestYesFlagDoesNotImplyForce(t *testing.T) {
	t.Parallel()

	cmd := NewInitCmd()
	flag := cmd.Flags().Lookup("yes")
	require.NotNil(t, flag, "init has a --yes flag")
	assert.Equal(t, "false", flag.DefValue)

	f := &initFlags{yes: true}
	assert.False(t, f.bastionRunParams().force)
	assert.False(t, buildBastionOptions(bastionModeOCFP, f.bastionRunParams()).Force)
}

func TestInitExamplesShowYes(t *testing.T) {
	t.Parallel()

	assert.Contains(t, getInitExamples(), "--yes")
}
