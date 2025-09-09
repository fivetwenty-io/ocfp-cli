package integration_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/commands"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvCommand(t *testing.T) {
	t.Parallel()
	configFile := setupEnvTestConfig(t)

	t.Run("CreateCommand", func(t *testing.T) {
		t.Parallel()
		testEnvCreateCommand(t)
	})

	t.Run("ListSubcommand", func(t *testing.T) {
		t.Parallel()
		testEnvListSubcommand(t)
	})

	t.Run("ShowSubcommand", func(t *testing.T) {
		t.Parallel()
		testEnvShowSubcommand(t)
	})

	t.Run("SetSubcommand", func(t *testing.T) {
		t.Parallel()
		testEnvSetSubcommand(t)
	})

	t.Run("ExportSubcommand", func(t *testing.T) {
		t.Parallel()
		testEnvExportSubcommand(t)
	})

	_ = configFile // Suppress unused variable warning
}

func setupEnvTestConfig(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "test-config.yml")

	testConfig := `
provider: stackit
environment: dev
environments:
  dev:
    provider: stackit
    region: eu-de-1
    project_id: dev-project
  staging:
    provider: stackit
    region: eu-de-2
    project_id: staging-project
  prod:
    provider: aws
    region: us-east-1
    account_id: prod-account
`

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	return configFile
}

func testEnvCreateCommand(t *testing.T) {
	t.Helper()

	cmd := commands.NewEnvCmd()
	assert.NotNil(t, cmd)
	assert.Equal(t, "env", cmd.Use)
}

func testEnvListSubcommand(t *testing.T) {
	t.Helper()

	cmd := commands.NewEnvCmd()

	var listCmd *cobra.Command

	for _, sub := range cmd.Commands() {
		if sub.Name() == "list" {
			listCmd = sub

			break
		}
	}

	assert.NotNil(t, listCmd)
	assert.Equal(t, "list", listCmd.Use)
}

func testEnvShowSubcommand(t *testing.T) {
	t.Helper()

	cmd := commands.NewEnvCmd()

	var showCmd *cobra.Command

	for _, sub := range cmd.Commands() {
		if sub.Name() == "show" {
			showCmd = sub

			break
		}
	}

	assert.NotNil(t, showCmd)
	assert.Equal(t, "show [environment]", showCmd.Use)
}

func testEnvSetSubcommand(t *testing.T) {
	t.Helper()

	cmd := commands.NewEnvCmd()

	var setCmd *cobra.Command

	for _, sub := range cmd.Commands() {
		if sub.Name() == "set" {
			setCmd = sub

			break
		}
	}

	assert.NotNil(t, setCmd)
	assert.Equal(t, "set <environment>", setCmd.Use)

	err := setCmd.Args(setCmd, []string{})
	require.Error(t, err)

	err = setCmd.Args(setCmd, []string{"dev"})
	assert.NoError(t, err)
}

func testEnvExportSubcommand(t *testing.T) {
	t.Helper()

	cmd := commands.NewEnvCmd()

	var exportCmd *cobra.Command

	for _, sub := range cmd.Commands() {
		if sub.Name() == "export" {
			exportCmd = sub

			break
		}
	}

	assert.NotNil(t, exportCmd)
	assert.Equal(t, "export [environment]", exportCmd.Use)
}

func TestBootstrapCommand(t *testing.T) {
	t.Parallel()
	t.Run("CreateCommand", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewBootstrapCmd()
		assert.NotNil(t, cmd)
		assert.Equal(t, "bootstrap", cmd.Use)
	})

	t.Run("FlagParsing", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewBootstrapCmd()

		// Test flag existence
		assert.NotNil(t, cmd.Flags().Lookup("blocs"))
		assert.NotNil(t, cmd.Flags().Lookup("force"))
	})
}

func TestTeardownCommand(t *testing.T) {
	t.Parallel()
	t.Run("CreateCommand", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewTeardownCmd()
		assert.NotNil(t, cmd)
		assert.Equal(t, "teardown", cmd.Use)
	})

	t.Run("FlagParsing", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewTeardownCmd()

		// Test flag existence
		assert.NotNil(t, cmd.Flags().Lookup("dry-run"))
		assert.NotNil(t, cmd.Flags().Lookup("force"))
		assert.NotNil(t, cmd.Flags().Lookup("skip"))
		assert.NotNil(t, cmd.Flags().Lookup("public-ips"))
		assert.NotNil(t, cmd.Flags().Lookup("all"))
	})
}
