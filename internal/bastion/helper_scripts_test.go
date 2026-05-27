package bastion

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHelperScripts_RegistersBlobstores guarantees the helper_scripts phase
// will ship the blobstores tool to every bastion. Forgetting to add a new
// helper here means it silently disappears from `ocfp bastion init`.
func TestHelperScripts_RegistersBlobstores(t *testing.T) {
	t.Parallel()

	var found bool

	for _, h := range helperScripts {
		if h.source == "blobstores" && h.dest == "blobstores" {
			found = true

			break
		}
	}

	if !found {
		t.Fatalf("helperScripts is missing the blobstores entry; got %+v", helperScripts)
	}
}

// TestResolveHelperScript_EnvOverride asserts OCFP_HELPER_SCRIPTS_DIR wins
// over every default search path. The override lets an operator point at a
// custom checkout without rebuilding ocfp.
func TestResolveHelperScript_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "blobstores")

	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	t.Setenv("OCFP_HELPER_SCRIPTS_DIR", dir)

	got, err := resolveHelperScript("blobstores")
	if err != nil {
		t.Fatalf("resolveHelperScript: %v", err)
	}

	if got != script {
		t.Errorf("got %q, want %q", got, script)
	}
}

// TestResolveHelperScript_ErrorListsCandidates ensures the error message tells
// the operator which paths were tried. Hidden lookup failures are a debugging
// hazard during bastion init.
func TestResolveHelperScript_ErrorListsCandidates(t *testing.T) {
	t.Setenv("OCFP_HELPER_SCRIPTS_DIR", "")
	// Force the cwd to a directory that cannot contain a scripts/ tree.
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	_, err := resolveHelperScript("definitely-not-a-script-xyz")
	if err == nil {
		t.Fatal("expected error for missing helper script, got nil")
	}

	msg := err.Error()
	if !contains(msg, "searched:") {
		t.Errorf("error message %q does not enumerate search paths", msg)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}

	return false
}
