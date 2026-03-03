package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateFilePath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	got := StateFilePath()
	want := filepath.Join(tmpDir, "state.yml")

	if got != want {
		t.Errorf("StateFilePath() = %q, want %q", got, want)
	}
}

func TestLoadState_FileNotExist(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState() returned unexpected error: %v", err)
	}

	if state == nil {
		t.Fatal("LoadState() returned nil state")
	}

	if state.CurrentBloc != "" {
		t.Errorf("expected empty CurrentBloc, got %q", state.CurrentBloc)
	}
}

func TestSaveState_WritesWithTwoSpaceIndent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	state := &StateFile{
		CurrentBloc: "my-bloc",
		Blocs: map[string]*BlocState{
			"my-bloc": {
				Keys: map[string]string{
					"my-key": "key-value",
				},
			},
		},
	}

	err := SaveState(state)
	if err != nil {
		t.Fatalf("SaveState() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "state.yml"))
	if err != nil {
		t.Fatalf("failed to read state file: %v", err)
	}

	content := string(data)

	// Verify 2-space indentation (not 4-space)
	if strings.Contains(content, "    my-bloc") {
		t.Error("state file uses 4-space indentation, expected 2-space")
	}

	if !strings.Contains(content, "  my-bloc") {
		t.Error("state file should contain 2-space indented bloc name")
	}

	// Verify content is correct
	if !strings.Contains(content, "current_bloc: my-bloc") {
		t.Errorf("expected current_bloc in output, got:\n%s", content)
	}

	if !strings.Contains(content, "my-key: key-value") {
		t.Errorf("expected key data in output, got:\n%s", content)
	}
}

func TestSaveState_Permissions(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	state := &StateFile{CurrentBloc: "test"}

	err := SaveState(state)
	if err != nil {
		t.Fatalf("SaveState() error: %v", err)
	}

	info, err := os.Stat(filepath.Join(tmpDir, "state.yml"))
	if err != nil {
		t.Fatalf("failed to stat state file: %v", err)
	}

	// Check that permissions are 0600
	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("state file permissions = %o, want 0600", perm)
	}
}

func TestSetCurrentBloc(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	err := SetCurrentBloc("prod", "/path/to/config.yml")
	if err != nil {
		t.Fatalf("SetCurrentBloc() error: %v", err)
	}

	// Verify round-trip
	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState() error: %v", err)
	}

	if state.CurrentBloc != "prod" {
		t.Errorf("CurrentBloc = %q, want %q", state.CurrentBloc, "prod")
	}

	if state.ConfigFile != "/path/to/config.yml" {
		t.Errorf("ConfigFile = %q, want %q", state.ConfigFile, "/path/to/config.yml")
	}
}

func TestSaveBlocKeys(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	keys := map[string]string{
		"my-keypair": "-----BEGIN OPENSSH PRIVATE KEY-----\ntest\n-----END OPENSSH PRIVATE KEY-----\n",
	}

	err := SaveBlocKeys("my-bloc", keys)
	if err != nil {
		t.Fatalf("SaveBlocKeys() error: %v", err)
	}

	// Verify round-trip
	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState() error: %v", err)
	}

	if state.Blocs == nil {
		t.Fatal("expected Blocs to be non-nil")
	}

	bs, ok := state.Blocs["my-bloc"]
	if !ok {
		t.Fatal("expected my-bloc in Blocs")
	}

	if bs.Keys["my-keypair"] != keys["my-keypair"] {
		t.Errorf("key mismatch: got %q", bs.Keys["my-keypair"])
	}
}

func TestGetBlocKeys(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	// Save keys first
	keys := map[string]string{"key1": "val1", "key2": "val2"}

	err := SaveBlocKeys("test-bloc", keys)
	if err != nil {
		t.Fatalf("SaveBlocKeys() error: %v", err)
	}

	// Retrieve
	got, err := GetBlocKeys("test-bloc")
	if err != nil {
		t.Fatalf("GetBlocKeys() error: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(got))
	}

	if got["key1"] != "val1" {
		t.Errorf("key1 = %q, want %q", got["key1"], "val1")
	}

	if got["key2"] != "val2" {
		t.Errorf("key2 = %q, want %q", got["key2"], "val2")
	}
}

func TestGetBlocKeys_NonExistentBloc(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	got, err := GetBlocKeys("nonexistent")
	if err != nil {
		t.Fatalf("GetBlocKeys() error: %v", err)
	}

	if got != nil {
		t.Errorf("expected nil keys for nonexistent bloc, got %v", got)
	}
}

func TestMigrateStateFromConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	// Write a legacy config.yml with old-style keys
	legacyConfig := `bloc: legacy-bloc
config_file: /old/path/config.yml
current_environment: legacy-bloc
`

	err := os.WriteFile(filepath.Join(tmpDir, "config.yml"), []byte(legacyConfig), 0o600)
	if err != nil {
		t.Fatalf("failed to write legacy config: %v", err)
	}

	// LoadState should trigger migration
	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState() error: %v", err)
	}

	if state.CurrentBloc != "legacy-bloc" {
		t.Errorf("CurrentBloc = %q, want %q", state.CurrentBloc, "legacy-bloc")
	}

	if state.ConfigFile != "/old/path/config.yml" {
		t.Errorf("ConfigFile = %q, want %q", state.ConfigFile, "/old/path/config.yml")
	}

	// Verify state.yml was created
	if _, err := os.Stat(filepath.Join(tmpDir, "state.yml")); os.IsNotExist(err) {
		t.Error("expected state.yml to be created during migration")
	}
}

func TestMigrateStateFromConfig_NoConfigFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	// No config.yml exists -- should return empty state
	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState() error: %v", err)
	}

	if state.CurrentBloc != "" {
		t.Errorf("expected empty CurrentBloc, got %q", state.CurrentBloc)
	}
}

func TestSaveBlocKeys_PreservesExistingState(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	// Set current bloc first
	err := SetCurrentBloc("my-bloc", "/some/config.yml")
	if err != nil {
		t.Fatalf("SetCurrentBloc() error: %v", err)
	}

	// Now save keys
	keys := map[string]string{"keypair": "private-key-data"}

	err = SaveBlocKeys("my-bloc", keys)
	if err != nil {
		t.Fatalf("SaveBlocKeys() error: %v", err)
	}

	// Verify both current_bloc and keys are present
	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState() error: %v", err)
	}

	if state.CurrentBloc != "my-bloc" {
		t.Errorf("CurrentBloc = %q, want %q", state.CurrentBloc, "my-bloc")
	}

	if state.Blocs["my-bloc"].Keys["keypair"] != "private-key-data" {
		t.Error("expected saved key to persist alongside current_bloc")
	}
}

func TestGetCurrentBloc(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	// Initially empty
	bloc, err := GetCurrentBloc()
	if err != nil {
		t.Fatalf("GetCurrentBloc() error: %v", err)
	}

	if bloc != "" {
		t.Errorf("expected empty bloc, got %q", bloc)
	}

	// Set and retrieve
	err = SetCurrentBloc("staging", "/path/to/staging.yml")
	if err != nil {
		t.Fatalf("SetCurrentBloc() error: %v", err)
	}

	bloc, err = GetCurrentBloc()
	if err != nil {
		t.Fatalf("GetCurrentBloc() error: %v", err)
	}

	if bloc != "staging" {
		t.Errorf("GetCurrentBloc() = %q, want %q", bloc, "staging")
	}
}
