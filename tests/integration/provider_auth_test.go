package integration_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/commands"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/stackit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProviderAuthenticationFlow tests the complete authentication flow.
func testStackitAuthWithToken(t *testing.T, tmpDir string) {
	t.Helper()
	t.Parallel()

	configFile := filepath.Join(tmpDir, "stackit-token-config.yml")

	testConfig := `
blocs:
  test:
    name: test
    provider: stackit
    service_account_token: "test-token-value"
    environment: test
    region: eu-de-1
    project_id: test-project-123
    organization_id: test-org-456
`

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	cfg, err := config.LoadWithParams(configFile, "test")
	require.NoError(t, err)

	assert.Equal(t, "stackit", cfg.Provider)
	assert.Equal(t, "test-token-value", cfg.ServiceAccountToken)

	stackitConfig := &stackit.Config{
		ProjectID:           "test-project-123",
		OrgID:               "test-org-456",
		AuthToken:           cfg.ServiceAccountToken,
		ServiceAccountToken: "",
		ServiceAccountJSON:  "",
		Region:              "eu-de-1",
		BaseURL:             "",
		Timeout:             0,
		MaxRetries:          0,
	}

	client, err := stackit.NewClient(stackitConfig)
	require.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, "stackit", client.Name())
	assert.Equal(t, "eu-de-1", client.Region())
}

func testStackitAuthWithJSON(t *testing.T, tmpDir string) {
	t.Helper()
	t.Parallel()

	configFile := filepath.Join(tmpDir, "stackit-json-config.yml")
	// #nosec G101 -- Test fixture with mock credentials
	serviceAccountJSON := `{
  "type": "service_account",
  "project_id": "test-project-json",
  "private_key": "-----BEGIN PRIVATE KEY-----\ntest-private-key\n-----END PRIVATE KEY-----",
  "client_email": "test@example.com"
}`

	testConfig := fmt.Sprintf(`
blocs:
  test:
    name: test
    provider: stackit
    service_account_json: '%s'
    environment: test
    region: eu-west-1
    project_id: test-project-json
`, strings.ReplaceAll(serviceAccountJSON, "'", "''"))

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	cfg, err := config.LoadWithParams(configFile, "test")
	require.NoError(t, err)

	assert.Equal(t, "stackit", cfg.Provider)
	assert.Contains(t, cfg.ServiceAccountJSON, "service_account")
	assert.Contains(t, cfg.ServiceAccountJSON, "test-project-json")
}

func testStackitAuthWithKeyPath(t *testing.T, tmpDir string) {
	t.Helper()
	t.Parallel()

	keyPath := filepath.Join(tmpDir, "service-account-key.json")
	// #nosec G101 -- Test fixture with mock credentials
	serviceAccountContent := `{
  "type": "service_account",
  "project_id": "test-project-keypath",
  "private_key": "-----BEGIN PRIVATE KEY-----\ntest-key-content\n-----END PRIVATE KEY-----",
  "client_email": "service@test-project-keypath.iam.stackit.cloud"
}`

	err := os.WriteFile(keyPath, []byte(serviceAccountContent), 0600)
	require.NoError(t, err)

	configFile := filepath.Join(tmpDir, "stackit-keypath-config.yml")
	testConfig := `
blocs:
  test:
    name: test
    provider: stackit
    service_account_key_path: ` + keyPath + `
    environment: test
    region: eu-central-1
    project_id: test-project-keypath
`

	err = os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	cfg, err := config.LoadWithParams(configFile, "test")
	require.NoError(t, err)

	assert.Equal(t, "stackit", cfg.Provider)
	assert.Equal(t, keyPath, cfg.ServiceAccountKeyPath)

	keyContent, err := os.ReadFile(cfg.ServiceAccountKeyPath)
	require.NoError(t, err)
	assert.Contains(t, string(keyContent), "service_account")
	assert.Contains(t, string(keyContent), "test-project-keypath")
}

func testProviderCommandWithStackitCLI(t *testing.T, tmpDir string) {
	t.Helper()
	t.Parallel()

	_, err := exec.LookPath("stackit")
	if err != nil {
		t.Skip("stackit CLI not available, skipping CLI integration test")
	}

	configFile := filepath.Join(tmpDir, "cli-integration-config.yml")
	testConfig := `
name: cli-integration-test
provider: stackit
service_account_token: "fake-token-for-testing"
blocs:
  - name: test
    provider: stackit
    environment: test
    region: eu-de-1
    project_id: fake-project-id
    organization_id: fake-org-id
`

	err = os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	t.Setenv("OCFP_CONFIG", configFile)

	cmd := commands.NewProviderCmd()
	cmd.SetArgs([]string{"login", "--iaas", "stackit", "--bloc", "test"})

	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not retrieve STACKIT service account credentials")
}

func TestProviderAuthenticationFlow(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping provider authentication integration tests in short mode")
	}

	tmpDir := t.TempDir()

	t.Run("StackitAuthWithToken", func(t *testing.T) {
		t.Parallel()
		testStackitAuthWithToken(t, tmpDir)
	})

	t.Run("StackitAuthWithJSON", func(t *testing.T) {
		t.Parallel()
		testStackitAuthWithJSON(t, tmpDir)
	})

	t.Run("StackitAuthWithKeyPath", func(t *testing.T) {
		t.Parallel()
		testStackitAuthWithKeyPath(t, tmpDir)
	})

	t.Run("ProviderCommandWithStackitCLI", func(t *testing.T) {
		t.Parallel()
		testProviderCommandWithStackitCLI(t, tmpDir)
	})
}

func testValidateProviderTypes(t *testing.T, tmpDir string) {
	t.Helper()
	t.Parallel()

	configFile := filepath.Join(tmpDir, "validation-config.yml")

	testConfig := `
name: validation-test
provider: stackit
blocs:
  - name: stackit-bloc
    provider: stackit
    environment: test
  - name: aws-bloc
    provider: aws
    environment: test
  - name: openstack-bloc
    provider: openstack
    environment: test
  - name: gcp-bloc
    provider: gcp
    environment: test
  - name: azure-bloc
    provider: azure
    environment: test
`

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	t.Setenv("OCFP_CONFIG", configFile)

	providers := []string{"stackit", "aws", "openstack", "gcp", "azure"}
	blocs := []string{"stackit-bloc", "aws-bloc", "openstack-bloc", "gcp-bloc", "azure-bloc"}

	for i, provider := range providers {
		cmd := commands.NewProviderCmd()
		cmd.SetArgs([]string{"login", "--iaas", provider, "--bloc", blocs[i]})

		err := cmd.Execute()

		if provider == "stackit" {
			require.Error(t, err)
			assert.Contains(t, err.Error(), "could not retrieve STACKIT service account credentials")
		} else {
			assert.NoError(t, err)
		}
	}
}

func testValidateEnvironmentVariables(t *testing.T, tmpDir string) {
	t.Helper()
	t.Parallel()
	t.Setenv("OCFP_PROVIDER", "aws")
	t.Setenv("OCFP_BLOC_NAME", "test-bloc")

	defer func() { _ = os.Unsetenv("OCFP_PROVIDER") }()
	defer func() { _ = os.Unsetenv("OCFP_BLOC_NAME") }()

	configFile := filepath.Join(tmpDir, "env-var-config.yml")
	testConfig := `
name: env-var-test
provider: stackit
blocs:
  - name: test-bloc
    provider: aws
    environment: test
`

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	t.Setenv("OCFP_CONFIG", configFile)

	cmd := commands.NewProviderCmd()
	cmd.SetArgs([]string{"login"})

	err = cmd.Execute()
	assert.NoError(t, err)
}

func testInvalidProviderHandling(t *testing.T, tmpDir string) {
	t.Helper()
	t.Parallel()

	configFile := filepath.Join(tmpDir, "invalid-provider-config.yml")
	testConfig := `
name: invalid-test
provider: unknown-provider
blocs:
  - name: test
    provider: unknown-provider
    environment: test
`

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	t.Setenv("OCFP_CONFIG", configFile)

	cmd := commands.NewProviderCmd()
	cmd.SetArgs([]string{"login", "--iaas", "unknown-provider", "--bloc", "test"})

	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported provider")
}

// TestProviderValidationFlow tests provider validation logic.
func TestProviderValidationFlow(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	t.Run("ValidateProviderTypes", func(t *testing.T) {
		t.Parallel()
		testValidateProviderTypes(t, tmpDir)
	})

	t.Run("ValidateEnvironmentVariables", func(t *testing.T) {
		t.Parallel()
		testValidateEnvironmentVariables(t, tmpDir)
	})

	t.Run("InvalidProviderHandling", func(t *testing.T) {
		t.Parallel()
		testInvalidProviderHandling(t, tmpDir)
	})
}

func testStackitClientNetworkOperations(t *testing.T) {
	t.Parallel()

	config := &stackit.Config{
		ProjectID:           "test-project",
		OrgID:               "test-org",
		AuthToken:           "test-token",
		ServiceAccountToken: "",
		ServiceAccountJSON:  "",
		Region:              "eu-de-1",
		BaseURL:             "https://api.stackit.cloud",
		Timeout:             30 * time.Second,
		MaxRetries:          0,
	}

	client, err := stackit.NewClient(config)
	require.NoError(t, err)

	networkManager := client.Network()
	assert.NotNil(t, networkManager)

	computeManager := client.Compute()
	assert.NotNil(t, computeManager)

	storageManager := client.Storage()
	assert.NotNil(t, storageManager)

	securityManager := client.Security()
	assert.NotNil(t, securityManager)

	loadBalancerManager := client.LoadBalancer()
	assert.NotNil(t, loadBalancerManager)
}

func testStackitClientInitialization(t *testing.T) {
	t.Parallel()

	config := &stackit.Config{
		ProjectID:           "test-project",
		OrgID:               "test-org",
		AuthToken:           "test-token",
		ServiceAccountToken: "",
		ServiceAccountJSON:  "",
		Region:              "eu-de-1",
		BaseURL:             "",
		Timeout:             0,
		MaxRetries:          0,
	}

	client := &stackit.Client{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.Initialize(ctx, config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to initialize STACKIT provider")
}

func testStackitClientCleanup(t *testing.T) {
	t.Parallel()

	config := &stackit.Config{
		ProjectID:           "test-project",
		OrgID:               "test-org",
		AuthToken:           "test-token",
		ServiceAccountToken: "",
		ServiceAccountJSON:  "",
		Region:              "eu-de-1",
		BaseURL:             "",
		Timeout:             0,
		MaxRetries:          0,
	}

	client, err := stackit.NewClient(config)
	require.NoError(t, err)

	err = client.Cleanup(context.Background())
	assert.NoError(t, err)
}

// TestProviderNetworkFlow tests network-related provider functionality.
func TestProviderNetworkFlow(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping network flow tests in short mode")
	}

	t.Run("StackitClientNetworkOperations", testStackitClientNetworkOperations)
	t.Run("StackitClientInitialization", testStackitClientInitialization)
	t.Run("StackitClientCleanup", testStackitClientCleanup)
}

func testCredentialPriority(t *testing.T, tmpDir string) {
	t.Helper()
	t.Parallel()

	configFile := filepath.Join(tmpDir, "priority-config.yml")
	testConfig := `
blocs:
  test:
    name: test
    provider: stackit
    service_account_token: "config-file-token"
    environment: test
    region: eu-de-1
    project_id: test-project
`

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	cfg, err := config.LoadWithParams(configFile, "test")
	require.NoError(t, err)

	assert.Equal(t, "config-file-token", cfg.ServiceAccountToken)
}

func testMultipleCredentialFormats(t *testing.T, tmpDir string) {
	t.Helper()
	t.Parallel()

	configFile := filepath.Join(tmpDir, "multi-format-config.yml")
	keyPath := filepath.Join(tmpDir, "multi-key.json")

	// #nosec G101 -- Test fixture with mock credentials
	keyContent := `{"type": "service_account", "project_id": "multi-test"}`
	err := os.WriteFile(keyPath, []byte(keyContent), 0600)
	require.NoError(t, err)

	testConfig := `
blocs:
  test:
    name: test
    provider: stackit
    service_account_token: "token-value"
    service_account_json: |
      {"type": "service_account", "project_id": "json-project"}
    service_account_key_path: ` + keyPath + `
    environment: test
`

	err = os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	cfg, err := config.LoadWithParams(configFile, "test")
	require.NoError(t, err)

	assert.Equal(t, "token-value", cfg.ServiceAccountToken)
	assert.Contains(t, cfg.ServiceAccountJSON, "json-project")
	assert.Equal(t, keyPath, cfg.ServiceAccountKeyPath)
}

func testVaultIntegrationCheck(t *testing.T) {
	t.Parallel()

	_, safeErr := exec.LookPath("safe")
	_, vaultErr := exec.LookPath("vault")

	if safeErr != nil && vaultErr != nil {
		t.Skip("neither safe nor vault CLI available, skipping vault integration test")
	}

	assert.True(t, safeErr == nil || vaultErr == nil, "at least one vault tool should be available")
}

// TestProviderCredentialDiscovery tests credential discovery mechanisms.
func TestProviderCredentialDiscovery(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	t.Run("CredentialPriority", func(t *testing.T) {
		t.Parallel()
		testCredentialPriority(t, tmpDir)
	})

	t.Run("MultipleCredentialFormats", func(t *testing.T) {
		t.Parallel()
		testMultipleCredentialFormats(t, tmpDir)
	})

	t.Run("VaultIntegrationCheck", testVaultIntegrationCheck)
}
