package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// keyMutationCounter fails the test for any cloud call except the ones it
// counts. Embedding the interface leaves every other method nil, so an
// unexpected call panics instead of passing.
type keyMutationCounter struct {
	cpi.ComputeManager

	deletes, creates, imports int
}

func (c *keyMutationCounter) DeleteKeyPair(_ context.Context, _ string) error {
	c.deletes++

	return nil
}

func (c *keyMutationCounter) CreateKeyPair(_ context.Context, _ *cpi.KeyPairRequest) (*cpi.KeyPair, error) {
	c.creates++

	return &cpi.KeyPair{}, nil
}

func (c *keyMutationCounter) ImportKeyPair(_ context.Context, _, _ string) error {
	c.imports++

	return nil
}

// useUnreadableXDGKeyDir installs a key in the bloc's XDG ssh dir and strips
// the dir's permissions, so any look at the key fails with something other
// than "does not exist". It returns the ssh dir.
func useUnreadableXDGKeyDir(t *testing.T, bloc string) string {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}

	root := t.TempDir()
	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))

	dir := filepath.Join(root, "data", "ocfp", bloc, "ssh")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "id_ed25519"), []byte("not-a-real-key"), 0o600))
	require.NoError(t, os.Chmod(dir, 0))

	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	return dir
}

// useLoopingRSALink leaves id_rsa as a link to itself, so Stat fails with
// ELOOP while id_ed25519 is simply absent.
func useLoopingRSALink(t *testing.T, bloc string) {
	t.Helper()

	root := t.TempDir()
	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))

	dir := filepath.Join(root, "data", "ocfp", bloc, "ssh")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.Symlink(filepath.Join(dir, "id_rsa"), filepath.Join(dir, "id_rsa")))
}

func TestVerifyExistingKeypair_UnreadableKeyDirKeepsStateAndFails(t *testing.T) {
	for _, provider := range []string{"pve", "aws"} {
		t.Run(provider, func(t *testing.T) {
			bloc := "kp-unreadable-verify-" + provider
			m := newKeypairTestManager(t, bloc, provider)
			useUnreadableXDGKeyDir(t, bloc)

			require.NoError(t, m.stateManager.AddResource(&state.Resource{
				ID:       bloc + "-keypair",
				Type:     state.ResourceTypeKeyPair,
				Name:     bloc + "-keypair",
				Provider: provider,
			}))

			shouldSkip, err := m.verifyExistingKeypair(t.Context(), bloc+"-keypair")
			require.Error(t, err)
			assert.False(t, shouldSkip)

			res, _ := m.stateManager.GetResource(state.ResourceTypeKeyPair, bloc+"-keypair")
			assert.NotNil(t, res, "an unreadable key dir must not drop the keypair from state")
		})
	}
}

func TestGenerateLocalSSHKeyPair_UnreadableKeyDirFailsWithoutGenerating(t *testing.T) {
	bloc := "kp-unreadable-generate"
	m := newKeypairTestManager(t, bloc, "pve")
	dir := useUnreadableXDGKeyDir(t, bloc)

	priv, pub, wasRead, err := m.generateLocalSSHKeyPair()
	require.Error(t, err)
	assert.Nil(t, priv)
	assert.Nil(t, pub)
	assert.False(t, wasRead)

	require.NoError(t, os.Chmod(dir, 0o700))

	got, readErr := os.ReadFile(filepath.Join(dir, "id_ed25519"))
	require.NoError(t, readErr)
	assert.Equal(t, "not-a-real-key", string(got), "the existing key must be left alone")
}

func TestHandleDuplicateKeyPair_UnreadableKeyDirDoesNotTouchTheCloud(t *testing.T) {
	bloc := "kp-unreadable-dup"
	m := newKeypairTestManager(t, bloc, "aws")
	useUnreadableXDGKeyDir(t, bloc)

	cloud := &keyMutationCounter{}

	kp, _, err := m.handleDuplicateKeyPair(t.Context(), cloud, bloc+"-keypair")
	require.Error(t, err)
	assert.Nil(t, kp)
	assert.Zero(t, cloud.deletes, "no cloud keypair may be deleted")
	assert.Zero(t, cloud.creates)
	assert.Zero(t, cloud.imports)
}

func TestHandleDuplicateKeyPair_UninspectableRSAKeyDoesNotTouchTheCloud(t *testing.T) {
	bloc := "kp-unreadable-rsa"
	m := newKeypairTestManager(t, bloc, "aws")
	useLoopingRSALink(t, bloc)

	cloud := &keyMutationCounter{}

	kp, _, err := m.handleDuplicateKeyPair(t.Context(), cloud, bloc+"-keypair")
	require.Error(t, err)
	assert.Nil(t, kp)
	assert.Zero(t, cloud.deletes, "no cloud keypair may be deleted")
}

func TestGenerateLocalSSHKeyPair_UninspectableEd25519KeyIsNotRotated(t *testing.T) {
	bloc := "kp-looping-ed25519"
	m := newKeypairTestManager(t, bloc, "pve")

	root := t.TempDir()
	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))

	dir := filepath.Join(root, "data", "ocfp", bloc, "ssh")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.Symlink(filepath.Join(dir, "id_ed25519"), filepath.Join(dir, "id_ed25519")))

	_, _, _, err := m.generateLocalSSHKeyPair()
	require.Error(t, err)
}

func TestHandleDuplicateKeyPair_UninspectableEd25519KeyDeletesNothing(t *testing.T) {
	bloc := "kp-looping-ed25519-dup"
	m := newKeypairTestManager(t, bloc, "aws")

	root := t.TempDir()
	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))

	dir := filepath.Join(root, "data", "ocfp", bloc, "ssh")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.Symlink(filepath.Join(dir, "id_ed25519"), filepath.Join(dir, "id_ed25519")))

	cloud := &keyMutationCounter{}

	kp, _, err := m.handleDuplicateKeyPair(t.Context(), cloud, bloc+"-keypair")
	require.Error(t, err)
	assert.Nil(t, kp)
	assert.Zero(t, cloud.deletes, "no cloud keypair may be deleted")
	assert.Zero(t, cloud.creates, "no cloud keypair may be created")
}
