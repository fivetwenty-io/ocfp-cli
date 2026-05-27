//go:build integration
// +build integration

package integration_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBastionConfigFiltering tests the config filtering behavior for bastion init.
// This integration test verifies that when a multi-bloc config is processed,
// only the target bloc and global settings are included in the filtered output.
func TestBastionConfigFiltering(t *testing.T) {
	t.Parallel()

	// Setup: Create a multi-bloc config file
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yml")

	testConfig := `debug: true
verbose: false
blocs:
  prod-aws:
    name: prod-aws
    provider: aws
    region: us-east-1
    access_key_id: AKIA_PROD
    secret_access_key: secret_prod_key
    network:
      cidr: 10.0.0.0/16
  prod-stackit:
    name: prod-stackit
    provider: stackit
    region: eu-01
    project_id: prod-project
    auth_token: stackit_token
    network:
      cidr: 10.1.0.0/16
  dev-aws:
    name: dev-aws
    provider: aws
    region: us-west-2
    access_key_id: AKIA_DEV
    secret_access_key: secret_dev_key
    network:
      cidr: 10.2.0.0/16
`

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err, "Failed to write test config file")

	// Load the full config file
	var fullConfig config.ConfigFile
	fullConfigBytes, err := os.ReadFile(configFile)
	require.NoError(t, err)

	err = yaml.Unmarshal(fullConfigBytes, &fullConfig)
	require.NoError(t, err)

	// Verify full config has all blocs
	assert.Len(t, fullConfig.Blocs, 3, "Full config should have 3 blocs")
	assert.True(t, fullConfig.Debug, "Full config should have debug=true")
	assert.False(t, fullConfig.Verbose, "Full config should have verbose=false")

	// Simulate filtering for prod-aws bloc
	targetBloc := "prod-aws"
	blocConfig, exists := fullConfig.Blocs[targetBloc]
	require.True(t, exists, "Target bloc should exist in full config")

	filteredConfig := &config.ConfigFile{
		Debug:   fullConfig.Debug,
		Verbose: fullConfig.Verbose,
		Blocs: map[string]*config.Config{
			targetBloc: blocConfig,
		},
	}

	// Write filtered config to temp file
	filteredFile := filepath.Join(tmpDir, "filtered-config.yml")
	filteredBytes, err := yaml.Marshal(filteredConfig)
	require.NoError(t, err, "Failed to marshal filtered config")

	err = os.WriteFile(filteredFile, filteredBytes, 0600)
	require.NoError(t, err, "Failed to write filtered config")

	// Verify filtered config file
	var loadedFiltered config.ConfigFile
	loadedBytes, err := os.ReadFile(filteredFile)
	require.NoError(t, err)

	err = yaml.Unmarshal(loadedBytes, &loadedFiltered)
	require.NoError(t, err)

	// Assertions
	assert.True(t, loadedFiltered.Debug, "Filtered config should preserve debug setting")
	assert.False(t, loadedFiltered.Verbose, "Filtered config should preserve verbose setting")
	assert.Len(t, loadedFiltered.Blocs, 1, "Filtered config should have exactly 1 bloc")
	assert.Contains(t, loadedFiltered.Blocs, "prod-aws", "Filtered config should contain target bloc")
	assert.NotContains(t, loadedFiltered.Blocs, "prod-stackit", "Filtered config should not contain other blocs")
	assert.NotContains(t, loadedFiltered.Blocs, "dev-aws", "Filtered config should not contain other blocs")

	// Verify bloc config is complete
	prodAWS := loadedFiltered.Blocs["prod-aws"]
	assert.Equal(t, "prod-aws", prodAWS.Name)
	assert.Equal(t, "aws", prodAWS.Provider)
	assert.Equal(t, "us-east-1", prodAWS.Region)
	assert.Equal(t, "AKIA_PROD", prodAWS.AccessKeyID)
	assert.Equal(t, "secret_prod_key", prodAWS.SecretAccessKey)
	assert.Equal(t, "10.0.0.0/16", prodAWS.Network.CIDR)
}

// TestBastionConfigFilteringMultipleBlocs tests filtering with different target blocs.
func TestBastionConfigFilteringMultipleBlocs(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yml")

	testConfig := `debug: false
verbose: true
blocs:
  bloc-a:
    name: bloc-a
    provider: stackit
    region: eu-01
  bloc-b:
    name: bloc-b
    provider: aws
    region: us-east-1
  bloc-c:
    name: bloc-c
    provider: aws
    region: ap-southeast-1
`

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	testCases := []struct {
		name       string
		targetBloc string
		provider   string
		region     string
	}{
		{
			name:       "filter bloc-a",
			targetBloc: "bloc-a",
			provider:   "stackit",
			region:     "eu-01",
		},
		{
			name:       "filter bloc-b",
			targetBloc: "bloc-b",
			provider:   "aws",
			region:     "us-east-1",
		},
		{
			name:       "filter bloc-c",
			targetBloc: "bloc-c",
			provider:   "aws",
			region:     "ap-southeast-1",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Load full config
			var fullConfig config.ConfigFile
			fullBytes, err := os.ReadFile(configFile)
			require.NoError(t, err)

			err = yaml.Unmarshal(fullBytes, &fullConfig)
			require.NoError(t, err)

			// Filter for target bloc
			blocConfig := fullConfig.Blocs[tc.targetBloc]
			filteredConfig := &config.ConfigFile{
				Debug:   fullConfig.Debug,
				Verbose: fullConfig.Verbose,
				Blocs: map[string]*config.Config{
					tc.targetBloc: blocConfig,
				},
			}

			// Verify filtered config
			assert.Len(t, filteredConfig.Blocs, 1)
			assert.Contains(t, filteredConfig.Blocs, tc.targetBloc)

			targetConfig := filteredConfig.Blocs[tc.targetBloc]
			assert.Equal(t, tc.provider, targetConfig.Provider)
			assert.Equal(t, tc.region, targetConfig.Region)
		})
	}
}

// TestBastionConfigFilteringSecurity verifies security aspects of config filtering.
func TestBastionConfigFilteringSecurity(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yml")

	// Config with sensitive credentials for multiple blocs
	testConfig := `blocs:
  prod:
    name: prod
    provider: aws
    access_key_id: AKIA_PROD_SECRET
    secret_access_key: prod_secret_key_123
  dev:
    name: dev
    provider: aws
    access_key_id: AKIA_DEV_SECRET
    secret_access_key: dev_secret_key_456
  staging:
    name: staging
    provider: stackit
    auth_token: staging_token_789
`

	err := os.WriteFile(configFile, []byte(testConfig), 0600)
	require.NoError(t, err)

	// Load and filter for 'prod' only
	var fullConfig config.ConfigFile
	fullBytes, err := os.ReadFile(configFile)
	require.NoError(t, err)

	err = yaml.Unmarshal(fullBytes, &fullConfig)
	require.NoError(t, err)

	filteredConfig := &config.ConfigFile{
		Blocs: map[string]*config.Config{
			"prod": fullConfig.Blocs["prod"],
		},
	}

	filteredBytes, err := yaml.Marshal(filteredConfig)
	require.NoError(t, err)

	filteredStr := string(filteredBytes)

	// Security assertions: filtered config should NOT contain credentials from other blocs
	assert.Contains(t, filteredStr, "AKIA_PROD_SECRET", "Should contain prod credentials")
	assert.Contains(t, filteredStr, "prod_secret_key_123", "Should contain prod secret")

	assert.NotContains(t, filteredStr, "AKIA_DEV_SECRET", "Should NOT contain dev credentials")
	assert.NotContains(t, filteredStr, "dev_secret_key_456", "Should NOT contain dev secret")
	assert.NotContains(t, filteredStr, "staging_token_789", "Should NOT contain staging credentials")

	// Verify only one bloc exists
	assert.Len(t, filteredConfig.Blocs, 1, "Filtered config should have exactly 1 bloc")
	assert.Contains(t, filteredConfig.Blocs, "prod", "Filtered config should only contain prod bloc")
}
