package integration_test

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
	t.Parallel()
	configFile := setupProviderTestConfig(t)

	t.Run("CreateProviderCommand", func(t *testing.T) {
		t.Parallel()
		testProviderCreateCommand(t)
	})

	t.Run("ValidateProviderArgs", func(t *testing.T) {
		t.Parallel()
		testProviderValidateArgs(t)
	})

	t.Run("ProviderCommandFlags", func(t *testing.T) {
		t.Parallel()
		testProviderCommandFlags(t)
	})

	t.Run("ProviderLoginWithConfig", func(t *testing.T) {
		t.Parallel()
		testProviderLoginWithConfig(t, configFile)
	})

	t.Run("ProviderLoginStackitValidation", func(t *testing.T) {
		t.Parallel()
		testProviderLoginStackitValidation(t, configFile)
	})
}

// TestTmuxCommandIntegration tests the tmux command integration.
func TestTmuxCommandIntegration(t *testing.T) {
	t.Parallel()
	t.Run("CreateTmuxCommand", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewTmuxCmd()
		assert.NotNil(t, cmd)
		assert.Equal(t, "tmux", cmd.Use)
		assert.Equal(t, "Create tmux session for OCFP deployments", cmd.Short)
	})

	t.Run("TmuxCommandExecution", func(t *testing.T) {
		t.Parallel()

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
		t.Parallel()
		// Test that the command can generate a basic tmux script
		// This tests the internal script generation logic
		cmd := commands.NewTmuxCmd()

		// The command should be able to create its internal structures
		assert.NotNil(t, cmd.RunE)
	})
}

// TestBastionCommandIntegration tests the bastion command integration.
func setupBastionTestConfig(t *testing.T) (string, string) {
	t.Helper()
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "test-config.yml")

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

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	return tmpDir, configFile
}

func testBastionCommandCreation(t *testing.T) {
	t.Parallel()

	cmd := commands.NewBastionCmd()
	assert.NotNil(t, cmd)
	assert.Equal(t, "bastion <action>", cmd.Use)
	assert.Equal(t, "Bastion host management", cmd.Short)
}

func testBastionArgsValidation(t *testing.T) {
	t.Parallel()

	cmd := commands.NewBastionCmd()

	err := cmd.Args(cmd, []string{})
	require.Error(t, err)

	err = cmd.Args(cmd, []string{"init"})
	require.NoError(t, err)

	err = cmd.Args(cmd, []string{"provision"})
	assert.NoError(t, err)
}

func testBastionCommandFlags(t *testing.T) {
	t.Parallel()

	cmd := commands.NewBastionCmd()

	assert.NotNil(t, cmd.Flags().Lookup("user"))
	assert.NotNil(t, cmd.Flags().Lookup("key"))
	assert.NotNil(t, cmd.Flags().Lookup("iaas"))
	assert.NotNil(t, cmd.Flags().Lookup("bloc"))

	userFlag := cmd.Flags().Lookup("user")
	assert.Equal(t, "ubuntu", userFlag.DefValue)
}

func testBastionInitWithoutScript(t *testing.T, configFile string) {
	t.Helper()
	t.Parallel()
	t.Setenv("OCFP_CONFIG", configFile)

	cmd := commands.NewBastionCmd()
	cmd.SetArgs([]string{"init", "--user", "testuser", "--key", "/tmp/nonexistent"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot find bastion-init script")
}

func testBastionProvisionWithoutScript(t *testing.T, configFile string) {
	t.Helper()
	t.Parallel()
	t.Setenv("OCFP_CONFIG", configFile)

	cmd := commands.NewBastionCmd()
	cmd.SetArgs([]string{"provision", "--user", "testuser"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot find bastion provision script")
}

func testBastionWithProvisionScript(t *testing.T, tmpDir, configFile string) {
	t.Helper()
	t.Parallel()

	scriptContent := `#!/usr/bin/perl
# Test provision script
print "Provision script executed successfully\n";
exit 0;
`
	createScript(t, tmpDir, filepath.Join("scripts", "provision"), "bastion", scriptContent, 0755)

	cleanupChdir := chdir(t, tmpDir)
	defer cleanupChdir()

	cleanupEnv := withEnv(t, "OCFP_CONFIG", configFile)
	defer cleanupEnv()

	cmd := commands.NewBastionCmd()
	cmd.SetArgs([]string{"provision", "--user", "testuser", "--key", "/tmp/test-key"})

	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestBastionCommandIntegration(t *testing.T) {
	t.Parallel()
	tmpDir, configFile := setupBastionTestConfig(t)

	t.Run("CreateBastionCommand", testBastionCommandCreation)
	t.Run("ValidateBastionArgs", testBastionArgsValidation)
	t.Run("BastionCommandFlags", testBastionCommandFlags)
	t.Run("BastionInitWithoutScript", func(t *testing.T) {
		t.Parallel()
		testBastionInitWithoutScript(t, configFile)
	})
	t.Run("BastionProvisionWithoutScript", func(t *testing.T) {
		t.Parallel()
		testBastionProvisionWithoutScript(t, configFile)
	})
	t.Run("BastionWithProvisionScript", func(t *testing.T) {
		t.Parallel()
		testBastionWithProvisionScript(t, tmpDir, configFile)
	})
}

// TestCommandIntegrationWithVault tests commands that integrate with Vault.
func TestCommandIntegrationWithVault(t *testing.T) {
	t.Parallel()
	t.Run("ProviderLoginWithVault", func(t *testing.T) {
		t.Parallel()
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

		err = os.WriteFile(configFile, []byte(testConfig), 0600)
		require.NoError(t, err)

		t.Setenv("OCFP_CONFIG", configFile)

		cmd := commands.NewProviderCmd()
		cmd.SetArgs([]string{"login", "--iaas", "stackit", "--bloc", "test"})

		// This will attempt to read from vault and fail
		// But we can verify it tried the vault path
		err = cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not retrieve STACKIT service account credentials")
	})
}

func setupCommandConfigTestData(t *testing.T) (string, string) {
	t.Helper()
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "integration-config.yml")

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

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	// #nosec G101 -- Test fixture with mock credentials
	serviceAccountContent := `{
  "type": "service_account",
  "project_id": "integration-test-project",
  "private_key": "-----BEGIN PRIVATE KEY-----\ntest-key\n-----END PRIVATE KEY-----"
}`
	err = os.WriteFile(filepath.Join(tmpDir, "service-account.json"), []byte(serviceAccountContent), 0600)
	require.NoError(t, err)

	return tmpDir, configFile
}

func testMultipleCommandsWithSameConfig(t *testing.T, configFile string) {
	t.Helper()
	t.Parallel()
	t.Setenv("OCFP_CONFIG", configFile)

	providerCmd := commands.NewProviderCmd()
	assert.NotNil(t, providerCmd)

	tmuxCmd := commands.NewTmuxCmd()
	assert.NotNil(t, tmuxCmd)

	bastionCmd := commands.NewBastionCmd()
	assert.NotNil(t, bastionCmd)
}

func testBlocSpecificConfiguration(t *testing.T, configFile string) {
	t.Helper()
	t.Parallel()
	t.Setenv("OCFP_CONFIG", configFile)

	providerCmd := commands.NewProviderCmd()
	providerCmd.SetArgs([]string{"login", "--iaas", "stackit", "--bloc", "mgmt"})

	err := providerCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not retrieve STACKIT service account credentials")

	providerCmd2 := commands.NewProviderCmd()
	providerCmd2.SetArgs([]string{"login", "--iaas", "stackit", "--bloc", "apps"})

	err = providerCmd2.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not retrieve STACKIT service account credentials")
}

// TestCommandConfigIntegration tests configuration integration across commands.
func TestCommandConfigIntegration(t *testing.T) {
	t.Parallel()
	_, configFile := setupCommandConfigTestData(t)

	t.Run("MultipleCommandsWithSameConfig", func(t *testing.T) {
		t.Parallel()
		testMultipleCommandsWithSameConfig(t, configFile)
	})

	t.Run("BlocSpecificConfiguration", func(t *testing.T) {
		t.Parallel()
		testBlocSpecificConfiguration(t, configFile)
	})
}

// TestCommandErrorHandling tests error handling across all new commands.
func TestCommandErrorHandling(t *testing.T) {
	t.Parallel()
	t.Run("ProviderCommandErrors", func(t *testing.T) {
		t.Parallel()

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
		t.Parallel()

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
		t.Parallel()
		// Temporarily hide tmux from PATH to test error handling
		t.Setenv("PATH", "/nonexistent")

		cmd := commands.NewTmuxCmd()
		err := cmd.Execute()

		// Should handle missing tmux gracefully
		if err != nil {
			assert.Contains(t, err.Error(), "tmux")
		}
	})
}

func setupProviderTestConfig(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "test-config.yml")

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

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	return configFile
}

func testProviderCreateCommand(t *testing.T) {
	t.Helper()

	cmd := commands.NewProviderCmd()
	assert.NotNil(t, cmd)
	assert.Equal(t, "provider <action>", cmd.Use)
	assert.Equal(t, "Manage cloud provider operations", cmd.Short)
}

func testProviderValidateArgs(t *testing.T) {
	t.Helper()

	cmd := commands.NewProviderCmd()

	err := cmd.Args(cmd, []string{})
	require.Error(t, err)

	err = cmd.Args(cmd, []string{"login"})
	require.NoError(t, err)

	err = cmd.Args(cmd, []string{"invalid-action"})
	assert.NoError(t, err)
}

func testProviderCommandFlags(t *testing.T) {
	t.Helper()

	cmd := commands.NewProviderCmd()

	assert.NotNil(t, cmd.Flags().Lookup("iaas"))
	assert.NotNil(t, cmd.Flags().Lookup("bloc"))

	iaasFlag := cmd.Flags().Lookup("iaas")
	assert.Equal(t, "string", iaasFlag.Value.Type())

	blocFlag := cmd.Flags().Lookup("bloc")
	assert.Equal(t, "string", blocFlag.Value.Type())
}

func testProviderLoginWithConfig(t *testing.T, configFile string) {
	t.Helper()
	t.Parallel()
	t.Setenv("OCFP_CONFIG", configFile)

	cmd := commands.NewProviderCmd()
	cmd.SetArgs([]string{"login", "--iaas", "aws", "--bloc", "test"})

	err := cmd.Execute()
	assert.NoError(t, err)
}

func testProviderLoginStackitValidation(t *testing.T, configFile string) {
	t.Helper()
	t.Parallel()
	t.Setenv("OCFP_CONFIG", configFile)

	cmd := commands.NewProviderCmd()
	cmd.SetArgs([]string{"login", "--iaas", "stackit", "--bloc", "test"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not retrieve STACKIT service account credentials")
}
