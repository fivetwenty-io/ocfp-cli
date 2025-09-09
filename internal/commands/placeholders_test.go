package commands_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/commands"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewProviderCmd(t *testing.T) {
	t.Parallel()

	cmd := commands.NewProviderCmd()
	assert.NotNil(t, cmd)
	assert.Equal(t, "provider <action>", cmd.Use)
	assert.Equal(t, "Manage cloud provider operations", cmd.Short)
	assert.NoError(t, cmd.Args(cmd, []string{"login"}))
}

func TestProviderLoginCommand(t *testing.T) {
	t.Parallel()

	tests := getProviderLoginTestCases()
	for _, testCase := range tests {
		testData := testCase
		t.Run(testData.name, func(t *testing.T) {
			t.Parallel()
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
			expectedErr: "--bloc flag or OCFP_BLOC_NAME environment variable required",
		},
		{
			name:        "login with stackit provider",
			args:        []string{"login", "--iaas", "stackit", "--bloc", "test"},
			env:         nil,
			expectedErr: "could not retrieve STACKIT service account credentials",
		},
		{
			name:        "login with aws provider",
			args:        []string{"login", "--iaas", "aws"},
			env:         nil,
			expectedErr: "",
		},
	}
}

func runProviderLoginTest(t *testing.T, testData providerLoginTestCase) {
	t.Helper()

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
	t.Parallel()

	cmd := commands.NewTmuxCmd()
	assert.NotNil(t, cmd)
	assert.Equal(t, "tmux", cmd.Use)
	assert.Equal(t, "Create tmux session for OCFP deployments", cmd.Short)
}

func TestTmuxCommand(t *testing.T) {
	t.Parallel()

	cmd := commands.NewTmuxCmd()
	cmd.SetArgs([]string{})

	// The command should fail if tmux is not installed
	err := cmd.Execute()

	_, tmuxErr := exec.LookPath("tmux")
	if tmuxErr != nil {
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tmux is not installed")
	}
	// If tmux is installed, it should attempt to create a session
	// but may fail due to no display/terminal, which is expected in tests
}

func TestFindTmuxScript(t *testing.T) {
	t.Parallel()
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
	t.Parallel()

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
	t.Parallel()
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
	t.Parallel()

	cmd := commands.NewBastionCmd()
	assert.NotNil(t, cmd)
	assert.Equal(t, "bastion <action>", cmd.Use)
	assert.Equal(t, "Bastion host management", cmd.Short)
	assert.NoError(t, cmd.Args(cmd, []string{"init"}))
}

func TestBastionCommand(t *testing.T) {
	t.Parallel()

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
			expectedErr: "cannot find bastion-init script",
		},
		{
			name:        "provision action",
			args:        []string{"provision"},
			expectedErr: "cannot find bastion provision script",
		},
	}

	for _, testCase := range tests {
		testData := testCase
		t.Run(testData.name, func(t *testing.T) {
			t.Parallel()

			cmd := commands.NewBastionCmd()
			cmd.SetArgs(testData.args)

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

func TestGetBastionContext(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{
		Use:                    "",
		Aliases:                nil,
		SuggestFor:             nil,
		Short:                  "",
		GroupID:                "",
		Long:                   "",
		Example:                "",
		ValidArgs:              nil,
		ValidArgsFunction:      nil,
		Args:                   nil,
		ArgAliases:             nil,
		BashCompletionFunction: "",
		Deprecated:             "",
		Annotations:            nil,
		Version:                "",
		PersistentPreRun:       nil,
		PersistentPreRunE:      nil,
		PreRun:                 nil,
		PreRunE:                nil,
		Run:                    nil,
		RunE:                   nil,
		PostRun:                nil,
		PostRunE:               nil,
		PersistentPostRun:      nil,
		PersistentPostRunE:     nil,
		FParseErrWhitelist:     cobra.FParseErrWhitelist{UnknownFlags: false},
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd:   false,
			DisableNoDescFlag:   false,
			DisableDescriptions: false,
			HiddenDefaultCmd:    false,
		},
		TraverseChildren:           false,
		Hidden:                     false,
		SilenceErrors:              false,
		SilenceUsage:               false,
		DisableFlagParsing:         false,
		DisableAutoGenTag:          false,
		DisableFlagsInUseLine:      false,
		DisableSuggestions:         false,
		SuggestionsMinimumDistance: 0,
	}
	cmd.Flags().String("user", "ubuntu", "SSH username")
	cmd.Flags().String("key", "/path/to/key", "SSH key path")

	// Set flag values
	_ = cmd.Flags().Set("user", "testuser")
	_ = cmd.Flags().Set("key", "/test/key")

	ctx, err := commands.GetBastionContext(cmd, nil)
	require.NoError(t, err)
	assert.Equal(t, "testuser", ctx.User)
	assert.Contains(t, ctx.SSHKeyOption, "/test/key")
	assert.Equal(t, "placeholder-ip", ctx.IP) // Placeholder implementation
}

func TestBuildEnvironmentVariables(t *testing.T) {
	t.Parallel()
	// Set some environment variables
	t.Setenv("OCFP_BLOC_NAME", "test-bloc")
	t.Setenv("OCFP_PROVIDER", "stackit")

	envString := commands.BuildEnvironmentVariables(nil)
	assert.Contains(t, envString, "OCFP_BLOC_NAME='test-bloc'")
	assert.Contains(t, envString, "OCFP_PROVIDER='stackit'")
}

func TestFetchGitHubKeys(t *testing.T) {
	t.Parallel()
	// Test with a known public GitHub user (this is a real API call)
	// Using "octocat" which is GitHub's mascot account
	keys, err := commands.FetchGitHubKeys(context.Background(), "octocat")

	// The API call might fail due to network issues, rate limiting, etc.
	// So we check if either we got keys or a reasonable error
	if err != nil {
		// If there's an error, it should be network-related, not a panic
		assert.Contains(t, err.Error(), "failed to fetch GitHub keys")
	} else {
		// If successful, we might have keys (some users have no public keys)
		// len(keys) >= 0 is always true for slices, removing useless assertion
		// Each key should be a valid SSH key format
		for _, key := range keys {
			assert.NotEmpty(t, key)
			// SSH keys typically start with ssh-rsa, ssh-ed25519, etc.
			assert.True(t,
				len(key) > 7 && (key[:7] == "ssh-rsa" ||
					key[:11] == "ssh-ed25519" ||
					key[:19] == "ecdsa-sha2-nistp256" ||
					key[:19] == "ecdsa-sha2-nistp384" ||
					key[:19] == "ecdsa-sha2-nistp521"),
				"Key should start with valid SSH key type: %s", key[:minimum(20, len(key))])
		}
	}
}

func TestFetchGitLabKeys(t *testing.T) {
	t.Parallel()
	// Test with GitLab API - using a test that should not cause issues
	// Note: This makes a real HTTP request which could fail
	keys, err := commands.FetchGitLabKeys(context.Background(), "root") // root user exists on most GitLab instances

	// Similar to GitHub test - check for reasonable behavior
	if err != nil {
		assert.Contains(t, err.Error(), "failed to fetch GitLab keys")
	} else if len(keys) > 0 {
		// If we got keys, they should be valid SSH key format
		for _, key := range keys {
			assert.NotEmpty(t, key)
		}
	}
	// If len(keys) == 0 and err == nil, that's also valid (user has no public keys)
}

func TestFindProvisionScript(t *testing.T) {
	t.Parallel()
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

// Helper function for minimum.
func minimum(a, b int) int {
	if a < b {
		return a
	}

	return b
}
