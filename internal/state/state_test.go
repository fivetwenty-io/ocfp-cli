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

// TestAddResource_UpdatesIDOfExistingResource pins the behaviour a bastion
// recycle depends on, and which it did not have.
//
// Resources are keyed by type and name, not by provider id, precisely so that
// a guest can be replaced while keeping its name. The recycle does exactly
// that: it builds a replacement, destroys the original, and rewrites the
// instance record so the bloc knows which VMID to talk to now. That rewrite
// went through AddResource, which copied state, properties, and tags onto the
// record it already held and left the ID alone. The bloc state then still
// named the VM that had just been destroyed, which aims the next teardown at a
// VMID that no longer exists, or at whatever guest later takes that number.
func TestAddResource_UpdatesIDOfExistingResource(t *testing.T) {
	mgr, err := state.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	_, err = mgr.Load("mybloc")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	err = mgr.AddResource(&state.Resource{
		ID:       "100",
		Type:     state.ResourceTypeInstance,
		Name:     "mybloc-bastion",
		Provider: "pve",
		State:    "active",
	})
	if err != nil {
		t.Fatalf("AddResource() error: %v", err)
	}

	err = mgr.AddResource(&state.Resource{
		ID:       "102",
		Type:     state.ResourceTypeInstance,
		Name:     "mybloc-bastion",
		Provider: "pve",
		State:    "active",
	})
	if err != nil {
		t.Fatalf("AddResource() re-record error: %v", err)
	}

	got, err := mgr.GetResource(state.ResourceTypeInstance, "mybloc-bastion")
	if err != nil {
		t.Fatalf("GetResource() error: %v", err)
	}

	if got.ID != "102" {
		t.Errorf("resource ID = %q, want %q; the replacement's id must replace the retired one", got.ID, "102")
	}
}
