package bootstrap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// dataVolStorage is a storage fake that records the calls the data-volume step
// makes, in order, so the tests can assert on sequence and on what did NOT
// happen. It embeds fakeStorage for the parts of the interface it does not
// care about.
type dataVolStorage struct {
	fakeStorage

	calls []string

	volumes []*cpi.Volume

	createdReq *cpi.VolumeRequest
	attachedTo string
	attachDev  string
	deletedID  string

	createErr error
	attachErr error
	listErr   error
}

func (d *dataVolStorage) CreateVolume(_ context.Context, req *cpi.VolumeRequest) (*cpi.Volume, error) {
	d.calls = append(d.calls, "CreateVolume")
	d.createdReq = req

	if d.createErr != nil {
		return nil, d.createErr
	}

	vol := &cpi.Volume{
		ID:    "local-lvm-data:vm-100-data",
		Name:  "vm-100-data",
		Size:  req.SizeGB,
		Type:  req.Type,
		State: cpi.ResourceStateAvailable,
	}
	d.volumes = append(d.volumes, vol)

	return vol, nil
}

func (d *dataVolStorage) AttachVolume(_ context.Context, volumeID, instanceID, device string) error {
	d.calls = append(d.calls, "AttachVolume")
	d.attachedTo = instanceID
	d.attachDev = device

	_ = volumeID

	return d.attachErr
}

func (d *dataVolStorage) DeleteVolume(_ context.Context, id string) error {
	d.calls = append(d.calls, "DeleteVolume")
	d.deletedID = id

	return nil
}

func (d *dataVolStorage) ListVolumes(_ context.Context, _ map[string]string) ([]*cpi.Volume, error) {
	d.calls = append(d.calls, "ListVolumes")

	if d.listErr != nil {
		return nil, d.listErr
	}

	return d.volumes, nil
}

func (d *dataVolStorage) called(name string) bool {
	for _, c := range d.calls {
		if c == name {
			return true
		}
	}

	return false
}

// newDataVolManager builds a Manager whose bastion is already recorded in
// state, which is the situation the data-volume step always runs in: it is a
// separate step precisely because CreateBastion returns early when a bastion
// already exists.
func newDataVolManager(t *testing.T, st *dataVolStorage) (*bootstrap.Manager, *state.Manager) {
	t.Helper()

	return newDataVolManagerWithData(t, st, config.BastionDataConfig{
		Enabled:     true,
		DiskSizeGiB: 64,
		StoragePool: "local-lvm-data",
		Filesystem:  config.BastionFilesystemExt4,
		Mountpoint:  "/data",
	})
}

func newDataVolManagerWithData(
	t *testing.T,
	st *dataVolStorage,
	data config.BastionDataConfig,
) (*bootstrap.Manager, *state.Manager) {
	t.Helper()

	stateManager, err := state.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	_, err = stateManager.Load("prod")
	if err != nil {
		t.Fatal(err)
	}

	err = stateManager.AddResource(&state.Resource{
		ID:       "100",
		Type:     state.ResourceTypeInstance,
		Name:     "prod-bastion",
		Provider: "pve",
		State:    "active",
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Name:     "prod",
		Provider: "pve",
		Bastion: config.Bastion{
			Flavor: "bastion",
			Data:   data,
		},
	}

	manager := bootstrap.NewManager(cfg, &fakeProvWithStorage{s: st}, stateManager, &bootstrap.Options{
		BlocName: "prod",
		Provider: "pve",
		Yes:      true,
	})

	return manager, stateManager
}

func TestEnsureBastionDataVolume_CreatesAndAttaches(t *testing.T) {
	t.Parallel()

	st := &dataVolStorage{}
	manager, stateManager := newDataVolManager(t, st)

	err := manager.EnsureBastionDataVolume(context.Background())
	if err != nil {
		t.Fatalf("EnsureBastionDataVolume: %v", err)
	}

	if st.createdReq == nil {
		t.Fatal("CreateVolume was never called")
	}

	if st.createdReq.Name != "prod-bastion-data" {
		t.Errorf("volume name = %q, want prod-bastion-data", st.createdReq.Name)
	}

	if st.createdReq.SizeGB != 64 {
		t.Errorf("volume size = %d, want 64", st.createdReq.SizeGB)
	}

	if st.createdReq.Type != "local-lvm-data" {
		t.Errorf("volume pool = %q, want local-lvm-data", st.createdReq.Type)
	}

	if st.createdReq.InstanceID != "100" {
		t.Errorf("volume owner = %q, want the bastion VMID 100", st.createdReq.InstanceID)
	}

	if st.attachedTo != "100" {
		t.Errorf("attached to %q, want 100", st.attachedTo)
	}

	// The slot must be pinned rather than left to PVE's next-free
	// arithmetic, and the serial is what gives the guest a stable by-id path.
	if st.attachDev != "scsi1,discard=on,serial=ocfpdata" {
		t.Errorf("attach device = %q, want the pinned slot with discard and serial", st.attachDev)
	}

	res, err := stateManager.GetResource(state.ResourceTypeVolume, "prod-bastion-data")
	if err != nil || res == nil {
		t.Fatalf("data volume was not recorded in state: %v", err)
	}

	if res.ID != "local-lvm-data:vm-100-data" {
		t.Errorf("state records volume id %q, want the full volid", res.ID)
	}

	if got := res.Properties["mountpoint"]; got != "/data" {
		t.Errorf("state mountpoint = %v, want /data", got)
	}
}

func TestEnsureBastionDataVolume_ReattachesRecordedVolume(t *testing.T) {
	t.Parallel()

	st := &dataVolStorage{}
	manager, stateManager := newDataVolManager(t, st)

	err := stateManager.AddResource(&state.Resource{
		ID:       "local-lvm-data:vm-100-data",
		Type:     state.ResourceTypeVolume,
		Name:     "prod-bastion-data",
		Provider: "pve",
		State:    "active",
		Properties: map[string]interface{}{
			"mountpoint": "/data",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	err = manager.EnsureBastionDataVolume(context.Background())
	if err != nil {
		t.Fatalf("EnsureBastionDataVolume: %v", err)
	}

	if st.called("CreateVolume") {
		t.Error("CreateVolume was called for a volume already recorded in state")
	}

	if !st.called("AttachVolume") {
		t.Error("AttachVolume was not called; a recorded volume must still be re-attached after a rebuild")
	}
}

func TestEnsureBastionDataVolume_AdoptsOrphan(t *testing.T) {
	t.Parallel()

	st := &dataVolStorage{
		volumes: []*cpi.Volume{
			{ID: "local-lvm-data:vm-100-data", Name: "vm-100-data", Size: 64, Type: "local-lvm-data"},
		},
	}
	manager, stateManager := newDataVolManager(t, st)

	err := manager.EnsureBastionDataVolume(context.Background())
	if err != nil {
		t.Fatalf("EnsureBastionDataVolume: %v", err)
	}

	if st.called("CreateVolume") {
		t.Error("CreateVolume was called even though a matching volume existed on the provider")
	}

	res, err := stateManager.GetResource(state.ResourceTypeVolume, "prod-bastion-data")
	if err != nil || res == nil {
		t.Fatalf("orphan volume was not adopted into state: %v", err)
	}
}

// TestEnsureBastionDataVolume_AttachFailureDeletesVolumeNotInstance is the one
// place this deliberately diverges from the artifacts path. Artifacts deletes
// the VM when the attach fails, which is right for a machine with no purpose
// without its disk. A bastion whose data disk failed to attach is still a
// working bastion holding an operator's deployment trees, so we clean up the
// orphaned volume and leave the guest alone.
func TestEnsureBastionDataVolume_AttachFailureDeletesVolumeNotInstance(t *testing.T) {
	t.Parallel()

	st := &dataVolStorage{attachErr: errors.New("attach refused")}
	manager, stateManager := newDataVolManager(t, st)

	err := manager.EnsureBastionDataVolume(context.Background())
	if err == nil {
		t.Fatal("expected an error when the attach fails")
	}

	if !st.called("DeleteVolume") {
		t.Error("the orphaned volume was not deleted after the attach failed")
	}

	if st.deletedID != "local-lvm-data:vm-100-data" {
		t.Errorf("deleted %q, want the volume that was just created", st.deletedID)
	}

	inst, _ := stateManager.GetResource(state.ResourceTypeInstance, "prod-bastion")
	if inst == nil {
		t.Error("the bastion was removed from state; an attach failure must leave the guest alone")
	}
}

func TestEnsureBastionDataVolume_DisabledIsANoOp(t *testing.T) {
	t.Parallel()

	st := &dataVolStorage{}
	manager, _ := newDataVolManagerWithData(t, st, config.BastionDataConfig{Enabled: false})

	err := manager.EnsureBastionDataVolume(context.Background())
	if err != nil {
		t.Fatalf("EnsureBastionDataVolume: %v", err)
	}

	if len(st.calls) != 0 {
		t.Errorf("a disabled data disk made provider calls: %v", st.calls)
	}
}

// TestEnsureBastionDataVolume_NoBastionIsANoOp covers bootstrap runs that skip
// the bastion entirely. The step must not invent a volume with no owner.
func TestEnsureBastionDataVolume_NoBastionIsANoOp(t *testing.T) {
	t.Parallel()

	st := &dataVolStorage{}

	stateManager, err := state.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	_, err = stateManager.Load("prod")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Name:     "prod",
		Provider: "pve",
		Bastion: config.Bastion{
			Data: config.BastionDataConfig{Enabled: true, DiskSizeGiB: 64, Mountpoint: "/data"},
		},
	}

	manager := bootstrap.NewManager(cfg, &fakeProvWithStorage{s: st}, stateManager, &bootstrap.Options{
		BlocName: "prod",
		Provider: "pve",
		Yes:      true,
	})

	err = manager.EnsureBastionDataVolume(context.Background())
	if err != nil {
		t.Fatalf("EnsureBastionDataVolume: %v", err)
	}

	if st.called("CreateVolume") {
		t.Error("a volume was created with no bastion to own it")
	}
}

// TestStepList_IncludesBastionDataVolume pins the step's position and its
// category.
//
// The position matters because the disk is attached to a bastion that must
// already exist. The category matters more: filterBastionSteps keeps required
// steps plus everything in "servers", and `ocfp bootstrap --bastion` is the
// documented rebuild flow, so a data disk in any other category would be
// silently excluded from the one flow it exists to serve. The "volumes"
// category in particular is unreachable for the bastion today.
func TestStepList_IncludesBastionDataVolume(t *testing.T) {
	t.Parallel()

	st := &dataVolStorage{}
	manager, _ := newDataVolManager(t, st)

	steps := manager.StepNamesAndCategories()

	bastionIdx, dataIdx := -1, -1
	dataCategory := ""

	for i, s := range steps {
		switch s.Name {
		case "Create Bastion":
			bastionIdx = i
		case "Ensure Bastion Data Volume":
			dataIdx = i
			dataCategory = s.Category
		}
	}

	if dataIdx < 0 {
		t.Fatalf("the bastion data volume step is missing from the step list: %v", steps)
	}

	if bastionIdx < 0 {
		t.Fatal("Create Bastion is missing from the step list")
	}

	if dataIdx != bastionIdx+1 {
		t.Errorf("data volume step is at %d, want immediately after Create Bastion at %d", dataIdx, bastionIdx)
	}

	if dataCategory != "servers" {
		t.Errorf("data volume step category = %q, want servers so --bastion keeps it", dataCategory)
	}
}

// TestFilterBastionSteps_KeepsDataVolume asserts the rebuild flow actually
// retains the step, rather than inferring it from the category alone.
func TestFilterBastionSteps_KeepsDataVolume(t *testing.T) {
	t.Parallel()

	st := &dataVolStorage{}
	manager, _ := newDataVolManager(t, st)

	kept := manager.BastionFilteredStepNames()

	for _, name := range kept {
		if name == "Ensure Bastion Data Volume" {
			return
		}
	}

	t.Errorf("`--bastion` dropped the data volume step; kept: %v", kept)
}

// TestSetupVolumesPlan_PreviewsBastionDataDisk asserts a dry run tells the
// operator the disk is coming. The volumes preview was hardcoded empty with
// the original code commented out beneath it, so a `--dry-run` reported no
// volumes at all while the run would in fact allocate one.
func TestSetupVolumesPlan_PreviewsBastionDataDisk(t *testing.T) {
	t.Parallel()

	st := &dataVolStorage{}
	manager, _ := newDataVolManager(t, st)

	vols := manager.PlannedVolumes()

	if len(vols) != 1 {
		t.Fatalf("planned volumes = %d (%v), want exactly the bastion data disk", len(vols), vols)
	}

	if vols[0].Name != "prod-bastion-data" {
		t.Errorf("planned volume name = %q, want prod-bastion-data", vols[0].Name)
	}

	if vols[0].SizeGB != 64 {
		t.Errorf("planned volume size = %d, want 64", vols[0].SizeGB)
	}

	if vols[0].Type != "local-lvm-data" {
		t.Errorf("planned volume pool = %q, want local-lvm-data", vols[0].Type)
	}
}

func TestSetupVolumesPlan_EmptyWhenDisabled(t *testing.T) {
	t.Parallel()

	st := &dataVolStorage{}
	manager, _ := newDataVolManagerWithData(t, st, config.BastionDataConfig{Enabled: false})

	if vols := manager.PlannedVolumes(); len(vols) != 0 {
		t.Errorf("planned volumes = %v, want none when the data disk is disabled", vols)
	}
}
