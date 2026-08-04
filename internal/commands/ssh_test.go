package commands

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

// findSSHKey must resolve a bloc's key via config.OcfpSSHKeyDir, which
// lands under the XDG data root ($XDG_DATA_HOME/ocfp/<bloc>/ssh) rather
// than the legacy flat ~/.ocfp/<bloc>/ssh layout, once XDG_DATA_HOME is
// set.
func TestFindSSHKeyResolvesUnderXDGDataHome(t *testing.T) {
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

	got, err := findSSHKey(blocName, nil)
	if err != nil {
		t.Fatalf("findSSHKey returned error: %v", err)
	}

	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// findSSHKey must fall back to id_rsa under the same XDG-resolved
// directory when no id_ed25519 key is present.
func TestFindSSHKeyRSAFallbackUnderXDGDataHome(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_DATA_HOME", dataHome)

	home := t.TempDir()
	t.Setenv("HOME", home)

	blocName := "ocfp-lab-wayne"

	want := filepath.Join(config.DataHome(), blocName, "ssh", "id_rsa")

	if err := os.MkdirAll(filepath.Dir(want), 0o700); err != nil {
		t.Fatalf("mkdir ssh dir: %v", err)
	}

	if err := os.WriteFile(want, []byte("placeholder-rsa-key-bytes"), 0o600); err != nil {
		t.Fatalf("write rsa key: %v", err)
	}

	got, err := findSSHKey(blocName, nil)
	if err != nil {
		t.Fatalf("findSSHKey returned error: %v", err)
	}

	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}
