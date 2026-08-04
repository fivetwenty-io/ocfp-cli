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
// ModeDetector.checkMarkerFiles / isOCFPProvisioned
// Both must find bastion state markers under the new XDG state root, not
// only the pre-migration ~/.ocfp layout.
// ---------------------------------------------------------------------------

func TestCheckMarkerFiles_FindsMarkerUnderXDGStateHome(t *testing.T) {
	tmpHome := t.TempDir()
	xdgStateDir := t.TempDir()

	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_STATE_HOME", xdgStateDir)

	stateRoot := filepath.Join(xdgStateDir, "ocfp")
	require.NoError(t, os.MkdirAll(stateRoot, 0750))
	require.NoError(t, os.WriteFile(filepath.Join(stateRoot, "provisioned"), []byte("1"), 0600))

	md := newTestModeDetector(newBaseConfig("bloc1", "aws"))
	assert.True(t, md.checkMarkerFiles(), "marker under XDG_STATE_HOME must be found")
}

func TestIsOCFPProvisioned_FindsMarkerUnderXDGStateHome(t *testing.T) {
	tmpHome := t.TempDir()
	xdgStateDir := t.TempDir()

	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_STATE_HOME", xdgStateDir)

	stateRoot := filepath.Join(xdgStateDir, "ocfp")
	require.NoError(t, os.MkdirAll(stateRoot, 0750))
	require.NoError(t, os.WriteFile(filepath.Join(stateRoot, "provisioned"), []byte("1"), 0600))

	assert.True(t, isOCFPProvisioned(), "marker under XDG_STATE_HOME must be found")
}

// ---------------------------------------------------------------------------
// ModeDetector.checkDirectoryStructure
// The OCFP-root leg of the check must resolve under the new XDG state root.
// ---------------------------------------------------------------------------

func TestCheckDirectoryStructure_UsesXDGStateHome(t *testing.T) {
	tmpHome := t.TempDir()
	xdgStateDir := t.TempDir()

	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_STATE_HOME", xdgStateDir)

	require.NoError(t, os.MkdirAll(filepath.Join(tmpHome, "ocfp", "deployments"), 0750))
	require.NoError(t, os.MkdirAll(filepath.Join(xdgStateDir, "ocfp"), 0750))

	md := newTestModeDetector(newBaseConfig("bloc1", "aws"))
	assert.True(t, md.checkDirectoryStructure(),
		"directory structure check must find the OCFP state root under XDG_STATE_HOME")
}

// ---------------------------------------------------------------------------
// Manager.findConfigFile (init.go)
// Must resolve config.yml under the new XDG config root, not only the
// pre-migration ~/.ocfp layout.
// ---------------------------------------------------------------------------

func TestManager_findConfigFile_ResolvesUnderXDGConfigHome(t *testing.T) {
	tmpHome := t.TempDir()
	xdgConfigDir := t.TempDir()

	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", xdgConfigDir)

	configRoot := filepath.Join(xdgConfigDir, "ocfp")
	require.NoError(t, os.MkdirAll(configRoot, 0750))
	expectedPath := filepath.Join(configRoot, "config.yml")
	require.NoError(t, os.WriteFile(expectedPath, []byte("blocs: {}\n"), 0600))

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))

	resolved, err := m.findConfigFile()
	require.NoError(t, err)
	assert.Equal(t, expectedPath, resolved)
}

// ---------------------------------------------------------------------------
// Manager.validatePrerequisites (init.go)
// The provisioning log directory must be created under the new XDG state
// root, not the pre-migration ~/.ocfp layout.
// ---------------------------------------------------------------------------

func TestManager_validatePrerequisites_CreatesLogDirUnderXDGStateHome(t *testing.T) {
	tmpHome := t.TempDir()
	xdgStateDir := t.TempDir()

	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_STATE_HOME", xdgStateDir)

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))

	err := m.validatePrerequisites()
	require.NoError(t, err)

	expectedDir := filepath.Join(xdgStateDir, "ocfp", "logs", "provision")
	_, statErr := os.Stat(expectedDir)
	assert.NoError(t, statErr, "provision log directory must land under XDG_STATE_HOME")

	legacyDir := filepath.Join(tmpHome, ".ocfp", "logs", "provision")
	_, legacyErr := os.Stat(legacyDir)
	assert.True(t, os.IsNotExist(legacyErr), "provision log directory must not land under legacy ~/.ocfp")
}
