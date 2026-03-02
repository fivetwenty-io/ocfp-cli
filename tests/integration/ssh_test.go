package integration_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/commands"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSSHCommand(t *testing.T) {
	t.Parallel()
	// Create test config directory
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "test-config.yml")

	// Create test config
	testConfig := `
provider: stackit
environment: test
ssh_key_storage_dir: ` + tmpDir + `
stackit:
  project_id: test-project
  api_key: test-key
  api_endpoint: https://api.stackit.cloud
environments:
  test:
    provider: stackit
    region: eu-de-1
    bastion:
      instance_name: test-bastion
      floating_ip: 10.0.0.1
`

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	// Create test SSH key
	keyFile := filepath.Join(tmpDir, "test-key")
	err = os.WriteFile(keyFile, []byte("test-key-content"), 0600)
	require.NoError(t, err)

	// Test SSH command creation
	t.Run("CreateCommand", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()
		assert.NotNil(t, cmd)
		assert.Equal(t, "ssh [target] [command...]", cmd.Use)
	})

	t.Run("ValidateArgs", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()

		// Test with no args (should be OK, uses default)
		err := cmd.Args(cmd, []string{})
		require.NoError(t, err)

		// Test with valid args
		err = cmd.Args(cmd, []string{"test-host"})
		assert.NoError(t, err)
	})

	t.Run("FlagParsing", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()

		// Test flag existence
		assert.NotNil(t, cmd.Flags().Lookup("key"))
		assert.NotNil(t, cmd.Flags().Lookup("user"))
		assert.NotNil(t, cmd.Flags().Lookup("ssh-options"))
	})
}

func TestSCPCommand(t *testing.T) {
	t.Parallel()
	t.Run("CreateCommand", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSCPCmd()
		assert.NotNil(t, cmd)
		assert.Equal(t, "scp <source> <destination>", cmd.Use)
	})

	t.Run("ValidateArgs", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSCPCmd()

		// Test with no args (should fail)
		err := cmd.Args(cmd, []string{})
		require.Error(t, err)

		// Test with one arg (should fail)
		err = cmd.Args(cmd, []string{"source"})
		require.Error(t, err)

		// Test with valid args
		err = cmd.Args(cmd, []string{"source", "dest"})
		assert.NoError(t, err)
	})

	t.Run("FlagParsing", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSCPCmd()

		// Test flag existence
		assert.NotNil(t, cmd.Flags().Lookup("key"))
		assert.NotNil(t, cmd.Flags().Lookup("user"))
		assert.NotNil(t, cmd.Flags().Lookup("recursive"))
		assert.NotNil(t, cmd.Flags().Lookup("scp-options"))
	})
}

func TestRsyncCommand(t *testing.T) {
	t.Parallel()
	t.Run("CreateCommand", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewRSyncCmd()
		assert.NotNil(t, cmd)
		assert.Equal(t, "rsync <source> <destination>", cmd.Use)
	})

	t.Run("ValidateArgs", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewRSyncCmd()

		// Test with no args (should fail)
		err := cmd.Args(cmd, []string{})
		require.Error(t, err)

		// Test with one arg (should fail)
		err = cmd.Args(cmd, []string{"source"})
		require.Error(t, err)

		// Test with valid args
		err = cmd.Args(cmd, []string{"source", "dest"})
		assert.NoError(t, err)
	})

	t.Run("FlagParsing", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewRSyncCmd()

		// Test flag existence
		assert.NotNil(t, cmd.Flags().Lookup("key"))
		assert.NotNil(t, cmd.Flags().Lookup("user"))
		assert.NotNil(t, cmd.Flags().Lookup("archive"))
		assert.NotNil(t, cmd.Flags().Lookup("compress"))
		assert.NotNil(t, cmd.Flags().Lookup("delete"))
		assert.NotNil(t, cmd.Flags().Lookup("exclude"))
		assert.NotNil(t, cmd.Flags().Lookup("include"))
		assert.NotNil(t, cmd.Flags().Lookup("rsync-options"))
	})
}
