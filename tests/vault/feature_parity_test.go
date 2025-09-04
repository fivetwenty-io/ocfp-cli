package vault_test

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFeatureParitySTACKIT tests that Go implementation provides feature parity with Perl for STACKIT
func TestFeatureParitySTACKIT(t *testing.T) {
	if !hasVaultServer() {
		t.Skip("Vault server not available, skipping feature parity tests")
	}

	// Create test configuration that matches Perl test scenarios
	cfg := createStackitTestConfig()
	blocName := "parity-test"

	// Create vault manager
	manager, err := vault.NewManagerFromEnv(cfg, blocName)
	require.NoError(t, err)
	defer func() {
		if err := manager.Close(); err != nil {
			t.Errorf("Failed to close manager: %v", err)
		}
	}()

	t.Run("VaultPopulateFullConfiguration", func(t *testing.T) {
		// Test dry-run populate (should not fail)
		opts := &vault.PopulateOptions{
			DryRun: true,
		}

		err := manager.Populate(opts)
		// For STACKIT, this should work; for others it should gracefully fail
		if cfg.Provider == "stackit" {
			assert.NoError(t, err, "STACKIT populate should succeed")
		} else {
			// Other providers should fail gracefully with "not implemented"
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "not implemented")
		}
	})

	t.Run("VaultPopulatePublicIPs", func(t *testing.T) {
		opts := &vault.PopulateOptions{
			Subcommand: "public-ips",
			DryRun:     true,
		}

		err := manager.Populate(opts)
		if cfg.Provider == "stackit" {
			assert.NoError(t, err, "STACKIT public IPs populate should succeed")
		} else {
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "not implemented")
		}
	})

	t.Run("VaultPathStructure", func(t *testing.T) {
		// Test that vault paths match Perl implementation exactly
		pathBuilder := vault.NewPathBuilder(cfg, blocName)

		// Test standard paths
		configPath := pathBuilder.GetConfigPath()
		assert.Equal(t, "secret/config/parity-test", configPath)

		ocfpPath := pathBuilder.GetOCFPConfigPath()
		assert.Equal(t, "secret/config/parity-test/ocfp", ocfpPath)

		mgmtPath := pathBuilder.GetEnvironmentPath("mgmt")
		assert.Equal(t, "secret/config/parity-test/mgmt", mgmtPath)

		vpcPath := pathBuilder.GetVPCPath("mgmt")
		assert.Equal(t, "secret/config/parity-test/mgmt/vpc", vpcPath)

		subnetPath := pathBuilder.GetSubnetPath("mgmt", "ocfp", 0)
		assert.Equal(t, "secret/config/parity-test/mgmt/vpc/subnets/ocfp-0", subnetPath)

		boshPath := pathBuilder.GetBOSHPath("mgmt")
		assert.Equal(t, "secret/config/parity-test/mgmt/bosh", boshPath)

		publicIPsPath := pathBuilder.GetPublicIPsPath()
		assert.Equal(t, "secret/config/parity-test/ocf/public-ips", publicIPsPath)

		inceptionPath := pathBuilder.GetInceptionPath()
		assert.Equal(t, "secret/parity-test-inception", inceptionPath)
	})

	t.Run("SecretGeneration", func(t *testing.T) {
		generator := vault.NewSecretGenerator()

		// Test inception secrets match expected structure
		secrets, err := generator.GenerateInceptionSecrets("test-deployment")
		require.NoError(t, err)

		secretsMap := secrets.ToMap()

		// Verify all expected keys are present (matching Perl implementation)
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

		// Test default secrets
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
	})
}

// TestStackitProviderSpecific tests STACKIT-specific vault functionality
func TestStackitProviderSpecific(t *testing.T) {
	if !hasVaultServer() {
		t.Skip("Vault server not available, skipping STACKIT provider tests")
	}

	cfg := createStackitTestConfig()
	client := createTestClient(t)
	defer func() {
		if err := client.Close(); err != nil {
			t.Errorf("Failed to close client: %v", err)
		}
	}()

	safe := vault.NewSafe(client)
	provider := vault.NewStackitVaultProvider(cfg, safe, "stackit-test")

	t.Run("SaveConfigToVault", func(t *testing.T) {
		err := provider.SaveConfigToVault()
		assert.NoError(t, err)

		// Verify config was stored
		configPath := "secret/config/stackit-test/ocfp"
		config, err := safe.Get(configPath, "config")
		assert.NoError(t, err)
		assert.NotEmpty(t, config)
	})

	t.Run("ConfigurePublicIPs", func(t *testing.T) {
		err := provider.ConfigurePublicIPs()
		assert.NoError(t, err)

		// Verify public IPs were configured
		publicIPsPath := "secret/config/stackit-test/ocf/public-ips"
		publicIPs, err := safe.GetAll(publicIPsPath)
		assert.NoError(t, err)
		assert.NotEmpty(t, publicIPs)

		// Check for expected CF router IPs
		assert.Contains(t, publicIPs, "cf_router_0")
		assert.Contains(t, publicIPs, "cf_tcp_router_0")
	})
}

// TestIntegrationWorkflow tests complete end-to-end workflow
func TestIntegrationWorkflow(t *testing.T) {
	if !hasVaultServer() {
		t.Skip("Vault server not available, skipping integration workflow tests")
	}

	cfg := createStackitTestConfig()
	blocName := "integration-test"

	t.Run("CompleteVaultWorkflow", func(t *testing.T) {
		// Step 1: Initialize vault manager
		manager, err := vault.NewManagerFromEnv(cfg, blocName)
		require.NoError(t, err)
		defer func() {
			if err := manager.Close(); err != nil {
				t.Errorf("Failed to close manager: %v", err)
			}
		}()

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
		assert.NoError(t, err, "Migration dry-run should succeed")

		// Step 4: Test populate
		populateOpts := &vault.PopulateOptions{
			DryRun: true,
		}

		err = manager.Populate(populateOpts)
		assert.NoError(t, err, "Populate dry-run should succeed")

		// Cleanup: Remove test data
		err = safe.Delete(inceptionPath, "")
		assert.NoError(t, err)
	})
}

// createStackitTestConfig creates a test configuration for STACKIT
func createStackitTestConfig() *config.Config {
	return &config.Config{
		Name:                "stackit-test",
		Provider:            "stackit",
		Region:              "eu01",
		ProjectID:           "test-project-123",
		ServiceAccountToken: "test-service-account-token",
		DNS:                 []string{"8.8.8.8", "1.1.1.1"},
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
		AZs: map[string]config.AvailabilityZone{
			"eu01-1": {Zone: "eu01-1"},
			"eu01-2": {Zone: "eu01-2"},
		},
		Subnets: []config.Subnet{
			{Type: "ocfp", CIDR: "10.0.1.0/24"},
			{Type: "services", CIDR: "10.0.2.0/24"},
			{Type: "reserved", CIDR: "10.0.3.0/24"},
		},
		FQDNs: map[string]interface{}{
			"mgmt": map[string]interface{}{
				"bosh":    "bosh.stackit-test.eu01",
				"jumpbox": "jumpbox.stackit-test.eu01",
			},
			"ocf": map[string]interface{}{
				"system": "cf.stackit-test.eu01",
				"apps":   "apps.stackit-test.eu01",
			},
		},
	}
}
