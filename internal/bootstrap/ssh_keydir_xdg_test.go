package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// TestSSHKeyDirConsumers_ResolveUnderXDGDataHome proves the bootstrap SSH-key
// consumers (generateLocalSSHKeyPair, bastionPublicKey — the same
// config.OcfpSSHKeyDir() call artifacts_provision.go's provisionArtifactsViaSSH
// uses to build its KeyPath) resolve under the XDG data root rather than a
// hardcoded ~/.ocfp join, when OCFP_HOME is unset and XDG_DATA_HOME points at
// a temp directory.
func TestSSHKeyDirConsumers_ResolveUnderXDGDataHome(t *testing.T) {
	xdgData := t.TempDir()
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_DATA_HOME", xdgData)

	blocName := "ssh-xdg-bloc"

	wantKeyDir := filepath.Join(xdgData, "ocfp", blocName, "ssh")
	gotKeyDir := config.OcfpSSHKeyDir(blocName)
	require.Equal(t, wantKeyDir, gotKeyDir, "OcfpSSHKeyDir must resolve under XDG_DATA_HOME/ocfp")

	sm, err := state.NewManager(t.TempDir())
	require.NoError(t, err)

	_, err = sm.Load(blocName)
	require.NoError(t, err)

	m := NewManager(&config.Config{}, nil, sm, &Options{BlocName: blocName, Provider: "pve"})

	// Seed a keypair directly at the XDG-resolved directory (not a legacy
	// ~/.ocfp path) and confirm the provision-path consumers read it back
	// from there instead of regenerating or missing it.
	require.NoError(t, os.MkdirAll(wantKeyDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(wantKeyDir, "id_ed25519"), []byte("test-private-key"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(wantKeyDir, "id_ed25519.pub"), []byte("ssh-ed25519 AAAAtest test@xdg"), 0o600))

	privKey, pubKey, wasRead, err := m.generateLocalSSHKeyPair()
	require.NoError(t, err)
	assert.True(t, wasRead, "existing XDG-resolved key must be read, not regenerated")
	assert.Equal(t, "test-private-key", string(privKey))
	assert.Equal(t, "ssh-ed25519 AAAAtest test@xdg", string(pubKey))

	// bastionPublicKey falls back to the same XDG-resolved <bloc>/ssh dir
	// when no keypair_public_key output is recorded in state.
	assert.Equal(t, "ssh-ed25519 AAAAtest test@xdg", m.bastionPublicKey())
}
