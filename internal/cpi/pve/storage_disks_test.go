package pve

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// newDiskTestManager returns a StorageManager pinned to node pve1 and backed
// by the given fake, so tests skip node discovery.
func newDiskTestManager(fake *fakePVEClient) *StorageManager {
	return &StorageManager{client: &Client{config: &Config{Node: "pve1"}, pveClient: fake}}
}

// legacyArtifactsConfig is the shape of an artifacts VM created before the
// data disk carried discard=on or a serial: the disk is identifiable only by
// the volid recorded in state.
func legacyArtifactsConfig() map[string]interface{} {
	return map[string]interface{}{
		"name":   "dev-artifacts",
		"digest": "3f1c0d9e",
		"scsi0":  "local-lvm-data:vm-105-disk-0,discard=on,size=32G,ssd=1",
		"scsi1":  "local-lvm-data:vm-105-data,size=500G",
		"ide2":   "local-lvm-data:vm-105-cloudinit,media=cdrom,size=4M",
		"memory": 8192,
	}
}

// TestVMDisks_ReadsSlotsAndDigest covers the read the drift check is built
// on: every attached disk with its pool and discard option, plus the config
// digest the write path sends back. The read asks for no pool type; that is
// ClassifyDisk's job, done only for the disks the caller cares about.
func TestVMDisks_ReadsSlotsAndDigest(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{getResponses: map[string]interface{}{
		"/nodes/pve1/qemu/105/config":  legacyArtifactsConfig(),
		"/nodes/pve1/qemu/105/pending": []interface{}{},
		"/storage/local-lvm-data":      map[string]interface{}{"type": "lvmthin"},
	}}

	manager := newDiskTestManager(fake)

	disks, err := manager.VMDisks(context.Background(), "105")
	if err != nil {
		t.Fatalf("VMDisks: %v", err)
	}

	// config and pending only.
	if fake.getCalls != 2 {
		t.Errorf("GET calls = %d, want 2: VMDisks must not look up pool types", fake.getCalls)
	}

	if disks.Digest != "3f1c0d9e" || disks.Node != "pve1" || disks.VMID != 105 {
		t.Errorf("header = node %q vmid %d digest %q, want pve1 105 3f1c0d9e", disks.Node, disks.VMID, disks.Digest)
	}

	if len(disks.Disks) != 2 {
		t.Fatalf("got %d disks %+v, want scsi0 and scsi1 only (the cloud-init cdrom is not a disk)", len(disks.Disks), disks.Disks)
	}

	root, data := disks.Disks[0], disks.Disks[1]

	if root.Slot != "scsi0" || root.Discard != "on" || root.PoolType != "" {
		t.Errorf("root = %+v, want scsi0 with discard on and no pool type yet", root)
	}

	if data.Slot != "scsi1" || data.VolumeID != "local-lvm-data:vm-105-data" || data.Pool != "local-lvm-data" {
		t.Errorf("data = %+v, want scsi1 local-lvm-data:vm-105-data", data)
	}

	data, err = manager.ClassifyDisk(context.Background(), data)
	if err != nil {
		t.Fatalf("ClassifyDisk: %v", err)
	}

	if data.PoolType != "lvmthin" || data.Provisioning != ProvisioningThin {
		t.Errorf("data pool type = %q (%q), want lvmthin thin", data.PoolType, data.Provisioning)
	}

	if data.DiscardOn() || data.Discard != "" {
		t.Errorf("data discard = %q, want absent", data.Discard)
	}

	if data.PendingValue != "" || data.PendingDiscardOn() {
		t.Errorf("data pending = %q, want none", data.PendingValue)
	}
}

// TestVMDisks_ReportsAQueuedDiscardChange covers how PVE reports a disk
// change queued on a running VM. GET .../config returns the config with
// pending changes already applied, so the slot there carries discard=on even
// though the running VM does not use it yet. GET .../pending lists the live
// value in "value" and the queued one in "pending". Read the merged value
// alone and a re-run after --fix reports ok before the VM was ever cold
// started.
func TestVMDisks_ReportsAQueuedDiscardChange(t *testing.T) {
	t.Parallel()

	live := "local-lvm-data:vm-105-data,size=500G"
	merged := "local-lvm-data:vm-105-data,size=500G,discard=on"

	config := legacyArtifactsConfig()
	config["scsi1"] = merged

	fake := &fakePVEClient{getResponses: map[string]interface{}{
		"/nodes/pve1/qemu/105/config": config,
		"/nodes/pve1/qemu/105/pending": []interface{}{
			map[string]interface{}{"key": "memory", "value": 8192},
			map[string]interface{}{"key": "scsi0", "value": "local-lvm-data:vm-105-disk-0,discard=on,size=32G,ssd=1"},
			map[string]interface{}{"key": "scsi1", "value": live, "pending": merged},
		},
		"/storage/local-lvm-data": map[string]interface{}{"type": "lvmthin"},
	}}

	disks, err := newDiskTestManager(fake).VMDisks(context.Background(), "105")
	if err != nil {
		t.Fatalf("VMDisks: %v", err)
	}

	data, ok := disks.Slot("scsi1")
	if !ok {
		t.Fatal("scsi1 missing from the disk list")
	}

	if data.DiscardOn() || data.Discard != "" {
		t.Errorf("live discard = %q, want absent: the running VM still uses %q", data.Discard, live)
	}

	if data.LiveValue != live {
		t.Errorf("live value = %q, want %q from the pending list", data.LiveValue, live)
	}

	if data.PendingValue != merged || !data.PendingDiscardOn() {
		t.Errorf("pending = %q, want the queued value with discard=on", data.PendingValue)
	}

	if data.Value != merged {
		t.Errorf("value = %q, want the merged config value %q, which a write builds on", data.Value, merged)
	}

	root, _ := disks.Slot("scsi0")
	if root.PendingValue != "" || !root.DiscardOn() || root.LiveValue != root.Value {
		t.Errorf("root = %+v, want discard on, no pending, and live equal to the config value", root)
	}
}

// TestVMDisks_KeepsOtherQueuedChangesInTheValue makes sure a slot with some
// other change queued keeps that change in Value, so a later discard write
// built on Value does not throw the queued change away.
func TestVMDisks_KeepsOtherQueuedChangesInTheValue(t *testing.T) {
	t.Parallel()

	live := "local-lvm-data:vm-105-data,size=500G"
	merged := "local-lvm-data:vm-105-data,backup=0,size=500G"

	config := legacyArtifactsConfig()
	config["scsi1"] = merged

	fake := &fakePVEClient{getResponses: map[string]interface{}{
		"/nodes/pve1/qemu/105/config": config,
		"/nodes/pve1/qemu/105/pending": []interface{}{
			map[string]interface{}{"key": "scsi1", "value": live, "pending": merged},
		},
		"/storage/local-lvm-data": map[string]interface{}{"type": "lvmthin"},
	}}

	disks, err := newDiskTestManager(fake).VMDisks(context.Background(), "105")
	if err != nil {
		t.Fatalf("VMDisks: %v", err)
	}

	data, _ := disks.Slot("scsi1")
	if data.Value != merged || data.LiveValue != live || data.PendingDiscardOn() || data.DiscardOn() {
		t.Errorf("data = %+v, want value %q, live %q, and no discard either way", data, merged, live)
	}
}

// TestVMDisks_ReportsAPendingDelete covers a disk whose removal is queued on
// a running VM. GET .../config already leaves the slot out, so the disk is
// known only from the pending list, which still carries its live value and a
// non-zero "delete". A delete flag of 0 is no delete, and a queued delete of a
// key that is not a disk is ignored.
func TestVMDisks_ReportsAPendingDelete(t *testing.T) {
	t.Parallel()

	live := "local-lvm-data:vm-105-data,size=500G"

	config := legacyArtifactsConfig()
	delete(config, "scsi1")

	fake := &fakePVEClient{getResponses: map[string]interface{}{
		"/nodes/pve1/qemu/105/config": config,
		"/nodes/pve1/qemu/105/pending": []interface{}{
			map[string]interface{}{"key": "scsi0", "value": "local-lvm-data:vm-105-disk-0,discard=on,size=32G,ssd=1", "delete": float64(0)},
			map[string]interface{}{"key": "scsi1", "value": live, "delete": float64(1)},
			map[string]interface{}{"key": "net1", "value": "virtio=AA:BB,bridge=vmbr1", "delete": float64(2)},
		},
		"/storage/local-lvm-data": map[string]interface{}{"type": "lvmthin"},
	}}

	disks, err := newDiskTestManager(fake).VMDisks(context.Background(), "105")
	if err != nil {
		t.Fatalf("VMDisks: %v", err)
	}

	data, ok := disks.Slot("scsi1")
	if !ok {
		t.Fatalf("scsi1 missing from %+v, want it listed with its queued delete", disks.Disks)
	}

	if !data.PendingDelete || data.LiveValue != live || data.VolumeID != "local-lvm-data:vm-105-data" {
		t.Errorf("data = %+v, want a pending delete of %q", data, live)
	}

	if found, _ := disks.DataDisk("local-lvm-data:vm-105-data", ""); found.Slot != "scsi1" {
		t.Errorf("DataDisk by volid = %q, want scsi1", found.Slot)
	}

	root, _ := disks.Slot("scsi0")
	if root.PendingDelete {
		t.Error("scsi0 reads as a pending delete with delete=0")
	}

	if len(disks.Disks) != 2 {
		t.Errorf("disks = %+v, want scsi0 and scsi1 only", disks.Disks)
	}
}

// TestClassifyDisk_ReadsEachPoolTypeOnce makes sure classifying several
// disks on the same pool asks for the pool's type once.
func TestClassifyDisk_ReadsEachPoolTypeOnce(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{getResponses: map[string]interface{}{
		"/nodes/pve1/qemu/105/config":  legacyArtifactsConfig(),
		"/nodes/pve1/qemu/105/pending": []interface{}{},
		"/storage/local-lvm-data":      map[string]interface{}{"type": "lvmthin"},
	}}

	manager := newDiskTestManager(fake)

	disks, err := manager.VMDisks(context.Background(), "105")
	if err != nil {
		t.Fatalf("VMDisks: %v", err)
	}

	for _, disk := range disks.Disks {
		if _, err := manager.ClassifyDisk(context.Background(), disk); err != nil {
			t.Fatalf("ClassifyDisk(%s): %v", disk.Slot, err)
		}
	}

	// config, pending, and one storage lookup.
	if fake.getCalls != 3 {
		t.Errorf("GET calls = %d, want 3", fake.getCalls)
	}
}

// TestClassifyDisk_ReturnsALookupFailure makes sure a pool whose type cannot
// be read leaves the disk unknown and hands the error back, so the caller
// decides whether that disk matters.
func TestClassifyDisk_ReturnsALookupFailure(t *testing.T) {
	t.Parallel()

	disk := VMDiskSlot{Slot: "scsi1", Value: "gone:vm-105-data,size=500G", VolumeID: "gone:vm-105-data", Pool: "gone"}

	got, err := newDiskTestManager(&fakePVEClient{}).ClassifyDisk(context.Background(), disk)
	if !errors.Is(err, errFakeNoCannedResponse) {
		t.Errorf("error = %v, want the lookup failure", err)
	}

	if got.Provisioning != ProvisioningUnknown || got.Slot != "scsi1" || got.Value != disk.Value {
		t.Errorf("disk = %+v, want the same disk with unknown provisioning", got)
	}
}

// TestClassifyDisk_ClassifiesFileBackedVolumesByFormat checks a qcow2 file on
// NFS reads as thin and a raw file on a dir pool as unknown.
func TestClassifyDisk_ClassifiesFileBackedVolumesByFormat(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{getResponses: map[string]interface{}{
		"/nodes/pve1/qemu/105/config": map[string]interface{}{
			"digest": "d",
			"scsi0":  "local-dir:105/vm-105-disk-0.raw,size=32G",
			"scsi1":  "nfs-a:105/vm-105-data.qcow2,size=500G",
		},
		"/nodes/pve1/qemu/105/pending": []interface{}{},
		"/storage/local-dir":           map[string]interface{}{"type": "dir"},
		"/storage/nfs-a":               map[string]interface{}{"type": "nfs"},
	}}

	manager := newDiskTestManager(fake)

	disks, err := manager.VMDisks(context.Background(), "105")
	if err != nil {
		t.Fatalf("VMDisks: %v", err)
	}

	root, _ := disks.Slot("scsi0")
	data, _ := disks.Slot("scsi1")

	root, err = manager.ClassifyDisk(context.Background(), root)
	if err != nil {
		t.Fatalf("ClassifyDisk(scsi0): %v", err)
	}

	data, err = manager.ClassifyDisk(context.Background(), data)
	if err != nil {
		t.Fatalf("ClassifyDisk(scsi1): %v", err)
	}

	if root.Provisioning != ProvisioningUnknown {
		t.Errorf("raw on dir = %q, want unknown", root.Provisioning)
	}

	if data.Provisioning != ProvisioningThin {
		t.Errorf("qcow2 on nfs = %q, want thin", data.Provisioning)
	}
}

// TestVMDisks_ReturnsReadErrors asserts each failed read surfaces as an error
// rather than as an empty disk list that would read as "nothing to fix".
func TestVMDisks_ReturnsReadErrors(t *testing.T) {
	t.Parallel()

	full := map[string]interface{}{
		"/nodes/pve1/qemu/105/config":  legacyArtifactsConfig(),
		"/nodes/pve1/qemu/105/pending": []interface{}{},
		"/storage/local-lvm-data":      map[string]interface{}{"type": "lvmthin"},
	}

	for _, missing := range []string{
		"/nodes/pve1/qemu/105/config",
		"/nodes/pve1/qemu/105/pending",
	} {
		t.Run(missing, func(t *testing.T) {
			t.Parallel()

			responses := map[string]interface{}{}

			for k, v := range full {
				if k != missing {
					responses[k] = v
				}
			}

			_, err := newDiskTestManager(&fakePVEClient{getResponses: responses}).VMDisks(context.Background(), "105")
			if err == nil {
				t.Fatalf("VMDisks with %s failing returned no error", missing)
			}

			if !errors.Is(err, errFakeNoCannedResponse) {
				t.Errorf("error = %v, want it to wrap the read failure", err)
			}
		})
	}

	if _, err := newDiskTestManager(&fakePVEClient{}).VMDisks(context.Background(), "not-a-vmid"); !errors.Is(err, ErrInvalidVMID) {
		t.Errorf("VMDisks(not-a-vmid) error = %v, want ErrInvalidVMID", err)
	}
}

// TestVMDisks_RejectsAnUnrecognisedPendingShape guards against reading a
// changed API shape as "nothing pending".
func TestVMDisks_RejectsAnUnrecognisedPendingShape(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{getResponses: map[string]interface{}{
		"/nodes/pve1/qemu/105/config":  legacyArtifactsConfig(),
		"/nodes/pve1/qemu/105/pending": map[string]interface{}{"scsi1": "x"},
		"/storage/local-lvm-data":      map[string]interface{}{"type": "lvmthin"},
	}}

	_, err := newDiskTestManager(fake).VMDisks(context.Background(), "105")
	if !errors.Is(err, ErrUnexpectedResponseType) {
		t.Errorf("error = %v, want ErrUnexpectedResponseType", err)
	}
}

// TestVMDisks_DataDiskByVolumeThenSerial covers how a managed data disk is
// identified: by the volid recorded in state first, because legacy disks have
// no serial, and by serial when the volid is unknown or stale after a
// reassign renamed the volume.
func TestVMDisks_DataDiskByVolumeThenSerial(t *testing.T) {
	t.Parallel()

	disks := &VMDisks{Disks: []VMDiskSlot{
		{Slot: "scsi0", Value: "local-lvm-data:vm-102-disk-0,discard=on,size=32G"},
		{Slot: "scsi1", Value: "local-lvm-data:vm-102-disk-1,discard=on,serial=ocfpdata,size=64G"},
		{Slot: "scsi2", Value: "local-lvm-data:vm-102-data2,size=8G"},
	}}

	tests := []struct {
		name     string
		volumeID string
		serial   string
		wantSlot string
		wantOK   bool
	}{
		{"recorded volid", "local-lvm-data:vm-102-data2", "ocfpdata", "scsi2", true},
		{"stale volid falls back to serial", "local-lvm-data:vm-100-data", "ocfpdata", "scsi1", true},
		{"no volid uses serial", "", "ocfpdata", "scsi1", true},
		{"volid prefix is not a match", "local-lvm-data:vm-102-data", "ocfpartifacts", "", false},
		{"neither", "", "", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := disks.DataDisk(tc.volumeID, tc.serial)
			if ok != tc.wantOK || got.Slot != tc.wantSlot {
				t.Errorf("DataDisk(%q, %q) = %q, %v, want %q, %v", tc.volumeID, tc.serial, got.Slot, ok, tc.wantSlot, tc.wantOK)
			}
		})
	}
}

// TestVMDisks_SkipsSlotsThatAreNotStorageVolumes keeps cdroms, passthrough
// devices, and unused slots out of the list.
func TestVMDisks_SkipsSlotsThatAreNotStorageVolumes(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{getResponses: map[string]interface{}{
		"/nodes/pve1/qemu/105/config": map[string]interface{}{
			"digest":  "d",
			"scsi0":   "local-lvm-data:vm-105-disk-0,size=32G",
			"ide0":    "none,media=cdrom",
			"sata0":   "/dev/disk/by-id/ata-X,size=1T",
			"unused0": "local-lvm-data:vm-105-orphan",
		},
		"/nodes/pve1/qemu/105/pending": []interface{}{},
		"/storage/local-lvm-data":      map[string]interface{}{"type": "lvmthin"},
	}}

	disks, err := newDiskTestManager(fake).VMDisks(context.Background(), "105")
	if err != nil {
		t.Fatalf("VMDisks: %v", err)
	}

	slots := make([]string, 0, len(disks.Disks))
	for _, d := range disks.Disks {
		slots = append(slots, d.Slot)
	}

	if strings.Join(slots, ",") != "scsi0" {
		t.Errorf("slots = %v, want [scsi0]", slots)
	}
}

// TestEnableDiskDiscard_PutsTheFullCorrectedValueWithTheDigest pins the one
// write the fix makes: a single PUT to the VM's config carrying the whole
// corrected slot value, every other option intact, plus the digest the value
// was read under so PVE refuses the write if the config changed in between.
// Nothing else is touched, and in particular the VM is never stopped,
// started, or rebooted.
func TestEnableDiskDiscard_PutsTheFullCorrectedValueWithTheDigest(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{}

	got, err := newDiskTestManager(fake).EnableDiskDiscard(context.Background(),
		"105", "scsi1", "local-lvm-data:vm-105-data,backup=0,size=500G", "3f1c0d9e")
	if err != nil {
		t.Fatalf("EnableDiskDiscard: %v", err)
	}

	want := "local-lvm-data:vm-105-data,backup=0,size=500G,discard=on"
	if got != want {
		t.Errorf("returned value = %q, want %q", got, want)
	}

	if fake.putCalls != 1 || len(fake.putParams) != 1 {
		t.Fatalf("PUT calls = %d, want exactly 1", fake.putCalls)
	}

	put := fake.putParams[0]
	if put.Path != "/nodes/pve1/qemu/105/config" {
		t.Errorf("PUT path = %q, want /nodes/pve1/qemu/105/config", put.Path)
	}

	if len(put.Params) != 2 || put.Params["scsi1"] != want || put.Params["digest"] != "3f1c0d9e" {
		t.Errorf("PUT body = %v, want exactly scsi1=%q and digest=3f1c0d9e", put.Params, want)
	}

	if fake.getCalls != 0 || fake.postCalls != 0 || fake.deleteCalls != 0 {
		t.Errorf("GET/POST/DELETE calls = %d/%d/%d, want none: no status, stop, start, or reboot",
			fake.getCalls, fake.postCalls, fake.deleteCalls)
	}
}

// TestEnableDiskDiscard_SkipsAValueThatIsAlreadyCorrect asserts a disk that
// already carries discard=on is not rewritten.
func TestEnableDiskDiscard_SkipsAValueThatIsAlreadyCorrect(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{}

	current := "local-lvm-data:vm-105-data,discard=on,size=500G"

	got, err := newDiskTestManager(fake).EnableDiskDiscard(context.Background(), "105", "scsi1", current, "d")
	if err != nil {
		t.Fatalf("EnableDiskDiscard: %v", err)
	}

	if got != current || fake.putCalls != 0 {
		t.Errorf("value %q with %d PUTs, want the value unchanged and no PUT", got, fake.putCalls)
	}
}

// TestEnableDiskDiscard_RefusesUnsafeWrites covers the inputs that must fail
// before any PUT: no digest to guard the write, a slot that is not an
// attached disk, a malformed value, and a bad VMID.
func TestEnableDiskDiscard_RefusesUnsafeWrites(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		vmid    string
		slot    string
		value   string
		digest  string
		wantErr error
	}{
		{"no digest", "105", "scsi1", "local-lvm-data:vm-105-data,size=500G", "", ErrConfigDigestMissing},
		{"unused slot", "105", "unused0", "local-lvm-data:vm-105-data", "d", ErrInvalidDiskSlotValue},
		{"non-disk key", "105", "net0", "virtio=AA:BB,bridge=vmbr0", "d", ErrInvalidDiskSlotValue},
		{"malformed value", "105", "scsi1", "local-lvm-data:vm-105-data,,size=500G", "d", ErrInvalidDiskSlotValue},
		{"bad vmid", "abc", "scsi1", "local-lvm-data:vm-105-data", "d", ErrInvalidVMID},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakePVEClient{}

			_, err := newDiskTestManager(fake).EnableDiskDiscard(context.Background(), tc.vmid, tc.slot, tc.value, tc.digest)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want %v", err, tc.wantErr)
			}

			if fake.putCalls != 0 {
				t.Errorf("PUT calls = %d, want none", fake.putCalls)
			}
		})
	}
}

// TestEnableDiskDiscard_ReturnsAFailedPut makes sure a refused write, such as
// a digest mismatch, reaches the caller.
func TestEnableDiskDiscard_ReturnsAFailedPut(t *testing.T) {
	t.Parallel()

	errDigest := errors.New("config file has been modified by another user")

	fake := &fakePVEClient{putErr: func(string, map[string]interface{}) error { return errDigest }}

	_, err := newDiskTestManager(fake).EnableDiskDiscard(context.Background(),
		"105", "scsi1", "local-lvm-data:vm-105-data,size=500G", "stale")
	if !errors.Is(err, errDigest) {
		t.Errorf("error = %v, want the PUT failure", err)
	}
}
