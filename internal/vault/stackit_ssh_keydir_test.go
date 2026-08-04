package vault

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
)

// TestStackitProvider_GetPrivateKeyPath_ResolvesUnderXDGDataHome proves
// StackitVaultProvider.getPrivateKeyPath resolves its "standard OCFP
// location" lookup through config.OcfpSSHKeyDir() under the XDG data root
// rather than a hardcoded ~/.ocfp join, when OCFP_HOME is unset and
// XDG_DATA_HOME points at a temp directory.
func TestStackitProvider_GetPrivateKeyPath_ResolvesUnderXDGDataHome(t *testing.T) {
	xdgData := t.TempDir()
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_DATA_HOME", xdgData)

	blocName := "stackit-ssh-xdg-bloc"

	wantKeyDir := filepath.Join(xdgData, "ocfp", blocName, "ssh")
	require.Equal(t, wantKeyDir, config.OcfpSSHKeyDir(blocName),
		"OcfpSSHKeyDir must resolve under XDG_DATA_HOME/ocfp")

	require.NoError(t, os.MkdirAll(wantKeyDir, 0o700))

	wantKeyPath := filepath.Join(wantKeyDir, "id_ed25519")
	require.NoError(t, os.WriteFile(wantKeyPath, []byte("test-private-key"), 0o600))

	cfg := &config.Config{Name: blocName, Provider: "stackit"}
	provider := &StackitVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, blocName),
		logger:            logger.Get(),
	}

	gotKeyPath := provider.getPrivateKeyPath("irrelevant-keypair-name")
	assert.Equal(t, wantKeyPath, gotKeyPath, "getPrivateKeyPath must find the key under the XDG-resolved ssh dir")
}
