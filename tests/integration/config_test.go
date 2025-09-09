package integration_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigLoading(t *testing.T) {
	t.Parallel()
	t.Run("LoadValidConfig", testLoadValidConfig)
	t.Run("LoadWithoutEnvironmentVariables", testLoadWithoutEnvironmentVariables)
	t.Run("BlocConfiguration", testBlocConfiguration)
	t.Run("NetworkConfiguration", testNetworkConfiguration)
	t.Run("BastionConfiguration", testBastionConfiguration)
}

func testLoadValidConfig(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yml")

	testConfig := `
blocs:
  test:
    name: test
    provider: stackit
    environment: test
    ssh_key_storage_dir: /tmp/keys
    project_id: test-project
    auth_token: test-key
    region: eu-de-1
`

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	cfg, err := config.LoadWithParams(configFile, "test")
	require.NoError(t, err)

	assert.Equal(t, "stackit", cfg.Provider)
	assert.Equal(t, "test", cfg.Name)
	assert.Equal(t, "/tmp/keys", cfg.SSHKeyStorageDir)
	assert.Equal(t, "test-project", cfg.ProjectID)
	assert.Equal(t, "test-key", cfg.AuthToken)
}

func testLoadWithoutEnvironmentVariables(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yml")

	testConfig := `
blocs:
  test-env:
    name: ${OCFP_TEST_VAR}
    provider: stackit
`

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	cfg, err := config.LoadWithParams(configFile, "test-env")
	require.NoError(t, err)

	assert.Equal(t, "${OCFP_TEST_VAR}", cfg.Name)
}

func testBlocConfiguration(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yml")

	testConfig := `
blocs:
  mgmt:
    name: mgmt
    provider: stackit
    type: management
    environment: dev
    network:
      name: mgmt-network
      cidr: 10.0.0.0/16
  ocf:
    name: ocf
    provider: stackit
    type: application
    environment: dev
    network:
      name: ocf-network
      cidr: 10.1.0.0/16
`

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	cfg, err := config.LoadWithParams(configFile, "mgmt")
	require.NoError(t, err)

	assert.Equal(t, "mgmt", cfg.Name)
	assert.Equal(t, "stackit", cfg.Provider)
	assert.Equal(t, "management", cfg.Type)
	assert.Equal(t, "10.0.0.0/16", cfg.Network.CIDR)
}

func testNetworkConfiguration(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yml")

	testConfig := `
blocs:
  test:
    name: test
    provider: stackit
    network:
      name: test-network
      cidr: 10.0.0.0/16
      network_cidr: 10.0.0.0/8
      dns:
        - 8.8.8.8
        - 8.8.4.4
`

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	cfg, err := config.LoadWithParams(configFile, "test")
	require.NoError(t, err)

	assert.Equal(t, "test-network", cfg.Network.Name)
	assert.Equal(t, "10.0.0.0/16", cfg.Network.CIDR)
	assert.Len(t, cfg.Network.DNS, 2)
}

func testBastionConfiguration(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yml")

	testConfig := `
blocs:
  test:
    name: test
    provider: stackit
    bastion:
      flavor: t3.small
      image: ubuntu-22.04
      os: ubuntu
      os_version: "22.04"
      keypair: test-key
      ssh_user: ubuntu
`

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	cfg, err := config.LoadWithParams(configFile, "test")
	require.NoError(t, err)

	assert.Equal(t, "t3.small", cfg.Bastion.Flavor)
	assert.Equal(t, "ubuntu-22.04", cfg.Bastion.Image)
	assert.Equal(t, "ubuntu", cfg.Bastion.SSHUser)
}
