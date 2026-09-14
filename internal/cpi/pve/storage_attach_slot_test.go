package pve

import "testing"

// TestParseAttachDevice covers the three shapes the cpi StorageManager's
// `device` argument can take on PVE.
//
// A Linux device path such as "/dev/sdb" is a legacy hint that names what the
// guest should see rather than what PVE should configure, so it pins no slot
// and PVE picks the next free index on the bus. An empty string behaves the
// same way. A PVE-native slot spec such as "scsi1" pins the slot exactly, and
// may carry disk properties after a comma — "scsi1,discard=on,serial=ocfpdata"
// — which ride along on the config value so the guest gets a stable
// /dev/disk/by-id path instead of an enumeration-order-dependent /dev/sdN.
func TestParseAttachDevice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		device    string
		wantBus   string
		wantSlot  string
		wantProps string
	}{
		{
			name:    "empty hint pins nothing",
			device:  "",
			wantBus: "scsi",
		},
		{
			name:    "linux device path pins nothing",
			device:  "/dev/sdb",
			wantBus: "scsi",
		},
		{
			name:    "virtio device path selects the virtio bus",
			device:  "virtio1",
			wantBus: "virtio", wantSlot: "virtio1",
		},
		{
			name:    "scsi slot pins the slot",
			device:  "scsi1",
			wantBus: "scsi", wantSlot: "scsi1",
		},
		{
			name:      "scsi slot with properties",
			device:    "scsi1,discard=on,serial=ocfpdata",
			wantBus:   "scsi",
			wantSlot:  "scsi1",
			wantProps: "discard=on,serial=ocfpdata",
		},
		{
			name:     "sata slot pins the slot",
			device:   "sata2",
			wantBus:  "sata",
			wantSlot: "sata2",
		},
		{
			name:    "bus name without an index is not a slot",
			device:  "scsi",
			wantBus: "scsi",
		},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bus, slot, props := parseAttachDevice(tc.device)

			if bus != tc.wantBus {
				t.Errorf("bus = %q, want %q", bus, tc.wantBus)
			}

			if slot != tc.wantSlot {
				t.Errorf("slot = %q, want %q", slot, tc.wantSlot)
			}

			if props != tc.wantProps {
				t.Errorf("props = %q, want %q", props, tc.wantProps)
			}
		})
	}
}

// TestAttachValueForVolume pins how the properties reach PVE. They cannot be
// sent as separate config parameters, because the client library's Extra map
// writes each key as its own top-level VM config key; PVE expects them
// appended to the disk's own value.
func TestAttachValueForVolume(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		volID string
		props string
		want  string
	}{
		{
			name:  "no properties leaves the volid bare",
			volID: "local-lvm-data:vm-100-data",
			want:  "local-lvm-data:vm-100-data",
		},
		{
			name:  "properties are appended after a comma",
			volID: "local-lvm-data:vm-100-data",
			props: "discard=on,serial=ocfpdata",
			want:  "local-lvm-data:vm-100-data,discard=on,serial=ocfpdata",
		},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := attachValueForVolume(tc.volID, tc.props)
			if got != tc.want {
				t.Errorf("attachValueForVolume(%q, %q) = %q, want %q", tc.volID, tc.props, got, tc.want)
			}
		})
	}
}
