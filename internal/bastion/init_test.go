package bastion

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// mockSSHClient implements the SSH client interface for testing.
type mockSSHClient struct {
	transferredFiles map[string]string // remote path -> local content
	transferError    error
}

func (m *mockSSHClient) Connect(ctx context.Context) error {
	return nil
}

func (m *mockSSHClient) TransferFile(ctx context.Context, local, remote string, opts ssh.TransferOptions) error {
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

func (m *mockSSHClient) ExecuteCommand(ctx context.Context, cmd string) (*ssh.CommandResult, error) {
	return &ssh.CommandResult{ExitCode: 0, Stdout: "", Stderr: ""}, nil
}

func (m *mockSSHClient) CreateTunnel(ctx context.Context, localPort, remotePort int) error {
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

	// Set HOME to temp dir so config is found
	origHome := os.Getenv("HOME")
	tmpHome := filepath.Dir(configPath)
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Move config to expected location
	ocfpDir := filepath.Join(tmpHome, ".ocfp")
	err := os.MkdirAll(ocfpDir, 0755)
	require.NoError(t, err)

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
	assert.Contains(t, mockSSH.transferredFiles, "~/.ocfp/config.yml", "Should transfer to remote path")

	// Parse transferred content
	transferredContent := mockSSH.transferredFiles["~/.ocfp/config.yml"]
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

	// Set HOME to temp dir
	origHome := os.Getenv("HOME")
	tmpHome := filepath.Dir(configPath)
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Move config to expected location
	ocfpDir := filepath.Join(tmpHome, ".ocfp")
	err := os.MkdirAll(ocfpDir, 0755)
	require.NoError(t, err)

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

			origHome := os.Getenv("HOME")
			tmpHome := filepath.Dir(configPath)
			os.Setenv("HOME", tmpHome)
			defer os.Setenv("HOME", origHome)

			ocfpDir := filepath.Join(tmpHome, ".ocfp")
			err := os.MkdirAll(ocfpDir, 0755)
			require.NoError(t, err)

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
			transferredContent := mockSSH.transferredFiles["~/.ocfp/config.yml"]
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

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

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

	// Set HOME to different location so ~/.ocfp/config.yml doesn't exist
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", "/nonexistent")
	defer os.Setenv("HOME", origHome)

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
	assert.Contains(t, mockSSH.transferredFiles, "~/.ocfp/config.yml")

	transferredContent := mockSSH.transferredFiles["~/.ocfp/config.yml"]
	var transferredConfig config.ConfigFile
	err = yaml.Unmarshal([]byte(transferredContent), &transferredConfig)
	require.NoError(t, err)

	assert.Len(t, transferredConfig.Blocs, 1)
	assert.Contains(t, transferredConfig.Blocs, "test-bloc")
}
