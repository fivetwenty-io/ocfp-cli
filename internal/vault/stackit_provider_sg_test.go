package vault

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"github.com/stretchr/testify/assert"
)

// TestBuildSecurityGroupMapping tests that all 8 security group types are correctly mapped.
func TestBuildSecurityGroupMapping(t *testing.T) {
	provider := &StackitVaultProvider{
		BaseVaultProvider: &providers.BaseVaultProvider{
			BlocName: "test-bloc",
		},
		logger: logger.Get(),
	}

	sgMapping := provider.buildSecurityGroupMapping()

	// Should have exactly 8 security groups (7 standard types + default)
	assert.Equal(t, 8, len(sgMapping), "Should have 8 security group mappings")

	// Verify standard security groups have bloc prefix
	expectedMappings := map[string]string{
		"bastion":                   "test-bloc-bastion",
		"infra":                     "test-bloc-infra",
		"ocfp":                      "test-bloc-ocfp",
		"lb-ext":                    "test-bloc-lb-ext",
		"ocf-cf-router-ingress":     "test-bloc-ocf-cf-router-ingress",
		"ocf-cf-tcp-router-ingress": "test-bloc-ocf-cf-tcp-router-ingress",
		"ocf-cf-ssh-proxy-ingress":  "test-bloc-ocf-cf-ssh-proxy-ingress",
		"default":                   "default", // default has no prefix
	}

	for sgType, expectedFullName := range expectedMappings {
		actualFullName, exists := sgMapping[sgType]
		assert.True(t, exists, "Security group type %s should exist", sgType)
		assert.Equal(t, expectedFullName, actualFullName, "Security group %s should map to %s", sgType, expectedFullName)
	}
}

// TestStoreSecurityGroupToVault_StandardPath tests that standard SGs use net/sgs/ path.
func TestStoreSecurityGroupToVault_StandardPath(t *testing.T) {
	mockSafe := &MockSafeInterface{}
	provider := &StackitVaultProvider{
		BaseVaultProvider: &providers.BaseVaultProvider{
			Config:   &config.Config{},
			BlocName: "test-bloc",
		},
		Safe:        mockSafe,
		PathBuilder: NewPathBuilder(&config.Config{}, "test-bloc"),
		logger:      logger.Get(),
	}

	sg := map[string]interface{}{
		"id":          "sg-12345",
		"name":        "test-bloc-bastion",
		"description": "Bastion security group",
	}

	// Test standard security group (should go under net/sgs/)
	netPath := "secret/test-bloc/mgmt/net"
	err := provider.storeSecurityGroupToVault(sg, "bastion", "test-bloc-bastion", netPath)

	assert.NoError(t, err)
	assert.Equal(t, 1, len(mockSafe.setCalls), "Should have one SetMultiple call")

	// Verify path is under net/sgs/
	expectedPath := "secret/test-bloc/mgmt/net/sgs/bastion"
	assert.Equal(t, expectedPath, mockSafe.setCalls[0].path, "Standard SG should use net/sgs/ path")

	// Verify data structure
	data := mockSafe.setCalls[0].data
	assert.Equal(t, "sg-12345", data["id"])
	assert.Equal(t, "test-bloc-bastion", data["name"])
	assert.Equal(t, "Bastion security group", data["description"])
}

// TestStoreSecurityGroupToVault_CFSpecificPath tests that CF SGs use net/ path directly.
func TestStoreSecurityGroupToVault_CFSpecificPath(t *testing.T) {
	testCases := []struct {
		name     string
		sgType   string
		sgName   string
		expected string
	}{
		{
			name:     "CF Router Ingress",
			sgType:   "ocf-cf-router-ingress",
			sgName:   "test-bloc-ocf-cf-router-ingress",
			expected: "secret/test-bloc/ocf/net/ocf-cf-router-ingress",
		},
		{
			name:     "CF TCP Router Ingress",
			sgType:   "ocf-cf-tcp-router-ingress",
			sgName:   "test-bloc-ocf-cf-tcp-router-ingress",
			expected: "secret/test-bloc/ocf/net/ocf-cf-tcp-router-ingress",
		},
		{
			name:     "CF SSH Proxy Ingress",
			sgType:   "ocf-cf-ssh-proxy-ingress",
			sgName:   "test-bloc-ocf-cf-ssh-proxy-ingress",
			expected: "secret/test-bloc/ocf/net/ocf-cf-ssh-proxy-ingress",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockSafe := &MockSafeInterface{}
			provider := &StackitVaultProvider{
				BaseVaultProvider: &providers.BaseVaultProvider{
					Config:   &config.Config{},
					BlocName: "test-bloc",
				},
				Safe:        mockSafe,
				PathBuilder: NewPathBuilder(&config.Config{}, "test-bloc"),
				logger:      logger.Get(),
			}

			sg := map[string]interface{}{
				"id":          "sg-cf-12345",
				"name":        tc.sgName,
				"description": "CF security group",
			}

			netPath := "secret/test-bloc/ocf/net"
			err := provider.storeSecurityGroupToVault(sg, tc.sgType, tc.sgName, netPath)

			assert.NoError(t, err)
			assert.Equal(t, 1, len(mockSafe.setCalls), "Should have one SetMultiple call")

			// CRITICAL: Verify CF SGs are stored directly under net/ NOT net/sgs/
			assert.Equal(t, tc.expected, mockSafe.setCalls[0].path,
				"CF SG should use net/{sg_type} path, NOT net/sgs/{sg_type}")
		})
	}
}

// TestStoreSecurityGroupToVault_MissingID tests error handling for SG without ID.
func TestStoreSecurityGroupToVault_MissingID(t *testing.T) {
	mockSafe := &MockSafeInterface{}
	provider := &StackitVaultProvider{
		BaseVaultProvider: &providers.BaseVaultProvider{
			Config:   &config.Config{},
			BlocName: "test-bloc",
		},
		Safe:        mockSafe,
		PathBuilder: NewPathBuilder(&config.Config{}, "test-bloc"),
		logger:      logger.Get(),
	}

	// Security group without ID
	sg := map[string]interface{}{
		"name": "test-bloc-bastion",
	}

	netPath := "secret/test-bloc/mgmt/net"
	err := provider.storeSecurityGroupToVault(sg, "bastion", "test-bloc-bastion", netPath)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing ID")
	assert.Equal(t, 0, len(mockSafe.setCalls), "Should not call SetMultiple")
}

// TestStoreSecurityGroupToVault_DefaultDescription tests that missing description gets default.
func TestStoreSecurityGroupToVault_DefaultDescription(t *testing.T) {
	mockSafe := &MockSafeInterface{}
	provider := &StackitVaultProvider{
		BaseVaultProvider: &providers.BaseVaultProvider{
			Config:   &config.Config{},
			BlocName: "test-bloc",
		},
		Safe:        mockSafe,
		PathBuilder: NewPathBuilder(&config.Config{}, "test-bloc"),
		logger:      logger.Get(),
	}

	// Security group without description
	sg := map[string]interface{}{
		"id":   "sg-12345",
		"name": "test-bloc-bastion",
	}

	netPath := "secret/test-bloc/mgmt/net"
	err := provider.storeSecurityGroupToVault(sg, "bastion", "test-bloc-bastion", netPath)

	assert.NoError(t, err)

	// Verify default description was added
	data := mockSafe.setCalls[0].data
	assert.Equal(t, "Security group for bastion", data["description"])
}

// TestFindSecurityGroup_NotFound tests graceful handling when SG not in state.
func TestFindSecurityGroup_NotFound(t *testing.T) {
	provider := &StackitVaultProvider{
		BaseVaultProvider: &providers.BaseVaultProvider{
			Config:   &config.Config{},
			BlocName: "test-bloc",
		},
		logger: logger.Get(),
	}

	// No state manager - should return nil gracefully
	sg := provider.findSecurityGroup(nil, "bastion", "test-bloc-bastion")
	assert.Nil(t, sg, "Should return nil when SG not found")
}

// TestConfigureSecurityGroups_Integration tests the full configuration flow.
func TestConfigureSecurityGroups_Integration(t *testing.T) {
	mockSafe := &MockSafeInterface{}
	provider := &StackitVaultProvider{
		BaseVaultProvider: &providers.BaseVaultProvider{
			Config:   &config.Config{},
			BlocName: "test-bloc",
		},
		Safe:        mockSafe,
		PathBuilder: NewPathBuilder(&config.Config{}, "test-bloc"),
		logger:      logger.Get(),
	}

	// Run configuration (will skip SGs not found in state - that's OK)
	err := provider.configureSecurityGroups("mgmt", nil, 1, 1)

	// Should not error even when SGs are not found
	assert.NoError(t, err, "Should handle missing SGs gracefully")
}

// MockSafeInterface for testing.
type MockSafeInterface struct {
	setCalls []setCall
}

type setCall struct {
	path string
	data map[string]interface{}
}

func (m *MockSafeInterface) Set(_path, _key string, _value interface{}) error {
	return nil
}

func (m *MockSafeInterface) SetMultiple(path string, data map[string]interface{}) error {
	m.setCalls = append(m.setCalls, setCall{
		path: path,
		data: data,
	})
	return nil
}

func (m *MockSafeInterface) Get(_path, _key string) (interface{}, error) {
	return "", nil
}

func (m *MockSafeInterface) GetAll(_path string) (map[string]interface{}, error) {
	return nil, nil
}

func (m *MockSafeInterface) Exists(_path string) (bool, error) {
	return false, nil
}

func (m *MockSafeInterface) Delete(_path, _key string) error {
	return nil
}

func (m *MockSafeInterface) List(_path string) ([]string, error) {
	return nil, nil
}

func (m *MockSafeInterface) Export(_path string) (map[string]interface{}, error) {
	return nil, nil
}

func (m *MockSafeInterface) Import(_path string, _data map[string]interface{}) error {
	return nil
}

func (m *MockSafeInterface) GetEngineInfo(_path string) (*EngineInfo, error) {
	return nil, nil
}

func (m *MockSafeInterface) MustGet(_path, _key string) interface{} {
	return ""
}

func (m *MockSafeInterface) GetString(_path, _key string) (string, error) {
	return "", nil
}

func (m *MockSafeInterface) GetJSON(_path, _key string) ([]byte, error) {
	return nil, nil
}
