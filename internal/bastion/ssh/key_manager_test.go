package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	xssh "golang.org/x/crypto/ssh"
)

// writeTestEd25519Key generates an OpenSSH-format ed25519 private key at path.
func writeTestEd25519Key(t *testing.T, path string) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	block, err := xssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
}

// FindPrivateKey must discover the shared OCFP fleet bastion key at
// ~/.ssh/ocfp-bastions when no bloc-specific key exists. This is the
// IdentityFile the ocfp bastions Host block uses, and PVE blocs rely on it.
func TestFindPrivateKeyFindsSharedOCFPBastionsKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatalf("mkdir .ssh: %v", err)
	}

	keyPath := filepath.Join(sshDir, "ocfp-bastions")
	writeTestEd25519Key(t, keyPath)

	got, err := NewKeyManager().FindPrivateKey("ocfp-lab-wayne")
	if err != nil {
		t.Fatalf("FindPrivateKey returned error: %v", err)
	}

	if got != keyPath {
		t.Errorf("expected %q, got %q", keyPath, got)
	}
}

// FindPrivateKey must resolve a bloc's key directory via
// config.OcfpSSHKeyDir, which lands under the XDG data root
// ($XDG_DATA_HOME/ocfp/<bloc>/ssh) rather than the legacy flat
// ~/.ocfp/<bloc>/ssh layout, once XDG_DATA_HOME is set.
func TestFindPrivateKeyResolvesUnderXDGDataHome(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_DATA_HOME", dataHome)

	home := t.TempDir()
	t.Setenv("HOME", home)

	blocName := "ocfp-lab-wayne"

	want := filepath.Join(config.DataHome(), blocName, "ssh", "id_ed25519")

	if err := os.MkdirAll(filepath.Dir(want), 0o700); err != nil {
		t.Fatalf("mkdir ssh dir: %v", err)
	}

	writeTestEd25519Key(t, want)

	got, err := NewKeyManager().FindPrivateKey(blocName)
	if err != nil {
		t.Fatalf("FindPrivateKey returned error: %v", err)
	}

	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// RestoreKeyFromConfig must write the restored key under
// config.OcfpSSHKeyDir, which resolves under the XDG data root when
// XDG_DATA_HOME is set, not the legacy flat ~/.ocfp layout.
func TestRestoreKeyFromConfigResolvesUnderXDGDataHome(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_DATA_HOME", dataHome)

	home := t.TempDir()
	t.Setenv("HOME", home)

	blocName := "ocfp-lab-wayne"

	tmpKeyPath := filepath.Join(t.TempDir(), "source_key")
	writeTestEd25519Key(t, tmpKeyPath)

	pemBytes, err := os.ReadFile(tmpKeyPath) //nolint:gosec // test-generated fixture path
	if err != nil {
		t.Fatalf("read fixture key: %v", err)
	}

	got, err := NewKeyManager().RestoreKeyFromConfig(blocName, string(pemBytes))
	if err != nil {
		t.Fatalf("RestoreKeyFromConfig returned error: %v", err)
	}

	want := filepath.Join(config.DataHome(), blocName, "ssh", "id_ed25519")
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}
