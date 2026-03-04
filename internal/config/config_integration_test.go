package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestConfigFileNeverWrittenBySetCurrentBloc verifies that SetCurrentBloc
// writes to state.yml and never touches config.yml.
func TestConfigFileNeverWrittenBySetCurrentBloc(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	// Create a config.yml with specific content
	configContent := "# User config - do not modify\nblocs:\n  my-bloc:\n    provider: aws\n"
	configPath := filepath.Join(tmpDir, "config.yml")

	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	if err != nil {
		t.Fatalf("failed to write config.yml: %v", err)
	}

	// Record modification time
	origInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("failed to stat config.yml: %v", err)
	}

	// Set current bloc via state
	err = SetCurrentBloc("my-bloc", configPath)
	if err != nil {
		t.Fatalf("SetCurrentBloc() error: %v", err)
	}

	// Verify config.yml content is unchanged
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config.yml: %v", err)
	}

	if string(data) != configContent {
		t.Errorf("config.yml was modified!\nExpected:\n%s\nGot:\n%s", configContent, string(data))
	}

	// Verify modification time is unchanged
	newInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("failed to stat config.yml after: %v", err)
	}

	if !newInfo.ModTime().Equal(origInfo.ModTime()) {
		t.Error("config.yml modification time changed, indicating it was written to")
	}

	// Verify state.yml was created
	statePath := filepath.Join(tmpDir, "state.yml")

	_, err = os.Stat(statePath)
	if os.IsNotExist(err) {
		t.Fatal("state.yml was not created")
	}
}

// TestConfigFileNeverWrittenBySaveBlocKeys verifies that SaveBlocKeys
// writes to state.yml and never touches config.yml.
func TestConfigFileNeverWrittenBySaveBlocKeys(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	// Create a config.yml with comments and 2-space indent
	configContent := `# My hand-crafted config
blocs:
  prod:
    provider: aws
    region: us-east-1
`
	configPath := filepath.Join(tmpDir, "config.yml")

	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	if err != nil {
		t.Fatalf("failed to write config.yml: %v", err)
	}

	// Save keys via state file
	keys := map[string]string{"prod-keypair": "private-key-data"}

	err = SaveBlocKeys("prod", keys)
	if err != nil {
		t.Fatalf("SaveBlocKeys() error: %v", err)
	}

	// Verify config.yml content is unchanged
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config.yml: %v", err)
	}

	if string(data) != configContent {
		t.Errorf("config.yml was modified!\nExpected:\n%s\nGot:\n%s", configContent, string(data))
	}
}

// TestKeyMergeFromStateInLoadWithParams verifies that LoadWithParams merges
// keys from the state file into the loaded config.
func TestKeyMergeFromStateInLoadWithParams(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	// Write a valid config file with a bloc
	configContent := `blocs:
  test-bloc:
    provider: aws
    region: us-east-1
    iaas: aws
`
	configPath := filepath.Join(tmpDir, "config.yml")

	err := os.WriteFile(configPath, []byte(configContent), 0o600)
	if err != nil {
		t.Fatalf("failed to write config.yml: %v", err)
	}

	// Save keys to state file
	keys := map[string]string{"test-keypair": "my-private-key"}

	err = SaveBlocKeys("test-bloc", keys)
	if err != nil {
		t.Fatalf("SaveBlocKeys() error: %v", err)
	}

	// Clear cache to force reload
	configMutex.Lock()
	cachedConfigs = make(map[string]*cachedConfig)
	configMutex.Unlock()

	// Load config -- keys should be merged from state
	cfg, err := LoadWithParams(configPath, "test-bloc")
	if err != nil {
		t.Fatalf("LoadWithParams() error: %v", err)
	}

	if cfg.Keys == nil {
		t.Fatal("expected Keys to be non-nil after merge")
	}

	if cfg.Keys["test-keypair"] != "my-private-key" {
		t.Errorf("expected merged key, got %q", cfg.Keys["test-keypair"])
	}
}
