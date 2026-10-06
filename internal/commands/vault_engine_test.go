package commands

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// isolateEngineLookup points PATH at bin alone and empties the fallback
// directories, so a real vault or bao on the workstation can never answer for
// a fake that a test left out on purpose.
func isolateEngineLookup(t *testing.T, bin string) {
	t.Helper()

	t.Setenv("PATH", bin)
	t.Setenv(safeEngineEnvVar, "")

	orig := inceptionCommandFallbackDirs
	inceptionCommandFallbackDirs = nil

	t.Cleanup(func() { inceptionCommandFallbackDirs = orig })
}

func TestResolveInceptionEngine_PrefersVaultLikeSafe(t *testing.T) {
	bin := t.TempDir()
	vaultPath := writeFakeExecutable(t, bin, "vault", "exit 0\n")
	writeFakeExecutable(t, bin, "bao", "exit 0\n")
	isolateEngineLookup(t, bin)

	engine, err := resolveInceptionEngine()
	require.NoError(t, err)

	assert.Equal(t, "vault", engine.name)
	assert.Equal(t, vaultPath, engine.path)
}

func TestResolveInceptionEngine_HonoursSafeEngine(t *testing.T) {
	bin := t.TempDir()
	writeFakeExecutable(t, bin, "vault", "exit 0\n")
	baoPath := writeFakeExecutable(t, bin, "bao", "exit 0\n")
	isolateEngineLookup(t, bin)
	t.Setenv(safeEngineEnvVar, " BAO ")

	engine, err := resolveInceptionEngine()
	require.NoError(t, err)

	assert.Equal(t, "bao", engine.name)
	assert.Equal(t, baoPath, engine.path)
}

// A pinned engine that is missing must fail rather than fall back: the
// operator chose it, and safe makes the same choice, so falling back here
// would migrate or restart with a different engine than the one safe runs.
func TestResolveInceptionEngine_PinnedEngineMissingFails(t *testing.T) {
	bin := t.TempDir()
	writeFakeExecutable(t, bin, "vault", "exit 0\n")
	isolateEngineLookup(t, bin)
	t.Setenv(safeEngineEnvVar, "bao")

	_, err := resolveInceptionEngine()
	require.ErrorIs(t, err, ErrVaultEngineNotFound)
	assert.Contains(t, err.Error(), "bao")
	assert.Contains(t, err.Error(), safeEngineEnvVar)
}

func TestResolveInceptionEngine_UnknownEngineNamesTheSupportedOnes(t *testing.T) {
	bin := t.TempDir()
	writeFakeExecutable(t, bin, "vault", "exit 0\n")
	isolateEngineLookup(t, bin)
	t.Setenv(safeEngineEnvVar, "consul")

	_, err := resolveInceptionEngine()
	require.ErrorIs(t, err, ErrUnknownVaultEngine)
	assert.Contains(t, err.Error(), "consul")
	assert.Contains(t, err.Error(), "vault, bao")
}

func TestResolveInceptionEngine_NoEngineFails(t *testing.T) {
	isolateEngineLookup(t, t.TempDir())

	_, err := resolveInceptionEngine()
	require.ErrorIs(t, err, ErrVaultEngineNotFound)
}

// The engine may sit in a fallback directory that is not on PATH, which is
// the bastion's non-interactive SSH case.
func TestResolveInceptionEngine_SearchesFallbackDirs(t *testing.T) {
	isolateEngineLookup(t, t.TempDir())

	fallback := t.TempDir()
	baoPath := writeFakeExecutable(t, fallback, "bao", "exit 0\n")
	inceptionCommandFallbackDirs = []string{fallback}

	engine, err := resolveInceptionEngine()
	require.NoError(t, err)

	assert.Equal(t, "bao", engine.name)
	assert.Equal(t, baoPath, engine.path)
	assert.Equal(t, fallback, filepath.Dir(engine.path))
}

// The prerequisite check must accept OpenBao alone, since a workstation
// pinned to bao may have no vault binary at all.
func TestCheckVaultInceptionPrerequisites_AcceptsBaoAlone(t *testing.T) {
	bin := t.TempDir()
	fakeSafe(t, bin, "safe v1.25.0", fakeSafeLocalHelp)
	writeFakeExecutable(t, bin, "bao", "exit 0\n")
	writeFakeExecutable(t, bin, "tmux", "exit 0\n")
	writeFakeExecutable(t, bin, "script", "exit 0\n")
	isolateEngineLookup(t, bin)

	tools, err := checkVaultInceptionPrerequisites(context.Background(), zap.NewNop().Sugar())
	require.NoError(t, err)
	assert.Equal(t, "bao", tools.engine.name)
	assert.Equal(t, filepath.Join(bin, "safe"), tools.safe, "the checked safe is the one that runs")
	assert.True(t, filepath.IsAbs(tools.safe))
}
