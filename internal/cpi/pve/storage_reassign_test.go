package pve

import "testing"

// TestFindDiskSlotForVolume covers the config scan that both DetachVolume and
// ReassignVolume depend on to turn a volume id into the slot that holds it.
//
// The scan must know about every bus PVE can put a disk on, plus the unusedN
// slots. An already-half-detached disk lives in unusedN, and a scan that skips
// those reports the volume as not found, which reads like the disk is gone
// when it is sitting right there in the config.
func TestFindDiskSlotForVolume(t *testing.T) {
	t.Parallel()

	cfg := map[string]interface{}{
		"name":    "ocfp-lab-bastion",
		"scsi0":   "local-lvm-data:vm-100-disk-0,discard=on,size=54784M,ssd=1",
		"scsi1":   "local-lvm-data:vm-100-data,discard=on,serial=ocfpdata,size=64G",
		"virtio3": "local-lvm-data:vm-100-virt,size=8G",
		"sata2":   "local-lvm-data:vm-100-sata,size=8G",
		"ide2":    "local-lvm-data:vm-100-cloudinit,media=cdrom,size=4M",
		"unused0": "local-lvm-data:vm-100-orphan",
		"memory":  8192,
	}

	tests := []struct {
		name     string
		volumeID string
		wantSlot string
		wantOK   bool
	}{
		{"scsi data disk", "local-lvm-data:vm-100-data", "scsi1", true},
		{"scsi boot disk", "local-lvm-data:vm-100-disk-0", "scsi0", true},
		{"virtio slot", "local-lvm-data:vm-100-virt", "virtio3", true},
		{"sata slot", "local-lvm-data:vm-100-sata", "sata2", true},
		{"ide slot", "local-lvm-data:vm-100-cloudinit", "ide2", true},
		{"unused slot", "local-lvm-data:vm-100-orphan", "unused0", true},
		{"absent volume", "local-lvm-data:vm-100-nope", "", false},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			slot, ok := findDiskSlotForVolume(cfg, tc.volumeID)

			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}

			if slot != tc.wantSlot {
				t.Errorf("slot = %q, want %q", slot, tc.wantSlot)
			}
		})
	}
}

// TestFindDiskSlotForVolume_DoesNotMatchSubstrings guards against a volume id
// that is a prefix of another matching the wrong slot. PVE volume names such
// as vm-100-data and vm-100-data2 can coexist on one pool.
func TestFindDiskSlotForVolume_DoesNotMatchSubstrings(t *testing.T) {
	t.Parallel()

	cfg := map[string]interface{}{
		"scsi1": "local-lvm-data:vm-100-data2,size=64G",
	}

	slot, ok := findDiskSlotForVolume(cfg, "local-lvm-data:vm-100-data")
	if ok {
		t.Errorf("matched slot %q for a volume id that is only a prefix of the configured one", slot)
	}
}

// TestBuildReassignParams pins the body of the move_disk call.
//
// The shape was confirmed against a live PVE 9 cluster on 2026-09-14 using two
// throwaway VMs: the source VM must be stopped ("Cannot move disk to another
// VM while the source VM is running - detach first"), the target slot must be
// free ("Target disk key 'scsi0' is already in use"), and on success PVE
// renames the volume to match its new owner and preserves the disk's
// properties.
func TestBuildReassignParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		slot         string
		targetVMID   int
		targetSlot   string
		wantTargetIn bool
	}{
		{
			name:         "explicit target slot is sent",
			slot:         "scsi1",
			targetVMID:   137,
			targetSlot:   "scsi1",
			wantTargetIn: true,
		},
		{
			name:         "omitted target slot lets PVE choose",
			slot:         "scsi1",
			targetVMID:   137,
			targetSlot:   "",
			wantTargetIn: false,
		},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			params := buildReassignParams(tc.slot, tc.targetVMID, tc.targetSlot)

			if got := params["disk"]; got != tc.slot {
				t.Errorf("disk = %v, want %q", got, tc.slot)
			}

			if got := params["target-vmid"]; got != tc.targetVMID {
				t.Errorf("target-vmid = %v, want %d", got, tc.targetVMID)
			}

			gotTarget, present := params["target-disk"]

			if present != tc.wantTargetIn {
				t.Fatalf("target-disk present = %v, want %v (params %v)", present, tc.wantTargetIn, params)
			}

			if tc.wantTargetIn && gotTarget != tc.targetSlot {
				t.Errorf("target-disk = %v, want %q", gotTarget, tc.targetSlot)
			}
		})
	}
}

// TestReassignPath pins the endpoint. The call is issued against the SOURCE
// VM, not the target, which is easy to get backwards.
func TestReassignPath(t *testing.T) {
	t.Parallel()

	got := reassignPath("lab-wayneeseguin-0", 100)
	want := "/nodes/lab-wayneeseguin-0/qemu/100/move_disk"

	if got != want {
		t.Errorf("reassignPath = %q, want %q", got, want)
	}
}

// TestUpidFromResponse covers the shapes a raw PVE POST can hand back. A nil
// response means the work was synchronous and there is no task to await, which
// must not be treated as an error.
func TestUpidFromResponse(t *testing.T) {
	t.Parallel()

	const upid = "UPID:lab-0:001234:00ABCD:6646FFFF:qmmove:100:root@pam:"

	tests := []struct {
		name    string
		resp    interface{}
		want    string
		wantErr bool
	}{
		{name: "nil means no task to await", resp: nil, want: ""},
		{name: "bare string", resp: upid, want: upid},
		{name: "object with upid key", resp: map[string]interface{}{"upid": upid}, want: upid},
		{name: "object with uppercase key", resp: map[string]interface{}{"UPID": upid}, want: upid},
		{name: "object with data key", resp: map[string]interface{}{"data": upid}, want: upid},
		{name: "object with no task id", resp: map[string]interface{}{"other": 1}, wantErr: true},
		{name: "unrecognized scalar", resp: 12345, wantErr: true},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := upidFromResponse(tc.resp)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %v, got upid %q", tc.resp, got)
				}

				return
			}

			if err != nil {
				t.Fatalf("upidFromResponse(%v): %v", tc.resp, err)
			}

			if got != tc.want {
				t.Errorf("upid = %q, want %q", got, tc.want)
			}
		})
	}
}
