package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// seedLegacyKey installs a real key pair under ~/.ocfp/<bloc>/ssh and leaves
// an empty ssh directory under the XDG data home.
func seedLegacyKey(t *testing.T, bloc string) (privateKey []byte) {
	t.Helper()

	root := t.TempDir()
	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))

	tmpKey := filepath.Join(t.TempDir(), "seed")
	require.NoError(t, ssh.NewKeyManager().GenerateKeyPair(tmpKey, "ed25519", 0))

	privateKey, err := os.ReadFile(tmpKey)
	require.NoError(t, err)
	pub, err := os.ReadFile(tmpKey + ".pub")
	require.NoError(t, err)

	legacy := filepath.Join(root, "home", ".ocfp", bloc, "ssh")
	require.NoError(t, os.MkdirAll(legacy, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "id_ed25519"), privateKey, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "id_ed25519.pub"), pub, 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "data", "ocfp", bloc, "ssh"), 0o700))

	return privateKey
}

func TestVerifyExistingKeypair_ReusesLegacyKeyBesideEmptyXDGSSHDir(t *testing.T) {
	m := newKeypairTestManager(t, "kp-legacy-verify", "pve")
	seedLegacyKey(t, "kp-legacy-verify")

	require.NoError(t, m.stateManager.AddResource(&state.Resource{
		ID:       "kp-legacy-verify-keypair",
		Type:     state.ResourceTypeKeyPair,
		Name:     "kp-legacy-verify-keypair",
		Provider: "pve",
	}))

	shouldSkip, err := m.verifyExistingKeypair(t.Context(), "kp-legacy-verify-keypair")
	require.NoError(t, err)
	assert.True(t, shouldSkip, "a legacy key must not be recreated because the XDG ssh dir is empty")
}

func TestGenerateLocalSSHKeyPair_ReusesLegacyKeyBesideEmptyXDGSSHDir(t *testing.T) {
	m := newKeypairTestManager(t, "kp-legacy-generate", "pve")
	want := seedLegacyKey(t, "kp-legacy-generate")

	got, _, wasRead, err := m.generateLocalSSHKeyPair()
	require.NoError(t, err)
	assert.True(t, wasRead, "the legacy key must be read, not regenerated")
	assert.Equal(t, string(want), string(got))
}
