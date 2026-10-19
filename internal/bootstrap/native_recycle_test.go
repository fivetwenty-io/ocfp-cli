package bootstrap

import (
	"context"
	"errors"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/recycle"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nativeTestCluster(t *testing.T) *NativeBastionRecycle {
	t.Helper()
	sm, err := state.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sm.Load("prod"); err != nil {
		t.Fatal(err)
	}
	return &NativeBastionRecycle{mgr: &Manager{stateManager: sm, options: &Options{BlocName: "prod", Provider: "aws"}}}
}
func TestNativeJournalCorruptionFailsClosed(t *testing.T) {
	c := nativeTestCluster(t)
	j, err := c.Load(t.Context())
	if err != nil || j != nil {
		t.Fatal(j, err)
	}
	if err = c.mgr.stateManager.AddResource(&state.Resource{ID: "journal", Type: nativeRecycleJournalType, Name: "prod-bastion", Properties: map[string]interface{}{"journal": "broken"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Load(t.Context()); err == nil {
		t.Fatal("corrupt journal ignored")
	}
}
func TestNativeJournalRoundTripAndRecoveryRecord(t *testing.T) {
	c := nativeTestCluster(t)
	j := &recycle.NativeJournal{Version: 1, Token: "token", Phase: "complete", Identity: cpi.BootIdentity{InstanceID: "vm", RootVolumeID: "old", RootFingerprint: "old", ReplacementFingerprint: "new", DataVolumeIDs: []string{"data"}, PrivateIPs: []string{"10.0.0.1"}, ReplacementUserData: "secret"}, SnapshotID: "snap", ReplacementID: "new"}
	if err := c.Save(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	got, err := c.Load(t.Context())
	if err != nil || got.Identity.ReplacementUserData != "secret" {
		t.Fatal("journal lost payload")
	}
	c.compute = &nativeAdapterCompute{}
	c.boot = &nativeFlowCompute{root: "new", power: cpi.ResourceStateActive}
	if err = c.mgr.stateManager.AddResource(&state.Resource{Type: state.ResourceTypeInstance, Name: "prod-bastion", ID: "vm"}); err != nil {
		t.Fatal(err)
	}
	if err = c.Complete(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	got, err = c.Load(t.Context())
	if err != nil || got != nil {
		t.Fatal("active journal remains")
	}
	history, err := c.mgr.stateManager.GetResource("boot_refresh_recovery", "token")
	if err != nil || history.Properties["snapshot_id"] != "snap" {
		t.Fatal("snapshot not retained")
	}
	if _, exists := history.Properties["journal"]; exists {
		t.Fatal("secret journal retained in history")
	}
}

type nativeAdapterCompute struct{ cpi.ComputeManager }

func (*nativeAdapterCompute) GetInstance(context.Context, string) (*cpi.Instance, error) {
	return &cpi.Instance{ID: "vm", PrivateIP: "10.0.0.1", State: cpi.ResourceStateActive}, nil
}

type nativeFlowCompute struct {
	cpi.ComputeManager
	root     string
	power    cpi.ResourceState
	deleted  []string
	prepared int
	tags     map[string]string
}

func (f *nativeFlowCompute) GetInstance(context.Context, string) (*cpi.Instance, error) {
	return &cpi.Instance{ID: "vm", PrivateIP: "10.0.0.1", PublicIP: "new-public", State: f.power}, nil
}
func (f *nativeFlowCompute) InspectBoot(context.Context, string) (*cpi.BootIdentity, error) {
	return &cpi.BootIdentity{InstanceID: "vm", RootVolumeID: f.root, RootFingerprint: f.root, DataVolumeIDs: []string{"data"}, PrivateIPs: []string{"10.0.0.1"}}, nil
}
func (f *nativeFlowCompute) PrepareBoot(_ context.Context, r *cpi.BootRefreshRequest) (*cpi.BootVolume, error) {
	f.prepared++
	f.tags = r.Tags
	return &cpi.BootVolume{ID: "new", Fingerprint: "new"}, nil
}
func (f *nativeFlowCompute) SnapshotBoot(context.Context, *cpi.BootIdentity, string) (string, error) {
	return "snap", nil
}
func (f *nativeFlowCompute) SwitchBoot(_ context.Context, _ *cpi.BootIdentity, from, to string) error {
	if f.power != cpi.ResourceStateStopped || (f.root != from && f.root != to) {
		return errors.New("bad switch")
	}
	f.root = to
	return nil
}
func (f *nativeFlowCompute) DeleteBoot(_ context.Context, id, fingerprint string) error {
	if id == f.root {
		return errors.New("deleting attached root")
	}
	f.deleted = append(f.deleted, id)
	return nil
}
func (f *nativeFlowCompute) StartInstance(context.Context, string) error {
	f.power = cpi.ResourceStateActive
	return nil
}
func (f *nativeFlowCompute) StopInstance(context.Context, string) error {
	f.power = cpi.ResourceStateStopped
	return nil
}
func nativeFlow(t *testing.T) (*NativeBastionRecycle, *nativeFlowCompute) {
	t.Helper()
	c := nativeTestCluster(t)
	f := &nativeFlowCompute{root: "old", power: cpi.ResourceStateActive}
	c.compute = f
	c.boot = f
	c.guestCheck = func(context.Context, *cpi.Instance, *recycle.NativeJournal) error { return nil }
	if err := c.mgr.stateManager.AddResource(&state.Resource{ID: "vm", Type: state.ResourceTypeInstance, Name: "prod-bastion"}); err != nil {
		t.Fatal(err)
	}
	j := &recycle.NativeJournal{Version: 1, Token: "token", Phase: "prepare", Image: "image", Tags: map[string]string{"immutable": "original"}, Identity: cpi.BootIdentity{InstanceID: "vm", RootVolumeID: "old", RootFingerprint: "old", DataVolumeIDs: []string{"data"}, PrivateIPs: []string{"10.0.0.1"}}}
	if err := c.Save(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	return c, f
}
func TestNativeAdapterPauseAbortRestoresRoot(t *testing.T) {
	c, f := nativeFlow(t)
	if err := c.Run(t.Context(), false, true); err != nil {
		t.Fatal(err)
	}
	if f.root != "new" || len(f.deleted) > 0 || f.tags["immutable"] != "original" {
		t.Fatal("pause state unsafe")
	}
	if err := c.Run(t.Context(), true, false); err != nil {
		t.Fatal(err)
	}
	if f.root != "old" || f.power != cpi.ResourceStateActive || len(f.deleted) != 1 || f.deleted[0] != "new" {
		t.Fatal("abort failed", f)
	}
}
func TestNativeAdapterGuestFailureRetainsOldRoot(t *testing.T) {
	c, f := nativeFlow(t)
	c.guestCheck = func(context.Context, *cpi.Instance, *recycle.NativeJournal) error {
		return errors.New("data unmounted")
	}
	if c.Run(t.Context(), false, false) == nil {
		t.Fatal("guest failure ignored")
	}
	if len(f.deleted) > 0 {
		t.Fatal("retired unverified root")
	}
	j, err := c.Load(t.Context())
	if err != nil || j.Phase != "verify" {
		t.Fatal("lost resume journal", err)
	}
}
func TestNativeAdapterLockRefusesMutation(t *testing.T) {
	c, f := nativeFlow(t)
	if err := c.mgr.stateManager.Lock("prod"); err != nil {
		t.Fatal(err)
	}
	defer c.mgr.stateManager.Unlock("prod")
	if c.Run(t.Context(), false, false) == nil || f.prepared > 0 {
		t.Fatal("concurrent mutation")
	}
}
func TestNativeAdapterAbortRecoversUnrecordedPrepare(t *testing.T) {
	c, f := nativeFlow(t)
	if err := c.Run(t.Context(), true, false); err != nil {
		t.Fatal(err)
	}
	if f.prepared != 0 || len(f.deleted) != 1 || f.deleted[0] != "new" || f.root != "old" {
		t.Fatal("lost created handle")
	}
}
func TestNativeVerificationProviderAndMountChecks(t *testing.T) {
	aws := nativeGuestVerificationForProvider("aws", "/data", "/home/ubuntu")
	gcp := nativeGuestVerificationForProvider("gcp", "/data", "/home/ubuntu")
	if !strings.Contains(aws, "cloud-init status") || strings.Contains(gcp, "cloud-init status") {
		t.Fatal("wrong startup engine")
	}
	for _, s := range []string{"mountpoint -q", "MAJ:MIN", "bootstrap-done", "ocfp-disk-resolve", "ocfp-data"} {
		if !strings.Contains(gcp, s) {
			t.Fatal("missing verification", s)
		}
	}
}

func TestNativePrivateKeyUsesConfiguredLocation(t *testing.T) {
	c := nativeTestCluster(t)
	dir := t.TempDir()
	c.mgr.config = &config.Config{Bastion: config.Bastion{SSHKeyDir: dir, SSHKeyName: "custom"}}
	want := filepath.Join(dir, "custom")
	if err := os.WriteFile(want, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := c.privateKey()
	if err != nil || got != want {
		t.Fatal(got, err)
	}
}

func (f *nativeFlowCompute) RecoverBoot(context.Context, *cpi.BootRefreshRequest) (*cpi.BootVolume, error) {
	return &cpi.BootVolume{ID: "new", Fingerprint: "new"}, nil
}

func TestNativePrepareRejectsReusedRootName(t *testing.T) {
	c, _ := nativeFlow(t)
	j, err := c.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	j.ReplacementID = "new"
	j.Identity.ReplacementFingerprint = "original-generation"
	if c.Prepare(t.Context(), j) == nil {
		t.Fatal("changed root generation adopted")
	}
	if j.Identity.ReplacementFingerprint != "original-generation" {
		t.Fatal("overwrote original fingerprint")
	}
}
func TestNativeCompleteRefusesRootDrift(t *testing.T) {
	c, f := nativeFlow(t)
	if err := c.Run(t.Context(), false, true); err != nil {
		t.Fatal(err)
	}
	j, err := c.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	j.Phase = "complete"
	f.root = "foreign"
	if c.Complete(t.Context(), j) == nil {
		t.Fatal("completed after identity drift")
	}
	if current, err := c.Load(t.Context()); err != nil || current == nil {
		t.Fatal("discarded recovery journal")
	}
}
