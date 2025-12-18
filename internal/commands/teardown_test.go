package commands_test

import (
	"context"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/commands"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// fakeStorageCreds implements cpi.StorageManager and a STACKIT-specific deletion method.
type fakeStorageCreds struct {
	deletedGroup string
}

func (f *fakeStorageCreds) DeleteCredentialsGroup(ctx context.Context, groupID string) error {
	f.deletedGroup = groupID

	return nil
}

// cpi.StorageManager required methods (stubs).
func (f *fakeStorageCreds) CreateVolume(ctx context.Context, req *cpi.VolumeRequest) (*cpi.Volume, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorageCreds) GetVolume(ctx context.Context, id string) (*cpi.Volume, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorageCreds) ListVolumes(ctx context.Context, filters map[string]string) ([]*cpi.Volume, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorageCreds) AttachVolume(ctx context.Context, volumeID string, instanceID string, device string) error {
	return nil
}
func (f *fakeStorageCreds) DetachVolume(ctx context.Context, volumeID string, instanceID string) error {
	return nil
}
func (f *fakeStorageCreds) ResizeVolume(ctx context.Context, id string, size int) error { return nil }
func (f *fakeStorageCreds) DeleteVolume(ctx context.Context, id string) error           { return nil }
func (f *fakeStorageCreds) CreateSnapshot(ctx context.Context, volumeID string, name string) (*cpi.Snapshot, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorageCreds) GetSnapshot(ctx context.Context, id string) (*cpi.Snapshot, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorageCreds) ListSnapshots(ctx context.Context, volumeID string, filters map[string]string) ([]*cpi.Snapshot, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorageCreds) DeleteSnapshot(ctx context.Context, id string) error { return nil }
func (f *fakeStorageCreds) CreateBucket(ctx context.Context, req *cpi.BucketRequest) (*cpi.Bucket, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorageCreds) GetBucket(ctx context.Context, name string) (*cpi.Bucket, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorageCreds) ListBuckets(ctx context.Context) ([]*cpi.Bucket, error) { //nolint:nilnil // test fake
	return nil, nil
}
func (f *fakeStorageCreds) DeleteBucket(ctx context.Context, name string) error { return nil }
func (f *fakeStorageCreds) EmptyBucket(ctx context.Context, name string) error  { return nil }
func (f *fakeStorageCreds) IsBucketEmpty(ctx context.Context, name string) (bool, error) {
	return true, nil
}
func (f *fakeStorageCreds) CreateCredentialsGroup(ctx context.Context, req *cpi.CredentialsGroupRequest) (*cpi.CredentialsGroup, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}

type fakeProviderCreds struct{ s *fakeStorageCreds }

func (p *fakeProviderCreds) Name() string                                  { return "stackit" }
func (p *fakeProviderCreds) Region() string                                { return "eu01" }
func (p *fakeProviderCreds) Authenticate(ctx context.Context) error        { return nil }
func (p *fakeProviderCreds) ValidateCredentials(ctx context.Context) error { return nil }

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

func (p *fakeProviderCreds) SupportsStorage() bool                                 { return true }
func (p *fakeProviderCreds) Initialize(ctx context.Context, cfg interface{}) error { return nil }
func (p *fakeProviderCreds) Cleanup(ctx context.Context) error                     { return nil }

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
		SubnetStrategy:        "",
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
