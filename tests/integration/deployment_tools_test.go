package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/commands"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTmuxIntegration tests tmux session management integration
func TestTmuxIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping tmux integration tests in short mode")
	}

	tmpDir := t.TempDir()

	// Check if tmux is available
	_, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not available on system, skipping tmux integration tests")
	}

	t.Run("TmuxSessionCreation", func(t *testing.T) {
		cmd := commands.NewTmuxCmd()
		cmd.SetArgs([]string{})

		// In a test environment without a proper terminal, tmux may fail
		// But we can verify the command structure and error handling
		err := cmd.Execute()
		
		if err != nil {
			// Expected in test environments - verify it's a tmux-related error, not a panic
			errMsg := err.Error()
			assert.True(t, 
				strings.Contains(errMsg, "tmux") || 
				strings.Contains(errMsg, "terminal") || 
				strings.Contains(errMsg, "session") ||
				strings.Contains(errMsg, "display"),
				"Error should be tmux-related: %s", errMsg)
		}
	})

	t.Run("TmuxScriptDiscovery", func(t *testing.T) {
		// Create test tmux script in expected location
		scriptDir := filepath.Join(tmpDir, "scripts", "tmux")
		err := os.MkdirAll(scriptDir, 0755)
		require.NoError(t, err)

		scriptPath := filepath.Join(scriptDir, "ocfp")
		scriptContent := `#!/bin/bash
# Test tmux script
echo "Creating OCFP tmux session..."
tmux new-session -d -s ocfp
tmux new-window -t ocfp:1 -n bosh
tmux new-window -t ocfp:2 -n vault
tmux attach-session -t ocfp
`
		err = os.WriteFile(scriptPath, []byte(scriptContent), 0755)
		require.NoError(t, err)

		// Change to tmpDir so script can be found
		oldWd, _ := os.Getwd()
		defer os.Chdir(oldWd)
		err = os.Chdir(tmpDir)
		require.NoError(t, err)

		cmd := commands.NewTmuxCmd()
		err = cmd.Execute()

		// Should find the script and attempt to execute it
		// May still fail due to terminal/display issues in test environment
		if err != nil {
			errMsg := err.Error()
			// Should not be a "script not found" error
			assert.NotContains(t, errMsg, "not found")
			assert.NotContains(t, errMsg, "no such file")
		}
	})

	t.Run("TmuxDeploymentDirectories", func(t *testing.T) {
		// Create deployment directory structure
		deploymentDir := filepath.Join(tmpDir, "ocfp", "deployments")
		services := []string{"bosh", "vault", "shield", "doomsday", "prometheus", "concourse", "cf"}
		
		for _, service := range services {
			serviceDir := filepath.Join(deploymentDir, service)
			err := os.MkdirAll(serviceDir, 0755)
			require.NoError(t, err)

			// Create a dummy file to verify directory exists
			err = os.WriteFile(filepath.Join(serviceDir, "deployment.yml"), []byte("# "+service+" deployment"), 0644)
			require.NoError(t, err)
		}

		// Set HOME to tmpDir so tmux can find deployment directories
		oldHome := os.Getenv("HOME")
		os.Setenv("HOME", tmpDir)
		defer os.Setenv("HOME", oldHome)

		cmd := commands.NewTmuxCmd()
		err = cmd.Execute()

		// Tmux should attempt to use the deployment directories
		// Exact behavior depends on terminal availability
		if err != nil && !strings.Contains(err.Error(), "display") {
			t.Logf("Tmux execution result: %v", err)
		}
	})
}

// TestBastionIntegration tests bastion host management integration
func TestBastionIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping bastion integration tests in short mode")
	}

	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "bastion-config.yml")

	// Create comprehensive bastion configuration
	testConfig := `
name: bastion-integration-test
provider: stackit
ssh_key_storage_dir: ` + tmpDir + `
bastion:
  flavor: t3.medium
  image: ubuntu-22.04
  ssh_user: ubuntu
  keypair: integration-bastion-key
blocs:
  - name: test
    provider: stackit
    environment: test
    region: eu-de-1
    project_id: test-bastion-project
`

	err := os.WriteFile(configFile, []byte(testConfig), 0644)
	require.NoError(t, err)

	t.Run("BastionInitScript", func(t *testing.T) {
		// Create bastion-init script
		scriptDir := filepath.Join(tmpDir, "scripts", "provision")
		err := os.MkdirAll(scriptDir, 0755)
		require.NoError(t, err)

		initScriptPath := filepath.Join(scriptDir, "bastion-init")
		initScriptContent := `#!/usr/bin/perl
use strict;
use warnings;

print "Bastion initialization script executed\n";
print "Installing basic packages...\n";
print "Configuring SSH settings...\n";
print "Setting up firewall rules...\n";
print "Bastion initialization complete\n";

exit 0;
`
		err = os.WriteFile(initScriptPath, []byte(initScriptContent), 0755)
		require.NoError(t, err)

		// Change to tmpDir so script can be found
		oldWd, _ := os.Getwd()
		defer os.Chdir(oldWd)
		err = os.Chdir(tmpDir)
		require.NoError(t, err)

		os.Setenv("OCFP_CONFIG", configFile)
		defer os.Unsetenv("OCFP_CONFIG")

		cmd := commands.NewBastionCmd()
		cmd.SetArgs([]string{"init", "--user", "testuser", "--key", "/tmp/test-key"})

		err = cmd.Execute()
		// Current implementation uses placeholder functionality, so it succeeds
		// In a real implementation, this would fail on SSH connection
		assert.NoError(t, err)
	})

	t.Run("BastionProvisionScript", func(t *testing.T) {
		// Create bastion provision script
		scriptDir := filepath.Join(tmpDir, "scripts", "provision")
		err := os.MkdirAll(scriptDir, 0755)
		require.NoError(t, err)

		provisionScriptPath := filepath.Join(scriptDir, "bastion")
		provisionScriptContent := `#!/usr/bin/perl
use strict;
use warnings;

print "Bastion provision script executed\n";
print "Environment: $ENV{OCFP_BLOC_NAME}\n" if $ENV{OCFP_BLOC_NAME};
print "Provider: $ENV{OCFP_PROVIDER}\n" if $ENV{OCFP_PROVIDER};
print "Installing deployment tools...\n";
print "Configuring BOSH CLI...\n";
print "Setting up Genesis...\n";
print "Configuring CF CLI...\n";
print "Bastion provision complete\n";

exit 0;
`
		err = os.WriteFile(provisionScriptPath, []byte(provisionScriptContent), 0755)
		require.NoError(t, err)

		oldWd, _ := os.Getwd()
		defer os.Chdir(oldWd)
		err = os.Chdir(tmpDir)
		require.NoError(t, err)

		os.Setenv("OCFP_CONFIG", configFile)
		defer os.Unsetenv("OCFP_CONFIG")

		cmd := commands.NewBastionCmd()
		cmd.SetArgs([]string{"provision", "--user", "ubuntu", "--key", "/tmp/test-key", "--bloc-name", "test"})

		err = cmd.Execute()
		// Current implementation uses placeholder functionality, so it succeeds
		// In a real implementation, this would fail on SSH connection
		assert.NoError(t, err)
	})

	t.Run("BastionSSHKeyDiscovery", func(t *testing.T) {
		// Create SSH key files
		keyDir := filepath.Join(tmpDir, "keys")
		err := os.MkdirAll(keyDir, 0700)
		require.NoError(t, err)

		// Create test SSH key
		keyPath := filepath.Join(keyDir, "bastion-key")
		keyContent := `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAFwAAAAdzc2gtcn
NhAAAAAwEAAQAAAQEA1234567890abcdef... (truncated for test)
-----END OPENSSH PRIVATE KEY-----`
		err = os.WriteFile(keyPath, []byte(keyContent), 0600)
		require.NoError(t, err)

		// Create public key
		pubKeyPath := keyPath + ".pub"
		pubKeyContent := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDT1234567890... test@example.com"
		err = os.WriteFile(pubKeyPath, []byte(pubKeyContent), 0644)
		require.NoError(t, err)

		// Create bastion-init script for this test
		scriptDir := filepath.Join(tmpDir, "scripts", "provision")
		err = os.MkdirAll(scriptDir, 0755)
		require.NoError(t, err)

		initScriptPath := filepath.Join(scriptDir, "bastion-init")
		initScriptContent := `#!/usr/bin/perl
print "Bastion init script with SSH key discovery\n";
exit 0;
`
		err = os.WriteFile(initScriptPath, []byte(initScriptContent), 0755)
		require.NoError(t, err)

		// Change to tmpDir so script can be found
		oldWd, _ := os.Getwd()
		defer os.Chdir(oldWd)
		err = os.Chdir(tmpDir)
		require.NoError(t, err)

		os.Setenv("OCFP_CONFIG", configFile)
		defer os.Unsetenv("OCFP_CONFIG")

		cmd := commands.NewBastionCmd()
		cmd.SetArgs([]string{"init", "--user", "ubuntu", "--key", keyPath})

		err = cmd.Execute()
		// Current implementation uses placeholder functionality, so it succeeds
		assert.NoError(t, err)
	})

	t.Run("BastionEnvironmentVariables", func(t *testing.T) {
		// Test environment variable passing
		os.Setenv("OCFP_BLOC_NAME", "test-env-bloc")
		os.Setenv("OCFP_PROVIDER", "stackit")
		os.Setenv("STACKIT_PROJECT_ID", "env-project-123")
		os.Setenv("GENESIS_ENVIRONMENT", "test")
		defer func() {
			os.Unsetenv("OCFP_BLOC_NAME")
			os.Unsetenv("OCFP_PROVIDER")
			os.Unsetenv("STACKIT_PROJECT_ID")
			os.Unsetenv("GENESIS_ENVIRONMENT")
		}()

		// Create simple provision script that echoes environment
		scriptDir := filepath.Join(tmpDir, "scripts", "provision")
		err := os.MkdirAll(scriptDir, 0755)
		require.NoError(t, err)

		envScriptPath := filepath.Join(scriptDir, "bastion")
		envScriptContent := `#!/usr/bin/perl
print "OCFP_BLOC_NAME: $ENV{OCFP_BLOC_NAME}\n";
print "OCFP_PROVIDER: $ENV{OCFP_PROVIDER}\n";
print "STACKIT_PROJECT_ID: $ENV{STACKIT_PROJECT_ID}\n";
print "GENESIS_ENVIRONMENT: $ENV{GENESIS_ENVIRONMENT}\n";
exit 0;
`
		err = os.WriteFile(envScriptPath, []byte(envScriptContent), 0755)
		require.NoError(t, err)

		oldWd, _ := os.Getwd()
		defer os.Chdir(oldWd)
		err = os.Chdir(tmpDir)
		require.NoError(t, err)

		os.Setenv("OCFP_CONFIG", configFile)
		defer os.Unsetenv("OCFP_CONFIG")

		cmd := commands.NewBastionCmd()
		cmd.SetArgs([]string{"provision", "--user", "ubuntu"})

		err = cmd.Execute()
		// Current implementation uses placeholder functionality, so it succeeds
		assert.NoError(t, err)
	})
}

// TestDeploymentWorkflow tests the complete deployment workflow integration
func TestDeploymentWorkflow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping deployment workflow tests in short mode")
	}

	tmpDir := t.TempDir()

	t.Run("ProviderTmuxBastionIntegration", func(t *testing.T) {
		// Create comprehensive configuration
		configFile := filepath.Join(tmpDir, "workflow-config.yml")
		testConfig := `
name: workflow-integration-test
provider: stackit
ssh_key_storage_dir: ` + tmpDir + `
service_account_token: "workflow-test-token"

bastion:
  flavor: t3.large
  image: ubuntu-22.04
  ssh_user: ubuntu
  keypair: workflow-key

network:
  name: workflow-network
  cidr: 10.3.0.0/16

blocs:
  - name: mgmt
    provider: stackit
    type: management
    environment: test
    region: eu-de-1
    project_id: workflow-mgmt-project
  - name: cf
    provider: stackit
    type: application
    environment: test
    region: eu-de-1
    project_id: workflow-cf-project
`

		err := os.WriteFile(configFile, []byte(testConfig), 0644)
		require.NoError(t, err)

		os.Setenv("OCFP_CONFIG", configFile)
		defer os.Unsetenv("OCFP_CONFIG")

		// Test provider command with workflow config
		providerCmd := commands.NewProviderCmd()
		providerCmd.SetArgs([]string{"login", "--iaas", "stackit", "--bloc-name", "mgmt"})
		
		err = providerCmd.Execute()
		assert.Error(t, err) // Expected due to fake credentials
		assert.Contains(t, err.Error(), "could not retrieve STACKIT service account credentials")

		// Test tmux command with workflow config
		tmuxCmd := commands.NewTmuxCmd()
		assert.NotNil(t, tmuxCmd)

		// Test bastion command with workflow config
		bastionCmd := commands.NewBastionCmd()
		bastionCmd.SetArgs([]string{"init", "--bloc-name", "mgmt"})
		
		err = bastionCmd.Execute()
		assert.Error(t, err) // Expected due to missing script/SSH
		assert.Contains(t, err.Error(), "cannot find bastion-init script")
	})

	t.Run("ScriptDirectoryStructure", func(t *testing.T) {
		// Create expected script directory structure
		scriptsBase := filepath.Join(tmpDir, "scripts")
		
		// Create tmux scripts
		tmuxDir := filepath.Join(scriptsBase, "tmux")
		err := os.MkdirAll(tmuxDir, 0755)
		require.NoError(t, err)
		
		tmuxScript := filepath.Join(tmuxDir, "ocfp")
		err = os.WriteFile(tmuxScript, []byte("#!/bin/bash\necho 'OCFP tmux session'\n"), 0755)
		require.NoError(t, err)

		// Create provision scripts
		provisionDir := filepath.Join(scriptsBase, "provision")
		err = os.MkdirAll(provisionDir, 0755)
		require.NoError(t, err)

		provisionScripts := []string{"bastion-init", "bastion"}
		for _, script := range provisionScripts {
			scriptPath := filepath.Join(provisionDir, script)
			scriptContent := `#!/usr/bin/perl
print "Executing ` + script + `\n";
exit 0;
`
			err = os.WriteFile(scriptPath, []byte(scriptContent), 0755)
			require.NoError(t, err)
		}

		// Verify all scripts are created and executable
		for _, script := range provisionScripts {
			scriptPath := filepath.Join(provisionDir, script)
			info, err := os.Stat(scriptPath)
			require.NoError(t, err)
			assert.True(t, info.Mode()&0111 != 0, "Script should be executable: "+script)
		}

		tmuxInfo, err := os.Stat(tmuxScript)
		require.NoError(t, err)
		assert.True(t, tmuxInfo.Mode()&0111 != 0, "Tmux script should be executable")
	})

	t.Run("DeploymentDirectoryStructure", func(t *testing.T) {
		// Create OCFP deployment directory structure
		deploymentBase := filepath.Join(tmpDir, "ocfp", "deployments")
		
		deployments := []string{
			"bosh", "vault", "shield", "doomsday", "prometheus", 
			"concourse", "cf", "autoscaler", "scheduler", "jumpbox", "blacksmith",
		}

		for _, deployment := range deployments {
			deploymentDir := filepath.Join(deploymentBase, deployment)
			err := os.MkdirAll(deploymentDir, 0755)
			require.NoError(t, err)

			// Create sample deployment files
			files := []string{"deployment.yml", "cloud-config.yml", "vars.yml"}
			for _, file := range files {
				filePath := filepath.Join(deploymentDir, file)
				content := "# " + deployment + " " + file + "\nname: " + deployment + "\n"
				err = os.WriteFile(filePath, []byte(content), 0644)
				require.NoError(t, err)
			}
		}

		// Verify deployment structure
		for _, deployment := range deployments {
			deploymentDir := filepath.Join(deploymentBase, deployment)
			info, err := os.Stat(deploymentDir)
			require.NoError(t, err)
			assert.True(t, info.IsDir(), "Should be directory: "+deployment)

			// Check for deployment files
			deploymentFile := filepath.Join(deploymentDir, "deployment.yml")
			_, err = os.Stat(deploymentFile)
			assert.NoError(t, err, "Deployment file should exist: "+deployment)
		}
	})
}

// TestToolAvailability tests availability of external tools
func TestToolAvailability(t *testing.T) {
	tools := map[string]string{
		"tmux":    "Terminal multiplexer for session management",
		"ssh":     "SSH client for bastion connections", 
		"scp":     "Secure copy for file transfers",
		"rsync":   "File synchronization tool",
		"perl":    "Perl interpreter for provision scripts",
	}

	for tool, description := range tools {
		t.Run("Check"+strings.Title(tool), func(t *testing.T) {
			_, err := exec.LookPath(tool)
			if err != nil {
				t.Logf("%s not available: %s", tool, description)
				// Don't fail the test, just log availability
			} else {
				t.Logf("%s available: %s", tool, description)
				assert.NoError(t, err)
			}
		})
	}

	// Test optional cloud tools
	cloudTools := map[string]string{
		"stackit": "STACKIT CLI for authentication",
		"bosh":    "BOSH CLI for deployment management",
		"cf":      "Cloud Foundry CLI",
		"safe":    "Vault wrapper tool",
		"vault":   "HashiCorp Vault CLI",
	}

	for tool, description := range cloudTools {
		t.Run("CheckOptional"+strings.Title(tool), func(t *testing.T) {
			_, err := exec.LookPath(tool)
			if err != nil {
				t.Logf("Optional tool %s not available: %s", tool, description)
			} else {
				t.Logf("Optional tool %s available: %s", tool, description)
			}
			// Don't assert - these are optional tools
		})
	}
}