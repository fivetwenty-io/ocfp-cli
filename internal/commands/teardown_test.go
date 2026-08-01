package commands_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/commands"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// fakeStorageCreds implements cpi.StorageManager and a STACKIT-specific deletion method.
type fakeStorageCreds struct {
	deletedGroup string
}

func (f *fakeStorageCreds) DeleteCredentialsGroup(_ctx context.Context, groupID string) error {
	f.deletedGroup = groupID

	return nil
}

// cpi.StorageManager required methods (stubs).
func (f *fakeStorageCreds) CreateVolume(_ctx context.Context, _req *cpi.VolumeRequest) (*cpi.Volume, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorageCreds) GetVolume(_ctx context.Context, _id string) (*cpi.Volume, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorageCreds) ListVolumes(_ctx context.Context, _filters map[string]string) ([]*cpi.Volume, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorageCreds) AttachVolume(_ctx context.Context, _volumeID string, _instanceID string, _device string) error {
	return nil
}
func (f *fakeStorageCreds) DetachVolume(_ctx context.Context, _volumeID string, _instanceID string) error {
	return nil
}
func (f *fakeStorageCreds) ResizeVolume(_ctx context.Context, _id string, _size int) error {
	return nil
}
func (f *fakeStorageCreds) DeleteVolume(_ctx context.Context, _id string) error { return nil }
func (f *fakeStorageCreds) CreateSnapshot(_ctx context.Context, _volumeID string, _name string) (*cpi.Snapshot, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorageCreds) GetSnapshot(_ctx context.Context, _id string) (*cpi.Snapshot, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorageCreds) ListSnapshots(_ctx context.Context, _volumeID string, _filters map[string]string) ([]*cpi.Snapshot, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorageCreds) DeleteSnapshot(_ctx context.Context, _id string) error { return nil }
func (f *fakeStorageCreds) CreateBucket(_ctx context.Context, _req *cpi.BucketRequest) (*cpi.Bucket, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorageCreds) GetBucket(_ctx context.Context, _name string) (*cpi.Bucket, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorageCreds) ListBuckets(_ctx context.Context) ([]*cpi.Bucket, error) { //nolint:nilnil // test fake
	return nil, nil
}
func (f *fakeStorageCreds) DeleteBucket(_ctx context.Context, _name string) error { return nil }
func (f *fakeStorageCreds) EmptyBucket(_ctx context.Context, _name string) error  { return nil }
func (f *fakeStorageCreds) IsBucketEmpty(_ctx context.Context, _name string) (bool, error) {
	return true, nil
}
func (f *fakeStorageCreds) CreateCredentialsGroup(_ctx context.Context, _req *cpi.CredentialsGroupRequest) (*cpi.CredentialsGroup, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}

type fakeProviderCreds struct{ s *fakeStorageCreds }

func (p *fakeProviderCreds) Name() string                                   { return "stackit" }
func (p *fakeProviderCreds) Region() string                                 { return "eu01" }
func (p *fakeProviderCreds) Authenticate(_ctx context.Context) error        { return nil }
func (p *fakeProviderCreds) ValidateCredentials(_ctx context.Context) error { return nil }

//nolint:ireturn
func (p *fakeProviderCreds) Network() cpi.NetworkManager { return nil }

//nolint:ireturn
func (p *fakeProviderCreds) Compute() cpi.ComputeManager { return nil }

//nolint:ireturn
func (p *fakeProviderCreds) Storage() cpi.StorageManager { return p.s }

//nolint:ireturn
func (p *fakeProviderCreds) Security() cpi.SecurityManager { return nil }

//nolint:ireturn
func (p *fakeProviderCreds) LoadBalancer() cpi.LoadBalancerManager { return nil }

// New method names for backward compatibility
//
//nolint:ireturn
func (p *fakeProviderCreds) NetworkManager() cpi.NetworkManager { return p.Network() }

//nolint:ireturn
func (p *fakeProviderCreds) ComputeManager() cpi.ComputeManager { return p.Compute() }

//nolint:ireturn
func (p *fakeProviderCreds) StorageManager() cpi.StorageManager { return p.Storage() }

//nolint:ireturn
func (p *fakeProviderCreds) SecurityManager() cpi.SecurityManager { return p.Security() }

//nolint:ireturn
func (p *fakeProviderCreds) LoadBalancerManager() cpi.LoadBalancerManager { return p.LoadBalancer() }

func (p *fakeProviderCreds) SupportsStorage() bool                                   { return true }
func (p *fakeProviderCreds) Initialize(_ctx context.Context, _cfg interface{}) error { return nil }
func (p *fakeProviderCreds) Cleanup(_ctx context.Context) error                      { return nil }

func TestTeardownDeletesCredentialsGroup(t *testing.T) {
	t.Parallel()

	fakeStorage, fakeProvider := setupFakeCredentialsProvider()
	cfg := createTestConfigForCredentials()
	stateManager := setupTestStateManager(t)
	manager := createTeardownManager(cfg, fakeProvider, stateManager)

	resource := createCredentialsGroupResource()

	err := manager.DeleteResource(context.Background(), resource)
	if err != nil {
		t.Fatalf("deleteResource: %v", err)
	}

	verifyCredentialsGroupDeletion(t, fakeStorage, "group-123")
}

func setupFakeCredentialsProvider() (*fakeStorageCreds, *fakeProviderCreds) {
	fakeStorage := &fakeStorageCreds{deletedGroup: ""}
	fakeProvider := &fakeProviderCreds{s: fakeStorage}

	return fakeStorage, fakeProvider
}

func createTestConfigForCredentials() *config.Config {
	return &config.Config{
		Name:                  "prod",
		Provider:              "stackit",
		IaaS:                  "",
		ProjectID:             "",
		OrgID:                 "",
		Region:                "eu01",
		AuthToken:             "",
		ServiceAccountToken:   "",
		ServiceAccountJSON:    "",
		ServiceAccountKeyPath: "",
		APIEndpoint:           "",
		AccessKeyID:           "",
		SecretAccessKey:       "",
		SubscriptionID:        "",
		TenantID:              "",
		ClientID:              "",
		ClientSecret:          "",
		AuthURL:               "",
		Username:              "",
		Password:              "",
		ProjectName:           "",
		DomainName:            "",
		SessionToken:          "",
		BastionIP:             "",
		Network:               config.NetworkConfig{}, //nolint:exhaustruct // Test config using zero values
		Bastion:               config.Bastion{},       //nolint:exhaustruct // Test config using zero values
		Genesis:               config.Genesis{},       //nolint:exhaustruct // Test config using zero values
		DNS:                   []string{},
		AZs:                   map[string]config.AvailabilityZone{},
		SSHKeyStorageDir:      "",
		Routers:               config.ComponentConfig{}, //nolint:exhaustruct // Test config using zero values
		Cells:                 config.ComponentConfig{}, //nolint:exhaustruct // Test config using zero values
		FQDNs:                 &config.FQDNConfig{Mgmt: map[string]string{}, OCF: map[string]string{}},
		S3:                    map[string]string{},
		AllowedIngressIPs:     []string{},
		Type:                  "",
		Environment:           "",
		Subnets:               []config.Subnet{},
		LBs:                   map[string]config.LBService{},
		Users:                 map[string]string{},
		RouterPublicIPs:       0,
		CFSSHPublicIPs:        0,
		JumpboxPublicIPs:      0,
		TCPRouterPublicIPs:    0,
		Blobstore:             config.BlobstoreConfig{}, //nolint:exhaustruct // Test config using zero values
	}
}

func setupTestStateManager(t *testing.T) *state.Manager {
	t.Helper()

	stateManager, err := state.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}

	_, err = stateManager.Load("prod")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	return stateManager
}

func createTeardownManager(cfg *config.Config, fakeProvider *fakeProviderCreds, stateManager *state.Manager) *commands.TeardownManager {
	return commands.NewTeardownManager(cfg, fakeProvider, stateManager, &commands.TeardownOptions{
		BlocName:       "prod",
		Provider:       "",
		Force:          false,
		DryRun:         false,
		All:            false,
		Nuke:           false,
		PublicIPs:      false,
		Bastion:        false,
		Servers:        false,
		Volumes:        false,
		Snapshots:      false,
		Buckets:        false,
		SecurityGroups: false,
		Network:        false,
		Empty:          false,
		Skip:           nil,
		Mode:           "",
		Output:         "",
	})
}

func createCredentialsGroupResource() *commands.ResourceToDelete {
	return &commands.ResourceToDelete{
		Type:         "credentials_group",
		ID:           "group-123",
		Name:         "ocfp-cli",
		Dependencies: nil,
		State:        "",
		Properties:   nil,
	}
}

func verifyCredentialsGroupDeletion(t *testing.T, fakeStorage *fakeStorageCreds, expectedGroupID string) {
	t.Helper()

	if fakeStorage.deletedGroup != expectedGroupID {
		t.Fatalf("expected credentials group deletion to be called, got %q", fakeStorage.deletedGroup)
	}
}

func TestFilterResourcesBastionOnly(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		blocName  string
		bastion   bool
		resources []*commands.ResourceToDelete
		expected  []string // Expected resource names after filtering
	}{
		{
			name:     "Bastion flag filters to only bastion instance",
			blocName: "test-bloc",
			bastion:  true,
			resources: []*commands.ResourceToDelete{
				{Type: "instance", Name: "test-bloc-bastion", ID: "i-123"},
				{Type: "instance", Name: "other-instance", ID: "i-456"},
				{Type: "volume", Name: "test-volume", ID: "v-789"},
				{Type: "network", Name: "test-network", ID: "n-012"},
			},
			expected: []string{"test-bloc-bastion"},
		},
		{
			name:     "Bastion flag with no bastion instance returns empty",
			blocName: "test-bloc",
			bastion:  true,
			resources: []*commands.ResourceToDelete{
				{Type: "instance", Name: "other-instance", ID: "i-456"},
				{Type: "volume", Name: "test-volume", ID: "v-789"},
			},
			expected: []string{},
		},
		{
			name:     "No bastion flag returns all resources",
			blocName: "test-bloc",
			bastion:  false,
			resources: []*commands.ResourceToDelete{
				{Type: "instance", Name: "test-bloc-bastion", ID: "i-123"},
				{Type: "instance", Name: "other-instance", ID: "i-456"},
				{Type: "volume", Name: "test-volume", ID: "v-789"},
			},
			expected: []string{"test-bloc-bastion", "other-instance", "test-volume"},
		},
		{
			name:     "Bastion flag ignores bastion from different bloc",
			blocName: "test-bloc",
			bastion:  true,
			resources: []*commands.ResourceToDelete{
				{Type: "instance", Name: "other-bloc-bastion", ID: "i-123"},
				{Type: "instance", Name: "test-bloc-bastion", ID: "i-456"},
			},
			expected: []string{"test-bloc-bastion"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{
				Name:     tc.blocName,
				Provider: "aws",
			}

			stateManager, err := state.NewManager(t.TempDir())
			if err != nil {
				t.Fatalf("failed to create state manager: %v", err)
			}

			_, err = stateManager.Load(tc.blocName)
			if err != nil {
				t.Fatalf("failed to load state: %v", err)
			}

			opts := &commands.TeardownOptions{
				BlocName: tc.blocName,
				Provider: cfg.Provider,
				Bastion:  tc.bastion,
			}

			manager := commands.NewTeardownManager(cfg, &fakeProviderCreds{}, stateManager, opts)

			// Use reflection to call the private filterResources method
			// In real test, we'd test the public Execute method with proper mocking
			filtered := manager.TestFilterResources(tc.resources)

			if len(filtered) != len(tc.expected) {
				t.Errorf("expected %d resources, got %d", len(tc.expected), len(filtered))
			}

			for i, expectedName := range tc.expected {
				if i >= len(filtered) {
					t.Errorf("missing resource at index %d: expected %s", i, expectedName)

					continue
				}

				if filtered[i].Name != expectedName {
					t.Errorf("resource %d: expected name %s, got %s", i, expectedName, filtered[i].Name)
				}
			}
		})
	}
}

func TestFilterResourcesCombinedBastionAndSelective(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name            string
		blocName        string
		bastion         bool
		securityGroups  bool
		servers         bool
		resources       []*commands.ResourceToDelete
		expectedNames   []string
		expectedTypeLen map[string]int
	}{
		{
			name:           "Bastion and security groups flags include both",
			blocName:       "520-aws-wayne",
			bastion:        true,
			securityGroups: true,
			resources: []*commands.ResourceToDelete{
				{Type: "instance", Name: "520-aws-wayne-bastion", ID: "i-123"},
				{Type: "instance", Name: "other-instance", ID: "i-456"},
				{Type: "security_group", Name: "sg-1", ID: "sg-1"},
				{Type: "security_group", Name: "sg-2", ID: "sg-2"},
				{Type: "volume", Name: "vol-1", ID: "v-1"},
			},
			expectedNames:   []string{"520-aws-wayne-bastion", "sg-1", "sg-2"},
			expectedTypeLen: map[string]int{"instance": 1, "security_group": 2},
		},
		{
			name:     "Bastion and servers flags include both bastion and other instances",
			blocName: "test-bloc",
			bastion:  true,
			servers:  true,
			resources: []*commands.ResourceToDelete{
				{Type: "instance", Name: "test-bloc-bastion", ID: "i-123"},
				{Type: "instance", Name: "worker-1", ID: "i-456"},
				{Type: "instance", Name: "worker-2", ID: "i-789"},
				{Type: "keypair", Name: "key-1", ID: "k-1"},
				{Type: "volume", Name: "vol-1", ID: "v-1"},
			},
			expectedNames:   []string{"test-bloc-bastion", "worker-1", "worker-2", "key-1"},
			expectedTypeLen: map[string]int{"instance": 3, "keypair": 1},
		},
		{
			name:           "Bastion with security groups only includes bastion from correct bloc",
			blocName:       "prod",
			bastion:        true,
			securityGroups: true,
			resources: []*commands.ResourceToDelete{
				{Type: "instance", Name: "dev-bastion", ID: "i-111"},
				{Type: "instance", Name: "prod-bastion", ID: "i-222"},
				{Type: "security_group", Name: "sg-1", ID: "sg-1"},
			},
			expectedNames:   []string{"prod-bastion", "sg-1"},
			expectedTypeLen: map[string]int{"instance": 1, "security_group": 1},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{
				Name:     tc.blocName,
				Provider: "aws",
			}

			stateManager, err := state.NewManager(t.TempDir())
			if err != nil {
				t.Fatalf("failed to create state manager: %v", err)
			}

			_, err = stateManager.Load(tc.blocName)
			if err != nil {
				t.Fatalf("failed to load state: %v", err)
			}

			opts := &commands.TeardownOptions{
				BlocName:       tc.blocName,
				Provider:       cfg.Provider,
				Bastion:        tc.bastion,
				SecurityGroups: tc.securityGroups,
				Servers:        tc.servers,
			}

			manager := commands.NewTeardownManager(cfg, &fakeProviderCreds{}, stateManager, opts)

			filtered := manager.TestFilterResources(tc.resources)

			// Check total count
			if len(filtered) != len(tc.expectedNames) {
				t.Errorf("expected %d resources, got %d", len(tc.expectedNames), len(filtered))
			}

			// Check resource names
			filteredNames := make([]string, len(filtered))
			for i, r := range filtered {
				filteredNames[i] = r.Name
			}

			for _, expectedName := range tc.expectedNames {
				found := false
				for _, name := range filteredNames {
					if name == expectedName {
						found = true

						break
					}
				}

				if !found {
					t.Errorf("expected resource %s not found in filtered results", expectedName)
				}
			}

			// Check resource type counts
			typeCounts := make(map[string]int)
			for _, r := range filtered {
				typeCounts[r.Type]++
			}

			for expectedType, expectedCount := range tc.expectedTypeLen {
				if typeCounts[expectedType] != expectedCount {
					t.Errorf("expected %d %s resources, got %d", expectedCount, expectedType, typeCounts[expectedType])
				}
			}
		})
	}
}

func TestMergeResources(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		existing    []*commands.ResourceToDelete
		discovered  []*commands.ResourceToDelete
		expectedIDs []string
	}{
		{
			name: "no duplicates added",
			existing: []*commands.ResourceToDelete{
				{Type: "instance", Name: "bastion", ID: "i-111"},
				{Type: "network", Name: "vpc", ID: "vpc-222"},
			},
			discovered: []*commands.ResourceToDelete{
				{Type: "instance", Name: "bastion", ID: "i-111"},
				{Type: "network", Name: "vpc", ID: "vpc-222"},
			},
			expectedIDs: []string{"i-111", "vpc-222"},
		},
		{
			name: "cloud-only resources added",
			existing: []*commands.ResourceToDelete{
				{Type: "network", Name: "vpc", ID: "vpc-222"},
			},
			discovered: []*commands.ResourceToDelete{
				{Type: "instance", Name: "orphaned-bastion", ID: "i-333"},
				{Type: "network", Name: "vpc", ID: "vpc-222"},
			},
			expectedIDs: []string{"vpc-222", "i-333"},
		},
		{
			name:        "both empty",
			existing:    []*commands.ResourceToDelete{},
			discovered:  []*commands.ResourceToDelete{},
			expectedIDs: []string{},
		},
		{
			name:     "empty existing gets all discovered",
			existing: []*commands.ResourceToDelete{},
			discovered: []*commands.ResourceToDelete{
				{Type: "instance", Name: "bastion", ID: "i-111"},
			},
			expectedIDs: []string{"i-111"},
		},
		{
			name: "empty discovered keeps existing",
			existing: []*commands.ResourceToDelete{
				{Type: "instance", Name: "bastion", ID: "i-111"},
			},
			discovered:  []*commands.ResourceToDelete{},
			expectedIDs: []string{"i-111"},
		},
		{
			name: "mixed overlap and new",
			existing: []*commands.ResourceToDelete{
				{Type: "instance", Name: "worker", ID: "i-111"},
				{Type: "volume", Name: "vol", ID: "vol-222"},
			},
			discovered: []*commands.ResourceToDelete{
				{Type: "instance", Name: "worker", ID: "i-111"},
				{Type: "instance", Name: "orphaned-bastion", ID: "i-333"},
				{Type: "security_group", Name: "sg", ID: "sg-444"},
			},
			expectedIDs: []string{"i-111", "vol-222", "i-333", "sg-444"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := commands.TestMergeResources(tc.existing, tc.discovered)

			if len(result) != len(tc.expectedIDs) {
				t.Fatalf("expected %d resources, got %d", len(tc.expectedIDs), len(result))
			}

			resultIDs := make(map[string]bool, len(result))
			for _, r := range result {
				resultIDs[r.ID] = true
			}

			for _, expectedID := range tc.expectedIDs {
				if !resultIDs[expectedID] {
					t.Errorf("expected resource ID %s not found in merged results", expectedID)
				}
			}
		})
	}
}

func TestFilterResourcesSelectiveMode(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name            string
		servers         bool
		volumes         bool
		snapshots       bool
		buckets         bool
		securityGroups  bool
		network         bool
		resources       []*commands.ResourceToDelete
		expectedNames   []string
		expectedTypeLen map[string]int // Count of each resource type expected
	}{
		{
			name:    "Servers flag includes instances and keypairs",
			servers: true,
			resources: []*commands.ResourceToDelete{
				{Type: "instance", Name: "server-1", ID: "i-1"},
				{Type: "instance", Name: "server-2", ID: "i-2"},
				{Type: "keypair", Name: "key-1", ID: "k-1"},
				{Type: "volume", Name: "vol-1", ID: "v-1"},
				{Type: "network", Name: "net-1", ID: "n-1"},
			},
			expectedNames:   []string{"server-1", "server-2", "key-1"},
			expectedTypeLen: map[string]int{"instance": 2, "keypair": 1},
		},
		{
			name:    "Volumes flag includes only volumes",
			volumes: true,
			resources: []*commands.ResourceToDelete{
				{Type: "volume", Name: "vol-1", ID: "v-1"},
				{Type: "volume", Name: "vol-2", ID: "v-2"},
				{Type: "instance", Name: "server-1", ID: "i-1"},
				{Type: "snapshot", Name: "snap-1", ID: "s-1"},
			},
			expectedNames:   []string{"vol-1", "vol-2"},
			expectedTypeLen: map[string]int{"volume": 2},
		},
		{
			name:      "Snapshots flag includes only snapshots",
			snapshots: true,
			resources: []*commands.ResourceToDelete{
				{Type: "snapshot", Name: "snap-1", ID: "s-1"},
				{Type: "snapshot", Name: "snap-2", ID: "s-2"},
				{Type: "volume", Name: "vol-1", ID: "v-1"},
			},
			expectedNames:   []string{"snap-1", "snap-2"},
			expectedTypeLen: map[string]int{"snapshot": 2},
		},
		{
			name:    "Network flag includes networks, subnets, and load balancers",
			network: true,
			resources: []*commands.ResourceToDelete{
				{Type: "network", Name: "net-1", ID: "n-1"},
				{Type: "subnet", Name: "subnet-1", ID: "sn-1"},
				{Type: "loadbalancer", Name: "lb-1", ID: "lb-1"},
				{Type: "instance", Name: "server-1", ID: "i-1"},
			},
			expectedNames:   []string{"net-1", "subnet-1", "lb-1"},
			expectedTypeLen: map[string]int{"network": 1, "subnet": 1, "loadbalancer": 1},
		},
		{
			name:           "Security groups flag includes only security groups",
			securityGroups: true,
			resources: []*commands.ResourceToDelete{
				{Type: "security_group", Name: "sg-1", ID: "sg-1"},
				{Type: "security_group", Name: "sg-2", ID: "sg-2"},
				{Type: "instance", Name: "server-1", ID: "i-1"},
			},
			expectedNames:   []string{"sg-1", "sg-2"},
			expectedTypeLen: map[string]int{"security_group": 2},
		},
		{
			name:    "Multiple flags combine resource types",
			servers: true,
			volumes: true,
			resources: []*commands.ResourceToDelete{
				{Type: "instance", Name: "server-1", ID: "i-1"},
				{Type: "volume", Name: "vol-1", ID: "v-1"},
				{Type: "snapshot", Name: "snap-1", ID: "s-1"},
				{Type: "network", Name: "net-1", ID: "n-1"},
			},
			expectedNames:   []string{"server-1", "vol-1"},
			expectedTypeLen: map[string]int{"instance": 1, "volume": 1},
		},
		{
			name:      "All selective flags include all resource types",
			servers:   true,
			volumes:   true,
			snapshots: true,
			buckets:   true,
			network:   true,
			resources: []*commands.ResourceToDelete{
				{Type: "instance", Name: "server-1", ID: "i-1"},
				{Type: "volume", Name: "vol-1", ID: "v-1"},
				{Type: "snapshot", Name: "snap-1", ID: "s-1"},
				{Type: "bucket", Name: "bucket-1", ID: "b-1"},
				{Type: "network", Name: "net-1", ID: "n-1"},
			},
			expectedNames:   []string{"server-1", "vol-1", "snap-1", "bucket-1", "net-1"},
			expectedTypeLen: map[string]int{"instance": 1, "volume": 1, "snapshot": 1, "bucket": 1, "network": 1},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{
				Name:     "test-bloc",
				Provider: "aws",
			}

			stateManager, err := state.NewManager(t.TempDir())
			if err != nil {
				t.Fatalf("failed to create state manager: %v", err)
			}

			_, err = stateManager.Load("test-bloc")
			if err != nil {
				t.Fatalf("failed to load state: %v", err)
			}

			opts := &commands.TeardownOptions{
				BlocName:       "test-bloc",
				Provider:       cfg.Provider,
				Servers:        tc.servers,
				Volumes:        tc.volumes,
				Snapshots:      tc.snapshots,
				Buckets:        tc.buckets,
				SecurityGroups: tc.securityGroups,
				Network:        tc.network,
			}

			manager := commands.NewTeardownManager(cfg, &fakeProviderCreds{}, stateManager, opts)

			filtered := manager.TestFilterResources(tc.resources)

			// Check total count
			if len(filtered) != len(tc.expectedNames) {
				t.Errorf("expected %d resources, got %d", len(tc.expectedNames), len(filtered))
			}

			// Check resource names
			filteredNames := make([]string, len(filtered))
			for i, r := range filtered {
				filteredNames[i] = r.Name
			}

			for _, expectedName := range tc.expectedNames {
				found := false
				for _, name := range filteredNames {
					if name == expectedName {
						found = true

						break
					}
				}

				if !found {
					t.Errorf("expected resource %s not found in filtered results", expectedName)
				}
			}

			// Check resource type counts
			typeCounts := make(map[string]int)
			for _, r := range filtered {
				typeCounts[r.Type]++
			}

			for expectedType, expectedCount := range tc.expectedTypeLen {
				if typeCounts[expectedType] != expectedCount {
					t.Errorf("expected %d %s resources, got %d", expectedCount, expectedType, typeCounts[expectedType])
				}
			}
		})
	}
}

// ---------- T10–T13: StateIsEmpty ----------

// TestStateIsEmpty_AbsentFile: absent file → true (nothing to delete).
func TestStateIsEmpty_AbsentFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	// File not written; must not exist.
	empty, err := commands.StateIsEmpty(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !empty {
		t.Fatal("expected StateIsEmpty=true for absent file")
	}
}

// TestStateIsEmpty_EmptyJSON: file containing "{}" → true.
func TestStateIsEmpty_EmptyJSON(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	empty, err := commands.StateIsEmpty(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !empty {
		t.Fatal("expected StateIsEmpty=true for empty JSON object")
	}
}

// TestStateIsEmpty_ValidStateBoshKey: file with top-level "bosh" key → false.
func TestStateIsEmpty_ValidStateBoshKey(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	content := `{"bosh":{"job_name":"bosh","vm_cid":"vm-abc123"}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	empty, err := commands.StateIsEmpty(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if empty {
		t.Fatal("expected StateIsEmpty=false when state.json has 'bosh' key")
	}
}

// TestStateIsEmpty_CorruptedJSON: malformed JSON → false (conservative).
func TestStateIsEmpty_CorruptedJSON(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not valid json!!"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	empty, err := commands.StateIsEmpty(path)
	if err != nil {
		t.Fatalf("unexpected error (conservative path returns nil error): %v", err)
	}
	if empty {
		t.Fatal("expected StateIsEmpty=false for corrupted JSON (conservative: let bosh delete-env fail)")
	}
}

// ---------- T50–T51: PVE VM existence probe ----------

// pveQEMUListResponse builds a PVE API envelope for /nodes/{node}/qemu responses.
func pveQEMUListResponse(t *testing.T, vms []map[string]interface{}) []byte {
	t.Helper()
	env := map[string]interface{}{"data": vms}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// newPVEConfigForTest builds a config.Config pointing at ts.URL for PVE API calls.
func newPVEConfigForTest(ts *httptest.Server, blocName string) *config.Config {
	return &config.Config{
		Name:        blocName,
		Provider:    "pve",
		Region:      "pvenode1",
		APIEndpoint: ts.URL,
		AuthToken:   "root@pam!testtoken",
		TokenSecret: "00000000-0000-0000-0000-000000000001",
		VerifySSL:   false,
	}
}

// initTestLogger initializes the logger for test contexts.
func initTestLogger(t *testing.T) logger.Logger {
	t.Helper()
	logDir := t.TempDir()
	err := logger.Initialize(logger.Config{
		Level:      "info",
		Debug:      false,
		Verbose:    false,
		Trace:      false,
		NoLog:      true,
		LogDir:     logDir,
		BlocName:   "test",
		Command:    "teardown",
		Subcommand: "",
		RequestID:  "",
	})
	if err != nil {
		t.Fatalf("initialize logger: %v", err)
	}
	return logger.Get()
}

// TestTeardown_VMAlreadyGone_NoOp: VMExists returns false → done=true (no-op).
func TestTeardown_VMAlreadyGone_NoOp(t *testing.T) {
	t.Parallel()

	body := pveQEMUListResponse(t, []map[string]interface{}{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(ts.Close)

	cfg := newPVEConfigForTest(ts, "ocfp-mgmt")
	log := initTestLogger(t)

	done, err := commands.TestPVECheckAlreadyTornDown(context.Background(), cfg, log)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Fatal("expected done=true when VM list is empty (VM already gone)")
	}
}

// TestTeardown_VMExists_RunsDeleteEnv: VMExists returns true → done=false (proceed with teardown).
func TestTeardown_VMExists_RunsDeleteEnv(t *testing.T) {
	t.Parallel()

	body := pveQEMUListResponse(t, []map[string]interface{}{
		{"vmid": 901, "name": "ocfp-mgmt", "status": "running"},
	})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(ts.Close)

	cfg := newPVEConfigForTest(ts, "ocfp-mgmt")
	log := initTestLogger(t)

	done, err := commands.TestPVECheckAlreadyTornDown(context.Background(), cfg, log)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done {
		t.Fatal("expected done=false when VM is present (teardown should proceed)")
	}
}

// TestTeardownSkipArtifactsProtectsCloudInstance verifies that "--skip artifacts"
// excludes the retained artifacts VM even when it is cloud-discovered as a
// generic "instance" named "<bloc>-artifacts" (rather than its state type
// "artifacts"). This is the regression guard for the bug where a --force
// teardown deleted the kept artifacts/RustFS VM.
func TestTeardownSkipArtifactsProtectsCloudInstance(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "ocfp-lab-wayne", Provider: "pve"}

	sm, err := state.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}
	if _, err = sm.Load("ocfp-lab-wayne"); err != nil {
		t.Fatalf("state load: %v", err)
	}

	opts := &commands.TeardownOptions{
		BlocName: "ocfp-lab-wayne",
		Provider: "pve",
		Skip:     []string{"artifacts"},
	}
	manager := commands.NewTeardownManager(cfg, &fakeProviderCreds{}, sm, opts)

	resources := []*commands.ResourceToDelete{
		{Type: "instance", Name: "ocfp-lab-wayne-bastion", ID: "100"},
		{Type: "instance", Name: "ocfp-lab-wayne-artifacts", ID: "101"},
		{Type: "security_group", Name: "ocfp-lab-wayne-artifacts", ID: "g-art"},
	}

	filtered := manager.TestFilterResources(resources)

	for _, r := range filtered {
		if r.Name == "ocfp-lab-wayne-artifacts" {
			t.Errorf("--skip artifacts must exclude %q (type %s, id %s) but it was kept", r.Name, r.Type, r.ID)
		}
	}

	foundBastion := false
	for _, r := range filtered {
		if r.Name == "ocfp-lab-wayne-bastion" {
			foundBastion = true
		}
	}
	if !foundBastion {
		t.Error("--skip artifacts must NOT exclude the bastion instance")
	}
}

// TestTeardownBelongsToTargetBloc verifies the bloc-attribution gate that
// prevents cloud network/security-group discovery from deleting other blocs'
// resources or shared host infrastructure (the PVE lvnetNNN / vmbrN leak).
func TestTeardownBelongsToTargetBloc(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "ocfp-lab-wayne", Provider: "pve"}

	sm, err := state.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}
	if _, err = sm.Load("ocfp-lab-wayne"); err != nil {
		t.Fatalf("state load: %v", err)
	}

	opts := &commands.TeardownOptions{BlocName: "ocfp-lab-wayne", Provider: "pve"}
	manager := commands.NewTeardownManager(cfg, &fakeProviderCreds{}, sm, opts)

	cases := []struct {
		name string
		r    *commands.ResourceToDelete
		want bool
	}{
		{"from-state", &commands.ResourceToDelete{Name: "lvnet001", FromState: true}, true},
		{"bloc-tag", &commands.ResourceToDelete{Name: "anything", Tags: map[string]string{"bloc": "ocfp-lab-wayne"}}, true},
		{"bloc-name-prefix", &commands.ResourceToDelete{Name: "ocfp-lab-wayne-infra"}, true},
		{"bloc-name-exact", &commands.ResourceToDelete{Name: "ocfp-lab-wayne"}, true},
		{"other-bloc-vnet", &commands.ResourceToDelete{Name: "lvnet004"}, false},
		{"host-bridge", &commands.ResourceToDelete{Name: "vmbr0"}, false},
		{"other-bloc-tag", &commands.ResourceToDelete{Name: "x", Tags: map[string]string{"bloc": "ocfp-lab-kevin"}}, false},
		{"stale-other-naming", &commands.ResourceToDelete{Name: "ocfp-pve-wayne-infra"}, false},
		// Shared PVE template disks (VMID-named, untagged) must NOT be
		// attributable to the bloc — these were swept by the --force volume
		// discovery that lacked an ownership gate.
		{"shared-base-template-disk", &commands.ResourceToDelete{Type: "volume", Name: "base-9001-disk-0"}, false},
		{"shared-generic-template-disk", &commands.ResourceToDelete{Type: "volume", Name: "base-9000-disk-0"}, false},
		{"other-vm-cloudinit", &commands.ResourceToDelete{Type: "volume", Name: "vm-9000-cloudinit"}, false},
		{"other-vm-disk", &commands.ResourceToDelete{Type: "volume", Name: "vm-15944-disk-0"}, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := manager.TestBelongsToTargetBloc(tc.r); got != tc.want {
				t.Errorf("belongsToTargetBloc(%s)=%v, want %v", tc.r.Name, got, tc.want)
			}
		})
	}
}
