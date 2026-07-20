package commands

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

// TestSaveRootTokenFromSafeRC_UsesOwnTargetNotCurrent guards the observed
// failure where two blocs wrote root.key one second apart: the root token was
// read from the .saferc *current* target, which a concurrent bootstrap can
// flip between vault startup and key capture, so a bloc persisted a sibling's
// root token and every later command authenticated against the wrong vault.
func TestSaveRootTokenFromSafeRC_UsesOwnTargetNotCurrent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	saferc := `version: 1
current: ocfp-lab-drhu-inception
vaults:
  ocfp-lab-drgao-inception:
    url: http://127.0.0.1:18755
    token: drgao-root-token
  ocfp-lab-drhu-inception:
    url: http://127.0.0.1:18889
    token: drhu-root-token
`

	err := os.WriteFile(filepath.Join(home, ".saferc"), []byte(saferc), 0o600)
	if err != nil {
		t.Fatalf("writing .saferc: %v", err)
	}

	rootKeyFile := filepath.Join(t.TempDir(), "root.key")
	paths := map[string]string{
		"vaultName":   "ocfp-lab-drgao-inception",
		"rootKeyFile": rootKeyFile,
	}

	err = saveRootTokenFromSafeRC(paths, zap.NewNop().Sugar())
	if err != nil {
		t.Fatalf("saveRootTokenFromSafeRC: %v", err)
	}

	data, err := os.ReadFile(rootKeyFile)
	if err != nil {
		t.Fatalf("reading root.key: %v", err)
	}

	got := string(data)
	if got != "drgao-root-token\n" {
		t.Errorf("root.key = %q, want this bloc's own token %q", got, "drgao-root-token\n")
	}
}
