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

// TestVMDisks_ReadsSlotsPoolTypesAndDigest covers the read the drift check
// is built on: every attached disk with its pool, the pool's plugin type, its
// provisioning class, and the discard option, plus the config digest the
// write path sends back.
func TestVMDisks_ReadsSlotsPoolTypesAndDigest(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{getResponses: map[string]interface{}{
		"/nodes/pve1/qemu/105/config":  legacyArtifactsConfig(),
		"/nodes/pve1/qemu/105/pending": []interface{}{},
		"/storage/local-lvm-data":      map[string]interface{}{"type": "lvmthin"},
	}}

	disks, err := newDiskTestManager(fake).VMDisks(context.Background(), "105")
	if err != nil {
		t.Fatalf("VMDisks: %v", err)
	}

	if disks.Digest != "3f1c0d9e" || disks.Node != "pve1" || disks.VMID != 105 {
		t.Errorf("header = node %q vmid %d digest %q, want pve1 105 3f1c0d9e", disks.Node, disks.VMID, disks.Digest)
	}

	if len(disks.Disks) != 2 {
		t.Fatalf("got %d disks %+v, want scsi0 and scsi1 only (the cloud-init cdrom is not a disk)", len(disks.Disks), disks.Disks)
	}

	root, data := disks.Disks[0], disks.Disks[1]

	if root.Slot != "scsi0" || root.Discard != "on" || root.Provisioning != ProvisioningThin {
		t.Errorf("root = %+v, want scsi0 thin with discard on", root)
	}

	if data.Slot != "scsi1" || data.VolumeID != "local-lvm-data:vm-105-data" || data.Pool != "local-lvm-data" {
		t.Errorf("data = %+v, want scsi1 local-lvm-data:vm-105-data", data)
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

// TestVMDisks_ReportsAQueuedDiscardChange covers the case the config read
// alone gets wrong. On a running VM a disk change is queued, GET .../config
// still shows the old value, and only GET .../pending carries the new one in
// its "pending" field. Without this a re-run after --fix would look like the
// fix never happened.
func TestVMDisks_ReportsAQueuedDiscardChange(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{getResponses: map[string]interface{}{
		"/nodes/pve1/qemu/105/config": legacyArtifactsConfig(),
		"/nodes/pve1/qemu/105/pending": []interface{}{
			map[string]interface{}{"key": "memory", "value": 8192},
			map[string]interface{}{"key": "scsi0", "value": "local-lvm-data:vm-105-disk-0,discard=on,size=32G,ssd=1"},
			map[string]interface{}{
				"key":     "scsi1",
				"value":   "local-lvm-data:vm-105-data,size=500G",
				"pending": "local-lvm-data:vm-105-data,size=500G,discard=on",
			},
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

	if data.DiscardOn() {
		t.Error("current value reads as discard=on, want the config value, which lacks it")
	}

	if data.PendingValue != "local-lvm-data:vm-105-data,size=500G,discard=on" || !data.PendingDiscardOn() {
		t.Errorf("pending = %q, want the queued value with discard=on", data.PendingValue)
	}

	root, _ := disks.Slot("scsi0")
	if root.PendingValue != "" {
		t.Errorf("root pending = %q, want none: an entry with no pending field is not a queued change", root.PendingValue)
	}
}

// TestVMDisks_ReadsEachPoolTypeOnce makes sure a VM with several disks on the
// same pool asks for the pool's type once.
func TestVMDisks_ReadsEachPoolTypeOnce(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{getResponses: map[string]interface{}{
		"/nodes/pve1/qemu/105/config":  legacyArtifactsConfig(),
		"/nodes/pve1/qemu/105/pending": []interface{}{},
		"/storage/local-lvm-data":      map[string]interface{}{"type": "lvmthin"},
	}}

	if _, err := newDiskTestManager(fake).VMDisks(context.Background(), "105"); err != nil {
		t.Fatalf("VMDisks: %v", err)
	}

	// config, pending, and one storage lookup.
	if fake.getCalls != 3 {
		t.Errorf("GET calls = %d, want 3", fake.getCalls)
	}
}

// TestVMDisks_ClassifiesFileBackedVolumesByFormat checks a qcow2 file on NFS
// reads as thin and a raw file on a dir pool as unknown.
func TestVMDisks_ClassifiesFileBackedVolumesByFormat(t *testing.T) {
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

	disks, err := newDiskTestManager(fake).VMDisks(context.Background(), "105")
	if err != nil {
		t.Fatalf("VMDisks: %v", err)
	}

	root, _ := disks.Slot("scsi0")
	data, _ := disks.Slot("scsi1")

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
		"/storage/local-lvm-data",
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
