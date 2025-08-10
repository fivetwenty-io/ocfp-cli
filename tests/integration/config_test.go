package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigLoading(t *testing.T) {
	t.Run("LoadValidConfig", func(t *testing.T) {
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "config.yml")

		testConfig := `
name: test-bloc
provider: stackit
ssh_key_storage_dir: /tmp/keys
project_id: test-project
auth_token: test-key
region: eu-de-1
blocs:
  - name: test
    provider: stackit
    environment: test
`

		err := os.WriteFile(configFile, []byte(testConfig), 0644)
		require.NoError(t, err)

		cfg, err := config.LoadWithParams(configFile, "test")
		require.NoError(t, err)

		assert.Equal(t, "stackit", cfg.Provider)
		assert.Equal(t, "test-bloc", cfg.Name)
		assert.Equal(t, "/tmp/keys", cfg.SSHKeyStorageDir)
		assert.Equal(t, "test-project", cfg.ProjectID)
		assert.Equal(t, "test-key", cfg.AuthToken)
	})

	t.Run("LoadWithoutEnvironmentVariables", func(t *testing.T) {
		// The config loader doesn't expand environment variables
		// This test just ensures config can be loaded with ${} syntax
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "config.yml")

		testConfig := `
provider: stackit
name: ${OCFP_TEST_VAR}
`

		err := os.WriteFile(configFile, []byte(testConfig), 0644)
		require.NoError(t, err)

		cfg, err := config.LoadWithParams(configFile, "")
		require.NoError(t, err)

		// Config loader doesn't expand env vars automatically
		assert.Equal(t, "${OCFP_TEST_VAR}", cfg.Name)
	})

	t.Run("BlocConfiguration", func(t *testing.T) {
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "config.yml")

		testConfig := `
name: test-deployment
provider: stackit
blocs:
  - name: mgmt
    provider: stackit
    type: management
    environment: dev
    network:
      name: mgmt-network
      cidr: 10.0.0.0/16
  - name: ocf
    provider: stackit
    type: application
    environment: dev
    network:
      name: ocf-network
      cidr: 10.1.0.0/16
`

		err := os.WriteFile(configFile, []byte(testConfig), 0644)
		require.NoError(t, err)

		cfg, err := config.LoadWithParams(configFile, "mgmt")
		require.NoError(t, err)

		assert.Equal(t, 2, len(cfg.Blocs))
		assert.Equal(t, "mgmt", cfg.Blocs[0].Name)
		assert.Equal(t, "ocf", cfg.Blocs[1].Name)
		assert.Equal(t, "10.0.0.0/16", cfg.Blocs[0].Network.CIDR)
	})

	t.Run("NetworkConfiguration", func(t *testing.T) {
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "config.yml")

		testConfig := `
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

		err := os.WriteFile(configFile, []byte(testConfig), 0644)
		require.NoError(t, err)

		cfg, err := config.LoadWithParams(configFile, "")
		require.NoError(t, err)

		assert.Equal(t, "test-network", cfg.Network.Name)
		assert.Equal(t, "10.0.0.0/16", cfg.Network.CIDR)
		assert.Equal(t, 2, len(cfg.Network.DNS))
	})

	t.Run("BastionConfiguration", func(t *testing.T) {
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "config.yml")

		testConfig := `
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

		err := os.WriteFile(configFile, []byte(testConfig), 0644)
		require.NoError(t, err)

		cfg, err := config.LoadWithParams(configFile, "")
		require.NoError(t, err)

		assert.Equal(t, "t3.small", cfg.Bastion.Flavor)
		assert.Equal(t, "ubuntu-22.04", cfg.Bastion.Image)
		assert.Equal(t, "ubuntu", cfg.Bastion.SSHUser)
	})
}
