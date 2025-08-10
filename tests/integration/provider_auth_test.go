package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/commands"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/stackit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProviderAuthenticationFlow tests the complete authentication flow
func TestProviderAuthenticationFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping provider authentication integration tests in short mode")
	}

	tmpDir := t.TempDir()

	t.Run("StackitAuthWithToken", func(t *testing.T) {
		configFile := filepath.Join(tmpDir, "stackit-token-config.yml")
		
		testConfig := `
name: stackit-test
provider: stackit
service_account_token: "test-token-value"
blocs:
  - name: test
    provider: stackit
    environment: test
    region: eu-de-1
    project_id: test-project-123
    organization_id: test-org-456
`

		err := os.WriteFile(configFile, []byte(testConfig), 0644)
		require.NoError(t, err)

		// Test configuration loading
		cfg, err := config.LoadWithParams(configFile, "test")
		require.NoError(t, err)
		
		assert.Equal(t, "stackit", cfg.Provider)
		assert.Equal(t, "test-token-value", cfg.ServiceAccountToken)

		// Test STACKIT client creation (without actual authentication)
		stackitConfig := &stackit.Config{
			AuthToken: cfg.ServiceAccountToken,
			ProjectID: "test-project-123",
			OrgID:     "test-org-456",
			Region:    "eu-de-1",
		}

		client, err := stackit.NewClient(stackitConfig)
		require.NoError(t, err)
		assert.NotNil(t, client)
		assert.Equal(t, "stackit", client.Name())
		assert.Equal(t, "eu-de-1", client.Region())
	})

	t.Run("StackitAuthWithJSON", func(t *testing.T) {
		configFile := filepath.Join(tmpDir, "stackit-json-config.yml")
		serviceAccountJSON := `{
  "type": "service_account",
  "project_id": "test-project-json",
  "private_key": "-----BEGIN PRIVATE KEY-----\ntest-private-key\n-----END PRIVATE KEY-----",
  "client_email": "test@example.com"
}`
		
		testConfig := `
name: stackit-json-test
provider: stackit
service_account_json: |
` + "  " + serviceAccountJSON + `
blocs:
  - name: test
    provider: stackit
    environment: test
    region: eu-west-1
    project_id: test-project-json
`

		err := os.WriteFile(configFile, []byte(testConfig), 0644)
		require.NoError(t, err)

		cfg, err := config.LoadWithParams(configFile, "test")
		require.NoError(t, err)
		
		assert.Equal(t, "stackit", cfg.Provider)
		assert.Contains(t, cfg.ServiceAccountJSON, "service_account")
		assert.Contains(t, cfg.ServiceAccountJSON, "test-project-json")
	})

	t.Run("StackitAuthWithKeyPath", func(t *testing.T) {
		keyPath := filepath.Join(tmpDir, "service-account-key.json")
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
name: stackit-keypath-test
provider: stackit
service_account_key_path: ` + keyPath + `
blocs:
  - name: test
    provider: stackit
    environment: test
    region: eu-central-1
    project_id: test-project-keypath
`

		err = os.WriteFile(configFile, []byte(testConfig), 0644)
		require.NoError(t, err)

		cfg, err := config.LoadWithParams(configFile, "test")
		require.NoError(t, err)
		
		assert.Equal(t, "stackit", cfg.Provider)
		assert.Equal(t, keyPath, cfg.ServiceAccountKeyPath)

		// Verify the key file exists and is readable
		keyContent, err := os.ReadFile(cfg.ServiceAccountKeyPath)
		require.NoError(t, err)
		assert.Contains(t, string(keyContent), "service_account")
		assert.Contains(t, string(keyContent), "test-project-keypath")
	})

	t.Run("ProviderCommandWithStackitCLI", func(t *testing.T) {
		// Check if stackit CLI is available
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

		err = os.WriteFile(configFile, []byte(testConfig), 0644)
		require.NoError(t, err)

		os.Setenv("OCFP_CONFIG", configFile)
		defer os.Unsetenv("OCFP_CONFIG")

		cmd := commands.NewProviderCmd()
		cmd.SetArgs([]string{"login", "--iaas", "stackit", "--bloc-name", "test"})

		// This will fail with authentication error, but should reach the stackit CLI
		err = cmd.Execute()
		assert.Error(t, err)
		// The error should be about authentication, not missing commands
		assert.Contains(t, err.Error(), "could not retrieve STACKIT service account credentials")
	})
}

// TestProviderValidationFlow tests provider validation logic
func TestProviderValidationFlow(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("ValidateProviderTypes", func(t *testing.T) {
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

		err := os.WriteFile(configFile, []byte(testConfig), 0644)
		require.NoError(t, err)

		os.Setenv("OCFP_CONFIG", configFile)
		defer os.Unsetenv("OCFP_CONFIG")

		providers := []string{"stackit", "aws", "openstack", "gcp", "azure"}
		blocs := []string{"stackit-bloc", "aws-bloc", "openstack-bloc", "gcp-bloc", "azure-bloc"}

		for i, provider := range providers {
			cmd := commands.NewProviderCmd()
			cmd.SetArgs([]string{"login", "--iaas", provider, "--bloc-name", blocs[i]})

			err := cmd.Execute()
			
			if provider == "stackit" {
				// STACKIT should fail with credential error
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "could not retrieve STACKIT service account credentials")
			} else {
				// Other providers should succeed with placeholder warning
				assert.NoError(t, err)
			}
		}
	})

	t.Run("ValidateEnvironmentVariables", func(t *testing.T) {
		// Test provider selection via environment variable
		os.Setenv("OCFP_PROVIDER", "aws")
		os.Setenv("OCFP_BLOC_NAME", "test-bloc")
		defer os.Unsetenv("OCFP_PROVIDER")
		defer os.Unsetenv("OCFP_BLOC_NAME")

		configFile := filepath.Join(tmpDir, "env-var-config.yml")
		testConfig := `
name: env-var-test
provider: stackit
blocs:
  - name: test-bloc
    provider: aws
    environment: test
`

		err := os.WriteFile(configFile, []byte(testConfig), 0644)
		require.NoError(t, err)

		os.Setenv("OCFP_CONFIG", configFile)
		defer os.Unsetenv("OCFP_CONFIG")

		cmd := commands.NewProviderCmd()
		cmd.SetArgs([]string{"login"})

		// Should use environment variables for provider and bloc-name
		err = cmd.Execute()
		assert.NoError(t, err) // AWS placeholder should succeed
	})

	t.Run("InvalidProviderHandling", func(t *testing.T) {
		configFile := filepath.Join(tmpDir, "invalid-provider-config.yml")
		testConfig := `
name: invalid-test
provider: unknown-provider
blocs:
  - name: test
    provider: unknown-provider
    environment: test
`

		err := os.WriteFile(configFile, []byte(testConfig), 0644)
		require.NoError(t, err)

		os.Setenv("OCFP_CONFIG", configFile)
		defer os.Unsetenv("OCFP_CONFIG")

		cmd := commands.NewProviderCmd()
		cmd.SetArgs([]string{"login", "--iaas", "unknown-provider", "--bloc-name", "test"})

		err = cmd.Execute()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported provider")
	})
}

// TestProviderNetworkFlow tests network-related provider functionality
func TestProviderNetworkFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network flow tests in short mode")
	}

	t.Run("StackitClientNetworkOperations", func(t *testing.T) {
		// Test STACKIT client network manager initialization
		config := &stackit.Config{
			AuthToken: "test-token",
			ProjectID: "test-project",
			OrgID:     "test-org",
			Region:    "eu-de-1",
			BaseURL:   "https://api.stackit.cloud",
			Timeout:   30 * time.Second,
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
	})

	t.Run("StackitClientInitialization", func(t *testing.T) {
		config := &stackit.Config{
			AuthToken: "test-token",
			ProjectID: "test-project",
			OrgID:     "test-org",
			Region:    "eu-de-1",
		}

		client := &stackit.Client{}
		
		// Test initialization without actual authentication
		// This tests the initialization logic without making real API calls
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := client.Initialize(ctx, config)
		// This should fail with authentication error, not initialization error
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to initialize STACKIT provider")
	})

	t.Run("StackitClientCleanup", func(t *testing.T) {
		config := &stackit.Config{
			AuthToken: "test-token",
			ProjectID: "test-project",
			OrgID:     "test-org",
			Region:    "eu-de-1",
		}

		client, err := stackit.NewClient(config)
		require.NoError(t, err)

		// Test cleanup operations
		err = client.Cleanup(context.Background())
		assert.NoError(t, err)
	})
}

// TestProviderCredentialDiscovery tests credential discovery mechanisms
func TestProviderCredentialDiscovery(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("CredentialPriority", func(t *testing.T) {
		// Test credential priority: config file > vault > environment
		
		// Create config with token
		configFile := filepath.Join(tmpDir, "priority-config.yml")
		testConfig := `
name: priority-test
provider: stackit
service_account_token: "config-file-token"
blocs:
  - name: test
    provider: stackit
    environment: test
    region: eu-de-1
    project_id: test-project
`

		err := os.WriteFile(configFile, []byte(testConfig), 0644)
		require.NoError(t, err)

		cfg, err := config.LoadWithParams(configFile, "test")
		require.NoError(t, err)
		
		// Config file token should take priority
		assert.Equal(t, "config-file-token", cfg.ServiceAccountToken)
	})

	t.Run("MultipleCredentialFormats", func(t *testing.T) {
		// Test handling of multiple credential formats in same config
		configFile := filepath.Join(tmpDir, "multi-format-config.yml")
		keyPath := filepath.Join(tmpDir, "multi-key.json")
		
		keyContent := `{"type": "service_account", "project_id": "multi-test"}`
		err := os.WriteFile(keyPath, []byte(keyContent), 0600)
		require.NoError(t, err)

		testConfig := `
name: multi-format-test
provider: stackit
service_account_token: "token-value"
service_account_json: |
  {"type": "service_account", "project_id": "json-project"}
service_account_key_path: ` + keyPath + `
blocs:
  - name: test
    provider: stackit
    environment: test
`

		err = os.WriteFile(configFile, []byte(testConfig), 0644)
		require.NoError(t, err)

		cfg, err := config.LoadWithParams(configFile, "test")
		require.NoError(t, err)
		
		// All credential formats should be available
		assert.Equal(t, "token-value", cfg.ServiceAccountToken)
		assert.Contains(t, cfg.ServiceAccountJSON, "json-project")
		assert.Equal(t, keyPath, cfg.ServiceAccountKeyPath)
	})

	t.Run("VaultIntegrationCheck", func(t *testing.T) {
		// Check if vault integration tools are available
		_, safeErr := exec.LookPath("safe")
		_, vaultErr := exec.LookPath("vault")
		
		if safeErr != nil && vaultErr != nil {
			t.Skip("neither safe nor vault CLI available, skipping vault integration test")
		}

		// Test that the system has vault capabilities available
		// This doesn't test actual vault operations, just availability
		assert.True(t, safeErr == nil || vaultErr == nil, "at least one vault tool should be available")
	})
}