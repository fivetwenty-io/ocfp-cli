package commands_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/commands"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewProviderCmd(t *testing.T) {
	cmd := commands.NewProviderCmd()
	assert.NotNil(t, cmd)
	assert.Equal(t, "provider <action>", cmd.Use)
	assert.Equal(t, "Manage cloud provider operations", cmd.Short)
	assert.NoError(t, cmd.Args(cmd, []string{"login"}))
}

func TestProviderLoginCommand(t *testing.T) {
	tests := getProviderLoginTestCases()
	for _, testCase := range tests {
		testData := testCase
		t.Run(testData.name, func(t *testing.T) {
			runProviderLoginTest(t, testData)
		})
	}
}

type providerLoginTestCase struct {
	name        string
	args        []string
	env         map[string]string
	expectedErr string
}

func getProviderLoginTestCases() []providerLoginTestCase {
	return []providerLoginTestCase{
		{
			name:        "missing action",
			args:        []string{},
			env:         nil,
			expectedErr: "requires at least 1 arg(s), only received 0",
		},
		{
			name:        "unknown action",
			args:        []string{"unknown"},
			env:         nil,
			expectedErr: "unknown provider action 'unknown'",
		},
		{
			name:        "login without provider",
			args:        []string{"login"},
			env:         nil,
			expectedErr: "provider not specified",
		},
		{
			name:        "login with env provider but no bloc",
			args:        []string{"login"},
			env:         map[string]string{"OCFP_PROVIDER": "stackit"},
			expectedErr: "--bloc flag or OCFP_BLOC environment variable required",
		},
		{
			name:        "login with stackit provider",
			args:        []string{"login", "--iaas", "stackit", "--bloc", "test"},
			env:         nil,
			expectedErr: "could not retrieve STACKIT service account credentials",
		},
		{
			name:        "login with aws provider",
			args:        []string{"login", "--iaas", "aws", "--bloc", "test"},
			env:         nil,
			expectedErr: "AWS credentials not found in config or vault",
		},
	}
}

func runProviderLoginTest(t *testing.T, testData providerLoginTestCase) {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)

	for key, value := range testData.env {
		t.Setenv(key, value)
	}

	cmd := commands.NewProviderCmd()
	cmd.SetArgs(testData.args)

	err := cmd.Execute()
	if testData.expectedErr != "" {
		require.Error(t, err)
		assert.Contains(t, err.Error(), testData.expectedErr)
	} else {
		require.NoError(t, err)
	}
}

func TestNewTmuxCmd(t *testing.T) {
	cmd := commands.NewTmuxCmd()
	assert.NotNil(t, cmd)
	assert.Equal(t, "tmux", cmd.Use)
	assert.Equal(t, "Create tmux session for OCFP deployments", cmd.Short)
}

func TestTmuxCommand(t *testing.T) {
	// Point PATH at an empty dir so tmux is never found: the not-installed
	// branch is the only one this test can assert deterministically (a real
	// tmux would spawn a session against whatever terminal runs the tests).
	t.Setenv("PATH", t.TempDir())

	cmd := commands.NewTmuxCmd()
	cmd.SetArgs([]string{})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tmux is not installed")
}

func TestFindTmuxScript(t *testing.T) {
	// Test that the function doesn't panic and returns some path
	scriptPath, err := commands.FindTmuxScript()
	// Either finds a script or creates a temporary one
	require.NoError(t, err)
	assert.NotEmpty(t, scriptPath)

	// Check that the path exists
	_, statErr := os.Stat(scriptPath)
	assert.NoError(t, statErr)
}

func TestCreateBasicTmuxScript(t *testing.T) {
	scriptPath, err := commands.CreateBasicTmuxScript()
	require.NoError(t, err)
	require.NotEmpty(t, scriptPath)

	defer func() { _ = os.Remove(scriptPath) }()

	// Check that the file exists and is readable
	// #nosec G304 - scriptPath comes from commands.CreateBasicTmuxScript() which uses os.CreateTemp()
	content, err := os.ReadFile(scriptPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "#!/bin/bash")
	assert.Contains(t, string(content), "tmux new-session")
	assert.Contains(t, string(content), "ocfp")
}

func TestEnsureExecutable(t *testing.T) {
	// Create a temporary file
	tempFile, err := os.CreateTemp(t.TempDir(), "test-executable-*")
	require.NoError(t, err)

	_ = tempFile.Close()

	defer func() { _ = os.Remove(tempFile.Name()) }()

	// Initially should not be executable
	info, err := os.Stat(tempFile.Name())
	require.NoError(t, err)
	assert.Equal(t, 0, int(info.Mode()&0111))

	// Make it executable
	err = commands.EnsureExecutable(tempFile.Name())
	require.NoError(t, err)

	// Now should be executable
	info, err = os.Stat(tempFile.Name())
	require.NoError(t, err)
	assert.NotEqual(t, 0, int(info.Mode()&0111))
}

func TestNewBastionCmd(t *testing.T) {
	cmd := commands.NewBastionCmd()
	assert.NotNil(t, cmd)
	assert.Equal(t, "bastion <action>", cmd.Use)
	assert.Equal(t, "Bastion host management", cmd.Short)
	assert.NoError(t, cmd.Args(cmd, []string{"init"}))
}

func TestNewBastionCmdDryRunFlag(t *testing.T) {
	cmd := commands.NewBastionCmd()

	flag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, flag, "bastion command must register a --dry-run flag")
	assert.Equal(t, "false", flag.DefValue)
	assert.Equal(t, "bool", flag.Value.Type())
}

func TestBastionCommand(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		expectedErr string
	}{
		{
			name:        "missing action",
			args:        []string{},
			expectedErr: "requires at least 1 arg(s), only received 0",
		},
		{
			name:        "unknown action",
			args:        []string{"unknown"},
			expectedErr: "unknown bastion action: unknown",
		},
		{
			name:        "init action",
			args:        []string{"init"},
			expectedErr: "provider validation failed",
		},
		{
			name: "provision action",
			args: []string{"provision"},
			// The bastion script is embedded in the binary, so lookup always
			// succeeds; without a real bastion the placeholder scp fails next.
			expectedErr: "scp command failed",
		},
	}

	for _, testCase := range tests {
		testData := testCase
		t.Run(testData.name, func(t *testing.T) {
			viper.Reset()
			t.Cleanup(viper.Reset)
			t.Setenv("OCFP_BLOC", "test-bloc")
			t.Setenv("OCFP_PROVIDER", "stackit")

			tempConfigDir := t.TempDir()
			configPath := filepath.Join(tempConfigDir, "config.yml")
			configContents := []byte("blocs:\n  test-bloc:\n    provider: stackit\n    region: eu01\n")
			require.NoError(t, os.WriteFile(configPath, configContents, 0o600))
			viper.Set("config", configPath)

			cmd := commands.NewBastionCmd()
			args := append([]string{}, testData.args...)
			args = append(args, "--bloc", "test-bloc")
			cmd.SetArgs(args)

			err := cmd.Execute()
			if testData.expectedErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), testData.expectedErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestBuildEnvironmentVariables(t *testing.T) {
	// Set some environment variables
	t.Setenv("OCFP_BLOC", "test-bloc")
	t.Setenv("OCFP_PROVIDER", "stackit")

	envString := commands.BuildEnvironmentVariables(nil, nil)
	assert.Contains(t, envString, "OCFP_BLOC='test-bloc'")
	assert.Contains(t, envString, "OCFP_PROVIDER='stackit'")
}

func TestFindProvisionScript(t *testing.T) {
	// This should fail for non-existent script
	_, err := commands.FindProvisionScript("non-existent-script")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in any search paths")

	// Create a test script
	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "scripts", "provision", "test-script")
	err = os.MkdirAll(filepath.Dir(scriptPath), 0750)
	require.NoError(t, err)
	err = os.WriteFile(scriptPath, []byte("#!/bin/bash\necho test"), 0600)
	require.NoError(t, err)

	// Change working directory to temp dir
	t.Chdir(tempDir)

	// Now it should find the script
	foundPath, err := commands.FindProvisionScript("test-script")
	require.NoError(t, err)
	assert.Contains(t, foundPath, "test-script")
}

// TestFindProvisionScript_EmbeddedFallback proves the shipped scripts resolve
// with no source checkout on disk (installed-binary scenario).
func TestFindProvisionScript_EmbeddedFallback(t *testing.T) {
	t.Chdir(t.TempDir()) // no scripts/provision below cwd

	for _, name := range []string{"artifacts", "bastion"} {
		path, err := commands.FindProvisionScript(name)
		require.NoError(t, err, "embedded script %s must resolve", name)

		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Positive(t, info.Size(), "materialized script %s must not be empty", name)
		assert.NotZero(t, info.Mode()&0o100, "materialized script %s must be executable", name)
	}
}
