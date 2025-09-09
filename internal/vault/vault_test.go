package vault_test

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPathBuilder tests vault path construction.
func TestPathBuilder(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestConfig().
		WithProvider("stackit").
		WithRegion("eu01").
		WithVaultNetwork().
		WithVaultBastion().
		WithVaultComponents().
		WithVaultBlobstore().
		Build()
	pathBuilder := vault.NewPathBuilder(cfg, "test-bloc")

	// Test basic paths
	assert.Equal(t, "secret/config/test-bloc", pathBuilder.GetConfigPath())
	assert.Equal(t, "secret/config/test-bloc/ocfp", pathBuilder.GetOCFPConfigPath())
	assert.Equal(t, "secret/config/test-bloc/mgmt", pathBuilder.GetEnvironmentPath("mgmt"))
	assert.Equal(t, "secret/config/test-bloc/ocf", pathBuilder.GetEnvironmentPath("ocf"))

	// Test VPC paths
	assert.Equal(t, "secret/config/test-bloc/mgmt/vpc", pathBuilder.GetVPCPath("mgmt"))
	assert.Equal(t, "secret/config/test-bloc/ocf/vpc", pathBuilder.GetVPCPath("ocf"))

	// Test subnet paths
	assert.Equal(t, "secret/config/test-bloc/mgmt/vpc/subnets", pathBuilder.GetSubnetsPath("mgmt"))
	assert.Equal(t, "secret/config/test-bloc/mgmt/vpc/subnets/ocfp-0", pathBuilder.GetSubnetPath("mgmt", "ocfp", 0))
	assert.Equal(t, "secret/config/test-bloc/ocf/vpc/subnets/services-1", pathBuilder.GetSubnetPath("ocf", "services", 1))

	// Test BOSH paths
	assert.Equal(t, "secret/config/test-bloc/mgmt/bosh", pathBuilder.GetBOSHPath("mgmt"))
	assert.Equal(t, "secret/config/test-bloc/mgmt/bosh/iam", pathBuilder.GetIAMPath("mgmt"))
	assert.Equal(t, "secret/config/test-bloc/mgmt/bosh/iam/s3", pathBuilder.GetS3IAMPath("mgmt"))
	assert.Equal(t, "secret/config/test-bloc/mgmt/bosh/keys", pathBuilder.GetKeysPath("mgmt"))
	assert.Equal(t, "secret/config/test-bloc/mgmt/bosh/keys/bosh", pathBuilder.GetBOSHKeyPath("mgmt"))

	// Test other paths
	assert.Equal(t, "secret/config/test-bloc/ocf/public-ips", pathBuilder.GetPublicIPsPath())
	assert.Equal(t, "secret/config/test-bloc/certs", pathBuilder.GetCertsPath())
	assert.Equal(t, "secret/test-bloc-inception", pathBuilder.GetInceptionPath())
	assert.Equal(t, "secret/config/test-bloc/mgmt/jumpbox/users", pathBuilder.GetJumpboxUsersPath())
}

// TestPathBuilderParsing tests path parsing functionality.
func TestPathBuilderParsing(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestConfig().
		WithVaultNetwork().
		WithVaultBastion().
		WithVaultComponents().
		WithVaultBlobstore().
		Build()
	pathBuilder := vault.NewPathBuilder(cfg, "test-bloc")

	// Test config path parsing
	info, err := pathBuilder.ParsePath("secret/config/test-bloc/mgmt/vpc/subnets")
	require.NoError(t, err)
	assert.Equal(t, "mgmt", info.Environment)
	assert.Equal(t, "vpc", info.Component)
	assert.Equal(t, "subnets", info.Subpath)

	// Test inception path parsing
	info, err = pathBuilder.ParsePath("secret/test-bloc-inception/admin")
	require.NoError(t, err)
	assert.Equal(t, "inception", info.Component)
	assert.Equal(t, "admin", info.Subpath)

	// Test path validation
	assert.True(t, pathBuilder.IsConfigPath("secret/config/test-bloc/mgmt"))
	assert.True(t, pathBuilder.IsInceptionPath("secret/test-bloc-inception"))
	assert.False(t, pathBuilder.IsConfigPath("secret/other/path"))
	assert.False(t, pathBuilder.IsInceptionPath("secret/regular/path"))
}

// TestSecretGenerator tests secret generation.
func TestSecretGenerator(t *testing.T) {
	t.Parallel()

	generator := vault.NewSecretGenerator()

	// Test simple password generation
	password, err := generator.GenerateSimplePassword(20)
	require.NoError(t, err)
	assert.Len(t, password, 20)
	assert.NotEmpty(t, password)

	// Test password with options
	opts := &vault.PasswordOptions{
		Length:           12,
		IncludeUpper:     true,
		IncludeLower:     true,
		IncludeNumbers:   true,
		IncludeSymbols:   false,
		ExcludeAmbiguous: true,
	}
	password2, err := generator.GeneratePassword(opts)
	require.NoError(t, err)
	assert.Len(t, password2, 12)

	// Test UUID generation
	uuid, err := generator.GenerateUUID()
	require.NoError(t, err)
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, uuid)

	// Test encryption key generation
	key, err := generator.GenerateEncryptionKey(32)
	require.NoError(t, err)
	assert.Len(t, key, 64) // 32 bytes = 64 hex chars
	assert.Regexp(t, `^[0-9a-f]+$`, key)

	// Test JWT secret generation
	jwtSecret, err := generator.GenerateJWTSecret()
	require.NoError(t, err)
	assert.Len(t, jwtSecret, 128) // 64 bytes = 128 hex chars
}

// TestInceptionSecrets tests inception secret generation.
func TestInceptionSecrets(t *testing.T) {
	t.Parallel()

	generator := vault.NewSecretGenerator()

	secrets, err := generator.GenerateInceptionSecrets("test-deployment")
	require.NoError(t, err)

	// Verify all fields are populated
	assert.NotEmpty(t, secrets.AdminPassword)
	assert.NotEmpty(t, secrets.DirectorPassword)
	assert.NotEmpty(t, secrets.PostgresPassword)
	assert.NotEmpty(t, secrets.MySQLPassword)
	assert.NotEmpty(t, secrets.NatsPassword)
	assert.NotEmpty(t, secrets.RedisPassword)
	assert.NotEmpty(t, secrets.RegistryPassword)
	assert.NotEmpty(t, secrets.HealthMonitorPassword)
	assert.NotEmpty(t, secrets.BlobstoreEncryptionKey)
	assert.NotEmpty(t, secrets.DBEncryptionKey)
	assert.Equal(t, "test-deployment", secrets.DeploymentName)
	assert.NotEmpty(t, secrets.InceptionDate)

	// Test ToMap conversion
	secretsMap := secrets.ToMap()
	assert.Len(t, secretsMap, 12)
	assert.Equal(t, secrets.AdminPassword, secretsMap["admin_password"])
	assert.Equal(t, secrets.DeploymentName, secretsMap["deployment_name"])
}

// TestDefaultSecrets tests default secret generation.
func TestDefaultSecrets(t *testing.T) {
	t.Parallel()

	generator := vault.NewSecretGenerator()

	secrets, err := generator.GenerateDefaultSecrets("test-deployment")
	require.NoError(t, err)

	// Verify all fields are populated
	assert.NotEmpty(t, secrets.AdminPassword)
	assert.NotEmpty(t, secrets.UAAAdminClientSecret)
	assert.NotEmpty(t, secrets.CredhubAdminClientSecret)
	assert.NotEmpty(t, secrets.NatsPassword)
	assert.NotEmpty(t, secrets.PostgresPassword)
	assert.NotEmpty(t, secrets.BlobstoreSecret)
	assert.Equal(t, "test-deployment", secrets.DeploymentName)
	assert.Equal(t, "test-deployment-bosh", secrets.DirectorName)
	assert.Equal(t, "10.0.0.6", secrets.InternalIP)

	// Test ToMap conversion
	secretsMap := secrets.ToMap()
	assert.Len(t, secretsMap, 9)
	assert.Equal(t, secrets.AdminPassword, secretsMap["admin_password"])
	assert.Equal(t, secrets.DirectorName, secretsMap["director_name"])
}

// TestRetryLogic tests retry functionality.
func TestRetryLogic(t *testing.T) {
	t.Parallel()
	// Test successful operation (no retry needed)
	attempts := 0
	err := vault.WithRetry(func() error {
		attempts++

		return nil
	}, vault.DefaultRetryConfig())

	require.NoError(t, err)
	assert.Equal(t, 1, attempts)

	// Test retryable error
	attempts = 0
	err = vault.WithRetry(func() error {
		attempts++
		if attempts < 3 {
			return vault.ErrConnectionTimeout // Retryable error
		}

		return nil
	}, vault.DefaultRetryConfig())

	require.NoError(t, err)
	assert.Equal(t, 3, attempts)

	// Test non-retryable error
	attempts = 0
	err = vault.WithRetry(func() error {
		attempts++

		return vault.ErrAccessDenied // Non-retryable error
	}, vault.DefaultRetryConfig())

	require.Error(t, err)
	assert.Equal(t, 1, attempts) // Should not retry
}

// TestIsRetryable tests error classification.
func TestIsRetryable(t *testing.T) {
	t.Parallel()
	// Test retryable errors
	retryableErrors := []string{
		"connection refused",
		"connection timeout",
		"502 Bad Gateway",
		"503 Service Unavailable",
		"network unreachable",
		"temporary failure",
	}

	for _, errMsg := range retryableErrors {
		err := vault.ErrDynamicTestMessage(errMsg)
		assert.True(t, vault.IsRetryable(err), "Error should be retryable: %s", errMsg)
	}

	// Test non-retryable errors
	nonRetryableErrors := []string{
		"access denied",
		"forbidden",
		"not found",
		"invalid input",
	}

	for _, errMsg := range nonRetryableErrors {
		err := vault.ErrDynamicTestMessage(errMsg)
		assert.False(t, vault.IsRetryable(err), "Error should not be retryable: %s", errMsg)
	}

	// Test nil error
	assert.False(t, vault.IsRetryable(nil))
}

// TestStackitProvider tests STACKIT provider functionality.
func TestStackitProvider(t *testing.T) {
	t.Parallel()

	cfg := createTestStackitConfig()
	mockSafe := &MockSafe{data: make(map[string]map[string]interface{})}
	provider := vault.NewStackitVaultProvider(cfg, mockSafe, "stackit-test")

	runStackitProviderTests(t, provider)
}

func createTestStackitConfig() *config.Config {
	return &config.Config{
		Name:                "stackit-test",
		Provider:            "stackit",
		Region:              "eu01",
		ProjectID:           "test-project",
		ServiceAccountToken: "test-token",
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
		Bastion:    createTestBastionConfig(),
		Genesis:    createTestGenesisConfig(),
		Deployment: createTestDeploymentConfig(),
		DNS:        []string{"1.1.1.1", "8.8.8.8"},
		AZs:        createTestAZConfig(),
		Routers:    config.ComponentConfig{},
		Cells:      config.ComponentConfig{},
		Subnets:    createTestSubnetsConfig(),
		Blobstore:  createTestBlobstoreConfig(),
	}
}

func createTestBastionConfig() config.Bastion {
	return config.Bastion{
		Genesis: createTestGenesisConfig(),
		Git: config.GitConfig{
			User: config.GitUser{},
		},
		Tools:     config.OverrideSets{},
		CFPlugins: config.OverrideSets{},
		Snaps:     config.OverrideSets{},
	}
}

func createTestGenesisConfig() config.Genesis {
	return config.Genesis{Enabled: false}
}

func createTestDeploymentConfig() config.Deployment {
	return config.Deployment{
		HierarchyFiles:      false,
		HierarchyVaultPaths: false,
	}
}

func createTestAZConfig() map[string]config.AvailabilityZone {
	return map[string]config.AvailabilityZone{
		"eu01-1": {Zone: "eu01-1"},
		"eu01-2": {Zone: "eu01-2"},
	}
}

func createTestSubnetsConfig() []config.Subnet {
	return []config.Subnet{
		{CIDR: "10.0.1.0/24", Type: "ocfp"},
		{CIDR: "10.0.2.0/24", Type: "services"},
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

func runStackitProviderTests(t *testing.T, provider *vault.StackitVaultProvider) {
	t.Helper()

	assert.Equal(t, "stackit", provider.GetProviderName())

	err := provider.SaveConfigToVault()
	require.NoError(t, err)

	err = provider.ConfigurePublicIPs()
	require.NoError(t, err)
}

// MockSafe is a mock implementation of Safe for unit testing.
type MockSafe struct {
	data map[string]map[string]interface{}
}

func (m *MockSafe) Set(path, key string, value interface{}) error {
	if m.data[path] == nil {
		m.data[path] = make(map[string]interface{})
	}

	m.data[path][key] = value

	return nil
}

func (m *MockSafe) SetMultiple(path string, data map[string]interface{}) error {
	m.data[path] = data

	return nil
}

func (m *MockSafe) Get(path, key string) (interface{}, error) {
	if pathData, exists := m.data[path]; exists {
		if key == "" {
			return pathData, nil
		}

		return pathData[key], nil
	}

	return nil, vault.ErrPathNotFound
}

func (m *MockSafe) GetAll(path string) (map[string]interface{}, error) {
	if pathData, exists := m.data[path]; exists {
		return pathData, nil
	}

	return nil, vault.ErrPathNotFound
}

func (m *MockSafe) Exists(path string) (bool, error) {
	_, exists := m.data[path]

	return exists, nil
}

func (m *MockSafe) Delete(path, key string) error {
	if key == "" {
		delete(m.data, path)
	} else if pathData, exists := m.data[path]; exists {
		delete(pathData, key)
	}

	return nil
}

func (m *MockSafe) List(path string) ([]string, error) {
	var keys []string

	for key := range m.data {
		if len(key) > len(path) && key[:len(path)] == path {
			keys = append(keys, key[len(path):])
		}
	}

	return keys, nil
}

func (m *MockSafe) Export(path string) (map[string]interface{}, error) {
	result := make(map[string]interface{})

	for dataPath, data := range m.data {
		if len(dataPath) >= len(path) && dataPath[:len(path)] == path {
			relativePath := dataPath[len(path):]
			if relativePath == "" {
				// Exact match
				for k, v := range data {
					result[k] = v
				}
			} else {
				result[relativePath] = data
			}
		}
	}

	return result, nil
}

func (m *MockSafe) Import(path string, data map[string]interface{}) error {
	m.data[path] = data

	return nil
}

func (m *MockSafe) GetEngineInfo(path string) (*vault.EngineInfo, error) {
	return &vault.EngineInfo{
		Type:    "kv-v2",
		Version: "2",
		Path:    "secret",
	}, nil
}

func (m *MockSafe) MustGet(path, key string) interface{} {
	value, err := m.Get(path, key)
	if err != nil {
		panic(err)
	}

	return value
}

func (m *MockSafe) GetString(path, key string) (string, error) {
	value, err := m.Get(path, key)
	if err != nil {
		return "", err
	}

	if str, ok := value.(string); ok {
		return str, nil
	}

	return "", vault.ErrNotAString
}

func (m *MockSafe) GetJSON(path, key string) ([]byte, error) {
	return nil, vault.ErrNotImplementedInMock
}
