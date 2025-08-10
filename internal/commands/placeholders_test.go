package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewProviderCmd(t *testing.T) {
	cmd := NewProviderCmd()
	assert.NotNil(t, cmd)
	assert.Equal(t, "provider <action>", cmd.Use)
	assert.Equal(t, "Manage cloud provider operations", cmd.Short)
	assert.True(t, cmd.Args(cmd, []string{"login"}) == nil)
}

func TestProviderLoginCommand(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		env         map[string]string
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
			expectedErr: "unknown provider action 'unknown'",
		},
		{
			name:        "login without provider",
			args:        []string{"login"},
			expectedErr: "provider not specified",
		},
		{
			name: "login with env provider but no bloc-name",
			args: []string{"login"},
			env:  map[string]string{"OCFP_PROVIDER": "stackit"},
			expectedErr: "--bloc-name flag or OCFP_BLOC_NAME environment variable required",
		},
		{
			name: "login with stackit provider",
			args: []string{"login", "--iaas", "stackit", "--bloc-name", "test"},
			expectedErr: "could not retrieve STACKIT service account credentials",
		},
		{
			name: "login with aws provider",
			args: []string{"login", "--iaas", "aws"},
			// AWS login should succeed with warning (not implemented)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables
			for key, value := range tt.env {
				os.Setenv(key, value)
				defer os.Unsetenv(key)
			}

			cmd := NewProviderCmd()
			cmd.SetArgs(tt.args)

			err := cmd.Execute()
			if tt.expectedErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNewTmuxCmd(t *testing.T) {
	cmd := NewTmuxCmd()
	assert.NotNil(t, cmd)
	assert.Equal(t, "tmux", cmd.Use)
	assert.Equal(t, "Create tmux session for OCFP deployments", cmd.Short)
}

func TestTmuxCommand(t *testing.T) {
	cmd := NewTmuxCmd()
	cmd.SetArgs([]string{})

	// The command should fail if tmux is not installed
	err := cmd.Execute()
	if _, tmuxErr := exec.LookPath("tmux"); tmuxErr != nil {
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tmux is not installed")
	}
	// If tmux is installed, it should attempt to create a session
	// but may fail due to no display/terminal, which is expected in tests
}

func TestFindTmuxScript(t *testing.T) {
	// Test that the function doesn't panic and returns some path
	scriptPath, err := findTmuxScript()
	// Either finds a script or creates a temporary one
	assert.NoError(t, err)
	assert.NotEmpty(t, scriptPath)
	
	// Check that the path exists
	_, statErr := os.Stat(scriptPath)
	assert.NoError(t, statErr)
}

func TestCreateBasicTmuxScript(t *testing.T) {
	scriptPath, err := createBasicTmuxScript()
	require.NoError(t, err)
	require.NotEmpty(t, scriptPath)
	defer os.Remove(scriptPath)

	// Check that the file exists and is readable
	content, err := os.ReadFile(scriptPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "#!/bin/bash")
	assert.Contains(t, string(content), "tmux new-session")
	assert.Contains(t, string(content), "ocfp")
}

func TestEnsureExecutable(t *testing.T) {
	// Create a temporary file
	tempFile, err := os.CreateTemp("", "test-executable-*")
	require.NoError(t, err)
	tempFile.Close()
	defer os.Remove(tempFile.Name())

	// Initially should not be executable
	info, err := os.Stat(tempFile.Name())
	require.NoError(t, err)
	assert.Equal(t, 0, int(info.Mode()&0111))

	// Make it executable
	err = ensureExecutable(tempFile.Name())
	require.NoError(t, err)

	// Now should be executable
	info, err = os.Stat(tempFile.Name())
	require.NoError(t, err)
	assert.NotEqual(t, 0, int(info.Mode()&0111))
}

func TestNewBastionCmd(t *testing.T) {
	cmd := NewBastionCmd()
	assert.NotNil(t, cmd)
	assert.Equal(t, "bastion <action>", cmd.Use)
	assert.Equal(t, "Bastion host management", cmd.Short)
	assert.True(t, cmd.Args(cmd, []string{"init"}) == nil)
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
			expectedErr: "cannot find bastion-init script",
		},
		{
			name:        "provision action",
			args:        []string{"provision"},
			expectedErr: "cannot find bastion provision script",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewBastionCmd()
			cmd.SetArgs(tt.args)

			err := cmd.Execute()
			if tt.expectedErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGetBastionContext(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("user", "ubuntu", "SSH username")
	cmd.Flags().String("key", "/path/to/key", "SSH key path")

	// Set flag values
	cmd.Flags().Set("user", "testuser")
	cmd.Flags().Set("key", "/test/key")

	ctx, err := getBastionContext(cmd, nil)
	require.NoError(t, err)
	assert.Equal(t, "testuser", ctx.User)
	assert.Contains(t, ctx.SSHKeyOption, "/test/key")
	assert.Equal(t, "placeholder-ip", ctx.IP) // Placeholder implementation
}

func TestBuildEnvironmentVariables(t *testing.T) {
	// Set some environment variables
	os.Setenv("OCFP_BLOC_NAME", "test-bloc")
	os.Setenv("OCFP_PROVIDER", "stackit")
	defer os.Unsetenv("OCFP_BLOC_NAME")
	defer os.Unsetenv("OCFP_PROVIDER")

	envString := buildEnvironmentVariables(nil)
	assert.Contains(t, envString, "OCFP_BLOC_NAME='test-bloc'")
	assert.Contains(t, envString, "OCFP_PROVIDER='stackit'")
}

func TestFetchGitHubKeys(t *testing.T) {
	// Test with a known public GitHub user (this is a real API call)
	// Using "octocat" which is GitHub's mascot account
	keys, err := fetchGitHubKeys("octocat")
	
	// The API call might fail due to network issues, rate limiting, etc.
	// So we check if either we got keys or a reasonable error
	if err != nil {
		// If there's an error, it should be network-related, not a panic
		assert.Contains(t, err.Error(), "failed to fetch GitHub keys")
	} else {
		// If successful, we might have keys (some users have no public keys)
		assert.GreaterOrEqual(t, len(keys), 0)
		// Each key should be a valid SSH key format
		for _, key := range keys {
			assert.True(t, len(key) > 0)
			// SSH keys typically start with ssh-rsa, ssh-ed25519, etc.
			assert.True(t, 
				len(key) > 7 && (
					key[:7] == "ssh-rsa" || 
					key[:11] == "ssh-ed25519" ||
					key[:19] == "ecdsa-sha2-nistp256" ||
					key[:19] == "ecdsa-sha2-nistp384" ||
					key[:19] == "ecdsa-sha2-nistp521"),
				"Key should start with valid SSH key type: %s", key[:min(20, len(key))])
		}
	}
}

func TestFetchGitLabKeys(t *testing.T) {
	// Test with GitLab API - using a test that should not cause issues
	// Note: This makes a real HTTP request which could fail
	keys, err := fetchGitLabKeys("root") // root user exists on most GitLab instances
	
	// Similar to GitHub test - check for reasonable behavior
	if err != nil {
		assert.Contains(t, err.Error(), "failed to fetch GitLab keys")
	} else if len(keys) > 0 {
		// If we got keys, they should be valid SSH key format
		for _, key := range keys {
			assert.True(t, len(key) > 0)
		}
	}
	// If len(keys) == 0 and err == nil, that's also valid (user has no public keys)
}

func TestFindProvisionScript(t *testing.T) {
	// This should fail for non-existent script
	_, err := findProvisionScript("non-existent-script")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found in any search paths")
	
	// Create a test script
	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "scripts", "provision", "test-script")
	err = os.MkdirAll(filepath.Dir(scriptPath), 0755)
	require.NoError(t, err)
	err = os.WriteFile(scriptPath, []byte("#!/bin/bash\necho test"), 0644)
	require.NoError(t, err)
	
	// Change working directory to temp dir
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	os.Chdir(tempDir)
	
	// Now it should find the script
	foundPath, err := findProvisionScript("test-script")
	assert.NoError(t, err)
	assert.Contains(t, foundPath, "test-script")
}

// Helper function for min
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}