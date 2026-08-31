package pve

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/qemu"
)

// fakeQemuService is a minimal qemu.Service stub for seedBastionTemplate
// tests. Start and Stop return an empty UPID so waitForTask is never
// invoked, keeping the fake independent of the tasks service; Status
// answers from a small, test-configured sequence so waitForVMStopped
// resolves deterministically without any real wall-clock waiting.
type fakeQemuService struct {
	startErr error
	stopErr  error

	// statuses is consumed one entry per Status call; once exhausted, the
	// last entry repeats. A nil/empty slice always answers "stopped".
	statuses  []string
	statusIdx int

	startCalls  int
	stopCalls   int
	statusCalls int
}

func (f *fakeQemuService) Start(_ context.Context, _ string, _ int) (string, error) {
	f.startCalls++

	return "", f.startErr
}

func (f *fakeQemuService) Stop(_ context.Context, _ string, _ int) (string, error) {
	f.stopCalls++

	return "", f.stopErr
}

func (f *fakeQemuService) Status(_ context.Context, _ string, _ int) (map[string]interface{}, error) {
	f.statusCalls++

	if len(f.statuses) == 0 {
		return map[string]interface{}{"status": "stopped"}, nil
	}

	idx := f.statusIdx
	if idx >= len(f.statuses) {
		idx = len(f.statuses) - 1
	} else {
		f.statusIdx++
	}

	return map[string]interface{}{"status": f.statuses[idx]}, nil
}

// Unused qemu.Service methods — zero-value stubs.

func (f *fakeQemuService) Create(_ context.Context, _ string, _ map[string]interface{}) (string, error) {
	return "", nil
}

func (f *fakeQemuService) Config(_ context.Context, _ string, _ int) (map[string]interface{}, error) {
	return nil, nil //nolint:nilnil // test stub
}

func (f *fakeQemuService) Reset(_ context.Context, _ string, _ int) (string, error) {
	return "", nil
}

func (f *fakeQemuService) Clone(_ context.Context, _ string, _ int, _ map[string]interface{}) (string, error) {
	return "", nil
}

func (f *fakeQemuService) Template(_ context.Context, _ string, _ int) (string, error) {
	return "", nil
}

func (f *fakeQemuService) AttachDisk(_ context.Context, _ string, _ int, _ string, _ string, _ *qemu.AttachOpts) (string, error) {
	return "", nil
}

func (f *fakeQemuService) DetachDisk(_ context.Context, _ string, _ int, _ string) error {
	return nil
}

func (f *fakeQemuService) ResizeDisk(_ context.Context, _ string, _ int, _ string, _ int) (string, error) {
	return "", nil
}

func (f *fakeQemuService) Snapshot(_ context.Context, _ string, _ int, _ string, _ map[string]interface{}) (string, error) {
	return "", nil
}

func (f *fakeQemuService) DeleteSnapshot(_ context.Context, _ string, _ int, _ string) error {
	return nil
}

func (f *fakeQemuService) ListSnapshots(_ context.Context, _ string, _ int) ([]map[string]interface{}, error) {
	return nil, nil
}

func (f *fakeQemuService) RollbackSnapshot(_ context.Context, _ string, _ int, _ string) (string, error) {
	return "", nil
}

var _ qemu.Service = (*fakeQemuService)(nil)

// newSeedTestComputeManager builds a ComputeManager wired to a fake PVE
// client and a fake qemu service so seedBastionTemplate can run without any
// network I/O. cfg defaults to a DHCP-mode bloc with a non-empty
// TemplateBridge when nil.
func newSeedTestComputeManager(fake *fakePVEClient, fakeQemu *fakeQemuService, cfg *Config) *ComputeManager {
	if cfg == nil {
		cfg = &Config{TemplateBridge: "vmbr1"}
	}

	client := &Client{
		config:      cfg,
		pveClient:   fake,
		qemuService: fakeQemu,
	}

	return &ComputeManager{client: client}
}

// canceledContext returns a context whose Done channel is already closed,
// used to make waitForVMStopped's select fall through to ctx.Done()
// immediately instead of sleeping out its 3-second poll interval, so tests
// that need the "VM did not stop in time" branch run in microseconds
// rather than real wall-clock seconds. Every fake in this file ignores its
// ctx argument otherwise, so this has no other effect on the code under
// test.
func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	return ctx
}

// staticSeedConfig returns a Config with a full static template-seed
// network identity set, shaped after the CHC lab bloc.
func staticSeedConfig() *Config {
	return &Config{
		TemplateBridge:           "vmbr1",
		TemplateSeedIP:           "10.61.148.2/24",
		TemplateSeedGateway:      "10.61.148.1",
		TemplateSeedDNS:          []string{"10.97.160.160", "10.97.160.161"},
		TemplateSeedSearchDomain: "ldschurch.org",
	}
}

// TestSeedBastionTemplate_MergedPUT_DHCPMode pins the actual backward-
// compatibility contract end to end: the merged PUT body seedBastionTemplate
// sends is byte-exact with today's four-key DHCP map, and no cleanup PUT is
// issued at all in DHCP mode (M4 in the static-seed adversarial review —
// TestBuildTemplateSeedNetParams alone only pinned the helper in isolation,
// not the merged map seedBastionTemplate actually sends).
func TestSeedBastionTemplate_MergedPUT_DHCPMode(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{}
	fakeQemu := &fakeQemuService{statuses: []string{"stopped"}}
	cm := newSeedTestComputeManager(fake, fakeQemu, nil)
	cm.seedTemplateVMFunc = func(context.Context, string, int, string) error { return nil }

	err := cm.seedBastionTemplate(context.Background(), "pve1", 9000)
	if err != nil {
		t.Fatalf("seedBastionTemplate() error = %v, want nil", err)
	}

	if len(fake.putParams) != 1 {
		t.Fatalf("PUT calls = %d, want exactly 1 (the seed PUT; no cleanup PUT in DHCP mode): %+v", len(fake.putParams), fake.putParams)
	}

	got := fake.putParams[0].Params
	if len(got) != 4 {
		t.Fatalf("merged seed PUT has %d keys, want exactly 4 (ciuser, cipassword, net0, ipconfig0): %v", len(got), got)
	}

	wantExact := map[string]interface{}{
		"ciuser":    templateSeedCIUser,
		"net0":      "virtio,bridge=vmbr1",
		"ipconfig0": "ip=dhcp",
	}

	for k, want := range wantExact {
		if got[k] != want {
			t.Errorf("merged seed PUT[%q] = %v, want %v", k, got[k], want)
		}
	}

	pw, ok := got["cipassword"].(string)
	if !ok || !strings.HasPrefix(pw, templateSeedPasswordPrefix) {
		t.Errorf("merged seed PUT[%q] = %v, want a string with prefix %q", "cipassword", got["cipassword"], templateSeedPasswordPrefix)
	}
}

// TestSeedBastionTemplate_CleanupPUT_StaticMode asserts the cleanup PUT is
// present, ordered after the seed PUT, and derives its delete list from
// what the seed actually wrote (M3) in static mode.
func TestSeedBastionTemplate_CleanupPUT_StaticMode(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{}
	fakeQemu := &fakeQemuService{statuses: []string{"stopped"}}
	cfg := staticSeedConfig()
	cm := newSeedTestComputeManager(fake, fakeQemu, cfg)
	cm.seedTemplateVMFunc = func(context.Context, string, int, string) error { return nil }

	err := cm.seedBastionTemplate(context.Background(), "pve1", 9000)
	if err != nil {
		t.Fatalf("seedBastionTemplate() error = %v, want nil", err)
	}

	if len(fake.putParams) != 2 {
		t.Fatalf("PUT calls = %d, want exactly 2 (seed PUT then cleanup PUT): %+v", len(fake.putParams), fake.putParams)
	}

	wantConfigPath := "/nodes/pve1/qemu/9000/config"
	if fake.putParams[0].Path != wantConfigPath || fake.putParams[1].Path != wantConfigPath {
		t.Errorf("PUT paths = %q, %q, want both %q", fake.putParams[0].Path, fake.putParams[1].Path, wantConfigPath)
	}

	cleanup := fake.putParams[1].Params
	if cleanup["ipconfig0"] != "ip=dhcp" {
		t.Errorf("cleanup PUT ipconfig0 = %v, want %q", cleanup["ipconfig0"], "ip=dhcp")
	}

	if cleanup["delete"] != "nameserver,searchdomain" {
		t.Errorf("cleanup PUT delete = %v, want %q (derived from what the seed set)", cleanup["delete"], "nameserver,searchdomain")
	}
}

// TestSeedBastionTemplate_CleanupPUT_FiresOnBothStopBranches covers C1 and
// M1 together: the cleanup PUT must run whether the guest shuts down
// cleanly or the fallback force-stop path is needed, and the force-stop
// path must not proceed to cleanup until the VM is actually re-verified
// stopped.
func TestSeedBastionTemplate_CleanupPUT_FiresOnBothStopBranches(t *testing.T) {
	t.Parallel()

	t.Run("graceful stop", func(t *testing.T) {
		t.Parallel()

		fake := &fakePVEClient{}
		fakeQemu := &fakeQemuService{statuses: []string{"stopped"}}
		cm := newSeedTestComputeManager(fake, fakeQemu, staticSeedConfig())
		cm.seedTemplateVMFunc = func(context.Context, string, int, string) error { return nil }

		err := cm.seedBastionTemplate(context.Background(), "pve1", 9000)
		if err != nil {
			t.Fatalf("seedBastionTemplate() error = %v, want nil", err)
		}

		if fakeQemu.stopCalls != 0 {
			t.Errorf("stopCalls = %d, want 0 (graceful shutdown never needs a force stop)", fakeQemu.stopCalls)
		}

		if len(fake.putParams) != 2 {
			t.Fatalf("PUT calls = %d, want 2 (seed + cleanup)", len(fake.putParams))
		}
	})

	t.Run("force stop then verified", func(t *testing.T) {
		t.Parallel()

		fake := &fakePVEClient{}
		// First Status call (from the initial waitForVMStopped) reports
		// "running", forcing the fallback branch. Second call (from
		// stopVMAndVerify's re-check) reports "stopped".
		fakeQemu := &fakeQemuService{statuses: []string{"running", "stopped"}}
		cm := newSeedTestComputeManager(fake, fakeQemu, staticSeedConfig())
		cm.seedTemplateVMFunc = func(context.Context, string, int, string) error { return nil }

		// A pre-canceled context makes the first waitForVMStopped call
		// fail immediately via ctx.Done() instead of polling for the real
		// 120-second deadline; see canceledContext's doc comment.
		err := cm.seedBastionTemplate(canceledContext(), "pve1", 9000)
		if err != nil {
			t.Fatalf("seedBastionTemplate() error = %v, want nil", err)
		}

		if fakeQemu.stopCalls != 1 {
			t.Errorf("stopCalls = %d, want 1 (the fallback force stop)", fakeQemu.stopCalls)
		}

		if len(fake.putParams) != 2 {
			t.Fatalf("PUT calls = %d, want 2 (seed + cleanup) even on the force-stop branch", len(fake.putParams))
		}
	})
}

// TestSeedBastionTemplate_ForceStop_StillRunning_Errors covers M1 directly:
// if the VM is still running after the force-stop task completes, the
// cleanup PUT must never be attempted, and seedBastionTemplate must return
// an error rather than silently proceeding, since PVE would park the
// cleanup's config change in [PENDING] against a running VM and the live
// seed address would survive into the template.
func TestSeedBastionTemplate_ForceStop_StillRunning_Errors(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{}
	// Every Status call reports "running": neither the initial wait nor
	// the post-force-stop re-check ever sees "stopped".
	fakeQemu := &fakeQemuService{statuses: []string{"running"}}
	cm := newSeedTestComputeManager(fake, fakeQemu, staticSeedConfig())
	cm.seedTemplateVMFunc = func(context.Context, string, int, string) error { return nil }

	err := cm.seedBastionTemplate(canceledContext(), "pve1", 9000)
	if err == nil {
		t.Fatal("seedBastionTemplate() error = nil, want an error when the VM never reaches stopped")
	}

	if !strings.Contains(err.Error(), "seed force stop") {
		t.Errorf("error = %v, want it to mention %q", err, "seed force stop")
	}

	for _, call := range fake.putParams {
		if call.Params["delete"] != nil {
			t.Errorf("cleanup PUT must never be issued against a still-running VM, got PUT: %+v", call)
		}
	}
}

// errFakeCleanupFailed is a sentinel used by
// TestSeedBastionTemplate_CleanupPUT_FailurePropagates.
var errFakeCleanupFailed = errors.New("fake: cleanup PUT rejected")

// TestSeedBastionTemplate_CleanupPUT_FailurePropagates asserts that a
// cleanup PUT failure surfaces as an error from seedBastionTemplate, and
// that when the subsequent deferred recovery also fails to clean up, its
// failure is appended to, not substituted for, the original error.
func TestSeedBastionTemplate_CleanupPUT_FailurePropagates(t *testing.T) {
	t.Parallel()

	putCount := 0
	fake := &fakePVEClient{
		putErr: func(_ string, _ map[string]interface{}) error {
			putCount++
			// The first PUT (the seed PUT) must succeed so the deferred
			// recovery is armed; every PUT after that (the cleanup PUT,
			// and the recovery handler's own retry) fails.
			if putCount >= 2 {
				return errFakeCleanupFailed
			}

			return nil
		},
	}
	fakeQemu := &fakeQemuService{statuses: []string{"stopped"}}
	cm := newSeedTestComputeManager(fake, fakeQemu, staticSeedConfig())
	cm.seedTemplateVMFunc = func(context.Context, string, int, string) error { return nil }

	err := cm.seedBastionTemplate(context.Background(), "pve1", 9000)
	if err == nil {
		t.Fatal("seedBastionTemplate() error = nil, want the cleanup PUT failure to propagate")
	}

	if !errors.Is(err, errFakeCleanupFailed) {
		t.Errorf("error = %v, want it to wrap errFakeCleanupFailed", err)
	}

	if !strings.Contains(err.Error(), "seed cleanup PUT") {
		t.Errorf("error = %v, want it to mention %q (the original failure)", err, "seed cleanup PUT")
	}

	if !strings.Contains(err.Error(), "seed recovery also failed") {
		t.Errorf("error = %v, want it to also mention the recovery handler's own failure, not mask it", err)
	}
}

// errFakeSeedTemplateVMFailed is a sentinel used by
// TestSeedBastionTemplate_RecoveryOnSeedTemplateVMFailure.
var errFakeSeedTemplateVMFailed = errors.New("fake: termproxy seed failed")

// TestSeedBastionTemplate_RecoveryOnSeedTemplateVMFailure is C1's direct
// regression test: a mid-flow failure in the termproxy seed step (the
// long, flaky step the adversarial review singled out) must not strand a
// running VM holding the reserved static seed address. The deferred
// recovery must force-stop the VM and reset the address-bearing config,
// and the original termproxy error must still be the one returned.
func TestSeedBastionTemplate_RecoveryOnSeedTemplateVMFailure(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{}
	// The VM is still running when recovery checks status; recovery must
	// force-stop it before resetting the config.
	fakeQemu := &fakeQemuService{statuses: []string{"running", "stopped"}}
	cm := newSeedTestComputeManager(fake, fakeQemu, staticSeedConfig())
	cm.seedTemplateVMFunc = func(context.Context, string, int, string) error {
		return errFakeSeedTemplateVMFailed
	}

	err := cm.seedBastionTemplate(context.Background(), "pve1", 9000)
	if err == nil {
		t.Fatal("seedBastionTemplate() error = nil, want the termproxy failure to propagate")
	}

	if !errors.Is(err, errFakeSeedTemplateVMFailed) {
		t.Errorf("error = %v, want it to wrap errFakeSeedTemplateVMFailed", err)
	}

	if !strings.Contains(err.Error(), "seed termproxy") {
		t.Errorf("error = %v, want it to mention %q", err, "seed termproxy")
	}

	if strings.Contains(err.Error(), "recovery also failed") {
		t.Errorf("error = %v, recovery was expected to succeed and must not be reported as failed", err)
	}

	if fakeQemu.stopCalls != 1 {
		t.Errorf("stopCalls = %d, want 1: recovery must force-stop the VM left running by the termproxy failure", fakeQemu.stopCalls)
	}

	if len(fake.putParams) != 2 {
		t.Fatalf("PUT calls = %d, want 2 (the seed PUT, then recovery's config reset)", len(fake.putParams))
	}

	recoveryPUT := fake.putParams[1].Params
	if recoveryPUT["ipconfig0"] != "ip=dhcp" {
		t.Errorf("recovery PUT ipconfig0 = %v, want %q: a failed run must not strand the reserved address in the VM config", recoveryPUT["ipconfig0"], "ip=dhcp")
	}
}

// TestSeedBastionTemplate_RecoveryNotArmed_BeforeSeedPUT asserts that a
// failure before the seed PUT succeeds (password generation) never invokes
// recovery: there is nothing to recover from, since the VM config was
// never touched.
func TestSeedBastionTemplate_RecoveryNotArmed_BeforeSeedPUT(t *testing.T) {
	t.Parallel()

	errFakeSeedPUTFailed := errors.New("fake: seed config PUT rejected")

	fake := &fakePVEClient{
		putErr: func(_ string, _ map[string]interface{}) error {
			return errFakeSeedPUTFailed
		},
	}
	fakeQemu := &fakeQemuService{statuses: []string{"stopped"}}
	cm := newSeedTestComputeManager(fake, fakeQemu, staticSeedConfig())
	cm.seedTemplateVMFunc = func(context.Context, string, int, string) error {
		t.Fatal("seedTemplateVMFunc must not run when the seed PUT itself failed")

		return nil
	}

	err := cm.seedBastionTemplate(context.Background(), "pve1", 9000)
	if err == nil {
		t.Fatal("seedBastionTemplate() error = nil, want the seed PUT failure to propagate")
	}

	if !errors.Is(err, errFakeSeedPUTFailed) {
		t.Errorf("error = %v, want it to wrap errFakeSeedPUTFailed", err)
	}

	if fakeQemu.startCalls != 0 || fakeQemu.stopCalls != 0 {
		t.Errorf("startCalls = %d, stopCalls = %d, want both 0: nothing should run after the seed PUT fails", fakeQemu.startCalls, fakeQemu.stopCalls)
	}
}

// TestStopVMAndVerify_StillRunningAfterStop_Errors is a focused unit test
// of the M1 fix in isolation: stopVMAndVerify must return an error, not
// nil, when the VM is still running after the stop task completes.
func TestStopVMAndVerify_StillRunningAfterStop_Errors(t *testing.T) {
	t.Parallel()

	fakeQemu := &fakeQemuService{statuses: []string{"running"}}
	client := &Client{qemuService: fakeQemu}

	err := stopVMAndVerify(canceledContext(), client, fakeQemu, "pve1", 9000, time.Millisecond)
	if err == nil {
		t.Fatal("stopVMAndVerify() error = nil, want an error when the VM never reaches stopped")
	}
}

// TestStopVMAndVerify_StopsSuccessfully covers the ordinary case: Stop
// succeeds and the VM is confirmed stopped on the very next status check.
func TestStopVMAndVerify_StopsSuccessfully(t *testing.T) {
	t.Parallel()

	fakeQemu := &fakeQemuService{statuses: []string{"stopped"}}
	client := &Client{qemuService: fakeQemu}

	err := stopVMAndVerify(context.Background(), client, fakeQemu, "pve1", 9000, time.Second)
	if err != nil {
		t.Fatalf("stopVMAndVerify() error = %v, want nil", err)
	}

	if fakeQemu.stopCalls != 1 {
		t.Errorf("stopCalls = %d, want 1", fakeQemu.stopCalls)
	}
}

// TestTemplateSeedCleanupParams covers M3 directly: the delete list must
// name only the keys setParams actually contains among
// templateSeedCleanupKeys, never a hardcoded pair regardless of input.
func TestTemplateSeedCleanupParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setParams  map[string]interface{}
		wantDelete interface{} // nil means the "delete" key must be absent
	}{
		{
			name:       "dhcp shape sets neither nameserver nor searchdomain",
			setParams:  map[string]interface{}{"ipconfig0": "ip=dhcp"},
			wantDelete: nil,
		},
		{
			name:       "nameserver only",
			setParams:  map[string]interface{}{"ipconfig0": "ip=10.0.0.2/24,gw=10.0.0.1", "nameserver": "1.1.1.1"},
			wantDelete: "nameserver",
		},
		{
			name:       "searchdomain only",
			setParams:  map[string]interface{}{"ipconfig0": "ip=10.0.0.2/24,gw=10.0.0.1", "searchdomain": "example.org"},
			wantDelete: "searchdomain",
		},
		{
			name:       "both nameserver and searchdomain",
			setParams:  map[string]interface{}{"ipconfig0": "ip=10.0.0.2/24,gw=10.0.0.1", "nameserver": "1.1.1.1", "searchdomain": "example.org"},
			wantDelete: "nameserver,searchdomain",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := templateSeedCleanupParams(tc.setParams)

			if got["ipconfig0"] != "ip=dhcp" {
				t.Errorf("ipconfig0 = %v, want %q", got["ipconfig0"], "ip=dhcp")
			}

			gotDelete, present := got["delete"]
			if tc.wantDelete == nil {
				if present {
					t.Errorf("delete = %v, want the key absent entirely (nothing to delete)", gotDelete)
				}

				return
			}

			if gotDelete != tc.wantDelete {
				t.Errorf("delete = %v, want %v", gotDelete, tc.wantDelete)
			}
		})
	}
}

// TestVMIsStopped covers the small status-parsing helper waitForVMStopped
// and recoverFailedSeed both rely on.
func TestVMIsStopped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status map[string]interface{}
		want   bool
	}{
		{name: "nil status", status: nil, want: false},
		{name: "missing status field", status: map[string]interface{}{}, want: false},
		{name: "non-string status field", status: map[string]interface{}{"status": 1}, want: false},
		{name: "running", status: map[string]interface{}{"status": "running"}, want: false},
		{name: "stopped", status: map[string]interface{}{"status": "stopped"}, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := vmIsStopped(tc.status); got != tc.want {
				t.Errorf("vmIsStopped(%v) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}
