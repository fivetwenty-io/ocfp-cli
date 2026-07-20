package bastion

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// mockSSHClient implements the SSH client interface for testing.
type mockSSHClient struct {
	transferredFiles map[string]string // remote path -> local content
	transferError    error
}

func (m *mockSSHClient) Connect(_ctx context.Context) error {
	return nil
}

func (m *mockSSHClient) TransferFile(_ctx context.Context, local, remote string, _opts ssh.TransferOptions) error {
	if m.transferError != nil {
		return m.transferError
	}

	if m.transferredFiles == nil {
		m.transferredFiles = make(map[string]string)
	}

	// Read local file content
	content, err := os.ReadFile(local)
	if err != nil {
		return err
	}

	m.transferredFiles[remote] = string(content)

	return nil
}

func (m *mockSSHClient) ExecuteCommand(_ctx context.Context, cmd string) (*ssh.CommandResult, error) {
	// Return realistic values for common commands
	if cmd == "echo $HOME" {
		return &ssh.CommandResult{ExitCode: 0, Stdout: "/home/testuser\n", Stderr: ""}, nil
	}

	return &ssh.CommandResult{ExitCode: 0, Stdout: "", Stderr: ""}, nil
}

func (m *mockSSHClient) CreateTunnel(_ctx context.Context, _localPort, _remotePort int) error {
	return nil
}

func (m *mockSSHClient) Close() error {
	return nil
}

// newTestLogger creates a simple no-op logger for testing.
func newTestLogger() *zap.SugaredLogger {
	cfg := zap.NewDevelopmentConfig()
	cfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	zapLogger, _ := cfg.Build()

	return zapLogger.Sugar()
}

// setupTestConfig creates a temporary config file for testing.
func setupTestConfig(t *testing.T, configData *config.ConfigFile) string {
	t.Helper()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yml")

	yamlBytes, err := yaml.Marshal(configData)
	require.NoError(t, err, "Failed to marshal test config")

	err = os.WriteFile(configPath, yamlBytes, 0600)
	require.NoError(t, err, "Failed to write test config file")

	return configPath
}

func TestManager_copyOCFPConfig_FiltersSingleBloc(t *testing.T) {
	// Setup: Create multi-bloc config file
	testConfig := &config.ConfigFile{
		Debug:   true,
		Verbose: false,
		Blocs: map[string]*config.Config{
			"test-bloc": {
				Name:     "test-bloc",
				Provider: "aws",
				Region:   "us-east-1",
			},
			"other-bloc": {
				Name:     "other-bloc",
				Provider: "stackit",
				Region:   "eu-01",
			},
			"third-bloc": {
				Name:     "third-bloc",
				Provider: "aws",
				Region:   "us-west-2",
			},
		},
	}

	configPath := setupTestConfig(t, testConfig)

	// Set OCFP_HOME to temp .ocfp dir so config is found
	tmpHome := filepath.Dir(configPath)
	ocfpDir := filepath.Join(tmpHome, ".ocfp")
	err := os.MkdirAll(ocfpDir, 0755)
	require.NoError(t, err)

	t.Setenv("OCFP_HOME", ocfpDir)

	// Move config to expected location
	expectedConfigPath := filepath.Join(ocfpDir, "config.yml")
	err = os.Rename(configPath, expectedConfigPath)
	require.NoError(t, err)

	// Create manager with mock SSH client
	mockSSH := &mockSSHClient{}
	log := newTestLogger()

	manager := &Manager{
		config: &config.Config{
			Name: "test-bloc",
		},
		sshClient: mockSSH,
		log:       log,
	}

	// Execute
	ctx := context.Background()
	err = manager.copyOCFPConfig(ctx)

	// Verify
	require.NoError(t, err, "copyOCFPConfig should succeed")
	assert.Contains(t, mockSSH.transferredFiles, "/home/testuser/.ocfp/config.yml", "Should transfer to remote path")

	// Parse transferred content
	transferredContent := mockSSH.transferredFiles["/home/testuser/.ocfp/config.yml"]
	var transferredConfig config.ConfigFile
	err = yaml.Unmarshal([]byte(transferredContent), &transferredConfig)
	require.NoError(t, err, "Transferred content should be valid YAML")

	// Verify globals preserved
	assert.Equal(t, true, transferredConfig.Debug, "Debug setting should be preserved")
	assert.Equal(t, false, transferredConfig.Verbose, "Verbose setting should be preserved")

	// Verify only target bloc included
	assert.Len(t, transferredConfig.Blocs, 1, "Should have exactly 1 bloc")
	assert.Contains(t, transferredConfig.Blocs, "test-bloc", "Should include test-bloc")
	assert.NotContains(t, transferredConfig.Blocs, "other-bloc", "Should not include other-bloc")
	assert.NotContains(t, transferredConfig.Blocs, "third-bloc", "Should not include third-bloc")

	// Verify bloc config correct
	testBlocConfig := transferredConfig.Blocs["test-bloc"]
	assert.Equal(t, "test-bloc", testBlocConfig.Name)
	assert.Equal(t, "aws", testBlocConfig.Provider)
	assert.Equal(t, "us-east-1", testBlocConfig.Region)
}

func TestManager_copyOCFPConfig_ErrorsOnMissingBloc(t *testing.T) {
	// Setup: Config without target bloc
	testConfig := &config.ConfigFile{
		Debug:   false,
		Verbose: true,
		Blocs: map[string]*config.Config{
			"existing-bloc": {
				Name:     "existing-bloc",
				Provider: "stackit",
				Region:   "eu-01",
			},
		},
	}

	configPath := setupTestConfig(t, testConfig)

	// Set OCFP_HOME to temp .ocfp dir
	tmpHome := filepath.Dir(configPath)
	ocfpDir := filepath.Join(tmpHome, ".ocfp")
	err := os.MkdirAll(ocfpDir, 0755)
	require.NoError(t, err)

	t.Setenv("OCFP_HOME", ocfpDir)

	// Move config to expected location
	expectedConfigPath := filepath.Join(ocfpDir, "config.yml")
	err = os.Rename(configPath, expectedConfigPath)
	require.NoError(t, err)

	// Create manager
	mockSSH := &mockSSHClient{}
	log := newTestLogger()

	manager := &Manager{
		config: &config.Config{
			Name: "missing-bloc",
		},
		sshClient: mockSSH,
		log:       log,
	}

	// Execute
	ctx := context.Background()
	err = manager.copyOCFPConfig(ctx)

	// Verify
	require.Error(t, err, "Should return error for missing bloc")
	assert.Contains(t, err.Error(), "missing-bloc", "Error should mention bloc name")
	assert.Contains(t, err.Error(), "not found", "Error should indicate bloc not found")
}

func TestManager_copyOCFPConfig_PreservesGlobals(t *testing.T) {
	testCases := []struct {
		name    string
		debug   bool
		verbose bool
	}{
		{
			name:    "both true",
			debug:   true,
			verbose: true,
		},
		{
			name:    "both false",
			debug:   false,
			verbose: false,
		},
		{
			name:    "debug true verbose false",
			debug:   true,
			verbose: false,
		},
		{
			name:    "debug false verbose true",
			debug:   false,
			verbose: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup
			testConfig := &config.ConfigFile{
				Debug:   tc.debug,
				Verbose: tc.verbose,
				Blocs: map[string]*config.Config{
					"test-bloc": {
						Name:     "test-bloc",
						Provider: "aws",
					},
				},
			}

			configPath := setupTestConfig(t, testConfig)

			tmpHome := filepath.Dir(configPath)
			ocfpDir := filepath.Join(tmpHome, ".ocfp")
			err := os.MkdirAll(ocfpDir, 0755)
			require.NoError(t, err)

			t.Setenv("OCFP_HOME", ocfpDir)

			expectedConfigPath := filepath.Join(ocfpDir, "config.yml")
			err = os.Rename(configPath, expectedConfigPath)
			require.NoError(t, err)

			mockSSH := &mockSSHClient{}
			log := newTestLogger()

			manager := &Manager{
				config: &config.Config{
					Name: "test-bloc",
				},
				sshClient: mockSSH,
				log:       log,
			}

			// Execute
			ctx := context.Background()
			err = manager.copyOCFPConfig(ctx)
			require.NoError(t, err)

			// Verify
			transferredContent := mockSSH.transferredFiles["/home/testuser/.ocfp/config.yml"]
			var transferredConfig config.ConfigFile
			err = yaml.Unmarshal([]byte(transferredContent), &transferredConfig)
			require.NoError(t, err)

			assert.Equal(t, tc.debug, transferredConfig.Debug, "Debug should match")
			assert.Equal(t, tc.verbose, transferredConfig.Verbose, "Verbose should match")
		})
	}
}

func TestManager_copyOCFPConfig_ErrorsOnMissingFile(t *testing.T) {
	// Setup: No config file at expected paths
	tmpDir := t.TempDir()

	t.Setenv("OCFP_HOME", filepath.Join(tmpDir, ".ocfp"))

	// Ensure working directory doesn't have config either
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	mockSSH := &mockSSHClient{}
	log := newTestLogger()

	manager := &Manager{
		config: &config.Config{
			Name: "test-bloc",
		},
		sshClient: mockSSH,
		log:       log,
	}

	// Execute
	ctx := context.Background()
	err := manager.copyOCFPConfig(ctx)

	// Verify
	require.Error(t, err, "Should return error when config file not found")
	assert.Equal(t, ErrOCFPConfigurationFileNotFound, err, "Should return specific error")
}

func TestManager_copyOCFPConfig_AlternativeConfigPath(t *testing.T) {
	// Setup: Config in alternative location (config/config.yml)
	testConfig := &config.ConfigFile{
		Debug:   false,
		Verbose: true,
		Blocs: map[string]*config.Config{
			"test-bloc": {
				Name:     "test-bloc",
				Provider: "stackit",
			},
		},
	}

	tmpDir := t.TempDir()

	// Create config in config/config.yml
	configDir := filepath.Join(tmpDir, "config")
	err := os.MkdirAll(configDir, 0755)
	require.NoError(t, err)

	configPath := filepath.Join(configDir, "config.yml")
	yamlBytes, err := yaml.Marshal(testConfig)
	require.NoError(t, err)

	err = os.WriteFile(configPath, yamlBytes, 0600)
	require.NoError(t, err)

	// Set OCFP_HOME to nonexistent location so config.yml won't be found there
	t.Setenv("OCFP_HOME", "/nonexistent/.ocfp")

	// Change to tmpDir so config/config.yml is found
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	mockSSH := &mockSSHClient{}
	log := newTestLogger()

	manager := &Manager{
		config: &config.Config{
			Name: "test-bloc",
		},
		sshClient: mockSSH,
		log:       log,
	}

	// Execute
	ctx := context.Background()
	err = manager.copyOCFPConfig(ctx)

	// Verify
	require.NoError(t, err, "Should succeed with alternative config path")
	assert.Contains(t, mockSSH.transferredFiles, "/home/testuser/.ocfp/config.yml")

	transferredContent := mockSSH.transferredFiles["/home/testuser/.ocfp/config.yml"]
	var transferredConfig config.ConfigFile
	err = yaml.Unmarshal([]byte(transferredContent), &transferredConfig)
	require.NoError(t, err)

	assert.Len(t, transferredConfig.Blocs, 1)
	assert.Contains(t, transferredConfig.Blocs, "test-bloc")
}

// boolPtr returns a pointer to b, for the *bool config fields that use nil
// to mean "unset" (tailscale.enabled, cloudflare.enabled).
func boolPtr(b bool) *bool {
	return &b
}

// TestManager_createFilteredConfig_CarriesGlobalTailscale pins the exact
// failure that broke `ocfp init bastion` at the vault_populate phase: the
// synced config dropped the global tailscale section, so the bastion's own
// load of that file failed ValidateIngress with "provider tailscale requires
// tailscale.enabled: true". The assertion is the full load chain, not just
// field equality, because that chain is what runs on the bastion.
func TestManager_createFilteredConfig_CarriesGlobalTailscale(t *testing.T) {
	sourceConfig := &config.ConfigFile{
		PVE: &config.PVEDefaults{Username: "root@pam", Password: "pve-password"},
		Tailscale: &config.TailscaleConfig{
			Enabled: boolPtr(true),
			AuthKey: "tskey-auth-test",
		},
		Cloudflare: &config.CloudflareConfig{
			Zone:     "example.com",
			APIToken: "cf-test-token",
		},
		Blocs: map[string]*config.Config{
			"test-bloc": {
				Name:     "test-bloc",
				Provider: "pve",
				Ingress:  &config.IngressConfig{Provider: config.IngressProviderTailscale},
			},
		},
	}

	manager := &Manager{
		config: &config.Config{Name: "test-bloc"},
		log:    newTestLogger(),
	}

	filteredConfig, err := manager.createFilteredConfig(sourceConfig, "test-config.yml")
	require.NoError(t, err)

	require.NotNil(t, filteredConfig.Tailscale, "global tailscale section must reach the bastion")
	assert.True(t, config.TailscaleEnabled(filteredConfig.Tailscale), "tailscale.enabled must survive filtering")

	// Round-trip through YAML and the real loader, exactly as the bastion does.
	syncedPath := filepath.Join(t.TempDir(), "config.yml")
	yamlBytes, err := yaml.Marshal(filteredConfig)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(syncedPath, yamlBytes, 0600))

	syncedBloc, err := config.LoadWithParams(syncedPath, "test-bloc")
	require.NoError(t, err, "synced config must load on the bastion")
	require.NoError(t, config.ValidateIngress(syncedBloc), "synced config must pass ingress validation")
}

// TestManager_createFilteredConfig_CarriesAllTopLevelSections pins the
// property that broke here in the first place: every top-level ConfigFile
// section except Blocs must survive filtering. Adding a field to ConfigFile
// without populating it here fails this test, which is the point — a new
// field must not be able to silently vanish from the synced config.
func TestManager_createFilteredConfig_CarriesAllTopLevelSections(t *testing.T) {
	sourceConfig := &config.ConfigFile{
		Debug:      true,
		Verbose:    true,
		PVE:        &config.PVEDefaults{Username: "root@pam", TokenSecret: "pve-secret"},
		Tailscale:  &config.TailscaleConfig{Enabled: boolPtr(true), AuthKey: "tskey-auth-test"},
		Cloudflare: &config.CloudflareConfig{Enabled: boolPtr(true), Zone: "example.com"},
		Ingress:    &config.IngressConfig{Provider: config.IngressProviderTailscale},
		Blocs: map[string]*config.Config{
			"test-bloc":  {Name: "test-bloc", Provider: "pve"},
			"other-bloc": {Name: "other-bloc", Provider: "aws"},
		},
	}

	manager := &Manager{
		config: &config.Config{Name: "test-bloc"},
		log:    newTestLogger(),
	}

	filteredConfig, err := manager.createFilteredConfig(sourceConfig, "test-config.yml")
	require.NoError(t, err)

	sourceValue := reflect.ValueOf(*sourceConfig)
	filteredValue := reflect.ValueOf(*filteredConfig)

	for i := range sourceValue.NumField() {
		fieldName := sourceValue.Type().Field(i).Name
		if fieldName == "Blocs" {
			continue
		}

		require.False(t, sourceValue.Field(i).IsZero(),
			"field %s must be populated by this test so its carry-through is actually asserted", fieldName)
		assert.Equal(t, sourceValue.Field(i).Interface(), filteredValue.Field(i).Interface(),
			"field %s must be carried through to the synced config", fieldName)
	}

	// Blocs remains the one narrowed section.
	assert.Len(t, filteredConfig.Blocs, 1)
	assert.Contains(t, filteredConfig.Blocs, "test-bloc")
	assert.NotContains(t, filteredConfig.Blocs, "other-bloc")
}
