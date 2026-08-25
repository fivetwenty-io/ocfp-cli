package vault_test

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

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

	// Test network paths
	assert.Equal(t, "secret/config/test-bloc/mgmt/net", pathBuilder.GetNetPath("mgmt"))
	assert.Equal(t, "secret/config/test-bloc/ocf/net", pathBuilder.GetNetPath("ocf"))

	// Test subnet paths
	assert.Equal(t, "secret/config/test-bloc/mgmt/net/subnets", pathBuilder.GetSubnetsPath("mgmt"))
	assert.Equal(t, "secret/config/test-bloc/mgmt/net/subnets/ocfp-0", pathBuilder.GetSubnetPath("mgmt", "ocfp", 0))
	assert.Equal(t, "secret/config/test-bloc/ocf/net/subnets/services-1", pathBuilder.GetSubnetPath("ocf", "services", 1))

	// Test BOSH paths
	assert.Equal(t, "secret/config/test-bloc/mgmt/bosh", pathBuilder.GetBOSHPath("mgmt"))
	assert.Equal(t, "secret/config/test-bloc/mgmt/bosh/iam", pathBuilder.GetIAMPath("mgmt"))
	assert.Equal(t, "secret/config/test-bloc/mgmt/bosh/s3", pathBuilder.GetS3Path("mgmt"))
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
	info, err := pathBuilder.ParsePath("secret/config/test-bloc/mgmt/net/subnets")
	require.NoError(t, err)
	assert.Equal(t, "mgmt", info.Environment)
	assert.Equal(t, "net", info.Component)
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

	// Test retryable error. Same attempt/backoff shape as the defaults but
	// with millisecond delays: the behavior under test is the retry decision,
	// not the production wait times.
	fastConfig := vault.DefaultRetryConfig()
	fastConfig.BaseDelay = time.Millisecond
	fastConfig.MaxDelay = 5 * time.Millisecond

	attempts = 0
	err = vault.WithRetry(func() error {
		attempts++
		if attempts < 3 {
			return vault.ErrConnectionTimeout // Retryable error
		}

		return nil
	}, fastConfig)

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
		Bastion:     createTestBastionConfig(),
		Genesis:     createTestGenesisConfig(),
		Deployments: config.NewDeploymentSettings("", nil),
		DNS:         []string{"1.1.1.1", "8.8.8.8"},
		AZs:         createTestAZConfig(),
		Routers:     config.ComponentConfig{},
		Cells:       config.ComponentConfig{},
		Subnets:     createTestSubnetsConfig(),
		Blobstore:   createTestBlobstoreConfig(),
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

	err := provider.SaveConfigToVault(nil, 1, 1)
	require.NoError(t, err)

	err = provider.ConfigurePublicIPs(nil, 1, 1)
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

func (m *MockSafe) GetEngineInfo(_path string) (*vault.EngineInfo, error) {
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

func (m *MockSafe) GetJSON(_path, _key string) ([]byte, error) {
	return nil, vault.ErrNotImplementedInMock
}

// TestConfigCompressionFormat tests that SaveConfigToVault uses gzip compression.
// This test verifies the format matches Perl implementation: Base64(gzip(JSON))
func TestConfigCompressionFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
	}{
		{
			name:     "STACKIT provider config compression",
			provider: "stackit",
		},
		{
			name:     "AWS provider config compression",
			provider: "aws",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create test config
			cfg := createTestConfigForProvider(tt.provider)
			mockSafe := &MockSafe{data: make(map[string]map[string]interface{})}

			// Create provider based on type
			var err error
			switch tt.provider {
			case "stackit":
				stackitProvider := vault.NewStackitVaultProvider(cfg, mockSafe, "test-bloc")
				err = stackitProvider.SaveConfigToVault(nil, 1, 1)
			case "aws":
				awsProvider := vault.NewAWSVaultProvider(cfg, mockSafe, "test-bloc")
				err = awsProvider.SaveConfigToVault(nil, 1, 1)
			}

			require.NoError(t, err)

			// Get the stored config value
			configPath := "secret/config/test-bloc/ocfp"
			pathData, exists := mockSafe.data[configPath]
			require.True(t, exists, "Config path should exist in vault")

			encodedValue, ok := pathData["config"].(string)
			require.True(t, ok, "Config value should be a string")
			require.NotEmpty(t, encodedValue, "Encoded value should not be empty")

			// Step 1: Decode Base64
			compressedData, err := base64.StdEncoding.DecodeString(encodedValue)
			require.NoError(t, err, "Should successfully decode base64")
			require.NotEmpty(t, compressedData, "Compressed data should not be empty")

			// Step 2: Decompress gzip
			gzipReader, err := gzip.NewReader(bytes.NewReader(compressedData))
			require.NoError(t, err, "Should successfully create gzip reader")

			var decompressedBuf bytes.Buffer
			_, err = decompressedBuf.ReadFrom(gzipReader)
			require.NoError(t, err, "Should successfully decompress gzip data")
			err = gzipReader.Close()
			require.NoError(t, err, "Should successfully close gzip reader")

			// Step 3: Parse JSON
			var decodedConfig config.Config
			err = json.Unmarshal(decompressedBuf.Bytes(), &decodedConfig)
			require.NoError(t, err, "Should successfully parse JSON from decompressed data")

			// Verify the config matches what we saved
			assert.Equal(t, cfg.Name, decodedConfig.Name, "Config name should match")
			assert.Equal(t, cfg.Provider, decodedConfig.Provider, "Config provider should match")
			assert.Equal(t, cfg.Region, decodedConfig.Region, "Config region should match")

			// Verify compression actually happened (compressed should be smaller than original)
			originalJSON, _ := json.Marshal(cfg)
			compressionRatio := float64(len(compressedData)) / float64(len(originalJSON))
			t.Logf("Compression ratio for %s: %.2f%% (%d bytes -> %d bytes)",
				tt.provider, compressionRatio*100, len(originalJSON), len(compressedData))

			// Compression should reduce size for typical configs
			// Allow up to 120% in case config is too small to compress effectively
			assert.LessOrEqual(t, compressionRatio, 1.2,
				"Compressed data should not be significantly larger than original")
		})
	}
}

// TestCompressionLevel tests that gzip uses maximum compression (level 9).
func TestCompressionLevel(t *testing.T) {
	t.Parallel()

	cfg := createTestStackitConfig()
	mockSafe := &MockSafe{data: make(map[string]map[string]interface{})}
	provider := vault.NewStackitVaultProvider(cfg, mockSafe, "test-bloc")

	err := provider.SaveConfigToVault(nil, 1, 1)
	require.NoError(t, err)

	// Get the stored config
	configPath := "secret/config/test-bloc/ocfp"
	pathData, exists := mockSafe.data[configPath]
	require.True(t, exists)

	encodedValue, ok := pathData["config"].(string)
	require.True(t, ok)

	// Decode to get compressed data
	compressedData, err := base64.StdEncoding.DecodeString(encodedValue)
	require.NoError(t, err)

	// Create a reference compression at different levels
	jsonData, _ := json.Marshal(cfg)

	var level1Buf, level9Buf bytes.Buffer

	// Level 1 (fastest)
	writer1, _ := gzip.NewWriterLevel(&level1Buf, gzip.BestSpeed)
	writer1.Write(jsonData)
	writer1.Close()

	// Level 9 (best compression)
	writer9, _ := gzip.NewWriterLevel(&level9Buf, gzip.BestCompression)
	writer9.Write(jsonData)
	writer9.Close()

	// The actual compressed data should match level 9 size more closely than level 1
	actualSize := len(compressedData)
	level1Size := level1Buf.Len()
	level9Size := level9Buf.Len()

	t.Logf("Compression sizes - Level 1: %d, Level 9: %d, Actual: %d",
		level1Size, level9Size, actualSize)

	// Actual size should be closer to level 9 than level 1
	// (or equal to level 9 for small data)
	assert.LessOrEqual(t, actualSize, level1Size,
		"Compressed data should be at least as small as level 1 compression")
	assert.Equal(t, level9Size, actualSize,
		"Compressed data should match level 9 (maximum compression)")
}

// TestErrorHandlingInCompression tests error handling during compression.
func TestErrorHandlingInCompression(t *testing.T) {
	t.Parallel()

	// This test verifies that compression errors are properly handled
	// For now, we test successful compression
	// Future enhancement: test with mock writer that fails
	cfg := createTestStackitConfig()
	mockSafe := &MockSafe{data: make(map[string]map[string]interface{})}
	provider := vault.NewStackitVaultProvider(cfg, mockSafe, "test-bloc")

	err := provider.SaveConfigToVault(nil, 1, 1)
	require.NoError(t, err, "SaveConfigToVault should succeed with valid config")
}

// createTestConfigForProvider creates a test config for a specific provider.
func createTestConfigForProvider(provider string) *config.Config {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: provider,
		Region:   "test-region",
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
		Bastion:     createTestBastionConfig(),
		Genesis:     createTestGenesisConfig(),
		Deployments: config.NewDeploymentSettings("", nil),
		DNS:         []string{"1.1.1.1", "8.8.8.8"},
		AZs:         createTestAZConfig(),
		Routers:     config.ComponentConfig{},
		Cells:       config.ComponentConfig{},
		Subnets:     createTestSubnetsConfig(),
		Blobstore:   createTestBlobstoreConfig(),
	}

	// Add provider-specific fields
	switch provider {
	case "stackit":
		cfg.ProjectID = "test-project"
		cfg.ServiceAccountToken = "test-token"
	case "aws":
		cfg.AccessKeyID = "test-access-key"
		cfg.SecretAccessKey = "test-secret-key"
	}

	return cfg
}
