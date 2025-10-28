package vault

import (
	"context"
	"fmt"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// MockNetworkManager is a simple mock implementation of cpi.NetworkManager for testing.
type MockNetworkManager struct {
	PublicIPs []*cpi.PublicIP
	Error     error
}

func (m *MockNetworkManager) ListPublicIPs(ctx context.Context) ([]*cpi.PublicIP, error) {
	return m.PublicIPs, m.Error
}

func (m *MockNetworkManager) CreateNetwork(ctx context.Context, request *cpi.NetworkRequest) (*cpi.Network, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) GetNetwork(ctx context.Context, networkID string) (*cpi.Network, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DeleteNetwork(ctx context.Context, networkID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) ListNetworks(ctx context.Context, filters map[string]string) ([]*cpi.Network, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) CreateSubnet(ctx context.Context, request *cpi.SubnetRequest) (*cpi.Subnet, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) GetSubnet(ctx context.Context, subnetID string) (*cpi.Subnet, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DeleteSubnet(ctx context.Context, subnetID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) ListSubnets(ctx context.Context, networkID string) ([]*cpi.Subnet, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) AllocatePublicIP(ctx context.Context, request *cpi.PublicIPRequest) (*cpi.PublicIP, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) GetPublicIP(ctx context.Context, ipID string) (*cpi.PublicIP, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) ReleasePublicIP(ctx context.Context, ipID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) AssociateFloatingIP(ctx context.Context, ipID, instanceID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DisassociateFloatingIP(ctx context.Context, ipID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) CreateSecurityGroup(ctx context.Context, request *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) GetSecurityGroup(ctx context.Context, groupID string) (*cpi.SecurityGroup, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DeleteSecurityGroup(ctx context.Context, groupID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) ListSecurityGroups(ctx context.Context, filters map[string]string) ([]*cpi.SecurityGroup, error) {
	return nil, fmt.Errorf("not implemented")
}

// Additional methods from NetworkManager interface
func (m *MockNetworkManager) CreatePublicIP(ctx context.Context, request *cpi.PublicIPRequest) (*cpi.PublicIP, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DeletePublicIP(ctx context.Context, ipID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) AllocateFloatingIP(ctx context.Context, req *cpi.AllocateFloatingIPRequest) (*cpi.FloatingIP, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) GetFloatingIP(ctx context.Context, ipID string) (*cpi.FloatingIP, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) ListFloatingIPs(ctx context.Context, filters map[string]string) ([]*cpi.FloatingIP, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) ReleaseFloatingIP(ctx context.Context, ipID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) CreateRouter(ctx context.Context, req *cpi.CreateRouterRequest) (*cpi.Router, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) GetRouter(ctx context.Context, id string) (*cpi.Router, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) ListRouters(ctx context.Context) ([]*cpi.Router, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) AttachRouterInterface(ctx context.Context, routerID, subnetID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DetachRouterInterface(ctx context.Context, routerID, subnetID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DeleteRouter(ctx context.Context, id string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) CreateLoadBalancer(ctx context.Context, config *cpi.LoadBalancer) (*cpi.LoadBalancer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) GetLoadBalancer(ctx context.Context, nameOrID string) (*cpi.LoadBalancer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) ListLoadBalancers(ctx context.Context, filters map[string]string) ([]*cpi.LoadBalancer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) UpdateLoadBalancer(ctx context.Context, lb *cpi.LoadBalancer) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DeleteLoadBalancer(ctx context.Context, id string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) GetBackendPools(ctx context.Context, lbID string) ([]*cpi.BackendPool, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) CreateBackendPool(ctx context.Context, lbID string, pool *cpi.BackendPool) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) UpdateBackendPool(ctx context.Context, lbID string, pool *cpi.BackendPool) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DeleteBackendPool(ctx context.Context, lbID, poolID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) AddBackendMember(ctx context.Context, lbID string, member *cpi.BackendMember) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) RemoveBackendMember(ctx context.Context, lbID, memberID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) ConfigureHealthCheck(ctx context.Context, lbID string, check *cpi.HealthCheck) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) GetLoadBalancerHealth(ctx context.Context, lbID string) (*cpi.HealthStatus, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) AttachBackendServer(ctx context.Context, lbID, poolID, serverID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DetachBackendServer(ctx context.Context, lbID, poolID, serverID string) error {
	return fmt.Errorf("not implemented")
}

// TestFilterBlocIPs tests the filtering of public IPs by bloc name and managed-by label.
func TestFilterBlocIPs(t *testing.T) {
	logger := zap.NewNop().Sugar()
	defer func() { _ = logger.Sync() }()

	cfg := &config.Config{
		Name: "test-bloc",
	}

	provider := &StackitVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
		logger:   logger,
	}

	tests := []struct {
		name     string
		input    []*cpi.PublicIP
		expected int
	}{
		{
			name: "filters by bloc and managed-by labels",
			input: []*cpi.PublicIP{
				{
					ID:        "ip-1",
					IPAddress: "1.2.3.4",
					Labels: map[string]string{
						"bloc":       "test-bloc",
						"managed-by": "ocfp",
						"job":        "router",
					},
				},
				{
					ID:        "ip-2",
					IPAddress: "5.6.7.8",
					Labels: map[string]string{
						"bloc":       "other-bloc",
						"managed-by": "ocfp",
						"job":        "router",
					},
				},
				{
					ID:        "ip-3",
					IPAddress: "9.10.11.12",
					Labels: map[string]string{
						"bloc":       "test-bloc",
						"managed-by": "manual",
						"job":        "bastion",
					},
				},
				{
					ID:        "ip-4",
					IPAddress: "13.14.15.16",
					Labels: map[string]string{
						"bloc":       "test-bloc",
						"managed-by": "ocfp",
						"job":        "tcp-router",
					},
				},
			},
			expected: 2,
		},
		{
			name: "handles empty labels",
			input: []*cpi.PublicIP{
				{
					ID:        "ip-1",
					IPAddress: "1.2.3.4",
					Labels:    nil,
				},
				{
					ID:        "ip-2",
					IPAddress: "5.6.7.8",
					Labels:    map[string]string{},
				},
			},
			expected: 0,
		},
		{
			name:     "handles empty input",
			input:    []*cpi.PublicIP{},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := provider.filterBlocIPs(tt.input)
			assert.Equal(t, tt.expected, len(result))
		})
	}
}

// TestGroupIPsByJob tests grouping of public IPs by job type.
func TestGroupIPsByJob(t *testing.T) {
	logger := zap.NewNop().Sugar()
	defer func() { _ = logger.Sync() }()


	cfg := &config.Config{
		Name: "test-bloc",
	}

	provider := &StackitVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
		logger:   logger,
	}

	tests := []struct {
		name     string
		input    []*cpi.PublicIP
		expected map[string]int
	}{
		{
			name: "groups by job label",
			input: []*cpi.PublicIP{
				{
					ID:        "ip-1",
					IPAddress: "1.2.3.4",
					Job:       "router",
					Labels: map[string]string{
						"job": "router",
					},
				},
				{
					ID:        "ip-2",
					IPAddress: "5.6.7.8",
					Job:       "router",
					Labels: map[string]string{
						"job": "router",
					},
				},
				{
					ID:        "ip-3",
					IPAddress: "9.10.11.12",
					Job:       "tcp-router",
					Labels: map[string]string{
						"job": "tcp-router",
					},
				},
			},
			expected: map[string]int{
				"router":     2,
				"tcp-router": 1,
			},
		},
		{
			name: "prefers Job field over labels",
			input: []*cpi.PublicIP{
				{
					ID:        "ip-1",
					IPAddress: "1.2.3.4",
					Job:       "router",
					Labels: map[string]string{
						"job": "bastion",
					},
				},
			},
			expected: map[string]int{
				"router": 1,
			},
		},
		{
			name: "uses unknown for missing job",
			input: []*cpi.PublicIP{
				{
					ID:        "ip-1",
					IPAddress: "1.2.3.4",
					Labels:    map[string]string{},
				},
			},
			expected: map[string]int{
				"unknown": 1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := provider.groupIPsByJob(tt.input)

			for job, expectedCount := range tt.expected {
				assert.Equal(t, expectedCount, len(result[job]), "job: %s", job)
			}
		})
	}
}

// TestDetermineVaultKeyAndEnvironment tests vault key and environment determination.
func TestDetermineVaultKeyAndEnvironment(t *testing.T) {
	logger := zap.NewNop().Sugar()
	defer func() { _ = logger.Sync() }()


	cfg := &config.Config{
		Name: "test-bloc",
	}

	provider := &StackitVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
		logger:   logger,
	}

	tests := []struct {
		name        string
		job         string
		index       string
		expectedKey string
		expectedEnv string
	}{
		{
			name:        "bastion job",
			job:         "bastion",
			index:       "0",
			expectedKey: "bastion_0",
			expectedEnv: "mgmt",
		},
		{
			name:        "ops job",
			job:         "ops",
			index:       "0",
			expectedKey: "ops_0",
			expectedEnv: "mgmt",
		},
		{
			name:        "router job",
			job:         "router",
			index:       "1",
			expectedKey: "cf_router_1",
			expectedEnv: "ocf",
		},
		{
			name:        "tcp-router job",
			job:         "tcp-router",
			index:       "0",
			expectedKey: "cf_tcp_router_0",
			expectedEnv: "ocf",
		},
		{
			name:        "jumpbox job",
			job:         "jumpbox",
			index:       "0",
			expectedKey: "jumpbox_0",
			expectedEnv: "mgmt",
		},
		{
			name:        "unknown job with cf prefix",
			job:         "cf-something",
			index:       "0",
			expectedKey: "cf-something_0",
			expectedEnv: "ocf",
		},
		{
			name:        "unknown job without cf prefix",
			job:         "custom",
			index:       "0",
			expectedKey: "custom_0",
			expectedEnv: "mgmt",
		},
		{
			name:        "empty index defaults to unknown",
			job:         "router",
			index:       "",
			expectedKey: "cf_router_unknown",
			expectedEnv: "ocf",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, env := provider.determineVaultKeyAndEnvironment(tt.job, tt.index)
			assert.Equal(t, tt.expectedKey, key)
			assert.Equal(t, tt.expectedEnv, env)
		})
	}
}

// TestPreparePublicIPVaultData tests preparation of vault data for mgmt and ocf environments.
func TestPreparePublicIPVaultData(t *testing.T) {
	logger := zap.NewNop().Sugar()
	defer func() { _ = logger.Sync() }()


	cfg := &config.Config{
		Name: "test-bloc",
	}

	provider := &StackitVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
		logger:   logger,
	}

	tests := []struct {
		name             string
		input            map[string][]*cpi.PublicIP
		expectedMgmtKeys []string
		expectedOcfKeys  []string
	}{
		{
			name: "separates mgmt and ocf IPs",
			input: map[string][]*cpi.PublicIP{
				"bastion": {
					{IPAddress: "1.2.3.4", Index: "0"},
				},
				"router": {
					{IPAddress: "5.6.7.8", Index: "0"},
					{IPAddress: "9.10.11.12", Index: "1"},
				},
				"tcp-router": {
					{IPAddress: "13.14.15.16", Index: "0"},
				},
			},
			expectedMgmtKeys: []string{"bastion_0"},
			expectedOcfKeys:  []string{"cf_router_0", "cf_router_1", "cf_tcp_router_0"},
		},
		{
			name: "handles multiple jobs in same environment",
			input: map[string][]*cpi.PublicIP{
				"bastion": {
					{IPAddress: "1.2.3.4", Index: "0"},
				},
				"ops": {
					{IPAddress: "5.6.7.8", Index: "0"},
				},
				"jumpbox": {
					{IPAddress: "9.10.11.12", Index: "0"},
				},
			},
			expectedMgmtKeys: []string{"bastion_0", "ops_0", "jumpbox_0"},
			expectedOcfKeys:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgmtData, ocfData := provider.preparePublicIPVaultData(tt.input)

			// Check mgmt keys
			assert.Equal(t, len(tt.expectedMgmtKeys), len(mgmtData))
			for _, key := range tt.expectedMgmtKeys {
				assert.Contains(t, mgmtData, key)
			}

			// Check ocf keys
			assert.Equal(t, len(tt.expectedOcfKeys), len(ocfData))
			for _, key := range tt.expectedOcfKeys {
				assert.Contains(t, ocfData, key)
			}
		})
	}
}

// TestSortIPsByIndex tests IP sorting by index.
func TestSortIPsByIndex(t *testing.T) {
	logger := zap.NewNop().Sugar()
	defer func() { _ = logger.Sync() }()


	cfg := &config.Config{
		Name: "test-bloc",
	}

	provider := &StackitVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
		logger:   logger,
	}

	tests := []struct {
		name     string
		input    []*cpi.PublicIP
		expected []string
	}{
		{
			name: "sorts by index",
			input: []*cpi.PublicIP{
				{IPAddress: "1.2.3.4", Index: "2"},
				{IPAddress: "5.6.7.8", Index: "0"},
				{IPAddress: "9.10.11.12", Index: "1"},
			},
			expected: []string{"5.6.7.8", "9.10.11.12", "1.2.3.4"},
		},
		{
			name: "handles empty indices",
			input: []*cpi.PublicIP{
				{IPAddress: "1.2.3.4", Index: ""},
				{IPAddress: "5.6.7.8", Index: "0"},
			},
			expected: []string{"5.6.7.8", "1.2.3.4"},
		},
		{
			name: "handles single IP",
			input: []*cpi.PublicIP{
				{IPAddress: "1.2.3.4", Index: "0"},
			},
			expected: []string{"1.2.3.4"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := provider.sortIPsByIndex(tt.input)

			for i, ip := range result {
				assert.Equal(t, tt.expected[i], ip.IPAddress)
			}
		})
	}
}

// TestCountKeysWithPrefix tests counting keys with a specific prefix.
func TestCountKeysWithPrefix(t *testing.T) {
	logger := zap.NewNop().Sugar()
	defer func() { _ = logger.Sync() }()


	cfg := &config.Config{
		Name: "test-bloc",
	}

	provider := &StackitVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
		logger:   logger,
	}

	tests := []struct {
		name     string
		data     map[string]interface{}
		prefix   string
		expected int
	}{
		{
			name: "counts matching keys",
			data: map[string]interface{}{
				"bastion_0": "1.2.3.4",
				"bastion_1": "5.6.7.8",
				"ops_0":     "9.10.11.12",
			},
			prefix:   "bastion_",
			expected: 2,
		},
		{
			name: "returns zero for no matches",
			data: map[string]interface{}{
				"bastion_0": "1.2.3.4",
				"ops_0":     "5.6.7.8",
			},
			prefix:   "router_",
			expected: 0,
		},
		{
			name:     "handles empty data",
			data:     map[string]interface{}{},
			prefix:   "bastion_",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := provider.countKeysWithPrefix(tt.data, tt.prefix)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestFetchAllPublicIPs tests fetching all public IPs from the API.
func TestFetchAllPublicIPs(t *testing.T) {
	logger := zap.NewNop().Sugar()
	defer func() { _ = logger.Sync() }()


	cfg := &config.Config{
		Name: "test-bloc",
	}

	provider := &StackitVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
		logger:   logger,
	}

	tests := []struct {
		name        string
		mockIPs     []*cpi.PublicIP
		mockError   error
		expectedLen int
		expectError bool
	}{
		{
			name: "successfully fetches IPs",
			mockIPs: []*cpi.PublicIP{
				{
					ID:        "ip-1",
					IPAddress: "1.2.3.4",
					Job:       "router",
					Index:     "0",
					Labels: map[string]string{
						"bloc":       "test-bloc",
						"managed-by": "ocfp",
					},
				},
				{
					ID:        "ip-2",
					IPAddress: "5.6.7.8",
					Job:       "tcp-router",
					Index:     "0",
					Labels: map[string]string{
						"bloc":       "test-bloc",
						"managed-by": "ocfp",
					},
				},
			},
			mockError:   nil,
			expectedLen: 2,
			expectError: false,
		},
		{
			name:        "handles API error",
			mockIPs:     nil,
			mockError:   fmt.Errorf("API error"),
			expectedLen: 0,
			expectError: true,
		},
		{
			name:        "handles empty response",
			mockIPs:     []*cpi.PublicIP{},
			mockError:   nil,
			expectedLen: 0,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockNetwork := &MockNetworkManager{
				PublicIPs: tt.mockIPs,
				Error:     tt.mockError,
			}

			result, err := provider.fetchAllPublicIPs(mockNetwork)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedLen, len(result))
			}
		})
	}
}
