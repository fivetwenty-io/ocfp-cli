package vault

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoBlocSaferc writes a ~/.saferc holding two blocs' inception targets plus
// mgmt targets, with the global current pointer aimed at the *other* bloc —
// the exact shape produced when several bootstraps run concurrently. drhu has
// BOTH its inception and mgmt targets, the post-migration shape in which the
// frozen inception vault is still listed in ~/.saferc.
func twoBlocSaferc(t *testing.T) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)

	body := `version: 1
current: ocfp-lab-drhu-inception
vaults:
  ocfp-lab-drgao-inception:
    url: http://127.0.0.1:18755
    token: drgao-token
    skip_verify: true
  ocfp-lab-drhu-inception:
    url: http://127.0.0.1:18889
    token: drhu-token
    skip_verify: true
  ocfp-lab-drhu-mgmt:
    url: https://10.64.60.12:8200
    token: drhu-mgmt-token
  ocfp-lab-krutten-mgmt:
    url: https://10.64.70.12:8200
    token: krutten-mgmt-token
`

	err := os.WriteFile(filepath.Join(home, ".saferc"), []byte(body), 0o600)
	require.NoError(t, err)
}

// clearVaultEnv removes the ambient vault env so a test exercises the .saferc
// path rather than whatever the developer's shell exports.
func clearVaultEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{"VAULT_ADDR", "VAULT_TOKEN", "VAULT_NAMESPACE", "VAULT_SKIP_VERIFY"} {
		t.Setenv(key, "")
	}
}

func TestReadSafeConfigTarget_ByNameIgnoresCurrent(t *testing.T) {
	twoBlocSaferc(t)

	addr, token, skip, err := readSafeConfigTarget("ocfp-lab-drgao-inception")
	require.NoError(t, err)

	assert.Equal(t, "http://127.0.0.1:18755", addr)
	assert.Equal(t, "drgao-token", token)
	assert.True(t, skip)
}

func TestReadSafeConfigTarget_UnknownTargetErrors(t *testing.T) {
	twoBlocSaferc(t)

	_, _, _, err := readSafeConfigTarget("ocfp-lab-dbell-inception")
	require.Error(t, err)
}

// TestResolveBlocVaultConfig_IgnoresGlobalCurrentTarget is the regression guard
// for the second cross-bloc corruption path: `safe` keeps one global current
// target, so a bloc that resolves its vault from it writes into whichever
// sibling happened to run last.
func TestResolveBlocVaultConfig_IgnoresGlobalCurrentTarget(t *testing.T) {
	twoBlocSaferc(t)
	clearVaultEnv(t)

	cfg, err := resolveBlocVaultConfig("ocfp-lab-drgao")
	require.NoError(t, err)

	assert.Equal(t, "http://127.0.0.1:18755", cfg.Address, "must use this bloc's own target")
	assert.Equal(t, "drgao-token", cfg.Token)
	assert.NotEqual(t, "drhu-token", cfg.Token, "must not inherit the global current target")
}

func TestResolveBlocVaultConfig_PrefersExplicitEnv(t *testing.T) {
	twoBlocSaferc(t)
	clearVaultEnv(t)
	t.Setenv("VAULT_ADDR", "http://127.0.0.1:19999")
	t.Setenv("VAULT_TOKEN", "explicit-token")

	cfg, err := resolveBlocVaultConfig("ocfp-lab-drgao")
	require.NoError(t, err)

	assert.Equal(t, "http://127.0.0.1:19999", cfg.Address)
	assert.Equal(t, "explicit-token", cfg.Token)
}

// After `ocfp vault migrate` the inception target is deleted and the bloc's
// secrets live in <bloc>-mgmt, so that is the fallback — still by name.
func TestResolveBlocVaultConfig_FallsBackToMgmtTarget(t *testing.T) {
	twoBlocSaferc(t)
	clearVaultEnv(t)

	cfg, err := resolveBlocVaultConfig("ocfp-lab-krutten")
	require.NoError(t, err)

	assert.Equal(t, "https://10.64.70.12:8200", cfg.Address)
	assert.Equal(t, "krutten-mgmt-token", cfg.Token)
}

// TestResolveBlocVaultConfig_MgmtWinsOverInception guards the post-migration
// shape: migration is supposed to delete the inception target but a frozen
// inception vault often stays listed in ~/.saferc. The bloc's secrets live in
// the mgmt vault, so with both targets present mgmt must win — resolving
// inception first writes every unpinned populate into the dead vault.
func TestResolveBlocVaultConfig_MgmtWinsOverInception(t *testing.T) {
	twoBlocSaferc(t)
	clearVaultEnv(t)

	cfg, err := resolveBlocVaultConfig("ocfp-lab-drhu")
	require.NoError(t, err)

	assert.Equal(t, "https://10.64.60.12:8200", cfg.Address, "mgmt target must win over the frozen inception target")
	assert.Equal(t, "drhu-mgmt-token", cfg.Token)
	assert.NotEqual(t, "drhu-token", cfg.Token, "must not resolve the frozen inception vault")
}

// A bloc with no target of its own must end up token-less — so the client
// fails with ErrTokenAuthRequiresVaultToken and the operator is told to set
// VAULT_ADDR/VAULT_TOKEN — rather than silently borrowing a sibling's vault.
func TestResolveBlocVaultConfig_UnknownBlocGetsNoForeignCredentials(t *testing.T) {
	twoBlocSaferc(t)
	clearVaultEnv(t)

	cfg, err := resolveBlocVaultConfig("ocfp-lab-dbell")
	require.NoError(t, err)

	assert.Empty(t, cfg.Token, "must not inherit any sibling's token")
	assert.Equal(t, "https://127.0.0.1:8200", cfg.Address)

	_, clientErr := NewClient(cfg)
	require.ErrorIs(t, clientErr, ErrTokenAuthRequiresVaultToken)
}

func TestResolveBlocVaultConfig_EmptyBlocErrors(t *testing.T) {
	twoBlocSaferc(t)
	clearVaultEnv(t)

	_, err := resolveBlocVaultConfig("")
	require.Error(t, err)
}
