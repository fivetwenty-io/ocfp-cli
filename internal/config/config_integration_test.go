package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestLoadAppliesPVEDefaultsWithBlocOverride verifies the end-to-end YAML
// loading path: global pve: credentials are inherited by blocs that omit them,
// and bloc-level values override the global defaults on a field-by-field basis.
func TestLoadAppliesPVEDefaultsWithBlocOverride(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	configContent := `pve:
  auth_token: "root@pam!ocfp-bosh-cpi-root"
  token_secret: "global-secret"
  username: "global-user"
  password: "global-pass"
blocs:
  inherit-all:
    provider: pve
    api_endpoint: https://pve.inherit.example
  override-token:
    provider: pve
    api_endpoint: https://pve.override.example
    auth_token: "root@pam!override-token"
    token_secret: "override-secret"
`
	configPath := filepath.Join(tmpDir, "config.yml")
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

	// Clear cache so each sub-test loads fresh from disk.
	clearCache := func() {
		configMutex.Lock()
		cachedConfigs = make(map[string]*cachedConfig)
		configMutex.Unlock()
	}

	t.Run("inherit-all bloc gets all global defaults", func(t *testing.T) {
		clearCache()

		cfg, err := LoadWithParams(configPath, "inherit-all")
		require.NoError(t, err)

		assert.Equal(t, "root@pam!ocfp-bosh-cpi-root", cfg.AuthToken, "AuthToken must come from global pve defaults")
		assert.Equal(t, "global-secret", cfg.TokenSecret, "TokenSecret must come from global pve defaults")
		assert.Equal(t, "global-user", cfg.Username, "Username must come from global pve defaults")
		assert.Equal(t, "global-pass", cfg.Password, "Password must come from global pve defaults")
	})

	t.Run("override-token bloc overrides token fields, inherits username+password", func(t *testing.T) {
		clearCache()

		cfg, err := LoadWithParams(configPath, "override-token")
		require.NoError(t, err)

		assert.Equal(t, "root@pam!override-token", cfg.AuthToken, "AuthToken must use bloc-level value")
		assert.Equal(t, "override-secret", cfg.TokenSecret, "TokenSecret must use bloc-level value")
		assert.Equal(t, "global-user", cfg.Username, "Username must fall back to global pve defaults")
		assert.Equal(t, "global-pass", cfg.Password, "Password must fall back to global pve defaults")
	})
}

// TestLoadAppliesTailscaleDefaultsWithBlocOverride exercises the end-to-end
// YAML loading path: global tailscale: defaults are inherited by blocs that
// omit fields, and bloc-level fields override the global defaults
// individually.
func TestLoadAppliesTailscaleDefaultsWithBlocOverride(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OCFP_HOME", tmpDir)

	configContent := `tailscale:
  auth_key_vault_path: "secret/ocfp/tailscale:auth_key"
  tags:
    - "tag:ocfp-bastion"
  accept_dns: false
  ssh: true
blocs:
  inherit-all:
    provider: pve
    api_endpoint: https://pve.inherit.example
  override-key:
    provider: pve
    api_endpoint: https://pve.override.example
    tailscale:
      auth_key: "tskey-bloc-literal"
      hostname: "override-host"
`
	configPath := filepath.Join(tmpDir, "config.yml")
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

	clearCache := func() {
		configMutex.Lock()
		cachedConfigs = make(map[string]*cachedConfig)
		configMutex.Unlock()
	}

	t.Run("inherit-all bloc gets all global tailscale defaults", func(t *testing.T) {
		clearCache()

		cfg, err := LoadWithParams(configPath, "inherit-all")
		require.NoError(t, err)
		require.NotNil(t, cfg.Tailscale, "Tailscale must be populated from global defaults")

		assert.Equal(t, "", cfg.Tailscale.AuthKey)
		assert.Equal(t, "secret/ocfp/tailscale:auth_key", cfg.Tailscale.AuthKeyVaultPath)
		assert.Equal(t, []string{"tag:ocfp-bastion"}, cfg.Tailscale.Tags)
		require.NotNil(t, cfg.Tailscale.AcceptDNS)
		assert.False(t, *cfg.Tailscale.AcceptDNS)
		require.NotNil(t, cfg.Tailscale.SSH)
		assert.True(t, *cfg.Tailscale.SSH)
	})

	t.Run("override-key bloc uses literal auth_key without inheriting vault path", func(t *testing.T) {
		clearCache()

		cfg, err := LoadWithParams(configPath, "override-key")
		require.NoError(t, err)
		require.NotNil(t, cfg.Tailscale)

		assert.Equal(t, "tskey-bloc-literal", cfg.Tailscale.AuthKey, "bloc literal wins")
		assert.Equal(t, "", cfg.Tailscale.AuthKeyVaultPath, "global vault path must not bleed through when bloc sets literal")
		assert.Equal(t, "override-host", cfg.Tailscale.Hostname)
		assert.Equal(t, []string{"tag:ocfp-bastion"}, cfg.Tailscale.Tags, "tags inherited from global")
		require.NotNil(t, cfg.Tailscale.SSH)
		assert.True(t, *cfg.Tailscale.SSH)
	})
}
