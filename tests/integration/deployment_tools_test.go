package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/commands"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// TestTmuxIntegration tests tmux session management integration.
func TestTmuxIntegration(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping tmux integration tests in short mode")
	}

	if !isTmuxAvailable() {
		t.Skip("tmux not available on system, skipping tmux integration tests")
	}

	tmpDir := t.TempDir()
	t.Run("TmuxSessionCreation", testTmuxSessionCreation)
	t.Run("TmuxScriptDiscovery", func(t *testing.T) {
		t.Parallel()
		testTmuxScriptDiscovery(t, tmpDir)
	})
	t.Run("TmuxDeploymentDirectories", func(t *testing.T) {
		t.Parallel()
		testTmuxDeploymentDirectories(t, tmpDir)
	})
}

func isTmuxAvailable() bool {
	_, err := exec.LookPath("tmux")

	return err == nil
}

func testTmuxSessionCreation(t *testing.T) {
	t.Parallel()

	cmd := commands.NewTmuxCmd()
	cmd.SetArgs([]string{})

	err := cmd.Execute()
	if err != nil {
		errMsg := err.Error()
		assert.True(t,
			strings.Contains(errMsg, "tmux") ||
				strings.Contains(errMsg, "terminal") ||
				strings.Contains(errMsg, "session") ||
				strings.Contains(errMsg, "display"),
			"Error should be tmux-related: %s", errMsg)
	}
}

func testTmuxScriptDiscovery(t *testing.T, tmpDir string) {
	t.Helper()

	scriptDir := filepath.Join(tmpDir, "scripts", "tmux")
	err := os.MkdirAll(scriptDir, 0750)
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
	err = os.WriteFile(scriptPath, []byte(scriptContent), 0600)
	require.NoError(t, err)

	t.Chdir(tmpDir)

	cmd := commands.NewTmuxCmd()

	err = cmd.Execute()
	if err != nil {
		errMsg := err.Error()
		assert.NotContains(t, errMsg, "not found")
		assert.NotContains(t, errMsg, "no such file")
	}
}

func testTmuxDeploymentDirectories(t *testing.T, tmpDir string) {
	t.Helper()

	deploymentDir := filepath.Join(tmpDir, "ocfp", "deployments")
	services := []string{"bosh", "vault", "shield", "doomsday", "prometheus", "concourse", "cf"}

	for _, service := range services {
		serviceDir := filepath.Join(deploymentDir, service)
		err := os.MkdirAll(serviceDir, 0750)
		require.NoError(t, err)

		err = os.WriteFile(filepath.Join(serviceDir, "deployment.yml"), []byte("# "+service+" deployment"), 0600)
		require.NoError(t, err)
	}

	t.Setenv("HOME", tmpDir)

	cmd := commands.NewTmuxCmd()
	err := cmd.Execute()

	if err != nil && !strings.Contains(err.Error(), "display") {
		t.Logf("Tmux execution result: %v", err)
	}
}

// TestBastionIntegration tests bastion host management integration.
func TestBastionIntegration(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping bastion integration tests in short mode")
	}

	tmpDir := t.TempDir()
	configFile := setupBastionConfig(t, tmpDir)

	t.Run("BastionInitScript", func(t *testing.T) {
		t.Parallel()
		testBastionInitScript(t, tmpDir, configFile)
	})

	t.Run("BastionProvisionScript", func(t *testing.T) {
		t.Parallel()
		testBastionProvisionScript(t, tmpDir, configFile)
	})

	t.Run("BastionSSHKeyDiscovery", func(t *testing.T) {
		t.Parallel()
		testBastionSSHKeyDiscovery(t, tmpDir, configFile)
	})

	t.Run("BastionEnvironmentVariables", func(t *testing.T) {
		t.Parallel()
		testBastionEnvironmentVariables(t, tmpDir, configFile)
	})
}

func setupBastionConfig(t *testing.T, tmpDir string) string {
	t.Helper()

	configFile := filepath.Join(tmpDir, "bastion-config.yml")
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
	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	return configFile
}

func testBastionInitScript(t *testing.T, tmpDir, configFile string) {
	t.Helper()
	t.Parallel()

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
	createScript(t, tmpDir, filepath.Join("scripts", "provision"), "bastion-init", initScriptContent, 0755)

	cleanupChdir := chdir(t, tmpDir)
	defer cleanupChdir()

	cleanupEnv := withEnv(t, "OCFP_CONFIG", configFile)
	defer cleanupEnv()

	cmd := commands.NewBastionCmd()
	cmd.SetArgs([]string{"init", "--user", "testuser", "--key", "/tmp/test-key"})

	err := cmd.Execute()
	assert.NoError(t, err)
}

func testBastionProvisionScript(t *testing.T, tmpDir, configFile string) {
	t.Helper()
	t.Parallel()

	scriptDir := filepath.Join(tmpDir, "scripts", "provision")
	err := os.MkdirAll(scriptDir, 0750)
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
	err = os.WriteFile(provisionScriptPath, []byte(provisionScriptContent), 0600)
	require.NoError(t, err)

	t.Chdir(tmpDir)
	t.Setenv("OCFP_CONFIG", configFile)

	cmd := commands.NewBastionCmd()
	cmd.SetArgs([]string{"provision", "--user", "ubuntu", "--key", "/tmp/test-key", "--bloc", "test"})

	err = cmd.Execute()
	assert.NoError(t, err)
}

func testBastionSSHKeyDiscovery(t *testing.T, tmpDir, configFile string) {
	t.Helper()
	t.Parallel()

	keyDir := filepath.Join(tmpDir, "keys")
	err := os.MkdirAll(keyDir, 0700)
	require.NoError(t, err)

	keyPath := filepath.Join(keyDir, "bastion-key")
	keyContent := `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAFwAAAAdzc2gtcn
NhAAAAAwEAAQAAAQEA1234567890abcdef... (truncated for test)
-----END OPENSSH PRIVATE KEY-----`
	err = os.WriteFile(keyPath, []byte(keyContent), 0600)
	require.NoError(t, err)

	pubKeyPath := keyPath + ".pub"
	pubKeyContent := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDT1234567890... test@example.com"
	err = os.WriteFile(pubKeyPath, []byte(pubKeyContent), 0600)
	require.NoError(t, err)

	scriptDir := filepath.Join(tmpDir, "scripts", "provision")
	err = os.MkdirAll(scriptDir, 0750)
	require.NoError(t, err)

	initScriptPath := filepath.Join(scriptDir, "bastion-init")
	initScriptContent := `#!/usr/bin/perl
print "Bastion init script with SSH key discovery\n";
exit 0;
`
	err = os.WriteFile(initScriptPath, []byte(initScriptContent), 0600)
	require.NoError(t, err)

	t.Chdir(tmpDir)
	t.Setenv("OCFP_CONFIG", configFile)

	cmd := commands.NewBastionCmd()
	cmd.SetArgs([]string{"init", "--user", "ubuntu", "--key", keyPath})

	err = cmd.Execute()
	assert.NoError(t, err)
}

func testBastionEnvironmentVariables(t *testing.T, tmpDir, configFile string) {
	t.Helper()
	t.Parallel()
	t.Setenv("OCFP_BLOC_NAME", "test-env-bloc")
	t.Setenv("OCFP_PROVIDER", "stackit")
	t.Setenv("STACKIT_PROJECT_ID", "env-project-123")
	t.Setenv("GENESIS_ENVIRONMENT", "test")

	scriptDir := filepath.Join(tmpDir, "scripts", "provision")
	err := os.MkdirAll(scriptDir, 0750)
	require.NoError(t, err)

	envScriptPath := filepath.Join(scriptDir, "bastion")
	envScriptContent := `#!/usr/bin/perl
print "OCFP_BLOC_NAME: $ENV{OCFP_BLOC_NAME}\n";
print "OCFP_PROVIDER: $ENV{OCFP_PROVIDER}\n";
print "STACKIT_PROJECT_ID: $ENV{STACKIT_PROJECT_ID}\n";
print "GENESIS_ENVIRONMENT: $ENV{GENESIS_ENVIRONMENT}\n";
exit 0;
`
	err = os.WriteFile(envScriptPath, []byte(envScriptContent), 0600)
	require.NoError(t, err)

	t.Chdir(tmpDir)
	t.Setenv("OCFP_CONFIG", configFile)

	cmd := commands.NewBastionCmd()
	cmd.SetArgs([]string{"provision", "--user", "ubuntu"})

	err = cmd.Execute()
	assert.NoError(t, err)
}

// TestDeploymentWorkflow tests the complete deployment workflow integration.
func TestDeploymentWorkflow(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping deployment workflow tests in short mode")
	}

	tmpDir := t.TempDir()

	t.Run("ProviderTmuxBastionIntegration", func(t *testing.T) {
		t.Parallel()
		testProviderTmuxBastionIntegration(t, tmpDir)
	})

	t.Run("ScriptDirectoryStructure", func(t *testing.T) {
		t.Parallel()
		testScriptDirectoryStructure(t, tmpDir)
	})

	t.Run("DeploymentDirectoryStructure", func(t *testing.T) {
		t.Parallel()
		testDeploymentDirectoryStructure(t, tmpDir)
	})
}

func testProviderTmuxBastionIntegration(t *testing.T, tmpDir string) {
	t.Helper()
	t.Parallel()

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

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	t.Setenv("OCFP_CONFIG", configFile)

	providerCmd := commands.NewProviderCmd()
	providerCmd.SetArgs([]string{"login", "--iaas", "stackit", "--bloc", "mgmt"})

	err = providerCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not retrieve STACKIT service account credentials")

	tmuxCmd := commands.NewTmuxCmd()
	assert.NotNil(t, tmuxCmd)

	bastionCmd := commands.NewBastionCmd()
	bastionCmd.SetArgs([]string{"init", "--bloc", "mgmt"})

	err = bastionCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot find bastion-init script")
}

func testScriptDirectoryStructure(t *testing.T, tmpDir string) {
	t.Helper()
	t.Parallel()

	scriptsBase := filepath.Join(tmpDir, "scripts")

	tmuxDir := filepath.Join(scriptsBase, "tmux")
	err := os.MkdirAll(tmuxDir, 0750)
	require.NoError(t, err)

	tmuxScript := filepath.Join(tmuxDir, "ocfp")
	err = os.WriteFile(tmuxScript, []byte("#!/bin/bash\necho 'OCFP tmux session'\n"), 0600)
	require.NoError(t, err)

	provisionDir := filepath.Join(scriptsBase, "provision")
	err = os.MkdirAll(provisionDir, 0750)
	require.NoError(t, err)

	provisionScripts := []string{"bastion-init", "bastion"}
	for _, script := range provisionScripts {
		scriptPath := filepath.Join(provisionDir, script)
		scriptContent := `#!/usr/bin/perl
print "Executing ` + script + `\n";
exit 0;
`
		err = os.WriteFile(scriptPath, []byte(scriptContent), 0600)
		require.NoError(t, err)
	}

	for _, script := range provisionScripts {
		scriptPath := filepath.Join(provisionDir, script)
		info, err := os.Stat(scriptPath)
		require.NoError(t, err)
		assert.NotEqual(t, 0, info.Mode()&0111, "Script should be executable: "+script)
	}

	tmuxInfo, err := os.Stat(tmuxScript)
	require.NoError(t, err)
	assert.NotEqual(t, 0, tmuxInfo.Mode()&0111, "Tmux script should be executable")
}

func testDeploymentDirectoryStructure(t *testing.T, tmpDir string) {
	t.Helper()
	t.Parallel()

	deploymentBase := filepath.Join(tmpDir, "ocfp", "deployments")

	deployments := []string{
		"bosh", "vault", "shield", "doomsday", "prometheus",
		"concourse", "cf", "autoscaler", "scheduler", "jumpbox", "blacksmith",
	}

	for _, deployment := range deployments {
		deploymentDir := filepath.Join(deploymentBase, deployment)
		err := os.MkdirAll(deploymentDir, 0750)
		require.NoError(t, err)

		files := []string{"deployment.yml", "cloud-config.yml", "vars.yml"}
		for _, file := range files {
			filePath := filepath.Join(deploymentDir, file)
			content := "# " + deployment + " " + file + "\nname: " + deployment + "\n"
			err = os.WriteFile(filePath, []byte(content), 0600)
			require.NoError(t, err)
		}
	}

	for _, deployment := range deployments {
		deploymentDir := filepath.Join(deploymentBase, deployment)
		info, err := os.Stat(deploymentDir)
		require.NoError(t, err)
		assert.True(t, info.IsDir(), "Should be directory: "+deployment)

		deploymentFile := filepath.Join(deploymentDir, "deployment.yml")
		_, err = os.Stat(deploymentFile)
		assert.NoError(t, err, "Deployment file should exist: "+deployment)
	}
}

// TestToolAvailability tests availability of external tools.
func TestToolAvailability(t *testing.T) {
	t.Parallel()

	tools := map[string]string{
		"tmux":  "Terminal multiplexer for session management",
		"ssh":   "SSH client for bastion connections",
		"scp":   "Secure copy for file transfers",
		"rsync": "File synchronization tool",
		"perl":  "Perl interpreter for provision scripts",
	}

	for tool, description := range tools {
		toolName, toolDesc := tool, description
		t.Run("Check"+cases.Title(language.English).String(toolName), func(t *testing.T) {
			t.Parallel()

			_, err := exec.LookPath(toolName)
			if err != nil {
				t.Logf("%s not available: %s", toolName, toolDesc)
				// Don't fail the test, just log availability
			} else {
				t.Logf("%s available: %s", toolName, toolDesc)
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
		toolName, toolDesc := tool, description
		t.Run("CheckOptional"+cases.Title(language.English).String(toolName), func(t *testing.T) {
			t.Parallel()

			_, err := exec.LookPath(toolName)
			if err != nil {
				t.Logf("Optional tool %s not available: %s", toolName, toolDesc)
			} else {
				t.Logf("Optional tool %s available: %s", toolName, toolDesc)
			}
			// Don't assert - these are optional tools
		})
	}
}
