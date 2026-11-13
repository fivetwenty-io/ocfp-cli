package ssh

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/fivetwenty-io/ocfp-cli/internal/testenv"
	"github.com/fivetwenty-io/ocfp-cli/internal/writeguard"
	"github.com/stretchr/testify/assert"
)

// A test process that restores a bloc's key into the developer's real data
// directory panics before it writes the key.
func TestRestoreKeyFromConfigPanicsUnderTheRealDataDirInATest(t *testing.T) {
	home := t.TempDir()
	t.Cleanup(writeguard.OverrideRealHome(home))
	t.Setenv(writeguard.EnvVar, "1")
	t.Setenv(writeguard.RootsEnvVar, "")
	t.Setenv("HOME", home)
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	assert.Contains(t, guardPanic(func() {
		_, _ = NewKeyManager().RestoreKeyFromConfig("mybloc", "-----BEGIN OPENSSH PRIVATE KEY-----\n")
	}), "SAFETY GUARD")

	_, err := os.Stat(filepath.Join(home, ".local"))
	assert.True(t, os.IsNotExist(err), "the guard must stop the restore before it creates anything")
}

// The config resolves the key directory first and checks it, so a test
// that guarded the directory would never reach the check in
// RestoreKeyFromConfig. Guarding the key file leaves the directory open.
func TestRestoreKeyFromConfigChecksTheKeyFileItself(t *testing.T) {
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	keyDir := config.OcfpSSHKeyDir("mybloc")
	testenv.GuardOnly(t, filepath.Join(keyDir, "id_ed25519"))

	assert.Contains(t, testenv.PanicMessage(func() {
		_, _ = NewKeyManager().RestoreKeyFromConfig("mybloc", "-----BEGIN OPENSSH PRIVATE KEY-----\n")
	}), "ssh.RestoreKeyFromConfig")
	assert.NoDirExists(t, keyDir, "the guard must stop the restore before it creates the directory")
}

// The key writers refuse a path under the real directories that a caller
// built for itself, without the config resolving it.
func TestKeyWritersPanicUnderTheRealDataDirInATest(t *testing.T) {
	home := t.TempDir()
	t.Cleanup(writeguard.OverrideRealHome(home))
	t.Setenv(writeguard.EnvVar, "1")

	keyPath := filepath.Join(home, ".local", "share", "ocfp", "mybloc", "ssh", "id_ed25519")
	km := NewKeyManager()

	assert.Contains(t, guardPanic(func() { _ = km.ensureSSHDirectory(keyPath) }), "ssh key directory")
	assert.Contains(t, guardPanic(func() { _ = km.writePublicKey(keyPath+".pub", nil) }), "ssh public key")

	_, err := os.Stat(filepath.Join(home, ".local"))
	assert.True(t, os.IsNotExist(err))
}

// guardPanic runs fn and returns what it panicked with, or "" when it
// returned.
func guardPanic(fn func()) (message string) {
	defer func() {
		if r := recover(); r != nil {
			message = fmt.Sprint(r)
		}
	}()

	fn()

	return ""
}
