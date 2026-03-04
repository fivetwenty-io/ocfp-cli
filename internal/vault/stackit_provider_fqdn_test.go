package vault

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSafeForFQDN is a simple mock Safe implementation for FQDN tests.
type mockSafeForFQDN struct {
	data map[string]map[string]interface{}
}

func newMockSafe() *mockSafeForFQDN {
	return &mockSafeForFQDN{data: make(map[string]map[string]interface{})}
}

func (m *mockSafeForFQDN) Set(path, key string, value interface{}) error {
	if m.data[path] == nil {
		m.data[path] = make(map[string]interface{})
	}
	m.data[path][key] = value
	return nil
}

func (m *mockSafeForFQDN) SetMultiple(path string, data map[string]interface{}) error {
	m.data[path] = data
	return nil
}

func (m *mockSafeForFQDN) Get(path, key string) (interface{}, error) {
	if m.data[path] == nil {
		return nil, nil
	}
	return m.data[path][key], nil
}

func (m *mockSafeForFQDN) GetAll(path string) (map[string]interface{}, error) {
	return m.data[path], nil
}

func (m *mockSafeForFQDN) Exists(path string) (bool, error) {
	_, exists := m.data[path]
	return exists, nil
}

func (m *mockSafeForFQDN) Delete(path, key string) error {
	if key == "" {
		delete(m.data, path)
	} else if m.data[path] != nil {
		delete(m.data[path], key)
	}
	return nil
}

func (m *mockSafeForFQDN) List(_path string) ([]string, error) {
	var keys []string
	for k := range m.data {
		keys = append(keys, k)
	}
	return keys, nil
}

func (m *mockSafeForFQDN) Export(path string) (map[string]interface{}, error) {
	return m.data[path], nil
}

func (m *mockSafeForFQDN) Import(path string, data map[string]interface{}) error {
	m.data[path] = data
	return nil
}

func (m *mockSafeForFQDN) GetEngineInfo(_path string) (*EngineInfo, error) {
	return &EngineInfo{}, nil
}

func (m *mockSafeForFQDN) GetJSON(_path, _key string) ([]byte, error) {
	return []byte("{}"), nil
}

func (m *mockSafeForFQDN) GetString(path, key string) (string, error) {
	val, err := m.Get(path, key)
	if err != nil {
		return "", err
	}
	if val == nil {
		return "", nil
	}
	if s, ok := val.(string); ok {
		return s, nil
	}
	return "", nil
}

func (m *mockSafeForFQDN) MustGet(path, key string) interface{} {
	val, _ := m.Get(path, key)
	return val
}

// TestShouldSkipCFForEnvType verifies CF system filtering logic for mgmt environment.
func TestShouldSkipCFForEnvType(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Region:   "eu01",
	}

	safe := newMockSafe()
	provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

	tests := []struct {
		name     string
		envType  string
		system   string
		expected bool
	}{
		// mgmt environment - should skip CF systems
		{"mgmt - cf direct match", MgmtEnvType, "cf", true},
		{"mgmt - cloud_controller", MgmtEnvType, "cloud_controller", true},
		{"mgmt - api", MgmtEnvType, "api", true},
		{"mgmt - uaa", MgmtEnvType, "uaa", true},
		{"mgmt - diego", MgmtEnvType, "diego", true},
		{"mgmt - credhub", MgmtEnvType, "credhub", true},
		{"mgmt - loggregator", MgmtEnvType, "loggregator", true},
		{"mgmt - router", MgmtEnvType, "router", true},
		{"mgmt - doppler", MgmtEnvType, "doppler", true},
		{"mgmt - log-api", MgmtEnvType, "log-api", true},
		{"mgmt - syslog-scheduler", MgmtEnvType, "syslog-scheduler", true},

		// mgmt environment - CF prefix/suffix patterns
		{"mgmt - cf- prefix", MgmtEnvType, "cf-router", true},
		{"mgmt - cf_ prefix", MgmtEnvType, "cf_router", true},
		{"mgmt - -cf suffix", MgmtEnvType, "router-cf", true},
		{"mgmt - _cf suffix", MgmtEnvType, "router_cf", true},
		{"mgmt - cf-tcp-router", MgmtEnvType, "cf-tcp-router", true},

		// mgmt environment - non-CF systems (should NOT skip)
		{"mgmt - shield", MgmtEnvType, "shield", false},
		{"mgmt - vault", MgmtEnvType, "vault", false},
		{"mgmt - prometheus", MgmtEnvType, "prometheus", false},
		{"mgmt - concourse", MgmtEnvType, "concourse", false},
		{"mgmt - bosh", MgmtEnvType, "bosh", false},

		// ocf environment - should NOT skip ANY systems
		{"ocf - cf direct match", OCFEnvType, "cf", false},
		{"ocf - cloud_controller", OCFEnvType, "cloud_controller", false},
		{"ocf - api", OCFEnvType, "api", false},
		{"ocf - uaa", OCFEnvType, "uaa", false},
		{"ocf - diego", OCFEnvType, "diego", false},
		{"ocf - router", OCFEnvType, "router", false},
		{"ocf - cf-router", OCFEnvType, "cf-router", false},
		{"ocf - shield", OCFEnvType, "shield", false},
		{"ocf - vault", OCFEnvType, "vault", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := provider.shouldSkipCFForEnvType(tt.envType, tt.system)
			assert.Equal(t, tt.expected, result,
				"shouldSkipCFForEnvType(%s, %s) = %v, want %v",
				tt.envType, tt.system, result, tt.expected)
		})
	}
}

// TestConfigureFQDNs_MgmtFiltering verifies CF FQDN filtering for mgmt environment.
func TestConfigureFQDNs_MgmtFiltering(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Region:   "eu01",
		FQDNs: &config.FQDNConfig{
			Mgmt: map[string]string{
				// CF systems - should be filtered out
				"cf":               "cf.example.com",
				"cloud_controller": "api.example.com",
				"uaa":              "uaa.example.com",
				"diego":            "diego.example.com",
				"router":           "router.example.com",
				"cf-router":        "cf-router.example.com",
				"router-cf":        "router-cf.example.com",

				// Non-CF systems - should be kept
				"shield":     "shield.example.com",
				"vault":      "vault.example.com",
				"prometheus": "prometheus.example.com",
			},
		},
	}

	safe := newMockSafe()
	provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

	err := provider.ConfigureFQDNs("", MgmtEnvType, nil, 1, 1)
	require.NoError(t, err)

	// Verify CF systems were filtered out
	fqdnPath := provider.PathBuilder.GetFQDNsPath(MgmtEnvType)
	storedData, _ := safe.GetAll(fqdnPath)
	require.NotNil(t, storedData, "FQDNs should be stored")

	// Should NOT contain CF systems
	assert.NotContains(t, storedData, "cf")
	assert.NotContains(t, storedData, "cloud_controller")
	assert.NotContains(t, storedData, "uaa")
	assert.NotContains(t, storedData, "diego")
	assert.NotContains(t, storedData, "router")
	assert.NotContains(t, storedData, "cf-router")
	assert.NotContains(t, storedData, "router-cf")

	// SHOULD contain non-CF systems
	assert.Contains(t, storedData, "shield")
	assert.Contains(t, storedData, "vault")
	assert.Contains(t, storedData, "prometheus")

	assert.Equal(t, "shield.example.com", storedData["shield"])
	assert.Equal(t, "vault.example.com", storedData["vault"])
	assert.Equal(t, "prometheus.example.com", storedData["prometheus"])
}

// TestConfigureFQDNs_OCFNoFiltering verifies no filtering for OCF environment.
func TestConfigureFQDNs_OCFNoFiltering(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Region:   "eu01",
		FQDNs: &config.FQDNConfig{
			OCF: map[string]string{
				// CF systems - should be kept for OCF
				"cf":               "cf.example.com",
				"cloud_controller": "api.example.com",
				"uaa":              "uaa.example.com",
				"diego":            "diego.example.com",
				"router":           "router.example.com",

				// Non-CF systems
				"shield":     "shield.example.com",
				"vault":      "vault.example.com",
				"prometheus": "prometheus.example.com",
			},
		},
	}

	safe := newMockSafe()
	provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

	err := provider.ConfigureFQDNs("", OCFEnvType, nil, 1, 1)
	require.NoError(t, err)

	// Verify ALL systems were kept (no filtering)
	fqdnPath := provider.PathBuilder.GetFQDNsPath(OCFEnvType)
	storedData, _ := safe.GetAll(fqdnPath)
	require.NotNil(t, storedData, "FQDNs should be stored")

	// SHOULD contain CF systems for OCF
	assert.Contains(t, storedData, "cf")
	assert.Contains(t, storedData, "cloud_controller")
	assert.Contains(t, storedData, "uaa")
	assert.Contains(t, storedData, "diego")
	assert.Contains(t, storedData, "router")

	// SHOULD also contain non-CF systems
	assert.Contains(t, storedData, "shield")
	assert.Contains(t, storedData, "vault")
	assert.Contains(t, storedData, "prometheus")

	assert.Equal(t, "cf.example.com", storedData["cf"])
	assert.Equal(t, "uaa.example.com", storedData["uaa"])
	assert.Equal(t, "shield.example.com", storedData["shield"])
}

// TestConfigureFQDNs_BaseFQDNDerivation verifies FQDN derivation from base domain.
func TestConfigureFQDNs_BaseFQDNDerivation(t *testing.T) {
	tests := []struct {
		name           string
		base           string
		explicitOCF    map[string]string
		expectedShield string
		expectedCF     string
	}{
		{
			name:           "derive from base",
			base:           "test.stackit.cloud",
			explicitOCF:    map[string]string{},
			expectedShield: "shield.test.stackit.cloud",
			expectedCF:     "cf.test.stackit.cloud",
		},
		{
			name: "explicit overrides base",
			base: "test.stackit.cloud",
			explicitOCF: map[string]string{
				"shield": "custom-shield.example.com",
			},
			expectedShield: "custom-shield.example.com",
			expectedCF:     "cf.test.stackit.cloud",
		},
		{
			name: "all explicit, no derivation needed",
			base: "",
			explicitOCF: map[string]string{
				"cf":     "cf.explicit.com",
				"shield": "shield.explicit.com",
			},
			expectedShield: "shield.explicit.com",
			expectedCF:     "cf.explicit.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Name:     "test-bloc",
				Provider: "stackit",
				Region:   "eu01",
				FQDNs: &config.FQDNConfig{
					Base: tt.base,
					OCF:  tt.explicitOCF,
				},
			}

			safe := newMockSafe()
			provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

			err := provider.ConfigureFQDNs("", OCFEnvType, nil, 1, 1)
			require.NoError(t, err)

			fqdnPath := provider.PathBuilder.GetFQDNsPath(OCFEnvType)
			storedData, _ := safe.GetAll(fqdnPath)

			if tt.base != "" || len(tt.explicitOCF) > 0 {
				require.NotNil(t, storedData, "FQDNs should be stored")
				assert.Equal(t, tt.expectedShield, storedData["shield"])
				assert.Equal(t, tt.expectedCF, storedData["cf"])
			}
		})
	}
}

// TestConfigureFQDNs_BaseFQDNStoredAtSharedPath verifies base FQDN is stored at shared path.
func TestConfigureFQDNs_BaseFQDNStoredAtSharedPath(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Region:   "eu01",
		FQDNs: &config.FQDNConfig{
			Base: "example.com",
			Mgmt: map[string]string{},
		},
	}

	safe := newMockSafe()
	provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

	// Base is only stored on first env type (mgmt)
	err := provider.ConfigureFQDNs("", MgmtEnvType, nil, 1, 2)
	require.NoError(t, err)

	// Verify base FQDN was stored at shared path
	basePath := provider.PathBuilder.GetBaseFQDNPath()
	baseData, _ := safe.GetAll(basePath)
	require.NotNil(t, baseData, "Base FQDN should be stored")
	assert.Equal(t, "example.com", baseData["value"])
}

// TestConfigureFQDNs_PrePopulatesAllKnownServices verifies all known services are pre-populated.
func TestConfigureFQDNs_PrePopulatesAllKnownServices(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Region:   "eu01",
		FQDNs: &config.FQDNConfig{
			Base: "example.com",
			OCF:  map[string]string{}, // Empty explicit, rely on base derivation
		},
	}

	safe := newMockSafe()
	provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

	err := provider.ConfigureFQDNs("", OCFEnvType, nil, 1, 1)
	require.NoError(t, err)

	fqdnPath := provider.PathBuilder.GetFQDNsPath(OCFEnvType)
	storedData, _ := safe.GetAll(fqdnPath)
	require.NotNil(t, storedData)

	// Verify all OCF services are populated
	for _, service := range OCFServices {
		assert.Contains(t, storedData, service, "Service %s should be pre-populated", service)
		expectedFQDN := service + ".example.com"
		assert.Equal(t, expectedFQDN, storedData[service], "Service %s should have derived FQDN", service)
	}
}

// TestConfigureFQDNs_EmptyFQDNs verifies handling of empty FQDN configs.
func TestConfigureFQDNs_EmptyFQDNs(t *testing.T) {
	tests := []struct {
		name    string
		fqdns   *config.FQDNConfig
		envType string
	}{
		{"nil FQDNs", nil, MgmtEnvType},
		{"empty FQDNConfig", &config.FQDNConfig{}, MgmtEnvType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Name:     "test-bloc",
				Provider: "stackit",
				Region:   "eu01",
				FQDNs:    tt.fqdns,
			}

			safe := newMockSafe()
			provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

			err := provider.ConfigureFQDNs("", tt.envType, nil, 1, 1)
			require.NoError(t, err)

			// Should not have stored anything
			fqdnPath := provider.PathBuilder.GetFQDNsPath(tt.envType)
			storedData, _ := safe.GetAll(fqdnPath)
			assert.Nil(t, storedData, "No FQDNs should be stored for empty config")
		})
	}
}

// TestConfigureFQDNs_FallbackToDomainName verifies fallback to DomainName when base not set.
func TestConfigureFQDNs_FallbackToDomainName(t *testing.T) {
	cfg := &config.Config{
		Name:       "test-bloc",
		Provider:   "stackit",
		Region:     "eu01",
		DomainName: "domain.example.com",
		FQDNs: &config.FQDNConfig{
			// No Base set, should fallback to DomainName
			OCF: map[string]string{},
		},
	}

	safe := newMockSafe()
	provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

	err := provider.ConfigureFQDNs("", OCFEnvType, nil, 1, 1)
	require.NoError(t, err)

	fqdnPath := provider.PathBuilder.GetFQDNsPath(OCFEnvType)
	storedData, _ := safe.GetAll(fqdnPath)
	require.NotNil(t, storedData)

	// Should derive from DomainName
	assert.Equal(t, "shield.domain.example.com", storedData["shield"])
	assert.Equal(t, "cf.domain.example.com", storedData["cf"])
}
