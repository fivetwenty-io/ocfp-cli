package state_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// TestGetStateDir_UsesXDGStateHome verifies GetStateDir resolves under the
// XDG state-class root ($XDG_STATE_HOME/ocfp) rather than the legacy flat
// ~/.ocfp layout when OCFP_HOME is unset and XDG_STATE_HOME is set.
func TestGetStateDir_UsesXDGStateHome(t *testing.T) {
	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", t.TempDir())

	xdgStateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdgStateHome)

	got, err := state.GetStateDir("mybloc")
	if err != nil {
		t.Fatalf("GetStateDir() error: %v", err)
	}

	want := filepath.Join(xdgStateHome, "ocfp", "mybloc", "state")
	if got != want {
		t.Errorf("GetStateDir() = %q, want %q (must resolve under XDG_STATE_HOME, not legacy ~/.ocfp)", got, want)
	}
}

// TestGetStateDir_LegacyFallback verifies GetStateDir falls back to the
// pre-migration ~/.ocfp/{blocName}/state directory when only that path
// exists on disk and the new XDG state-class path does not.
func TestGetStateDir_LegacyFallback(t *testing.T) {
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	legacyStateDir := filepath.Join(tmpHome, ".ocfp", "mybloc", "state")

	err := os.MkdirAll(legacyStateDir, 0o750)
	if err != nil {
		t.Fatalf("failed to create legacy state dir: %v", err)
	}

	got, err := state.GetStateDir("mybloc")
	if err != nil {
		t.Fatalf("GetStateDir() error: %v", err)
	}

	if got != legacyStateDir {
		t.Errorf("GetStateDir() = %q, want %q (legacy fallback)", got, legacyStateDir)
	}
}

// TestGetStateDir_EmptyBlocName verifies GetStateDir rejects an empty bloc
// name before touching any XDG resolution.
func TestGetStateDir_EmptyBlocName(t *testing.T) {
	_, err := state.GetStateDir("")
	if err != state.ErrBlocNameEmpty {
		t.Errorf("GetStateDir(\"\") error = %v, want %v", err, state.ErrBlocNameEmpty)
	}
}

// TestGetStateDir_OcfpHomeOverride verifies OCFP_HOME, when set, still
// collapses GetStateDir onto the legacy flat layout (OCFP_HOME/{bloc}/state)
// regardless of XDG_STATE_HOME.
func TestGetStateDir_OcfpHomeOverride(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	got, err := state.GetStateDir("mybloc")
	if err != nil {
		t.Fatalf("GetStateDir() error: %v", err)
	}

	want := filepath.Join(tmpDir, "mybloc", "state")
	if got != want {
		t.Errorf("GetStateDir() = %q, want %q", got, want)
	}
}

// TestNewManager_EmptyStateDirUsesXDGStateHome verifies NewManager, when
// given an empty stateDir, creates the manager's directory under the XDG
// state-class root rather than the legacy ~/.ocfp/state.
func TestNewManager_EmptyStateDirUsesXDGStateHome(t *testing.T) {
	t.Setenv("OCFP_HOME", "")
	t.Setenv("HOME", t.TempDir())

	xdgStateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdgStateHome)

	mgr, err := state.NewManager("")
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	if mgr == nil {
		t.Fatal("NewManager() returned nil manager with nil error")
	}

	wantDir := filepath.Join(xdgStateHome, "ocfp", "state")

	info, err := os.Stat(wantDir)
	if err != nil {
		t.Fatalf("expected state directory %q to exist: %v", wantDir, err)
	}

	if !info.IsDir() {
		t.Errorf("%q exists but is not a directory", wantDir)
	}
}
