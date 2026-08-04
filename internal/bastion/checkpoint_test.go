package bastion

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Note: tests using t.Setenv cannot call t.Parallel() (Go enforcement).

// ---------------------------------------------------------------------------
// NewCheckpointManager / CheckpointManager.Save
// checkpointDir must resolve under the new XDG state root (StateHome()),
// not the pre-migration ~/.ocfp layout, when only XDG_STATE_HOME is set.
// ---------------------------------------------------------------------------

func TestNewCheckpointManager_ResolvesUnderXDGStateHome(t *testing.T) {
	tmpHome := t.TempDir()
	xdgStateDir := t.TempDir()

	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_STATE_HOME", xdgStateDir)

	cm := NewCheckpointManager(newBaseConfig("bloc1", "aws"))

	expectedDir := filepath.Join(xdgStateDir, "ocfp", "checkpoints")
	assert.Equal(t, expectedDir, cm.checkpointDir,
		"checkpointDir must resolve under XDG_STATE_HOME, not legacy ~/.ocfp")

	legacyDir := filepath.Join(tmpHome, ".ocfp", "checkpoints")
	assert.NotEqual(t, legacyDir, cm.checkpointDir)
}

func TestCheckpointManager_Save_WritesUnderXDGStateHome(t *testing.T) {
	tmpHome := t.TempDir()
	xdgStateDir := t.TempDir()

	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_STATE_HOME", xdgStateDir)

	cm := NewCheckpointManager(newBaseConfig("bloc1", "aws"))
	cm.log = newTestLogger()

	progress := &ProvisioningProgress{
		TotalSteps:     4,
		CompletedSteps: 2,
		CurrentStep:    "provision",
		Checkpoints:    map[string]bool{"provision": true},
	}

	err := cm.Save(progress, nil)
	require.NoError(t, err)

	expectedFile := filepath.Join(xdgStateDir, "ocfp", "checkpoints", "bastion-bloc1-aws.json")
	_, statErr := os.Stat(expectedFile)
	assert.NoError(t, statErr, "checkpoint file must land under XDG_STATE_HOME")

	legacyFile := filepath.Join(tmpHome, ".ocfp", "checkpoints", "bastion-bloc1-aws.json")
	_, legacyErr := os.Stat(legacyFile)
	assert.True(t, os.IsNotExist(legacyErr), "checkpoint file must not land under legacy ~/.ocfp")
}

// ---------------------------------------------------------------------------
// Manager.saveCheckpoint (init.go) shares the same checkpoints directory
// resolution as CheckpointManager.
// ---------------------------------------------------------------------------

func TestManager_saveCheckpoint_WritesUnderXDGStateHome(t *testing.T) {
	tmpHome := t.TempDir()
	xdgStateDir := t.TempDir()

	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_STATE_HOME", xdgStateDir)

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))

	err := m.saveCheckpoint()
	require.NoError(t, err)

	expectedDir := filepath.Join(xdgStateDir, "ocfp", "checkpoints")
	_, statErr := os.Stat(expectedDir)
	assert.NoError(t, statErr, "checkpoint directory must land under XDG_STATE_HOME")

	legacyDir := filepath.Join(tmpHome, ".ocfp", "checkpoints")
	_, legacyErr := os.Stat(legacyDir)
	assert.True(t, os.IsNotExist(legacyErr), "checkpoint directory must not land under legacy ~/.ocfp")
}
