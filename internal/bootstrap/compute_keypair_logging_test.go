package bootstrap

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

type noKeypairCloud struct{ keyMutationCounter }

func (*noKeypairCloud) GetKeyPair(_ context.Context, _ string) (*cpi.KeyPair, error) {
	return nil, errors.New("not found")
}

// debugLogText turns debug logging on into a temp dir and returns a func that
// reads everything written there.
func debugLogText(t *testing.T) func() string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, logger.Initialize(logger.Config{Level: "debug", Debug: true, LogDir: dir}))

	t.Cleanup(func() { _ = logger.Initialize(logger.Config{Level: "info", NoLog: true}) })

	return func() string {
		_ = logger.Sync()

		var all strings.Builder

		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				data, _ := os.ReadFile(path) // #nosec G304 -- test temp dir

				all.Write(data)
			}

			return nil
		})

		return all.String()
	}
}

func TestSavePrivateKey_DebugLogDoesNotQuoteTheKey(t *testing.T) {
	const bloc = "kp-log-save"

	root := t.TempDir()
	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))

	m := newKeypairTestManager(t, bloc, "pve")
	logText := debugLogText(t)

	require.NoError(t, m.savePrivateKey("KEYMARKER-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ", "ssh-ed25519 AAAA test"))

	assert.NotContains(t, logText(), "KEYMARKER")
}

func TestCreateLocalKeyPair_DebugLogDoesNotQuoteTheKey(t *testing.T) {
	const bloc = "kp-log-create"

	root := t.TempDir()
	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))

	m := newKeypairTestManager(t, bloc, "pve")
	logText := debugLogText(t)

	_, _, err := m.createLocalKeyPair(t.Context(), &noKeypairCloud{}, bloc+"-keypair")
	require.NoError(t, err)

	// A generated OpenSSH private key opens with this header and base64 body.
	assert.NotContains(t, logText(), "BEGIN OPENSSH PRIVATE KEY")
}
