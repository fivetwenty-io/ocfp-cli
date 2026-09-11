package pve

import (
	"context"
	"testing"
)

// A template must never reach a caller that is deciding what to delete.
// ListInstances is the only inventory the teardown nuke path consults, so the
// exclusion here is what keeps a golden image out of the deletion plan.
func TestListInstances_LeavesTemplatesOut(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{
		getResponses: map[string]interface{}{
			"/nodes": []interface{}{
				map[string]interface{}{"node": "pvenode1", "status": "online"},
			},
			"/nodes/pvenode1/qemu": []interface{}{
				map[string]interface{}{"vmid": 20000.0, "name": "ocfp-cf1-lab-bastion", "status": "running"},
				map[string]interface{}{"vmid": 9000.0, "name": "ocfp-cf1-lab-template", "status": "stopped", "template": 1.0},
			},
		},
	}

	mgr := &ComputeManager{client: &Client{config: &Config{}, pveClient: fake}}

	instances, err := mgr.ListInstances(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}

	if len(instances) != 1 {
		t.Fatalf("expected exactly one non-template guest, got %d", len(instances))
	}

	if instances[0].Name != "ocfp-cf1-lab-bastion" {
		t.Errorf("expected the bastion, got %q", instances[0].Name)
	}

	for _, instance := range instances {
		if instance.Name == "ocfp-cf1-lab-template" {
			t.Error("a template was listed as a deletable guest")
		}
	}
}

// Volume attribution rests on the owner VMID PVE reports on each content
// entry. Without it, a caller has only the volume's name to go on.
func TestListVolumes_CarriesOwnerVMID(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{
		getResponses: map[string]interface{}{
			"/nodes/pvenode3/storage/vmdata/content": []interface{}{
				map[string]interface{}{"volid": "vmdata:vm-20000-disk-0", "content": "images", "vmid": 20000.0, "size": 0.0},
				map[string]interface{}{"volid": "vmdata:orphan-disk", "content": "images", "size": 0.0},
				map[string]interface{}{"volid": "vmdata:debian.iso", "content": "iso", "size": 0.0},
			},
		},
	}

	mgr := &StorageManager{client: &Client{config: &Config{DefaultStorage: "unused-default"}, pveClient: fake}}

	volumes, err := mgr.ListVolumes(context.Background(), map[string]string{"node": "pvenode3", "storage": "vmdata"})
	if err != nil {
		t.Fatalf("ListVolumes: %v", err)
	}

	if len(volumes) != 2 {
		t.Fatalf("expected the two image volumes and not the ISO, got %d", len(volumes))
	}

	byID := make(map[string]string, len(volumes))
	for _, volume := range volumes {
		byID[volume.ID] = volume.AttachedTo
	}

	if byID["vmdata:vm-20000-disk-0"] != "20000" {
		t.Errorf("expected owner 20000 on the bastion disk, got %q", byID["vmdata:vm-20000-disk-0"])
	}

	if byID["vmdata:orphan-disk"] != "" {
		t.Errorf("expected no owner on the unattributed volume, got %q", byID["vmdata:orphan-disk"])
	}
}

// The node filter has to win over the configured default node, or a nuke would
// read one node's storage while deleting another node's guests.
func TestListVolumes_NodeFilterWins(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{
		getResponses: map[string]interface{}{
			"/nodes/pvenode2/storage/vmdata/content": []interface{}{
				map[string]interface{}{"volid": "vmdata:vm-20001-disk-0", "content": "images", "vmid": 20001.0, "size": 0.0},
			},
		},
	}

	mgr := &StorageManager{client: &Client{config: &Config{Node: "pvenode1"}, pveClient: fake}}

	volumes, err := mgr.ListVolumes(context.Background(), map[string]string{"node": "pvenode2", "storage": "vmdata"})
	if err != nil {
		t.Fatalf("ListVolumes: %v", err)
	}

	if len(volumes) != 1 || volumes[0].ID != "vmdata:vm-20001-disk-0" {
		t.Fatalf("expected the volume from pvenode2, got %+v", volumes)
	}
}

// A guest owns the disks on its bus slots, its cloud-init drive, and anything
// parked in an unused slot. Teardown needs all three to clean up after itself.
func TestParseVMVolumeIDs(t *testing.T) {
	t.Parallel()

	config := map[string]interface{}{
		"scsi0":   "vmdata:vm-20000-disk-0,size=64G,iothread=1",
		"ide2":    "vmdata:vm-20000-cloudinit,media=cdrom",
		"unused0": "slowdata:vm-20000-disk-1",
		"sata0":   "none",
		"name":    "ocfp-cf1-lab-bastion",
		"cores":   4.0,
	}

	got := parseVMVolumeIDs(config)

	want := []string{"slowdata:vm-20000-disk-1", "vmdata:vm-20000-cloudinit", "vmdata:vm-20000-disk-0"}
	if len(got) != len(want) {
		t.Fatalf("expected %d volids, got %d: %v", len(want), len(got), got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("volid %d = %q, want %q", i, got[i], want[i])
		}
	}
}
