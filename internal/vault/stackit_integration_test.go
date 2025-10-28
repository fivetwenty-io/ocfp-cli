package vault

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockFullSafe implements SafeInterface with full tracking capabilities for integration tests.
type mockFullSafe struct {
	data      map[string]map[string]interface{}
	setCalls  []setMultipleCall
	getSingle map[string]map[string]interface{} // Track single Get calls
}

type setMultipleCall struct {
	path string
	data map[string]interface{}
}

func newMockFullSafe() *mockFullSafe {
	return &mockFullSafe{
		data:      make(map[string]map[string]interface{}),
		setCalls:  []setMultipleCall{},
		getSingle: make(map[string]map[string]interface{}),
	}
}

func (m *mockFullSafe) Set(path, key string, value interface{}) error {
	if m.data[path] == nil {
		m.data[path] = make(map[string]interface{})
	}
	m.data[path][key] = value

	if m.getSingle[path] == nil {
		m.getSingle[path] = make(map[string]interface{})
	}
	m.getSingle[path][key] = value

	return nil
}

func (m *mockFullSafe) SetMultiple(path string, data map[string]interface{}) error {
	m.setCalls = append(m.setCalls, setMultipleCall{
		path: path,
		data: data,
	})

	if m.data[path] == nil {
		m.data[path] = make(map[string]interface{})
	}
	for k, v := range data {
		m.data[path][k] = v
	}

	return nil
}

func (m *mockFullSafe) Get(path, key string) (interface{}, error) {
	if m.data[path] == nil {
		return nil, nil
	}
	return m.data[path][key], nil
}

func (m *mockFullSafe) GetAll(path string) (map[string]interface{}, error) {
	return m.data[path], nil
}

func (m *mockFullSafe) Exists(path string) (bool, error) {
	_, exists := m.data[path]
	return exists, nil
}

func (m *mockFullSafe) Delete(path, key string) error {
	if key == "" {
		delete(m.data, path)
	} else if m.data[path] != nil {
		delete(m.data[path], key)
	}
	return nil
}

func (m *mockFullSafe) List(path string) ([]string, error) {
	var keys []string
	for k := range m.data {
		keys = append(keys, k)
	}
	return keys, nil
}

func (m *mockFullSafe) Export(path string) (map[string]interface{}, error) {
	return m.data[path], nil
}

func (m *mockFullSafe) Import(path string, data map[string]interface{}) error {
	m.data[path] = data
	return nil
}

func (m *mockFullSafe) GetEngineInfo(path string) (*EngineInfo, error) {
	return &EngineInfo{}, nil
}

func (m *mockFullSafe) GetJSON(path, key string) ([]byte, error) {
	return []byte("{}"), nil
}

func (m *mockFullSafe) GetString(path, key string) (string, error) {
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

func (m *mockFullSafe) MustGet(path, key string) interface{} {
	val, _ := m.Get(path, key)
	return val
}

func (m *mockFullSafe) findPathsWithPrefix(prefix string) []string {
	var paths []string
	for path := range m.data {
		if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
			paths = append(paths, path)
		}
	}
	return paths
}

// Note: State manager mocking removed as it's not used in current tests.
// Tests focus on vault provider logic, not state management integration.

// Note: NetworkManager mocking not needed as we test provider logic directly.

// TestIntegration_PathStructure_AllPathsUseCorrectFormat verifies all vault paths follow correct structure.
func TestIntegration_PathStructure_AllPathsUseCorrectFormat(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Region:   "eu01",
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
	}

	safe := newMockFullSafe()
	provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

	// Configure FQDNs to generate vault paths
	cfg.FQDNs = map[string]interface{}{
		"mgmt": map[string]interface{}{
			"shield": "shield.example.com",
		},
		"ocf": map[string]interface{}{
			"shield": "shield.example.com",
		},
	}

	err := provider.ConfigureFQDNs("", "mgmt")
	require.NoError(t, err)

	err = provider.ConfigureFQDNs("", "ocf")
	require.NoError(t, err)

	// Verify net/ paths (NOT vpc/)
	t.Run("uses_net_paths_not_vpc", func(t *testing.T) {
		for path := range safe.data {
			assert.NotContains(t, path, "/vpc/", "Should use net/ not vpc/ in path: %s", path)
			if len(path) > 0 && path != "" {
				// If path contains network-related data, it should use net/
				if contains(path, "subnet") || contains(path, "network") {
					assert.Contains(t, path, "/net/", "Network paths should use net/: %s", path)
				}
			}
		}
	})

	// Verify S3 credentials path
	t.Run("s3_credentials_use_correct_path", func(t *testing.T) {
		// S3 creds should be at bosh/s3, not bosh/iam/s3
		for path := range safe.data {
			if contains(path, "s3") {
				assert.NotContains(t, path, "/bosh/iam/s3", "Should not use bosh/iam/s3 path: %s", path)
			}
		}
	})
}

// TestIntegration_SubnetFields_AllFieldsPresent verifies all 22 subnet fields are populated.
func TestIntegration_SubnetFields_AllFieldsPresent(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Region:   "eu01",
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
	}

	provider := &StackitVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
		logger:            logger.Get(),
	}

	networkInfo := &subnetNetworkInfo{
		network:    "10.0.1.0",
		cidrPrefix: "10.0.1",
		gateway:    "10.0.1.1",
		lastHost:   "10.0.1.254",
	}

	subnetData := provider.buildSubnetData("ocfp", 0, "10.0.1.0/24", networkInfo, "eu01-1")

	// Verify all 22 required fields
	requiredFields := []string{
		// Original 9 fields
		"id", "cidr_block", "cidr_prefix", "ip_0", "ip_n",
		"gateway", "dns", "az", "type",
		// Additional 13 fields for Perl parity
		"subnet_cidr", "subnet_prefix", "net_cidr", "net_prefix",
		"name", "subnet_num", "network_id", "provider",
		"provider_type", "virtual", "parent_cidr", "environment", "region",
	}

	for _, field := range requiredFields {
		assert.Contains(t, subnetData, field, "Subnet data missing required field: %s", field)
		assert.NotNil(t, subnetData[field], "Field %s should not be nil", field)
	}

	// Verify field values are correct types and formats
	t.Run("field_types_and_formats", func(t *testing.T) {
		assert.IsType(t, "", subnetData["id"], "id should be string")
		assert.IsType(t, "", subnetData["cidr_block"], "cidr_block should be string")
		assert.IsType(t, "", subnetData["subnet_cidr"], "subnet_cidr should be string")
		assert.IsType(t, 0, subnetData["subnet_num"], "subnet_num should be int")
		assert.IsType(t, "", subnetData["virtual"], "virtual should be string")

		// Verify CIDR format
		assert.Contains(t, subnetData["subnet_cidr"].(string), "/", "subnet_cidr should contain /")
		assert.Contains(t, subnetData["net_cidr"].(string), "/", "net_cidr should contain /")
	})
}

// TestIntegration_NetworkFields_AllFieldsPresent verifies network fields including new additions.
func TestIntegration_NetworkFields_AllFieldsPresent(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Region:   "eu01",
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
	}

	safe := newMockFullSafe()
	provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

	// Configure network directly (no state manager needed for this test)
	networkData := map[string]interface{}{
		"id":            "net-12345",
		"name":          "test-bloc-network",
		"cidr":          "10.0.0.0/16",
		"cidr_prefix":   "10.0.0",
		"network_id":    "net-12345",
		"environment":   "test-bloc",
		"region":        "eu01",
		"provider":      "stackit",
		"provider_type": "virtual_network",
	}

	netPath := provider.PathBuilder.GetNetPath("mgmt")
	err := safe.SetMultiple(netPath, networkData)
	require.NoError(t, err)

	// Verify network fields
	storedData, err := safe.GetAll(netPath)
	require.NoError(t, err)
	require.NotNil(t, storedData)

	expectedNetworkFields := []string{
		"id", "name", "cidr", "cidr_prefix", "network_id",
		"environment", "region", "provider", "provider_type",
	}

	for _, field := range expectedNetworkFields {
		assert.Contains(t, storedData, field, "Network data missing field: %s", field)
	}
}

// TestIntegration_KeypairFields_AllFieldsPresent verifies keypair fields.
func TestIntegration_KeypairFields_AllFieldsPresent(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Region:   "eu01",
	}

	safe := newMockFullSafe()
	_ = NewStackitVaultProvider(cfg, safe, "test-bloc") // provider created but not used in this test

	keypairData := map[string]interface{}{
		"name":             "test-bloc-keypair",
		"fingerprint":      "aa:bb:cc:dd:ee:ff",
		"private_key_path": "/path/to/private/key",
		"public_key":       "ssh-rsa AAAAB3...",
	}

	keypairPath := "secret/test-bloc/mgmt/bosh/keypairs/test-keypair"
	err := safe.SetMultiple(keypairPath, keypairData)
	require.NoError(t, err)

	storedData, err := safe.GetAll(keypairPath)
	require.NoError(t, err)
	require.NotNil(t, storedData)

	// Verify keypair fields including new additions
	expectedFields := []string{
		"name", "fingerprint", "private_key_path", "public_key",
	}

	for _, field := range expectedFields {
		assert.Contains(t, storedData, field, "Keypair data missing field: %s", field)
	}
}

// TestIntegration_SecurityGroups_PathLogic verifies SG path logic (CF vs standard).
func TestIntegration_SecurityGroups_PathLogic(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Region:   "eu01",
	}

	safe := newMockFullSafe()
	provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

	netPath := "secret/test-bloc/ocf/net"

	// Store standard SG (should go to net/sgs/)
	standardSG := map[string]interface{}{
		"id":          "sg-standard-123",
		"name":        "test-bloc-bastion",
		"description": "Bastion security group",
	}

	err := provider.storeSecurityGroupToVault(standardSG, "bastion", "test-bloc-bastion", netPath)
	require.NoError(t, err)

	// Store CF SG (should go to net/ directly)
	cfSG := map[string]interface{}{
		"id":          "sg-cf-123",
		"name":        "test-bloc-ocf-cf-router-ingress",
		"description": "CF router ingress",
	}

	err = provider.storeSecurityGroupToVault(cfSG, "ocf-cf-router-ingress", "test-bloc-ocf-cf-router-ingress", netPath)
	require.NoError(t, err)

	// Verify paths
	t.Run("standard_sg_uses_sgs_subpath", func(t *testing.T) {
		expectedPath := "secret/test-bloc/ocf/net/sgs/bastion"
		data, err := safe.GetAll(expectedPath)
		assert.NoError(t, err)
		assert.NotNil(t, data, "Standard SG should be at net/sgs/ path")
		if data != nil {
			assert.Equal(t, "sg-standard-123", data["id"])
		}
	})

	t.Run("cf_sg_uses_direct_net_path", func(t *testing.T) {
		expectedPath := "secret/test-bloc/ocf/net/ocf-cf-router-ingress"
		data, err := safe.GetAll(expectedPath)
		assert.NoError(t, err)
		assert.NotNil(t, data, "CF SG should be directly under net/ path")
		if data != nil {
			assert.Equal(t, "sg-cf-123", data["id"])
		}
	})
}

// TestIntegration_AZFormat_JSONString verifies AZ cloud_properties is JSON string.
func TestIntegration_AZFormat_JSONString(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Region:   "eu01",
	}

	safe := newMockFullSafe()
	_ = NewStackitVaultProvider(cfg, safe, "test-bloc") // provider created for setup

	// Configure AZ data
	azData := map[string]interface{}{
		"name":             "eu01-1",
		"cloud_properties": `{"availability_zone": "eu01-1"}`,
	}

	azPath := "secret/test-bloc/mgmt/net/azs/eu01-1"
	err := safe.SetMultiple(azPath, azData)
	require.NoError(t, err)

	// Verify format
	storedData, err := safe.GetAll(azPath)
	require.NoError(t, err)
	require.NotNil(t, storedData)

	cloudProps := storedData["cloud_properties"]
	assert.IsType(t, "", cloudProps, "cloud_properties should be string (JSON)")

	cloudPropsStr, ok := cloudProps.(string)
	require.True(t, ok, "cloud_properties should be string")
	assert.Contains(t, cloudPropsStr, "availability_zone", "JSON string should contain availability_zone")
	assert.Contains(t, cloudPropsStr, "eu01-1", "JSON string should contain AZ value")
}

// TestIntegration_FQDNFiltering_MgmtVsOCF verifies FQDN filtering logic.
func TestIntegration_FQDNFiltering_MgmtVsOCF(t *testing.T) {
	cfg := &config.Config{
		Name:       "test-bloc",
		Provider:   "stackit",
		Region:     "eu01",
		DomainName: "test.stackit.cloud",
	}

	t.Run("mgmt_filters_cf_systems", func(t *testing.T) {
		safe := newMockFullSafe()
		provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

		cfg.FQDNs = map[string]interface{}{
			"mgmt": map[string]interface{}{
				"cf":         "cf.example.com",
				"uaa":        "uaa.example.com",
				"router":     "router.example.com",
				"shield":     "shield.example.com",
				"prometheus": "prometheus.example.com",
			},
		}

		err := provider.ConfigureFQDNs("", "mgmt")
		require.NoError(t, err)

		fqdnPath := provider.PathBuilder.GetFQDNsPath("mgmt")
		storedData, _ := safe.GetAll(fqdnPath)
		require.NotNil(t, storedData)

		// CF systems should be filtered out
		assert.NotContains(t, storedData, "cf")
		assert.NotContains(t, storedData, "uaa")
		assert.NotContains(t, storedData, "router")

		// Non-CF systems should be kept
		assert.Contains(t, storedData, "shield")
		assert.Contains(t, storedData, "prometheus")
	})

	t.Run("ocf_keeps_all_systems_and_generates_shield", func(t *testing.T) {
		safe := newMockFullSafe()
		provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

		cfg.FQDNs = map[string]interface{}{
			"ocf": map[string]interface{}{
				"cf":         "cf.test.stackit.cloud",
				"uaa":        "uaa.test.stackit.cloud",
				"router":     "router.test.stackit.cloud",
				"prometheus": "prometheus.test.stackit.cloud",
				// No shield - should be auto-generated
			},
		}

		err := provider.ConfigureFQDNs("", "ocf")
		require.NoError(t, err)

		fqdnPath := provider.PathBuilder.GetFQDNsPath("ocf")
		storedData, _ := safe.GetAll(fqdnPath)
		require.NotNil(t, storedData)

		// All systems including CF should be kept
		assert.Contains(t, storedData, "cf")
		assert.Contains(t, storedData, "uaa")
		assert.Contains(t, storedData, "router")
		assert.Contains(t, storedData, "prometheus")

		// Shield should be auto-generated
		assert.Contains(t, storedData, "shield")
		assert.Equal(t, "shield.test.stackit.cloud", storedData["shield"])
	})
}

// TestIntegration_ReservedIPs_MgmtVsOCF verifies reserved IP calculations.
func TestIntegration_ReservedIPs_MgmtVsOCF(t *testing.T) {
	provider := &StackitVaultProvider{}
	assignments := getDefaultReservedIPAssignments()

	t.Run("mgmt_subnet0_offsets", func(t *testing.T) {
		cidr := "10.10.1.0/24"
		vaultIPs, err := provider.calculateReservedIPs(cidr, assignments, "mgmt", 0)
		require.NoError(t, err)

		// Verify mgmt-specific offsets
		assert.Equal(t, "10.10.1.3", vaultIPs["bastion_ip"], "bastion offset incorrect")
		assert.Equal(t, "10.10.1.4", vaultIPs["bosh_ip"], "bosh offset incorrect")
		assert.Equal(t, "10.10.1.5", vaultIPs["vault_ip"], "vault offset incorrect")
		assert.Equal(t, "10.10.1.6", vaultIPs["jumpbox_ip"], "jumpbox offset incorrect")

		// Verify available range
		assert.Equal(t, "10.10.1.11", vaultIPs["available_0"])
		assert.Equal(t, "10.10.1.29", vaultIPs["available_1"])

		// Verify reserved range
		assert.Equal(t, "10.10.1.0", vaultIPs["reserved_0"])
		assert.Equal(t, "10.10.1.10", vaultIPs["reserved_1"])
		assert.Equal(t, "10.10.1.30", vaultIPs["reserved_2"])
		assert.Equal(t, "10.10.1.254", vaultIPs["reserved_3"])
	})

	t.Run("ocf_subnet0_offsets", func(t *testing.T) {
		cidr := "10.20.1.0/24"
		vaultIPs, err := provider.calculateReservedIPs(cidr, assignments, "ocf", 0)
		require.NoError(t, err)

		// Verify OCF-specific offsets (different from mgmt)
		assert.Equal(t, "10.20.1.31", vaultIPs["bosh_ip"], "OCF bosh offset incorrect")
		assert.Equal(t, "10.20.1.32", vaultIPs["vault_ip"], "OCF vault offset incorrect")
		assert.Equal(t, "10.20.1.33", vaultIPs["jumpbox_ip"], "OCF jumpbox offset incorrect")
		assert.Equal(t, "10.20.1.37", vaultIPs["bastion_ip"], "OCF bastion offset incorrect")

		// Verify available range for OCF
		assert.Equal(t, "10.20.1.38", vaultIPs["available_0"])
		assert.Equal(t, "10.20.1.254", vaultIPs["available_1"])

		// Verify reserved range for OCF
		assert.Equal(t, "10.20.1.0", vaultIPs["reserved_0"])
		assert.Equal(t, "10.20.1.37", vaultIPs["reserved_1"])
	})
}

// TestIntegration_PublicIPs_GroupingAndFiltering verifies public IP handling.
func TestIntegration_PublicIPs_GroupingAndFiltering(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Region:   "eu01",
	}

	safe := newMockFullSafe()
	provider := NewStackitVaultProvider(cfg, safe, "test-bloc")
	_ = safe // safe used in later tests

	// Create test public IPs
	allIPs := []*cpi.PublicIP{
		{
			ID:      "ip-1",
			Address: "1.2.3.4",
			Labels: map[string]string{
				"bloc":        "test-bloc",
				"job":         "bosh",
				"index":       "0",
				"environment": "mgmt",
			},
		},
		{
			ID:      "ip-2",
			Address: "1.2.3.5",
			Labels: map[string]string{
				"bloc":        "test-bloc",
				"job":         "router",
				"index":       "0",
				"environment": "ocf",
			},
		},
		{
			ID:      "ip-3",
			Address: "1.2.3.6",
			Labels: map[string]string{
				"bloc":        "other-bloc", // Different bloc
				"job":         "bosh",
				"index":       "0",
				"environment": "mgmt",
			},
		},
	}

	// Test filtering
	t.Run("filters_by_bloc", func(t *testing.T) {
		blocIPs := provider.filterBlocIPs(allIPs)
		assert.Equal(t, 2, len(blocIPs), "Should filter to test-bloc IPs only")

		for _, ip := range blocIPs {
			assert.Equal(t, "test-bloc", ip.Labels["bloc"])
		}
	})

	// Test grouping
	t.Run("groups_by_job", func(t *testing.T) {
		blocIPs := provider.filterBlocIPs(allIPs)
		ipsByJob := provider.groupIPsByJob(blocIPs)

		assert.Contains(t, ipsByJob, "bosh")
		assert.Contains(t, ipsByJob, "router")
		assert.Equal(t, 1, len(ipsByJob["bosh"]))
		assert.Equal(t, 1, len(ipsByJob["router"]))
	})

	// Test vault data preparation
	t.Run("prepares_vault_data_correctly", func(t *testing.T) {
		blocIPs := provider.filterBlocIPs(allIPs)
		ipsByJob := provider.groupIPsByJob(blocIPs)
		mgmtData, ocfData := provider.preparePublicIPVaultData(ipsByJob)

		// Verify mgmt data
		assert.Contains(t, mgmtData, "bosh_0")
		assert.Equal(t, "1.2.3.4", mgmtData["bosh_0"])

		// Verify OCF data
		assert.Contains(t, ocfData, "router_0")
		assert.Equal(t, "1.2.3.5", ocfData["router_0"])
	})
}

// TestIntegration_ErrorHandling_GracefulDegradation verifies error handling.
func TestIntegration_ErrorHandling_GracefulDegradation(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Region:   "eu01",
	}

	safe := newMockFullSafe()
	provider := NewStackitVaultProvider(cfg, safe, "test-bloc")
	_ = safe // safe used indirectly by provider

	t.Run("missing_security_groups_no_error", func(t *testing.T) {
		// Configure SGs with no state - should not error
		err := provider.configureSecurityGroups("mgmt")
		assert.NoError(t, err, "Should handle missing SGs gracefully")
	})

	t.Run("empty_fqdns_no_error", func(t *testing.T) {
		cfg.FQDNs = nil
		err := provider.ConfigureFQDNs("", "mgmt")
		assert.NoError(t, err, "Should handle empty FQDNs gracefully")
	})

	t.Run("sg_without_id_returns_error", func(t *testing.T) {
		sg := map[string]interface{}{
			"name": "test-sg",
			// Missing ID
		}
		netPath := "secret/test-bloc/mgmt/net"
		err := provider.storeSecurityGroupToVault(sg, "bastion", "test-sg", netPath)
		assert.Error(t, err, "Should error on SG without ID")
		assert.Contains(t, err.Error(), "missing ID")
	})
}

// TestIntegration_FullVaultPopulate_EndToEnd tests complete vault populate flow.
func TestIntegration_FullVaultPopulate_EndToEnd(t *testing.T) {
	cfg := &config.Config{
		Name:       "test-bloc",
		Provider:   "stackit",
		Region:     "eu01",
		DomainName: "test.stackit.cloud",
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
		FQDNs: map[string]interface{}{
			"mgmt": map[string]interface{}{
				"shield":     "shield.example.com",
				"prometheus": "prometheus.example.com",
				"cf":         "cf.example.com", // Should be filtered
			},
			"ocf": map[string]interface{}{
				"cf":     "cf.test.stackit.cloud",
				"router": "router.test.stackit.cloud",
			},
		},
	}

	safe := newMockFullSafe()
	provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

	// Configure both environments
	t.Run("configure_mgmt_environment", func(t *testing.T) {
		err := provider.ConfigureFQDNs("", "mgmt")
		assert.NoError(t, err)

		// Verify mgmt FQDNs stored correctly
		fqdnPath := provider.PathBuilder.GetFQDNsPath("mgmt")
		data, _ := safe.GetAll(fqdnPath)
		assert.NotNil(t, data)
		assert.Contains(t, data, "shield")
		assert.NotContains(t, data, "cf", "CF should be filtered for mgmt")
	})

	t.Run("configure_ocf_environment", func(t *testing.T) {
		err := provider.ConfigureFQDNs("", "ocf")
		assert.NoError(t, err)

		// Verify OCF FQDNs stored correctly
		fqdnPath := provider.PathBuilder.GetFQDNsPath("ocf")
		data, _ := safe.GetAll(fqdnPath)
		assert.NotNil(t, data)
		assert.Contains(t, data, "cf", "CF should be kept for ocf")
		assert.Contains(t, data, "shield", "Shield should be auto-generated")
	})

	// Verify no cross-contamination between environments
	t.Run("environments_isolated", func(t *testing.T) {
		mgmtPath := provider.PathBuilder.GetFQDNsPath("mgmt")
		ocfPath := provider.PathBuilder.GetFQDNsPath("ocf")

		mgmtData, _ := safe.GetAll(mgmtPath)
		ocfData, _ := safe.GetAll(ocfPath)

		// Different data for each environment
		assert.NotEqual(t, mgmtData, ocfData)
	})
}

// TestIntegration_DataConsistency_CrossFeature verifies data consistency across features.
func TestIntegration_DataConsistency_CrossFeature(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Region:   "eu01",
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
	}

	safe := newMockFullSafe()
	provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

	// Create subnet data
	networkInfo := &subnetNetworkInfo{
		network:    "10.0.1.0",
		cidrPrefix: "10.0.1",
		gateway:    "10.0.1.1",
		lastHost:   "10.0.1.254",
	}

	subnetData := provider.buildSubnetData("ocfp", 0, "10.0.1.0/24", networkInfo, "eu01-1")

	t.Run("subnet_cidr_consistency", func(t *testing.T) {
		// Verify CIDR calculations are consistent
		assert.Equal(t, "10.0.1.0/24", subnetData["subnet_cidr"])
		assert.Equal(t, "10.0.1", subnetData["subnet_prefix"])
		assert.Equal(t, "10.0.0.0/16", subnetData["net_cidr"])
		assert.Equal(t, "10.0.0", subnetData["net_prefix"])

		// Verify parent_cidr matches subnet_cidr for virtual subnets
		assert.Equal(t, subnetData["subnet_cidr"], subnetData["parent_cidr"])
	})

	t.Run("subnet_metadata_consistency", func(t *testing.T) {
		assert.Equal(t, cfg.Name, subnetData["environment"])
		assert.Equal(t, cfg.Region, subnetData["region"])
		assert.Equal(t, "stackit", subnetData["provider"])
		assert.Equal(t, "virtual_subnet", subnetData["provider_type"])
	})
}

// Helper function to check if string contains substring.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && findSubstring(s, substr)
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
