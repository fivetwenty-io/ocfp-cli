package bootstrap_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// cancelOnAttachStorage cancels the run's context the moment the data volume
// attaches. That stands in for the operator interrupting bootstrap during the
// long delivery and readiness waits that follow the attach.
type cancelOnAttachStorage struct {
	dataVolStorage

	cancel context.CancelFunc
}

func (s *cancelOnAttachStorage) AttachVolume(ctx context.Context, volumeID, instanceID, device string) error {
	err := s.dataVolStorage.AttachVolume(ctx, volumeID, instanceID, device)
	s.cancel()

	return err
}

// newArtifactsOrderManager builds a Manager that can run CreateArtifacts end
// to end against fakes: the network, subnet, and security group the step reads
// are pre-seeded in state, and the provider is not pve so no SSH delivery is
// attempted. It returns the state directory so a test can reopen the state
// file the way a later `ocfp teardown` would.
func newArtifactsOrderManager(
	t *testing.T,
	st *cancelOnAttachStorage,
) (*bootstrap.Manager, *fakeComputeEnhanced, string) {
	t.Helper()

	stateDir := t.TempDir()

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = stateManager.Load("prod"); err != nil {
		t.Fatal(err)
	}

	if err = stateManager.SetOutput("network_id", "net-1"); err != nil {
		t.Fatal(err)
	}

	seed := []*state.Resource{
		{
			ID: "subnet-1", Type: "subnet", Name: "prod-ocfp-0", State: "active",
			Properties: map[string]interface{}{"cidr": "10.64.64.0/24"},
		},
		{ID: "sg-artifacts", Type: "security_group", Name: "prod-artifacts", State: "active"},
	}

	for _, res := range seed {
		if err = stateManager.AddResource(res); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		Name:   "prod",
		Region: "eu01",
		AZs:    map[string]config.AvailabilityZone{"eu01-1": {}},
		Artifacts: config.ArtifactsConfig{
			Enabled:  true,
			Flavor:   "c1.4",
			Template: "Ubuntu 24.04 LTS",
			Rustfs:   config.RustfsConfig{AccessKey: "AK", SecretKey: "SK", S3Port: 9000, ConsolePort: 9001},
			Data: config.ArtifactsDataConfig{
				DiskSizeGiB: 100, StoragePool: "local-lvm-data", Mountpoint: "/data",
			},
			TLS: config.ArtifactsTLSConfig{Mode: config.ArtifactsTLSModeDisabled},
		},
	}

	compute := newFakeComputeEnhanced()
	prov := &fakeProvWithStorage{c: compute, s: st}

	manager := bootstrap.NewManager(cfg, prov, stateManager, &bootstrap.Options{
		BlocName: "prod", Provider: "fake", Region: "eu01", Yes: true,
	})

	return manager, compute, stateDir
}

// reopenState loads the bloc state from disk into a fresh manager, which is
// all a later `ocfp teardown` can see once the bootstrap process is gone.
func reopenState(t *testing.T, stateDir string) *state.Manager {
	t.Helper()

	sm, err := state.NewManager(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = sm.Load("prod"); err != nil {
		t.Fatal(err)
	}

	return sm
}

// TestCreateArtifacts_RecordsAndSavesStateBeforeReadinessWait guards the
// window found on a live lab. The artifacts VM was created and its disk
// attached, then the step waited minutes for sshd and RustFS before it
// recorded anything. A process killed in that wait left a running VM that the
// bloc state did not know about, so `ocfp teardown` could not see it.
//
// The context is cancelled as the data volume attaches, so everything after
// that point sees an interrupted run. The state file on disk must already hold
// the VM and its data volume, and the error must say so.
func TestCreateArtifacts_RecordsAndSavesStateBeforeReadinessWait(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := &cancelOnAttachStorage{cancel: cancel}
	manager, _, stateDir := newArtifactsOrderManager(t, st)

	err := manager.CreateArtifacts(ctx)
	if err == nil {
		t.Fatal("CreateArtifacts returned nil for a run interrupted during the readiness wait")
	}

	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}

	for _, want := range []string{
		"artifacts: interrupted while waiting for readiness",
		"VM prod-artifacts is recorded in state",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), want)
		}
	}

	onDisk := reopenState(t, stateDir)

	vm, _ := onDisk.GetResource("artifacts", "prod-artifacts")
	if vm == nil {
		t.Fatal("artifacts VM is not in the state file on disk; teardown cannot find it")
	}

	if vm.ID != "prod-artifacts" || vm.Properties["vm_id"] != "inst-prod-artifacts" {
		t.Errorf("recorded VM = id %q vm_id %v, want the created instance", vm.ID, vm.Properties["vm_id"])
	}

	vol, _ := onDisk.GetResource(state.ResourceTypeVolume, "prod-artifacts-data")
	if vol == nil {
		t.Fatal("artifacts data volume is not in the state file on disk")
	}

	if got := vol.Properties["preserve"]; got != true {
		t.Errorf("data volume preserve = %v, want true", got)
	}

	if ip, _ := onDisk.GetOutput("artifacts_ip"); ip == nil {
		t.Error("artifacts_ip output was not saved")
	}
}

// TestCreateArtifacts_InterruptedRunLeavesVMInPlace asserts the interrupted
// run does not delete the VM it just recorded; the operator tears it down
// deliberately, or re-runs and converges on it.
func TestCreateArtifacts_InterruptedRunLeavesVMInPlace(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := &cancelOnAttachStorage{cancel: cancel}
	manager, compute, _ := newArtifactsOrderManager(t, st)

	if err := manager.CreateArtifacts(ctx); err == nil {
		t.Fatal("CreateArtifacts returned nil for an interrupted run")
	}

	if _, ok := compute.instances["inst-prod-artifacts"]; !ok {
		t.Error("the artifacts VM was deleted; an interrupted run must leave it in place")
	}

	if st.called("DeleteVolume") {
		t.Error("the data volume was deleted; an interrupted run must leave it in place")
	}
}

// TestCreateArtifacts_RequestsProtectionAndStartOnBoot asserts the artifacts VM
// is created guarded against deletion and set to come back when the provider's
// host reboots, rather than relying on a later convergence pass to fix either.
func TestCreateArtifacts_RequestsProtectionAndStartOnBoot(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := &cancelOnAttachStorage{cancel: cancel}
	manager, compute, _ := newArtifactsOrderManager(t, st)

	if err := manager.CreateArtifacts(ctx); err == nil {
		t.Fatal("CreateArtifacts returned nil for an interrupted run")
	}

	if compute.lastReq == nil {
		t.Fatal("the artifacts VM was never requested")
	}

	if !compute.lastReq.Protected {
		t.Error("the artifacts request must ask for a protected VM")
	}

	if !compute.lastReq.StartOnBoot {
		t.Error("the artifacts request must ask for a VM that starts on boot")
	}
}

// TestCreateArtifacts_StateSaveFailureIsFatalAndKeepsVM asserts that when the
// early save cannot reach disk, the step fails with a wrapped error rather
// than carrying on into a long wait with nothing durable recorded. The VM
// stays in place either way.
func TestCreateArtifacts_StateSaveFailureIsFatalAndKeepsVM(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not bind root")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := &cancelOnAttachStorage{}
	manager, compute, stateDir := newArtifactsOrderManager(t, st)

	// Make the state directory unwritable once the data volume has attached,
	// so the save that follows the record is the one that fails. The context
	// is cancelled as well, so a build that skips the early save ends in the
	// interrupted-wait error rather than polling for the full readiness
	// timeout.
	st.cancel = func() {
		if err := os.Chmod(stateDir, 0o500); err != nil {
			t.Errorf("chmod state dir: %v", err)
		}

		cancel()
	}

	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })

	err := manager.CreateArtifacts(ctx)
	if err == nil {
		t.Fatal("CreateArtifacts returned nil although the state could not be saved")
	}

	if !strings.Contains(err.Error(), "artifacts: save state") {
		t.Errorf("error = %q, want it to name the failed state save", err.Error())
	}

	if _, ok := compute.instances["inst-prod-artifacts"]; !ok {
		t.Error("the artifacts VM was deleted after a state save failure")
	}
}
