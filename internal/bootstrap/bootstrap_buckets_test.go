package bootstrap_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// fakeStorage implements cpi.StorageManager partially for testing createBuckets.
type fakeStorage struct {
	created        []string
	enabledCalls   map[string]bool
	lifecycleCalls map[string]int32
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{
		created:        nil,
		enabledCalls:   make(map[string]bool),
		lifecycleCalls: make(map[string]int32),
	}
}

func (f *fakeStorage) CreateBucket(_ctx context.Context, req *cpi.BucketRequest) (*cpi.Bucket, error) {
	f.created = append(f.created, req.Name)

	return &cpi.Bucket{
		ID:           req.Name + "-id",
		Name:         req.Name,
		Region:       "",
		StorageClass: "",
		Versioning:   false,
		Encryption:   false,
		Public:       false,
		Size:         0,
		ObjectCount:  0,
		Tags:         nil,
		CreatedAt:    time.Time{},
	}, nil
}
func (f *fakeStorage) ListBuckets(_ctx context.Context) ([]*cpi.Bucket, error) { //nolint:nilnil // test fake intentionally returns no data and no error
	return nil, nil //nolint:nilnil // test fake
}

// Unused interface methods.
func (f *fakeStorage) CreateVolume(_ctx context.Context, _req *cpi.VolumeRequest) (*cpi.Volume, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorage) GetVolume(_ctx context.Context, _id string) (*cpi.Volume, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorage) ListVolumes(_ctx context.Context, _filters map[string]string) ([]*cpi.Volume, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorage) AttachVolume(_ctx context.Context, _volumeID, _instanceID, _device string) error {
	return nil
}
func (f *fakeStorage) DetachVolume(_ctx context.Context, _volumeID string, _instanceID string) error {
	return nil
}
func (f *fakeStorage) ResizeVolume(_ctx context.Context, _id string, _size int) error { return nil }
func (f *fakeStorage) DeleteVolume(_ctx context.Context, _id string) error            { return nil }
func (f *fakeStorage) CreateSnapshot(_ctx context.Context, _volumeID string, _name string) (*cpi.Snapshot, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorage) GetSnapshot(_ctx context.Context, _id string) (*cpi.Snapshot, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorage) ListSnapshots(_ctx context.Context, _volumeID string, _filters map[string]string) ([]*cpi.Snapshot, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorage) DeleteSnapshot(_ctx context.Context, _id string) error { return nil }
func (f *fakeStorage) GetBucket(_ctx context.Context, _name string) (*cpi.Bucket, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorage) DeleteBucket(_ctx context.Context, _name string) error { return nil }
func (f *fakeStorage) EmptyBucket(_ctx context.Context, _name string) error  { return nil }
func (f *fakeStorage) IsBucketEmpty(_ctx context.Context, _name string) (bool, error) {
	return true, nil
}
func (f *fakeStorage) CreateCredentialsGroup(_ctx context.Context, req *cpi.CredentialsGroupRequest) (*cpi.CredentialsGroup, error) {
	return &cpi.CredentialsGroup{
		ID:        "group-123",
		Name:      req.Name,
		CreatedAt: time.Now(),
	}, nil
}

func (f *fakeStorage) EnableBucketVersioning(_ctx context.Context, name string) error {
	if f.enabledCalls == nil {
		f.enabledCalls = make(map[string]bool)
	}

	f.enabledCalls[name] = true

	return nil
}

func (f *fakeStorage) SetBucketVersioning(_ctx context.Context, name string, enabled bool) error {
	if f.enabledCalls == nil {
		f.enabledCalls = make(map[string]bool)
	}

	if enabled {
		f.enabledCalls[name] = true
	} else {
		delete(f.enabledCalls, name)
	}

	return nil
}
func (f *fakeStorage) SetBucketLifecycleNoncurrentDays(_ctx context.Context, name string, days int32) error {
	if f.lifecycleCalls == nil {
		f.lifecycleCalls = make(map[string]int32)
	}

	f.lifecycleCalls[name] = days

	return nil
}

func (f *fakeStorage) SetBucketLifecycle(_ctx context.Context, name string, days int) error {
	if f.lifecycleCalls == nil {
		f.lifecycleCalls = make(map[string]int32)
	}

	f.lifecycleCalls[name] = int32(days)

	return nil
}
func (f *fakeStorage) EnsureObjectStorageCredentialsGroup(_ctx context.Context, _displayName string) (string, error) {
	return "group-123", nil
}

type fakeProvider struct{ s cpi.StorageManager }

func (p *fakeProvider) Name() string                                   { return "fake" }
func (p *fakeProvider) Region() string                                 { return "eu01" }
func (p *fakeProvider) Authenticate(_ctx context.Context) error        { return nil }
func (p *fakeProvider) ValidateCredentials(_ctx context.Context) error { return nil }

//nolint:ireturn
func (p *fakeProvider) Network() cpi.NetworkManager { return nil }

//nolint:ireturn
func (p *fakeProvider) Compute() cpi.ComputeManager { return nil }

//nolint:ireturn
func (p *fakeProvider) Storage() cpi.StorageManager { return p.s }

//nolint:ireturn
func (p *fakeProvider) Security() cpi.SecurityManager { return nil }

//nolint:ireturn
func (p *fakeProvider) LoadBalancer() cpi.LoadBalancerManager { return nil }

// New method names for backward compatibility
//
//nolint:ireturn
func (p *fakeProvider) NetworkManager() cpi.NetworkManager { return p.Network() }

//nolint:ireturn
func (p *fakeProvider) ComputeManager() cpi.ComputeManager { return p.Compute() }

//nolint:ireturn
func (p *fakeProvider) StorageManager() cpi.StorageManager { return p.Storage() }

//nolint:ireturn
func (p *fakeProvider) SecurityManager() cpi.SecurityManager { return p.Security() }

//nolint:ireturn
func (p *fakeProvider) LoadBalancerManager() cpi.LoadBalancerManager { return p.LoadBalancer() }

func (p *fakeProvider) SupportsStorage() bool                                   { return true }
func (p *fakeProvider) Initialize(_ctx context.Context, _cfg interface{}) error { return nil }
func (p *fakeProvider) Cleanup(_ctx context.Context) error                      { return nil }

func TestCreateBucketsEnsuresExpectedNames(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	// Isolate state under temp directory
	stateDir := filepath.Join(tmp, ".ocfp-state")

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}

	_, err = stateManager.Load("prod")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	fakeStore := newFakeStorage()
	provider := &fakeProvider{s: fakeStore}
	cfg := config.NewTestConfig().WithName("prod").WithRegion("eu01").WithBootstrapNetwork().WithBootstrapBastion().Build()
	manager := bootstrap.NewManager(cfg, provider, stateManager, &bootstrap.Options{
		BlocName: "prod",
		Provider: "stackit",
		Region:   "eu01",
		Force:    false,
		DryRun:   false,
		Output:   "",
		Timeout:  0,
	})

	ctx := context.Background()

	err = manager.CreateBuckets(ctx)
	if err != nil {
		t.Fatalf("createBuckets: %v", err)
	}

	want := map[string]bool{
		// mgmt environment buckets
		"prod-mgmt-bosh":      true,
		"prod-mgmt-artifacts": true,
		"prod-mgmt-shield":    true,
		// ocf environment buckets
		"prod-ocf-bosh":             true,
		"prod-ocf-artifacts":        true,
		"prod-ocf-cf-packages":      true,
		"prod-ocf-cf-droplets":      true,
		"prod-ocf-cf-buildpacks":    true,
		"prod-ocf-cf-resource-pool": true,
		"prod-ocf-shield":           true,
	}
	if len(fakeStore.created) != len(want) {
		t.Fatalf("created %d buckets, want %d: %+v", len(fakeStore.created), len(want), fakeStore.created)
	}

	for _, name := range fakeStore.created {
		if !want[name] {
			t.Fatalf("unexpected bucket created: %s", name)
		}
	}

	// Unset optional feature flag for cleanliness
	_ = os.Unsetenv("OCFP_ENABLE_BUCKET_POLICIES")
}

func TestCreateBucketsPoliciesAppliedWhenEnabled(t *testing.T) {
	t.Parallel()

	stateManager := setupTestStateManager(t)
	provider, store := createFakeProvider()
	cfg := createBlobstoreTestConfig()
	manager := createTestBootstrapManager(cfg, provider, stateManager)

	ctx := context.Background()

	err := manager.CreateBuckets(ctx)
	if err != nil {
		t.Fatalf("createBuckets: %v", err)
	}

	verifyPolicyCallsWhenEnabled(t, store)
}

func setupTestStateManager(t *testing.T) *state.Manager {
	t.Helper()
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, ".ocfp-state")

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}

	_, err = stateManager.Load("prod")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	return stateManager
}

func createFakeProvider() (*fakeProvider, *fakeStorage) {
	fakeStore := newFakeStorage()

	return &fakeProvider{s: fakeStore}, fakeStore
}

func createBlobstoreTestConfig() *config.Config {
	cfg := config.NewTestConfig().WithName("prod").WithRegion("eu01").WithBootstrapNetwork().WithBootstrapBastion().Build()
	cfg.Blobstore.EnablePolicies = true
	cfg.Blobstore.CFBuildpacks.Versioning = true
	cfg.Blobstore.CFBuildpacks.NoncurrentDays = 11
	cfg.Blobstore.CFDroplets.Versioning = true
	cfg.Blobstore.CFDroplets.NoncurrentDays = 5
	cfg.Blobstore.CFAppPackages.Versioning = false
	cfg.Blobstore.CFAppPackages.NoncurrentDays = 3
	cfg.Blobstore.BoshBlobstore.Versioning = true
	cfg.Blobstore.BoshBlobstore.NoncurrentDays = 9

	return cfg
}

func createTestBootstrapManager(cfg *config.Config, provider cpi.Provider, stateManager *state.Manager) *bootstrap.Manager {
	return bootstrap.NewManager(cfg, provider, stateManager, &bootstrap.Options{
		BlocName: "prod",
		Provider: "stackit",
		Region:   "eu01",
		Force:    false,
		DryRun:   false,
		Output:   "",
		Timeout:  0,
	})
}

func verifyPolicyCallsWhenEnabled(t *testing.T, store *fakeStorage) {
	t.Helper()

	if !store.enabledCalls["prod-ocf-cf-buildpacks"] || store.lifecycleCalls["prod-ocf-cf-buildpacks"] != 11 {
		t.Fatalf("expected policies on ocf-buildpacks: %+v %+v", store.enabledCalls, store.lifecycleCalls)
	}

	if !store.enabledCalls["prod-ocf-cf-droplets"] || store.lifecycleCalls["prod-ocf-cf-droplets"] != 5 {
		t.Fatalf("expected policies on ocf-droplets")
	}

	if store.enabledCalls["prod-ocf-cf-packages"] {
		t.Fatalf("did not expect versioning on ocf-app-packages")
	}

	if store.lifecycleCalls["prod-ocf-cf-packages"] != 3 {
		t.Fatalf("expected noncurrent days on ocf-app-packages = 3")
	}

	if !store.enabledCalls["prod-ocf-bosh"] || store.lifecycleCalls["prod-ocf-bosh"] != 9 {
		t.Fatalf("expected policies on ocf-bosh")
	}

	if !store.enabledCalls["prod-mgmt-bosh"] || store.lifecycleCalls["prod-mgmt-bosh"] != 9 {
		t.Fatalf("expected policies on mgmt-bosh")
	}
}

func TestCreateBucketsPoliciesNotAppliedWhenDisabled(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, ".ocfp-state")

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}

	_, err = stateManager.Load("prod")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	fakeStore := newFakeStorage()
	provider := &fakeProvider{s: fakeStore}
	cfg := config.NewTestConfig().WithName("prod").WithRegion("eu01").WithBootstrapNetwork().WithBootstrapBastion().Build()
	// Do not enable cfg.Blobstore.EnablePolicies
	manager := bootstrap.NewManager(cfg, provider, stateManager, &bootstrap.Options{
		BlocName: "prod",
		Provider: "stackit",
		Region:   "eu01",
		Force:    false,
		DryRun:   false,
		Output:   "",
		Timeout:  0,
	})

	ctx := context.Background()

	err = manager.CreateBuckets(ctx)
	if err != nil {
		t.Fatalf("createBuckets: %v", err)
	}

	// Expect no policy calls
	if len(fakeStore.enabledCalls) != 0 || len(fakeStore.lifecycleCalls) != 0 {
		t.Fatalf("no policies should be applied when disabled: enabled=%v lifecycle=%v", fakeStore.enabledCalls, fakeStore.lifecycleCalls)
	}
}
