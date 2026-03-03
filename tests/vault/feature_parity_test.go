package vault_test

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFeatureParitySTACKIT tests that Go implementation provides feature parity with Perl for STACKIT.
func setupFeatureParityTest(t *testing.T) (*config.Config, string, *vault.Manager) {
	t.Helper()

	if !hasVaultServer() {
		t.Skip("Vault server not available, skipping feature parity tests")
	}

	cfg := createStackitTestConfig()
	blocName := "parity-test"

	manager, err := vault.NewManagerFromEnv(cfg, blocName)
	require.NoError(t, err)

	t.Cleanup(func() {
		err := manager.Close()
		if err != nil {
			t.Errorf("Failed to close manager: %v", err)
		}
	})

	return cfg, blocName, manager
}

func testVaultPopulateFullConfiguration(t *testing.T, cfg *config.Config, manager *vault.Manager) {
	t.Helper()
	t.Parallel()

	opts := &vault.PopulateOptions{
		Subcommand: "",
		DryRun:     true,
		Force:      false,
	}

	err := manager.Populate(opts)
	if cfg.Provider == "stackit" {
		assert.NoError(t, err, "STACKIT populate should succeed")
	} else {
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not implemented")
	}
}

func testVaultPopulatePublicIPs(t *testing.T, cfg *config.Config, manager *vault.Manager) {
	t.Helper()
	t.Parallel()

	opts := &vault.PopulateOptions{
		Subcommand: "public-ips",
		DryRun:     true,
		Force:      false,
	}

	err := manager.Populate(opts)
	if cfg.Provider == "stackit" {
		assert.NoError(t, err, "STACKIT public IPs populate should succeed")
	} else {
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not implemented")
	}
}

func testVaultPathStructure(t *testing.T, cfg *config.Config, blocName string) {
	t.Helper()
	t.Parallel()

	pathBuilder := vault.NewPathBuilder(cfg, blocName)

	configPath := pathBuilder.GetConfigPath()
	assert.Equal(t, "secret/config/parity-test", configPath)

	ocfpPath := pathBuilder.GetOCFPConfigPath()
	assert.Equal(t, "secret/config/parity-test/ocfp", ocfpPath)

	mgmtPath := pathBuilder.GetEnvironmentPath("mgmt")
	assert.Equal(t, "secret/config/parity-test/mgmt", mgmtPath)

	netPath := pathBuilder.GetNetPath("mgmt")
	assert.Equal(t, "secret/config/parity-test/mgmt/net", netPath)

	subnetPath := pathBuilder.GetSubnetPath("mgmt", "ocfp", 0)
	assert.Equal(t, "secret/config/parity-test/mgmt/net/subnets/ocfp-0", subnetPath)

	boshPath := pathBuilder.GetBOSHPath("mgmt")
	assert.Equal(t, "secret/config/parity-test/mgmt/bosh", boshPath)

	publicIPsPath := pathBuilder.GetPublicIPsPath()
	assert.Equal(t, "secret/config/parity-test/ocf/public-ips", publicIPsPath)

	inceptionPath := pathBuilder.GetInceptionPath()
	assert.Equal(t, "secret/parity-test-inception", inceptionPath)
}

func testSecretGeneration(t *testing.T) {
	t.Parallel()

	generator := vault.NewSecretGenerator()

	secrets, err := generator.GenerateInceptionSecrets("test-deployment")
	require.NoError(t, err)

	secretsMap := secrets.ToMap()

	expectedKeys := []string{
		"admin_password",
		"director_password",
		"postgres_password",
		"mysql_password",
		"nats_password",
		"redis_password",
		"registry_password",
		"health_monitor_password",
		"blobstore_encryption_key",
		"db_encryption_key",
		"deployment_name",
		"inception_date",
	}

	for _, key := range expectedKeys {
		assert.Contains(t, secretsMap, key, "Missing expected secret key: %s", key)
		assert.NotEmpty(t, secretsMap[key], "Secret key should not be empty: %s", key)
	}

	defaultSecrets, err := generator.GenerateDefaultSecrets("test-deployment")
	require.NoError(t, err)

	defaultMap := defaultSecrets.ToMap()
	expectedDefaultKeys := []string{
		"admin_password",
		"uaa_admin_client_secret",
		"credhub_admin_client_secret",
		"nats_password",
		"postgres_password",
		"blobstore_secret",
		"deployment_name",
		"director_name",
		"internal_ip",
	}

	for _, key := range expectedDefaultKeys {
		assert.Contains(t, defaultMap, key, "Missing expected default secret key: %s", key)
	}
}

func TestFeatureParitySTACKIT(t *testing.T) {
	t.Parallel()

	cfg, blocName, manager := setupFeatureParityTest(t)

	t.Run("VaultPopulateFullConfiguration", func(t *testing.T) {
		t.Parallel()
		testVaultPopulateFullConfiguration(t, cfg, manager)
	})

	t.Run("VaultPopulatePublicIPs", func(t *testing.T) {
		t.Parallel()
		testVaultPopulatePublicIPs(t, cfg, manager)
	})

	t.Run("VaultPathStructure", func(t *testing.T) {
		t.Parallel()
		testVaultPathStructure(t, cfg, blocName)
	})

	t.Run("SecretGeneration", testSecretGeneration)
}

// TestStackitProviderSpecific tests STACKIT-specific vault functionality.
func TestStackitProviderSpecific(t *testing.T) {
	t.Parallel()

	if !hasVaultServer() {
		t.Skip("Vault server not available, skipping STACKIT provider tests")
	}

	cfg := createStackitTestConfig()
	client := createTestClient(t)

	t.Cleanup(func() {
		err := client.Close()
		if err != nil {
			t.Errorf("Failed to close client: %v", err)
		}
	})

	safe := vault.NewSafe(client)
	provider := vault.NewStackitVaultProvider(cfg, safe, "stackit-test")

	t.Run("SaveConfigToVault", func(t *testing.T) {
		t.Parallel()

		err := provider.SaveConfigToVault(nil, 1, 1)
		require.NoError(t, err)

		// Verify config was stored
		configPath := "secret/config/stackit-test/ocfp"
		config, err := safe.Get(configPath, "config")
		require.NoError(t, err)
		assert.NotEmpty(t, config)
	})

	t.Run("ConfigurePublicIPs", func(t *testing.T) {
		t.Parallel()

		err := provider.ConfigurePublicIPs(nil, 1, 1)
		require.NoError(t, err)

		// Verify public IPs were configured
		publicIPsPath := "secret/config/stackit-test/ocf/public-ips"
		publicIPs, err := safe.GetAll(publicIPsPath)
		require.NoError(t, err)
		assert.NotEmpty(t, publicIPs)

		// Check for expected CF router IPs
		assert.Contains(t, publicIPs, "cf_router_0")
		assert.Contains(t, publicIPs, "cf_tcp_router_0")
	})
}

// TestIntegrationWorkflow tests complete end-to-end workflow.
func TestIntegrationWorkflow(t *testing.T) {
	t.Parallel()

	if !hasVaultServer() {
		t.Skip("Vault server not available, skipping integration workflow tests")
	}

	cfg := createStackitTestConfig()
	blocName := "integration-test"

	t.Run("CompleteVaultWorkflow", func(t *testing.T) {
		t.Parallel()
		// Step 1: Initialize vault manager
		manager, err := vault.NewManagerFromEnv(cfg, blocName)
		require.NoError(t, err)

		t.Cleanup(func() {
			err := manager.Close()
			if err != nil {
				t.Errorf("Failed to close manager: %v", err)
			}
		})

		// Step 2: Generate and store inception secrets
		generator := vault.NewSecretGenerator()
		inceptionSecrets, err := generator.GenerateInceptionSecrets(blocName)
		require.NoError(t, err)

		pathBuilder := vault.NewPathBuilder(cfg, blocName)
		inceptionPath := pathBuilder.GetInceptionPath()

		safe := manager.GetSafe()
		err = safe.SetMultiple(inceptionPath, inceptionSecrets.ToMap())
		require.NoError(t, err)

		// Step 3: Perform migration (dry-run)
		migrateOpts := &vault.MigrateOptions{
			DryRun: true,
			Force:  true,
		}

		err = manager.Migrate(migrateOpts)
		require.NoError(t, err, "Migration dry-run should succeed")

		// Step 4: Test populate
		populateOpts := &vault.PopulateOptions{
			Subcommand: "",
			DryRun:     true,
			Force:      false,
		}

		err = manager.Populate(populateOpts)
		require.NoError(t, err, "Populate dry-run should succeed")

		// Cleanup: Remove test data
		err = safe.Delete(inceptionPath, "")
		assert.NoError(t, err)
	})
}

// createStackitTestConfig creates a test configuration for STACKIT.
func createStackitTestConfig() *config.Config {
	cfg := &config.Config{
		Name:                "stackit-test",
		Provider:            "stackit",
		ProjectID:           "test-project-123",
		Region:              "eu01",
		ServiceAccountToken: "test-service-account-token",
	}

	cfg.Network = createTestNetworkConfig()
	cfg.Bastion = createTestBastionConfig()
	cfg.Genesis = createTestGenesisConfig()
	cfg.AZs = createTestAvailabilityZones()
	cfg.Routers = createTestComponentConfig()
	cfg.Cells = createTestComponentConfig()
	cfg.Subnets = createTestSubnets()
	cfg.Blobstore = createTestBlobstoreConfig()

	cfg.DNS = []string{}
	cfg.FQDNs = &config.FQDNConfig{Mgmt: map[string]string{}, OCF: map[string]string{}}
	cfg.S3 = map[string]string{}
	cfg.AllowedIngressIPs = []string{}
	cfg.LBs = map[string]config.LBService{}
	cfg.Users = map[string]string{}

	return cfg
}

func createTestNetworkConfig() config.NetworkConfig {
	return config.NetworkConfig{
		NetworkCIDR: "10.0.0.0/16",
		DNS:         []string{},
	}
}

func createTestBastionConfig() config.Bastion {
	return config.Bastion{
		Genesis:           createTestGenesisConfig(),
		Git:               createTestGitConfig(),
		Tools:             config.OverrideSets{},
		CFPlugins:         config.OverrideSets{},
		Snaps:             config.OverrideSets{},
		ToolOverrides:     map[string]config.ToolOverride{},
		CFPluginOverrides: map[string]config.CFPluginOverride{},
		SnapOverrides:     map[string]config.SnapOverride{},
	}
}

func createTestGenesisConfig() config.Genesis {
	return config.Genesis{
		Enabled: false,
	}
}

func createTestGitConfig() config.GitConfig {
	return config.GitConfig{
		User: config.GitUser{},
	}
}

func createTestAvailabilityZones() map[string]config.AvailabilityZone {
	return map[string]config.AvailabilityZone{
		"eu01-1": {Zone: "eu01-1"},
		"eu01-2": {Zone: "eu01-2"},
	}
}

func createTestComponentConfig() config.ComponentConfig {
	return config.ComponentConfig{}
}

func createTestSubnets() []config.Subnet {
	return []config.Subnet{
		{CIDR: "10.0.1.0/24", Type: "ocfp"},
		{CIDR: "10.0.2.0/24", Type: "services"},
		{CIDR: "10.0.3.0/24", Type: "reserved"},
	}
}

func createTestBlobstoreConfig() config.BlobstoreConfig {
	return config.BlobstoreConfig{
		EnablePolicies: false,
		BoshBlobstore:  config.BucketSettings{},
		CFBuildpacks:   config.BucketSettings{},
		CFDroplets:     config.BucketSettings{},
		CFAppPackages:  config.BucketSettings{},
	}
}
