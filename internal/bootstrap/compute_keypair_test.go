package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// newKeypairTestManager builds a Manager with its own bloc name so each test
// gets an isolated $OCFP_HOME/<bloc>/ssh directory.
func newKeypairTestManager(t *testing.T, blocName, provider string) *Manager {
	t.Helper()

	sm, err := state.NewManager(t.TempDir())
	require.NoError(t, err)

	_, err = sm.Load(blocName)
	require.NoError(t, err)

	return NewManager(&config.Config{}, nil, sm, &Options{BlocName: blocName, Provider: provider})
}

// seedPrivateKey generates a real ed25519 OpenSSH key pair and installs the
// requested halves into the bloc's ssh dir, returning their contents.
func seedPrivateKey(t *testing.T, blocName string, withPub bool) (privateKey, publicKey []byte) {
	t.Helper()

	tmpKey := filepath.Join(t.TempDir(), "seed")
	require.NoError(t, ssh.NewKeyManager().GenerateKeyPair(tmpKey, "ed25519", 0))

	privateKey, err := os.ReadFile(tmpKey)
	require.NoError(t, err)
	publicKey, err = os.ReadFile(tmpKey + ".pub")
	require.NoError(t, err)

	keyDir := config.OcfpSSHKeyDir(blocName)
	// OCFP_HOME is shared package-wide (TestMain), so wipe any halves left by
	// a previous -count repeat — a stale .pub would mask the freshly seeded key.
	require.NoError(t, os.RemoveAll(keyDir))
	require.NoError(t, os.MkdirAll(keyDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(keyDir, "id_ed25519"), privateKey, 0o600))

	if withPub {
		require.NoError(t, os.WriteFile(filepath.Join(keyDir, "id_ed25519.pub"), publicKey, 0o600))
	}

	return privateKey, publicKey
}

// TestGenerateLocalSSHKeyPair_ReusesExistingPair proves that an on-disk pair
// is returned verbatim instead of being regenerated.
func TestGenerateLocalSSHKeyPair_ReusesExistingPair(t *testing.T) {
	t.Parallel()

	m := newKeypairTestManager(t, "kp-reuse-bloc", "pve")
	wantPriv, wantPub := seedPrivateKey(t, "kp-reuse-bloc", true)

	gotPriv, gotPub, wasRead, err := m.generateLocalSSHKeyPair()
	require.NoError(t, err)
	assert.True(t, wasRead, "existing pair must be read, not regenerated")
	assert.Equal(t, string(wantPriv), string(gotPriv))
	assert.Equal(t, string(wantPub), string(gotPub))
}

// TestGenerateLocalSSHKeyPair_DerivesMissingPub proves that a private key
// without its .pub sidecar (older releases never wrote one) is reused with
// the public half derived and persisted — not rotated.
func TestGenerateLocalSSHKeyPair_DerivesMissingPub(t *testing.T) {
	t.Parallel()

	m := newKeypairTestManager(t, "kp-derive-bloc", "pve")
	wantPriv, wantPub := seedPrivateKey(t, "kp-derive-bloc", false)

	gotPriv, gotPub, wasRead, err := m.generateLocalSSHKeyPair()
	require.NoError(t, err)
	assert.True(t, wasRead, "private key on disk must be reused even without .pub")
	assert.Equal(t, string(wantPriv), string(gotPriv))
	assert.Equal(t, string(wantPub), string(gotPub), "derived public key must match the original")

	persisted, err := os.ReadFile(filepath.Join(config.OcfpSSHKeyDir("kp-derive-bloc"), "id_ed25519.pub"))
	require.NoError(t, err, "derived .pub must be written back for future runs")
	assert.Equal(t, string(wantPub), string(persisted))
}

// TestVerifyExistingKeypair_LocalProviderSkipsCloudLookup proves that for
// providers without a server-side keypair store the local private key is
// authoritative: state + local file ⇒ skip, no cloud call (provider is nil).
func TestVerifyExistingKeypair_LocalProviderSkipsCloudLookup(t *testing.T) {
	t.Parallel()

	m := newKeypairTestManager(t, "kp-skip-bloc", "pve")
	seedPrivateKey(t, "kp-skip-bloc", true)

	shouldSkip, err := m.verifyExistingKeypair(t.Context(), "kp-skip-bloc-keypair")
	require.NoError(t, err)
	assert.True(t, shouldSkip, "keypair in state with local key must not be recreated")
}

// TestVerifyExistingKeypair_LocalProviderMissingKeyRecreates proves the stale
// path still recreates when the private key file is gone.
func TestVerifyExistingKeypair_LocalProviderMissingKeyRecreates(t *testing.T) {
	t.Parallel()

	m := newKeypairTestManager(t, "kp-missing-bloc", "pve")

	require.NoError(t, m.stateManager.AddResource(&state.Resource{
		ID:       "kp-missing-bloc-keypair",
		Type:     state.ResourceTypeKeyPair,
		Name:     "kp-missing-bloc-keypair",
		Provider: "pve",
	}))

	shouldSkip, err := m.verifyExistingKeypair(t.Context(), "kp-missing-bloc-keypair")
	require.NoError(t, err)
	assert.False(t, shouldSkip, "missing private key must trigger recreation")

	res, _ := m.stateManager.GetResource(state.ResourceTypeKeyPair, "kp-missing-bloc-keypair")
	assert.Nil(t, res, "stale state entry must be removed")
}
