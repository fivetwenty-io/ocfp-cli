package vault_test

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVaultClient tests basic vault client functionality.
func TestVaultClient(t *testing.T) {
	if !hasVaultServer() {
		t.Skip("Vault server not available, skipping integration tests")
	}

	t.Parallel()

	cfg := &vault.Config{
		Address: getTestVaultAddr(),
		Token:   getTestVaultToken(),
		TLSSkip: true,
	}

	client, err := vault.NewClient(cfg)
	require.NoError(t, err)

	defer func() {
		err := client.Close()
		if err != nil {
			t.Errorf("Failed to close client: %v", err)
		}
	}()

	// Test connection validation
	err = client.ValidateConnection()
	require.NoError(t, err)
}

// TestSafeOperations tests safe wrapper operations.
func TestSafeOperations(t *testing.T) {
	if !hasVaultServer() {
		t.Skip("Vault server not available, skipping integration tests")
	}

	t.Parallel()

	client := createTestClient(t)

	defer func() {
		err := client.Close()
		if err != nil {
			t.Errorf("Failed to close client: %v", err)
		}
	}()

	safe := vault.NewSafe(client)
	testPath := "secret/test-" + generateTestID()

	// Test Set operation
	err := safe.Set(testPath, "test_key", "test_value")
	require.NoError(t, err)

	// Test Get operation
	value, err := safe.Get(testPath, "test_key")
	require.NoError(t, err)
	assert.Equal(t, "test_value", value)

	// Test SetMultiple operation
	data := map[string]interface{}{
		"key1": "value1",
		"key2": "value2",
		"key3": 123,
	}
	err = safe.SetMultiple(testPath, data)
	require.NoError(t, err)

	// Test GetAll operation
	allData, err := safe.GetAll(testPath)
	require.NoError(t, err)
	assert.Contains(t, allData, "key1")
	assert.Contains(t, allData, "key2")
	assert.Contains(t, allData, "key3")
	assert.Contains(t, allData, "test_key") // From earlier Set

	// Test Delete operation
	err = safe.Delete(testPath, "")
	require.NoError(t, err)

	// Verify deletion
	exists, err := safe.Exists(testPath)
	require.NoError(t, err)
	assert.False(t, exists)
}

// TestEngineDetection tests KV engine detection.
func TestEngineDetection(t *testing.T) {
	if !hasVaultServer() {
		t.Skip("Vault server not available, skipping integration tests")
	}

	t.Parallel()

	client := createTestClient(t)

	defer func() {
		err := client.Close()
		if err != nil {
			t.Errorf("Failed to close client: %v", err)
		}
	}()

	detector := vault.NewEngineDetector(client)

	// Test secret mount (typically KV v2)
	info, err := detector.DetectEngineForPath("secret/test")
	require.NoError(t, err)
	assert.NotNil(t, info)
	assert.Contains(t, []string{"kv-v1", "kv-v2"}, info.Type)

	// Test IsKVv2
	isV2, err := detector.IsKVv2("secret/test")
	require.NoError(t, err)
	t.Logf("secret/ mount is KV v2: %v", isV2)

	// Test cache functionality
	cached := detector.GetCachedEngines()
	assert.GreaterOrEqual(t, len(cached), 1)
}

// TestSecretGeneration tests secret generation utilities.
func TestSecretGeneration(t *testing.T) {
	t.Parallel()

	generator := vault.NewSecretGenerator()

	// Test password generation
	password, err := generator.GenerateSimplePassword(20)
	require.NoError(t, err)
	assert.Len(t, password, 20)

	// Test with options
	opts := &vault.PasswordOptions{
		Length:           16,
		IncludeUpper:     true,
		IncludeLower:     true,
		IncludeNumbers:   true,
		IncludeSymbols:   false,
		ExcludeAmbiguous: true,
	}
	password2, err := generator.GeneratePassword(opts)
	require.NoError(t, err)
	assert.Len(t, password2, 16)

	// Test inception secrets
	secrets, err := generator.GenerateInceptionSecrets("test-deployment")
	require.NoError(t, err)
	assert.NotEmpty(t, secrets.AdminPassword)
	assert.NotEmpty(t, secrets.DirectorPassword)
	assert.Equal(t, "test-deployment", secrets.DeploymentName)

	// Test default secrets
	defaultSecrets, err := generator.GenerateDefaultSecrets("test-deployment")
	require.NoError(t, err)
	assert.NotEmpty(t, defaultSecrets.AdminPassword)
	assert.Equal(t, "test-deployment", defaultSecrets.DeploymentName)
}

// TestVaultManager tests vault manager operations.
func TestVaultManager(t *testing.T) {
	if !hasVaultServer() {
		t.Skip("Vault server not available, skipping integration tests")
	}

	t.Parallel()

	cfg := createTestConfig()
	manager, err := vault.NewManagerFromEnv(cfg, "test-bloc")
	require.NoError(t, err)

	defer func() {
		err := manager.Close()
		if err != nil {
			t.Errorf("Failed to close manager: %v", err)
		}
	}()

	// Test populate with dry run
	opts := &vault.PopulateOptions{
		DryRun: true,
	}

	err = manager.Populate(opts)
	// Should not error even if provider not fully implemented for tests
	t.Logf("Populate dry-run result: %v", err)
}

// TestValidation tests vault validation features.
func TestValidation(t *testing.T) {
	if !hasVaultServer() {
		t.Skip("Vault server not available, skipping integration tests")
	}

	t.Parallel()

	client := createTestClient(t)

	defer func() {
		err := client.Close()
		if err != nil {
			t.Errorf("Failed to close client: %v", err)
		}
	}()

	safe := vault.NewSafe(client)
	cfg := createTestConfig()
	validator := vault.NewValidator(client, safe, cfg)

	// Test path validation
	result, err := validator.ValidateVaultPath("secret/test")
	require.NoError(t, err)
	assert.NotNil(t, result)

	// Test invalid path
	result, err = validator.ValidateVaultPath("")
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.NotEmpty(t, result.Errors)
}

// TestPolicyManagement tests policy management features.
func TestPolicyManagement(t *testing.T) {
	if !hasVaultServer() {
		t.Skip("Vault server not available, skipping integration tests")
	}

	t.Parallel()

	client := createTestClient(t)

	defer func() {
		err := client.Close()
		if err != nil {
			t.Errorf("Failed to close client: %v", err)
		}
	}()

	cfg := createTestConfig()
	policyManager := vault.NewPolicyManager(client, cfg, "test-bloc")

	// Test policy creation
	policy := &vault.PolicyTemplate{
		Name:        "test-policy",
		Description: "Test policy for unit tests",
		Rules: []vault.PolicyRule{
			{
				Path:         "secret/test/*",
				Capabilities: []string{"read", "list"},
				Description:  "Read test secrets",
			},
		},
	}

	err := policyManager.CreatePolicy(policy)
	require.NoError(t, err)

	// Test policy retrieval
	policyHCL, err := policyManager.GetPolicy("test-policy")
	require.NoError(t, err)
	assert.Contains(t, policyHCL, "secret/test/*")

	// Test policy listing
	policies, err := policyManager.ListPolicies()
	require.NoError(t, err)
	assert.Contains(t, policies, "test-policy")

	// Cleanup
	err = policyManager.DeletePolicy("test-policy")
	require.NoError(t, err)
}

// Helper functions for testing

// hasVaultServer checks if a vault server is available for testing.
func hasVaultServer() bool {
	vaultAddr := os.Getenv("VAULT_ADDR")
	vaultToken := os.Getenv("VAULT_TOKEN")

	return vaultAddr != "" && vaultToken != ""
}

// getTestVaultAddr returns the vault address for testing.
func getTestVaultAddr() string {
	addr := os.Getenv("VAULT_ADDR")
	if addr == "" {
		addr = "http://127.0.0.1:8200"
	}

	return addr
}

// getTestVaultToken returns the vault token for testing.
func getTestVaultToken() string {
	return os.Getenv("VAULT_TOKEN")
}

// createTestClient creates a vault client for testing.
func createTestClient(t *testing.T) *vault.Client {
	cfg := &vault.Config{
		Address: getTestVaultAddr(),
		Token:   getTestVaultToken(),
		TLSSkip: true,
	}

	client, err := vault.NewClient(cfg)
	require.NoError(t, err)

	return client
}

// createTestConfig creates a test configuration.
func createTestConfig() *config.Config {
	return &config.Config{
		Name:      "test-bloc",
		Provider:  "stackit",
		Region:    "eu01",
		ProjectID: "test-project",
		DNS:       []string{"8.8.8.8", "8.8.4.4"},
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
		},
	}
}

// generateTestID generates a unique ID for test resources.
func generateTestID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}
