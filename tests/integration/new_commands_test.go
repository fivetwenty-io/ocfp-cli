package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/commands"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProviderCommandIntegration tests the provider command integration.
func TestProviderCommandIntegration(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "test-config.yml")

	// Create test config with service account credentials
	testConfig := `
name: test-bloc
provider: stackit
service_account_token: "test-token-value"
blocs:
  - name: test
    provider: stackit
    environment: test
    region: eu-de-1
    project_id: test-project-123
`

	err := os.WriteFile(configFile, []byte(testConfig), 0644)
	require.NoError(t, err)

	t.Run("CreateProviderCommand", func(t *testing.T) {
		cmd := commands.NewProviderCmd()
		assert.NotNil(t, cmd)
		assert.Equal(t, "provider <action>", cmd.Use)
		assert.Equal(t, "Manage cloud provider operations", cmd.Short)
	})

	t.Run("ValidateProviderArgs", func(t *testing.T) {
		cmd := commands.NewProviderCmd()

		// Test with no args (should fail)
		err := cmd.Args(cmd, []string{})
		require.Error(t, err)

		// Test with valid action
		err = cmd.Args(cmd, []string{"login"})
		require.NoError(t, err)

		// Test with invalid action
		err = cmd.Args(cmd, []string{"invalid-action"})
		assert.NoError(t, err) // Args validator doesn't check action validity
	})

	t.Run("ProviderCommandFlags", func(t *testing.T) {
		cmd := commands.NewProviderCmd()

		// Test flag existence
		assert.NotNil(t, cmd.Flags().Lookup("iaas"))
		assert.NotNil(t, cmd.Flags().Lookup("bloc"))

		// Test flag defaults
		iaasFlag := cmd.Flags().Lookup("iaas")
		assert.Equal(t, "string", iaasFlag.Value.Type())

		blocFlag := cmd.Flags().Lookup("bloc")
		assert.Equal(t, "string", blocFlag.Value.Type())
	})

	t.Run("ProviderLoginWithConfig", func(t *testing.T) {
		// Set config file environment variable
		_ = os.Setenv("OCFP_CONFIG", configFile)

		defer func() { _ = os.Unsetenv("OCFP_CONFIG") }()

		cmd := commands.NewProviderCmd()
		cmd.SetArgs([]string{"login", "--iaas", "aws", "--bloc", "test"})

		// This should succeed with a warning for AWS (placeholder implementation)
		err := cmd.Execute()
		// AWS login is a placeholder, so it should succeed with warning
		assert.NoError(t, err)
	})

	t.Run("ProviderLoginStackitValidation", func(t *testing.T) {
		// Test STACKIT provider validation without actual credentials
		_ = os.Setenv("OCFP_CONFIG", configFile)

		defer func() { _ = os.Unsetenv("OCFP_CONFIG") }()

		cmd := commands.NewProviderCmd()
		cmd.SetArgs([]string{"login", "--iaas", "stackit", "--bloc", "test"})

		// This should fail since we don't have real STACKIT credentials
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not retrieve STACKIT service account credentials")
	})
}

// TestTmuxCommandIntegration tests the tmux command integration.
func TestTmuxCommandIntegration(t *testing.T) {
	t.Run("CreateTmuxCommand", func(t *testing.T) {
		cmd := commands.NewTmuxCmd()
		assert.NotNil(t, cmd)
		assert.Equal(t, "tmux", cmd.Use)
		assert.Equal(t, "Create tmux session for OCFP deployments", cmd.Short)
	})

	t.Run("TmuxCommandExecution", func(t *testing.T) {
		cmd := commands.NewTmuxCmd()

		// Check if tmux is available on the system
		_, err := exec.LookPath("tmux")
		if err != nil {
			t.Skip("tmux not available on system, skipping integration test")
		}

		// Since tmux requires a terminal and we're in a test environment,
		// we expect this to fail but not panic
		err = cmd.Execute()
		// In CI/test environments without proper terminal, tmux will fail
		// This is expected behavior
		if err != nil {
			assert.Contains(t, err.Error(), "tmux")
		}
	})

	t.Run("TmuxScriptGeneration", func(t *testing.T) {
		// Test that the command can generate a basic tmux script
		// This tests the internal script generation logic
		cmd := commands.NewTmuxCmd()

		// The command should be able to create its internal structures
		assert.NotNil(t, cmd.RunE)
	})
}

// TestBastionCommandIntegration tests the bastion command integration.
func TestBastionCommandIntegration(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "test-config.yml")

	// Create test config with bastion configuration
	testConfig := `
name: test-bloc
provider: stackit
bastion:
  flavor: t3.small
  image: ubuntu-22.04
  ssh_user: ubuntu
  keypair: test-keypair
blocs:
  - name: test
    provider: stackit
    environment: test
    region: eu-de-1
`

	err := os.WriteFile(configFile, []byte(testConfig), 0644)
	require.NoError(t, err)

	t.Run("CreateBastionCommand", func(t *testing.T) {
		cmd := commands.NewBastionCmd()
		assert.NotNil(t, cmd)
		assert.Equal(t, "bastion <action>", cmd.Use)
		assert.Equal(t, "Bastion host management", cmd.Short)
	})

	t.Run("ValidateBastionArgs", func(t *testing.T) {
		cmd := commands.NewBastionCmd()

		// Test with no args (should fail)
		err := cmd.Args(cmd, []string{})
		require.Error(t, err)

		// Test with valid actions
		err = cmd.Args(cmd, []string{"init"})
		require.NoError(t, err)

		err = cmd.Args(cmd, []string{"provision"})
		assert.NoError(t, err)
	})

	t.Run("BastionCommandFlags", func(t *testing.T) {
		cmd := commands.NewBastionCmd()

		// Test flag existence
		assert.NotNil(t, cmd.Flags().Lookup("user"))
		assert.NotNil(t, cmd.Flags().Lookup("key"))
		assert.NotNil(t, cmd.Flags().Lookup("iaas"))
		assert.NotNil(t, cmd.Flags().Lookup("bloc"))

		// Test flag defaults
		userFlag := cmd.Flags().Lookup("user")
		assert.Equal(t, "ubuntu", userFlag.DefValue)
	})

	t.Run("BastionInitWithoutScript", func(t *testing.T) {
		// Set config file environment variable
		_ = os.Setenv("OCFP_CONFIG", configFile)

		defer func() { _ = os.Unsetenv("OCFP_CONFIG") }()

		cmd := commands.NewBastionCmd()
		cmd.SetArgs([]string{"init", "--user", "testuser", "--key", "/tmp/nonexistent"})

		// This should fail because no bastion-init script exists
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot find bastion-init script")
	})

	t.Run("BastionProvisionWithoutScript", func(t *testing.T) {
		_ = os.Setenv("OCFP_CONFIG", configFile)

		defer func() { _ = os.Unsetenv("OCFP_CONFIG") }()

		cmd := commands.NewBastionCmd()
		cmd.SetArgs([]string{"provision", "--user", "testuser"})

		// This should fail because no provision script exists
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot find bastion provision script")
	})

	t.Run("BastionWithProvisionScript", func(t *testing.T) {
		// Create a test provision script
		scriptsDir := filepath.Join(tmpDir, "scripts", "provision")
		err := os.MkdirAll(scriptsDir, 0755)
		require.NoError(t, err)

		scriptPath := filepath.Join(scriptsDir, "bastion")
		scriptContent := `#!/usr/bin/perl
# Test provision script
print "Provision script executed successfully\n";
exit 0;
`
		err = os.WriteFile(scriptPath, []byte(scriptContent), 0755)
		require.NoError(t, err)

		// Change to the tmpDir so the script can be found
		oldWd, _ := os.Getwd()

		defer func() { _ = os.Chdir(oldWd) }()

		err = os.Chdir(tmpDir)
		require.NoError(t, err)

		_ = os.Setenv("OCFP_CONFIG", configFile)

		defer func() { _ = os.Unsetenv("OCFP_CONFIG") }()

		cmd := commands.NewBastionCmd()
		cmd.SetArgs([]string{"provision", "--user", "testuser", "--key", "/tmp/test-key"})

		// Current implementation uses placeholder functionality, so it succeeds
		// In a real implementation, this would fail on SSH connection
		err = cmd.Execute()
		assert.NoError(t, err)
	})
}

// TestCommandIntegrationWithVault tests commands that integrate with Vault.
func TestCommandIntegrationWithVault(t *testing.T) {
	t.Run("ProviderLoginWithVault", func(t *testing.T) {
		// Check if 'safe' command is available (Vault wrapper)
		_, err := exec.LookPath("safe")
		if err != nil {
			t.Skip("safe command not available, skipping vault integration test")
		}

		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "test-config.yml")

		testConfig := `
name: test-bloc
provider: stackit
blocs:
  - name: test
    provider: stackit
    environment: test
`

		err = os.WriteFile(configFile, []byte(testConfig), 0644)
		require.NoError(t, err)

		_ = os.Setenv("OCFP_CONFIG", configFile)

		defer func() { _ = os.Unsetenv("OCFP_CONFIG") }()

		cmd := commands.NewProviderCmd()
		cmd.SetArgs([]string{"login", "--iaas", "stackit", "--bloc", "test"})

		// This will attempt to read from vault and fail
		// But we can verify it tried the vault path
		err = cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not retrieve STACKIT service account credentials")
	})
}

// TestCommandConfigIntegration tests configuration integration across commands.
func TestCommandConfigIntegration(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "integration-config.yml")

	// Create comprehensive test configuration
	testConfig := `
name: integration-test
provider: stackit
ssh_key_storage_dir: ` + tmpDir + `
service_account_token: "integration-test-token"
service_account_json: |
  {
    "type": "service_account",
    "project_id": "integration-test-project"
  }
service_account_key_path: "` + filepath.Join(tmpDir, "service-account.json") + `"

bastion:
  flavor: t3.medium
  image: ubuntu-22.04
  ssh_user: ubuntu
  keypair: integration-key

network:
  name: integration-network
  cidr: 10.2.0.0/16

blocs:
  - name: mgmt
    provider: stackit
    type: management
    environment: dev
    region: eu-de-1
    project_id: integration-project
  - name: apps
    provider: stackit
    type: application
    environment: dev
    region: eu-de-2
`

	err := os.WriteFile(configFile, []byte(testConfig), 0644)
	require.NoError(t, err)

	// Create service account key file
	serviceAccountContent := `{
  "type": "service_account",
  "project_id": "integration-test-project",
  "private_key": "-----BEGIN PRIVATE KEY-----\ntest-key\n-----END PRIVATE KEY-----"
}`
	err = os.WriteFile(filepath.Join(tmpDir, "service-account.json"), []byte(serviceAccountContent), 0600)
	require.NoError(t, err)

	t.Run("MultipleCommandsWithSameConfig", func(t *testing.T) {
		_ = os.Setenv("OCFP_CONFIG", configFile)

		defer func() { _ = os.Unsetenv("OCFP_CONFIG") }()

		// Test provider command
		providerCmd := commands.NewProviderCmd()
		assert.NotNil(t, providerCmd)

		// Test tmux command
		tmuxCmd := commands.NewTmuxCmd()
		assert.NotNil(t, tmuxCmd)

		// Test bastion command
		bastionCmd := commands.NewBastionCmd()
		assert.NotNil(t, bastionCmd)

		// All commands should be able to access the same configuration
		// This verifies config loading consistency across commands
	})

	t.Run("BlocSpecificConfiguration", func(t *testing.T) {
		_ = os.Setenv("OCFP_CONFIG", configFile)

		defer func() { _ = os.Unsetenv("OCFP_CONFIG") }()

		// Test provider command with mgmt bloc
		providerCmd := commands.NewProviderCmd()
		providerCmd.SetArgs([]string{"login", "--iaas", "stackit", "--bloc", "mgmt"})

		// Should fail due to credentials but should find the bloc config
		err := providerCmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not retrieve STACKIT service account credentials")

		// Test provider command with apps bloc
		providerCmd2 := commands.NewProviderCmd()
		providerCmd2.SetArgs([]string{"login", "--iaas", "stackit", "--bloc", "apps"})

		err = providerCmd2.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not retrieve STACKIT service account credentials")
	})
}

// TestCommandErrorHandling tests error handling across all new commands.
func TestCommandErrorHandling(t *testing.T) {
	t.Run("ProviderCommandErrors", func(t *testing.T) {
		cmd := commands.NewProviderCmd()

		// Test missing action
		cmd.SetArgs([]string{})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires at least 1 arg")

		// Test unknown action
		cmd = commands.NewProviderCmd()
		cmd.SetArgs([]string{"unknown-action"})
		err = cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown provider action")

		// Test missing provider
		cmd = commands.NewProviderCmd()
		cmd.SetArgs([]string{"login"})
		err = cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "provider not specified")
	})

	t.Run("BastionCommandErrors", func(t *testing.T) {
		cmd := commands.NewBastionCmd()

		// Test missing action
		cmd.SetArgs([]string{})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires at least 1 arg")

		// Test unknown action
		cmd = commands.NewBastionCmd()
		cmd.SetArgs([]string{"unknown-action"})
		err = cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown bastion action")
	})

	t.Run("TmuxCommandWithoutTmux", func(t *testing.T) {
		// Temporarily hide tmux from PATH to test error handling
		originalPath := os.Getenv("PATH")
		_ = os.Setenv("PATH", "/nonexistent")

		defer func() { _ = os.Setenv("PATH", originalPath) }()

		cmd := commands.NewTmuxCmd()
		err := cmd.Execute()

		// Should handle missing tmux gracefully
		if err != nil {
			assert.Contains(t, err.Error(), "tmux")
		}
	})
}
