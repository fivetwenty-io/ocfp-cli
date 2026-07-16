package vault

import (
	"os"
	"path/filepath"
	"testing"
)

// genesisWorkDir must prefer an explicit DEPLOYMENTS_DIR over any detected
// layout: genesis @env:type addressing only resolves from inside the
// deployments repository, and the override is the operator's word on where
// that repository lives.
func TestGenesisWorkDir_DeploymentsDirOverride(t *testing.T) {
	t.Setenv("DEPLOYMENTS_DIR", "/custom/deployments")

	got := genesisWorkDir()
	if got != "/custom/deployments" {
		t.Fatalf("expected DEPLOYMENTS_DIR override to win, got %q", got)
	}
}

// Without an override, the standard ~/ocfp/deployments layout is used when
// it exists.
func TestGenesisWorkDir_StandardLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DEPLOYMENTS_DIR", "")
	t.Setenv("HOME", home)

	dir := filepath.Join(home, "ocfp", "deployments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("failed to create standard layout: %v", err)
	}

	got := genesisWorkDir()
	if got != dir {
		t.Fatalf("expected standard layout %q, got %q", dir, got)
	}
}

// When neither the override nor the standard layout exists, the caller cwd
// must be inherited (empty Dir) so an operator already inside their
// deployments repository keeps working.
func TestGenesisWorkDir_FallsBackToCallerCwd(t *testing.T) {
	t.Setenv("DEPLOYMENTS_DIR", "")
	t.Setenv("HOME", t.TempDir())

	got := genesisWorkDir()
	if got != "" {
		t.Fatalf("expected empty workdir (inherit caller cwd), got %q", got)
	}
}
