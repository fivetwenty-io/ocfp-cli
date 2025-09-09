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
	created []string
}

func (f *fakeStorage) CreateBucket(ctx context.Context, req *cpi.BucketRequest) (*cpi.Bucket, error) {
	f.created = append(f.created, req.Name)

	return &cpi.Bucket{
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
func (f *fakeStorage) ListBuckets(ctx context.Context) ([]*cpi.Bucket, error) { //nolint:nilnil // test fake intentionally returns no data and no error
	return nil, nil //nolint:nilnil // test fake
}

// Unused interface methods.
func (f *fakeStorage) CreateVolume(ctx context.Context, req *cpi.VolumeRequest) (*cpi.Volume, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorage) GetVolume(ctx context.Context, id string) (*cpi.Volume, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorage) ListVolumes(ctx context.Context, filters map[string]string) ([]*cpi.Volume, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorage) AttachVolume(ctx context.Context, volumeID, instanceID, device string) error {
	return nil
}
func (f *fakeStorage) DetachVolume(ctx context.Context, volumeID string, instanceID string) error {
	return nil
}
func (f *fakeStorage) ResizeVolume(ctx context.Context, id string, size int) error { return nil }
func (f *fakeStorage) DeleteVolume(ctx context.Context, id string) error           { return nil }
func (f *fakeStorage) CreateSnapshot(ctx context.Context, volumeID string, name string) (*cpi.Snapshot, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorage) GetSnapshot(ctx context.Context, id string) (*cpi.Snapshot, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorage) ListSnapshots(ctx context.Context, volumeID string) ([]*cpi.Snapshot, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorage) DeleteSnapshot(ctx context.Context, id string) error { return nil }
func (f *fakeStorage) GetBucket(ctx context.Context, name string) (*cpi.Bucket, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}
func (f *fakeStorage) DeleteBucket(ctx context.Context, name string) error { return nil }
func (f *fakeStorage) EmptyBucket(ctx context.Context, name string) error  { return nil }
func (f *fakeStorage) CreateCredentialsGroup(ctx context.Context, req *cpi.CredentialsGroupRequest) (*cpi.CredentialsGroup, error) { //nolint:nilnil // test fake
	return nil, nil //nolint:nilnil // test fake
}

// Policy methods for tests.
var (
	enabledCalls   = make(map[string]bool)  //nolint:gochecknoglobals // test helper state
	lifecycleCalls = make(map[string]int32) //nolint:gochecknoglobals // test helper state
)

func (f *fakeStorage) EnableBucketVersioning(ctx context.Context, name string) error {
	enabledCalls[name] = true

	return nil
}
func (f *fakeStorage) SetBucketLifecycleNoncurrentDays(ctx context.Context, name string, days int32) error {
	lifecycleCalls[name] = days

	return nil
}
func (f *fakeStorage) EnsureObjectStorageCredentialsGroup(ctx context.Context, displayName string) (string, error) {
	return "group-123", nil
}

type fakeProvider struct{ s cpi.StorageManager }

func (p *fakeProvider) Name() string                                  { return "fake" }
func (p *fakeProvider) Region() string                                { return "eu01" }
func (p *fakeProvider) Authenticate(ctx context.Context) error        { return nil }
func (p *fakeProvider) ValidateCredentials(ctx context.Context) error { return nil }

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

func (p *fakeProvider) SupportsStorage() bool                                 { return true }
func (p *fakeProvider) Initialize(ctx context.Context, cfg interface{}) error { return nil }
func (p *fakeProvider) Cleanup(ctx context.Context) error                     { return nil }

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

	fakeStore := &fakeStorage{
		created: nil,
	}
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
		"prod-bosh-blobstore":  true,
		"prod-cf-app-packages": true,
		"prod-cf-buildpacks":   true,
		"prod-cf-droplets":     true,
		"prod-cf-resources":    true,
		"prod-artifacts":       true,
		"prod-shield-backups":  true,
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

	resetCallTrackers()

	stateManager := setupTestStateManager(t)
	provider := createFakeProvider()
	cfg := createBlobstoreTestConfig()
	manager := createTestBootstrapManager(cfg, provider, stateManager)

	ctx := context.Background()

	err := manager.CreateBuckets(ctx)
	if err != nil {
		t.Fatalf("createBuckets: %v", err)
	}

	verifyPolicyCallsWhenEnabled(t)
}

func resetCallTrackers() {
	enabledCalls = make(map[string]bool)
	lifecycleCalls = make(map[string]int32)
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

func createFakeProvider() *fakeProvider {
	fakeStore := &fakeStorage{created: nil}

	return &fakeProvider{s: fakeStore}
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

func verifyPolicyCallsWhenEnabled(t *testing.T) {
	t.Helper()

	if !enabledCalls["prod-cf-buildpacks"] || lifecycleCalls["prod-cf-buildpacks"] != 11 {
		t.Fatalf("expected policies on cf-buildpacks: %+v %+v", enabledCalls, lifecycleCalls)
	}

	if !enabledCalls["prod-cf-droplets"] || lifecycleCalls["prod-cf-droplets"] != 5 {
		t.Fatalf("expected policies on cf-droplets")
	}

	if enabledCalls["prod-cf-app-packages"] {
		t.Fatalf("did not expect versioning on cf-app-packages")
	}

	if lifecycleCalls["prod-cf-app-packages"] != 3 {
		t.Fatalf("expected noncurrent days on cf-app-packages = 3")
	}

	if !enabledCalls["prod-bosh-blobstore"] || lifecycleCalls["prod-bosh-blobstore"] != 9 {
		t.Fatalf("expected policies on bosh-blobstore")
	}
}

func TestCreateBucketsPoliciesNotAppliedWhenDisabled(t *testing.T) {
	t.Parallel()

	enabledCalls = make(map[string]bool)
	lifecycleCalls = make(map[string]int32)

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

	fakeStore := &fakeStorage{
		created: nil,
	}
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
	if len(enabledCalls) != 0 || len(lifecycleCalls) != 0 {
		t.Fatalf("no policies should be applied when disabled: enabled=%v lifecycle=%v", enabledCalls, lifecycleCalls)
	}
}
