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
	PublicIPs        []*cpi.PublicIP
	Networks         []*cpi.Network
	GetNetworkResult *cpi.Network
	Error            error
}

func (m *MockNetworkManager) ListPublicIPs(_ctx context.Context) ([]*cpi.PublicIP, error) {
	return m.PublicIPs, m.Error
}

func (m *MockNetworkManager) CreateNetwork(_ctx context.Context, _request *cpi.NetworkRequest) (*cpi.Network, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) GetNetwork(_ctx context.Context, _networkID string) (*cpi.Network, error) {
	if m.GetNetworkResult != nil {
		return m.GetNetworkResult, nil
	}

	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DeleteNetwork(_ctx context.Context, _networkID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) ListNetworks(_ctx context.Context, _filters map[string]string) ([]*cpi.Network, error) {
	if m.Networks != nil {
		return m.Networks, nil
	}

	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) CreateSubnet(_ctx context.Context, _request *cpi.SubnetRequest) (*cpi.Subnet, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) GetSubnet(_ctx context.Context, _subnetID string) (*cpi.Subnet, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DeleteSubnet(_ctx context.Context, _subnetID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) ListSubnets(_ctx context.Context, _networkID string) ([]*cpi.Subnet, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) AllocatePublicIP(_ctx context.Context, _request *cpi.PublicIPRequest) (*cpi.PublicIP, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) GetPublicIP(_ctx context.Context, _ipID string) (*cpi.PublicIP, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) ReleasePublicIP(_ctx context.Context, _ipID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) AssociateFloatingIP(_ctx context.Context, _ipID, _instanceID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DisassociateFloatingIP(_ctx context.Context, _ipID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) CreateSecurityGroup(_ctx context.Context, _request *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) GetSecurityGroup(_ctx context.Context, _groupID string) (*cpi.SecurityGroup, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DeleteSecurityGroup(_ctx context.Context, _groupID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) ListSecurityGroups(_ctx context.Context, _filters map[string]string) ([]*cpi.SecurityGroup, error) {
	return nil, fmt.Errorf("not implemented")
}

// Additional methods from NetworkManager interface
func (m *MockNetworkManager) CreatePublicIP(_ctx context.Context, _request *cpi.PublicIPRequest) (*cpi.PublicIP, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DeletePublicIP(_ctx context.Context, _ipID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) AllocateFloatingIP(_ctx context.Context, _req *cpi.AllocateFloatingIPRequest) (*cpi.FloatingIP, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) GetFloatingIP(_ctx context.Context, _ipID string) (*cpi.FloatingIP, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) ListFloatingIPs(_ctx context.Context, _filters map[string]string) ([]*cpi.FloatingIP, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) ReleaseFloatingIP(_ctx context.Context, _ipID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) CreateRouter(_ctx context.Context, _req *cpi.CreateRouterRequest) (*cpi.Router, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) GetRouter(_ctx context.Context, _id string) (*cpi.Router, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) ListRouters(_ctx context.Context) ([]*cpi.Router, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) AttachRouterInterface(_ctx context.Context, _routerID, _subnetID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DetachRouterInterface(_ctx context.Context, _routerID, _subnetID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DeleteRouter(_ctx context.Context, _id string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) CreateLoadBalancer(_ctx context.Context, _config *cpi.LoadBalancer) (*cpi.LoadBalancer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) GetLoadBalancer(_ctx context.Context, _nameOrID string) (*cpi.LoadBalancer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) ListLoadBalancers(_ctx context.Context, _filters map[string]string) ([]*cpi.LoadBalancer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) UpdateLoadBalancer(_ctx context.Context, _lb *cpi.LoadBalancer) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DeleteLoadBalancer(_ctx context.Context, _id string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) GetBackendPools(_ctx context.Context, _lbID string) ([]*cpi.BackendPool, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) CreateBackendPool(_ctx context.Context, _lbID string, _pool *cpi.BackendPool) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) UpdateBackendPool(_ctx context.Context, _lbID string, _pool *cpi.BackendPool) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DeleteBackendPool(_ctx context.Context, _lbID, _poolID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) AddBackendMember(_ctx context.Context, _lbID string, _member *cpi.BackendMember) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) RemoveBackendMember(_ctx context.Context, _lbID, _memberID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) ConfigureHealthCheck(_ctx context.Context, _lbID string, _check *cpi.HealthCheck) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) GetLoadBalancerHealth(_ctx context.Context, _lbID string) (*cpi.HealthStatus, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) AttachBackendServer(_ctx context.Context, _lbID, _poolID, _serverID string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockNetworkManager) DetachBackendServer(_ctx context.Context, _lbID, _poolID, _serverID string) error {
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
		logger:            logger,
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
		logger:            logger,
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
		logger:            logger,
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
			expectedKey: "router_1",
			expectedEnv: "ocf",
		},
		{
			name:        "tcp-router job",
			job:         "tcp-router",
			index:       "0",
			expectedKey: "tcp-router_0",
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
			expectedKey: "router_unknown",
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
		logger:            logger,
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
			expectedOcfKeys:  []string{"router_0", "router_1", "tcp-router_0"},
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
		logger:            logger,
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
		logger:            logger,
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
		logger:            logger,
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
