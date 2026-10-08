package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// linkVaultDir moves the directory at link to a directory elsewhere and
// leaves a link to it in its place, so the vault is reached only through
// the link. It returns where the directory now lives.
func linkVaultDir(t *testing.T, link string) string {
	t.Helper()

	target := filepath.Join(t.TempDir(), "elsewhere", filepath.Base(link))

	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o700))
	require.NoError(t, os.Rename(link, target))
	require.NoError(t, os.Symlink(target, link))

	return target
}

// requireLinkRefusal checks that err refuses to archive link, names the link
// and its target, and tells the operator how to go on.
func requireLinkRefusal(t *testing.T, err error, link, target string) {
	t.Helper()

	require.ErrorIs(t, err, ErrVaultDirIsLink)

	for _, want := range []string{link, target, "remove the link"} {
		assert.Contains(t, err.Error(), want)
	}
}

// requireLinkUntouched checks that link still leads to target and that
// nothing was archived beside either of them.
func requireLinkUntouched(t *testing.T, link, target string) {
	t.Helper()

	got, err := os.Readlink(link)
	require.NoError(t, err, "the link must stay where it was")
	assert.Equal(t, target, got)

	for _, dir := range []string{link, target} {
		archives, globErr := filepath.Glob(dir + VaultArchiveSuffix + "*")
		require.NoError(t, globErr)
		assert.Empty(t, archives, "nothing may be archived beside %s", dir)
	}
}

// An archive renames the vault directory. When that directory is a link,
// the rename moves the link and leaves the vault where it was, so a later
// resolution can find the old vault again. The archive refuses instead, in
// the bloc layout and in the layout without a bloc alike, and moves nothing,
// not even a loose key file.
func TestArchiveAndForgetVault_RefusesAVaultDirThatIsALink(t *testing.T) {
	t.Run("bloc layout", func(t *testing.T) {
		paths := blocVaultLayout(t)
		vaultRoot := filepath.Dir(paths["vaultDir"])
		require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte("root-token"), 0o600))

		target := linkVaultDir(t, vaultRoot)

		archived, err := archiveAndForgetVault(paths, "20261008-120000", zap.NewNop().Sugar())
		requireLinkRefusal(t, err, vaultRoot, target)
		assert.Empty(t, archived)
		requireLinkUntouched(t, vaultRoot, target)
		assert.FileExists(t, filepath.Join(target, "root.key"))
		assert.FileExists(t, filepath.Join(target, "data", "vault.db"))
	})

	t.Run("layout without a bloc", func(t *testing.T) {
		home := t.TempDir()
		paths := map[string]string{
			"vaultDir":       filepath.Join(home, ".vault"),
			"rootKeyFile":    filepath.Join(home, "vault.root.key"),
			"unsealKeysFile": filepath.Join(home, "vault.unseal.keys"),
		}

		require.NoError(t, os.MkdirAll(filepath.Join(paths["vaultDir"], "core"), 0o700))
		require.NoError(t, os.WriteFile(paths["rootKeyFile"], []byte("root-token"), 0o600))

		target := linkVaultDir(t, paths["vaultDir"])

		archived, err := archiveAndForgetVault(paths, "20261008-120000", zap.NewNop().Sugar())
		requireLinkRefusal(t, err, paths["vaultDir"], target)
		assert.Empty(t, archived)
		requireLinkUntouched(t, paths["vaultDir"], target)
		assert.FileExists(t, paths["rootKeyFile"], "a loose key file must stay where it is")
	})
}

// cleanupExistingVault stops the vault before it archives it, so it checks
// for a linked vault directory first and leaves the vault running.
func TestCleanupExistingVault_RefusesAVaultDirThatIsALinkBeforeStopping(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	vaultRoot := filepath.Dir(paths["vaultDir"])
	target := linkVaultDir(t, vaultRoot)

	ops := installFakeVaultOps(t)
	t.Setenv("HOME", t.TempDir())

	err := cleanupExistingVault(context.Background(), paths, zap.NewNop().Sugar())
	requireLinkRefusal(t, err, vaultRoot, target)
	assert.Empty(t, ops.commands, "nothing may be stopped")
	requireLinkUntouched(t, vaultRoot, target)
}

// Teardown refuses a linked vault directory before it probes, stops, or
// archives anything, so the vault keeps running and the boot unit stays
// enabled.
func TestTeardown_RefusesAVaultDirThatIsALink(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	vaultRoot := filepath.Dir(paths["vaultDir"])
	target := linkVaultDir(t, vaultRoot)
	before := treeDigest(t, target)

	fake := &fakeInception{probe: healthyRaftProbe(), session: true}

	ops, err := runInceptionTeardown(t, paths, fake, false)
	requireLinkRefusal(t, err, vaultRoot, target)
	assert.Contains(t, err.Error(), "teardown refused")
	assert.Empty(t, fake.calls, "nothing may be probed, stopped, archived, or disabled")
	assert.Empty(t, ops.commands, "no stop command may run")
	assert.Equal(t, before, treeDigest(t, target))
	requireLinkUntouched(t, vaultRoot, target)
}

// The one archive a reconcile makes, of key files left with no data,
// refuses a linked vault directory and starts no new vault.
func TestReconcile_LeftoverKeysInALinkedVaultDirAreNotArchived(t *testing.T) {
	paths := reconcilePaths(t)
	writeKeys(t, paths, true, true)

	vaultRoot := filepath.Dir(paths["vaultDir"])
	target := linkVaultDir(t, vaultRoot)
	before := treeDigest(t, target)

	fake := &fakeInception{probe: stoppedProbe()}

	err := runReconcile(t, paths, fake)
	requireLinkRefusal(t, err, vaultRoot, target)
	assert.NotContains(t, fake.calls, "start fresh")
	assert.Equal(t, before, treeDigest(t, target))
	requireLinkUntouched(t, vaultRoot, target)
}

// hideVaultRoot strips all access from the directory that holds the vault
// directory, so any look at the vault directory fails with something other
// than "does not exist". It restores access when the test ends.
func hideVaultRoot(t *testing.T, paths map[string]string) string {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}

	vaultRoot := vaultArchiveTarget(paths)
	parent := filepath.Dir(vaultRoot)

	require.NoError(t, os.Chmod(parent, 0))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	return vaultRoot
}

// A vault directory that cannot be examined is refused with the path and the
// cause before anything stops, since the archive would fail on it after the
// stop and leave the vault down.
func TestCleanupExistingVault_RefusesAnUnexaminableVaultDirBeforeStopping(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	ops := installFakeVaultOps(t)
	t.Setenv("HOME", t.TempDir())

	vaultRoot := hideVaultRoot(t, paths)

	err := cleanupExistingVault(context.Background(), paths, zap.NewNop().Sugar())
	require.ErrorIs(t, err, os.ErrPermission)
	assert.Contains(t, err.Error(), vaultRoot)
	assert.Empty(t, ops.commands, "nothing may be stopped")
}

func TestTeardown_RefusesAnUnexaminableVaultDirAndLeavesTheVaultRunning(t *testing.T) {
	paths := reconcilePaths(t)
	writeRaftData(t, paths["vaultDir"])
	writeKeys(t, paths, true, true)

	fake := &fakeInception{probe: healthyRaftProbe(), session: true}
	vaultRoot := hideVaultRoot(t, paths)

	ops, err := runInceptionTeardown(t, paths, fake, false)
	require.ErrorIs(t, err, os.ErrPermission)
	assert.Contains(t, err.Error(), vaultRoot)
	assert.Contains(t, err.Error(), "teardown refused")
	assert.Empty(t, fake.calls, "nothing may be probed, stopped, archived, or disabled")
	assert.Empty(t, ops.commands, "no stop command may run")
}
