package commands

import (
	"context"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// fakeStorageCreds implements cpi.StorageManager and a STACKIT-specific deletion method
type fakeStorageCreds struct{ deletedGroup string }

func (f *fakeStorageCreds) DeleteCredentialsGroup(ctx context.Context, groupID string) error {
	f.deletedGroup = groupID
	return nil
}

// cpi.StorageManager required methods (stubs)
func (f *fakeStorageCreds) CreateVolume(ctx context.Context, req *cpi.CreateVolumeRequest) (*cpi.Volume, error) {
	return nil, nil
}
func (f *fakeStorageCreds) GetVolume(ctx context.Context, id string) (*cpi.Volume, error) {
	return nil, nil
}
func (f *fakeStorageCreds) ListVolumes(ctx context.Context, filters map[string]string) ([]*cpi.Volume, error) {
	return nil, nil
}
func (f *fakeStorageCreds) AttachVolume(ctx context.Context, volumeID string, instanceID string, device string) error {
	return nil
}
func (f *fakeStorageCreds) DetachVolume(ctx context.Context, volumeID string) error     { return nil }
func (f *fakeStorageCreds) ResizeVolume(ctx context.Context, id string, size int) error { return nil }
func (f *fakeStorageCreds) DeleteVolume(ctx context.Context, id string) error           { return nil }
func (f *fakeStorageCreds) CreateSnapshot(ctx context.Context, volumeID string, name string) (*cpi.Snapshot, error) {
	return nil, nil
}
func (f *fakeStorageCreds) GetSnapshot(ctx context.Context, id string) (*cpi.Snapshot, error) {
	return nil, nil
}
func (f *fakeStorageCreds) ListSnapshots(ctx context.Context, volumeID string) ([]*cpi.Snapshot, error) {
	return nil, nil
}
func (f *fakeStorageCreds) DeleteSnapshot(ctx context.Context, id string) error { return nil }
func (f *fakeStorageCreds) CreateBucket(ctx context.Context, name string) (*cpi.Bucket, error) {
	return nil, nil
}
func (f *fakeStorageCreds) GetBucket(ctx context.Context, name string) (*cpi.Bucket, error) {
	return nil, nil
}
func (f *fakeStorageCreds) ListBuckets(ctx context.Context) ([]*cpi.Bucket, error) { return nil, nil }
func (f *fakeStorageCreds) DeleteBucket(ctx context.Context, name string) error    { return nil }
func (f *fakeStorageCreds) EmptyBucket(ctx context.Context, name string) error     { return nil }

type fakeProviderCreds struct{ s *fakeStorageCreds }

func (p *fakeProviderCreds) Name() string                                          { return "stackit" }
func (p *fakeProviderCreds) Region() string                                        { return "eu01" }
func (p *fakeProviderCreds) Authenticate(ctx context.Context) error                { return nil }
func (p *fakeProviderCreds) ValidateCredentials(ctx context.Context) error         { return nil }
func (p *fakeProviderCreds) Network() cpi.NetworkManager                           { return nil }
func (p *fakeProviderCreds) Compute() cpi.ComputeManager                           { return nil }
func (p *fakeProviderCreds) Storage() cpi.StorageManager                           { return p.s }
func (p *fakeProviderCreds) Security() cpi.SecurityManager                         { return nil }
func (p *fakeProviderCreds) LoadBalancer() cpi.LoadBalancerManager                 { return nil }
func (p *fakeProviderCreds) Initialize(ctx context.Context, cfg interface{}) error { return nil }
func (p *fakeProviderCreds) Cleanup(ctx context.Context) error                     { return nil }

func TestTeardownDeletesCredentialsGroup(t *testing.T) {
	st := &fakeStorageCreds{}
	p := &fakeProviderCreds{s: st}
	cfg := &config.Config{Name: "prod", Provider: "stackit", Region: "eu01"}
	sm, err := state.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}
	if _, err := sm.Load("prod"); err != nil {
		t.Fatalf("load state: %v", err)
	}
	m := NewTeardownManager(cfg, p, sm, &TeardownOptions{BlocName: "prod"})

	r := &ResourceToDelete{Type: "credentials_group", ID: "group-123", Name: "ocfp-cli"}
	if err := m.deleteResource(context.Background(), r); err != nil {
		t.Fatalf("deleteResource: %v", err)
	}
	if st.deletedGroup != "group-123" {
		t.Fatalf("expected credentials group deletion to be called, got %q", st.deletedGroup)
	}
}
