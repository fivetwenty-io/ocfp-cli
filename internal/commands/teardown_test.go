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
func (f *fakeStorageCreds) ListSnapshots(ctx context.Context, volumeID string) ([]*cpi.Snapshot, error) { //nolint:nilnil // test fake
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
		Deployment:            config.Deployment{},    //nolint:exhaustruct // Test config using zero values
		DNS:                   []string{},
		AZs:                   map[string]config.AvailabilityZone{},
		SSHKeyStorageDir:      "",
		Routers:               config.ComponentConfig{}, //nolint:exhaustruct // Test config using zero values
		Cells:                 config.ComponentConfig{}, //nolint:exhaustruct // Test config using zero values
		FQDNs:                 map[string]interface{}{},
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
		BlocName:  "prod",
		Provider:  "",
		Force:     false,
		DryRun:    false,
		All:       false,
		Nuke:      false,
		PublicIPs: false,
		Skip:      nil,
		Mode:      "",
		Output:    "",
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
